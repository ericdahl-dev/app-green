package resolver_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aws/smithy-go"
	"github.com/ericdahl-dev/app-green/internal/github"
	"github.com/ericdahl-dev/app-green/internal/jira"
	"github.com/ericdahl-dev/app-green/internal/model"
	"github.com/ericdahl-dev/app-green/internal/resolver"
)

type harness struct {
	r       *resolver.Resolver
	tracker *fakeTracker
	host    *fakeHost
	stage   *fakeDeployer
	prod    *fakeDeployer
	clock   *clock
}

func newHarness(t *testing.T, log *slog.Logger, envs ...string) *harness {
	t.Helper()
	if len(envs) == 0 {
		envs = []string{testEnv}
	}
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	h := &harness{tracker: &fakeTracker{}, host: &fakeHost{}, stage: &fakeDeployer{}, prod: &fakeDeployer{}, clock: newClock()}
	h.r = resolver.New(loadConfig(t, envs...), resolver.Adapters{
		Tracker: h.tracker,
		Code:    h.host,
		AWS:     map[string]resolver.Deployer{"stage-acct": h.stage, "prod-acct": h.prod},
	}, h.clock.Now, log)
	return h
}

// withShippedChain sets up ABC-1 with one merged PR (sha aaaa111) whose exact
// SHA succeeded in Test.
func (h *harness) withShippedChain() {
	h.tracker.set(func(f *fakeTracker) { f.my = []model.Ticket{ticket("ABC-1")} })
	h.host.set(func(f *fakeHost) { f.prs = map[string][]model.PR{repo: {mergedPR(1, "ABC-1: x", "aaaa111")}} })
	h.stage.set(func(f *fakeDeployer) {
		f.hist = map[string]map[string][]model.Deploy{"app-pipeline": {"Test": {succeeded("aaaa111", t0.Add(-time.Hour))}}}
	})
}

func TestPollBuildsChainsWithoutCompare(t *testing.T) {
	h := newHarness(t, nil)
	h.withShippedChain()

	snap := h.r.Poll(context.Background())

	if len(snap.Chains) != 1 || snap.Chains[0].Ticket.Key != "ABC-1" {
		t.Fatalf("chains = %+v, want one for ABC-1", snap.Chains)
	}
	if got := snap.Chains[0].Stage; got != model.StageInTest {
		t.Errorf("stage = %v, want %v", got, model.StageInTest)
	}
	if n := len(h.host.compares()); n != 0 {
		t.Errorf("compare calls = %d, want 0 on an exact SHA match", n)
	}
	if !snap.At.Equal(t0) {
		t.Errorf("At = %v, want %v", snap.At, t0)
	}
}

// withCompareChain sets up ABC-1 whose merge sha aaaa111 reached Test only
// inside the newer cccc222, so the slot needs a compare.
func (h *harness) withCompareChain(inc model.Inclusion, err error) {
	h.withShippedChain()
	h.stage.set(func(f *fakeDeployer) {
		f.hist = map[string]map[string][]model.Deploy{"app-pipeline": {"Test": {succeeded("cccc222", t0.Add(-time.Hour))}}}
	})
	h.host.set(func(f *fakeHost) {
		f.compare = func(string, string, string) (model.Inclusion, error) { return inc, err }
	})
}

func TestCompareCachedAcrossPolls(t *testing.T) {
	h := newHarness(t, nil)
	h.withCompareChain(model.Included, nil)

	h.r.Poll(context.Background())
	snap := h.r.Poll(context.Background())

	want := []compareCall{{repo, "aaaa111", "cccc222"}}
	if got := h.host.compares(); len(got) != 1 || got[0] != want[0] {
		t.Errorf("compare calls = %v, want %v", got, want)
	}
	if got := snap.Chains[0].Stage; got != model.StageInTest {
		t.Errorf("stage = %v, want %v from the cached Included", got, model.StageInTest)
	}
}

