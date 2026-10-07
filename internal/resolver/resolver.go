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
	Transitions(ctx context.Context, key string) ([]jira.Transition, error)
	DoTransition(ctx context.Context, key, transitionID string) error
}

// CodeHost is the part of github.Client the resolver uses.
type CodeHost interface {
	RecentPRs(ctx context.Context, owner, name, author string, since time.Time) ([]model.PR, []string, error)
	BasePRs(ctx context.Context, owner, name string, prs []model.PR) ([]model.PR, []string, error)
	Compare(ctx context.Context, repo, base, head string) (model.Inclusion, error)
	RerunFailedJobs(ctx context.Context, repo string, runID int64) error
}

// Deployer is the part of aws.Client (one account) the resolver uses.
type Deployer interface {
	Sources(ctx context.Context, pipeline string) (map[string]string, error)
	History(ctx context.Context, pipeline string, sources map[string]string, specs ...aws.StageSpec) (map[string][]model.Deploy, []string, error)
	Health(ctx context.Context, services []aws.Service) (model.Health, []string, error)
	Approve(ctx context.Context, pipeline, stage, action, token string, ok bool, summary string) error
}

// Adapters are the resolver's sources. AWS is keyed by config account name.
type Adapters struct {
	Tracker Tracker
	Code    CodeHost
	AWS     map[string]Deployer
}

// AdapterStatus is how one source fared, for the header.
type AdapterStatus struct {
	Name string    // "jira", "github", "aws <account>"
	OK   bool      // the last attempt succeeded
	At   time.Time // last success; zero if none yet
	Err  string    // the last attempt's error, short
	SSO  bool      // the error is an expired AWS SSO session
	// Auth: Jira or GitHub rejected the token (401). The source is not
	// called again until a refresh, which retries it once, or a restart.
	Auth      bool
	Throttled bool // rate limited: the source is skipped until RetryAt
	RetryAt   time.Time
}

// Snapshot is one poll's result, for the UI, which must treat it as
// read-only: its slices and pointers (EnvSlot.Deploy, PR.Failing) may share
// backing data with other Snapshots and with the Resolver's cached results.
// The Resolver replaces that data on each poll and never mutates it, so
// reading a Snapshot from another goroutine is safe.
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
	// RepoAt is when each watched repo last loaded, and EnvAt when each env
	// (by Env.ID()) last loaded both its history and its health; zero if
	// never. A time before At means that input failed or was skipped this
	// poll.
	RepoAt map[string]time.Time
	EnvAt  map[string]time.Time
}

// prSince is how far back RecentPRs looks.
const prSince = 30 * 24 * time.Hour

// AWSThrottleBackoff is the floor of the backoff after AWS throttles an
// account (AWS errors carry no Retry-After): the account is skipped until
// max(AWSThrottleBackoff, poll interval) + poll interval after the error is
// recorded. See retryAfter for why.
const AWSThrottleBackoff = time.Minute

// maxErr caps AdapterStatus.Err, in runes.
const maxErr = 160

// repoData is the last good GitHub result for one repo.
type repoData struct {
	prs     []model.PR // mine
	context []model.PR // base-branch PRs of mine, any author
	warn    []string
	at      time.Time // the poll that fetched it
}

// pipelineData is the last good AWS result for one pipeline.
type pipelineData struct {
	deploys map[string][]model.Deploy // by stage
	warn    []string
	at      time.Time // the poll that fetched it
}

// healthData is the last good health of one Env.
type healthData struct {
	health model.Health
	warn   []string
	at     time.Time // the poll that fetched it
}

