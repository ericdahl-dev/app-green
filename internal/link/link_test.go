package link_test

import (
	"testing"
	"time"

	"github.com/ericdahl-dev/app-green/internal/link"
	"github.com/ericdahl-dev/app-green/internal/model"
)

func pr(n int, title, head, base string, st model.PRState, sha string) model.PR {
	return model.PR{Repo: "acme/app", DefaultBranch: "main", Number: n, Title: title,
		HeadRef: head, BaseRef: base, State: st, MergeSHA: sha}
}

func TestLinkGroupsPRsByKey(t *testing.T) {
	tickets := []model.Ticket{{Key: "ABC-1"}, {Key: "ABC-2"}}
	prs := []model.PR{
		pr(10, "ABC-1: first", "abc-1-a", "main", model.PRMerged, "aaaa111"),
		pr(11, "ABC-1: follow-up", "abc-1-b", "main", model.PROpen, ""),
		pr(12, "Bump deps", "dependabot/x", "main", model.PROpen, ""),
	}
	chains, unlinked := link.Link(tickets, prs, nil, []string{"ABC"})
	if len(chains) != 2 || len(chains[0].PRs) != 2 || len(chains[1].PRs) != 0 {
		t.Fatalf("chains = %+v", chains)
	}
	if chains[0].PRs[0].EffectiveSHA != "aaaa111" {
		t.Errorf("merged into main: EffectiveSHA = %q", chains[0].PRs[0].EffectiveSHA)
	}
	if len(unlinked) != 1 || unlinked[0].Number != 12 {
		t.Errorf("unlinked = %+v", unlinked)
	}
}

func TestLinkKeyWithoutTicketIsDropped(t *testing.T) {
	// The PR names a key that is not one of my tickets (someone else's, or Done long ago).
	chains, unlinked := link.Link(nil, []model.PR{pr(5, "ABC-99: x", "abc-99", "main", model.PROpen, "")}, nil, []string{"ABC"})
	if len(chains) != 0 || len(unlinked) != 0 {
		t.Errorf("a PR for a ticket outside my rows is ignored, not unlinked: %v %v", chains, unlinked)
	}
}

func TestLinkStackedPRs(t *testing.T) {
	// main <- s1 (#1) <- s2 (#2) <- s3 (#3). #2 and #3 merged into their bases;
	// #1 merged into main with sha "bbbb222". All three name ABC-5.
	prs := []model.PR{
		pr(3, "[3/3] ABC-5: c", "s3", "s2", model.PRMerged, "cccc333"),
		pr(2, "[2/3] ABC-5: b", "s2", "s1", model.PRMerged, "cccc333"),
		pr(1, "[1/3] ABC-5: a", "s1", "main", model.PRMerged, "bbbb222"),
	}
	chains, _ := link.Link([]model.Ticket{{Key: "ABC-5"}}, prs, nil, []string{"ABC"})
	for _, p := range chains[0].PRs {
		if p.EffectiveSHA != "bbbb222" || p.StackPending {
			t.Errorf("#%d EffectiveSHA=%q pending=%v, want bbbb222/false", p.Number, p.EffectiveSHA, p.StackPending)
		}
	}
}

func TestLinkStackNotYetOnMain(t *testing.T) {
	prs := []model.PR{
		pr(2, "ABC-6: b", "s2", "s1", model.PRMerged, "dddd444"),
		pr(1, "ABC-6: a", "s1", "main", model.PROpen, ""),
	}
	chains, _ := link.Link([]model.Ticket{{Key: "ABC-6"}}, prs, nil, []string{"ABC"})
	got := chains[0].PRs[0] // #2
	if got.Number != 2 || got.EffectiveSHA != "" || !got.StackPending {
		t.Errorf("#2 = %+v, want pending with no EffectiveSHA", got)
	}
}

func TestLinkStackEdgeCases(t *testing.T) {
	prs := []model.PR{
		// Base branch has no PR: pending and stranded.
		pr(1, "ABC-7: orphan", "o1", "gone", model.PRMerged, "eeee555"),
		// Cycle between two stack branches: pending (not stranded), and it terminates.
		pr(2, "ABC-7: loop a", "la", "lb", model.PRMerged, "ffff666"),
		pr(3, "ABC-7: loop b", "lb", "la", model.PRMerged, "ffff666"),
		// Open PR: neither set.
		pr(4, "ABC-7: open", "op", "main", model.PROpen, ""),
	}
	chains, _ := link.Link([]model.Ticket{{Key: "ABC-7"}}, prs, nil, []string{"ABC"})
	for _, p := range chains[0].PRs {
		wantPending, wantStranded := p.State == model.PRMerged, p.Number == 1
		if p.EffectiveSHA != "" || p.StackPending != wantPending || p.Stranded != wantStranded {
			t.Errorf("#%d EffectiveSHA=%q pending=%v stranded=%v, want \"\"/%v/%v", p.Number, p.EffectiveSHA, p.StackPending, p.Stranded, wantPending, wantStranded)
		}
	}
}

