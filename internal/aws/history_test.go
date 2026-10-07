package aws

import (
	"context"
	"maps"
	"testing"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/codepipeline"
	"github.com/aws/aws-sdk-go-v2/service/codepipeline/types"

	"github.com/ericdahl-dev/app-green/internal/model"
)

var t0 = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

func at(min int) *time.Time { t := t0.Add(time.Duration(min) * time.Minute); return &t }

var testSources = map[string]string{"acme/app": "AppCode", "acme/infra": "InfraCode", "acme/reports": "ReportsCode"}

var (
	prodSpec = StageSpec{Stage: "Production", DeployAction: "deploy", ApprovalStage: "Test", ApprovalAction: "ManualApprovalOfTestEnvironment"}
	testSpec = StageSpec{Stage: "Test", DeployAction: "Deploy"}
)

func execSummary(id string, status types.PipelineExecutionStatus, revs ...string) types.PipelineExecutionSummary {
	s := types.PipelineExecutionSummary{PipelineExecutionId: awssdk.String(id), Status: status}
	for i := 0; i+1 < len(revs); i += 2 {
		s.SourceRevisions = append(s.SourceRevisions, types.SourceRevision{ActionName: awssdk.String(revs[i]), RevisionId: awssdk.String(revs[i+1])})
	}
	return s
}

func actionExec(exec, stage, action string, status types.ActionExecutionStatus, updated *time.Time) types.ActionExecutionDetail {
	return types.ActionExecutionDetail{
		PipelineExecutionId: awssdk.String(exec),
		ActionExecutionId:   awssdk.String(exec + "-" + action),
		StageName:           awssdk.String(stage),
		ActionName:          awssdk.String(action),
		Status:              status,
		StartTime:           updated,
		LastUpdateTime:      updated,
	}
}

func allRevs(sha string) []string {
	return []string{"AppCode", sha, "InfraCode", sha + "i", "ReportsCode", sha + "r"}
}

func TestHistoryStatuses(t *testing.T) {
	f := &fakePipeline{
		execs: &codepipeline.ListPipelineExecutionsOutput{PipelineExecutionSummaries: []types.PipelineExecutionSummary{
			execSummary("exec3", types.PipelineExecutionStatusInProgress, allRevs("ccc")...),
			execSummary("exec2", types.PipelineExecutionStatusSucceeded, allRevs("bbb")...),
			execSummary("exec1", types.PipelineExecutionStatusFailed, allRevs("aaa")...),
		}},
		actionPages: []*codepipeline.ListActionExecutionsOutput{{ActionExecutionDetails: []types.ActionExecutionDetail{
			actionExec("exec3", "Production", "deploy", types.ActionExecutionStatusInProgress, at(30)),
			actionExec("exec2", "Production", "deploy", types.ActionExecutionStatusSucceeded, at(20)),
			actionExec("exec1", "Production", "deploy", types.ActionExecutionStatusFailed, at(10)),
			actionExec("exec3", "Test", "Deploy", types.ActionExecutionStatusSucceeded, at(25)),
		}}},
	}
	c := &Client{cp: f}
	got, _, err := c.History(context.Background(), "app-pipeline", testSources, StageSpec{Stage: "Production", DeployAction: "deploy"})
	if err != nil {
		t.Fatal(err)
	}
	want := []model.DeployStatus{model.DeployInProgress, model.DeploySucceeded, model.DeployFailed}
	assertStatuses(t, got["Production"], []string{"exec3", "exec2", "exec1"}, want)
}

func assertStatuses(t *testing.T, got []model.Deploy, ids []string, statuses []model.DeployStatus) {
	t.Helper()
	if len(got) != len(ids) {
		t.Fatalf("got %d deploys %+v, want %v", len(got), got, ids)
	}
	for i, d := range got {
		if d.ExecutionID != ids[i] || d.Status != statuses[i] {
			t.Errorf("deploy %d = %s %s, want %s %s", i, d.ExecutionID, d.Status, ids[i], statuses[i])
		}
	}
}

