package rules_test

import (
	"testing"

	"github.com/ericdahl-dev/app-green/internal/model"
	"github.com/ericdahl-dev/app-green/internal/rules"
)

var (
	testEnv = model.Env{Account: "a", Stage: "Test"}
	prodEnv = model.Env{Account: "a", Stage: "Production", Prod: true}
	// otherProd is a second prod Env, e.g. another account's pipeline.
	otherProd = model.Env{Account: "b", Stage: "Production", Prod: true}
)

func slot(e model.Env, st model.SlotState) model.EnvSlot {
	return model.EnvSlot{Env: e, State: st, Applies: true}
}

func TestStage(t *testing.T) {
	open := model.PR{State: model.PROpen}
	merged := model.PR{State: model.PRMerged, EffectiveSHA: "a"}
	cases := []struct {
		name string
		c    model.Chain
		want model.Stage
	}{
		{"a prod Env that deploys none of the chain's repos is ignored", model.Chain{PRs: []model.PR{merged},
			Slots: []model.EnvSlot{slot(testEnv, model.SlotDeployed), slot(prodEnv, model.SlotAwaitingApproval),
				{Env: otherProd, State: model.SlotDeployed}}}, model.StageAwaitingProd},
		{"in prod in one prod Env but not yet in another is awaiting prod", model.Chain{PRs: []model.PR{merged},
			Slots: []model.EnvSlot{slot(testEnv, model.SlotDeployed), slot(prodEnv, model.SlotDeployed), slot(otherProd, model.SlotNotYet)}}, model.StageAwaitingProd},
		{"deployed in every prod Env is in prod", model.Chain{PRs: []model.PR{merged},
			Slots: []model.EnvSlot{slot(testEnv, model.SlotDeployed), slot(prodEnv, model.SlotDeployed), slot(otherProd, model.SlotDeployed)}}, model.StageInProd},
		{"a ticket with no PRs has only started", model.Chain{}, model.StageStarted},
		{"an open PR puts the ticket at PR open", model.Chain{PRs: []model.PR{open}}, model.StagePROpen},
		{"one open PR holds a merged one back at PR open", model.Chain{PRs: []model.PR{merged, open}}, model.StagePROpen},
		{"a merged PR not deployed anywhere yet is merged", model.Chain{PRs: []model.PR{merged},
			Slots: []model.EnvSlot{slot(testEnv, model.SlotNotYet), slot(prodEnv, model.SlotNotYet)}}, model.StageMerged},
		{"deployed to test, not prod, is in test", model.Chain{PRs: []model.PR{merged},
			Slots: []model.EnvSlot{slot(testEnv, model.SlotDeployed), slot(prodEnv, model.SlotNotYet)}}, model.StageInTest},
		{"prod waiting for approval is awaiting prod", model.Chain{PRs: []model.PR{merged},
			Slots: []model.EnvSlot{slot(testEnv, model.SlotDeployed), slot(prodEnv, model.SlotAwaitingApproval)}}, model.StageAwaitingProd},
		{"a prod run in progress is awaiting prod", model.Chain{PRs: []model.PR{merged},
			Slots: []model.EnvSlot{slot(testEnv, model.SlotDeployed), slot(prodEnv, model.SlotInProgress)}}, model.StageAwaitingProd},
		{"deployed to prod is in prod", model.Chain{PRs: []model.PR{merged},
			Slots: []model.EnvSlot{slot(testEnv, model.SlotDeployed), slot(prodEnv, model.SlotDeployed)}}, model.StageInProd},
		{"unhealthy in prod is still in prod (a flag says what is wrong)", model.Chain{PRs: []model.PR{merged},
			Slots: []model.EnvSlot{slot(testEnv, model.SlotDeployed),
				{Env: prodEnv, Applies: true, State: model.SlotDeployed, Health: model.Health{Known: true, Desired: 2, Healthy: 0}}}}, model.StageInProd},
		{"an unknown prod slot does not advance past test", model.Chain{PRs: []model.PR{merged},
			Slots: []model.EnvSlot{slot(testEnv, model.SlotDeployed), slot(prodEnv, model.SlotUnknown)}}, model.StageInTest},
		{"a failed prod slot does not advance past test", model.Chain{PRs: []model.PR{merged},
			Slots: []model.EnvSlot{slot(testEnv, model.SlotDeployed), slot(prodEnv, model.SlotFailed)}}, model.StageInTest},
		{"unknown and failed slots leave a merged PR at merged", model.Chain{PRs: []model.PR{merged},
			Slots: []model.EnvSlot{slot(testEnv, model.SlotUnknown), slot(prodEnv, model.SlotFailed)}}, model.StageMerged},
		{"test awaiting approval or in progress is not in test yet", model.Chain{PRs: []model.PR{merged},
			Slots: []model.EnvSlot{slot(testEnv, model.SlotInProgress), {Env: model.Env{Account: "b", Stage: "Test"}, Applies: true, State: model.SlotAwaitingApproval}}}, model.StageMerged},
		{"a merged PR still pending in a stack counts as merged", model.Chain{PRs: []model.PR{{State: model.PRMerged, StackPending: true}}}, model.StageMerged},
		{"a stack-pending PR holds the stage at merged even when another PR is in prod", model.Chain{
			PRs:   []model.PR{merged, {State: model.PRMerged, StackPending: true}},
			Slots: []model.EnvSlot{slot(testEnv, model.SlotDeployed), slot(prodEnv, model.SlotDeployed)}}, model.StageMerged},
	}
	for _, c := range cases {
		if got := rules.Stage(c.c); got != c.want {
			t.Errorf("%s: Stage = %v, want %v", c.name, got, c.want)
		}
	}
}
