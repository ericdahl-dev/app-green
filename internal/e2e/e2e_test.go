// Package e2e_test runs link.Link -> link.Slots -> rules.Evaluate on
// realistic synthetic chains: two accounts, each with a Test and a Production
// Env, deploying acme/app.
package e2e_test

import (
	"slices"
	"testing"
	"time"

	"github.com/ericdahl-dev/app-green/internal/link"
	"github.com/ericdahl-dev/app-green/internal/model"
	"github.com/ericdahl-dev/app-green/internal/rules"
)

const repo = "acme/app"

var (
	now = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	th  = rules.Thresholds{StaleReview: 48 * time.Hour, DoneGrace: 24 * time.Hour, FadeAfter: 72 * time.Hour, PartialProd: 4 * time.Hour}

	stageTest = env("stage-acct", "Test", 0, false)
	stageProd = env("stage-acct", "Production", 1, true)
	prodTest  = env("prod-acct", "Test", 2, false)
	prodProd  = env("prod-acct", "Production", 3, true)

	// done is Done in Jira an hour ago, well inside DoneGrace, so a chain that
	// is not in prod yet raises no status mismatch and flags come only from
	// what the PRs and deploys say.
	done = model.Ticket{Key: "ABC-1", Title: "x", Status: "Done", StatusCategory: model.StatusDone,
		Updated: now.Add(-time.Hour), StatusSince: now.Add(-time.Hour)}
)

func env(acct, stage string, order int, prod bool) model.Env {
	return model.Env{Account: acct, Pipeline: "app-pipe", Stage: stage, Order: order, Prod: prod, Repos: []string{repo}}
}

func deploy(st model.DeployStatus, sha string, at time.Time) model.Deploy {
	d := model.Deploy{ExecutionID: sha + at.Format("150405"), Status: st, Revisions: map[string]string{repo: sha}, FinishedAt: at}
	if st == model.DeployAwaitingApproval {
		d.ApprovalToken = "tok-" + sha
	}
	return d
}

func ok(sha string, at time.Time) model.Deploy { return deploy(model.DeploySucceeded, sha, at) }

// main's history, oldest first: z, a, b, c. A commit contains every older one.
var ancestry = map[string]int{"z": 0, "a": 1, "b": 2, "c": 3}

func compare(t *testing.T) model.CompareFunc {
	return func(r, base, head string) model.Inclusion {
		bi, bok := ancestry[base]
		hi, hok := ancestry[head]
		if r != repo || !bok || !hok {
			t.Fatalf("compare outside the fixture: %s %s...%s", r, base, head)
		}
		if bi <= hi {
			return model.Included
		}
		return model.NotIncluded
	}
}

func merged(n int, head, base, sha string, at time.Time) model.PR {
	return model.PR{Repo: repo, DefaultBranch: "main", Number: n, Title: "ABC-1: part", HeadRef: head, BaseRef: base,
		State: model.PRMerged, MergeSHA: sha, MergedAt: at, OpenedAt: at.Add(-24 * time.Hour),
		Checks: model.ChecksPassing, Review: model.ReviewApproved, Reviewers: 1}
}

// squash is PR #1, squash-merged into main as a ten hours ago.
var squash = merged(1, "abc-1", "main", "a", now.Add(-10*time.Hour))

// everywhere is a deployed to all four Envs, prod last six hours ago.
func everywhere() []model.EnvHistory {
	return []model.EnvHistory{
		{Env: stageTest, Deploys: []model.Deploy{ok("a", now.Add(-9*time.Hour)), ok("z", now.Add(-48*time.Hour))}},
		{Env: stageProd, Deploys: []model.Deploy{ok("a", now.Add(-7*time.Hour)), ok("z", now.Add(-47*time.Hour))}},
		{Env: prodTest, Deploys: []model.Deploy{ok("a", now.Add(-8*time.Hour)), ok("z", now.Add(-46*time.Hour))}},
		{Env: prodProd, Deploys: []model.Deploy{ok("a", now.Add(-6*time.Hour)), ok("z", now.Add(-45*time.Hour))}},
	}
}

