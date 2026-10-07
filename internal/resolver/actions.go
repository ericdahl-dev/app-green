package resolver

import (
	"context"
	"fmt"
	"net/url"

	"github.com/ericdahl-dev/app-green/internal/jira"
	"github.com/ericdahl-dev/app-green/internal/model"
)

// Actions change the world on the user's behalf. They call the adapters
// directly and never take the poll lock, so they do not wait for a poll.
// Each successful action asks Run for a poll.

// Approve approves (ok) or rejects the approval gating env, identified by
// token from the slot's AwaitingApproval deploy, using env's ApprovalStage
// and ApprovalAction. It refuses, calling nothing, for a read-only env, an
// empty token, an env with no approval configured, or an account with no
// AWS client.
func (r *Resolver) Approve(ctx context.Context, env model.Env, token string, ok bool, summary string) error {
	switch {
	case env.ReadOnly:
		return fmt.Errorf("approve %s: the account is read-only", env.ID())
	case token == "":
		return fmt.Errorf("approve %s: no approval token (nothing is waiting)", env.ID())
	case env.ApprovalAction == "":
		return fmt.Errorf("approve %s: no approval_action configured", env.ID())
	}
	d, found := r.ad.AWS[env.Account]
	if !found {
		return fmt.Errorf("approve %s: no AWS client for account %s", env.ID(), env.Account)
	}
	return r.refreshed(d.Approve(ctx, env.Pipeline, env.ApprovalStage, env.ApprovalAction, token, ok, summary))
}

// Rerun re-runs the failed jobs of GitHub Actions run runID in repo
// ("owner/name"), from a failing check's Check.RunID. A runID of 0 or less
// (a check that is not an Actions run) is refused without calling GitHub.
func (r *Resolver) Rerun(ctx context.Context, repo string, runID int64) error {
	if runID <= 0 {
		return fmt.Errorf("rerun in %s: check has no GitHub Actions run to re-run", repo)
	}
	return r.refreshed(r.ad.Code.RerunFailedJobs(ctx, repo, runID))
}

// Transitions lists the workflow moves Jira allows from ticket key's
// current status. It only reads, so it asks for no poll.
func (r *Resolver) Transitions(ctx context.Context, key string) ([]jira.Transition, error) {
	return r.ad.Tracker.Transitions(ctx, key)
}

// Transition moves ticket key through transition id, one of Transitions'.
func (r *Resolver) Transition(ctx context.Context, key, id string) error {
	return r.refreshed(r.ad.Tracker.DoTransition(ctx, key, id))
}

// refreshed asks Run for a poll when err is nil, so the action shows up
// without waiting for the interval, and returns err. It never blocks: a
// poll already asked for covers this one.
func (r *Resolver) refreshed(err error) error {
	if err == nil {
		select {
		case r.kick <- struct{}{}:
		default:
		}
	}
	return err
}

// PipelineURL is the AWS console page for env's pipeline, in env's region.
func PipelineURL(env model.Env) string {
	return fmt.Sprintf("https://%s.console.aws.amazon.com/codesuite/codepipeline/pipelines/%s/view?region=%s",
		env.Region, url.PathEscape(env.Pipeline), url.QueryEscape(env.Region))
}
