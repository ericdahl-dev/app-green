package rules

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ericdahl-dev/app-green/internal/model"
)

// Thresholds are the only configurable parts of the rules. Each one at zero
// or below turns its check off.
type Thresholds struct {
	// StaleReview: an open PR with no approval this long after it opened is
	// yellow (a PR with no reviewer at all is flagged whatever this is).
	StaleReview time.Duration
	// DoneGrace: a ticket Done in Jira this long (since StatusSince, else
	// Updated) without being in prod is yellow.
	DoneGrace time.Duration
	// FadeAfter: a row with no flags, in prod in every applicable prod Env,
	// drops out of the list once the last of them went live this long ago.
	FadeAfter time.Duration
	// PartialProd: a row live in some applicable prod Envs but not all, this
	// long after the earliest went live, is yellow.
	PartialProd time.Duration
}

// Flags returns c's flags, worst level first, then in FlagKind order.
// c.Stage must already be set (call Stage first). Each flag's PR or Slot
// points into the backing arrays of c.PRs or c.Slots, so it aliases the
// caller's chain; copy the chain's slices first if they will change.
func Flags(c model.Chain, now time.Time, th Thresholds) []model.Flag {
	var fs []model.Flag
	add := func(l model.Level, k model.FlagKind, reason string, pr *model.PR, s *model.EnvSlot) {
		fs = append(fs, model.Flag{Level: l, Kind: k, Reason: reason, PR: pr, Slot: s})
	}
	for i := range c.Slots {
		s := &c.Slots[i]
		if !s.Applies {
			continue // this Env deploys none of the chain's merged repos
		}
		switch {
		case s.State == model.SlotFailed:
			add(model.Red, model.FlagPipelineFailed, fmt.Sprintf("%s %s failed", s.Env.Account, s.Env.Stage), nil, s)
		case s.State == model.SlotRolledBack:
			add(model.Red, model.FlagRolledBack, fmt.Sprintf("%s %s rolled back", s.Env.Account, s.Env.Stage), nil, s)
		case s.State == model.SlotDeployed && !s.Health.OK():
			add(model.Red, model.FlagUnhealthy, fmt.Sprintf("%s %s %d/%d healthy", s.Env.Account, s.Env.Stage, s.Health.Healthy, s.Health.Desired), nil, s)
		case s.State == model.SlotAwaitingApproval:
			add(model.Yellow, model.FlagAwaitingApproval, fmt.Sprintf("%s %s awaiting approval", s.Env.Account, s.Env.Stage), nil, s)
		case s.State == model.SlotUnknown:
			add(model.Yellow, model.FlagDeployUnknown, fmt.Sprintf("%s %s deploy status unknown", s.Env.Account, s.Env.Stage), nil, s)
		}
	}
	for i := range c.PRs {
		p := &c.PRs[i]
		if p.State == model.PRMerged && p.Stranded {
			add(model.Red, model.FlagStranded, fmt.Sprintf("PR #%d merged into a dead branch", p.Number), p, nil)
		}
		if p.State != model.PROpen {
			continue
		}
		switch {
		case checksFailing(p.Checks):
			add(model.Red, model.FlagCheckFailed, fmt.Sprintf("PR #%d %s failing", p.Number, checkNames(p.Failing)), p, nil)
		case p.Review == model.ReviewChangesRequested:
			add(model.Red, model.FlagChangesRequested, fmt.Sprintf("PR #%d changes requested", p.Number), p, nil)
		case p.IsDraft:
			// A draft is not asking for review yet: no ready-to-merge, no-reviewer
			// or stale-review flag. The red flags above still apply.
		case p.Review == model.ReviewApproved && (p.Checks == model.ChecksPassing || p.Checks == model.ChecksNone):
			add(model.Yellow, model.FlagReadyToMerge, fmt.Sprintf("PR #%d approved, not merged", p.Number), p, nil)
		case p.Reviewers == 0 && p.Checks != model.ChecksPending:
			add(model.Yellow, model.FlagStaleReview, fmt.Sprintf("PR #%d has no reviewer", p.Number), p, nil)
		case p.Review != model.ReviewApproved && th.StaleReview > 0 && now.Sub(p.OpenedAt) > th.StaleReview:
			add(model.Yellow, model.FlagStaleReview, fmt.Sprintf("PR #%d no review %s", p.Number, days(now.Sub(p.OpenedAt))), p, nil)
		}
	}
	for _, repo := range unclaimedRepos(c) {
		add(model.Yellow, model.FlagDeployUnknown, repo+" not deployed by any configured environment", nil, nil)
	}
	if r, s := partialProd(c, now, th); r != "" {
		add(model.Yellow, model.FlagPartialProd, r, nil, s)
	}
	if r := statusMismatch(c, now, th); r != "" {
		add(model.Yellow, model.FlagStatusMismatch, r, nil, nil)
	}
	slices.SortStableFunc(fs, func(a, b model.Flag) int {
		if a.Level != b.Level {
			return int(b.Level - a.Level)
		}
		return int(a.Kind - b.Kind)
	})
	return fs
}