func TestHistoryNewestFirst(t *testing.T) {
	f := &fakePipeline{
		execs: &codepipeline.ListPipelineExecutionsOutput{PipelineExecutionSummaries: []types.PipelineExecutionSummary{
			execSummary("exec3", types.PipelineExecutionStatusSucceeded, allRevs("ccc")...),
			execSummary("exec2", types.PipelineExecutionStatusSucceeded, allRevs("bbb")...),
			execSummary("exec1", types.PipelineExecutionStatusSucceeded, allRevs("aaa")...),
		}},
		// AWS orders by start time; a retried older run can finish last.
		actionPages: []*codepipeline.ListActionExecutionsOutput{{ActionExecutionDetails: []types.ActionExecutionDetail{
			actionExec("exec1", "Test", "Deploy", types.ActionExecutionStatusSucceeded, at(10)),
			actionExec("exec3", "Test", "Deploy", types.ActionExecutionStatusSucceeded, at(5)),
			actionExec("exec2", "Test", "Deploy", types.ActionExecutionStatusSucceeded, at(40)),
		}}},
	}
	got, _, err := (&Client{cp: f}).History(context.Background(), "app-pipeline", testSources, testSpec)
	if err != nil {
		t.Fatal(err)
	}
	d := got["Test"]
	ok := model.DeploySucceeded
	assertStatuses(t, d, []string{"exec2", "exec1", "exec3"}, []model.DeployStatus{ok, ok, ok})
	if !d[0].FinishedAt.Equal(*at(40)) {
		t.Errorf("FinishedAt = %v, want %v", d[0].FinishedAt, *at(40))
	}
}

func TestHistoryRevisions(t *testing.T) {
	f := &fakePipeline{
		execs: &codepipeline.ListPipelineExecutionsOutput{PipelineExecutionSummaries: []types.PipelineExecutionSummary{
			// exec2 has no ReportsCode revision, plus a source action no
			// configured repo maps to.
			execSummary("exec2", types.PipelineExecutionStatusSucceeded, "AppCode", "bbb", "InfraCode", "bbbi", "OtherCode", "zzz"),
			execSummary("exec1", types.PipelineExecutionStatusSucceeded, allRevs("aaa")...),
		}},
		actionPages: []*codepipeline.ListActionExecutionsOutput{{ActionExecutionDetails: []types.ActionExecutionDetail{
			actionExec("exec2", "Test", "Deploy", types.ActionExecutionStatusSucceeded, at(20)),
			actionExec("exec1", "Test", "Deploy", types.ActionExecutionStatusSucceeded, at(10)),
			// exec0 is older than the execution window: its revisions are
			// unknown, so it is left out rather than shown carrying nothing.
			actionExec("exec0", "Test", "Deploy", types.ActionExecutionStatusSucceeded, at(5)),
		}}},
	}
	got, _, err := (&Client{cp: f}).History(context.Background(), "app-pipeline", testSources, testSpec)
	if err != nil {
		t.Fatal(err)
	}
	d := got["Test"]
	ok := model.DeploySucceeded
	assertStatuses(t, d, []string{"exec2", "exec1"}, []model.DeployStatus{ok, ok})
	if want := map[string]string{"acme/app": "bbb", "acme/infra": "bbbi"}; !maps.Equal(d[0].Revisions, want) {
		t.Errorf("exec2 Revisions = %v, want %v", d[0].Revisions, want)
	}
	if want := map[string]string{"acme/app": "aaa", "acme/infra": "aaai", "acme/reports": "aaar"}; !maps.Equal(d[1].Revisions, want) {
		t.Errorf("exec1 Revisions = %v, want %v", d[1].Revisions, want)
	}
}

func TestHistoryKeepsFinishedActionsOfEndedRuns(t *testing.T) {
	f := &fakePipeline{
		execs: &codepipeline.ListPipelineExecutionsOutput{PipelineExecutionSummaries: []types.PipelineExecutionSummary{
			execSummary("exec6", types.PipelineExecutionStatusCancelled, allRevs("fff")...),
			execSummary("exec5", types.PipelineExecutionStatusStopped, allRevs("eee")...),
			execSummary("exec4", types.PipelineExecutionStatusSuperseded, allRevs("ddd")...),
			execSummary("exec3", types.PipelineExecutionStatusCancelled, allRevs("ccc")...),
			execSummary("exec2", types.PipelineExecutionStatusStopped, allRevs("bbb")...),
			execSummary("exec1", types.PipelineExecutionStatusSucceeded, allRevs("aaa")...),
		}},
		actionPages: []*codepipeline.ListActionExecutionsOutput{{ActionExecutionDetails: []types.ActionExecutionDetail{
			// The run ended while its deploy showed InProgress: it never finished.
			actionExec("exec6", "Test", "Deploy", types.ActionExecutionStatusInProgress, at(60)),
			actionExec("exec5", "Test", "Deploy", types.ActionExecutionStatusAbandoned, at(50)),
			// Superseded while waiting at the approval: its Test deploy
			// really ran, and the approval it waited at never finished.
			actionExec("exec4", "Test", "ManualApprovalOfTestEnvironment", types.ActionExecutionStatusInProgress, at(42)),
			actionExec("exec4", "Test", "Deploy", types.ActionExecutionStatusSucceeded, at(40)),
			// A canceled run's finished failure is still a real failure.
			actionExec("exec3", "Test", "Deploy", types.ActionExecutionStatusFailed, at(30)),
			// Stopped after the deploy finished: the deploy still counts.
			actionExec("exec2", "Test", "Deploy", types.ActionExecutionStatusSucceeded, at(20)),
			actionExec("exec1", "Test", "Deploy", types.ActionExecutionStatusSucceeded, at(10)),
		}}},
	}
	got, _, err := (&Client{cp: f}).History(context.Background(), "app-pipeline", testSources, testSpec, prodSpec)
	if err != nil {
		t.Fatal(err)
	}
	ok := model.DeploySucceeded
	assertStatuses(t, got["Test"], []string{"exec4", "exec3", "exec2", "exec1"}, []model.DeployStatus{ok, model.DeployFailed, ok, ok})
	assertStatuses(t, got["Production"], []string{}, nil)
	if f.stateCalls != 0 {
		t.Errorf("GetPipelineState calls = %d, want 0", f.stateCalls)
	}
}

