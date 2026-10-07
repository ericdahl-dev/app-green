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
	shipped := []model.PR{{Repo: "acme/app", State: model.PRMerged, EffectiveSHA: "a"}}
	scanning := model.PR{State: model.PROpen, Checks: model.ChecksFailing, Failing: []model.Check{{Name: "CodeQL", CodeScanning: true}}}
	cases := []struct {
		name string
		c    model.Chain
		want []model.FlagKind
	}{
		{"an open PR with checks running and a reviewer needs nothing", model.Chain{PRs: []model.PR{{State: model.PROpen, Checks: model.ChecksPending, Reviewers: 1, OpenedAt: now}}}, nil},
		{"a failing check flags the PR", model.Chain{PRs: []model.PR{failing}}, []model.FlagKind{model.FlagCheckFailed}},
		{"a failing code scan flags the PR like any check", model.Chain{PRs: []model.PR{scanning}}, []model.FlagKind{model.FlagCheckFailed}},
		{"checks in any other state (ERROR) count as failing, even when approved", model.Chain{PRs: []model.PR{{State: model.PROpen, Checks: "ERROR", Review: model.ReviewApproved, Reviewers: 1}}},
			[]model.FlagKind{model.FlagCheckFailed}},
		{"a required check that has not reported is a yellow wait, not a failure", model.Chain{PRs: []model.PR{{State: model.PROpen, Checks: model.ChecksExpected, Review: model.ReviewApproved, Reviewers: 1}}},
			[]model.FlagKind{model.FlagCheckExpected}},
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
		{"Jira still In Progress after the PR merged is a status mismatch", model.Chain{Ticket: model.Ticket{StatusCategory: model.StatusInProgress}, Stage: model.StageMerged,
			PRs: []model.PR{{State: model.PRMerged}}}, []model.FlagKind{model.FlagStatusMismatch}},
		{"Jira still To Do after the PR merged is a status mismatch", model.Chain{Ticket: model.Ticket{StatusCategory: model.StatusToDo, Status: "Backlog"}, Stage: model.StageMerged,
			PRs: []model.PR{{State: model.PRMerged}}}, []model.FlagKind{model.FlagStatusMismatch}},
		{"Done but not in prod within DoneGrace is not flagged yet", model.Chain{Ticket: model.Ticket{StatusCategory: model.StatusDone, StatusSince: now.Add(-time.Hour)}, Stage: model.StageInTest, PRs: shipped}, nil},
		{"Done but not in prod past DoneGrace is a status mismatch", model.Chain{Ticket: model.Ticket{StatusCategory: model.StatusDone, StatusSince: now.Add(-3 * time.Hour)}, Stage: model.StageInTest, PRs: shipped},
			[]model.FlagKind{model.FlagStatusMismatch}},
		{"Done with no status time falls back to the ticket's last update", model.Chain{Ticket: model.Ticket{StatusCategory: model.StatusDone, Updated: now.Add(-3 * time.Hour)}, Stage: model.StageInTest, PRs: shipped},
			[]model.FlagKind{model.FlagStatusMismatch}},
		{"Done with neither a status time nor an update time is not flagged", model.Chain{Ticket: model.Ticket{StatusCategory: model.StatusDone}, Stage: model.StageInTest, PRs: shipped}, nil},
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
	c := model.Chain{Ticket: model.Ticket{StatusCategory: model.StatusToDo, Status: "Backlog"}, Stage: model.StageInTest}
	fs := rules.Flags(c, now, rules.Thresholds{})
	if len(fs) != 1 || fs[0].Reason != "Jira still Backlog, PR merged" {
		t.Errorf("flags %+v, want one mismatch with reason %q", fs, "Jira still Backlog, PR merged")
	}
}