func TestUnknownCompareNotCached(t *testing.T) {
	h := newHarness(t, nil)
	h.withCompareChain(model.InclusionUnknown, nil)

	h.r.Poll(context.Background())
	h.r.Poll(context.Background())

	if n := len(h.host.compares()); n != 2 {
		t.Errorf("compare calls = %d, want 2 (Unknown is asked again)", n)
	}
}

func TestCompareErrorIsUnknownWithOneWarning(t *testing.T) {
	h := newHarness(t, nil)
	h.withCompareChain(model.InclusionUnknown, errors.New("github: HTTP 502 Bad Gateway"))
	// Two chains whose slots need the same failing compare: one warning.
	h.tracker.set(func(f *fakeTracker) { f.my = []model.Ticket{ticket("ABC-1"), ticket("ABC-2")} })
	h.host.set(func(f *fakeHost) {
		pr := mergedPR(1, "ABC-1 ABC-2: x", "aaaa111")
		f.prs = map[string][]model.PR{repo: {pr}}
	})

	snap := h.r.Poll(context.Background())

	if len(snap.Warnings) != 1 || !strings.Contains(snap.Warnings[0], "502") || !strings.Contains(snap.Warnings[0], "aaaa111") {
		t.Errorf("warnings = %q, want one naming the compare and its error", snap.Warnings)
	}
	for _, c := range snap.Chains {
		if got := c.Slots[0].State; got != model.SlotUnknown {
			t.Errorf("%s slot = %v, want unknown", c.Ticket.Key, got)
		}
	}
	h.r.Poll(context.Background())
	if n := len(h.host.compares()); n != 4 {
		t.Errorf("compare calls = %d, want 4 (errors are not cached)", n)
	}
}