// approvalFake is the plan's scenario: exec3 passed Test/Deploy and waits at
// the Test approval; exec2 reached Production; exec1 failed there.
func approvalFake() *fakePipeline {
	return &fakePipeline{
		execs: &codepipeline.ListPipelineExecutionsOutput{PipelineExecutionSummaries: []types.PipelineExecutionSummary{
			execSummary("exec3", types.PipelineExecutionStatusInProgress, allRevs("ccc")...),
			execSummary("exec2", types.PipelineExecutionStatusSucceeded, allRevs("bbb")...),
			execSummary("exec1", types.PipelineExecutionStatusFailed, allRevs("aaa")...),
		}},
		actionPages: []*codepipeline.ListActionExecutionsOutput{{ActionExecutionDetails: []types.ActionExecutionDetail{
			actionExec("exec3", "Test", "ManualApprovalOfTestEnvironment", types.ActionExecutionStatusInProgress, at(36)),
			actionExec("exec3", "Test", "SmokeTests", types.ActionExecutionStatusSucceeded, at(35)),
			actionExec("exec3", "Test", "Deploy", types.ActionExecutionStatusSucceeded, at(34)),
			actionExec("exec2", "Production", "deploy", types.ActionExecutionStatusSucceeded, at(25)),
			actionExec("exec2", "Test", "ManualApprovalOfTestEnvironment", types.ActionExecutionStatusSucceeded, at(23)),
			actionExec("exec2", "Test", "Deploy", types.ActionExecutionStatusSucceeded, at(20)),
			actionExec("exec1", "Production", "deploy", types.ActionExecutionStatusFailed, at(15)),
			actionExec("exec1", "Test", "ManualApprovalOfTestEnvironment", types.ActionExecutionStatusSucceeded, at(12)),
			actionExec("exec1", "Test", "Deploy", types.ActionExecutionStatusSucceeded, at(10)),
		}}},
		state: &codepipeline.GetPipelineStateOutput{StageStates: []types.StageState{
			{
				StageName:       awssdk.String("Test"),
				LatestExecution: &types.StageExecution{PipelineExecutionId: awssdk.String("exec3"), Status: types.StageExecutionStatusInProgress},
				ActionStates: []types.ActionState{
					{ActionName: awssdk.String("Deploy"), LatestExecution: &types.ActionExecution{Status: types.ActionExecutionStatusSucceeded}},
					{ActionName: awssdk.String("ManualApprovalOfTestEnvironment"), LatestExecution: &types.ActionExecution{
						ActionExecutionId: awssdk.String("exec3-ManualApprovalOfTestEnvironment"),
						Status:            types.ActionExecutionStatusInProgress,
						Token:             awssdk.String("tok-3"),
					}},
				},
			},
			{
				StageName:       awssdk.String("Production"),
				LatestExecution: &types.StageExecution{PipelineExecutionId: awssdk.String("exec2"), Status: types.StageExecutionStatusSucceeded},
			},
		}},
	}
}