// run links prs to ticket, resolves its slots and evaluates the one row. ok
// is false when Evaluate dropped it.
func run(t *testing.T, ticket model.Ticket, prs []model.PR, hs []model.EnvHistory) (row model.Chain, ok bool) {
	t.Helper()
	return runWith(t, ticket, prs, nil, hs)
}

// runWith is run with context PRs: base-branch PRs used only for stack
// walking, as the GitHub adapter's BasePRs returns them.
func runWith(t *testing.T, ticket model.Ticket, prs, context []model.PR, hs []model.EnvHistory) (row model.Chain, ok bool) {
	t.Helper()
	chains, unlinked := link.Link([]model.Ticket{ticket}, prs, context, []string{"ABC"})
	if len(chains) != 1 || len(unlinked) != 0 {
		t.Fatalf("link: chains %d, unlinked %d, want 1 and 0", len(chains), len(unlinked))
	}
	rows := rules.Evaluate([]model.Chain{link.Slots(chains[0], hs, compare(t))}, now, th)
	if len(rows) == 0 {
		return model.Chain{}, false
	}
	return rows[0], true
}

func kinds(fs []model.Flag) []model.FlagKind {
	var out []model.FlagKind
	for _, f := range fs {
		out = append(out, f.Kind)
	}
	return out
}

func slotState(c model.Chain, e model.Env) model.SlotState {
	for _, s := range c.Slots {
		if s.Env.ID() == e.ID() {
			return s.State
		}
	}
	return -1
}

// want checks a row's stage, level and first flag (none when kind is -1).
func want(t *testing.T, c model.Chain, stage model.Stage, level model.Level, kind model.FlagKind) {
	t.Helper()
	if c.Stage != stage || c.Level() != level {
		t.Errorf("stage %v level %d flags %v, want stage %v level %d", c.Stage, c.Level(), kinds(c.Flags), stage, level)
	}
	switch {
	case kind < 0 && len(c.Flags) != 0:
		t.Errorf("flags %v, want none", kinds(c.Flags))
	case kind >= 0 && (len(c.Flags) == 0 || c.Flags[0].Kind != kind):
		t.Errorf("flags %v, want %v first", kinds(c.Flags), kind)
	}
}

func TestSquashMergeDeployedEverywhereIsInProd(t *testing.T) {
	c, ok := run(t, done, []model.PR{squash}, everywhere())
	if !ok {
		t.Fatal("row dropped, want it kept: prod went live 6h ago, inside FadeAfter")
	}
	want(t, c, model.StageInProd, model.None, -1)
}

func TestApprovalWaitInOneAccountIsAwaitingProd(t *testing.T) {
	hs := everywhere()
	hs[3].Deploys = []model.Deploy{deploy(model.DeployAwaitingApproval, "a", now.Add(-30*time.Minute)), ok("z", now.Add(-45*time.Hour))}
	c, _ := run(t, done, []model.PR{squash}, hs)
	want(t, c, model.StageAwaitingProd, model.Yellow, model.FlagAwaitingApproval)
	if f := c.Flags[0]; f.Slot == nil || f.Slot.Env.ID() != prodProd.ID() || f.Slot.Deploy == nil || f.Slot.Deploy.ApprovalToken != "tok-a" {
		t.Errorf("flag %+v, want it on prod-acct Production with the approval token", f)
	}
}

func TestProdHistoryMissingInOneAccountIsUnknown(t *testing.T) {
	hs := everywhere()
	hs[3].Deploys = nil
	c, _ := run(t, done, []model.PR{squash}, hs)
	if c.Stage == model.StageInProd {
		t.Errorf("stage %v, want short of in prod: prod-acct Production history is missing", c.Stage)
	}
	if !slices.Contains(kinds(c.Flags), model.FlagDeployUnknown) || c.Level() != model.Yellow {
		t.Errorf("level %d flags %v, want yellow with deploy unknown", c.Level(), kinds(c.Flags))
	}
	if got := slotState(c, prodProd); got != model.SlotUnknown {
		t.Errorf("prod-acct Production = %v, want unknown", got)
	}
}

