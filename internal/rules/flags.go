package rules

import (
	"fmt"
	"slices"
	"time"

	"github.com/ericdahl-dev/app-green/internal/model"
)

// Thresholds are the only configurable parts of the rules.
type Thresholds struct {
	StaleReview time.Duration // no review for this long → yellow
	DoneGrace   time.Duration // Done but not in prod for this long → yellow
	FadeAfter   time.Duration // a clean row in prod this long drops out
}

// Flags returns c's flags, worst level first, then in FlagKind order.
// c.Stage must already be set (call Stage first).
func Flags(c model.Chain, now time.Time, th Thresholds) []model.Flag {
	var fs []model.Flag
	add := func(l model.Level, k model.FlagKind, reason string, pr *model.PR, s *model.EnvSlot) {
		fs = append(fs, model.Flag{Level: l, Kind: k, Reason: reason, PR: pr, Slot: s})
	}
	for i := range c.Slots {
		s := &c.Slots[i]
		switch {
		case s.State == model.SlotFailed:
			add(model.Red, model.FlagPipelineFailed, fmt.Sprintf("%s %s failed", s.Env.Account, s.Env.Stage), nil, s)
		case s.State == model.SlotDeployed && !s.Health.OK():
			add(model.Red, model.FlagUnhealthy, fmt.Sprintf("%s %s %d/%d healthy", s.Env.Account, s.Env.Stage, s.Health.Healthy, s.Health.Desired), nil, s)
		case s.State == model.SlotAwaitingApproval:
			add(model.Yellow, model.FlagAwaitingApproval, fmt.Sprintf("%s %s awaiting approval", s.Env.Account, s.Env.Stage), nil, s)
		}
	}
	for i := range c.PRs {
		p := &c.PRs[i]
		if p.State != model.PROpen {
			continue
		}
		switch {
		case p.Checks == model.ChecksFailing:
			add(model.Red, model.FlagCheckFailed, fmt.Sprintf("PR #%d %s failing", p.Number, checkNames(p.Failing)), p, nil)
		case p.Review == model.ReviewChangesRequested:
			add(model.Red, model.FlagChangesRequested, fmt.Sprintf("PR #%d changes requested", p.Number), p, nil)
		case p.Review == model.ReviewApproved && p.Checks == model.ChecksPassing:
			add(model.Yellow, model.FlagReadyToMerge, fmt.Sprintf("PR #%d approved, not merged", p.Number), p, nil)
		case p.Reviewers == 0 && p.Checks != model.ChecksPending:
			add(model.Yellow, model.FlagStaleReview, fmt.Sprintf("PR #%d has no reviewer", p.Number), p, nil)
		case p.Review != model.ReviewApproved && th.StaleReview > 0 && now.Sub(p.OpenedAt) > th.StaleReview:
			add(model.Yellow, model.FlagStaleReview, fmt.Sprintf("PR #%d no review %s", p.Number, days(now.Sub(p.OpenedAt))), p, nil)
		}
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

func statusMismatch(c model.Chain, now time.Time, th Thresholds) string {
	switch {
	case c.Ticket.StatusCategory == "In Progress" && c.Stage >= model.StageMerged:
		return "Jira still " + orDefault(c.Ticket.Status, "In Progress") + ", PR merged"
	case c.Ticket.StatusCategory == "Done" && c.Stage != model.StageInProd &&
		!c.Ticket.StatusSince.IsZero() && now.Sub(c.Ticket.StatusSince) > th.DoneGrace:
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
