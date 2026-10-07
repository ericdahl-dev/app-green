package rules_test

import (
	"testing"

	"github.com/ericdahl-dev/app-green/internal/model"
	"github.com/ericdahl-dev/app-green/internal/rules"
)

var (
	testEnv = model.Env{Account: "a", Stage: "Test", Repos: []string{"acme/app"}}
	prodEnv = model.Env{Account: "a", Stage: "Production", Prod: true, Repos: []string{"acme/app"}}
	// otherProd is a second prod Env, e.g. another account's pipeline.
	otherProd = model.Env{Account: "b", Stage: "Production", Prod: true, Repos: []string{"acme/app"}}
)

func slot(e model.Env, st model.SlotState) model.EnvSlot {
	return model.EnvSlot{Env: e, State: st, Applies: true}
}

func TestStage(t *testing.T) {
	open := model.PR{State: model.PROpen}
	merged := model.PR{Repo: "acme/app", State: model.PRMerged, EffectiveSHA: "a"}
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
		{"a deployed test Env that deploys none of the chain's repos does not give in test", model.Chain{PRs: []model.PR{merged},
			Slots: []model.EnvSlot{{Env: testEnv, State: model.SlotDeployed}}}, model.StageMerged},
		{"a ticket with no PRs has only started", model.Chain{}, model.StageStarted},
		{"a ticket whose PRs are all closed has only started", model.Chain{PRs: []model.PR{{State: model.PRClosed}}}, model.StageStarted},
		{"a closed PR beside a merged one does not count", model.Chain{PRs: []model.PR{{State: model.PRClosed}, merged},
			Slots: []model.EnvSlot{slot(testEnv, model.SlotDeployed)}}, model.StageInTest},
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
		{"a rolled-back prod slot does not advance past test", model.Chain{PRs: []model.PR{merged},
			Slots: []model.EnvSlot{slot(testEnv, model.SlotDeployed), slot(prodEnv, model.SlotRolledBack)}}, model.StageInTest},
		{"a rolled-back test slot leaves a merged PR at merged", model.Chain{PRs: []model.PR{merged},
			Slots: []model.EnvSlot{slot(testEnv, model.SlotRolledBack)}}, model.StageMerged},
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

func TestStageUnclaimedRepoCapsAtAwaitingProd(t *testing.T) {
	// acme/app is deployed in every prod Env, but acme/other is merged and no
	// configured Env deploys it: the chain is not in prod.
	c := model.Chain{PRs: []model.PR{
		{Repo: "acme/app", State: model.PRMerged, EffectiveSHA: "a"},
		{Repo: "acme/other", State: model.PRMerged, EffectiveSHA: "b"},
	}, Slots: []model.EnvSlot{slot(testEnv, model.SlotDeployed), slot(prodEnv, model.SlotDeployed)}}
	if got := rules.Stage(c); got != model.StageAwaitingProd {
		t.Errorf("Stage = %v, want %v: acme/other is deployed nowhere", got, model.StageAwaitingProd)
	}
	// With no Env.Repos anywhere (history fallback) rules cannot tell: in prod.
	c.Slots[0].Env.Repos, c.Slots[1].Env.Repos = nil, nil
	if got := rules.Stage(c); got != model.StageInProd {
		t.Errorf("Stage = %v, want %v in history-fallback mode", got, model.StageInProd)
	}
}
