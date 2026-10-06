package aws

import (
	"context"
	"maps"
	"slices"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/codepipeline"
	"github.com/aws/aws-sdk-go-v2/service/codepipeline/types"
)

func sourceAction(name, repo string) types.ActionDeclaration {
	return types.ActionDeclaration{
		Name: awssdk.String(name),
		ActionTypeId: &types.ActionTypeId{
			Category: types.ActionCategorySource,
			Owner:    types.ActionOwnerAws,
			Provider: awssdk.String("CodeStarSourceConnection"),
			Version:  awssdk.String("1"),
		},
		Configuration: map[string]string{"FullRepositoryId": repo, "BranchName": "main"},
	}
}

func testPipeline() *codepipeline.GetPipelineOutput {
	return &codepipeline.GetPipelineOutput{Pipeline: &types.PipelineDeclaration{
		Name: awssdk.String("app-pipeline"),
		Stages: []types.StageDeclaration{
			{Name: awssdk.String("Source"), Actions: []types.ActionDeclaration{
				sourceAction("AppCode", "Acme/App"),
				sourceAction("InfraCode", "acme/infra"),
				sourceAction("ReportsCode", "acme/reports"),
			}},
			{Name: awssdk.String("Test"), Actions: []types.ActionDeclaration{{
				Name:          awssdk.String("Deploy"),
				ActionTypeId:  &types.ActionTypeId{Category: types.ActionCategoryDeploy},
				Configuration: map[string]string{"FullRepositoryId": "acme/not-a-source"},
			}}},
		},
	}}
}

func TestSources(t *testing.T) {
	c := &Client{cp: &fakePipeline{pipeline: testPipeline()}}
	got, err := c.Sources(context.Background(), "app-pipeline")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"acme/app": "AppCode", "acme/infra": "InfraCode", "acme/reports": "ReportsCode"}
	if !maps.Equal(got, want) {
		t.Errorf("Sources = %v, want %v", got, want)
	}
	if repos := Repos(got); !slices.Equal(repos, []string{"acme/app", "acme/infra", "acme/reports"}) {
		t.Errorf("Repos = %v", repos)
	}
}
