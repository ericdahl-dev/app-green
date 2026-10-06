package link

import "github.com/ericdahl-dev/app-green/internal/model"

// Slot decides where prs stand in one Env right now. Only merged PRs in a
// repo this Env deploys count; open and closed PRs never hold a slot back. A
// merged PR that is StackPending (not on the default branch yet) counts as
// SlotNotYet, so the chain is never ahead of its least advanced merged PR.
func Slot(prs []model.PR, h model.EnvHistory, cmp model.CompareFunc) model.EnvSlot {
	slot := model.EnvSlot{Env: h.Env, Health: h.Health, State: model.SlotNotYet}
	var results []model.EnvSlot
	for _, p := range prs {
		if p.State != model.PRMerged || !deploysRepo(h, p.Repo) {
			continue
		}
		if p.StackPending {
			// Merged, but not on the default branch yet: it holds the chain back.
			results = append(results, model.EnvSlot{Env: h.Env, State: model.SlotNotYet})
			continue
		}
		if p.EffectiveSHA == "" {
			continue
		}
		results = append(results, slotFor(p, h, cmp))
	}
	if len(results) == 0 {
		return slot
	}
	// The chain is only as far as its least advanced PR.
	worst := results[0]
	for _, r := range results[1:] {
		if rank(r.State) < rank(worst.State) {
			worst = r
		}
	}
	worst.Health = h.Health
	return worst
}

func deploysRepo(h model.EnvHistory, repo string) bool {
	for _, d := range h.Deploys {
		if _, ok := d.Revisions[repo]; ok {
			return true
		}
	}
	return false
}

// rank orders states from least to most advanced for "worst of" decisions.
func rank(s model.SlotState) int {
	return map[model.SlotState]int{
		model.SlotUnknown: 0, model.SlotFailed: 1, model.SlotNotYet: 2,
		model.SlotInProgress: 3, model.SlotAwaitingApproval: 4, model.SlotDeployed: 5,
	}[s]
}

// slotFor decides where one PR stands in h. "Deployed" means running now: the
// newest succeeded deploy carrying p.Repo (N) contains p.EffectiveSHA, either
// as its exact SHA or by compare. At is then the oldest exact-SHA success, so a
// redeploy of the same SHA does not reset it, or N's time when there is none.
// An older exact success does not count when N does not contain the PR (a
// rollback). Otherwise the newest non-success carrying the exact SHA decides
// (failed, in progress, awaiting approval); with none, NotYet, or Unknown when
// the compare could not answer.
func slotFor(p model.PR, h model.EnvHistory, cmp model.CompareFunc) model.EnvSlot {
	s := model.EnvSlot{Env: h.Env, SHA: p.EffectiveSHA}
	// 1. Scan for the exact SHA: the oldest success and the newest non-success.
	var pending, firstExact *model.Deploy
	for i := range h.Deploys {
		d := &h.Deploys[i]
		if d.Revisions[p.Repo] != p.EffectiveSHA {
			continue
		}
		if d.Status == model.DeploySucceeded {
			firstExact = d // newest first, so the last one seen is the oldest
		} else if pending == nil {
			pending = d
		}
	}
	// 2. Does N, the newest succeeded deploy carrying p.Repo, contain the PR?
	for i := range h.Deploys {
		d := &h.Deploys[i]
		if d.Status != model.DeploySucceeded || d.Revisions[p.Repo] == "" {
			continue
		}
		head := d.Revisions[p.Repo]
		if head == p.EffectiveSHA {
			// firstExact is set: d itself is an exact success.
			s.State, s.At, s.Deploy = model.SlotDeployed, firstExact.FinishedAt, d
			return s
		}
		switch cmp(p.Repo, p.EffectiveSHA, head) {
		case model.Included:
			s.State, s.At, s.SHA, s.Deploy = model.SlotDeployed, d.FinishedAt, head, d
			if firstExact != nil {
				s.At = firstExact.FinishedAt
			}
			return s
		case model.InclusionUnknown:
			if pending == nil {
				s.State = model.SlotUnknown
				return s
			}
		}
		break // only the newest success is compared
	}
	if pending != nil {
		s.Deploy, s.At = pending, pending.FinishedAt
		switch pending.Status {
		case model.DeployFailed:
			s.State = model.SlotFailed
		case model.DeployAwaitingApproval:
			s.State = model.SlotAwaitingApproval
		default:
			s.State = model.SlotInProgress
		}
		return s
	}
	s.State = model.SlotNotYet
	return s
}

// Slots fills c.Slots from histories, which are in Env.Order.
func Slots(c model.Chain, histories []model.EnvHistory, cmp model.CompareFunc) model.Chain {
	c.Slots = make([]model.EnvSlot, len(histories))
	for i, h := range histories {
		c.Slots[i] = Slot(c.PRs, h, cmp)
	}
	return c
}
