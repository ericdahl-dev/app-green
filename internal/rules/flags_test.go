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
		{"checks in any other state (EXPECTED, ERROR) count as failing, even when approved", model.Chain{PRs: []model.PR{{State: model.PROpen, Checks: "EXPECTED", Review: model.ReviewApproved, Reviewers: 1}}},
			[]model.FlagKind{model.FlagCheckFailed}},
		{"requested changes flag the PR", model.Chain{PRs: []model.PR{{State: model.PROpen, Review: model.ReviewChangesRequested, Reviewers: 1}}},
			[]model.FlagKind{model.FlagChangesRequested}},
		{"an approved, green PR that is not merged is ready to merge", model.Chain{PRs: []model.PR{{State: model.PROpen, Checks: model.ChecksPassing, Review: model.ReviewApproved, Reviewers: 1}}},
			[]model.FlagKind{model.FlagReadyToMerge}},
		{"an approved PR in a repo with no checks is ready to merge", model.Chain{PRs: []model.PR{{State: model.PROpen, Checks: model.ChecksNone, Review: model.ReviewApproved, Reviewers: 1}}},
			[]model.FlagKind{model.FlagReadyToMerge}},
		{"a PR with checks done and no reviewer is waiting on review", model.Chain{PRs: []model.PR{{State: model.PROpen, Checks: model.ChecksPassing, OpenedAt: now}}},
			[]model.FlagKind{model.FlagStaleReview}},
		{"a PR with no review after StaleReview is stale", model.Chain{PRs: []model.PR{{State: model.PROpen, Checks: model.ChecksPassing, Reviewers: 1, Review: model.ReviewRequired, OpenedAt: now.Add(-72 * time.Hour)}}},
			[]model.FlagKind{model.FlagStaleReview}},
		{"a failed pipeline run flags the row", model.Chain{Slots: []model.EnvSlot{{Env: testEnv, Applies: true, State: model.SlotFailed}}}, []model.FlagKind{model.FlagPipelineFailed}},
		{"a rolled-back deploy flags the row", model.Chain{Slots: []model.EnvSlot{{Env: prodEnv, Applies: true, State: model.SlotRolledBack}}}, []model.FlagKind{model.FlagRolledBack}},
		{"fewer healthy tasks than desired after a deploy flags the row", model.Chain{Slots: []model.EnvSlot{{Env: prodEnv, Applies: true, State: model.SlotDeployed, Health: model.Health{Known: true, Desired: 2, Healthy: 1}}}},
			[]model.FlagKind{model.FlagUnhealthy}},
		{"a pipeline paused for approval flags the row", model.Chain{Slots: []model.EnvSlot{{Env: prodEnv, Applies: true, State: model.SlotAwaitingApproval}}}, []model.FlagKind{model.FlagAwaitingApproval}},
		{"Jira still In Progress after the PR merged is a status mismatch", model.Chain{Ticket: model.Ticket{StatusCategory: "In Progress"}, Stage: model.StageMerged,
			PRs: []model.PR{{State: model.PRMerged}}}, []model.FlagKind{model.FlagStatusMismatch}},
		{"Jira still To Do after the PR merged is a status mismatch", model.Chain{Ticket: model.Ticket{StatusCategory: "To Do", Status: "Backlog"}, Stage: model.StageMerged,
			PRs: []model.PR{{State: model.PRMerged}}}, []model.FlagKind{model.FlagStatusMismatch}},
		{"Done but not in prod within DoneGrace is not flagged yet", model.Chain{Ticket: model.Ticket{StatusCategory: "Done", StatusSince: now.Add(-time.Hour)}, Stage: model.StageInTest}, nil},
		{"Done but not in prod past DoneGrace is a status mismatch", model.Chain{Ticket: model.Ticket{StatusCategory: "Done", StatusSince: now.Add(-3 * time.Hour)}, Stage: model.StageInTest},
			[]model.FlagKind{model.FlagStatusMismatch}},
		{"Done with no status time falls back to the ticket's last update", model.Chain{Ticket: model.Ticket{StatusCategory: "Done", Updated: now.Add(-3 * time.Hour)}, Stage: model.StageInTest},
			[]model.FlagKind{model.FlagStatusMismatch}},
		{"Done with neither a status time nor an update time is not flagged", model.Chain{Ticket: model.Ticket{StatusCategory: "Done"}, Stage: model.StageInTest}, nil},
		{"red flags come before yellow ones", model.Chain{PRs: []model.PR{failing},
			Slots: []model.EnvSlot{{Env: prodEnv, Applies: true, State: model.SlotAwaitingApproval}}},
			[]model.FlagKind{model.FlagCheckFailed, model.FlagAwaitingApproval}},
		{"within a level, flags follow FlagKind order, not PR order", model.Chain{PRs: []model.PR{
			{Number: 1, State: model.PROpen, Checks: model.ChecksPassing, OpenedAt: now},
			{Number: 2, State: model.PROpen, Checks: model.ChecksPassing, Review: model.ReviewApproved, Reviewers: 1}}},
			[]model.FlagKind{model.FlagReadyToMerge, model.FlagStaleReview}},
		{"a deploy with unknown health raises nothing", model.Chain{Slots: []model.EnvSlot{{Env: prodEnv, Applies: true, State: model.SlotDeployed}}}, nil},
		{"slots in Envs that do not deploy the chain's repos raise nothing", model.Chain{Slots: []model.EnvSlot{
			{Env: testEnv, State: model.SlotFailed}, {Env: prodEnv, State: model.SlotAwaitingApproval},
			{Env: otherProd, State: model.SlotDeployed, Health: model.Health{Known: true, Desired: 2}}}}, nil},
		{"a deploy status that cannot be decided is flagged", model.Chain{Slots: []model.EnvSlot{{Env: prodEnv, Applies: true, State: model.SlotUnknown}}},
			[]model.FlagKind{model.FlagDeployUnknown}},
		{"an unknown deploy status on a test Env is flagged too", model.Chain{Slots: []model.EnvSlot{{Env: testEnv, Applies: true, State: model.SlotUnknown}}},
			[]model.FlagKind{model.FlagDeployUnknown}},
		{"awaiting approval comes before deploy unknown", model.Chain{Slots: []model.EnvSlot{
			{Env: testEnv, Applies: true, State: model.SlotUnknown}, {Env: prodEnv, Applies: true, State: model.SlotAwaitingApproval}}},
			[]model.FlagKind{model.FlagAwaitingApproval, model.FlagDeployUnknown}},
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

