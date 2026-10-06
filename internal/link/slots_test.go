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

func TestSlotIncludedByNewerKeepsFirstExactAt(t *testing.T) {
	// aaaa deployed at t-2h; the newer bbbb deploy still contains it, so it is
	// running now, and At stays at the aaaa deploy.
	h := model.EnvHistory{Env: test, Deploys: []model.Deploy{
		dep(model.DeploySucceeded, "bbbb", t0), dep(model.DeploySucceeded, "aaaa", t0.Add(-2*time.Hour)),
	}}
	cmp := func(_, base, head string) model.Inclusion {
		if base == "aaaa" && head == "bbbb" {
			return model.Included
		}
		return model.NotIncluded
	}
	s := link.Slot([]model.PR{merged("aaaa")}, h, cmp)
	if s.State != model.SlotDeployed || !s.At.Equal(t0.Add(-2*time.Hour)) {
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

func TestSlotNotIncludedIsNotYet(t *testing.T) {
	h := model.EnvHistory{Env: test, Deploys: []model.Deploy{dep(model.DeploySucceeded, "cccc", t0)}}
	no := func(_, _, _ string) model.Inclusion { return model.NotIncluded }
	if s := link.Slot([]model.PR{merged("aaaa")}, h, no); s.State != model.SlotNotYet {
		t.Errorf("slot = %+v, want not yet", s)
	}
}

func TestSlotCompareUnknownIsUnknown(t *testing.T) {
	h := model.EnvHistory{Env: test, Deploys: []model.Deploy{dep(model.DeploySucceeded, "cccc", t0)}}
	unk := func(_, _, _ string) model.Inclusion { return model.InclusionUnknown }
	if s := link.Slot([]model.PR{merged("aaaa")}, h, unk); s.State != model.SlotUnknown {
		t.Errorf("slot = %+v, want unknown", s)
	}
}

func TestSlotNeedsEveryMergedPR(t *testing.T) {
	h := model.EnvHistory{Env: test, Deploys: []model.Deploy{dep(model.DeploySucceeded, "aaaa", t0)}}
	no := func(_, _, _ string) model.Inclusion { return model.NotIncluded }
	open := model.PR{Repo: "acme/app", State: model.PROpen}
	if s := link.Slot([]model.PR{merged("aaaa"), open}, h, no); s.State != model.SlotDeployed {
		t.Errorf("slot = %+v, want deployed; open PRs do not hold a slot back", s)
	}
	if s := link.Slot([]model.PR{merged("aaaa"), merged("bbbb")}, h, no); s.State != model.SlotNotYet {
		t.Errorf("slot = %+v, want not yet; bbbb is not deployed", s)
	}
}

func TestSlotNoMergedPRs(t *testing.T) {
	h := model.EnvHistory{Env: test}
	if s := link.Slot(nil, h, noCompare(t)); s.State != model.SlotNotYet {
		t.Errorf("slot = %+v, want not yet", s)
	}
}

func TestSlotOtherRepoNotInPipeline(t *testing.T) {
	// The Env deploys acme/app only; a PR in acme/docs has no slot here.
	p := model.PR{Repo: "acme/docs", State: model.PRMerged, EffectiveSHA: "dddd"}
	h := model.EnvHistory{Env: test, Deploys: []model.Deploy{dep(model.DeploySucceeded, "aaaa", t0)}}
	if s := link.Slot([]model.PR{p}, h, noCompare(t)); s.State != model.SlotNotYet {
		t.Errorf("slot = %+v, want not yet; acme/docs is not in this pipeline", s)
	}
}

func TestSlotsOnePerEnv(t *testing.T) {
	hs := []model.EnvHistory{{Env: test}, {Env: prod}}
	c := link.Slots(model.Chain{}, hs, noCompare(t))
	if len(c.Slots) != 2 || c.Slots[1].Env.Stage != "Production" {
		t.Errorf("slots = %+v, want two, Production second", c.Slots)
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

func TestSlotRollbackIsNotDeployed(t *testing.T) {
	// aaaa deployed, then a rollback deployed zzzz, which does not contain it.
	h := model.EnvHistory{Env: test, Deploys: []model.Deploy{
		dep(model.DeploySucceeded, "zzzz", t0), dep(model.DeploySucceeded, "aaaa", t0.Add(-2*time.Hour)),
	}}
	cmp := func(_, base, head string) model.Inclusion {
		if base == "aaaa" && head == "zzzz" {
			return model.NotIncluded
		}
		t.Fatalf("unexpected compare %s...%s", base, head)
		return model.InclusionUnknown
	}
	if s := link.Slot([]model.PR{merged("aaaa")}, h, cmp); s.State != model.SlotNotYet {
		t.Errorf("slot = %+v, want not yet after rollback", s)
	}
}

func TestSlotSameSHARedeployKeepsFirstTime(t *testing.T) {
	// An infra-only redeploy of aaaa must not reset when aaaa first went out.
	h := model.EnvHistory{Env: test, Deploys: []model.Deploy{
		dep(model.DeploySucceeded, "aaaa", t0), dep(model.DeploySucceeded, "aaaa", t0.Add(-time.Hour)),
	}}
	s := link.Slot([]model.PR{merged("aaaa")}, h, noCompare(t))
	if s.State != model.SlotDeployed || !s.At.Equal(t0.Add(-time.Hour)) {
		t.Errorf("slot = %+v, want deployed at the first aaaa deploy", s)
	}
}

func TestSlotNewestSuccessIsExactNoCompare(t *testing.T) {
	h := model.EnvHistory{Env: test, Deploys: []model.Deploy{
		dep(model.DeploySucceeded, "aaaa", t0), dep(model.DeploySucceeded, "bbbb", t0.Add(-time.Hour)),
	}}
	s := link.Slot([]model.PR{merged("aaaa")}, h, noCompare(t))
	if s.State != model.SlotDeployed || !s.At.Equal(t0) || s.SHA != "aaaa" {
		t.Errorf("slot = %+v, want deployed at the newest deploy", s)
	}
}

func TestSlotStackPendingHoldsBack(t *testing.T) {
	h := model.EnvHistory{Env: test, Deploys: []model.Deploy{dep(model.DeploySucceeded, "aaaa", t0)}}
	stacked := model.PR{Repo: "acme/app", State: model.PRMerged, MergeSHA: "ssss", StackPending: true}
	if s := link.Slot([]model.PR{merged("aaaa"), stacked}, h, noCompare(t)); s.State != model.SlotNotYet {
		t.Errorf("slot = %+v, want not yet while a merged PR waits in a stack", s)
	}
}

func TestSlotStackPendingOtherRepoIgnored(t *testing.T) {
	h := model.EnvHistory{Env: test, Deploys: []model.Deploy{dep(model.DeploySucceeded, "aaaa", t0)}}
	stacked := model.PR{Repo: "acme/docs", State: model.PRMerged, MergeSHA: "ssss", StackPending: true}
	if s := link.Slot([]model.PR{merged("aaaa"), stacked}, h, noCompare(t)); s.State != model.SlotDeployed {
		t.Errorf("slot = %+v, want deployed; acme/docs is not in this pipeline", s)
	}
}

func TestSlotMergedWithoutSHAHoldsBack(t *testing.T) {
	// Malformed input (merged, no EffectiveSHA, not marked StackPending) fails safe.
	h := model.EnvHistory{Env: test, Deploys: []model.Deploy{dep(model.DeploySucceeded, "aaaa", t0)}}
	bare := model.PR{Repo: "acme/app", State: model.PRMerged}
	if s := link.Slot([]model.PR{merged("aaaa"), bare}, h, noCompare(t)); s.State != model.SlotNotYet {
		t.Errorf("slot = %+v, want not yet", s)
	}
}

func TestSlotTwoDeployedKeepsLaterAt(t *testing.T) {
	// The chain is fully deployed only when its last PR is.
	h := model.EnvHistory{Env: test, Deploys: []model.Deploy{
		dep(model.DeploySucceeded, "bbbb", t0), dep(model.DeploySucceeded, "aaaa", t0.Add(-time.Hour)),
	}}
	inc := func(_, _, _ string) model.Inclusion { return model.Included }
	s := link.Slot([]model.PR{merged("aaaa"), merged("bbbb")}, h, inc)
	if s.State != model.SlotDeployed || !s.At.Equal(t0) {
		t.Errorf("slot = %+v, want deployed at t0, when bbbb went out", s)
	}
}

func TestSlotTwoAwaitingKeepsNewerToken(t *testing.T) {
	older := dep(model.DeployAwaitingApproval, "aaaa", t0.Add(-time.Hour))
	older.ApprovalToken = "tok-old"
	newer := dep(model.DeployAwaitingApproval, "bbbb", t0)
	newer.ApprovalToken = "tok-new"
	h := model.EnvHistory{Env: prod, Deploys: []model.Deploy{newer, older, dep(model.DeploySucceeded, "zzzz", t0.Add(-2*time.Hour))}}
	no := func(_, _, _ string) model.Inclusion { return model.NotIncluded }
	s := link.Slot([]model.PR{merged("aaaa"), merged("bbbb")}, h, no)
	if s.State != model.SlotAwaitingApproval || s.Deploy == nil || s.Deploy.ApprovalToken != "tok-new" {
		t.Errorf("slot = %+v, want awaiting approval with tok-new", s)
	}
}

func TestSlotUnknownDeployStatusIsUnknown(t *testing.T) {
	h := model.EnvHistory{Env: test, Deploys: []model.Deploy{dep(model.DeployStatus("Stopped"), "aaaa", t0)}}
	if s := link.Slot([]model.PR{merged("aaaa")}, h, noCompare(t)); s.State != model.SlotUnknown {
		t.Errorf("slot = %+v, want unknown for an unmapped deploy status", s)
	}
}

func TestSlotInProgressRun(t *testing.T) {
	h := model.EnvHistory{Env: test, Deploys: []model.Deploy{dep(model.DeployInProgress, "aaaa", t0)}}
	if s := link.Slot([]model.PR{merged("aaaa")}, h, noCompare(t)); s.State != model.SlotInProgress {
		t.Errorf("slot = %+v, want in progress", s)
	}
}

func TestSlotFailedWinsOverUnknown(t *testing.T) {
	// aaaa's run failed; bbbb has no run and its compare fails.
	h := model.EnvHistory{Env: test, Deploys: []model.Deploy{
		dep(model.DeployFailed, "aaaa", t0), dep(model.DeploySucceeded, "zzzz", t0.Add(-time.Hour)),
	}}
	cmp := func(_, base, _ string) model.Inclusion {
		if base == "aaaa" {
			return model.NotIncluded
		}
		return model.InclusionUnknown
	}
	if s := link.Slot([]model.PR{merged("aaaa"), merged("bbbb")}, h, cmp); s.State != model.SlotFailed {
		t.Errorf("slot = %+v, want failed", s)
	}
}

func TestSlotSinceBackLive(t *testing.T) {
	// aaaa went out at t-3h, was rolled back by zzzz at t-2h, and came back
	// inside cccc at t0. It has been live since t0, not t-3h.
	h := model.EnvHistory{Env: test, Deploys: []model.Deploy{
		dep(model.DeploySucceeded, "cccc", t0),
		dep(model.DeploySucceeded, "zzzz", t0.Add(-2*time.Hour)),
		dep(model.DeploySucceeded, "aaaa", t0.Add(-3*time.Hour)),
	}}
	cmp := func(_, base, head string) model.Inclusion {
		if base == "aaaa" && head == "cccc" {
			return model.Included
		}
		return model.NotIncluded
	}
	s := link.Slot([]model.PR{merged("aaaa")}, h, cmp)
	if s.State != model.SlotDeployed || !s.At.Equal(t0) {
		t.Errorf("slot = %+v, want deployed since t0", s)
	}
}

func TestSlotNewerInFlightSupersedesFailure(t *testing.T) {
	// aaaa's own run failed; a newer run of bbbb, which contains aaaa, is in
	// flight. N (zzzz) is older than both and does not contain aaaa.
	h := model.EnvHistory{Env: test, Deploys: []model.Deploy{
		dep(model.DeployInProgress, "bbbb", t0),
		dep(model.DeployFailed, "aaaa", t0.Add(-2*time.Hour)),
		dep(model.DeploySucceeded, "zzzz", t0.Add(-3*time.Hour)),
	}}
	cmp := func(_, base, head string) model.Inclusion {
		if base == "aaaa" && head == "bbbb" {
			return model.Included
		}
		return model.NotIncluded
	}
	s := link.Slot([]model.PR{merged("aaaa")}, h, cmp)
	if s.State != model.SlotInProgress || s.Deploy == nil || s.Deploy.Revisions["acme/app"] != "bbbb" {
		t.Errorf("slot = %+v, want in progress on the bbbb run", s)
	}
}

func TestSlotRollbackWithUnknownCompareIsUnknown(t *testing.T) {
	h := model.EnvHistory{Env: test, Deploys: []model.Deploy{
		dep(model.DeploySucceeded, "zzzz", t0), dep(model.DeploySucceeded, "aaaa", t0.Add(-2*time.Hour)),
	}}
	unk := func(_, _, _ string) model.Inclusion { return model.InclusionUnknown }
	if s := link.Slot([]model.PR{merged("aaaa")}, h, unk); s.State != model.SlotUnknown {
		t.Errorf("slot = %+v, want unknown", s)
	}
}

func TestSlotDeployedAndUnknownIsUnknown(t *testing.T) {
	h := model.EnvHistory{Env: test, Deploys: []model.Deploy{dep(model.DeploySucceeded, "aaaa", t0)}}
	unk := func(_, _, _ string) model.Inclusion { return model.InclusionUnknown }
	if s := link.Slot([]model.PR{merged("aaaa"), merged("bbbb")}, h, unk); s.State != model.SlotUnknown {
		t.Errorf("slot = %+v, want unknown", s)
	}
}

func TestSlotMultiRepoChainNeedsBoth(t *testing.T) {
	// One pipeline deploys acme/app and acme/svc. The app PR is live, the svc PR is not.
	d := model.Deploy{Status: model.DeploySucceeded, FinishedAt: t0,
		Revisions: map[string]string{"acme/app": "aaaa", "acme/svc": "ssss"}}
	h := model.EnvHistory{Env: test, Deploys: []model.Deploy{d}}
	svc := model.PR{Repo: "acme/svc", State: model.PRMerged, MergeSHA: "tttt", EffectiveSHA: "tttt"}
	no := func(_, _, _ string) model.Inclusion { return model.NotIncluded }
	if s := link.Slot([]model.PR{merged("aaaa"), svc}, h, no); s.State != model.SlotNotYet {
		t.Errorf("slot = %+v, want not yet; the svc PR is not deployed", s)
	}
}

func TestSlotCompareCallCount(t *testing.T) {
	calls := map[string]int{}
	counting := func(_, base, _ string) model.Inclusion {
		calls[base]++
		return model.Included // newer commits on main contain older ones
	}
	// N is exact: no compare at all.
	h := model.EnvHistory{Env: test, Deploys: []model.Deploy{dep(model.DeploySucceeded, "aaaa", t0)}}
	link.Slot([]model.PR{merged("aaaa")}, h, counting)
	if len(calls) != 0 {
		t.Errorf("calls = %v, want none when N is the exact SHA", calls)
	}
	// N (bbbb) is not exact for aaaa: one compare against N, and the walk back
	// stops free at aaaa's own exact success. bbbb is exact at N: none.
	h = model.EnvHistory{Env: test, Deploys: []model.Deploy{
		dep(model.DeploySucceeded, "bbbb", t0), dep(model.DeploySucceeded, "aaaa", t0.Add(-time.Hour)),
	}}
	link.Slot([]model.PR{merged("aaaa"), merged("bbbb")}, h, counting)
	if calls["aaaa"] != 1 || calls["bbbb"] != 0 {
		t.Errorf("calls = %v, want aaaa:1 bbbb:0", calls)
	}
	// Each success between the PR's own run and N costs one more compare in
	// the walk back (D1): aaaa compares cccc (N) and bbbb, then stops at its
	// own run; bbbb compares cccc, then stops at its own run.
	clear(calls)
	h = model.EnvHistory{Env: test, Deploys: []model.Deploy{
		dep(model.DeploySucceeded, "cccc", t0),
		dep(model.DeploySucceeded, "bbbb", t0.Add(-time.Hour)),
		dep(model.DeploySucceeded, "aaaa", t0.Add(-2*time.Hour)),
	}}
	link.Slot([]model.PR{merged("aaaa"), merged("bbbb")}, h, counting)
	if calls["aaaa"] != 2 || calls["bbbb"] != 1 {
		t.Errorf("calls = %v, want aaaa:2 bbbb:1", calls)
	}
}

func TestSlotHealthPassesThrough(t *testing.T) {
	hl := model.Health{Known: true, Desired: 2, Healthy: 1}
	h := model.EnvHistory{Env: test, Health: hl, Deploys: []model.Deploy{dep(model.DeploySucceeded, "aaaa", t0)}}
	if s := link.Slot([]model.PR{merged("aaaa")}, h, noCompare(t)); s.Health != hl {
		t.Errorf("slot = %+v, want health %+v", s, hl)
	}
	if s := link.Slot(nil, h, noCompare(t)); s.Health != hl {
		t.Errorf("slot = %+v, want health %+v with no PRs", s, hl)
	}
}
