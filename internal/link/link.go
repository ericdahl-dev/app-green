package link

import "github.com/ericdahl-dev/app-green/internal/model"

// Link groups prs under the tickets whose keys they name, in ticket order.
// A PR that names no configured-project key is returned as unlinked. A PR
// whose keys are all outside tickets is dropped: it is not my work.
// Closed (unmerged) PRs are dropped.
func Link(tickets []model.Ticket, prs []model.PR, projects []string) ([]model.Chain, []model.PR) {
	prs = withEffectiveSHAs(prs)
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
// default branch.
func withEffectiveSHAs(prs []model.PR) []model.PR {
	type rk struct{ repo, head string }
	byHead := map[rk]model.PR{}
	for _, p := range prs {
		byHead[rk{p.Repo, p.HeadRef}] = p
	}
	out := make([]model.PR, len(prs))
	for i, p := range prs {
		out[i] = p
		if p.State != model.PRMerged {
			continue
		}
		cur, seen := p, map[int]bool{}
		for cur.BaseRef != cur.DefaultBranch {
			next, ok := byHead[rk{cur.Repo, cur.BaseRef}]
			if !ok || seen[next.Number] {
				break // base branch has no PR (or a cycle): treat as pending
			}
			seen[next.Number] = true
			cur = next
		}
		switch {
		case cur.BaseRef == cur.DefaultBranch && cur.State == model.PRMerged:
			out[i].EffectiveSHA = cur.MergeSHA
		default:
			out[i].StackPending = true
		}
	}
	return out
}
