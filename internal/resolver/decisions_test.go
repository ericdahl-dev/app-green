package resolver_test

import (
	"context"
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

const slowPoll = `
[settings]
  poll_interval = "90s"
`

func TestAWSThrottleBackoffIsAtLeastPollInterval(t *testing.T) {
	h := newHarness(t, nil, testEnv, slowPoll)
	h.withShippedChain()
	h.stage.set(func(f *fakeDeployer) {
		f.histErr = &smithy.GenericAPIError{Code: "ThrottlingException", Message: "Rate exceeded"}
	})

	snap := h.r.Poll(context.Background())

	if s := statusOf(t, snap, "aws stage-acct"); !s.Throttled || !s.RetryAt.Equal(t0.Add(90*time.Second)) {
		t.Errorf("status = %+v, want throttled until poll time + 90s", s)
	}
}

func TestDiscoveredReposWatchedFromTheNextPoll(t *testing.T) {
	h := newHarness(t, nil, bareTestEnv)
	h.withShippedChain()
	infra := mergedPR(5, "ABC-1: infra", "ffff666")
	infra.Repo = "acme/infra"
	h.stage.set(func(f *fakeDeployer) {
		f.sources = map[string]map[string]string{"app-pipeline": {"acme/app": "AppSource", "acme/infra": "InfraSource"}}
	})
	h.host.set(func(f *fakeHost) { f.prs["acme/infra"] = []model.PR{infra} })

	h.r.Poll(context.Background())
	h.host.set(func(f *fakeHost) {
		if !slices.Equal(f.prRepos, []string{"acme/app"}) {
			t.Errorf("poll 1 RecentPRs repos = %v, want [acme/app]", f.prRepos)
		}
		f.prRepos = nil
	})

	snap := h.r.Poll(context.Background())
	h.host.set(func(f *fakeHost) {
		if !slices.Equal(f.prRepos, []string{"acme/app", "acme/infra"}) {
			t.Errorf("poll 2 RecentPRs repos = %v, want acme/app and the discovered acme/infra", f.prRepos)
		}
	})
	if len(snap.Chains) != 1 || len(snap.Chains[0].PRs) != 2 {
		t.Errorf("chains = %+v, want ABC-1 with both PRs", snap.Chains)
	}
}

func TestRejectedTokenStopsTheSource(t *testing.T) {
	cases := []struct {
		name, source string
		fail         func(h *harness)
		calls        func(h *harness) int
	}{
		{"jira", "jira",
			func(h *harness) { h.tracker.set(func(f *fakeTracker) { f.myErr = &jira.APIError{Status: 401} }) },
			func(h *harness) int { n, _ := h.tracker.calls(); return n }},
		{"github", "github",
			func(h *harness) {
				h.host.set(func(f *fakeHost) { f.prErr = &github.APIError{Status: 401, Message: "Bad credentials"} })
			},
			func(h *harness) int { h.host.mu.Lock(); defer h.host.mu.Unlock(); return h.host.prCalls }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, nil)
			h.withShippedChain()
			c.fail(h)

			snap := h.r.Poll(context.Background())
			if s := statusOf(t, snap, c.source); s.OK || !s.Auth || s.Err != "token rejected" {
				t.Fatalf("status = %+v, want Auth with Err %q", s, "token rejected")
			}
			if _, byKey := h.tracker.calls(); c.source == "jira" && len(byKey) != 0 {
				t.Errorf("TicketsByKey called %v after a rejected token", byKey)
			}
			before := c.calls(h)
			h.clock.Advance(time.Hour)
			snap = h.r.Poll(context.Background())
			if got := c.calls(h); got != before {
				t.Errorf("calls after auth stop = %d, want %d (not called until refresh)", got, before)
			}
			if s := statusOf(t, snap, c.source); !s.Auth {
				t.Errorf("status = %+v, want still Auth", s)
			}
			if s := statusOf(t, snap, "aws stage-acct"); !s.OK {
				t.Errorf("aws status = %+v, want other sources still polled", s)
			}
		})
	}
}

