package ui_test

import (
	"testing"

	"github.com/ericdahl-dev/app-green/internal/model"
	"github.com/ericdahl-dev/app-green/internal/ui"
)

func TestRowAction(t *testing.T) {
	rspec := model.Check{Name: "rspec", RunID: 42}
	codeql := model.Check{Name: "CodeQL", RunID: 43, CodeScanning: true}
	external := model.Check{Name: "ci/external"}
	failingPR := func(cs ...model.Check) *model.PR {
		return &model.PR{Number: 3, State: model.PROpen, Checks: model.ChecksFailing, Failing: cs}
	}
	waiting := func(readOnly bool, token string) *model.EnvSlot {
		return &model.EnvSlot{
			Env:    model.Env{Account: "prod-acct", Stage: "Production", ReadOnly: readOnly},
			State:  model.SlotAwaitingApproval,
			Deploy: &model.Deploy{Status: model.DeployAwaitingApproval, ApprovalToken: token},
		}
	}
	slot := &model.EnvSlot{Env: model.Env{Account: "stage-acct", Stage: "Test"}}
	pr := &model.PR{Number: 3}
	tests := []struct {
		name string
		flag *model.Flag
		want ui.Action
		key  string
	}{
		{"no flags", nil, ui.ActionNone, ""},
		{"re-runnable failed check", &model.Flag{Kind: model.FlagCheckFailed, PR: failingPR(rspec)}, ui.ActionRerun, "f"},
		{"two re-runnable failed checks", &model.Flag{Kind: model.FlagCheckFailed, PR: failingPR(rspec, model.Check{Name: "lint", RunID: 44})}, ui.ActionRerun, "f"},
		{"code scanning failed", &model.Flag{Kind: model.FlagCheckFailed, PR: failingPR(codeql)}, ui.ActionOpen, "o"},
		{"code scanning beside a re-runnable check", &model.Flag{Kind: model.FlagCheckFailed, PR: failingPR(rspec, codeql)}, ui.ActionOpen, "o"},
		{"failed check outside Actions", &model.Flag{Kind: model.FlagCheckFailed, PR: failingPR(external)}, ui.ActionNone, ""},
		{"failed check with no details", &model.Flag{Kind: model.FlagCheckFailed, PR: failingPR()}, ui.ActionNone, ""},
		{"failed check without a PR", &model.Flag{Kind: model.FlagCheckFailed}, ui.ActionNone, ""},
		{"pipeline failed", &model.Flag{Kind: model.FlagPipelineFailed, Slot: slot}, ui.ActionOpen, "o"},
		{"rolled back", &model.Flag{Kind: model.FlagRolledBack, Slot: slot}, ui.ActionOpen, "o"},
		{"stranded", &model.Flag{Kind: model.FlagStranded, PR: pr}, ui.ActionOpen, "o"},
		{"check expected", &model.Flag{Kind: model.FlagCheckExpected, PR: pr}, ui.ActionOpen, "o"},
		{"deploy unknown in an env", &model.Flag{Kind: model.FlagDeployUnknown, Slot: slot}, ui.ActionOpen, "o"},
		{"repo no env deploys", &model.Flag{Kind: model.FlagDeployUnknown}, ui.ActionNone, ""},
		{"pipeline failed without a slot", &model.Flag{Kind: model.FlagPipelineFailed}, ui.ActionNone, ""},
		{"awaiting approval", &model.Flag{Kind: model.FlagAwaitingApproval, Slot: waiting(false, "tok")}, ui.ActionApprove, "a/x"},
		{"awaiting approval, read-only profile", &model.Flag{Kind: model.FlagAwaitingApproval, Slot: waiting(true, "tok")}, ui.ActionNone, ""},
		{"awaiting approval, no token", &model.Flag{Kind: model.FlagAwaitingApproval, Slot: waiting(false, "")}, ui.ActionNone, ""},
		{"awaiting approval, no deploy", &model.Flag{Kind: model.FlagAwaitingApproval, Slot: &model.EnvSlot{State: model.SlotAwaitingApproval}}, ui.ActionNone, ""},
		{"status mismatch", &model.Flag{Kind: model.FlagStatusMismatch}, ui.ActionStatus, "t"},
		{"unhealthy", &model.Flag{Kind: model.FlagUnhealthy, Slot: slot}, ui.ActionNone, ""},
		{"changes requested", &model.Flag{Kind: model.FlagChangesRequested, PR: pr}, ui.ActionNone, ""},
		{"partial prod", &model.Flag{Kind: model.FlagPartialProd, Slot: slot}, ui.ActionNone, ""},
		{"ready to merge", &model.Flag{Kind: model.FlagReadyToMerge, PR: pr}, ui.ActionNone, ""},
		{"stale review", &model.Flag{Kind: model.FlagStaleReview, PR: pr}, ui.ActionNone, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var c model.Chain
			if tt.flag != nil {
				// A second flag must not change the answer: only Flags[0] counts.
				c.Flags = []model.Flag{*tt.flag, {Kind: model.FlagStatusMismatch}}
			}
			got := ui.RowAction(c)
			if got != tt.want {
				t.Errorf("RowAction = %v, want %v", got, tt.want)
			}
			if k := got.Key(); k != tt.key {
				t.Errorf("Key = %q, want %q", k, tt.key)
			}
		})
	}
}
