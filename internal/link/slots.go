package link

import (
	"slices"
	"strings"
	"time"

	"github.com/ericdahl-dev/app-green/internal/model"
)

// Slot decides where prs stand in one Env right now. Only merged PRs in a
// repo this Env deploys count (Env.Repos when config set it, else the repos in
// its history as a last resort); repos match ignoring case, and a deploy
// carries a repo only with a non-empty SHA for it. Open and closed PRs never
// hold a slot back. A merged PR that
// is StackPending or has no EffectiveSHA (not on the default branch yet, or
// malformed input) counts as SlotNotYet. With Env.Repos set, a PR whose repo
// no deploy in the history carries counts as SlotUnknown. The chain's slot is
// its least advanced PR (see rank; Failed and RolledBack are the least
// advanced); for ties, the latest Deployed time and the newest
// AwaitingApproval deploy win. Applies is set when at least one merged PR is
// in a repo this Env deploys.
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
		if !historyCarries(h, p.Repo) {
			// Config says this Env deploys the repo, but no deploy in the
			// history carries it: the history is missing or truncated.
			results = append(results, model.EnvSlot{Env: h.Env, State: model.SlotUnknown})
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

// deploysRepo reports whether h's Env deploys repo: from Env.Repos when
// config set it, else from the repos seen in its deploys.
func deploysRepo(h model.EnvHistory, repo string) bool {
	if len(h.Env.Repos) > 0 {
		return slices.ContainsFunc(h.Env.Repos, func(r string) bool { return strings.EqualFold(r, repo) })
	}
	return historyCarries(h, repo)
}

// revision is the SHA revs holds for repo, matching the repo ignoring case,
// or "" when it holds none.
func revision(revs map[string]string, repo string) string {
	if sha, ok := revs[repo]; ok {
		return sha
	}
	for k, sha := range revs {
		if strings.EqualFold(k, repo) {
			return sha
		}
	}
	return ""
}

// historyCarries reports whether any deploy in h carries repo: holds a
// non-empty SHA for it. Every "carries" check in this package means that.
func historyCarries(h model.EnvHistory, repo string) bool {
	for _, d := range h.Deploys {
		if revision(d.Revisions, repo) != "" {
			return true
		}
	}
	return false
}

// rank orders states from least to most advanced for "worst of" decisions.
// Failed and RolledBack rank lowest so a real failure is never hidden behind
// Unknown.
func rank(s model.SlotState) int {
	switch s {
	case model.SlotFailed, model.SlotRolledBack:
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
// (failed or rejected, in progress, awaiting approval): exact SHA, or by compare for runs
// newer than N. When N does not contain the PR but an older success did (its
// exact SHA, or else, walking older successes newest to oldest, the first one
// whose compare says Included: a batched deploy), the slot is RolledBack
// (Deploy and At are N's) unless a run newer than N contains it. That walk
// stops at the first compare that cannot answer. With no such run: Unknown if
// N's compare or the walk could not answer, else NotYet.
func slotFor(p model.PR, h model.EnvHistory, cmp model.CompareFunc) model.EnvSlot {
	s := model.EnvSlot{Env: h.Env, SHA: p.EffectiveSHA}
	// 1. The PR's oldest exact-SHA success, where liveSince stops.
	var firstExact *model.Deploy
	for i := range h.Deploys {
		d := &h.Deploys[i]
		if d.Status == model.DeploySucceeded && revision(d.Revisions, p.Repo) == p.EffectiveSHA {
			firstExact = d // newest first, so the last one seen is the oldest
		}
	}
	// 2. Does N, the newest succeeded deploy carrying p.Repo, contain the PR?
	contains := func(d *model.Deploy) model.Inclusion {
		if revision(d.Revisions, p.Repo) == p.EffectiveSHA {
			return model.Included
		}
		return cmp(p.Repo, p.EffectiveSHA, revision(d.Revisions, p.Repo))
	}
	unknown, rolledBack := false, false
	ni := newestSuccess(h, p.Repo)
	if ni >= 0 {
		n := &h.Deploys[ni]
		switch contains(n) {
		case model.Included:
			s.State, s.SHA, s.Deploy = model.SlotDeployed, revision(n.Revisions, p.Repo), n
			s.At = liveSince(h, p.Repo, ni, firstExact, contains)
			return s
		case model.InclusionUnknown:
			unknown = true
		case model.NotIncluded:
			// N is the newest success, so an exact success for the PR is older:
			// it was live, and N replaced it.
			rolledBack = firstExact != nil
			if !rolledBack {
				// It may have gone out inside a batch (an older success whose
				// SHA contains it). The first answer from newest to oldest decides.
				rolledBack, unknown = olderSuccessContains(h, p.Repo, ni, contains)
			}
		}
	}
	// 3. Not running now: the newest non-success run that contains the PR
	//    decides. A run contains it by exact SHA, or, when it is newer than N,
	//    by compare (a newer in-flight run supersedes an old failure).
	var pending *model.Deploy
	pi := -1
	for i := range h.Deploys {
		d := &h.Deploys[i]
		if d.Status == model.DeploySucceeded || revision(d.Revisions, p.Repo) == "" {
			continue
		}
		if revision(d.Revisions, p.Repo) == p.EffectiveSHA || ((ni < 0 || i < ni) && contains(d) == model.Included) {
			pending, pi = d, i
			break
		}
	}
	if rolledBack && (pending == nil || pi > ni) {
		// Only a run newer than the rollback can supersede it.
		s.State, s.Deploy, s.At = model.SlotRolledBack, &h.Deploys[ni], h.Deploys[ni].FinishedAt
		s.SHA = revision(h.Deploys[ni].Revisions, p.Repo)
		return s
	}
	if pending == nil && unknown {
		s.State = model.SlotUnknown
		return s
	}
	if pending != nil {
		s.Deploy, s.At, s.SHA = pending, pending.FinishedAt, revision(pending.Revisions, p.Repo)
		switch pending.Status {
		case model.DeployFailed, model.DeployRejected:
			// A rejected approval stops the run like a failure; rules names it.
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

// olderSuccessContains walks the succeeded deploys older than index ni that
// carry repo, newest to oldest, and stops at the first that contains the PR
// (included) or whose compare cannot answer (unknown).
func olderSuccessContains(h model.EnvHistory, repo string, ni int, contains func(*model.Deploy) model.Inclusion) (included, unknown bool) {
	for i := ni + 1; i < len(h.Deploys); i++ {
		d := &h.Deploys[i]
		if d.Status != model.DeploySucceeded || revision(d.Revisions, repo) == "" {
			continue
		}
		switch contains(d) {
		case model.Included:
			return true, false
		case model.InclusionUnknown:
			return false, true
		}
	}
	return false, false
}

// newestSuccess is the index of the newest succeeded deploy carrying repo, or -1.
func newestSuccess(h model.EnvHistory, repo string) int {
	for i, d := range h.Deploys {
		if d.Status == model.DeploySucceeded && revision(d.Revisions, repo) != "" {
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
		if d.Status != model.DeploySucceeded || revision(d.Revisions, repo) == "" {
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
