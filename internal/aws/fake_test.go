package aws

import (
	"context"
	"errors"

	"github.com/aws/aws-sdk-go-v2/service/codepipeline"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
)

// fakePipeline is a pipelineAPI that returns canned outputs and records the
// inputs it was given.
type fakePipeline struct {
	pipeline *codepipeline.GetPipelineOutput
	execs    *codepipeline.ListPipelineExecutionsOutput
	// actionPages are returned in turn, one per ListActionExecutions call.
	actionPages []*codepipeline.ListActionExecutionsOutput
	state       *codepipeline.GetPipelineStateOutput
	err         error

	actionInputs []*codepipeline.ListActionExecutionsInput
	execInputs   []*codepipeline.ListPipelineExecutionsInput
	stateCalls   int
	approval     *codepipeline.PutApprovalResultInput
}

func (f *fakePipeline) GetPipeline(_ context.Context, in *codepipeline.GetPipelineInput, _ ...func(*codepipeline.Options)) (*codepipeline.GetPipelineOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.pipeline, nil
}

func (f *fakePipeline) ListPipelineExecutions(_ context.Context, in *codepipeline.ListPipelineExecutionsInput, _ ...func(*codepipeline.Options)) (*codepipeline.ListPipelineExecutionsOutput, error) {
	f.execInputs = append(f.execInputs, in)
	if f.err != nil {
		return nil, f.err
	}
	if f.execs == nil {
		return &codepipeline.ListPipelineExecutionsOutput{}, nil
	}
	return f.execs, nil
}

func (f *fakePipeline) ListActionExecutions(_ context.Context, in *codepipeline.ListActionExecutionsInput, _ ...func(*codepipeline.Options)) (*codepipeline.ListActionExecutionsOutput, error) {
	f.actionInputs = append(f.actionInputs, in)
	if f.err != nil {
		return nil, f.err
	}
	i := len(f.actionInputs) - 1
	if i >= len(f.actionPages) {
		if len(f.actionPages) == 0 {
			return &codepipeline.ListActionExecutionsOutput{}, nil
		}
		// Past the last canned page: repeat it, so a fake that always
		// returns a NextToken pages forever.
		i = len(f.actionPages) - 1
	}
	return f.actionPages[i], nil
}

func (f *fakePipeline) GetPipelineState(_ context.Context, _ *codepipeline.GetPipelineStateInput, _ ...func(*codepipeline.Options)) (*codepipeline.GetPipelineStateOutput, error) {
	f.stateCalls++
	if f.err != nil {
		return nil, f.err
	}
	if f.state == nil {
		return &codepipeline.GetPipelineStateOutput{}, nil
	}
	return f.state, nil
}

func (f *fakePipeline) PutApprovalResult(_ context.Context, in *codepipeline.PutApprovalResultInput, _ ...func(*codepipeline.Options)) (*codepipeline.PutApprovalResultOutput, error) {
	f.approval = in
	if f.err != nil {
		return nil, f.err
	}
	return &codepipeline.PutApprovalResultOutput{}, nil
}

// fakeECS is an ecsAPI keyed by cluster.
type fakeECS struct {
	byCluster map[string]*ecs.DescribeServicesOutput
	err       error
	inputs    []*ecs.DescribeServicesInput
}

func (f *fakeECS) DescribeServices(_ context.Context, in *ecs.DescribeServicesInput, _ ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error) {
	f.inputs = append(f.inputs, in)
	if f.err != nil {
		return nil, f.err
	}
	out, ok := f.byCluster[*in.Cluster]
	if !ok {
		return nil, errors.New("no such cluster")
	}
	return out, nil
}