func TestRollbackIsRed(t *testing.T) {
	hs := everywhere()
	hs[3].Deploys = []model.Deploy{ok("z", now.Add(-time.Hour)), ok("a", now.Add(-6*time.Hour))}
	c, _ := run(t, done, []model.PR{squash}, hs)
	want(t, c, model.StageAwaitingProd, model.Red, model.FlagRolledBack)
	if got := slotState(c, prodProd); got != model.SlotRolledBack {
		t.Errorf("prod-acct Production = %v, want rolled back", got)
	}
}

func TestBatchedRollbackIsRed(t *testing.T) {
	// a went to prod-acct Production only inside b, then z replaced b.
	hs := everywhere()
	hs[3].Deploys = []model.Deploy{ok("z", now.Add(-time.Hour)), ok("b", now.Add(-6*time.Hour))}
	c, _ := run(t, done, []model.PR{squash}, hs)
	want(t, c, model.StageAwaitingProd, model.Red, model.FlagRolledBack)
}

func TestStackedSeriesLandedIsInProd(t *testing.T) {
	// main <- s1 (#1) <- s2 (#2) <- s3 (#3). #3 and #2 merged into their bases
	// first, then #1 merged the whole stack into main as b.
	prs := []model.PR{
		merged(3, "s3", "s2", "s3-merge", now.Add(-13*time.Hour)),
		merged(2, "s2", "s1", "s2-merge", now.Add(-12*time.Hour)),
		merged(1, "s1", "main", "b", now.Add(-10*time.Hour)),
	}
	hs := everywhere()
	for i := range hs {
		hs[i].Deploys = append([]model.Deploy{ok("b", now.Add(-time.Duration(6+i)*time.Hour))}, hs[i].Deploys...)
	}
	c, ok := run(t, done, prs, hs)
	if !ok {
		t.Fatal("row dropped, want it kept inside FadeAfter")
	}
	want(t, c, model.StageInProd, model.None, -1)
}

func TestStackOnSomeoneElsesBaseIsInProd(t *testing.T) {
	// My #2 merged into s1; someone else's #1 (context, naming my key too)
	// then merged s1 into main as b, which is deployed everywhere.
	mine := []model.PR{merged(2, "s2", "s1", "q", now.Add(-12*time.Hour))}
	theirs := merged(1, "s1", "main", "b", now.Add(-10*time.Hour))
	hs := everywhere()
	for i := range hs {
		hs[i].Deploys = append([]model.Deploy{ok("b", now.Add(-time.Duration(6+i)*time.Hour))}, hs[i].Deploys...)
	}
	c, ok := runWith(t, done, mine, []model.PR{theirs}, hs)
	if !ok {
		t.Fatal("row dropped, want it kept inside FadeAfter")
	}
	want(t, c, model.StageInProd, model.None, -1)
	if len(c.PRs) != 1 || c.PRs[0].Number != 2 {
		t.Errorf("row PRs = %+v, want only my #2", c.PRs)
	}
}

func TestStrandedStackPRIsRed(t *testing.T) {
	// #1 merged s1 into main as a; #2 merged into s1 an hour later, so its
	// commits never reach main.
	prs := []model.PR{merged(1, "s1", "main", "a", now.Add(-10*time.Hour)), merged(2, "s2", "s1", "q", now.Add(-9*time.Hour))}
	c, _ := run(t, done, prs, everywhere())
	want(t, c, model.StageMerged, model.Red, model.FlagStranded)
	if f := c.Flags[0]; f.PR == nil || f.PR.Number != 2 {
		t.Errorf("flag %+v, want it on PR #2", f)
	}
}

func TestRepoCaseMismatchStillApplies(t *testing.T) {
	// prod-acct's config and deploys spell the repo differently from GitHub.
	hs := everywhere()
	for _, i := range []int{2, 3} {
		hs[i].Env.Repos = []string{"Acme/App"}
		for j := range hs[i].Deploys {
			d := &hs[i].Deploys[j]
			d.Revisions = map[string]string{"ACME/app": d.Revisions[repo]}
		}
	}
	c, ok := run(t, done, []model.PR{squash}, hs)
	if !ok {
		t.Fatal("row dropped, want it kept inside FadeAfter")
	}
	want(t, c, model.StageInProd, model.None, -1)
	for _, s := range c.Slots {
		if !s.Applies {
			t.Errorf("%s does not apply, want every Env to apply despite the repo's case", s.Env.ID())
		}
	}
}