func statusOf(t *testing.T, snap resolver.Snapshot, name string) resolver.AdapterStatus {
	t.Helper()
	for _, s := range snap.Status {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no status %q in %+v", name, snap.Status)
	return resolver.AdapterStatus{}
}

func TestFailedSourceKeepsLastGoodWhileOthersUpdate(t *testing.T) {
	h := newHarness(t, nil)
	h.withShippedChain()
	h.r.Poll(context.Background())

	h.clock.Advance(time.Minute)
	h.tracker.set(func(f *fakeTracker) { f.myErr = errors.New("jira: HTTP 502 Bad Gateway") })
	// GitHub has news: the PR now names ABC-1 and is joined by an unlinked one.
	h.host.set(func(f *fakeHost) {
		f.prs[repo] = append(f.prs[repo], openPR(2, "no key here"))
	})
	snap := h.r.Poll(context.Background())

	if len(snap.Chains) != 1 || snap.Chains[0].Ticket.Key != "ABC-1" {
		t.Fatalf("chains = %+v, want ABC-1 kept from the last good poll", snap.Chains)
	}
	if len(snap.Unlinked) != 1 || snap.Unlinked[0].Number != 2 {
		t.Errorf("unlinked = %+v, want the new PR #2 (GitHub still updates)", snap.Unlinked)
	}
	j := statusOf(t, snap, "jira")
	if j.OK || !strings.Contains(j.Err, "502") || !j.At.Equal(t0) {
		t.Errorf("jira status = %+v, want not OK, the error, At = first poll", j)
	}
	for _, name := range []string{"github", "aws stage-acct"} {
		if s := statusOf(t, snap, name); !s.OK || s.Err != "" || !s.At.Equal(t0.Add(time.Minute)) {
			t.Errorf("%s status = %+v, want OK at the second poll", name, s)
		}
	}
	if got := []string{snap.Status[0].Name, snap.Status[1].Name, snap.Status[2].Name}; got[0] != "jira" || got[1] != "github" || got[2] != "aws stage-acct" || len(snap.Status) != 3 {
		t.Errorf("status order = %v, want jira, github, then accounts with envs", snap.Status)
	}
}

// prodEnv is the Production stage in prod-acct, deploying acme/app.
const prodEnv = `
[[envs]]
  account = "prod-acct"
  pipeline = "app-pipeline"
  stage = "Production"
  deploy_action = "Deploy"
  prod = true
  repos = ["acme/app"]
`

func TestSSOExpiredMarksOnlyThatAccount(t *testing.T) {
	h := newHarness(t, nil, testEnv, prodEnv)
	h.withShippedChain()
	h.prod.set(func(f *fakeDeployer) {
		f.histErr = errors.New("operation error CodePipeline: ListPipelineExecutions, the SSO session has expired or is invalid")
	})

	snap := h.r.Poll(context.Background())

	p := statusOf(t, snap, "aws prod-acct")
	if p.OK || !p.SSO || p.Err == "" {
		t.Errorf("prod-acct status = %+v, want not OK with SSO set", p)
	}
	if s := statusOf(t, snap, "aws stage-acct"); !s.OK || s.SSO {
		t.Errorf("stage-acct status = %+v, want OK", s)
	}
	if got := snap.Chains[0].Stage; got != model.StageInTest {
		t.Errorf("stage = %v, want %v from stage-acct's history", got, model.StageInTest)
	}
}

func TestThrottledSourceSkippedUntilRetryAt(t *testing.T) {
	cases := []struct {
		name   string
		fail   func(h *harness, err bool)
		calls  func(h *harness) int
		retry  time.Duration // RetryAt - poll time
		source string
	}{
		{
			name: "jira 429", source: "jira", retry: 2 * time.Minute,
			fail: func(h *harness, on bool) {
				h.tracker.set(func(f *fakeTracker) {
					f.myErr = nil
					if on {
						f.myErr = &jira.APIError{Status: 429, RetryAfter: 2 * time.Minute}
					}
				})
			},
			calls: func(h *harness) int { n, _ := h.tracker.calls(); return n },
		},
		{
			name: "github rate-limited 403", source: "github", retry: 2 * time.Minute,
			fail: func(h *harness, on bool) {
				h.host.set(func(f *fakeHost) {
					f.prErr = nil
					if on {
						f.prErr = &github.APIError{Status: 403, Message: "API rate limit exceeded", RetryAfter: 2 * time.Minute}
					}
				})
			},
			calls: func(h *harness) int { h.host.mu.Lock(); defer h.host.mu.Unlock(); return h.host.prCalls },
		},
		{
			name: "aws throttling", source: "aws stage-acct", retry: resolver.AWSThrottleBackoff,
			fail: func(h *harness, on bool) {
				h.stage.set(func(f *fakeDeployer) {
					f.histErr = nil
					if on {
						f.histErr = &smithy.GenericAPIError{Code: "ThrottlingException", Message: "Rate exceeded"}
					}
				})
			},
			calls: func(h *harness) int { _, n := h.stage.calls(); return n },
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, nil)
			h.withShippedChain()
			h.r.Poll(context.Background()) // last good data
			h.clock.Advance(time.Minute)
			polledAt := h.clock.Now()
			c.fail(h, true)

			snap := h.r.Poll(context.Background())
			s := statusOf(t, snap, c.source)
			if s.OK || !s.Throttled || !s.RetryAt.Equal(polledAt.Add(c.retry)) {
				t.Fatalf("status = %+v, want throttled until %v", s, polledAt.Add(c.retry))
			}
			before := c.calls(h)
			c.fail(h, false)

			h.clock.Advance(c.retry - time.Second)
			snap = h.r.Poll(context.Background())
			if got := c.calls(h); got != before {
				t.Errorf("calls before RetryAt = %d, want %d (skipped)", got, before)
			}
			if s := statusOf(t, snap, c.source); !s.Throttled || s.OK {
				t.Errorf("status while skipped = %+v, want still throttled", s)
			}
			if len(snap.Chains) != 1 || snap.Chains[0].Stage != model.StageInTest {
				t.Errorf("chains while skipped = %+v, want last good kept", snap.Chains)
			}

			h.clock.Advance(time.Second)
			snap = h.r.Poll(context.Background())
			if got := c.calls(h); got <= before {
				t.Errorf("calls at RetryAt = %d, want more than %d", got, before)
			}
			if s := statusOf(t, snap, c.source); !s.OK || s.Throttled {
				t.Errorf("status after RetryAt = %+v, want OK", s)
			}
		})
	}
}

