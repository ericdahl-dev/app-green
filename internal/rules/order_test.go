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
		t.Fatalf("rows %s, want exactly ABC-1", describe(got))
	}
	if got[0].Stage != model.StagePROpen || got[0].Level() != model.Red {
		t.Errorf("rows %s, want ABC-1 (PR open, red)", describe(got))
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
	rows := rules.Evaluate([]model.Chain{none, yellow, red}, now, rules.Thresholds{})
	got := keysOf(rows)
	if want := []string{"ABC-3", "ABC-2", "ABC-1"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("order %s, want %v (red, yellow, none)", describe(rows), want)
	}
}

func TestEvaluateSortsFurthestStageFirstWithinALevel(t *testing.T) {
	started := model.Chain{Ticket: model.Ticket{Key: "ABC-1"}}
	inTest := model.Chain{Ticket: model.Ticket{Key: "ABC-2"}, PRs: []model.PR{{State: model.PRMerged, EffectiveSHA: "a"}},
		Slots: []model.EnvSlot{slot(testEnv, model.SlotDeployed)}}
	rows := rules.Evaluate([]model.Chain{started, inTest}, now, rules.Thresholds{})
	got := keysOf(rows)
	if want := []string{"ABC-2", "ABC-1"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("order %s, want %v (in test before started)", describe(rows), want)
	}
}

func inProdSince(key string, at time.Time) model.Chain {
	return model.Chain{Ticket: model.Ticket{Key: key}, PRs: []model.PR{{State: model.PRMerged, EffectiveSHA: "a"}},
		Slots: []model.EnvSlot{{Env: prodEnv, Applies: true, State: model.SlotDeployed, At: at}}}
}

func TestEvaluateFadesCleanRowsInProdLongerThanFadeAfter(t *testing.T) {
	th := rules.Thresholds{FadeAfter: 24 * time.Hour}
	rows := rules.Evaluate([]model.Chain{inProdSince("ABC-1", now.Add(-30*time.Hour))}, now, th)
	got := keysOf(rows)
	if len(got) != 0 {
		t.Errorf("rows %s, want none: a clean row in prod for 30h should fade after 24h", describe(rows))
	}
}

func TestEvaluateKeepsRowsInProdWithinFadeAfter(t *testing.T) {
	th := rules.Thresholds{FadeAfter: 24 * time.Hour}
	rows := rules.Evaluate([]model.Chain{inProdSince("ABC-1", now.Add(-time.Hour))}, now, th)
	got := keysOf(rows)
	if want := []string{"ABC-1"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("rows %s, want %v: a row in prod for 1h should not fade yet", describe(rows), want)
	}
}

func TestEvaluateNeverFadesAFlaggedRowInProd(t *testing.T) {
	th := rules.Thresholds{FadeAfter: 24 * time.Hour}
	c := inProdSince("ABC-1", now.Add(-30*time.Hour))
	c.Slots[0].Health = model.Health{Known: true, Desired: 2, Healthy: 1}
	got := rules.Evaluate([]model.Chain{c}, now, th)
	if len(got) != 1 || got[0].Stage != model.StageInProd || got[0].Level() != model.Red {
		t.Errorf("rows %s, want ABC-1 kept at %v with a red unhealthy flag", describe(got), model.StageInProd)
	}
}

func TestEvaluateSortsAndFades(t *testing.T) {
	th := rules.Thresholds{StaleReview: 48 * time.Hour, FadeAfter: 24 * time.Hour}
	inProdOld := inProdSince("ABC-1", now.Add(-30*time.Hour))
	inProdNew := inProdSince("ABC-2", now.Add(-time.Hour))
	red := model.Chain{Ticket: model.Ticket{Key: "ABC-3"}, PRs: []model.PR{{State: model.PROpen, Checks: model.ChecksFailing}}}
	started := model.Chain{Ticket: model.Ticket{Key: "ABC-4"}}

	rows := rules.Evaluate([]model.Chain{started, inProdOld, inProdNew, red}, now, th)
	got := keysOf(rows)
	want := []string{"ABC-3", "ABC-2", "ABC-4"} // red first; then furthest stage; ABC-1 faded
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("order %s, want %v", describe(rows), want)
	}
}