func TestLinkSharedHeadPrefersMerged(t *testing.T) {
	// Head "s1" has #1 merged into main and #9 open (reopened on the same branch).
	one := pr(1, "ABC-8: a", "s1", "main", model.PRMerged, "aaaa111")
	nine := pr(9, "ABC-8: a again", "s1", "main", model.PROpen, "")
	two := pr(2, "ABC-8: b", "s2", "s1", model.PRMerged, "bbbb222")
	for _, prs := range [][]model.PR{{one, nine, two}, {nine, one, two}, {two, nine, one}} {
		chains, _ := link.Link([]model.Ticket{{Key: "ABC-8"}}, prs, nil, []string{"ABC"})
		for _, p := range chains[0].PRs {
			if p.Number == 2 && (p.EffectiveSHA != "aaaa111" || p.StackPending) {
				t.Errorf("order %d,%d,%d: #2 EffectiveSHA=%q pending=%v, want aaaa111/false",
					prs[0].Number, prs[1].Number, prs[2].Number, p.EffectiveSHA, p.StackPending)
			}
		}
	}
}

func TestLinkSharedHeadIgnoresClosed(t *testing.T) {
	one := pr(1, "ABC-8: a", "s1", "main", model.PRMerged, "aaaa111")
	closed := pr(9, "ABC-8: a old", "s1", "main", model.PRClosed, "")
	two := pr(2, "ABC-8: b", "s2", "s1", model.PRMerged, "bbbb222")
	chains, _ := link.Link([]model.Ticket{{Key: "ABC-8"}}, []model.PR{one, two, closed}, nil, []string{"ABC"})
	for _, p := range chains[0].PRs {
		if p.Number == 2 && (p.EffectiveSHA != "aaaa111" || p.StackPending) {
			t.Errorf("#2 EffectiveSHA=%q pending=%v, want aaaa111/false", p.EffectiveSHA, p.StackPending)
		}
	}
}

func TestLinkMergedIntoStackAfterBaseMerged(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name         string
		twoAt        time.Time
		wantSHA      string
		wantPending  bool
		wantStranded bool
	}{
		{"normal order", t0, "aaaa111", false, false},
		{"stranded after base merged", t0.Add(2 * time.Hour), "", true, true},
	}
	for _, c := range cases {
		one := pr(1, "ABC-9: a", "s1", "main", model.PRMerged, "aaaa111")
		one.MergedAt = t0.Add(time.Hour)
		two := pr(2, "ABC-9: b", "s2", "s1", model.PRMerged, "bbbb222")
		two.MergedAt = c.twoAt
		chains, _ := link.Link([]model.Ticket{{Key: "ABC-9"}}, []model.PR{two, one}, nil, []string{"ABC"})
		got := chains[0].PRs[0]
		if got.Number != 2 || got.EffectiveSHA != c.wantSHA || got.StackPending != c.wantPending || got.Stranded != c.wantStranded {
			t.Errorf("%s: #%d EffectiveSHA=%q pending=%v stranded=%v, want %q/%v/%v",
				c.name, got.Number, got.EffectiveSHA, got.StackPending, got.Stranded, c.wantSHA, c.wantPending, c.wantStranded)
		}
	}
}

func TestLinkStrandedWhenBaseBranchHasNoPR(t *testing.T) {
	// #1 merged into "gone", which no PR (open or merged) will ever bring to main.
	prs := []model.PR{pr(1, "ABC-10: a", "o1", "gone", model.PRMerged, "eeee555")}
	chains, _ := link.Link([]model.Ticket{{Key: "ABC-10"}}, prs, nil, []string{"ABC"})
	if got := chains[0].PRs[0]; !got.Stranded || !got.StackPending || got.EffectiveSHA != "" {
		t.Errorf("#1 = %+v, want stranded and pending with no EffectiveSHA", got)
	}
}

func TestLinkWaitingOnOpenBaseIsNotStranded(t *testing.T) {
	prs := []model.PR{
		pr(2, "ABC-11: b", "s2", "s1", model.PRMerged, "dddd444"),
		pr(1, "ABC-11: a", "s1", "main", model.PROpen, ""),
	}
	chains, _ := link.Link([]model.Ticket{{Key: "ABC-11"}}, prs, nil, []string{"ABC"})
	if got := chains[0].PRs[0]; got.Number != 2 || got.Stranded || !got.StackPending {
		t.Errorf("#2 = %+v, want pending but not stranded: its base PR is still open", got)
	}
}

func TestLinkContextPRsOnlyFeedStackWalking(t *testing.T) {
	t1 := time.Date(2026, 9, 21, 9, 0, 0, 0, time.UTC)
	mine := []model.PR{pr(2, "ABC-12: on someone's branch", "abc-12", "s1", model.PRMerged, "bbbb222")}
	mine[0].MergedAt = t1
	base := pr(1, "ABC-12: their base names my key", "s1", "main", model.PRMerged, "aaaa111")
	base.MergedAt = t1.Add(time.Hour)
	noKey := pr(3, "Refactor", "s9", "main", model.PROpen, "")

	chains, unlinked := link.Link([]model.Ticket{{Key: "ABC-12"}}, mine, []model.PR{base, noKey}, []string{"ABC"})
	if len(chains) != 1 || len(chains[0].PRs) != 1 || chains[0].PRs[0].Number != 2 {
		t.Fatalf("chains = %+v, want only my #2", chains)
	}
	if len(unlinked) != 0 {
		t.Errorf("unlinked = %+v, want context PRs left out", unlinked)
	}
	if p := chains[0].PRs[0]; p.EffectiveSHA != "aaaa111" || p.StackPending || p.Stranded {
		t.Errorf("#2 = %+v, want carried to main by context #1's aaaa111", p)
	}

	// Without the context PR, #2's base has no PR: stranded.
	chains, _ = link.Link([]model.Ticket{{Key: "ABC-12"}}, mine, nil, []string{"ABC"})
	if !chains[0].PRs[0].Stranded {
		t.Errorf("without context: #2 = %+v, want Stranded", chains[0].PRs[0])
	}
}