func TestTicketsFromMyOpenPRs(t *testing.T) {
	h := newHarness(t, nil)
	h.withShippedChain()
	h.tracker.set(func(f *fakeTracker) {
		f.byKey = map[string]model.Ticket{"ABC-9": ticket("ABC-9"), "ABC-8": ticket("ABC-8")}
	})
	h.host.set(func(f *fakeHost) {
		f.prs[repo] = append(f.prs[repo],
			openPR(9, "ABC-9: not assigned to me"),
			openPR(10, "ABC-1: already mine"),
			mergedPR(8, "ABC-8: merged, not looked up", "bbbb222"))
	})

	snap := h.r.Poll(context.Background())

	if _, byKey := h.tracker.calls(); len(byKey) != 1 || len(byKey[0]) != 1 || byKey[0][0] != "ABC-9" {
		t.Errorf("TicketsByKey calls = %v, want one for [ABC-9]", byKey)
	}
	var keys []string
	for _, c := range snap.Chains {
		keys = append(keys, c.Ticket.Key)
	}
	if !slices.Contains(keys, "ABC-9") || !slices.Contains(keys, "ABC-1") || slices.Contains(keys, "ABC-8") {
		t.Errorf("chain keys = %v, want ABC-1 and ABC-9 only", keys)
	}
}

func TestNoTicketsByKeyCallWithoutExtraKeys(t *testing.T) {
	h := newHarness(t, nil)
	h.withShippedChain()
	h.r.Poll(context.Background())
	if _, byKey := h.tracker.calls(); len(byKey) != 0 {
		t.Errorf("TicketsByKey calls = %v, want none", byKey)
	}
}

func TestExtraTicketsFailureKeepsLastGood(t *testing.T) {
	h := newHarness(t, nil)
	h.withShippedChain()
	h.tracker.set(func(f *fakeTracker) { f.byKey = map[string]model.Ticket{"ABC-9": ticket("ABC-9")} })
	h.host.set(func(f *fakeHost) { f.prs[repo] = append(f.prs[repo], openPR(9, "ABC-9: x")) })
	h.r.Poll(context.Background())

	h.tracker.set(func(f *fakeTracker) { f.byKeyErr = errors.New("jira: HTTP 500") })
	snap := h.r.Poll(context.Background())

	if len(snap.Chains) != 2 {
		t.Errorf("chains = %d, want 2 (ABC-9 kept)", len(snap.Chains))
	}
	if j := statusOf(t, snap, "jira"); j.OK || !strings.Contains(j.Err, "500") {
		t.Errorf("jira status = %+v, want the TicketsByKey error", j)
	}
}

func TestBasePRsAreContextOnly(t *testing.T) {
	h := newHarness(t, nil)
	h.withShippedChain()
	mine := mergedPR(3, "ABC-1: part 2", "dddd444")
	mine.BaseRef = "colleague/base"
	// A colleague's base PR with no ticket key: it carries mine onto main.
	base := mergedPR(4, "refactor the thing", "aaaa111")
	base.HeadRef = "colleague/base"
	base.MergedAt = mine.MergedAt.Add(time.Hour)
	h.host.set(func(f *fakeHost) {
		f.prs = map[string][]model.PR{repo: {mine}}
		f.base = map[string][]model.PR{repo: {base}}
	})

	snap := h.r.Poll(context.Background())

	h.host.mu.Lock()
	in := h.host.baseInputs[repo]
	h.host.mu.Unlock()
	if len(in) != 1 || in[0].Number != 3 {
		t.Errorf("BasePRs got %+v, want my PR #3", in)
	}
	if len(snap.Unlinked) != 0 {
		t.Errorf("unlinked = %+v, want none (a base PR is context only)", snap.Unlinked)
	}
	if len(snap.Chains) != 1 || len(snap.Chains[0].PRs) != 1 || snap.Chains[0].PRs[0].Number != 3 {
		t.Fatalf("chains = %+v, want ABC-1 with only #3", snap.Chains)
	}
	pr := snap.Chains[0].PRs[0]
	if pr.Stranded || pr.EffectiveSHA != "aaaa111" {
		t.Errorf("#3 stranded=%v effective=%q, want carried by the base PR's aaaa111", pr.Stranded, pr.EffectiveSHA)
	}
	if got := snap.Chains[0].Stage; got != model.StageInTest {
		t.Errorf("stage = %v, want %v", got, model.StageInTest)
	}
}