func TestDeployUnknownFlagTargetsItsSlotAtYellow(t *testing.T) {
	c := model.Chain{Slots: []model.EnvSlot{{Env: prodEnv, Applies: true, State: model.SlotUnknown}}}
	fs := rules.Flags(c, now, rules.Thresholds{})
	if len(fs) != 1 {
		t.Fatalf("flags %v, want one deploy-unknown flag", kinds(fs))
	}
	f := fs[0]
	if f.Level != model.Yellow || f.Slot == nil || f.Slot.Env.ID() != prodEnv.ID() || f.Reason != "a Production deploy status unknown" {
		t.Errorf("flag %+v, want yellow on the Production slot with reason %q", f, "a Production deploy status unknown")
	}
}

func TestRolledBackFlagTargetsItsSlotAtRed(t *testing.T) {
	c := model.Chain{Slots: []model.EnvSlot{{Env: prodEnv, Applies: true, State: model.SlotRolledBack}}}
	fs := rules.Flags(c, now, rules.Thresholds{})
	if len(fs) != 1 {
		t.Fatalf("flags %v, want one rolled-back flag", kinds(fs))
	}
	if f := fs[0]; f.Level != model.Red || f.Slot == nil || f.Slot.Env.ID() != prodEnv.ID() || f.Reason != "a Production rolled back" {
		t.Errorf("flag %+v, want red on the Production slot with reason %q", f, "a Production rolled back")
	}
}

func TestToDoMismatchNamesTheStatus(t *testing.T) {
	c := model.Chain{Ticket: model.Ticket{StatusCategory: "To Do", Status: "Backlog"}, Stage: model.StageInTest}
	fs := rules.Flags(c, now, rules.Thresholds{})
	if len(fs) != 1 || fs[0].Reason != "Jira still Backlog, PR merged" {
		t.Errorf("flags %+v, want one mismatch with reason %q", fs, "Jira still Backlog, PR merged")
	}
}

// partialProd is a chain live in prodEnv since at and not yet in otherProd.
func partialProd(at time.Time) model.Chain {
	return model.Chain{Stage: model.StageAwaitingProd, PRs: []model.PR{{State: model.PRMerged, EffectiveSHA: "a"}},
		Slots: []model.EnvSlot{
			{Env: prodEnv, Applies: true, State: model.SlotDeployed, At: at},
			{Env: otherProd, Applies: true, State: model.SlotNotYet}}}
}

func TestPartialProdPastThresholdIsFlagged(t *testing.T) {
	fs := rules.Flags(partialProd(now.Add(-5*time.Hour)), now, rules.Thresholds{PartialProd: 4 * time.Hour})
	if len(fs) != 1 || fs[0].Kind != model.FlagPartialProd || fs[0].Level != model.Yellow || fs[0].Reason != "live in a only" {
		t.Errorf("flags %+v, want one yellow partial prod flag with reason %q", fs, "live in a only")
	}
}

func TestPartialProdIsNotFlaggedTooSoonOrWithoutData(t *testing.T) {
	th := rules.Thresholds{PartialProd: 4 * time.Hour}
	allLive := partialProd(now.Add(-5 * time.Hour))
	allLive.Stage, allLive.Slots[1].State, allLive.Slots[1].At = model.StageInProd, model.SlotDeployed, now.Add(-5*time.Hour)
	cases := []struct {
		name string
		c    model.Chain
		th   rules.Thresholds
	}{
		{"within the threshold", partialProd(now.Add(-3 * time.Hour)), th},
		{"zero threshold", partialProd(now.Add(-5 * time.Hour)), rules.Thresholds{}},
		{"deploy with no time", partialProd(time.Time{}), th},
		{"live in every prod Env", allLive, th},
	}
	for _, c := range cases {
		if fs := rules.Flags(c.c, now, c.th); len(fs) != 0 {
			t.Errorf("%s: flags %v, want none", c.name, kinds(fs))
		}
	}
}

func TestPartialProdFlagTargetsTheFirstMissingProdEnv(t *testing.T) {
	c := partialProd(now.Add(-5 * time.Hour))
	third := model.Env{Account: "c", Stage: "Production", Prod: true}
	c.Slots = append(c.Slots, model.EnvSlot{Env: third, Applies: true, State: model.SlotNotYet})
	fs := rules.Flags(c, now, rules.Thresholds{PartialProd: 4 * time.Hour})
	if len(fs) != 1 || fs[0].Slot == nil || fs[0].Slot.Env.ID() != otherProd.ID() {
		t.Fatalf("flags %+v, want one partial prod flag targeting %s, the first prod Env without it", fs, otherProd.ID())
	}
}
