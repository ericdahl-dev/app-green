package config

import (
	"reflect"
	"time"

	"github.com/ericdahl-dev/app-green/internal/aws"
	"github.com/ericdahl-dev/app-green/internal/model"
	"github.com/ericdahl-dev/app-green/internal/rules"
	"path/filepath"
	"strings"
	"testing"
)

func load(t *testing.T, name string) *Config {
	t.Helper()
	c, err := Load(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("Load(%s): %v", name, err)
	}
	return c
}

func TestLoadOKJira(t *testing.T) {
	c := load(t, "ok.toml")
	if c.Jira.Site != "https://example.atlassian.net" {
		t.Errorf("site = %q, want trailing slash trimmed", c.Jira.Site)
	}
	if c.Jira.Email != "me@example.com" {
		t.Errorf("email = %q", c.Jira.Email)
	}
	if strings.Join(c.Jira.Projects, ",") != "ABC" {
		t.Errorf("projects = %v", c.Jira.Projects)
	}
}

// TestLoadBad checks that each bad-*.toml fails with an error naming the
// problem.
func TestLoadBad(t *testing.T) {
	tests := []struct{ file, want string }{
		{"bad-unknown-key.toml", "unknown key(s): settings.done_grase"},
		{"bad-env-order.toml", "unknown key(s): envs.order"}, // Order is file order
		{"bad-duration.toml", `settings.fade_after: invalid duration "soon"`},
		{"bad-jira-http.toml", "jira.site must be an https URL"},
		{"bad-jira-userinfo.toml", "jira.site must not contain a user name or password"},
		{"bad-jira-no-email.toml", "jira.email is required"},
		{"bad-no-projects.toml", "jira.projects: no projects configured"},
		{"bad-project-key.toml", `jira.projects: "abc" is not a Jira project key`},
		{"bad-github-no-author.toml", "github.author is required"},
		{"bad-github-no-repos.toml", "github.repos: no repos configured"},
		{"bad-github-repo.toml", `github.repos: "acme" is not an owner/name repo`},
		{"bad-unknown-account.toml", `envs[1].account: unknown account "nope-acct"`},
		{"bad-no-envs.toml", "envs: no envs configured"},
		{"bad-duplicate-env.toml", "envs[1]: duplicate env stage-acct/app-pipeline/Test"},
		{"bad-duplicate-account.toml", `aws.accounts[1].name: duplicate account "stage-acct"`},
		{"bad-account-no-region.toml", "aws.accounts[1].region is required"},
		{"bad-env-no-deploy-action.toml", "envs[1].deploy_action is required"},
		{"bad-approval-stage-only.toml", "envs[1].approval_action is required when approval_stage is set"},
		{"bad-ecs-no-cluster.toml", "envs[0].ecs[0].cluster is required"},
		{"bad-ecs-no-services.toml", "envs[0].ecs[0].services: no services listed"},
		{"bad-token-both.toml", "github: set exactly one of token_env or token_command"},
		{"bad-token-none.toml", "jira: set exactly one of token_env or token_command"},
		{"bad-duration-negative.toml", `settings.stale_review_after must be positive, got "-1h"`},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			_, err := Load(filepath.Join("testdata", tt.file))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Load(%s) error = %v, want it to contain %q", tt.file, err, tt.want)
			}
		})
	}
}

func TestThresholds(t *testing.T) {
	got := load(t, "ok.toml").Thresholds()
	want := rules.Thresholds{StaleReview: 48 * time.Hour, DoneGrace: 2 * time.Hour, FadeAfter: 24 * time.Hour, PartialProd: 4 * time.Hour}
	if got != want {
		t.Errorf("ok.toml Thresholds() = %+v, want %+v", got, want)
	}
	// minimal.toml sets only partial_prod_after; the rest default.
	got = load(t, "minimal.toml").Thresholds()
	want.PartialProd = 90 * time.Minute
	if got != want {
		t.Errorf("minimal.toml Thresholds() = %+v, want %+v", got, want)
	}
}

func TestGitHubReposLowercased(t *testing.T) {
	c := load(t, "ok.toml")
	if strings.Join(c.GitHub.Repos, ",") != "acme/app" {
		t.Errorf("github.repos = %v, want [acme/app]", c.GitHub.Repos)
	}
	if c.GitHub.Author != "me" {
		t.Errorf("github.author = %q", c.GitHub.Author)
	}
}

func TestEnvs(t *testing.T) {
	envs := load(t, "ok.toml").Envs()
	if len(envs) != 2 {
		t.Fatalf("got %d envs, want 2", len(envs))
	}
	want := []model.Env{
		{Account: "stage-acct", Pipeline: "app-pipeline", Stage: "Test", Order: 0, Repos: []string{"acme/app"}},
		{Account: "prod-acct", Pipeline: "app-pipeline", Stage: "Production", Order: 1, Prod: true, ReadOnly: true, Repos: []string{"acme/app"}},
	}
	for i, w := range want {
		if !reflect.DeepEqual(envs[i], w) {
			t.Errorf("envs[%d] = %+v, want %+v", i, envs[i], w)
		}
	}
}

