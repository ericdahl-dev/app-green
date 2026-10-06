package aws

import (
	"context"
	"errors"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/codepipeline/types"
	"github.com/aws/smithy-go"
)

func TestApprove(t *testing.T) {
	cases := []struct {
		ok          bool
		summary     string
		wantStatus  types.ApprovalStatus
		wantSummary string
	}{
		{true, "ABC-1 looks good in test", types.ApprovalStatusApproved, "ABC-1 looks good in test"},
		{false, "", types.ApprovalStatusRejected, "Rejected via app-green"},
		{true, "  ", types.ApprovalStatusApproved, "Approved via app-green"},
	}
	for _, tc := range cases {
		f := &fakePipeline{}
		err := (&Client{cp: f}).Approve(context.Background(), "app-pipeline", "Test", "ManualApprovalOfTestEnvironment", "tok-3", tc.ok, tc.summary)
		if err != nil {
			t.Fatal(err)
		}
		in := f.approval
		if in == nil {
			t.Fatal("PutApprovalResult not called")
		}
		if awssdk.ToString(in.PipelineName) != "app-pipeline" || awssdk.ToString(in.StageName) != "Test" ||
			awssdk.ToString(in.ActionName) != "ManualApprovalOfTestEnvironment" || awssdk.ToString(in.Token) != "tok-3" {
			t.Errorf("input = %+v", in)
		}
		if in.Result == nil || in.Result.Status != tc.wantStatus || awssdk.ToString(in.Result.Summary) != tc.wantSummary {
			t.Errorf("result = %+v, want %s %q", in.Result, tc.wantStatus, tc.wantSummary)
		}
	}
}

func TestApproveNeedsToken(t *testing.T) {
	f := &fakePipeline{}
	if err := (&Client{cp: f}).Approve(context.Background(), "app-pipeline", "Test", "ManualApprovalOfTestEnvironment", "", true, ""); err == nil {
		t.Error("want an error for an empty token")
	}
	if f.approval != nil {
		t.Error("PutApprovalResult called without a token")
	}
}

func TestApproveErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want error
	}{
		{"stale token", &types.InvalidApprovalTokenException{Message: awssdk.String("bad token")}, ErrApprovalAlreadyDecided},
		{"already done", &types.ApprovalAlreadyCompletedException{Message: awssdk.String("done")}, ErrApprovalAlreadyDecided},
		{"access denied", &smithy.OperationError{Err: &smithy.GenericAPIError{Code: "AccessDeniedException"}}, ErrApprovalNotPermitted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakePipeline{err: tc.err}
			err := (&Client{cp: f}).Approve(context.Background(), "app-pipeline", "Test", "ManualApprovalOfTestEnvironment", "tok-3", true, "")
			if !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
		})
	}
	boom := errors.New("boom")
	err := (&Client{cp: &fakePipeline{err: boom}}).Approve(context.Background(), "app-pipeline", "Test", "a", "tok", false, "")
	if !errors.Is(err, boom) || errors.Is(err, ErrApprovalAlreadyDecided) || errors.Is(err, ErrApprovalNotPermitted) {
		t.Errorf("err = %v, want boom only", err)
	}
}