// apiEnv deploys a second repo, acme/api, from its own pipeline.
const apiEnv = `
[[envs]]
  account = "prod-acct"
  pipeline = "api-pipeline"
  stage = "Test"
  deploy_action = "Deploy"
  repos = ["acme/api"]
`

func TestOneFailingRepoDoesNotStopTheOthers(t *testing.T) {
	h := newHarness(t, nil, testEnv, apiEnv)
	h.withShippedChain()
	h.host.set(func(f *fakeHost) {
		// acme/api sorts first and fails; acme/app must still be fetched.
		f.prErrRepo = map[string]error{"acme/api": &github.APIError{Status: 404, Message: "Not Found"}}
	})

	snap := h.r.Poll(context.Background())

	if len(snap.Chains) != 1 || len(snap.Chains[0].PRs) != 1 {
		t.Errorf("chains = %+v, want ABC-1 with acme/app's PR", snap.Chains)
	}
	if g := statusOf(t, snap, "github"); g.OK || !strings.Contains(g.Err, "acme/api") {
		t.Errorf("github status = %+v, want the error naming acme/api", g)
	}
}

// prodTestEnv is a Test stage in prod-acct's app-pipeline, before prodEnv.
const prodTestEnv = `
[[envs]]
  account = "prod-acct"
  pipeline = "app-pipeline"
  stage = "Test"
  deploy_action = "Deploy"
  approval_action = "ApproveTest"
  repos = ["acme/app"]
`

func TestOneHistoryCallPerPipelineAndSourcesCached(t *testing.T) {
	h := newHarness(t, nil, prodTestEnv, prodEnv, apiEnv)
	h.withShippedChain()

	h.r.Poll(context.Background())
	h.r.Poll(context.Background())

	src, hist := h.prod.calls()
	if src != 2 {
		t.Errorf("Sources calls = %d, want 2 (once per pipeline for the session)", src)
	}
	if hist != 4 {
		t.Errorf("History calls = %d, want 4 (once per pipeline per poll)", hist)
	}
	h.prod.mu.Lock()
	specs := h.prod.histSpecs[0]
	h.prod.mu.Unlock()
	if len(specs) != 2 || specs[0].Stage != "Test" || specs[0].ApprovalAction != "ApproveTest" || specs[1].Stage != "Production" {
		t.Errorf("app-pipeline specs = %+v, want Test (with its approval) then Production", specs)
	}
}

func TestOneFailingPipelineDoesNotStopTheAccount(t *testing.T) {
	h := newHarness(t, nil, testEnv, prodEnv, apiEnv)
	h.withShippedChain()
	h.prod.set(func(f *fakeDeployer) {
		f.histErrPipe = map[string]error{"app-pipeline": &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "no"}}
		f.hist = map[string]map[string][]model.Deploy{"api-pipeline": {"Test": {{ExecutionID: "e1", Status: model.DeploySucceeded,
			Revisions: map[string]string{"acme/api": "eeee555"}, FinishedAt: t0}}}}
	})

	snap := h.r.Poll(context.Background())

	h.prod.mu.Lock()
	n := len(h.prod.histSpecs)
	h.prod.mu.Unlock()
	if n != 2 {
		t.Errorf("History calls = %d, want 2 (api-pipeline still asked)", n)
	}
	if p := statusOf(t, snap, "aws prod-acct"); p.OK || !strings.Contains(p.Err, "AccessDenied") {
		t.Errorf("prod-acct status = %+v, want the AccessDenied error", p)
	}
}