// Resolver polls the adapters and builds Snapshots. Poll and Run are safe
// to call from several goroutines; polls never overlap.
type Resolver struct {
	cfg *config.Config
	ad  Adapters
	now func() time.Time
	log *slog.Logger

	interval time.Duration
	kick     chan struct{} // an action asks Run for a poll; buffer of 1

	pollMu sync.Mutex // held for a whole poll; guards everything below
	cache  *compareCache
	status map[string]*AdapterStatus
	order  []string // status names in display order

	tickets    []model.Ticket
	ticketWarn []string
	extra      []model.Ticket // named by my open PRs, not in tickets
	extraWarn  []string
	extraErr   string // the last extras lookup's failure, as a warning
	repos      map[string]repoData
	sources    map[config.PipelineKey]map[string]string // cached until a refresh
	recheck    map[config.PipelineKey]bool              // cached sources a refresh wants asked again
	pipelines  map[config.PipelineKey]pipelineData
	health     map[string]healthData // by Env.ID()
	warned     map[string]bool       // the last poll's warnings, so only new ones are logged
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
		kick:      make(chan struct{}, 1),
		cache:     newCompareCache(),
		status:    map[string]*AdapterStatus{},
		repos:     map[string]repoData{},
		sources:   map[config.PipelineKey]map[string]string{},
		recheck:   map[config.PipelineKey]bool{},
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
// as soon as it ends, and the interval restarts after every poll. A refresh
// also retries, once, a source stopped by a rejected token and asks every
// pipeline for its sources again. Send on
// refresh without blocking (a buffer of 1 coalesces repeated presses). A
// successful action (Approve, Rerun, Transition) also starts a poll, but
// not the token retry: it is a re-read after a change, not the user asking.
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
			r.onRefresh()
		case <-r.kick:
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
		ask   = maps.Clone(known)     // sources the AWS goroutines need not ask for
	)
	for k := range r.recheck {
		delete(ask, k)
	}
	run := func(name string, fetch func()) bool {
		if r.backingOff(name, now) {
			return false
		}
		wg.Add(1)
		go func() { defer wg.Done(); fetch() }()
		return true
	}
	jiraRan := run("jira", func() { jr = r.fetchJira(ctx) })
	watched := r.watchedRepos(known)
	githubRan := run("github", func() { gr = r.fetchGitHub(ctx, watched, now) })
	acctRan := make([]bool, len(accts))
	for i, a := range accts {
		acctRan[i] = run("aws "+a, func() { ars[i] = r.fetchAccount(ctx, a, ask, known) })
	}
	wg.Wait()
	if ctx.Err() != nil {
		// Shutting down: what failed failed because of ctx, so record
		// nothing and ask nothing more.
		return r.build(ctx, now)
	}

	if githubRan {
		for repo, d := range gr.repos {
			d.at = now
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
		// A Jira that just failed is not asked again this poll; the
		// previous extras stay.
		if err == nil {
			err = r.fetchExtraTickets(ctx)
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
			delete(r.recheck, k)
		}
		for k, v := range ar.pipelines {
			v.at = now
			r.pipelines[k] = v
		}
		for k, v := range ar.health {
			v.at = now
			r.health[k] = v
		}
		r.record("aws "+a, ar.err, now)
	}
	snap := r.build(ctx, now)
	current := make(map[string]bool, len(snap.Warnings))
	for _, w := range snap.Warnings {
		current[w] = true
		if !r.warned[w] { // a warning that persists is logged once, not every poll
			r.log.Warn("poll warning", "msg", w)
		}
	}
	r.warned = current
	return snap
}

func (r *Resolver) fetchJira(ctx context.Context) jiraResult {
	t, w, err := r.ad.Tracker.MyTickets(ctx, r.cfg.Jira.Projects)
	return jiraResult{tickets: t, warn: w, err: err}
}

// fetchExtraTickets looks up the tickets my open PRs name that MyTickets
// did not return (assigned to someone else, say), so those PRs still get a
// row. On error the previous extras stay. A failed lookup is a warning, not
// a Jira failure (my tickets loaded), except a rejected token or a rate
// limit, which it returns so Jira stops or backs off.
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
		r.extra, r.extraWarn, r.extraErr = nil, nil, ""
		return nil
	}
	slices.Sort(keys)
	t, w, err := r.ad.Tracker.TicketsByKey(ctx, keys)
	if err != nil {
		if isAuth(err) || r.retryAfter(err) > 0 {
			return err
		}
		r.extraErr = short(fmt.Sprintf("jira: looking up %s: %v", strings.Join(keys, ", "), err))
		return nil
	}
	r.extra, r.extraWarn, r.extraErr = t, w, ""
	return nil
}

