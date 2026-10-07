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
	externalURL := model.Check{Name: "ci/external", URL: "https://ci.example.com/acme/app/1"}
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
		{"code scanning beside a re-runnable check", &model.Flag{Kind: model.FlagCheckFailed, PR: failingPR(rspec, codeql)}, ui.ActionRerun, "f"},
		{"outside Actions beside a re-runnable check", &model.Flag{Kind: model.FlagCheckFailed, PR: failingPR(external, rspec)}, ui.ActionRerun, "f"},
		{"failed check outside Actions with a URL", &model.Flag{Kind: model.FlagCheckFailed, PR: failingPR(externalURL)}, ui.ActionOpen, "o"},
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
		{"awaiting approval, read-only profile", &model.Flag{Kind: model.FlagAwaitingApproval, Slot: waiting(true, "tok")}, ui.ActionOpen, "o"},
		{"awaiting approval, no token", &model.Flag{Kind: model.FlagAwaitingApproval, Slot: waiting(false, "")}, ui.ActionOpen, "o"},
		{"awaiting approval, no deploy", &model.Flag{Kind: model.FlagAwaitingApproval, Slot: &model.EnvSlot{State: model.SlotAwaitingApproval}}, ui.ActionOpen, "o"},
		{"awaiting approval without a slot", &model.Flag{Kind: model.FlagAwaitingApproval}, ui.ActionNone, ""},
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

// TestRowActionNeedsOneTarget: when the row's flags give the first flag's key
// two targets, the key is not offered; the detail screen chooses.
func TestRowActionNeedsOneTarget(t *testing.T) {
	failing := func(n int, cs ...model.Check) *model.PR {
		return &model.PR{Repo: "acme/app", Number: n, State: model.PROpen, Checks: model.ChecksFailing, Failing: cs}
	}
	approval := func(account string, readOnly bool) *model.EnvSlot {
		return &model.EnvSlot{
			Env:    model.Env{Account: account, Pipeline: "app", Stage: "Production", ReadOnly: readOnly},
			State:  model.SlotAwaitingApproval,
			Deploy: &model.Deploy{Status: model.DeployAwaitingApproval, ApprovalToken: "tok"},
		}
	}
	rspec := model.Check{Name: "rspec", RunID: 42}
	codeql := model.Check{Name: "CodeQL", RunID: 43, CodeScanning: true}
	failedSlot := &model.EnvSlot{Env: model.Env{Account: "stage-acct", Pipeline: "app", Stage: "Test"}, State: model.SlotFailed}
	pr3 := &model.PR{Repo: "acme/app", Number: 3}
	tests := []struct {
		name  string
		flags []model.Flag
		want  ui.Action
	}{
		{"two approvals waiting", []model.Flag{
			{Kind: model.FlagAwaitingApproval, Slot: approval("stage-acct", false)},
			{Kind: model.FlagAwaitingApproval, Slot: approval("prod-acct", false)},
		}, ui.ActionNone},
		{"approval beside one the profile cannot approve", []model.Flag{
			{Kind: model.FlagAwaitingApproval, Slot: approval("stage-acct", false)},
			{Kind: model.FlagAwaitingApproval, Slot: approval("prod-acct", true)},
		}, ui.ActionApprove},
		{"re-runnable checks in two PRs", []model.Flag{
			{Kind: model.FlagCheckFailed, PR: failing(3, rspec)},
			{Kind: model.FlagCheckFailed, PR: failing(4, rspec)},
		}, ui.ActionNone},
		{"re-runnable check beside a PR with only code scanning", []model.Flag{
			{Kind: model.FlagCheckFailed, PR: failing(3, rspec)},
			{Kind: model.FlagCheckFailed, PR: failing(4, codeql)},
		}, ui.ActionRerun},
		{"two things to open", []model.Flag{
			{Kind: model.FlagPipelineFailed, Slot: failedSlot},
			{Kind: model.FlagCheckExpected, PR: pr3},
		}, ui.ActionNone},
		{"the same target twice", []model.Flag{
			{Kind: model.FlagCheckExpected, PR: pr3},
			{Kind: model.FlagStranded, PR: &model.PR{Repo: "acme/app", Number: 3}},
		}, ui.ActionOpen},
		{"a second key elsewhere does not count", []model.Flag{
			{Kind: model.FlagPipelineFailed, Slot: failedSlot},
			{Kind: model.FlagAwaitingApproval, Slot: approval("prod-acct", false)},
			{Kind: model.FlagStatusMismatch},
		}, ui.ActionOpen},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ui.RowAction(model.Chain{Flags: tt.flags}); got != tt.want {
				t.Errorf("RowAction = %v, want %v", got, tt.want)
			}
		})
	}
}
