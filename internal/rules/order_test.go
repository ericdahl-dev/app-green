package rules_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/ericdahl-dev/app-green/internal/model"
	"github.com/ericdahl-dev/app-green/internal/rules"
)

func TestEvaluateSetsStageAndFlags(t *testing.T) {
	c := model.Chain{Ticket: model.Ticket{Key: "ABC-1"}, PRs: []model.PR{{State: model.PROpen, Checks: model.ChecksFailing}}}
	got := rules.Evaluate([]model.Chain{c}, now, rules.Thresholds{})
	if len(got) != 1 {
		t.Fatalf("want 1 row, got %d", len(got))
	}
	if got[0].Stage != model.StagePROpen || got[0].Level() != model.Red {
		t.Errorf("want stage %v at %v, got stage %v at %v", model.StagePROpen, model.Red, got[0].Stage, got[0].Level())
	}
}

func keysOf(cs []model.Chain) []string {
	var keys []string
	for _, c := range cs {
		keys = append(keys, c.Ticket.Key)
	}
	return keys
}

func TestEvaluateSortsRedThenYellowThenNone(t *testing.T) {
	none := model.Chain{Ticket: model.Ticket{Key: "ABC-1"}, PRs: []model.PR{{State: model.PROpen, Checks: model.ChecksPending, Reviewers: 1, OpenedAt: now}}}
	yellow := model.Chain{Ticket: model.Ticket{Key: "ABC-2"}, PRs: []model.PR{{State: model.PROpen, Checks: model.ChecksPassing, Review: model.ReviewApproved, Reviewers: 1}}}
	red := model.Chain{Ticket: model.Ticket{Key: "ABC-3"}, PRs: []model.PR{{State: model.PROpen, Checks: model.ChecksFailing}}}
	got := keysOf(rules.Evaluate([]model.Chain{none, yellow, red}, now, rules.Thresholds{}))
	if want := []string{"ABC-3", "ABC-2", "ABC-1"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("order %v, want %v (red, yellow, none)", got, want)
	}
}

func TestEvaluateSortsFurthestStageFirstWithinALevel(t *testing.T) {
	started := model.Chain{Ticket: model.Ticket{Key: "ABC-1"}}
	inTest := model.Chain{Ticket: model.Ticket{Key: "ABC-2"}, PRs: []model.PR{{State: model.PRMerged, EffectiveSHA: "a"}},
		Slots: []model.EnvSlot{slot(testEnv, model.SlotDeployed)}}
	got := keysOf(rules.Evaluate([]model.Chain{started, inTest}, now, rules.Thresholds{}))
	if want := []string{"ABC-2", "ABC-1"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("order %v, want %v (in test before started)", got, want)
	}
}

func TestEvaluateBreaksTiesByTicketKeyInNumberOrder(t *testing.T) {
	chains := []model.Chain{{Ticket: model.Ticket{Key: "ABC-10"}}, {Ticket: model.Ticket{Key: "ABC-9"}}, {Ticket: model.Ticket{Key: "ABC-2"}}}
	got := keysOf(rules.Evaluate(chains, now, rules.Thresholds{}))
	if want := []string{"ABC-2", "ABC-9", "ABC-10"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("order %v, want %v (ABC-9 before ABC-10)", got, want)
	}
}

func inProdSince(key string, at time.Time) model.Chain {
	return model.Chain{Ticket: model.Ticket{Key: key}, PRs: []model.PR{{State: model.PRMerged, EffectiveSHA: "a"}},
		Slots: []model.EnvSlot{{Env: prodEnv, Applies: true, State: model.SlotDeployed, At: at}}}
}

func TestEvaluateFadesCleanRowsInProdLongerThanFadeAfter(t *testing.T) {
	th := rules.Thresholds{FadeAfter: 24 * time.Hour}
	got := keysOf(rules.Evaluate([]model.Chain{inProdSince("ABC-1", now.Add(-30*time.Hour))}, now, th))
	if len(got) != 0 {
		t.Errorf("rows %v, want none: a clean row in prod for 30h should fade after 24h", got)
	}
}

