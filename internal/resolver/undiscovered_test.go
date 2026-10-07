package resolver_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ericdahl-dev/app-green/internal/model"
)

// bareStageProd and bareProdProd are two prod envs with no config repos:
// what they deploy comes only from their pipelines' sources.
const bareStageProd = `
[[envs]]
  account = "stage-acct"
  pipeline = "app-pipeline"
  stage = "Production"
  deploy_action = "Deploy"
  prod = true
`

const bareProdProd = `
[[envs]]
  account = "prod-acct"
  pipeline = "app-pipeline"
  stage = "Production"
  deploy_action = "Deploy"
  prod = true
`

// An env whose sources were never discovered (SSO expired at startup) must
// not drop out of the chain: that would show "in prod" from the other prod
// env alone.
func TestUndiscoveredEnvIsUnknownNotSkipped(t *testing.T) {
	h := newHarness(t, nil, bareStageProd, bareProdProd)
	h.withShippedChain()
	h.stage.set(func(f *fakeDeployer) {
		f.hist = map[string]map[string][]model.Deploy{"app-pipeline": {"Production": {succeeded("aaaa111", t0.Add(-time.Hour))}}}
	})
	h.prod.set(func(f *fakeDeployer) { f.histErr = errors.New("the SSO session has expired or is invalid") })

	c := h.r.Poll(context.Background()).Chains[0]

	if c.Stage == model.StageInProd {
		t.Errorf("stage = %v, want not in prod while prod-acct is unknown", c.Stage)
	}
	p := c.Slots[1]
	if p.Env.Account != "prod-acct" || !p.Applies || p.State != model.SlotUnknown || len(p.Env.Repos) != 0 {
		t.Errorf("prod-acct slot = %+v, want applying, unknown, no repos", p)
	}
	var found bool
	for _, f := range c.Flags {
		if f.Kind == model.FlagDeployUnknown && f.Level == model.Yellow && strings.Contains(f.Reason, "prod-acct") {
			found = true
		}
	}
	if !found {
		t.Errorf("flags = %+v, want a yellow deploy unknown naming prod-acct", c.Flags)
	}
}
