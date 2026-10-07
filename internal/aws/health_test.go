package aws

import (
	"context"
	"errors"
	"slices"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/ericdahl-dev/app-green/internal/model"
)

func svc(name string, desired, running int32) ecstypes.Service {
	return ecstypes.Service{ServiceName: awssdk.String(name), Status: awssdk.String("ACTIVE"), DesiredCount: desired, RunningCount: running}
}

func TestHealthOneService(t *testing.T) {
	f := &fakeECS{byCluster: map[string]*ecs.DescribeServicesOutput{
		"c1": {Services: []ecstypes.Service{svc("s1", 2, 1)}},
	}}
	got, warns, err := (&Client{ecs: f}).Health(context.Background(), []Service{{Cluster: "c1", Name: "s1"}})
	if err != nil {
		t.Fatal(err)
	}
	if want := (model.Health{Known: true, Desired: 2, Healthy: 1}); got != want || len(warns) > 0 {
		t.Errorf("Health = %+v %v, want %+v", got, warns, want)
	}
	if in := f.inputs[0]; awssdk.ToString(in.Cluster) != "c1" || !slices.Equal(in.Services, []string{"s1"}) {
		t.Errorf("input = %s %v", awssdk.ToString(in.Cluster), in.Services)
	}
}

func TestHealthSumsServicesOneCallPerCluster(t *testing.T) {
	f := &fakeECS{byCluster: map[string]*ecs.DescribeServicesOutput{
		"c1": {Services: []ecstypes.Service{svc("s1", 2, 2), svc("s2", 3, 1)}},
		"c2": {Services: []ecstypes.Service{svc("s3", 1, 1)}},
	}}
	got, warns, err := (&Client{ecs: f}).Health(context.Background(), []Service{
		{Cluster: "c1", Name: "s1"}, {Cluster: "c2", Name: "s3"}, {Cluster: "c1", Name: "s2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := (model.Health{Known: true, Desired: 6, Healthy: 4}); got != want || len(warns) > 0 {
		t.Errorf("Health = %+v %v, want %+v", got, warns, want)
	}
	if len(f.inputs) != 2 || !slices.Equal(f.inputs[0].Services, []string{"s1", "s2"}) {
		t.Errorf("DescribeServices calls = %d, first %v", len(f.inputs), f.inputs[0].Services)
	}
}

func TestHealthMissingServiceIsUnknown(t *testing.T) {
	inactive := svc("s3", 1, 0)
	inactive.Status = awssdk.String("INACTIVE")
	cases := map[string]*ecs.DescribeServicesOutput{
		"failure":  {Services: []ecstypes.Service{svc("s1", 2, 2)}, Failures: []ecstypes.Failure{{Arn: awssdk.String("arn:aws:ecs:us-east-1:111111111111:service/c1/s2"), Reason: awssdk.String("MISSING")}}},
		"absent":   {Services: []ecstypes.Service{svc("s1", 2, 2)}},
		"inactive": {Services: []ecstypes.Service{svc("s1", 2, 2), func() ecstypes.Service { s := inactive; s.ServiceName = awssdk.String("s2"); return s }()}},
	}
	for name, out := range cases {
		t.Run(name, func(t *testing.T) {
			f := &fakeECS{byCluster: map[string]*ecs.DescribeServicesOutput{"c1": out}}
			got, warns, err := (&Client{ecs: f}).Health(context.Background(), []Service{{Cluster: "c1", Name: "s1"}, {Cluster: "c1", Name: "s2"}})
			if err != nil {
				t.Fatal(err)
			}
			if got.Known || len(warns) != 1 {
				t.Errorf("Health = %+v warnings %v, want unknown with one warning", got, warns)
			}
		})
	}
}

func TestHealthNoServicesIsUnknown(t *testing.T) {
	got, _, err := (&Client{ecs: &fakeECS{}}).Health(context.Background(), nil)
	if err != nil || got.Known {
		t.Errorf("Health = %+v, %v; want unknown, nil", got, err)
	}
}

func TestHealthError(t *testing.T) {
	boom := errors.New("boom")
	got, _, err := (&Client{ecs: &fakeECS{err: boom}}).Health(context.Background(), []Service{{Cluster: "c1", Name: "s1"}})
	if !errors.Is(err, boom) || got.Known {
		t.Errorf("Health = %+v, %v; want unknown, boom", got, err)
	}
}

func TestHealthServiceARNsMatchByName(t *testing.T) {
	f := &fakeECS{byCluster: map[string]*ecs.DescribeServicesOutput{
		"c1": {Services: []ecstypes.Service{svc("s1", 2, 2), svc("s2", 1, 1)}},
	}}
	got, warns, err := (&Client{ecs: f}).Health(context.Background(), []Service{
		{Cluster: "c1", Name: "arn:aws:ecs:us-east-1:111111111111:service/c1/s1"},
		{Cluster: "c1", Name: "s2"},
		{Cluster: "c1", Name: "s1"}, // the same service again
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := (model.Health{Known: true, Desired: 3, Healthy: 3}); got != want || len(warns) > 0 {
		t.Errorf("Health = %+v %v, want %+v", got, warns, want)
	}
	if !slices.Equal(f.inputs[0].Services, []string{"s1", "s2"}) {
		t.Errorf("services = %v", f.inputs[0].Services)
	}
}