func TestEvaluateKeepsRowsInProdWithinFadeAfter(t *testing.T) {
	th := rules.Thresholds{FadeAfter: 24 * time.Hour}
	got := keysOf(rules.Evaluate([]model.Chain{inProdSince("ABC-1", now.Add(-time.Hour))}, now, th))
	if want := []string{"ABC-1"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("rows %v, want %v: a row in prod for 1h should not fade yet", got, want)
	}
}

func TestEvaluateNeverFadesAFlaggedRowInProd(t *testing.T) {
	th := rules.Thresholds{FadeAfter: 24 * time.Hour}
	c := inProdSince("ABC-1", now.Add(-30*time.Hour))
	c.Slots[0].Health = model.Health{Known: true, Desired: 2, Healthy: 1}
	got := rules.Evaluate([]model.Chain{c}, now, th)
	if len(got) != 1 || got[0].Stage != model.StageInProd || got[0].Level() != model.Red {
		t.Errorf("got %d rows, want ABC-1 kept at %v with a red unhealthy flag", len(got), model.StageInProd)
	}
}

func TestEvaluateSortsAndFades(t *testing.T) {
	th := rules.Thresholds{StaleReview: 48 * time.Hour, FadeAfter: 24 * time.Hour}
	inProdOld := inProdSince("ABC-1", now.Add(-30*time.Hour))
	inProdNew := inProdSince("ABC-2", now.Add(-time.Hour))
	red := model.Chain{Ticket: model.Ticket{Key: "ABC-3"}, PRs: []model.PR{{State: model.PROpen, Checks: model.ChecksFailing}}}
	started := model.Chain{Ticket: model.Ticket{Key: "ABC-4"}}

	got := keysOf(rules.Evaluate([]model.Chain{started, inProdOld, inProdNew, red}, now, th))
	want := []string{"ABC-3", "ABC-2", "ABC-4"} // red first; then furthest stage; ABC-1 faded
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("order %v, want %v", got, want)
	}
}

func TestEvaluateDoesNotFadeUntilEveryProdEnvHasIt(t *testing.T) {
	th := rules.Thresholds{FadeAfter: 24 * time.Hour}
	c := inProdSince("ABC-1", now.Add(-30*time.Hour))
	c.Slots = append(c.Slots, model.EnvSlot{Env: otherProd, Applies: true, State: model.SlotNotYet})
	got := rules.Evaluate([]model.Chain{c}, now, th)
	if len(got) != 1 || got[0].Stage != model.StageAwaitingProd {
		t.Errorf("got %v, want ABC-1 kept at %v: the second prod Env does not have it yet", keysOf(got), model.StageAwaitingProd)
	}
}

func TestEvaluateNeverFadesAProdDeployWithNoTime(t *testing.T) {
	th := rules.Thresholds{FadeAfter: 24 * time.Hour}
	got := keysOf(rules.Evaluate([]model.Chain{inProdSince("ABC-1", time.Time{})}, now, th))
	if want := []string{"ABC-1"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("rows %v, want %v: with no deploy time we cannot know it has been in prod long enough", got, want)
	}
}

func TestEvaluateFadeIgnoresProdEnvsThatDoNotApply(t *testing.T) {
	th := rules.Thresholds{FadeAfter: 24 * time.Hour}
	c := inProdSince("ABC-1", now.Add(-30*time.Hour))
	c.Slots = append(c.Slots, model.EnvSlot{Env: otherProd, State: model.SlotDeployed, At: now.Add(-time.Hour)})
	if got := keysOf(rules.Evaluate([]model.Chain{c}, now, th)); len(got) != 0 {
		t.Errorf("rows %v, want none: only the applicable prod Env's 30h counts, not another repo's 1h-old deploy", got)
	}
}

func TestEvaluateBreaksTiesByProjectThenNumber(t *testing.T) {
	chains := []model.Chain{{Ticket: model.Ticket{Key: "XY-2"}}, {Ticket: model.Ticket{Key: "ABC-10"}}, {Ticket: model.Ticket{Key: "ABC-9"}}}
	got := keysOf(rules.Evaluate(chains, now, rules.Thresholds{}))
	if want := []string{"ABC-9", "ABC-10", "XY-2"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("order %v, want %v (project first, then number)", got, want)
	}
}
