package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// Ported from git-green internal/github/client.go (RerunFailedJobs), onto net/http.

// noFailedJobsFragment is what GitHub says when rerun-failed-jobs has nothing
// to retry individually: every job either passed or never started. Re-running
// the whole run is the right call in that case.
const noFailedJobsFragment = "no failed jobs"

// isNoFailedJobs reports whether err is GitHub declining a rerun-failed-jobs
// request because the run has no individually failed jobs.
func isNoFailedJobs(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && strings.Contains(strings.ToLower(ae.Message), noFailedJobsFragment)
}

// RerunFailedJobs re-runs the failed jobs of an Actions run in repo
// ("owner/name"), falling back to re-running the whole run when GitHub
// reports there were none. A read-only token fails here with a 403
// *APIError, returned so the UI can show it.
func (c *Client) RerunFailedJobs(ctx context.Context, repo string, runID int64) error {
	err := c.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/actions/runs/%d/rerun-failed-jobs", repo, runID), nil, nil)
	if err == nil {
		return nil
	}
	if !isNoFailedJobs(err) {
		return fmt.Errorf("re-running failed jobs for run %d in %s: %w", runID, repo, err)
	}
	if err := c.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/actions/runs/%d/rerun", repo, runID), nil, nil); err != nil {
		return fmt.Errorf("re-running run %d in %s: %w", runID, repo, err)
	}
	return nil
}