func TestEvaluateDoesNotFadeUntilEveryProdEnvHasIt(t *testing.T) {
	th := rules.Thresholds{FadeAfter: 24 * time.Hour}
	c := inProdSince("ABC-1", now.Add(-30*time.Hour))
	c.Slots = append(c.Slots, model.EnvSlot{Env: otherProd, Applies: true, State: model.SlotNotYet})
	got := rules.Evaluate([]model.Chain{c}, now, th)
	if len(got) != 1 || got[0].Stage != model.StageAwaitingProd {
		t.Errorf("got %s, want ABC-1 kept at %v: the second prod Env does not have it yet", describe(got), model.StageAwaitingProd)
	}
}

func TestEvaluateNeverFadesAProdDeployWithNoTime(t *testing.T) {
	th := rules.Thresholds{FadeAfter: 24 * time.Hour}
	rows := rules.Evaluate([]model.Chain{inProdSince("ABC-1", time.Time{})}, now, th)
	got := keysOf(rows)
	if want := []string{"ABC-1"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("rows %s, want %v: with no deploy time we cannot know it has been in prod long enough", describe(rows), want)
	}
}

func TestEvaluateFadeIgnoresProdEnvsThatDoNotApply(t *testing.T) {
	th := rules.Thresholds{FadeAfter: 24 * time.Hour}
	c := inProdSince("ABC-1", now.Add(-30*time.Hour))
	c.Slots = append(c.Slots, model.EnvSlot{Env: otherProd, State: model.SlotDeployed, At: now.Add(-time.Hour)})
	if rows := rules.Evaluate([]model.Chain{c}, now, th); len(rows) != 0 {
		t.Errorf("rows %s, want none: only the applicable prod Env's 30h counts, not another repo's 1h-old deploy", describe(rows))
	}
}

func TestEvaluateBreaksTiesByProjectThenNumber(t *testing.T) {
	chains := []model.Chain{{Ticket: model.Ticket{Key: "XY-2"}}, {Ticket: model.Ticket{Key: "ABC-10"}}, {Ticket: model.Ticket{Key: "ABC-9"}}}
	rows := rules.Evaluate(chains, now, rules.Thresholds{})
	got := keysOf(rows)
	if want := []string{"ABC-9", "ABC-10", "XY-2"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("order %s, want %v (project first, then number)", describe(rows), want)
	}
}

func TestEvaluateFadeCountsFromTheLatestProdDeploy(t *testing.T) {
	th := rules.Thresholds{FadeAfter: 24 * time.Hour}
	c := inProdSince("ABC-1", now.Add(-30*time.Hour))
	c.Slots = append(c.Slots, model.EnvSlot{Env: otherProd, Applies: true, State: model.SlotDeployed, At: now.Add(-time.Hour)})
	got := rules.Evaluate([]model.Chain{c}, now, th)
	if len(got) != 1 {
		t.Errorf("rows %s, want ABC-1 kept: the second prod Env went live 1h ago", describe(got))
	}
}

func TestEvaluateNeverFadesAYellowRowInProd(t *testing.T) {
	th := rules.Thresholds{FadeAfter: 24 * time.Hour}
	c := inProdSince("ABC-1", now.Add(-30*time.Hour))
	c.Ticket.StatusCategory = "In Progress"
	got := rules.Evaluate([]model.Chain{c}, now, th)
	if len(got) != 1 || got[0].Level() != model.Yellow {
		t.Errorf("rows %s, want ABC-1 kept at yellow: Jira still In Progress", describe(got))
	}
}

// describe renders rows as "KEY (stage, level)" for failure messages.
func describe(cs []model.Chain) string {
	levels := map[model.Level]string{model.None: "none", model.Yellow: "yellow", model.Red: "red"}
	var out []string
	for _, c := range cs {
		out = append(out, fmt.Sprintf("%s (%v, %s)", c.Ticket.Key, c.Stage, levels[c.Level()]))
	}
	return fmt.Sprint(out)
}

func TestEvaluateKeyOrderIsTotal(t *testing.T) {
	chains := []model.Chain{{Ticket: model.Ticket{Key: "AAA"}}, {Ticket: model.Ticket{Key: "ABC-1"}}, {Ticket: model.Ticket{Key: "ABC-01"}}, {Ticket: model.Ticket{Key: "XY-2"}}}
	rows := rules.Evaluate(chains, now, rules.Thresholds{})
	got := keysOf(rows)
	if want := []string{"ABC-01", "ABC-1", "XY-2", "AAA"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("order %s, want %v (parseable keys first; same project and number fall back to text)", describe(rows), want)
	}
}
