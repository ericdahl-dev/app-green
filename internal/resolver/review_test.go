package resolver_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/ericdahl-dev/app-green/internal/github"
	"github.com/ericdahl-dev/app-green/internal/jira"
	"github.com/ericdahl-dev/app-green/internal/model"
)

func TestRefreshRediscoversSources(t *testing.T) {
	h := newHarness(t, nil, bareTestEnv) // default 60s interval: only refresh polls again
	h.withShippedChain()
	out, refresh, _ := start(t, h)
	next(t, out)

	// The pipeline gains a source while the app runs.
	h.stage.set(func(f *fakeDeployer) {
		f.sources = map[string]map[string]string{"app-pipeline": {"acme/app": "AppSource", "acme/infra": "InfraSource"}}
	})
	refresh <- struct{}{}
	snap := next(t, out)

	if n, _ := h.stage.calls(); n != 2 {
		t.Errorf("Sources calls = %d, want 2 (asked again after refresh)", n)
	}
	if got := snap.Chains[0].Slots[0].Env.Repos; !slices.Equal(got, []string{"acme/app", "acme/infra"}) {
		t.Errorf("Env.Repos = %v, want the new source too", got)
	}
	h.host.set(func(f *fakeHost) { f.prRepos = nil; f.prs["acme/infra"] = []model.PR{} })
	refresh <- struct{}{}
	next(t, out)
	h.host.set(func(f *fakeHost) {
		if !slices.Contains(f.prRepos, "acme/infra") {
			t.Errorf("RecentPRs repos = %v, want acme/infra watched", f.prRepos)
		}
	})
}

func TestNoExtraTicketsWhenMyTicketsFailed(t *testing.T) {
	h := newHarness(t, nil)
	h.withShippedChain()
	h.tracker.set(func(f *fakeTracker) {
		f.byKey = map[string]model.Ticket{"ABC-9": ticket("ABC-9")}
		f.myErr = errors.New("jira: HTTP 502 Bad Gateway")
	})
	h.host.set(func(f *fakeHost) { f.prs[repo] = append(f.prs[repo], openPR(9, "ABC-9: x")) })

	h.r.Poll(context.Background())

	if _, byKey := h.tracker.calls(); len(byKey) != 0 {
		t.Errorf("TicketsByKey calls = %v, want none after MyTickets failed", byKey)
	}
}

func TestRejectedTokenDuringCompareStopsGitHub(t *testing.T) {
	h := newHarness(t, nil)
	h.withCompareChain(model.InclusionUnknown, &github.APIError{Status: 401, Message: "Bad credentials"})
	h.tracker.set(func(f *fakeTracker) { f.my = []model.Ticket{ticket("ABC-1"), ticket("ABC-2")} })
	h.host.set(func(f *fakeHost) {
		f.prs = map[string][]model.PR{repo: {mergedPR(1, "ABC-1: x", "aaaa111"), mergedPR(2, "ABC-2: y", "bbbb222")}}
	})

	snap := h.r.Poll(context.Background())

	if n := len(h.host.compares()); n != 1 {
		t.Errorf("compare calls = %d, want 1 (the rest of the poll asks nothing)", n)
	}
	if g := statusOf(t, snap, "github"); g.OK || !g.Auth || g.Err != "token rejected" {
		t.Fatalf("github status = %+v, want Auth with Err %q", g, "token rejected")
	}
	prCalls := func() int { h.host.mu.Lock(); defer h.host.mu.Unlock(); return h.host.prCalls }
	before := prCalls()
	h.clock.Advance(time.Hour)
	h.r.Poll(context.Background())
	if prCalls() != before || len(h.host.compares()) != 1 {
		t.Errorf("after the 401: RecentPRs calls %d -> %d, compares %d; want GitHub stopped until refresh", before, prCalls(), len(h.host.compares()))
	}
}

// Retry-After counts from when the error came back, not from the poll's
// start: a slow poll must not eat into it.
func TestRetryAfterFromRecordTime(t *testing.T) {
	const slow = 20 * time.Second
	rateLimited := func() error { return &github.APIError{Status: 429, RetryAfter: 2 * time.Minute} }
	cases := []struct {
		name, source string
		fail         func(h *harness)
	}{
		{"jira", "jira", func(h *harness) {
			h.tracker.set(func(f *fakeTracker) { f.myErr = &jira.APIError{Status: 429, RetryAfter: 2 * time.Minute} })
			h.stage.set(func(f *fakeDeployer) { f.during = func() { h.clock.Advance(slow) } })
		}},
		{"github", "github", func(h *harness) {
			h.host.set(func(f *fakeHost) { f.prErr = rateLimited() })
			h.stage.set(func(f *fakeDeployer) { f.during = func() { h.clock.Advance(slow) } })
		}},
		{"compare", "github", func(h *harness) {
			h.withCompareChain(model.InclusionUnknown, nil)
			h.host.set(func(f *fakeHost) {
				f.compare = func(string, string, string) (model.Inclusion, error) {
					h.clock.Advance(slow)
					return model.InclusionUnknown, rateLimited()
				}
			})
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, nil)
			h.withShippedChain()
			c.fail(h)

			snap := h.r.Poll(context.Background())

			if s, want := statusOf(t, snap, c.source), t0.Add(slow+2*time.Minute); !s.RetryAt.Equal(want) {
				t.Errorf("RetryAt = %v, want %v", s.RetryAt, want)
			}
		})
	}
}

// A compare answer nobody asked for in 50 polls is dropped, so the cache
// does not grow for the whole session.
func TestCompareCacheDropsKeysUnusedFor50Polls(t *testing.T) {
	h := newHarness(t, nil)
	h.withCompareChain(model.Included, nil)
	h.r.Poll(context.Background())
	idle := func(polls int) {
		h.tracker.set(func(f *fakeTracker) { f.my = nil }) // no chain, no compare
		for range polls {
			h.r.Poll(context.Background())
		}
		h.tracker.set(func(f *fakeTracker) { f.my = []model.Ticket{ticket("ABC-1")} })
		h.r.Poll(context.Background())
	}

	idle(49)
	if n := len(h.host.compares()); n != 1 {
		t.Errorf("compare calls after 49 idle polls = %d, want 1 (still cached)", n)
	}
	idle(50)
	if n := len(h.host.compares()); n != 2 {
		t.Errorf("compare calls after 50 idle polls = %d, want 2 (dropped, asked again)", n)
	}
}