// unclaimedRepos lists, once each and in PR order, the repos of the chain's
// merged PRs (EffectiveSHA set) that no slot's Env.Repos names, compared
// ignoring case. Such a repo is deployed nowhere this app watches, so the
// chain must not look done. With no Env.Repos on any slot (link fell back to
// history) rules cannot tell which Env deploys which repo and returns nothing.
func unclaimedRepos(c model.Chain) []string {
	configured := false
	for _, s := range c.Slots {
		configured = configured || len(s.Env.Repos) > 0
	}
	if !configured {
		return nil
	}
	var out []string
	for _, p := range c.PRs {
		if p.State != model.PRMerged || p.EffectiveSHA == "" {
			continue
		}
		same := func(r string) bool { return strings.EqualFold(r, p.Repo) }
		if slices.ContainsFunc(out, same) {
			continue
		}
		claimed := slices.ContainsFunc(c.Slots, func(s model.EnvSlot) bool { return slices.ContainsFunc(s.Env.Repos, same) })
		if !claimed {
			out = append(out, p.Repo)
		}
	}
	return out
}

// partialProd explains a chain live in some applicable prod Envs but not all,
// once the earliest of those went live more than th.PartialProd ago, and
// returns the first applicable prod slot (in Env order) that does not have it.
// Deployed slots with no time are skipped; with no times at all there is no
// flag.
func partialProd(c model.Chain, now time.Time, th Thresholds) (string, *model.EnvSlot) {
	if th.PartialProd <= 0 || c.Stage != model.StageAwaitingProd {
		return "", nil
	}
	var live []string
	var earliest time.Time
	var missing *model.EnvSlot
	for i := range c.Slots {
		s := &c.Slots[i]
		if !s.Applies || !s.Env.Prod {
			continue
		}
		if s.State != model.SlotDeployed {
			if missing == nil {
				missing = s
			}
			continue
		}
		live = append(live, s.Env.Account)
		if !s.At.IsZero() && (earliest.IsZero() || s.At.Before(earliest)) {
			earliest = s.At
		}
	}
	if missing == nil || len(live) == 0 || earliest.IsZero() || now.Sub(earliest) <= th.PartialProd {
		return "", nil
	}
	return "live in " + strings.Join(live, ", ") + " only", missing
}

func statusMismatch(c model.Chain, now time.Time, th Thresholds) string {
	since := c.Ticket.StatusSince
	if since.IsZero() {
		since = c.Ticket.Updated // a safe stand-in: it is never older than the status change
	}
	switch {
	case (c.Ticket.StatusCategory == model.StatusInProgress || c.Ticket.StatusCategory == model.StatusToDo) && c.Stage >= model.StageMerged:
		return "Jira still " + orDefault(c.Ticket.Status, c.Ticket.StatusCategory.Label()) + ", PR merged"
	case c.Ticket.StatusCategory == model.StatusDone && c.Stage != model.StageInProd && th.DoneGrace > 0 && !nothingToShip(c) &&
		!since.IsZero() && now.Sub(since) > th.DoneGrace:
		return "Jira Done, not in prod"
	}
	return ""
}

// nothingToShip reports whether c has no PRs once closed ones are ignored.
func nothingToShip(c model.Chain) bool {
	for _, p := range c.PRs {
		if p.State != model.PRClosed {
			return false
		}
	}
	return true
}

func checkNames(cs []model.Check) string {
	if len(cs) == 0 {
		return "checks"
	}
	if len(cs) == 1 {
		return cs[0].Name
	}
	return fmt.Sprintf("%s +%d", cs[0].Name, len(cs)-1)
}

func days(d time.Duration) string {
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// checksFailing treats every state other than passing, pending or none as a
// failure: GitHub also sends ERROR and EXPECTED, and neither is green.
func checksFailing(s model.ChecksState) bool {
	switch s {
	case model.ChecksPassing, model.ChecksPending, model.ChecksNone:
		return false
	}
	return true
}
