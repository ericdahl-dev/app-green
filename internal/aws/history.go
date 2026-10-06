package aws

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/codepipeline"
	"github.com/aws/aws-sdk-go-v2/service/codepipeline/types"

	"github.com/ericdahl-dev/app-green/internal/model"
)

// StageSpec names one Env's stage in a pipeline. Action names are matched
// exactly (CodePipeline names are case-sensitive).
type StageSpec struct {
	Stage        string // "Production"
	DeployAction string // the action whose status is the Env's deploy status
	// ApprovalStage and ApprovalAction name the manual approval that gates
	// this stage, which may sit at the end of the previous stage. Empty when
	// no approval gates it.
	ApprovalStage  string
	ApprovalAction string
}

// maxExecutions is how many pipeline executions one History call reads.
const maxExecutions = 50

// maxActionPages bounds ListActionExecutions paging. With no execution
// filter it lists every action of every run (about nine per run in a
// Source → Test → Production pipeline), so 100 per page covers about eleven
// runs; five pages cover the execution window.
const maxActionPages = 5

// History returns each spec's deploys, keyed by StageSpec.Stage, newest
// first. sources is the repo → source action map from Sources. An execution
// that has not run the spec's deploy action but waits at its approval action
// is a DeployAwaitingApproval deploy carrying the approval token. A warning
// means the history is usable but incomplete.
func (c *Client) History(ctx context.Context, pipeline string, sources map[string]string, specs ...StageSpec) (map[string][]model.Deploy, []string, error) {
	execList, err := c.cp.ListPipelineExecutions(ctx, &codepipeline.ListPipelineExecutionsInput{
		PipelineName: awssdk.String(pipeline),
		MaxResults:   awssdk.Int32(maxExecutions),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("aws: ListPipelineExecutions %s: %w", pipeline, err)
	}
	acts, err := c.actionExecutions(ctx, pipeline, oldestStart(execList.PipelineExecutionSummaries))
	if err != nil {
		return nil, nil, err
	}
	execs := executions(execList.PipelineExecutionSummaries, sources)

	var warns []string
	var state *codepipeline.GetPipelineStateOutput // fetched once, on the first pending approval
	out := map[string][]model.Deploy{}
	for _, spec := range specs {
		deploys, pending := stageDeploys(spec, acts, execs)
		for _, p := range pending {
			if state == nil {
				state, err = c.cp.GetPipelineState(ctx, &codepipeline.GetPipelineStateInput{Name: awssdk.String(pipeline)})
				if err != nil {
					return nil, warns, fmt.Errorf("aws: GetPipelineState %s: %w", pipeline, err)
				}
			}
			d := model.Deploy{
				ExecutionID:   awssdk.ToString(p.PipelineExecutionId),
				Status:        model.DeployAwaitingApproval,
				Revisions:     maps.Clone(execs[awssdk.ToString(p.PipelineExecutionId)]),
				FinishedAt:    awssdk.ToTime(p.LastUpdateTime),
				ApprovalToken: approvalToken(state, p),
			}
			if d.ApprovalToken == "" {
				warns = append(warns, fmt.Sprintf("aws: %s: no approval token for %s/%s in execution %s", pipeline, spec.ApprovalStage, spec.ApprovalAction, d.ExecutionID))
			}
			deploys = append(deploys, d)
		}
		sortNewestFirst(deploys)
		out[spec.Stage] = deploys
	}
	return out, warns, nil
}

// actionExecutions lists the pipeline's action executions, newest first,
// paging until AWS has no more, a page reaches actions that started before
// oldest (the oldest execution in the window; zero means unknown), or
// maxActionPages.
func (c *Client) actionExecutions(ctx context.Context, pipeline string, oldest time.Time) ([]types.ActionExecutionDetail, error) {
	var all []types.ActionExecutionDetail
	var next *string
	for range maxActionPages {
		out, err := c.cp.ListActionExecutions(ctx, &codepipeline.ListActionExecutionsInput{
			PipelineName: awssdk.String(pipeline),
			MaxResults:   awssdk.Int32(100),
			NextToken:    next,
		})
		if err != nil {
			return nil, fmt.Errorf("aws: ListActionExecutions %s: %w", pipeline, err)
		}
		all = append(all, out.ActionExecutionDetails...)
		if out.NextToken == nil || pastWindow(out.ActionExecutionDetails, oldest) {
			break
		}
		next = out.NextToken
	}
	return all, nil
}

// pastWindow reports whether page holds an action that started before
// oldest, so later pages hold only runs older than the execution window.
func pastWindow(page []types.ActionExecutionDetail, oldest time.Time) bool {
	if oldest.IsZero() {
		return false
	}
	for _, a := range page {
		if a.StartTime != nil && a.StartTime.Before(oldest) {
			return true
		}
	}
	return false
}

// oldestStart is the earliest StartTime among summaries, zero if any is
// missing one.
func oldestStart(summaries []types.PipelineExecutionSummary) time.Time {
	var oldest time.Time
	for _, e := range summaries {
		if e.StartTime == nil {
			return time.Time{}
		}
		if oldest.IsZero() || e.StartTime.Before(oldest) {
			oldest = *e.StartTime
		}
	}
	return oldest
}

// stageDeploys returns spec's deploys from its deploy action's executions
// (one per execution, the latest attempt), and the approval action
// executions still waiting for a run that has not reached the deploy action. Runs whose execution is not in execs (outside
// the window, superseded or canceled) are dropped.
func stageDeploys(spec StageSpec, acts []types.ActionExecutionDetail, execs map[string]map[string]string) ([]model.Deploy, []types.ActionExecutionDetail) {
	deploys := []model.Deploy{}
	deployed := map[string]int{} // execution ID → index in deploys
	var waiting []types.ActionExecutionDetail
	for _, a := range acts {
		id := awssdk.ToString(a.PipelineExecutionId)
		revs, known := execs[id]
		if !known {
			continue
		}
		stage, action := awssdk.ToString(a.StageName), awssdk.ToString(a.ActionName)
		switch {
		case stage == spec.Stage && action == spec.DeployAction:
			status, ok := deployStatus(a.Status)
			if !ok {
				continue
			}
			d := model.Deploy{
				ExecutionID: id,
				Status:      status,
				Revisions:   maps.Clone(revs),
				FinishedAt:  awssdk.ToTime(a.LastUpdateTime),
			}
			// A stage retry re-runs the action under the same execution:
			// keep the latest attempt.
			if i, seen := deployed[id]; seen {
				if d.FinishedAt.After(deploys[i].FinishedAt) {
					deploys[i] = d
				}
				continue
			}
			deployed[id] = len(deploys)
			deploys = append(deploys, d)
		case spec.ApprovalAction != "" && stage == spec.ApprovalStage && action == spec.ApprovalAction &&
			a.Status == types.ActionExecutionStatusInProgress:
			waiting = append(waiting, a)
		}
	}
	var pending []types.ActionExecutionDetail
	for _, a := range waiting {
		if _, ok := deployed[awssdk.ToString(a.PipelineExecutionId)]; !ok {
			pending = append(pending, a)
		}
	}
	return deploys, pending
}

// approvalToken finds the token for the waiting approval a in the pipeline's
// current state. Only the approval that is waiting right now has one. The
// state's action execution ID ties it to a; when AWS leaves that ID out, the
// stage's current execution must be a's.
func approvalToken(state *codepipeline.GetPipelineStateOutput, a types.ActionExecutionDetail) string {
	for _, st := range state.StageStates {
		if awssdk.ToString(st.StageName) != awssdk.ToString(a.StageName) {
			continue
		}
		for _, as := range st.ActionStates {
			le := as.LatestExecution
			if awssdk.ToString(as.ActionName) != awssdk.ToString(a.ActionName) || le == nil ||
				le.Status != types.ActionExecutionStatusInProgress {
				continue
			}
			if id := awssdk.ToString(le.ActionExecutionId); id != "" {
				if id == awssdk.ToString(a.ActionExecutionId) {
					return awssdk.ToString(le.Token)
				}
				continue
			}
			if st.LatestExecution != nil && awssdk.ToString(st.LatestExecution.PipelineExecutionId) == awssdk.ToString(a.PipelineExecutionId) {
				return awssdk.ToString(le.Token)
			}
		}
	}
	return ""
}

// executions maps each execution ID to the commits it carries, repo → SHA,
// using sources (repo → source action) inverted. A source action no repo
// maps to is ignored, and a repo with no revision is left out. Superseded and
// canceled executions are left out (see model.Deploy), so their action
// executions are dropped like those outside the window.
func executions(summaries []types.PipelineExecutionSummary, sources map[string]string) map[string]map[string]string {
	repoOf := make(map[string]string, len(sources))
	for repo, action := range sources {
		repoOf[action] = strings.ToLower(repo)
	}
	out := make(map[string]map[string]string, len(summaries))
	for _, e := range summaries {
		if e.Status == types.PipelineExecutionStatusSuperseded || e.Status == types.PipelineExecutionStatusCancelled {
			continue
		}
		revs := map[string]string{}
		for _, r := range e.SourceRevisions {
			repo, ok := repoOf[awssdk.ToString(r.ActionName)]
			if sha := awssdk.ToString(r.RevisionId); ok && sha != "" {
				revs[repo] = sha
			}
		}
		out[awssdk.ToString(e.PipelineExecutionId)] = revs
	}
	return out
}

// deployStatus maps an action status to a deploy status. Abandoned and any
// status AWS adds later report false: they never deployed anything.
func deployStatus(s types.ActionExecutionStatus) (model.DeployStatus, bool) {
	switch s {
	case types.ActionExecutionStatusSucceeded:
		return model.DeploySucceeded, true
	case types.ActionExecutionStatusFailed:
		return model.DeployFailed, true
	case types.ActionExecutionStatusInProgress:
		return model.DeployInProgress, true
	}
	return "", false
}

// sortNewestFirst orders deploys by FinishedAt, newest first; ties keep a
// stable order by execution ID so output does not flicker between polls.
func sortNewestFirst(deploys []model.Deploy) {
	slices.SortFunc(deploys, func(a, b model.Deploy) int {
		if c := b.FinishedAt.Compare(a.FinishedAt); c != 0 {
			return c
		}
		return cmp.Compare(b.ExecutionID, a.ExecutionID)
	})
}
