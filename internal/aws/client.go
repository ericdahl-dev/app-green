// Package aws reads CodePipeline and ECS for one account and turns them into
// model types: the repos a pipeline deploys, each stage's deploy history
// (approvals included), the runtime health of an Env's services, and the
// approve/reject action.
package aws

import (
	"context"
	"errors"
	"strings"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/ssocreds"
	"github.com/aws/aws-sdk-go-v2/service/codepipeline"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
)

// pipelineAPI is the part of the CodePipeline client the adapter uses, so
// tests can pass a fake.
type pipelineAPI interface {
	GetPipeline(context.Context, *codepipeline.GetPipelineInput, ...func(*codepipeline.Options)) (*codepipeline.GetPipelineOutput, error)
	ListPipelineExecutions(context.Context, *codepipeline.ListPipelineExecutionsInput, ...func(*codepipeline.Options)) (*codepipeline.ListPipelineExecutionsOutput, error)
	ListActionExecutions(context.Context, *codepipeline.ListActionExecutionsInput, ...func(*codepipeline.Options)) (*codepipeline.ListActionExecutionsOutput, error)
	GetPipelineState(context.Context, *codepipeline.GetPipelineStateInput, ...func(*codepipeline.Options)) (*codepipeline.GetPipelineStateOutput, error)
	PutApprovalResult(context.Context, *codepipeline.PutApprovalResultInput, ...func(*codepipeline.Options)) (*codepipeline.PutApprovalResultOutput, error)
}

// ecsAPI is the part of the ECS client the adapter uses.
type ecsAPI interface {
	DescribeServices(context.Context, *ecs.DescribeServicesInput, ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error)
}

// Client talks to one account (one SSO profile).
type Client struct {
	Account string
	cp      pipelineAPI
	ecs     ecsAPI
}

// New builds a Client from a shared-config profile. It does not call AWS, so
// an expired SSO session shows up on the first request, not here.
func New(ctx context.Context, account, profile, region string) (*Client, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithSharedConfigProfile(profile), config.WithRegion(region))
	if err != nil {
		return nil, err
	}
	return &Client{Account: account, cp: codepipeline.NewFromConfig(cfg), ecs: ecs.NewFromConfig(cfg)}, nil
}

// ssoExpiredMessages are the SDK's wordings for an SSO session that needs a
// new `aws sso login`, for errors that arrive without the typed error. With
// an sso-session profile the token provider's errors are not wrapped in
// ssocreds.InvalidTokenError, so a failed refresh or a missing cached token
// is only visible in the message.
var ssoExpiredMessages = []string{
	"the sso session has expired",
	"token has expired",
	"cached sso token is expired",
	"refresh cached sso token failed",
	"failed to read cached sso token file",
}

// IsSSOExpired reports whether err means the profile's SSO session has
// expired, so the UI can tell the user to run `aws sso login`.
func IsSSOExpired(err error) bool {
	if err == nil {
		return false
	}
	var invalid *ssocreds.InvalidTokenError
	if errors.As(err, &invalid) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, m := range ssoExpiredMessages {
		if strings.Contains(msg, m) {
			return true
		}
	}
	return false
}