func TestRefreshRetriesARejectedTokenOnce(t *testing.T) {
	h := newHarness(t, nil)
	h.withShippedChain()
	h.tracker.set(func(f *fakeTracker) { f.myErr = &jira.APIError{Status: 401} })
	resolver.SetInterval(h.r, 10*time.Millisecond)
	out, refresh, _ := start(t, h)

	if s := statusOf(t, next(t, out), "jira"); !s.Auth {
		t.Fatalf("jira status = %+v, want Auth", s)
	}
	next(t, out) // interval polls skip jira
	if n, _ := h.tracker.calls(); n != 1 {
		t.Fatalf("MyTickets calls = %d, want 1 before refresh", n)
	}

	// Still rejected: refresh retries exactly once, then it stays stopped.
	refresh <- struct{}{}
	waitFor(t, func() bool { n, _ := h.tracker.calls(); return n == 2 })
	for range 3 {
		next(t, out)
	}
	if n, _ := h.tracker.calls(); n != 2 {
		t.Errorf("MyTickets calls = %d, want 2 (one retry per refresh)", n)
	}

	// Fixed token: the next refresh brings jira back.
	h.tracker.set(func(f *fakeTracker) { f.myErr = nil })
	refresh <- struct{}{}
	waitFor(t, func() bool { n, _ := h.tracker.calls(); return n == 3 })
	var s resolver.AdapterStatus
	waitFor(t, func() bool { s = statusOf(t, next(t, out), "jira"); return s.OK })
	if s.Auth {
		t.Errorf("jira status = %+v, want Auth cleared", s)
	}
}

func TestRateLimitedCompareBacksOffGitHub(t *testing.T) {
	h := newHarness(t, nil)
	h.withCompareChain(model.InclusionUnknown, &github.APIError{Status: 429, RetryAfter: 2 * time.Minute})
	h.tracker.set(func(f *fakeTracker) { f.my = []model.Ticket{ticket("ABC-1"), ticket("ABC-2")} })
	h.host.set(func(f *fakeHost) {
		f.prs = map[string][]model.PR{repo: {mergedPR(1, "ABC-1: x", "aaaa111"), mergedPR(2, "ABC-2: y", "bbbb222")}}
	})

	snap := h.r.Poll(context.Background())

	if n := len(h.host.compares()); n != 1 {
		t.Errorf("compare calls = %d, want 1 (the rest of the poll asks nothing)", n)
	}
	var cmpWarn int
	for _, w := range snap.Warnings {
		if strings.HasPrefix(w, "compare ") {
			cmpWarn++
		}
	}
	if cmpWarn != 1 {
		t.Errorf("compare warnings = %q, want exactly one", snap.Warnings)
	}
	g := statusOf(t, snap, "github")
	if !g.Throttled || !g.RetryAt.Equal(t0.Add(2*time.Minute)) {
		t.Fatalf("github status = %+v, want throttled until +2m", g)
	}

	h.host.set(func(f *fakeHost) {
		f.compare = func(string, string, string) (model.Inclusion, error) { return model.Included, nil }
	})
	prCalls := func() int { h.host.mu.Lock(); defer h.host.mu.Unlock(); return h.host.prCalls }
	before := prCalls()
	h.clock.Advance(time.Minute)
	h.r.Poll(context.Background())
	if prCalls() != before || len(h.host.compares()) != 1 {
		t.Errorf("before RetryAt: RecentPRs calls %d -> %d, compares %d; want GitHub left alone", before, prCalls(), len(h.host.compares()))
	}

	h.clock.Advance(time.Minute)
	snap = h.r.Poll(context.Background())
	if prCalls() == before || len(h.host.compares()) != 3 {
		t.Errorf("at RetryAt: RecentPRs calls %d, compares %d; want both resumed", prCalls(), len(h.host.compares()))
	}
	if g := statusOf(t, snap, "github"); !g.OK || g.Throttled {
		t.Errorf("github status = %+v, want OK", g)
	}
}
