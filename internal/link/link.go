package link

import (
	"slices"

	"github.com/ericdahl-dev/app-green/internal/model"
)

// Link groups prs under the tickets whose keys they name, in ticket order.
// A PR that names one of tickets links to it, even if it also names keys
// outside tickets. A PR that names no configured-project key is returned as
// unlinked. A PR whose keys are all outside tickets is dropped: it is not my
// work. Closed (unmerged) PRs are ignored everywhere, including stack walking.
//
// context holds PRs that matter only as stack bases: the PR for each base
// branch of prs, whoever wrote it (github.Client.BasePRs). They are used for
// stack walking alone, never linked to a ticket and never returned as
// unlinked, so another author's PR naming my key does not join my row.
func Link(tickets []model.Ticket, prs, context []model.PR, projects []string) ([]model.Chain, []model.PR) {
	prs = withEffectiveSHAs(slices.Concat(prs, context))[:len(prs)]
	byKey := map[string][]model.PR{}
	var unlinked []model.PR
	for _, p := range prs {
		if p.State == model.PRClosed {
			continue
		}
		keys := KeysIn(projects, p.Title, p.HeadRef)
		if len(keys) == 0 {
			unlinked = append(unlinked, p)
			continue
		}
		for _, k := range keys {
			byKey[k] = append(byKey[k], p)
		}
	}
	chains := make([]model.Chain, 0, len(tickets))
	for _, t := range tickets {
		chains = append(chains, model.Chain{Ticket: t, PRs: byKey[t.Key]})
	}
	return chains, unlinked
}

// withEffectiveSHAs sets EffectiveSHA and StackPending on every merged PR by
// following BaseRef through other PRs' HeadRefs until it reaches the repo's
// default branch. Closed PRs are not followed, and when two PRs share a head
// branch the merged one is followed. A PR merged into a stack branch after
// that branch's own PR merged, or into a branch with no live PR (none, or only
// closed ones), is Stranded: StackPending too, no EffectiveSHA. A PR waiting
// on an open base PR, or caught in a cycle, is StackPending only.
func withEffectiveSHAs(prs []model.PR) []model.PR {
	type rk struct{ repo, head string }
	byHead := map[rk]model.PR{}
	for _, p := range prs {
		if p.State == model.PRClosed {
			continue
		}
		k := rk{p.Repo, p.HeadRef}
		if old, ok := byHead[k]; ok && old.State == model.PRMerged && p.State != model.PRMerged {
			continue // a merged PR wins, whatever the input order
		}
		byHead[k] = p
	}
	out := make([]model.PR, len(prs))
	for i, p := range prs {
		out[i] = p
		if p.State != model.PRMerged {
			continue
		}
		cur, seen, stranded := p, map[int]bool{p.Number: true}, false
		for cur.BaseRef != cur.DefaultBranch {
			next, ok := byHead[rk{cur.Repo, cur.BaseRef}]
			if !ok {
				stranded = true // base branch has no live PR: nothing brings it to main
				break
			}
			if seen[next.Number] {
				break // a cycle: treat as pending
			}
			if cur.State == model.PRMerged && next.State == model.PRMerged &&
				!cur.MergedAt.IsZero() && !next.MergedAt.IsZero() && cur.MergedAt.After(next.MergedAt) {
				stranded = true // merged into a branch whose PR had already merged
				break
			}
			seen[next.Number] = true
			cur = next
		}
		switch {
		case !stranded && cur.BaseRef == cur.DefaultBranch && cur.State == model.PRMerged:
			out[i].EffectiveSHA = cur.MergeSHA
		default:
			out[i].StackPending = true
			out[i].Stranded = stranded
		}
	}
	return out
}