// watchedRepos are the config's watched repos plus every repo a pipeline's
// sources name, so an env without config repos still has its PRs fetched.
// It reads the sources known when the poll starts: a repo first discovered
// in a poll is fetched from the next one.
func (r *Resolver) watchedRepos(sources map[config.PipelineKey]map[string]string) []string {
	all := r.cfg.WatchedRepos()
	for _, src := range sources {
		all = append(all, aws.Repos(src)...)
	}
	slices.Sort(all)
	return slices.Compact(all)
}

// fetchGitHub fetches my recent PRs and their base-branch PRs per watched
// repo. A repo that fails keeps its previous data and the rest go on, except
// that a rate limit or a bad token stops the loop: every later call would
// fail the same way.
func (r *Resolver) fetchGitHub(ctx context.Context, repos []string, now time.Time) githubResult {
	res := githubResult{repos: map[string]repoData{}}
	for _, full := range repos {
		d, err := r.fetchRepo(ctx, full, now)
		if err != nil {
			if res.err == nil {
				res.err = fmt.Errorf("%s: %w", full, err)
			}
			if r.retryAfter(err) > 0 || isAuth(err) {
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
// health. Sources is asked for pipelines missing from ask; cached holds
// every pipeline's previous sources. A failing call keeps that pipeline's or
// env's previous data and the rest go on, except that an expired SSO session
// or throttling stops the account: every later call would fail the same
// way. A pipeline asked again after a refresh whose Sources call fails
// otherwise reads its history with its cached sources, with a warning.
func (r *Resolver) fetchAccount(ctx context.Context, account string, ask, cached map[config.PipelineKey]map[string]string) accountResult {
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
		var fallback string // a warning when the cached sources stand in
		src, ok := ask[ps.Key]
		if !ok {
			fresh, err := d.Sources(ctx, ps.Key.Pipeline)
			old, hadOld := cached[ps.Key]
			switch {
			case err == nil:
				src = fresh
				res.sources[ps.Key] = src
			case aws.IsSSOExpired(err) || aws.IsThrottled(err) || !hadOld:
				if fail(err) {
					return res
				}
				continue
			default:
				src = old
				fallback = short(fmt.Sprintf("aws %s %s: using cached sources: %v", account, ps.Key.Pipeline, err))
			}
		}
		deploys, warn, err := d.History(ctx, ps.Key.Pipeline, src, ps.Specs...)
		if err != nil {
			if fail(err) {
				return res
			}
			continue
		}
		if fallback != "" {
			warn = append(slices.Clone(warn), fallback)
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

// record updates a source's status after a poll's attempt. At is the poll's
// time; RetryAt runs from now, when the error is recorded, so a slow poll
// does not eat into a backoff.
func (r *Resolver) record(name string, err error, now time.Time) {
	s := r.status[name]
	if err == nil {
		*s = AdapterStatus{Name: name, OK: true, At: now}
		return
	}
	r.log.Warn("source failed", "source", name, "err", err)
	*s = AdapterStatus{Name: name, At: s.At, Err: short(err.Error()),
		SSO: strings.HasPrefix(name, "aws ") && aws.IsSSOExpired(err)}
	if !strings.HasPrefix(name, "aws ") && isAuth(err) {
		s.Auth, s.Err = true, "token rejected"
	}
	if d := r.retryAfter(err); d > 0 {
		s.Throttled, s.RetryAt = true, r.now().Add(d)
	}
}

// isAuth reports whether err is a Jira or GitHub 401: the token is bad.
func isAuth(err error) bool { return jira.IsAuth(err) || github.IsAuth(err) }

// backingOff reports whether a source is stopped: its token was rejected
// (until a refresh), or it is throttled and RetryAt has not come.
func (r *Resolver) backingOff(name string, now time.Time) bool {
	s := r.status[name]
	return s.Auth || s.Throttled && now.Before(s.RetryAt)
}

// onRefresh is a user's refresh: every source stopped by a rejected token
// tries once more on the next poll, and every pipeline is asked for its
// sources again, so a changed pipeline is picked up. Until a pipeline
// answers (a skipped or failing account asks again next poll), its previous
// sources stay in use for watched repos and env repos, so a failed
// rediscovery loses nothing.
func (r *Resolver) onRefresh() {
	r.pollMu.Lock()
	defer r.pollMu.Unlock()
	for _, s := range r.status {
		s.Auth = false
	}
	for k := range r.sources {
		r.recheck[k] = true
	}
}

// retryAfter is how long err asks the source to back off, from when the
// error is recorded: Retry-After from a Jira or GitHub rate limit, else
// max(AWSThrottleBackoff, interval) + interval for AWS throttling, else 0.
// The extra interval makes sure at least one poll is skipped: backoff is
// checked when a poll starts, and the next poll starts one interval after
// this one ends, so a backoff of exactly one interval would already have
// expired by then.
func (r *Resolver) retryAfter(err error) time.Duration {
	var je *jira.APIError
	if errors.As(err, &je) && je.RetryAfter > 0 {
		return je.RetryAfter
	}
	var ge *github.APIError
	if errors.As(err, &ge) && ge.RetryAfter > 0 {
		return ge.RetryAfter
	}
	if aws.IsThrottled(err) {
		return max(AWSThrottleBackoff, r.interval) + r.interval
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
	if r.extraErr != "" {
		warn.add(r.extraErr)
	}
	tickets := slices.Clone(r.tickets)
	for _, x := range r.extra {
		if !slices.ContainsFunc(tickets, func(t model.Ticket) bool { return t.Key == x.Key }) {
			tickets = append(tickets, x)
		}
	}
	var prs, bases []model.PR
	repoAt := map[string]time.Time{}
	for _, repo := range r.watchedRepos(r.sources) {
		d := r.repos[repo]
		prs = append(prs, d.prs...)
		bases = append(bases, d.context...)
		warn.add(d.warn...)
		repoAt[repo] = d.at
	}
	var (
		histories    []model.EnvHistory
		undiscovered []int // indexes of envs with no config repos and no sources yet
		envAt        = map[string]time.Time{}
	)
	for i, e := range r.cfg.Envs() {
		k := config.PipelineKey{Account: e.Account, Pipeline: e.Pipeline}
		if len(e.Repos) == 0 {
			// Config repos win; without them, the pipeline's source actions
			// say what it deploys (better than guessing from history).
			src, ok := r.sources[k]
			if !ok {
				undiscovered = append(undiscovered, i)
			}
			e.Repos = aws.Repos(src)
		}
		p := r.pipelines[k]
		h := r.health[e.ID()]
		envAt[e.ID()] = earliest(p.at, h.at)
		warn.add(p.warn...)
		warn.add(h.warn...)
		histories = append(histories, model.EnvHistory{Env: e, Deploys: p.deploys[e.Stage], Health: h.health})
	}

	chains, unlinked := link.Link(tickets, prs, bases, r.cfg.Jira.Projects)
	// Read before compares, which can stop GitHub mid-build.
	githubFresh := r.status["github"].OK && r.status["github"].At.Equal(now)
	r.cache.next()
	defer r.cache.sweep()
	// Compares run one at a time, here, after the fetches: the first poll
	// of a session, with an empty cache, can take a while (left as is on
	// purpose). A rate-limited GitHub, or one that rejected the token, is
	// not asked for compares; misses are Unknown. link calls cmp from this goroutine
	// only, so stop needs no lock.
	stop := r.backingOff("github", now)
	cmp := func(repo, base, head string) model.Inclusion {
		if inc, ok := r.cache.get(repo, base, head); ok {
			return inc
		}
		if stop || ctx.Err() != nil {
			return model.InclusionUnknown
		}
		inc, err := r.ad.Code.Compare(ctx, repo, base, head)
		if err != nil {
			warn.add(fmt.Sprintf("compare %s %s...%s: %v", repo, base, head, err))
			inc = model.InclusionUnknown
			if d := r.retryAfter(err); d > 0 || isAuth(err) {
				// Stop GitHub as a whole, from the rest of this poll's
				// compares to the PR fetches: until RetryAt, or for a
				// rejected token until a refresh.
				stop = true
				r.log.Warn("source failed", "source", "github", "err", err)
				st := r.status["github"]
				st.OK, st.Err = false, short(err.Error())
				if isAuth(err) {
					st.Auth, st.Err = true, "token rejected"
				} else {
					st.Throttled, st.RetryAt = true, r.now().Add(d)
				}
			}
		}
		r.cache.put(repo, base, head, inc)
		return inc
	}
	for i := range chains {
		chains[i] = link.Slots(chains[i], histories, cmp)
		markUndiscovered(&chains[i], undiscovered)
		markStale(&chains[i], repoAt, envAt, githubFresh, now)
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
		RepoAt:   repoAt,
		EnvAt:    envAt,
	}
}

// markStale sets c.Stale when an input of c did not load this poll (it
// failed, was skipped, or never loaded): a repo one of c's PRs comes from,
// or an env whose slot applies. A chain with no PRs is stale when GitHub
// failed or was skipped this poll (githubFresh false): nobody can tell
// whether a PR for it exists. repoAt is keyed by watched repo, and PR.Repo
// matches it ignoring case; envAt is keyed by Env.ID().
func markStale(c *model.Chain, repoAt, envAt map[string]time.Time, githubFresh bool, now time.Time) {
	if len(c.PRs) == 0 && !githubFresh {
		c.Stale, c.StaleReason = true, "GitHub not refreshed"
		return
	}
	for _, p := range c.PRs {
		at, ok := lookupFold(repoAt, p.Repo)
		if !ok || !at.Equal(now) {
			c.Stale, c.StaleReason = true, staleReason(p.Repo, at)
			return
		}
	}
	for _, s := range c.Slots {
		if at := envAt[s.Env.ID()]; s.Applies && !at.Equal(now) {
			c.Stale, c.StaleReason = true, staleReason(s.Env.ID(), at)
			return
		}
	}
}

// earliest is the earlier of a and b; zero if either is.
func earliest(a, b time.Time) time.Time {
	if a.IsZero() || b.IsZero() {
		return time.Time{}
	}
	if a.Before(b) {
		return a
	}
	return b
}

// lookupFold is m[key], matching key ignoring case when there is no exact
// match.
func lookupFold(m map[string]time.Time, key string) (time.Time, bool) {
	if v, ok := m[key]; ok {
		return v, true
	}
	for k, v := range m {
		if strings.EqualFold(k, key) {
			return v, true
		}
	}
	return time.Time{}, false
}

// staleReason says why what (a repo or Env.ID()) is stale.
func staleReason(what string, at time.Time) string {
	if at.IsZero() {
		return what + " never loaded"
	}
	return what + " not refreshed since " + at.Format("15:04:05")
}

// markUndiscovered makes each env in envs (indexes into c.Slots) an
// applying SlotUnknown when c has a merged PR on its way to the default
// branch. Such an env has no config repos and its pipeline's sources were
// never discovered (an expired SSO session at startup, say), so nobody knows
// what it deploys; leaving its slot out would let the other envs alone show
// a false green. Env.Repos stays empty. Such an env has no history either
// (History is only asked once Sources answered), so nothing better is lost.
func markUndiscovered(c *model.Chain, envs []int) {
	if len(envs) == 0 || !slices.ContainsFunc(c.PRs, func(p model.PR) bool {
		return p.State == model.PRMerged && p.EffectiveSHA != ""
	}) {
		return
	}
	for _, i := range envs {
		s := &c.Slots[i]
		*s = model.EnvSlot{Env: s.Env, Health: s.Health, State: model.SlotUnknown, Applies: true}
	}
}

// warnings collects one poll's warnings, deduplicated, in first-seen order.
// Only build's goroutine uses it.
type warnings struct {
	seen map[string]bool
	all  []string
}

func (w *warnings) add(msgs ...string) {
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

func (w *warnings) list() []string { return slices.Clone(w.all) }
