package link

import "github.com/ericdahl-dev/app-green/internal/model"

// Slot decides where prs stand in one Env. Only merged PRs with an
// EffectiveSHA in a repo this Env deploys count.
func Slot(prs []model.PR, h model.EnvHistory, cmp model.CompareFunc) model.EnvSlot {
	slot := model.EnvSlot{Env: h.Env, Health: h.Health, State: model.SlotNotYet}
	var results []model.EnvSlot
	for _, p := range prs {
		if p.State != model.PRMerged || p.EffectiveSHA == "" || !deploysRepo(h, p.Repo) {
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

func slotFor(p model.PR, h model.EnvHistory, cmp model.CompareFunc) model.EnvSlot {
	s := model.EnvSlot{Env: h.Env, SHA: p.EffectiveSHA}
	// 1. Exact match, newest first. A success wins; otherwise the newest
	//    non-success carrying the SHA decides (failed, in progress, approval).
	var pending *model.Deploy
	for i := range h.Deploys {
		d := &h.Deploys[i]
		if d.Revisions[p.Repo] != p.EffectiveSHA {
			continue
		}
		if d.Status == model.DeploySucceeded {
			s.State, s.At, s.Deploy = model.SlotDeployed, d.FinishedAt, d
			return s
		}
		if pending == nil {
			pending = d
		}
	}
	// 2. Compare against the newest succeeded deploy that carries p.Repo.
	for i := range h.Deploys {
		d := &h.Deploys[i]
		if d.Status != model.DeploySucceeded || d.Revisions[p.Repo] == "" {
			continue
		}
		switch cmp(p.Repo, p.EffectiveSHA, d.Revisions[p.Repo]) {
		case model.Included:
			s.State, s.At, s.SHA, s.Deploy = model.SlotDeployed, d.FinishedAt, d.Revisions[p.Repo], d
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
