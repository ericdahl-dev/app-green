package link

import (
	"time"

	"github.com/ericdahl-dev/app-green/internal/model"
)

// Slot decides where prs stand in one Env right now. Only merged PRs in a
// repo this Env deploys count; open and closed PRs never hold a slot back. A
// merged PR that is StackPending or has no EffectiveSHA (not on the default
// branch yet, or malformed input) counts as SlotNotYet. The chain's slot is
// its least advanced PR (see rank; Failed is the least advanced of all); for
// ties, the latest Deployed time and the newest AwaitingApproval deploy win.
// Applies is set when at least one merged PR is in a repo this Env deploys.
func Slot(prs []model.PR, h model.EnvHistory, cmp model.CompareFunc) model.EnvSlot {
	slot := model.EnvSlot{Env: h.Env, Health: h.Health, State: model.SlotNotYet}
	var results []model.EnvSlot
	for _, p := range prs {
		if p.State != model.PRMerged || !deploysRepo(h, p.Repo) {
			continue
		}
		if p.StackPending || p.EffectiveSHA == "" {
			// Merged, but not on the default branch yet (or malformed input):
			// it holds the chain back.
			results = append(results, model.EnvSlot{Env: h.Env, State: model.SlotNotYet})
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
		if behind(r, worst) {
			worst = r
		}
	}
	worst.Health = h.Health
	worst.Applies = true
	return worst
}

// behind reports whether r should replace worst in the "worst of" across PRs.
// For two Deployed PRs the later one wins: the chain is fully deployed only
// when its last PR is. For two AwaitingApproval PRs the newer deploy wins.
func behind(r, worst model.EnvSlot) bool {
	if rank(r.State) != rank(worst.State) {
		return rank(r.State) < rank(worst.State)
	}
	switch r.State {
	case model.SlotDeployed:
		return r.At.After(worst.At)
	case model.SlotAwaitingApproval:
		// The newer run carries the token that moves the whole chain on.
		return r.Deploy != nil && (worst.Deploy == nil || r.Deploy.FinishedAt.After(worst.Deploy.FinishedAt))
	}
	return false
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
// Failed ranks lowest so a real failure is never hidden behind Unknown.
func rank(s model.SlotState) int {
	switch s {
	case model.SlotFailed:
		return 0
	case model.SlotUnknown:
		return 1
	case model.SlotNotYet:
		return 2
	case model.SlotInProgress:
		return 3
	case model.SlotAwaitingApproval:
		return 4
	case model.SlotDeployed:
		return 5
	default:
		return 0 // a state added later ranks lowest on purpose: never look further along than we know
	}
}

// slotFor decides where one PR stands in h. "Deployed" means running now: the
// newest succeeded deploy carrying p.Repo (N) contains p.EffectiveSHA, either
// as its exact SHA or by compare. At is then when it went live this time: the
// oldest success in the unbroken run of successes back from N that contain it,
// never older than the PR's own oldest exact-SHA success (see liveSince). An
// older exact success does not count when N does not contain the PR (a
// rollback). Otherwise the newest non-success run that contains the PR decides
// (failed, in progress, awaiting approval): exact SHA, or by compare for runs
// newer than N. With no such run: Unknown if N's compare could not answer,
// else NotYet.
func slotFor(p model.PR, h model.EnvHistory, cmp model.CompareFunc) model.EnvSlot {
	s := model.EnvSlot{Env: h.Env, SHA: p.EffectiveSHA}
	// 1. The PR's oldest exact-SHA success, where liveSince stops.
	var firstExact *model.Deploy
	for i := range h.Deploys {
		d := &h.Deploys[i]
		if d.Status == model.DeploySucceeded && d.Revisions[p.Repo] == p.EffectiveSHA {
			firstExact = d // newest first, so the last one seen is the oldest
		}
	}
	// 2. Does N, the newest succeeded deploy carrying p.Repo, contain the PR?
	contains := func(d *model.Deploy) model.Inclusion {
		if d.Revisions[p.Repo] == p.EffectiveSHA {
			return model.Included
		}
		return cmp(p.Repo, p.EffectiveSHA, d.Revisions[p.Repo])
	}
	unknown := false
	ni := newestSuccess(h, p.Repo)
	if ni >= 0 {
		n := &h.Deploys[ni]
		switch contains(n) {
		case model.Included:
			s.State, s.SHA, s.Deploy = model.SlotDeployed, n.Revisions[p.Repo], n
			s.At = liveSince(h, p.Repo, ni, firstExact, contains)
			return s
		case model.InclusionUnknown:
			unknown = true
		}
	}
	// 3. Not running now: the newest non-success run that contains the PR
	//    decides. A run contains it by exact SHA, or, when it is newer than N,
	//    by compare (a newer in-flight run supersedes an old failure).
	var pending *model.Deploy
	for i := range h.Deploys {
		d := &h.Deploys[i]
		if d.Status == model.DeploySucceeded || d.Revisions[p.Repo] == "" {
			continue
		}
		if d.Revisions[p.Repo] == p.EffectiveSHA || ((ni < 0 || i < ni) && contains(d) == model.Included) {
			pending = d
			break
		}
	}
	if pending == nil && unknown {
		s.State = model.SlotUnknown
		return s
	}
	if pending != nil {
		s.Deploy, s.At, s.SHA = pending, pending.FinishedAt, pending.Revisions[p.Repo]
		switch pending.Status {
		case model.DeployFailed:
			s.State = model.SlotFailed
		case model.DeployAwaitingApproval:
			s.State = model.SlotAwaitingApproval
		case model.DeployInProgress:
			s.State = model.SlotInProgress
		default:
			s.State = model.SlotUnknown // a status the adapter should not send
		}
		return s
	}
	s.State = model.SlotNotYet
	return s
}

// newestSuccess is the index of the newest succeeded deploy carrying repo, or -1.
func newestSuccess(h model.EnvHistory, repo string) int {
	for i, d := range h.Deploys {
		if d.Status == model.DeploySucceeded && d.Revisions[repo] != "" {
			return i
		}
	}
	return -1
}

// liveSince walks back from N (index ni) through the unbroken run of
// successes that contain the PR and returns the oldest one's FinishedAt. It
// stops at the first success that does not contain it (a rollback) and never
// walks past firstExact, the PR's oldest exact-SHA success.
func liveSince(h model.EnvHistory, repo string, ni int, firstExact *model.Deploy, contains func(*model.Deploy) model.Inclusion) time.Time {
	at := h.Deploys[ni].FinishedAt
	if &h.Deploys[ni] == firstExact {
		return at
	}
	for i := ni + 1; i < len(h.Deploys); i++ {
		d := &h.Deploys[i]
		if d.Status != model.DeploySucceeded || d.Revisions[repo] == "" {
			continue
		}
		if contains(d) != model.Included {
			break
		}
		at = d.FinishedAt
		if d == firstExact {
			break
		}
	}
	return at
}

// Slots fills c.Slots from histories, which are in Env.Order.
func Slots(c model.Chain, histories []model.EnvHistory, cmp model.CompareFunc) model.Chain {
	c.Slots = make([]model.EnvSlot, len(histories))
	for i, h := range histories {
		c.Slots[i] = Slot(c.PRs, h, cmp)
	}
	return c
}
