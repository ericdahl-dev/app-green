package resolver_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ericdahl-dev/app-green/internal/aws"
	"github.com/ericdahl-dev/app-green/internal/config"
	"github.com/ericdahl-dev/app-green/internal/jira"
	"github.com/ericdahl-dev/app-green/internal/model"
)

const repo = "acme/app"

var t0 = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

// clock is a settable clock for the resolver.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock() *clock { return &clock{now: t0} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// baseTOML is a config with two accounts; envs are appended per test.
const baseTOML = `
[jira]
  site = "https://example.atlassian.net"
  email = "me@example.com"
  token_env = "APP_GREEN_TEST_JIRA_TOKEN"
  projects = ["ABC"]

[github]
  author = "me"
  token_env = "APP_GREEN_TEST_GITHUB_TOKEN"
  repos = ["acme/app"]

[[aws.accounts]]
  name = "stage-acct"
  profile = "stage"
  region = "us-east-1"

[[aws.accounts]]
  name = "prod-acct"
  profile = "prod"
  region = "us-east-1"
`

// testEnv is the Test stage in stage-acct, deploying acme/app.
const testEnv = `
[[envs]]
  account = "stage-acct"
  pipeline = "app-pipeline"
  stage = "Test"
  deploy_action = "Deploy"
  repos = ["acme/app"]
`

// loadConfig writes baseTOML plus envs to a temp file and loads it.
func loadConfig(t *testing.T, envs ...string) *config.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(baseTOML+strings.Join(envs, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

type fakeTracker struct {
	mu         sync.Mutex
	my         []model.Ticket
	myWarn     []string
	myErr      error
	myCalls    int
	byKey      map[string]model.Ticket
	byKeyErr   error
	byKeyCalls [][]string
	block      chan struct{} // when set, MyTickets waits for it to close
	inFlight   int
	maxFlight  int

	transitions map[string][]jira.Transition // by key
	moves       [][2]string                  // DoTransition calls: key, transition ID
	moveErr     error
}

func (f *fakeTracker) Transitions(_ context.Context, key string) ([]jira.Transition, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.transitions[key]), nil
}

func (f *fakeTracker) DoTransition(_ context.Context, key, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.moves = append(f.moves, [2]string{key, id})
	return f.moveErr
}

func (f *fakeTracker) MyTickets(ctx context.Context, projects []string) ([]model.Ticket, []string, error) {
	f.mu.Lock()
	f.myCalls++
	f.inFlight++
	f.maxFlight = max(f.maxFlight, f.inFlight)
	block := f.block
	f.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inFlight--
	return slices.Clone(f.my), slices.Clone(f.myWarn), f.myErr
}

func (f *fakeTracker) TicketsByKey(_ context.Context, keys []string) ([]model.Ticket, []string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byKeyCalls = append(f.byKeyCalls, slices.Clone(keys))
	if f.byKeyErr != nil {
		return nil, nil, f.byKeyErr
	}
	var out []model.Ticket
	for _, k := range keys {
		if t, ok := f.byKey[k]; ok {
			out = append(out, t)
		}
	}
	return out, nil, nil
}

func (f *fakeTracker) set(fn func(f *fakeTracker)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *fakeTracker) calls() (my int, byKey [][]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.myCalls, slices.Clone(f.byKeyCalls)
}

type compareCall struct{ repo, base, head string }

type fakeHost struct {
	mu           sync.Mutex
	prs          map[string][]model.PR // by repo
	prWarn       []string
	prErr        error
	prErrRepo    map[string]error
	prCalls      int
	prRepos      []string
	base         map[string][]model.PR // BasePRs result by repo
	baseInputs   map[string][]model.PR
	compare      func(repo, base, head string) (model.Inclusion, error)
	compareCalls []compareCall
	rerunCalls   []rerunCall
	rerunErr     error
}

type rerunCall struct {
	repo  string
	runID int64
}

func (f *fakeHost) RerunFailedJobs(_ context.Context, repo string, runID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rerunCalls = append(f.rerunCalls, rerunCall{repo, runID})
	return f.rerunErr
}

func (f *fakeHost) reruns() []rerunCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.rerunCalls)
}

func (f *fakeHost) RecentPRs(_ context.Context, owner, name, _ string, _ time.Time) ([]model.PR, []string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prCalls++
	f.prRepos = append(f.prRepos, owner+"/"+name)
	if f.prErr != nil {
		return nil, nil, f.prErr
	}
	if err := f.prErrRepo[owner+"/"+name]; err != nil {
		return nil, nil, err
	}
	return slices.Clone(f.prs[owner+"/"+name]), slices.Clone(f.prWarn), nil
}