func TestHistoryAwaitingApproval(t *testing.T) {
	f := approvalFake()
	got, warns, err := (&Client{cp: f}).History(context.Background(), "app-pipeline", testSources, testSpec, prodSpec)
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) > 0 {
		t.Errorf("warnings: %v", warns)
	}
	prod := got["Production"]
	assertStatuses(t, prod, []string{"exec3", "exec2", "exec1"},
		[]model.DeployStatus{model.DeployAwaitingApproval, model.DeploySucceeded, model.DeployFailed})
	if prod[0].ApprovalToken != "tok-3" {
		t.Errorf("token = %q, want tok-3", prod[0].ApprovalToken)
	}
	if prod[0].Revisions["acme/app"] != "ccc" {
		t.Errorf("exec3 Revisions = %v", prod[0].Revisions)
	}
	if !prod[0].FinishedAt.Equal(*at(36)) {
		t.Errorf("exec3 FinishedAt = %v, want the approval's update time", prod[0].FinishedAt)
	}
	if prod[1].ApprovalToken != "" {
		t.Errorf("exec2 token = %q, want none", prod[1].ApprovalToken)
	}
	ok := model.DeploySucceeded
	assertStatuses(t, got["Test"], []string{"exec3", "exec2", "exec1"}, []model.DeployStatus{ok, ok, ok})
	if f.stateCalls != 1 {
		t.Errorf("GetPipelineState calls = %d, want 1", f.stateCalls)
	}
}

func TestHistoryApprovalTokenMatching(t *testing.T) {
	cases := []struct {
		name      string
		edit      func(*types.ActionExecution, *types.StageState)
		wantToken string
	}{
		{"no action execution ID, stage on exec3", func(ae *types.ActionExecution, _ *types.StageState) { ae.ActionExecutionId = nil }, "tok-3"},
		{"no action execution ID, stage on another run", func(ae *types.ActionExecution, st *types.StageState) {
			ae.ActionExecutionId = nil
			st.LatestExecution.PipelineExecutionId = awssdk.String("exec9")
		}, ""},
		{"state shows another run's approval", func(ae *types.ActionExecution, _ *types.StageState) { ae.ActionExecutionId = awssdk.String("exec9-x") }, ""},
		{"approval no longer waiting", func(ae *types.ActionExecution, _ *types.StageState) { ae.Status = types.ActionExecutionStatusSucceeded }, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := approvalFake()
			st := &f.state.StageStates[0]
			tc.edit(st.ActionStates[1].LatestExecution, st)
			got, warns, err := (&Client{cp: f}).History(context.Background(), "app-pipeline", testSources, prodSpec)
			if err != nil {
				t.Fatal(err)
			}
			prod := got["Production"]
			if prod[0].Status != model.DeployAwaitingApproval || prod[0].ApprovalToken != tc.wantToken {
				t.Errorf("deploy 0 = %s token %q, want AwaitingApproval token %q", prod[0].Status, prod[0].ApprovalToken, tc.wantToken)
			}
			if wantWarn := tc.wantToken == ""; (len(warns) > 0) != wantWarn {
				t.Errorf("warnings = %v, want some: %v", warns, wantWarn)
			}
		})
	}
}

func TestHistoryNoStateCallWithoutPendingApproval(t *testing.T) {
	f := approvalFake()
	if _, _, err := (&Client{cp: f}).History(context.Background(), "app-pipeline", testSources, testSpec); err != nil {
		t.Fatal(err)
	}
	if f.stateCalls != 0 {
		t.Errorf("GetPipelineState calls = %d, want 0", f.stateCalls)
	}
}

func TestHistoryPagesActionExecutions(t *testing.T) {
	f := &fakePipeline{
		execs: &codepipeline.ListPipelineExecutionsOutput{PipelineExecutionSummaries: []types.PipelineExecutionSummary{
			execSummary("exec2", types.PipelineExecutionStatusSucceeded, allRevs("bbb")...),
			execSummary("exec1", types.PipelineExecutionStatusSucceeded, allRevs("aaa")...),
		}},
		actionPages: []*codepipeline.ListActionExecutionsOutput{
			{NextToken: awssdk.String("p2"), ActionExecutionDetails: []types.ActionExecutionDetail{
				actionExec("exec2", "Test", "Deploy", types.ActionExecutionStatusSucceeded, at(20)),
			}},
			{ActionExecutionDetails: []types.ActionExecutionDetail{
				actionExec("exec1", "Test", "Deploy", types.ActionExecutionStatusSucceeded, at(10)),
			}},
		},
	}
	got, _, err := (&Client{cp: f}).History(context.Background(), "app-pipeline", testSources, testSpec)
	if err != nil {
		t.Fatal(err)
	}
	ok := model.DeploySucceeded
	assertStatuses(t, got["Test"], []string{"exec2", "exec1"}, []model.DeployStatus{ok, ok})
	if len(f.actionInputs) != 2 || awssdk.ToString(f.actionInputs[1].NextToken) != "p2" {
		t.Errorf("ListActionExecutions inputs = %d, second NextToken %q", len(f.actionInputs), awssdk.ToString(f.actionInputs[len(f.actionInputs)-1].NextToken))
	}
}

