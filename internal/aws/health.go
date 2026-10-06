package aws

import (
	"context"
	"fmt"
	"slices"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"

	"github.com/ericdahl-dev/app-green/internal/model"
)

// Service is one ECS service an Env runs.
type Service struct {
	Cluster string
	Name    string
}

// describeLimit is how many services one DescribeServices call takes.
const describeLimit = 10

// Health sums desired and running tasks across services: one Health per Env.
// It is unknown when no services are configured, and when any configured
// service is missing or not ACTIVE (with a warning naming it), so a
// half-found Env never looks healthy or unhealthy on partial counts.
func (c *Client) Health(ctx context.Context, services []Service) (model.Health, []string, error) {
	if len(services) == 0 {
		return model.Health{}, nil, nil
	}
	var clusters []string
	names := map[string][]string{}
	for _, s := range services {
		if _, ok := names[s.Cluster]; !ok {
			clusters = append(clusters, s.Cluster)
		}
		if !slices.Contains(names[s.Cluster], s.Name) {
			names[s.Cluster] = append(names[s.Cluster], s.Name)
		}
	}

	h := model.Health{Known: true}
	var warns []string
	for _, cluster := range clusters {
		for chunk := range slices.Chunk(names[cluster], describeLimit) {
			out, err := c.ecs.DescribeServices(ctx, &ecs.DescribeServicesInput{
				Cluster:  awssdk.String(cluster),
				Services: chunk,
			})
			if err != nil {
				return model.Health{}, warns, fmt.Errorf("aws: DescribeServices %s: %w", cluster, err)
			}
			found := map[string]bool{}
			for _, s := range out.Services {
				if awssdk.ToString(s.Status) != "ACTIVE" {
					continue
				}
				found[awssdk.ToString(s.ServiceName)] = true
				h.Desired += int(s.DesiredCount)
				h.Healthy += int(s.RunningCount)
			}
			for _, name := range chunk {
				if !found[name] {
					warns = append(warns, fmt.Sprintf("aws: ECS service %s/%s not found or not active", cluster, name))
				}
			}
		}
	}
	if len(warns) > 0 {
		return model.Health{}, warns, nil
	}
	return h, nil, nil
}
