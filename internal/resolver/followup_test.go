package resolver_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ericdahl-dev/app-green/internal/github"
	"github.com/ericdahl-dev/app-green/internal/jira"
	"github.com/ericdahl-dev/app-green/internal/resolver"
)

// A source skipped for backoff loads nothing, so rows that depend on it stay
// stale for the whole window.
func TestSkippedSourceKeepsRowsStaleForTheWindow(t *testing.T) {
	h := newHarness(t, nil)
	h.withShippedChain()
	h.r.Poll(context.Background())
	h.clock.Advance(time.Minute)
	h.host.set(func(f *fakeHost) { f.prErr = &github.APIError{Status: 429, RetryAfter: 2 * time.Minute} })
	h.r.Poll(context.Background())
	h.host.set(func(f *fakeHost) { f.prErr = nil })

	h.clock.Advance(time.Minute) // inside the window: GitHub is skipped
	snap := h.r.Poll(context.Background())

	if c := chainOf(t, snap, "ABC-1"); !c.Stale {
		t.Errorf("ABC-1 not stale while GitHub is skipped, want stale")
	}
}

// A chain with no PRs cannot tell whether a PR exists when GitHub did not
// load, so it is stale then, and fresh after a clean poll.
func TestNoPRChainStaleWhenGitHubDidNotLoad(t *testing.T) {
	h := newHarness(t, nil)
	h.withShippedChain()
	h.tracker.set(func(f *fakeTracker) { f.my = append(f.my, ticket("ABC-7")) })

	snap := h.r.Poll(context.Background())
	if c := chainOf(t, snap, "ABC-7"); c.Stale {
		t.Errorf("ABC-7 stale (%q) after a clean poll, want fresh", c.StaleReason)
	}

	h.clock.Advance(time.Minute)
	h.host.set(func(f *fakeHost) { f.prErr = &github.APIError{Status: 502, Message: "Bad Gateway"} })
	snap = h.r.Poll(context.Background())
	if c := chainOf(t, snap, "ABC-7"); !c.Stale || c.StaleReason != "GitHub not refreshed" {
		t.Errorf("ABC-7 stale=%v reason=%q, want stale: GitHub not refreshed", c.Stale, c.StaleReason)
	}
}

func TestRerunRefusesACheckWithoutARun(t *testing.T) {
	h := newHarness(t, nil)
	for _, id := range []int64{0, -1} {
		err := h.r.Rerun(context.Background(), "acme/app", id)
		if err == nil || !strings.Contains(err.Error(), "check has no GitHub Actions run to re-run") {
			t.Errorf("Rerun(%d) = %v, want the no-run refusal", id, err)
		}
	}
	if got := h.host.reruns(); len(got) != 0 {
		t.Errorf("RerunFailedJobs calls = %+v, want none", got)
	}
}

// Only ordinary lookup failures are warnings: a rejected token or a rate
// limit from TicketsByKey still stops or backs off Jira.
func TestTicketsByKeyAuthAndRateLimitStillStopJira(t *testing.T) {
	cases := []struct {
		name  string
		err   error
		check func(s resolver.AdapterStatus) bool
	}{
		{"401", &jira.APIError{Status: 401}, func(s resolver.AdapterStatus) bool { return s.Auth }},
		{"429", &jira.APIError{Status: 429, RetryAfter: time.Minute}, func(s resolver.AdapterStatus) bool {
			return s.Throttled && s.RetryAt.Equal(t0.Add(time.Minute))
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, nil)
			h.withShippedChain()
			h.tracker.set(func(f *fakeTracker) { f.byKeyErr = c.err })
			h.host.set(func(f *fakeHost) { f.prs[repo] = append(f.prs[repo], openPR(9, "ABC-9: x")) })

			snap := h.r.Poll(context.Background())

			if s := statusOf(t, snap, "jira"); s.OK || !c.check(s) {
				t.Errorf("jira status = %+v, want stopped or backed off", s)
			}
		})
	}
}

// After a refresh, a pipeline whose Sources call fails still has its
// history read, with the sources it had, and says so in a warning.
func TestFailedRediscoveryFallsBackToCachedSources(t *testing.T) {
	h := newHarness(t, nil) // default 60s interval: only refresh polls again
	h.withShippedChain()
	out, refresh, _ := start(t, h)
	next(t, out)
	h.stage.set(func(f *fakeDeployer) { f.sourcesErr = errors.New("aws: HTTP 500") })

	refresh <- struct{}{}
	snap := next(t, out)

	if src, hist := h.stage.calls(); src != 2 || hist != 2 {
		t.Errorf("Sources/History calls = %d/%d, want 2/2 (history read with the cached sources)", src, hist)
	}
	if !slices.ContainsFunc(snap.Warnings, func(w string) bool { return strings.Contains(w, "cached sources") && strings.Contains(w, "500") }) {
		t.Errorf("warnings = %q, want one saying the cached sources were used", snap.Warnings)
	}
	if c := chainOf(t, snap, "ABC-1"); c.Stale {
		t.Errorf("ABC-1 stale (%q), want fresh: its history loaded", c.StaleReason)
	}
}
