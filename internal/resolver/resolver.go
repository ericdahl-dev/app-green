// Package resolver is the only package that orchestrates I/O: it polls the
// Jira, GitHub and AWS adapters, runs the pure core (link, rules) and emits
// Snapshots for the UI.
package resolver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ericdahl-dev/app-green/internal/aws"
	"github.com/ericdahl-dev/app-green/internal/config"
	"github.com/ericdahl-dev/app-green/internal/github"
	"github.com/ericdahl-dev/app-green/internal/jira"
	"github.com/ericdahl-dev/app-green/internal/link"
	"github.com/ericdahl-dev/app-green/internal/model"
	"github.com/ericdahl-dev/app-green/internal/rules"
)

// Tracker is the part of jira.Client the resolver uses.
type Tracker interface {
	MyTickets(ctx context.Context, projects []string) ([]model.Ticket, []string, error)
	TicketsByKey(ctx context.Context, keys []string) ([]model.Ticket, []string, error)
}

// CodeHost is the part of github.Client the resolver uses.
type CodeHost interface {
	RecentPRs(ctx context.Context, owner, name, author string, since time.Time) ([]model.PR, []string, error)
	BasePRs(ctx context.Context, owner, name string, prs []model.PR) ([]model.PR, []string, error)
	Compare(ctx context.Context, repo, base, head string) (model.Inclusion, error)
}

// Deployer is the part of aws.Client (one account) the resolver uses.
type Deployer interface {
	Sources(ctx context.Context, pipeline string) (map[string]string, error)
	History(ctx context.Context, pipeline string, sources map[string]string, specs ...aws.StageSpec) (map[string][]model.Deploy, []string, error)
	Health(ctx context.Context, services []aws.Service) (model.Health, []string, error)
}

// Adapters are the resolver's sources. AWS is keyed by config account name.
type Adapters struct {
	Tracker Tracker
	Code    CodeHost
	AWS     map[string]Deployer
}

// AdapterStatus is how one source fared, for the header.
type AdapterStatus struct {
	Name      string    // "jira", "github", "aws <account>"
	OK        bool      // the last attempt succeeded
	At        time.Time // last success; zero if none yet
	Err       string    // the last attempt's error, short
	SSO       bool      // the error is an expired AWS SSO session
	Throttled bool      // rate limited: the source is skipped until RetryAt
	RetryAt   time.Time
}

// Snapshot is one poll's result, for the UI.
type Snapshot struct {
	Chains   []model.Chain // evaluated and sorted
	Unlinked []model.PR
	Status   []AdapterStatus // jira, github, then AWS accounts in config order
	// Warnings are problems that left data usable but maybe incomplete
	// (decode errors, truncation, a stage spec that matched nothing, failed
	// compares), deduplicated, in first-seen order. They cover the data
	// shown, so a source that failed this poll keeps its last warnings.
	Warnings []string
	At       time.Time
}

// prSince is how far back RecentPRs looks.
const prSince = 30 * 24 * time.Hour

// AWSThrottleBackoff is how long an AWS account is skipped after AWS
// throttles it; AWS errors carry no Retry-After.
const AWSThrottleBackoff = time.Minute

// maxErr caps AdapterStatus.Err, in runes.
const maxErr = 160

// repoData is the last good GitHub result for one repo.
type repoData struct {
	prs     []model.PR // mine
	context []model.PR // base-branch PRs of mine, any author
	warn    []string
}

// pipelineData is the last good AWS result for one pipeline.
type pipelineData struct {
	deploys map[string][]model.Deploy // by stage
	warn    []string
}

// healthData is the last good health of one Env.
type healthData struct {
	health model.Health
	warn   []string
}

// Resolver polls the adapters and builds Snapshots. Poll and Run are safe
// to call from several goroutines; polls never overlap.
type Resolver struct {
	cfg *config.Config
	ad  Adapters
	now func() time.Time
	log *slog.Logger

	interval time.Duration

	pollMu sync.Mutex // held for a whole poll; guards everything below
	cache  *compareCache
	status map[string]*AdapterStatus
	order  []string // status names in display order

	tickets    []model.Ticket
	ticketWarn []string
	extra      []model.Ticket // named by my open PRs, not in tickets
	extraWarn  []string
	repos      map[string]repoData
	sources    map[config.PipelineKey]map[string]string // session cache
	pipelines  map[config.PipelineKey]pipelineData
	health     map[string]healthData // by Env.ID()
}

