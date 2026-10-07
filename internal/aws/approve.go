package aws

// Error folding adapted from aws-green internal/fix/actioner_aws.go
// (approvalError) and the summaries from internal/fix/fix.go.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/codepipeline"
	"github.com/aws/aws-sdk-go-v2/service/codepipeline/types"
)

// ErrApprovalAlreadyDecided means the approval token went stale between the
// poll and the decision: someone decided elsewhere, or the run moved on.
// Refresh rather than report a failure.
var ErrApprovalAlreadyDecided = errors.New("aws: approval already decided")

// ErrApprovalNotPermitted means the profile can see the approval but may not
// decide it (no codepipeline:PutApprovalResult).
var ErrApprovalNotPermitted = errors.New("aws: profile not permitted to decide approvals")

// maxSummary is PutApprovalResult's limit on the summary, in characters.
const maxSummary = 512

// Approve approves (ok) or rejects the waiting approval action identified by
// token. An empty summary gets a default naming app-green; a long one is cut
// to maxSummary.
func (c *Client) Approve(ctx context.Context, pipeline, stage, action, token string, ok bool, summary string) error {
	if token == "" {
		return fmt.Errorf("aws: approve %s/%s/%s: no approval token", pipeline, stage, action)
	}
	status := types.ApprovalStatusApproved
	if !ok {
		status = types.ApprovalStatusRejected
	}
	if strings.TrimSpace(summary) == "" {
		summary = "Approved via app-green"
		if !ok {
			summary = "Rejected via app-green"
		}
	}
	if r := []rune(summary); len(r) > maxSummary {
		summary = string(r[:maxSummary])
	}
	_, err := c.cp.PutApprovalResult(ctx, &codepipeline.PutApprovalResultInput{
		PipelineName: awssdk.String(pipeline),
		StageName:    awssdk.String(stage),
		ActionName:   awssdk.String(action),
		Token:        awssdk.String(token),
		Result: &types.ApprovalResult{
			Status:  status,
			Summary: awssdk.String(summary),
		},
	})
	return approvalError(err)
}

// approvalError folds AWS's two "someone already decided" errors into
// ErrApprovalAlreadyDecided and access denied into ErrApprovalNotPermitted,
// keeping the original error in the chain.
func approvalError(err error) error {
	if err == nil {
		return nil
	}
	var invalid *types.InvalidApprovalTokenException
	var completed *types.ApprovalAlreadyCompletedException
	if errors.As(err, &invalid) || errors.As(err, &completed) {
		return fmt.Errorf("%w: %w", ErrApprovalAlreadyDecided, err)
	}
	if IsAccessDenied(err) {
		return fmt.Errorf("%w: %w", ErrApprovalNotPermitted, err)
	}
	return fmt.Errorf("aws: PutApprovalResult: %w", err)
}