func TestStageSpecs(t *testing.T) {
	got := load(t, "grouped.toml").StageSpecs()
	want := map[PipelineKey][]aws.StageSpec{
		{Account: "stage-acct", Pipeline: "app-pipeline"}: {
			// approval_stage defaults to the env's own stage.
			{Stage: "Test", DeployAction: "Deploy", ApprovalStage: "Test", ApprovalAction: "ApproveTest"},
			{Stage: "Staging", DeployAction: "Deploy"},
		},
		{Account: "prod-acct", Pipeline: "app-pipeline"}: {
			{Stage: "Production", DeployAction: "deploy", ApprovalStage: "Test", ApprovalAction: "ApproveProd"},
		},
		{Account: "stage-acct", Pipeline: "other-pipeline"}: {
			{Stage: "Test", DeployAction: "Deploy"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("StageSpecs() = %+v\nwant %+v", got, want)
	}
}

func TestServicesAndStageSpec(t *testing.T) {
	c := load(t, "ok.toml")
	envs := c.Envs()
	want := []aws.Service{
		{Cluster: "c1", Name: "s1"},
		{Cluster: "c1", Name: "arn:aws:ecs:us-east-1:111111111111:service/c1/s2"}, // aws reduces ARNs
	}
	if got := c.Services(envs[0]); !reflect.DeepEqual(got, want) {
		t.Errorf("Services(Test) = %+v, want %+v", got, want)
	}
	if got := c.Services(envs[1]); len(got) != 0 {
		t.Errorf("Services(Production) = %+v, want none", got)
	}
	spec, ok := c.StageSpec(envs[1])
	if !ok || spec != (aws.StageSpec{Stage: "Production", DeployAction: "deploy", ApprovalStage: "Test", ApprovalAction: "ApproveProd"}) {
		t.Errorf("StageSpec(Production) = %+v, %v", spec, ok)
	}
	if _, ok := c.StageSpec(model.Env{Account: "x", Pipeline: "y", Stage: "z"}); ok {
		t.Error("StageSpec of an unknown env reported ok")
	}
}

func TestWarnings(t *testing.T) {
	if w := load(t, "ok.toml").Warnings(); len(w) != 0 {
		t.Errorf("ok.toml warnings = %q, want none", w)
	}
	w := load(t, "minimal.toml").Warnings()
	if len(w) != 1 || !strings.Contains(w[0], "envs[0] (stage-acct/app-pipeline/Test) lists no repos") || !strings.Contains(w[0], "last resort") {
		t.Errorf("minimal.toml warnings = %q, want one naming the env and the history fallback", w)
	}
}

func TestTokenEnv(t *testing.T) {
	c := load(t, "ok.toml")
	t.Setenv("JIRA_API_TOKEN", " jtok\n")
	t.Setenv("GITHUB_TOKEN", "")
	if tok, err := c.JiraToken(); err != nil || tok != "jtok" {
		t.Errorf("JiraToken() = %q, %v; want jtok", tok, err)
	}
	if _, err := c.GitHubToken(); err == nil || !strings.Contains(err.Error(), `github.token_env "GITHUB_TOKEN" is unset`) {
		t.Errorf("GitHubToken() error = %v, want unset error", err)
	}
}

func TestTokenCommand(t *testing.T) {
	c := load(t, "minimal.toml")
	for name, f := range map[string]func() (string, error){"jira": c.JiraToken, "github": c.GitHubToken} {
		if tok, err := f(); err != nil || tok != "tok" {
			t.Errorf("%s token = %q, %v; want tok", name, tok, err)
		}
	}
}

func TestTokenCommandErrors(t *testing.T) {
	c := load(t, "token-fail.toml")
	_, err := c.JiraToken()
	if err == nil || !strings.Contains(err.Error(), "jira.token_command failed") || !strings.Contains(err.Error(), "boom") {
		t.Errorf("JiraToken() error = %v, want a failure with stderr", err)
	}
	if err != nil && strings.Contains(err.Error(), "sekrit") {
		t.Errorf("JiraToken() error leaks stdout: %v", err)
	}
	if _, err := c.GitHubToken(); err == nil || !strings.Contains(err.Error(), "github.token_command produced no output") {
		t.Errorf("GitHubToken() error = %v, want no-output error", err)
	}
}

func TestDefaultPath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/xdg")
	if p, err := DefaultPath(); err != nil || p != "/xdg/app-green/config.toml" {
		t.Errorf("DefaultPath() with XDG_CONFIG_HOME = %q, %v", p, err)
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "/home/me")
	if p, err := DefaultPath(); err != nil || p != "/home/me/.config/app-green/config.toml" {
		t.Errorf("DefaultPath() without XDG_CONFIG_HOME = %q, %v", p, err)
	}
}