// bareTestEnv is testEnv without repos: they come from the pipeline.
const bareTestEnv = `
[[envs]]
  account = "stage-acct"
  pipeline = "app-pipeline"
  stage = "Test"
  deploy_action = "Deploy"
`

func TestEnvReposFromSourcesOnlyWhenConfigHasNone(t *testing.T) {
	cases := []struct {
		name string
		env  string
		want []string
	}{
		{"config has none: discovered", bareTestEnv, []string{"acme/app", "acme/lib"}},
		{"config wins", testEnv, []string{"acme/app"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, nil, c.env)
			h.withShippedChain()
			h.stage.set(func(f *fakeDeployer) {
				f.sources = map[string]map[string]string{"app-pipeline": {"acme/lib": "LibSource", "acme/app": "AppSource"}}
			})

			snap := h.r.Poll(context.Background())

			if got := snap.Chains[0].Slots[0].Env.Repos; !slices.Equal(got, c.want) {
				t.Errorf("Env.Repos = %v, want %v", got, c.want)
			}
		})
	}
}

func TestWarningsDedupedAndLoggedOncePerPoll(t *testing.T) {
	var buf safeBuffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	h := newHarness(t, log, testEnv, apiEnv)
	h.withShippedChain()
	h.tracker.set(func(f *fakeTracker) { f.myWarn = []string{"jira: w1"} })
	// Both repos return the same warnings.
	h.host.set(func(f *fakeHost) { f.prWarn = []string{"jira: w1", "github: w2"} })
	h.stage.set(func(f *fakeDeployer) { f.histWarn = []string{"aws: w3", "aws: w3"} })

	snap := h.r.Poll(context.Background())

	want := []string{"jira: w1", "github: w2", "aws: w3"}
	if !slices.Equal(snap.Warnings, want) {
		t.Errorf("warnings = %q, want %q", snap.Warnings, want)
	}
	for _, w := range want {
		if n := strings.Count(buf.String(), w); n != 1 {
			t.Errorf("%q logged %d times in one poll, want 1\n%s", w, n, buf.String())
		}
	}
	h.r.Poll(context.Background())
	if n := strings.Count(buf.String(), "github: w2"); n != 2 {
		t.Errorf("w2 logged %d times over two polls, want 2", n)
	}
}

func TestSourceErrorIsLogged(t *testing.T) {
	var buf safeBuffer
	h := newHarness(t, slog.New(slog.NewTextHandler(&buf, nil)))
	h.withShippedChain()
	h.tracker.set(func(f *fakeTracker) { f.myErr = errors.New("jira: HTTP 502 Bad Gateway") })

	h.r.Poll(context.Background())

	if out := buf.String(); !strings.Contains(out, "source=jira") || !strings.Contains(out, "502") {
		t.Errorf("log = %q, want the jira error", out)
	}
}

func TestAccountWithoutClientIsAStatusError(t *testing.T) {
	h := newHarness(t, nil, testEnv, prodEnv)
	h.withShippedChain()
	r := resolver.New(loadConfig(t, testEnv, prodEnv), resolver.Adapters{
		Tracker: h.tracker, Code: h.host, AWS: map[string]resolver.Deployer{"stage-acct": h.stage},
	}, h.clock.Now, nil)

	snap := r.Poll(context.Background())

	if p := statusOf(t, snap, "aws prod-acct"); p.OK || !strings.Contains(p.Err, "no AWS client") {
		t.Errorf("prod-acct status = %+v, want a missing-client error", p)
	}
	if s := statusOf(t, snap, "aws stage-acct"); !s.OK {
		t.Errorf("stage-acct status = %+v, want OK", s)
	}
}