// partialProd is a chain live in prodEnv since at and not yet in otherProd.
func partialProd(at time.Time) model.Chain {
	return model.Chain{Stage: model.StageAwaitingProd, PRs: []model.PR{{Repo: "acme/app", State: model.PRMerged, EffectiveSHA: "a"}},
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

func TestMismatchUsesJiraStatusCategoryKeys(t *testing.T) {
	// Adapters map Jira's stable statusCategory.key; with no status name the
	// reason falls back to the category's display label, not the raw key.
	c := model.Chain{Ticket: model.Ticket{StatusCategory: model.StatusInProgress}, Stage: model.StageMerged}
	fs := rules.Flags(c, now, rules.Thresholds{})
	if want := "Jira still In Progress, PR merged"; len(fs) != 1 || fs[0].Reason != want {
		t.Errorf("flags %+v, want one mismatch with reason %q", fs, want)
	}
	c.Ticket.StatusCategory = model.StatusToDo
	if fs := rules.Flags(c, now, rules.Thresholds{}); len(fs) != 1 || fs[0].Reason != "Jira still To Do, PR merged" {
		t.Errorf("flags %+v, want one mismatch naming To Do", fs)
	}
}

func TestZeroDoneGraceDisablesTheDoneMismatch(t *testing.T) {
	c := model.Chain{Ticket: model.Ticket{StatusCategory: model.StatusDone, StatusSince: now.Add(-72 * time.Hour)}, Stage: model.StageInTest,
		PRs: []model.PR{{Repo: "acme/app", State: model.PRMerged, EffectiveSHA: "a"}}}
	if fs := rules.Flags(c, now, rules.Thresholds{}); len(fs) != 0 {
		t.Errorf("flags %v, want none: DoneGrace 0 turns the check off, like the other thresholds", kinds(fs))
	}
}

func TestStrandedPRFlagTargetsItsPRAtRed(t *testing.T) {
	c := model.Chain{Stage: model.StageMerged, PRs: []model.PR{
		{Repo: "acme/app", Number: 3, State: model.PRMerged, StackPending: true, Stranded: true},
		{Repo: "acme/app", Number: 4, State: model.PRMerged, StackPending: true}, // waiting, not stranded
	}}
	fs := rules.Flags(c, now, rules.Thresholds{})
	if len(fs) != 1 {
		t.Fatalf("flags %v, want one stranded flag", kinds(fs))
	}
	if f := fs[0]; f.Kind != model.FlagStranded || f.Level != model.Red || f.PR == nil || f.PR.Number != 3 || f.Reason != "PR #3 merged into a dead branch" {
		t.Errorf("flag %+v, want red stranded on PR #3 with reason %q", f, "PR #3 merged into a dead branch")
	}
}

func TestDraftPRsSkipReviewFlags(t *testing.T) {
	th := rules.Thresholds{StaleReview: 48 * time.Hour}
	old := now.Add(-72 * time.Hour)
	cases := []struct {
		name string
		pr   model.PR
		want []model.FlagKind
	}{
		{"an approved, green draft is not ready to merge", model.PR{IsDraft: true, Checks: model.ChecksPassing, Review: model.ReviewApproved, Reviewers: 1, OpenedAt: old}, nil},
		{"a draft with no reviewer is not waiting on review", model.PR{IsDraft: true, Checks: model.ChecksPassing, OpenedAt: now}, nil},
		{"a draft past StaleReview is not stale", model.PR{IsDraft: true, Checks: model.ChecksPassing, Reviewers: 1, Review: model.ReviewRequired, OpenedAt: old}, nil},
		{"a draft with failing checks is still flagged", model.PR{IsDraft: true, Checks: model.ChecksFailing, OpenedAt: now}, []model.FlagKind{model.FlagCheckFailed}},
		{"a draft with changes requested is still flagged", model.PR{IsDraft: true, Checks: model.ChecksPassing, Review: model.ReviewChangesRequested, Reviewers: 1, OpenedAt: now},
			[]model.FlagKind{model.FlagChangesRequested}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.pr.State = model.PROpen
			got := kinds(rules.Flags(model.Chain{Stage: model.StagePROpen, PRs: []model.PR{tc.pr}}, now, th))
			if !slices.Equal(got, tc.want) {
				t.Errorf("flags %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCheckExpectedFlagIsYellowWithItsOwnReason(t *testing.T) {
	c := model.Chain{Stage: model.StagePROpen, PRs: []model.PR{{Number: 7, State: model.PROpen, Checks: model.ChecksExpected, OpenedAt: now}}}
	fs := rules.Flags(c, now, rules.Thresholds{})
	if len(fs) != 1 {
		t.Fatalf("flags %v, want one", kinds(fs))
	}
	if f := fs[0]; f.Kind != model.FlagCheckExpected || f.Level != model.Yellow || f.PR == nil || f.Reason != "PR #7 waiting on a required check" {
		t.Errorf("flag %+v, want yellow check expected on PR #7", f)
	}
}

func TestCheckExpectedSortsAfterPartialProdBeforeReadyToMerge(t *testing.T) {
	if model.FlagCheckExpected <= model.FlagPartialProd || model.FlagCheckExpected >= model.FlagReadyToMerge {
		t.Errorf("FlagCheckExpected = %d, want between FlagPartialProd and FlagReadyToMerge", model.FlagCheckExpected)
	}
}

func TestRejectedApprovalFlagIsRedPipelineFailed(t *testing.T) {
	rejected := &model.Deploy{ExecutionID: "exec3", Status: model.DeployRejected}
	failed := &model.Deploy{ExecutionID: "exec2", Status: model.DeployFailed}
	cases := []struct {
		deploy *model.Deploy
		reason string
	}{
		{rejected, "a Production approval rejected"},
		{failed, "a Production failed"},
		{nil, "a Production failed"},
	}
	for _, tc := range cases {
		c := model.Chain{Slots: []model.EnvSlot{{Env: prodEnv, Applies: true, State: model.SlotFailed, Deploy: tc.deploy}}}
		fs := rules.Flags(c, now, rules.Thresholds{})
		if len(fs) != 1 {
			t.Fatalf("flags %v, want one pipeline-failed flag", kinds(fs))
		}
		if f := fs[0]; f.Level != model.Red || f.Kind != model.FlagPipelineFailed || f.Slot == nil || f.Reason != tc.reason {
			t.Errorf("flag %+v, want red pipeline failed with reason %q", f, tc.reason)
		}
	}
}
