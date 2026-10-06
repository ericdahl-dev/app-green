package link_test

import (
	"testing"
	"time"

	"github.com/ericdahl-dev/app-green/internal/link"
	"github.com/ericdahl-dev/app-green/internal/model"
)

var (
	t0   = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	test = model.Env{Account: "stage-acct", Pipeline: "p", Stage: "Test", Order: 0}
	prod = model.Env{Account: "stage-acct", Pipeline: "p", Stage: "Production", Order: 1, Prod: true}
)

func dep(st model.DeployStatus, sha string, at time.Time) model.Deploy {
	return model.Deploy{Status: st, Revisions: map[string]string{"acme/app": sha}, FinishedAt: at}
}

func merged(sha string) model.PR {
	return model.PR{Repo: "acme/app", State: model.PRMerged, MergeSHA: sha, EffectiveSHA: sha}
}

func noCompare(t *testing.T) model.CompareFunc {
	return func(repo, base, head string) model.Inclusion {
		t.Fatalf("unexpected compare %s %s...%s", repo, base, head)
		return model.InclusionUnknown
	}
}

func TestSlotExactMatch(t *testing.T) {
	h := model.EnvHistory{Env: test, Deploys: []model.Deploy{
		dep(model.DeploySucceeded, "bbbb", t0), dep(model.DeploySucceeded, "aaaa", t0.Add(-time.Hour)),
	}}
	s := link.Slot([]model.PR{merged("aaaa")}, h, noCompare(t))
	if s.State != model.SlotDeployed || !s.At.Equal(t0.Add(-time.Hour)) {
		t.Errorf("slot = %+v, want deployed at the aaaa deploy", s)
	}
}

func TestSlotCompareFallback(t *testing.T) {
	// aaaa was superseded; the newest succeeded deploy is cccc, which includes it.
	h := model.EnvHistory{Env: test, Deploys: []model.Deploy{dep(model.DeploySucceeded, "cccc", t0)}}
	cmp := func(repo, base, head string) model.Inclusion {
		if base == "aaaa" && head == "cccc" {
			return model.Included
		}
		return model.NotIncluded
	}
	if s := link.Slot([]model.PR{merged("aaaa")}, h, cmp); s.State != model.SlotDeployed || s.SHA != "cccc" {
		t.Errorf("slot = %+v, want deployed via cccc", s)
	}
}

func TestSlotAwaitingApproval(t *testing.T) {
	d := dep(model.DeployAwaitingApproval, "aaaa", t0)
	d.ApprovalToken = "tok"
	h := model.EnvHistory{Env: prod, Deploys: []model.Deploy{d, dep(model.DeploySucceeded, "zzzz", t0.Add(-time.Hour))}}
	cmp := func(_, _, _ string) model.Inclusion { return model.NotIncluded }
	s := link.Slot([]model.PR{merged("aaaa")}, h, cmp)
	if s.State != model.SlotAwaitingApproval || s.Deploy == nil || s.Deploy.ApprovalToken != "tok" {
		t.Errorf("slot = %+v, want awaiting approval with the token", s)
	}
}

func TestSlotFailedWithoutLaterSuccess(t *testing.T) {
	h := model.EnvHistory{Env: test, Deploys: []model.Deploy{dep(model.DeployFailed, "aaaa", t0)}}
	if s := link.Slot([]model.PR{merged("aaaa")}, h, noCompare(t)); s.State != model.SlotFailed {
		t.Errorf("slot = %+v, want failed", s)
	}
}

func TestSlotNotYetAndUnknown(t *testing.T) {
	h := model.EnvHistory{Env: test, Deploys: []model.Deploy{dep(model.DeploySucceeded, "cccc", t0)}}
	no := func(_, _, _ string) model.Inclusion { return model.NotIncluded }
	unk := func(_, _, _ string) model.Inclusion { return model.InclusionUnknown }
	if s := link.Slot([]model.PR{merged("aaaa")}, h, no); s.State != model.SlotNotYet {
		t.Errorf("NotIncluded: %+v", s)
	}
	if s := link.Slot([]model.PR{merged("aaaa")}, h, unk); s.State != model.SlotUnknown {
		t.Errorf("Unknown: %+v", s)
	}
}

func TestSlotNeedsEveryMergedPR(t *testing.T) {
	h := model.EnvHistory{Env: test, Deploys: []model.Deploy{dep(model.DeploySucceeded, "aaaa", t0)}}
	no := func(_, _, _ string) model.Inclusion { return model.NotIncluded }
	open := model.PR{Repo: "acme/app", State: model.PROpen}
	if s := link.Slot([]model.PR{merged("aaaa"), open}, h, no); s.State != model.SlotDeployed {
		t.Errorf("open PRs do not hold a slot back: %+v", s)
	}
	if s := link.Slot([]model.PR{merged("aaaa"), merged("bbbb")}, h, no); s.State != model.SlotNotYet {
		t.Errorf("bbbb not deployed, so the chain is not: %+v", s)
	}
}

func TestSlotNoMergedPRs(t *testing.T) {
	h := model.EnvHistory{Env: test}
	if s := link.Slot(nil, h, noCompare(t)); s.State != model.SlotNotYet {
		t.Errorf("%+v", s)
	}
}

func TestSlotOtherRepoNotInPipeline(t *testing.T) {
	// The Env deploys acme/app only; a PR in acme/docs has no slot here.
	p := model.PR{Repo: "acme/docs", State: model.PRMerged, EffectiveSHA: "dddd"}
	h := model.EnvHistory{Env: test, Deploys: []model.Deploy{dep(model.DeploySucceeded, "aaaa", t0)}}
	if s := link.Slot([]model.PR{p}, h, noCompare(t)); s.State != model.SlotNotYet {
		t.Errorf("%+v", s)
	}
}

func TestSlotsOnePerEnv(t *testing.T) {
	hs := []model.EnvHistory{{Env: test}, {Env: prod}}
	c := link.Slots(model.Chain{}, hs, noCompare(t))
	if len(c.Slots) != 2 || c.Slots[1].Env.Stage != "Production" {
		t.Errorf("%+v", c.Slots)
	}
}

func TestSlotCompareSkipsDeployWithoutRepo(t *testing.T) {
	// The newest success does not carry acme/app (e.g. the pipeline's sources
	// changed); compare against the newest success that does, never with "".
	other := model.Deploy{Status: model.DeploySucceeded, Revisions: map[string]string{"acme/infra": "eeee"}, FinishedAt: t0}
	h := model.EnvHistory{Env: test, Deploys: []model.Deploy{other, dep(model.DeploySucceeded, "cccc", t0.Add(-time.Hour))}}
	cmp := func(_, base, head string) model.Inclusion {
		if head == "" {
			t.Fatalf("compare called with empty head for %s", base)
		}
		if base == "aaaa" && head == "cccc" {
			return model.Included
		}
		return model.NotIncluded
	}
	if s := link.Slot([]model.PR{merged("aaaa")}, h, cmp); s.State != model.SlotDeployed || s.SHA != "cccc" {
		t.Errorf("slot = %+v, want deployed via cccc", s)
	}
}
