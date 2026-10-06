package rules

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ericdahl-dev/app-green/internal/model"
)

// Thresholds are the only configurable parts of the rules.
type Thresholds struct {
	StaleReview time.Duration // no review for this long → yellow
	DoneGrace   time.Duration // Done but not in prod for this long → yellow
	FadeAfter   time.Duration // a clean row in prod this long drops out
	PartialProd time.Duration // live in some prod Envs, not all, this long → yellow
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
		if p.State != model.PROpen {
			continue
		}
		switch {
		case checksFailing(p.Checks):
			add(model.Red, model.FlagCheckFailed, fmt.Sprintf("PR #%d %s failing", p.Number, checkNames(p.Failing)), p, nil)
		case p.Review == model.ReviewChangesRequested:
			add(model.Red, model.FlagChangesRequested, fmt.Sprintf("PR #%d changes requested", p.Number), p, nil)
		case p.Review == model.ReviewApproved && (p.Checks == model.ChecksPassing || p.Checks == model.ChecksNone):
			add(model.Yellow, model.FlagReadyToMerge, fmt.Sprintf("PR #%d approved, not merged", p.Number), p, nil)
		case p.Reviewers == 0 && p.Checks != model.ChecksPending:
			add(model.Yellow, model.FlagStaleReview, fmt.Sprintf("PR #%d has no reviewer", p.Number), p, nil)
		case p.Review != model.ReviewApproved && th.StaleReview > 0 && now.Sub(p.OpenedAt) > th.StaleReview:
			add(model.Yellow, model.FlagStaleReview, fmt.Sprintf("PR #%d no review %s", p.Number, days(now.Sub(p.OpenedAt))), p, nil)
		}
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
	case (c.Ticket.StatusCategory == "In Progress" || c.Ticket.StatusCategory == "To Do") && c.Stage >= model.StageMerged:
		return "Jira still " + orDefault(c.Ticket.Status, c.Ticket.StatusCategory) + ", PR merged"
	case c.Ticket.StatusCategory == "Done" && c.Stage != model.StageInProd &&
		!since.IsZero() && now.Sub(since) > th.DoneGrace:
		return "Jira Done, not in prod"
	}
	return ""
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