func (f *fakeHost) BasePRs(_ context.Context, owner, name string, prs []model.PR) ([]model.PR, []string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.baseInputs == nil {
		f.baseInputs = map[string][]model.PR{}
	}
	f.baseInputs[owner+"/"+name] = slices.Clone(prs)
	return slices.Clone(f.base[owner+"/"+name]), nil, nil
}

func (f *fakeHost) Compare(_ context.Context, repo, base, head string) (model.Inclusion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.compareCalls = append(f.compareCalls, compareCall{repo, base, head})
	if f.compare == nil {
		return model.InclusionUnknown, nil
	}
	return f.compare(repo, base, head)
}

func (f *fakeHost) set(fn func(f *fakeHost)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *fakeHost) compares() []compareCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.compareCalls)
}

type fakeDeployer struct {
	mu           sync.Mutex
	sources      map[string]map[string]string // by pipeline
	sourcesCalls int
	sourcesErr   error                                // when set, Sources fails with it (histErr also fails it)
	hist         map[string]map[string][]model.Deploy // by pipeline, then stage
	histWarn     []string
	histErr      error
	histErrPipe  map[string]error
	histCalls    int
	histSpecs    [][]aws.StageSpec
	health       map[string]model.Health // by first service name
	healthErr    error
	during       func() // when set, History calls it first (to move the clock mid-poll)
	approveCalls []approveCall
	approveErr   error
}

func (f *fakeDeployer) Approve(_ context.Context, pipeline, stage, action, token string, ok bool, summary string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.approveCalls = append(f.approveCalls, approveCall{pipeline, stage, action, token, ok, summary})
	return f.approveErr
}

func (f *fakeDeployer) approvals() []approveCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.approveCalls)
}

func (f *fakeDeployer) Sources(_ context.Context, pipeline string) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sourcesCalls++
	if f.histErr != nil {
		return nil, f.histErr
	}
	if f.sourcesErr != nil {
		return nil, f.sourcesErr
	}
	src, ok := f.sources[pipeline]
	if !ok {
		src = map[string]string{repo: "AppSource"}
	}
	return src, nil
}

func (f *fakeDeployer) History(_ context.Context, pipeline string, _ map[string]string, specs ...aws.StageSpec) (map[string][]model.Deploy, []string, error) {
	f.mu.Lock()
	during := f.during
	f.mu.Unlock()
	if during != nil {
		during()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.histCalls++
	f.histSpecs = append(f.histSpecs, slices.Clone(specs))
	if f.histErr != nil {
		return nil, nil, f.histErr
	}
	if err := f.histErrPipe[pipeline]; err != nil {
		return nil, nil, err
	}
	return f.hist[pipeline], slices.Clone(f.histWarn), nil
}

func (f *fakeDeployer) Health(_ context.Context, services []aws.Service) (model.Health, []string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.healthErr != nil {
		return model.Health{}, nil, f.healthErr
	}
	if len(services) == 0 {
		return model.Health{}, nil, nil
	}
	return f.health[services[0].Name], nil, nil
}

func (f *fakeDeployer) set(fn func(f *fakeDeployer)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *fakeDeployer) calls() (sources, history int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sourcesCalls, f.histCalls
}

func ticket(key string) model.Ticket {
	return model.Ticket{Key: key, Title: "x", Status: "In Progress", StatusCategory: model.StatusInProgress,
		Updated: t0.Add(-time.Hour), StatusSince: t0.Add(-time.Hour)}
}

func mergedPR(n int, title, sha string) model.PR {
	return model.PR{Repo: repo, DefaultBranch: "main", Number: n, Title: title, HeadRef: "feature/x" + sha, BaseRef: "main",
		State: model.PRMerged, MergeSHA: sha, MergedAt: t0.Add(-3 * time.Hour), OpenedAt: t0.Add(-5 * time.Hour)}
}

func openPR(n int, title string) model.PR {
	return model.PR{Repo: repo, DefaultBranch: "main", Number: n, Title: title, HeadRef: "feature/open" + title, BaseRef: "main",
		State: model.PROpen, OpenedAt: t0.Add(-5 * time.Hour), Checks: model.ChecksPending}
}

func succeeded(sha string, at time.Time) model.Deploy {
	return model.Deploy{ExecutionID: "ex-" + sha, Status: model.DeploySucceeded, Revisions: map[string]string{repo: sha}, FinishedAt: at}
}

// safeBuffer is a bytes.Buffer safe for concurrent writes.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