// New returns a Resolver over ad. now is the clock (nil is time.Now); log
// receives each poll's warnings and source errors (nil discards them).
func New(cfg *config.Config, ad Adapters, now func() time.Time, log *slog.Logger) *Resolver {
	if now == nil {
		now = time.Now
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	r := &Resolver{
		cfg: cfg, ad: ad, now: now, log: log,
		interval:  cfg.PollInterval(),
		cache:     newCompareCache(),
		status:    map[string]*AdapterStatus{},
		repos:     map[string]repoData{},
		sources:   map[config.PipelineKey]map[string]string{},
		pipelines: map[config.PipelineKey]pipelineData{},
		health:    map[string]healthData{},
	}
	r.order = append(r.order, "jira", "github")
	for _, a := range r.accounts() {
		r.order = append(r.order, "aws "+a)
	}
	for _, n := range r.order {
		r.status[n] = &AdapterStatus{Name: n}
	}
	return r
}

// Run polls now, then every poll interval, and on each receive from
// refresh, sending each Snapshot to out, until ctx is canceled. Polls run
// one at a time: a refresh that arrives during a poll starts the next one
// as soon as it ends, and the interval restarts after every poll. Send on
// refresh without blocking (a buffer of 1 coalesces repeated presses).
func (r *Resolver) Run(ctx context.Context, out chan<- Snapshot, refresh <-chan struct{}) {
	timer := time.NewTimer(r.interval)
	defer timer.Stop()
	for {
		snap := r.Poll(ctx)
		if ctx.Err() != nil {
			return
		}
		select {
		case out <- snap:
		case <-ctx.Done():
			return
		}
		timer.Reset(r.interval)
		select {
		case <-timer.C:
		case <-refresh:
		case <-ctx.Done():
			return
		}
	}
}

// accounts are the config accounts that have envs, in config order.
func (r *Resolver) accounts() []string {
	var out []string
	for _, a := range r.cfg.AWS.Accounts {
		if slices.ContainsFunc(r.cfg.EnvList, func(e config.EnvConfig) bool { return e.Account == a.Name }) {
			out = append(out, a.Name)
		}
	}
	return out
}

// jiraResult, githubResult and accountResult are one poll's fetches,
// merged into the Resolver after every fetch is done.
type jiraResult struct {
	tickets []model.Ticket
	warn    []string
	err     error
}

type githubResult struct {
	repos map[string]repoData // repos fetched without error
	err   error               // the first error, naming its repo
}

type accountResult struct {
	sources   map[config.PipelineKey]map[string]string // newly discovered
	pipelines map[config.PipelineKey]pipelineData
	health    map[string]healthData
	err       error
}

// Poll fetches every source once, concurrently, and returns the resulting
// Snapshot. A source that fails keeps its previous result.
func (r *Resolver) Poll(ctx context.Context) Snapshot {
	r.pollMu.Lock()
	defer r.pollMu.Unlock()
	now := r.now()
	accts := r.accounts()

	var (
		wg    sync.WaitGroup
		jr    jiraResult
		gr    githubResult
		ars   = make([]accountResult, len(accts))
		known = maps.Clone(r.sources) // read by the AWS goroutines
	)
	run := func(name string, fetch func()) bool {
		if r.backingOff(name, now) {
			return false
		}
		wg.Add(1)
		go func() { defer wg.Done(); fetch() }()
		return true
	}
	jiraRan := run("jira", func() { jr = r.fetchJira(ctx) })
	githubRan := run("github", func() { gr = r.fetchGitHub(ctx, now) })
	acctRan := make([]bool, len(accts))
	for i, a := range accts {
		acctRan[i] = run("aws "+a, func() { ars[i] = r.fetchAccount(ctx, a, known) })
	}
	wg.Wait()
	if ctx.Err() != nil {
		// Shutting down: what failed failed because of ctx, so record
		// nothing and ask nothing more.
		return r.build(ctx, now)
	}

	if githubRan {
		for repo, d := range gr.repos {
			r.repos[repo] = d
		}
		r.record("github", gr.err, now)
	}
	if jiraRan {
		err := jr.err
		if err == nil {
			r.tickets, r.ticketWarn = jr.tickets, jr.warn
		}
		// Extra tickets need GitHub's PRs, so they come after the fetches.
		// A rate-limited Jira is not asked again this poll.
		if retryAfter(err) == 0 {
			if xerr := r.fetchExtraTickets(ctx); err == nil {
				err = xerr
			}
		}
		r.record("jira", err, now)
	}
	for i, a := range accts {
		if !acctRan[i] {
			continue
		}
		ar := ars[i]
		for k, v := range ar.sources {
			r.sources[k] = v
		}
		for k, v := range ar.pipelines {
			r.pipelines[k] = v
		}
		for k, v := range ar.health {
			r.health[k] = v
		}
		r.record("aws "+a, ar.err, now)
	}
	snap := r.build(ctx, now)
	for _, w := range snap.Warnings {
		r.log.Warn("poll warning", "msg", w)
	}
	return snap
}

func (r *Resolver) fetchJira(ctx context.Context) jiraResult {
	t, w, err := r.ad.Tracker.MyTickets(ctx, r.cfg.Jira.Projects)
	return jiraResult{tickets: t, warn: w, err: err}
}

// fetchExtraTickets looks up the tickets my open PRs name that MyTickets
// did not return (assigned to someone else, say), so those PRs still get a
// row. On error the previous extras stay.
func (r *Resolver) fetchExtraTickets(ctx context.Context) error {
	have := map[string]bool{}
	for _, t := range r.tickets {
		have[t.Key] = true
	}
	var keys []string
	for _, d := range r.repos {
		for _, p := range d.prs {
			if p.State != model.PROpen {
				continue
			}
			for _, k := range link.KeysIn(r.cfg.Jira.Projects, p.Title, p.HeadRef) {
				if !have[k] && !slices.Contains(keys, k) {
					keys = append(keys, k)
				}
			}
		}
	}
	if len(keys) == 0 {
		r.extra, r.extraWarn = nil, nil
		return nil
	}
	slices.Sort(keys)
	t, w, err := r.ad.Tracker.TicketsByKey(ctx, keys)
	if err != nil {
		return err
	}
	r.extra, r.extraWarn = t, w
	return nil
}

// fetchGitHub fetches my recent PRs and their base-branch PRs per watched
// repo. A repo that fails keeps its previous data and the rest go on, except
// that a rate limit or a bad token stops the loop: every later call would
// fail the same way.
func (r *Resolver) fetchGitHub(ctx context.Context, now time.Time) githubResult {
	res := githubResult{repos: map[string]repoData{}}
	for _, full := range r.cfg.WatchedRepos() {
		d, err := r.fetchRepo(ctx, full, now)
		if err != nil {
			if res.err == nil {
				res.err = fmt.Errorf("%s: %w", full, err)
			}
			if retryAfter(err) > 0 || github.IsAuth(err) {
				break
			}
			continue
		}
		res.repos[full] = d
	}
	return res
}

func (r *Resolver) fetchRepo(ctx context.Context, full string, now time.Time) (repoData, error) {
	owner, name, _ := strings.Cut(full, "/")
	prs, warn, err := r.ad.Code.RecentPRs(ctx, owner, name, r.cfg.GitHub.Author, now.Add(-prSince))
	if err != nil {
		return repoData{}, err
	}
	bases, bwarn, err := r.ad.Code.BasePRs(ctx, owner, name, prs)
	if err != nil {
		return repoData{}, err
	}
	return repoData{prs: prs, context: bases, warn: slices.Concat(warn, bwarn)}, nil
}

// fetchAccount reads one account: per pipeline, its sources (once per
// session) and one History call for all its envs' stages, then each env's
// health. A failing call keeps that pipeline's or env's previous data and
// the rest go on, except that an expired SSO session or throttling stops
// the account: every later call would fail the same way.
func (r *Resolver) fetchAccount(ctx context.Context, account string, known map[config.PipelineKey]map[string]string) accountResult {
	res := accountResult{
		sources:   map[config.PipelineKey]map[string]string{},
		pipelines: map[config.PipelineKey]pipelineData{},
		health:    map[string]healthData{},
	}
	d, ok := r.ad.AWS[account]
	if !ok {
		res.err = fmt.Errorf("no AWS client for account %s", account)
		return res
	}
	// fail records err and reports whether the account should stop.
	fail := func(err error) bool {
		if res.err == nil {
			res.err = err
		}
		return aws.IsSSOExpired(err) || aws.IsThrottled(err)
	}
	for _, ps := range r.cfg.StageSpecs() {
		if ps.Key.Account != account {
			continue
		}
		src, ok := known[ps.Key]
		if !ok {
			var err error
			if src, err = d.Sources(ctx, ps.Key.Pipeline); err != nil {
				if fail(err) {
					return res
				}
				continue
			}
			res.sources[ps.Key] = src
		}
		deploys, warn, err := d.History(ctx, ps.Key.Pipeline, src, ps.Specs...)
		if err != nil {
			if fail(err) {
				return res
			}
			continue
		}
		res.pipelines[ps.Key] = pipelineData{deploys: deploys, warn: warn}
	}
	for _, e := range r.cfg.Envs() {
		if e.Account != account {
			continue
		}
		h, warn, err := d.Health(ctx, r.cfg.Services(e))
		if err != nil {
			if fail(err) {
				return res
			}
			continue
		}
		res.health[e.ID()] = healthData{health: h, warn: warn}
	}
	return res
}

// record updates a source's status after a poll's attempt.
func (r *Resolver) record(name string, err error, now time.Time) {
	s := r.status[name]
	if err == nil {
		*s = AdapterStatus{Name: name, OK: true, At: now}
		return
	}
	r.log.Warn("source failed", "source", name, "err", err)
	*s = AdapterStatus{Name: name, At: s.At, Err: short(err.Error()), SSO: aws.IsSSOExpired(err)}
	if d := retryAfter(err); d > 0 {
		s.Throttled, s.RetryAt = true, now.Add(d)
	}
}

// backingOff reports whether a throttled source is still waiting out its
// RetryAt.
func (r *Resolver) backingOff(name string, now time.Time) bool {
	s := r.status[name]
	return s.Throttled && now.Before(s.RetryAt)
}

// retryAfter is how long err asks the source to back off: Retry-After from
// a Jira or GitHub rate limit, AWSThrottleBackoff for AWS throttling, else 0.
func retryAfter(err error) time.Duration {
	var je *jira.APIError
	if errors.As(err, &je) && je.RetryAfter > 0 {
		return je.RetryAfter
	}
	var ge *github.APIError
	if errors.As(err, &ge) && ge.RetryAfter > 0 {
		return ge.RetryAfter
	}
	if aws.IsThrottled(err) {
		return AWSThrottleBackoff
	}
	return 0
}

// short cuts s to maxErr runes.
func short(s string) string {
	if r := []rune(s); len(r) > maxErr {
		return string(r[:maxErr-1]) + "…"
	}
	return s
}

// build runs link and rules over the last good data.
func (r *Resolver) build(ctx context.Context, now time.Time) Snapshot {
	warn := &warnings{}
	warn.add(r.ticketWarn...)
	warn.add(r.extraWarn...)
	tickets := slices.Clone(r.tickets)
	for _, x := range r.extra {
		if !slices.ContainsFunc(tickets, func(t model.Ticket) bool { return t.Key == x.Key }) {
			tickets = append(tickets, x)
		}
	}
	var prs, bases []model.PR
	for _, repo := range r.cfg.WatchedRepos() {
		d := r.repos[repo]
		prs = append(prs, d.prs...)
		bases = append(bases, d.context...)
		warn.add(d.warn...)
	}
	var histories []model.EnvHistory
	for _, e := range r.cfg.Envs() {
		k := config.PipelineKey{Account: e.Account, Pipeline: e.Pipeline}
		if len(e.Repos) == 0 {
			// Config repos win; without them, the pipeline's source actions
			// say what it deploys (better than guessing from history).
			e.Repos = aws.Repos(r.sources[k])
		}
		p := r.pipelines[k]
		h := r.health[e.ID()]
		warn.add(p.warn...)
		warn.add(h.warn...)
		histories = append(histories, model.EnvHistory{Env: e, Deploys: p.deploys[e.Stage], Health: h.health})
	}

	chains, unlinked := link.Link(tickets, prs, bases, r.cfg.Jira.Projects)
	cmp := func(repo, base, head string) model.Inclusion {
		if inc, ok := r.cache.get(repo, base, head); ok {
			return inc
		}
		if ctx.Err() != nil {
			return model.InclusionUnknown
		}
		inc, err := r.ad.Code.Compare(ctx, repo, base, head)
		if err != nil {
			warn.add(fmt.Sprintf("compare %s %s...%s: %v", repo, base, head, err))
			inc = model.InclusionUnknown
		}
		r.cache.put(repo, base, head, inc)
		return inc
	}
	for i := range chains {
		chains[i] = link.Slots(chains[i], histories, cmp)
	}

	status := make([]AdapterStatus, len(r.order))
	for i, n := range r.order {
		status[i] = *r.status[n]
	}
	return Snapshot{
		Chains:   rules.Evaluate(chains, now, r.cfg.Thresholds()),
		Unlinked: unlinked,
		Status:   status,
		Warnings: warn.list(),
		At:       now,
	}
}

// warnings collects one poll's warnings, deduplicated, in first-seen order.
// Safe for concurrent use.
type warnings struct {
	mu   sync.Mutex
	seen map[string]bool
	all  []string
}

func (w *warnings) add(msgs ...string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.seen == nil {
		w.seen = map[string]bool{}
	}
	for _, m := range msgs {
		if !w.seen[m] {
			w.seen[m] = true
			w.all = append(w.all, m)
		}
	}
}

func (w *warnings) list() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.all)
}
