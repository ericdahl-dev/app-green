package rules_test

import (
	"slices"
	"testing"
	"time"

	"github.com/ericdahl-dev/app-green/internal/model"
	"github.com/ericdahl-dev/app-green/internal/rules"
)

var now = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

func kinds(fs []model.Flag) []model.FlagKind {
	var out []model.FlagKind
	for _, f := range fs {
		out = append(out, f.Kind)
	}
	return out
}

func TestFlags(t *testing.T) {
	th := rules.Thresholds{StaleReview: 48 * time.Hour, DoneGrace: 2 * time.Hour}
	failing := model.PR{State: model.PROpen, Checks: model.ChecksFailing, Failing: []model.Check{{Name: "rspec", RunID: 9}}}
	scanning := model.PR{State: model.PROpen, Checks: model.ChecksFailing, Failing: []model.Check{{Name: "CodeQL", CodeScanning: true}}}
	cases := []struct {
		name string
		c    model.Chain
		want []model.FlagKind
	}{
		{"an open PR with checks running and a reviewer needs nothing", model.Chain{PRs: []model.PR{{State: model.PROpen, Checks: model.ChecksPending, Reviewers: 1, OpenedAt: now}}}, nil},
		{"a failing check flags the PR", model.Chain{PRs: []model.PR{failing}}, []model.FlagKind{model.FlagCheckFailed}},
		{"a failing code scan flags the PR like any check", model.Chain{PRs: []model.PR{scanning}}, []model.FlagKind{model.FlagCheckFailed}},
		{"requested changes flag the PR", model.Chain{PRs: []model.PR{{State: model.PROpen, Review: model.ReviewChangesRequested, Reviewers: 1}}},
			[]model.FlagKind{model.FlagChangesRequested}},
		{"an approved, green PR that is not merged is ready to merge", model.Chain{PRs: []model.PR{{State: model.PROpen, Checks: model.ChecksPassing, Review: model.ReviewApproved, Reviewers: 1}}},
			[]model.FlagKind{model.FlagReadyToMerge}},
		{"a PR with checks done and no reviewer is waiting on review", model.Chain{PRs: []model.PR{{State: model.PROpen, Checks: model.ChecksPassing, OpenedAt: now}}},
			[]model.FlagKind{model.FlagStaleReview}},
		{"a PR with no review after StaleReview is stale", model.Chain{PRs: []model.PR{{State: model.PROpen, Checks: model.ChecksPassing, Reviewers: 1, Review: model.ReviewRequired, OpenedAt: now.Add(-72 * time.Hour)}}},
			[]model.FlagKind{model.FlagStaleReview}},
		{"a failed pipeline run flags the row", model.Chain{Slots: []model.EnvSlot{{Env: testEnv, State: model.SlotFailed}}}, []model.FlagKind{model.FlagPipelineFailed}},
		{"fewer healthy tasks than desired after a deploy flags the row", model.Chain{Slots: []model.EnvSlot{{Env: prodEnv, State: model.SlotDeployed, Health: model.Health{Known: true, Desired: 2, Healthy: 1}}}},
			[]model.FlagKind{model.FlagUnhealthy}},
		{"a pipeline paused for approval flags the row", model.Chain{Slots: []model.EnvSlot{{Env: prodEnv, State: model.SlotAwaitingApproval}}}, []model.FlagKind{model.FlagAwaitingApproval}},
		{"Jira still In Progress after the PR merged is a status mismatch", model.Chain{Ticket: model.Ticket{StatusCategory: "In Progress"}, Stage: model.StageMerged,
			PRs: []model.PR{{State: model.PRMerged}}}, []model.FlagKind{model.FlagStatusMismatch}},
		{"Done but not in prod within DoneGrace is not flagged yet", model.Chain{Ticket: model.Ticket{StatusCategory: "Done", StatusSince: now.Add(-time.Hour)}, Stage: model.StageInTest}, nil},
		{"Done but not in prod past DoneGrace is a status mismatch", model.Chain{Ticket: model.Ticket{StatusCategory: "Done", StatusSince: now.Add(-3 * time.Hour)}, Stage: model.StageInTest},
			[]model.FlagKind{model.FlagStatusMismatch}},
		{"red flags come before yellow ones", model.Chain{PRs: []model.PR{failing},
			Slots: []model.EnvSlot{{Env: prodEnv, State: model.SlotAwaitingApproval}}},
			[]model.FlagKind{model.FlagCheckFailed, model.FlagAwaitingApproval}},
		{"within a level, flags follow FlagKind order, not PR order", model.Chain{PRs: []model.PR{
			{Number: 1, State: model.PROpen, Checks: model.ChecksPassing, OpenedAt: now},
			{Number: 2, State: model.PROpen, Checks: model.ChecksPassing, Review: model.ReviewApproved, Reviewers: 1}}},
			[]model.FlagKind{model.FlagReadyToMerge, model.FlagStaleReview}},
		{"a deploy with unknown health raises nothing", model.Chain{Slots: []model.EnvSlot{{Env: prodEnv, State: model.SlotDeployed}}}, nil},
	}
	for _, c := range cases {
		if got := kinds(rules.Flags(c.c, now, th)); !slices.Equal(got, c.want) {
			t.Errorf("%s: flags %v, want %v", c.name, got, c.want)
		}
	}
}

func TestCheckFailedFlagTargetsItsPRAtRed(t *testing.T) {
	c := model.Chain{PRs: []model.PR{{Number: 7, State: model.PROpen, Checks: model.ChecksFailing, Failing: []model.Check{{RunID: 1}}}}}
	fs := rules.Flags(c, now, rules.Thresholds{})
	if len(fs) == 0 {
		t.Fatal("want a check-failed flag on PR #7, got no flags")
	}
	if fs[0].PR == nil || fs[0].PR.Number != 7 || fs[0].Level != model.Red {
		t.Errorf("flag %+v should target PR #7 at red", fs[0])
	}
}
