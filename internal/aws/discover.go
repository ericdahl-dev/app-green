package aws

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/codepipeline"
	"github.com/aws/aws-sdk-go-v2/service/codepipeline/types"
)

// Sources returns the repos a pipeline builds from: lowercased "owner/name"
// → the source action's name. It reads every Source-category action with a
// FullRepositoryId (CodeStar connections to GitHub).
func (c *Client) Sources(ctx context.Context, pipeline string) (map[string]string, error) {
	out, err := c.cp.GetPipeline(ctx, &codepipeline.GetPipelineInput{Name: awssdk.String(pipeline)})
	if err != nil {
		return nil, fmt.Errorf("aws: GetPipeline %s: %w", pipeline, err)
	}
	sources := map[string]string{}
	if out.Pipeline == nil {
		return sources, nil
	}
	for _, st := range out.Pipeline.Stages {
		for _, a := range st.Actions {
			if a.ActionTypeId == nil || a.ActionTypeId.Category != types.ActionCategorySource {
				continue
			}
			repo := strings.ToLower(strings.TrimSpace(a.Configuration["FullRepositoryId"]))
			if repo == "" {
				continue
			}
			sources[repo] = awssdk.ToString(a.Name)
		}
	}
	return sources, nil
}

// Repos returns the repos in sources, sorted: the value for Env.Repos.
func Repos(sources map[string]string) []string {
	return slices.Sorted(maps.Keys(sources))
}
