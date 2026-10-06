package link_test

import (
	"testing"

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
	chains, unlinked := link.Link(tickets, prs, []string{"ABC"})
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

func TestLinkKeyWithoutTicketIsUnlinked(t *testing.T) {
	// The PR names a key that is not one of my tickets (someone else's, or Done long ago).
	chains, unlinked := link.Link(nil, []model.PR{pr(5, "ABC-99: x", "abc-99", "main", model.PROpen, "")}, []string{"ABC"})
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
	chains, _ := link.Link([]model.Ticket{{Key: "ABC-5"}}, prs, []string{"ABC"})
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
	chains, _ := link.Link([]model.Ticket{{Key: "ABC-6"}}, prs, []string{"ABC"})
	got := chains[0].PRs[0] // #2
	if got.Number != 2 || got.EffectiveSHA != "" || !got.StackPending {
		t.Errorf("#2 = %+v, want pending with no EffectiveSHA", got)
	}
}

func TestLinkStackEdgeCases(t *testing.T) {
	prs := []model.PR{
		// Base branch has no PR: pending.
		pr(1, "ABC-7: orphan", "o1", "gone", model.PRMerged, "eeee555"),
		// Cycle between two stack branches: pending, and it terminates.
		pr(2, "ABC-7: loop a", "la", "lb", model.PRMerged, "ffff666"),
		pr(3, "ABC-7: loop b", "lb", "la", model.PRMerged, "ffff666"),
		// Open PR: neither set.
		pr(4, "ABC-7: open", "op", "main", model.PROpen, ""),
	}
	chains, _ := link.Link([]model.Ticket{{Key: "ABC-7"}}, prs, []string{"ABC"})
	for _, p := range chains[0].PRs {
		wantPending := p.State == model.PRMerged
		if p.EffectiveSHA != "" || p.StackPending != wantPending {
			t.Errorf("#%d EffectiveSHA=%q pending=%v, want \"\"/%v", p.Number, p.EffectiveSHA, p.StackPending, wantPending)
		}
	}
}
