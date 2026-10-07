package resolver_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ericdahl-dev/app-green/internal/jira"
	"github.com/ericdahl-dev/app-green/internal/model"
	"github.com/ericdahl-dev/app-green/internal/resolver"
)

type approveCall struct {
	pipeline, stage, action, token string
	ok                             bool
	summary                        string
}

// envFor returns the configured env with this ID, as the UI sees it in a
// snapshot slot.
func envFor(t *testing.T, h *harness, id string) model.Env {
	t.Helper()
	for _, c := range h.r.Poll(context.Background()).Chains {
		for _, s := range c.Slots {
			if s.Env.ID() == id {
				return s.Env
			}
		}
	}
	t.Fatalf("no slot for env %s", id)
	return model.Env{}
}

func TestApproveUsesTheEnvsApproval(t *testing.T) {
	h := newHarness(t, nil, prodTestEnv, prodEnv)
	h.withShippedChain()
	env := envFor(t, h, "prod-acct/app-pipeline/Test")

	if err := h.r.Approve(context.Background(), env, "tok-1", true, "ship it"); err != nil {
		t.Fatalf("Approve = %v", err)
	}

	want := approveCall{"app-pipeline", "Test", "ApproveTest", "tok-1", true, "ship it"}
	if got := h.prod.approvals(); len(got) != 1 || got[0] != want {
		t.Errorf("Approve calls = %+v, want [%+v]", got, want)
	}
}

func TestApproveRefuses(t *testing.T) {
	h := newHarness(t, nil, prodTestEnv, prodEnv)
	h.withShippedChain()
	env := envFor(t, h, "prod-acct/app-pipeline/Test")
	readOnly := env
	readOnly.ReadOnly = true
	noApproval := envFor(t, h, "prod-acct/app-pipeline/Production")
	noClient := env
	noClient.Account = "other-acct"

	cases := []struct {
		name  string
		env   model.Env
		token string
		want  string
	}{
		{"read-only env", readOnly, "tok-1", "read-only"},
		{"empty token", env, "", "no approval token"},
		{"no approval configured", noApproval, "tok-1", "no approval_action"},
		{"no AWS client", noClient, "tok-1", "no AWS client"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := h.r.Approve(context.Background(), c.env, c.token, true, "")
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("Approve = %v, want an error containing %q", err, c.want)
			}
		})
	}
	if got := h.prod.approvals(); len(got) != 0 {
		t.Errorf("Approve calls = %+v, want none", got)
	}
}

func TestSuccessfulApproveRefreshes(t *testing.T) {
	h := newHarness(t, nil, prodTestEnv, prodEnv) // default 60s interval: only a refresh polls again
	h.withShippedChain()
	env := envFor(t, h, "prod-acct/app-pipeline/Test")
	out, _, _ := start(t, h)
	next(t, out)
	before, _ := h.tracker.calls()

	if err := h.r.Approve(context.Background(), env, "tok-1", true, ""); err != nil {
		t.Fatalf("Approve = %v", err)
	}

	next(t, out)
	if n, _ := h.tracker.calls(); n != before+1 {
		t.Errorf("MyTickets calls = %d, want %d (one poll after the action)", n, before+1)
	}
}

func TestRerunReRunsTheFailedJobsAndRefreshes(t *testing.T) {
	h := newHarness(t, nil)
	h.withShippedChain()
	out, _, _ := start(t, h)
	next(t, out)

	if err := h.r.Rerun(context.Background(), "acme/app", 42); err != nil {
		t.Fatalf("Rerun = %v", err)
	}

	if got := h.host.reruns(); len(got) != 1 || got[0] != (rerunCall{"acme/app", 42}) {
		t.Errorf("RerunFailedJobs calls = %+v, want one for acme/app run 42", got)
	}
	next(t, out)
}

func TestTransitionsListsAndTransitionMovesAndRefreshes(t *testing.T) {
	h := newHarness(t, nil)
	h.withShippedChain()
	h.tracker.set(func(f *fakeTracker) {
		f.transitions = map[string][]jira.Transition{"ABC-1": {{ID: "31", Name: "Done", ToName: "Done"}}}
	})
	out, _, _ := start(t, h)
	next(t, out)

	ts, err := h.r.Transitions(context.Background(), "ABC-1")
	if err != nil || len(ts) != 1 || ts[0].ID != "31" {
		t.Fatalf("Transitions = %+v, %v; want Done (31)", ts, err)
	}
	if err := h.r.Transition(context.Background(), "ABC-1", "31"); err != nil {
		t.Fatalf("Transition = %v", err)
	}

	h.tracker.set(func(f *fakeTracker) {
		if want := [][2]string{{"ABC-1", "31"}}; !slices.Equal(f.moves, want) {
			t.Errorf("DoTransition calls = %v, want %v", f.moves, want)
		}
	})
	next(t, out)
}

func TestPipelineURL(t *testing.T) {
	env := model.Env{Account: "prod-acct", Region: "us-east-1", Pipeline: "app-pipeline", Stage: "Production"}
	want := "https://us-east-1.console.aws.amazon.com/codesuite/codepipeline/pipelines/app-pipeline/view?region=us-east-1"
	if got := resolver.PipelineURL(env); got != want {
		t.Errorf("PipelineURL = %q, want %q", got, want)
	}
}
