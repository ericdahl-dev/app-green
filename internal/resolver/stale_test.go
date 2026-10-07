package resolver_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ericdahl-dev/app-green/internal/github"
	"github.com/ericdahl-dev/app-green/internal/model"
	"github.com/ericdahl-dev/app-green/internal/resolver"
)

func chainOf(t *testing.T, snap resolver.Snapshot, key string) model.Chain {
	t.Helper()
	for _, c := range snap.Chains {
		if c.Ticket.Key == key {
			return c
		}
	}
	t.Fatalf("no chain %s in %+v", key, snap.Chains)
	return model.Chain{}
}

// withAPIChain adds ABC-2 with a merged PR in acme/api (apiEnv's repo).
func (h *harness) withAPIChain() {
	api := mergedPR(2, "ABC-2: api", "eeee555")
	api.Repo = "acme/api"
	h.tracker.set(func(f *fakeTracker) { f.my = append(f.my, ticket("ABC-2")) })
	h.host.set(func(f *fakeHost) { f.prs["acme/api"] = []model.PR{api} })
}

func TestFailingRepoMarksItsChainsStale(t *testing.T) {
	h := newHarness(t, nil, testEnv, apiEnv)
	h.withShippedChain()
	h.withAPIChain()
	h.r.Poll(context.Background())

	h.clock.Advance(time.Minute)
	h.host.set(func(f *fakeHost) {
		f.prErrRepo = map[string]error{"acme/api": &github.APIError{Status: 502, Message: "Bad Gateway"}}
	})
	snap := h.r.Poll(context.Background())

	if c := chainOf(t, snap, "ABC-2"); !c.Stale || c.StaleReason == "" {
		t.Errorf("ABC-2 stale=%v reason=%q, want stale: acme/api failed", c.Stale, c.StaleReason)
	}
	if c := chainOf(t, snap, "ABC-1"); c.Stale {
		t.Errorf("ABC-1 stale (%q), want fresh: acme/app loaded", c.StaleReason)
	}
	if got := snap.RepoAt["acme/api"]; !got.Equal(t0) {
		t.Errorf("RepoAt[acme/api] = %v, want the last success %v", got, t0)
	}
	if got := snap.RepoAt["acme/app"]; !got.Equal(t0.Add(time.Minute)) {
		t.Errorf("RepoAt[acme/app] = %v, want this poll", got)
	}
}

func TestFailingEnvHistoryMarksChainsWhereItAppliesStale(t *testing.T) {
	h := newHarness(t, nil, testEnv, prodEnv, apiEnv)
	h.withShippedChain()
	h.withAPIChain()
	h.r.Poll(context.Background())

	h.clock.Advance(time.Minute)
	h.prod.set(func(f *fakeDeployer) {
		f.histErrPipe = map[string]error{"app-pipeline": errors.New("aws: HTTP 500")}
	})
	snap := h.r.Poll(context.Background())

	if c := chainOf(t, snap, "ABC-1"); !c.Stale || c.StaleReason == "" {
		t.Errorf("ABC-1 stale=%v reason=%q, want stale: prod-acct app-pipeline failed", c.Stale, c.StaleReason)
	}
	if c := chainOf(t, snap, "ABC-2"); c.Stale {
		t.Errorf("ABC-2 stale (%q), want fresh: its envs loaded", c.StaleReason)
	}
	prodID := "prod-acct/app-pipeline/Production"
	if got := snap.EnvAt[prodID]; !got.Equal(t0) {
		t.Errorf("EnvAt[%s] = %v, want the last success %v", prodID, got, t0)
	}
	if got := snap.EnvAt["stage-acct/app-pipeline/Test"]; !got.Equal(t0.Add(time.Minute)) {
		t.Errorf("EnvAt[Test] = %v, want this poll", got)
	}
}

func TestHealthyPollIsNotStale(t *testing.T) {
	h := newHarness(t, nil, ecsTestEnv, prodEnv, apiEnv)
	h.withShippedChain()
	h.withAPIChain()
	h.r.Poll(context.Background())
	h.clock.Advance(time.Minute)

	snap := h.r.Poll(context.Background())

	for _, c := range snap.Chains {
		if c.Stale {
			t.Errorf("%s stale (%q), want fresh after a clean poll", c.Ticket.Key, c.StaleReason)
		}
	}
	for k, at := range snap.EnvAt {
		if !at.Equal(t0.Add(time.Minute)) {
			t.Errorf("EnvAt[%s] = %v, want this poll", k, at)
		}
	}
}

func TestFailingEnvHealthMarksChainsStale(t *testing.T) {
	h := newHarness(t, nil, ecsTestEnv)
	h.withShippedChain()
	h.r.Poll(context.Background())
	h.clock.Advance(time.Minute)
	h.stage.set(func(f *fakeDeployer) { f.healthErr = errors.New("aws: DescribeServices c1: boom") })

	snap := h.r.Poll(context.Background())

	if c := chainOf(t, snap, "ABC-1"); !c.Stale {
		t.Errorf("ABC-1 not stale, want stale: Test's health failed")
	}
}

func TestNeverLoadedEnvMarksChainsStale(t *testing.T) {
	h := newHarness(t, nil, bareStageProd, bareProdProd)
	h.withShippedChain()
	h.prod.set(func(f *fakeDeployer) { f.histErr = errors.New("the SSO session has expired or is invalid") })

	snap := h.r.Poll(context.Background())

	c := chainOf(t, snap, "ABC-1")
	if want := "prod-acct/app-pipeline/Production never loaded"; !c.Stale || c.StaleReason != want {
		t.Errorf("stale=%v reason=%q, want %q", c.Stale, c.StaleReason, want)
	}
	if at, ok := snap.EnvAt["prod-acct/app-pipeline/Production"]; !ok || !at.IsZero() {
		t.Errorf("EnvAt[prod] = %v, %v; want present and zero", at, ok)
	}
}