func TestHistoryActionPagingIsBounded(t *testing.T) {
	f := &fakePipeline{
		execs: &codepipeline.ListPipelineExecutionsOutput{PipelineExecutionSummaries: []types.PipelineExecutionSummary{
			execSummary("exec1", types.PipelineExecutionStatusSucceeded, allRevs("aaa")...),
		}},
		// Always another page.
		actionPages: []*codepipeline.ListActionExecutionsOutput{{NextToken: awssdk.String("more"), ActionExecutionDetails: []types.ActionExecutionDetail{
			actionExec("exec1", "Test", "SmokeTests", types.ActionExecutionStatusSucceeded, at(10)),
		}}},
	}
	if _, _, err := (&Client{cp: f}).History(context.Background(), "app-pipeline", testSources, testSpec); err != nil {
		t.Fatal(err)
	}
	if len(f.actionInputs) != maxActionPages {
		t.Errorf("ListActionExecutions calls = %d, want %d", len(f.actionInputs), maxActionPages)
	}
}

func TestHistoryActionPagingStopsPastTheWindow(t *testing.T) {
	exec2 := execSummary("exec2", types.PipelineExecutionStatusSucceeded, allRevs("bbb")...)
	exec2.StartTime = at(15)
	f := &fakePipeline{
		execs: &codepipeline.ListPipelineExecutionsOutput{PipelineExecutionSummaries: []types.PipelineExecutionSummary{exec2}},
		actionPages: []*codepipeline.ListActionExecutionsOutput{{NextToken: awssdk.String("more"), ActionExecutionDetails: []types.ActionExecutionDetail{
			// Started after the window was read: not a reason to stop.
			actionExec("exec3", "Test", "Deploy", types.ActionExecutionStatusInProgress, at(30)),
			actionExec("exec2", "Test", "Deploy", types.ActionExecutionStatusSucceeded, at(20)),
			// Started before the oldest execution in the window: later pages
			// are older still.
			actionExec("exec1", "Test", "Deploy", types.ActionExecutionStatusSucceeded, at(10)),
		}}},
	}
	if _, _, err := (&Client{cp: f}).History(context.Background(), "app-pipeline", testSources, testSpec); err != nil {
		t.Fatal(err)
	}
	if len(f.actionInputs) != 1 {
		t.Errorf("ListActionExecutions calls = %d, want 1", len(f.actionInputs))
	}
}

func TestHistoryRetriedStageIsOneDeploy(t *testing.T) {
	f := &fakePipeline{
		execs: &codepipeline.ListPipelineExecutionsOutput{PipelineExecutionSummaries: []types.PipelineExecutionSummary{
			execSummary("exec5", types.PipelineExecutionStatusSucceeded, allRevs("eee")...),
		}},
		// A stage retry re-runs the action under the same execution ID.
		actionPages: []*codepipeline.ListActionExecutionsOutput{{ActionExecutionDetails: []types.ActionExecutionDetail{
			{
				PipelineExecutionId: awssdk.String("exec5"), ActionExecutionId: awssdk.String("exec5-deploy-2"),
				StageName: awssdk.String("Production"), ActionName: awssdk.String("deploy"),
				Status: types.ActionExecutionStatusSucceeded, StartTime: at(12), LastUpdateTime: at(15),
			},
			{
				PipelineExecutionId: awssdk.String("exec5"), ActionExecutionId: awssdk.String("exec5-deploy-1"),
				StageName: awssdk.String("Production"), ActionName: awssdk.String("deploy"),
				Status: types.ActionExecutionStatusFailed, StartTime: at(8), LastUpdateTime: at(10),
			},
		}}},
	}
	got, _, err := (&Client{cp: f}).History(context.Background(), "app-pipeline", testSources, StageSpec{Stage: "Production", DeployAction: "deploy"})
	if err != nil {
		t.Fatal(err)
	}
	prod := got["Production"]
	assertStatuses(t, prod, []string{"exec5"}, []model.DeployStatus{model.DeploySucceeded})
	if !prod[0].FinishedAt.Equal(*at(15)) {
		t.Errorf("FinishedAt = %v, want the retry's", prod[0].FinishedAt)
	}
}
