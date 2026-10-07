package config

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ericdahl-dev/app-green/internal/aws"
	"github.com/ericdahl-dev/app-green/internal/model"
	"github.com/ericdahl-dev/app-green/internal/rules"
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
		{"bad-no-repos-to-watch.toml", "no repos to watch: set github.repos or envs[].repos"},
		{"bad-github-repo.toml", `github.repos: "acme" is not an owner/name repo`},
		{"bad-unknown-account.toml", `envs[1] (nope-acct/app-pipeline/Production).account: unknown account "nope-acct"`},
		{"bad-no-envs.toml", "envs: no envs configured"},
		{"bad-duplicate-env.toml", "envs[1] (stage-acct/app-pipeline/Test): duplicate env"},
		{"bad-duplicate-account.toml", `aws.accounts[1] (stage-acct).name: duplicate account "stage-acct"`},
		{"bad-account-no-region.toml", "aws.accounts[1] (prod-acct).region is required"},
		{"bad-env-no-deploy-action.toml", "envs[1] (prod-acct/app-pipeline/Production).deploy_action is required"},
		{"bad-approval-stage-only.toml", "envs[1] (prod-acct/app-pipeline/Production).approval_action is required when approval_stage is set"},
		{"bad-ecs-no-cluster.toml", "envs[0] (stage-acct/app-pipeline/Test).ecs[0].cluster is required"},
		{"bad-ecs-no-services.toml", "envs[0] (stage-acct/app-pipeline/Test).ecs[0].services: no services listed"},
		{"bad-token-both.toml", "github: set exactly one of token_env or token_command"},
		{"bad-token-none.toml", "jira: set exactly one of token_env or token_command"},
		{"bad-duration-unit.toml", `settings.done_grace: invalid duration "2x"`},
		{"bad-poll-interval.toml", `settings.poll_interval must be at least 15s, got "10s"`},
		{"bad-jira-query.toml", `jira.site must not have a query or fragment`},
		{"bad-jira-forcequery.toml", `jira.site must not have a query or fragment`},
		{"bad-jira-fragment.toml", `jira.site must not have a query or fragment`},
		{"bad-project-duplicate.toml", `jira.projects: duplicate project "ABC"`},
		{"bad-account-no-profile.toml", `aws.accounts[1] (prod-acct).profile is required`},
		{"bad-account-blank-region.toml", `aws.accounts[1] (prod-acct).region is required`},
		{"bad-env-repo.toml", `envs[1] (prod-acct/app-pipeline/Production).repos: "acme" is not an owner/name repo`},
		{"bad-env-pipeline-chars.toml", `envs[1] (prod-acct/app pipeline/Production).pipeline: "app pipeline" has characters outside A-Z a-z 0-9 . @ _ -`},
		{"bad-env-action-chars.toml", `envs[1] (prod-acct/app-pipeline/Production).deploy_action: "deploy/now" has characters outside`},
		{"bad-env-approval-stage-chars.toml", `envs[1] (prod-acct/app-pipeline/Production).approval_stage: "Test!" has characters outside`},
		{"bad-approval-is-deploy.toml", `envs[0] (stage-acct/app-pipeline/Test).approval_action must differ from deploy_action when the approval is in the same stage`},
		{"bad-ecs-duplicate.toml", `envs[0] (stage-acct/app-pipeline/Test).ecs[2].services: duplicate service "s1"`},
		{"bad-ecs-duplicate-same-entry.toml", `envs[0] (stage-acct/app-pipeline/Test).ecs[0].services: duplicate service "s1"`},
		{"bad-repo-leading-dash.toml", `github.repos: "-acme/app" is not an owner/name repo`},
		{"bad-repo-dot.toml", `github.repos: "acme/." is not an owner/name repo`},
		{"bad-repo-dotdot.toml", `github.repos: "acme/.." is not an owner/name repo`},
		{"bad-repo-two-slashes.toml", `github.repos: "acme/app/x" is not an owner/name repo`},
		{"bad-duration-day-minus.toml", `settings.fade_after: invalid duration "1d-23h"`},
		{"bad-duration-day-plus.toml", `settings.fade_after: invalid duration "1d+1h"`},
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

func TestThresholdsDays(t *testing.T) {
	got := load(t, "days.toml").Thresholds()
	want := rules.Thresholds{StaleReview: 48 * time.Hour, DoneGrace: DefaultDoneGrace, FadeAfter: 36 * time.Hour, PartialProd: 90 * time.Minute}
	if got != want {
		t.Errorf("days.toml Thresholds() = %+v, want %+v", got, want)
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
		// approval_stage defaults to the env's own stage.
		{Account: "stage-acct", Region: "us-east-1", Pipeline: "app-pipeline", Stage: "Test", Order: 0, Repos: []string{"acme/app"},
			ApprovalStage: "Test", ApprovalAction: "ApproveTest"},
		{Account: "prod-acct", Region: "us-east-1", Pipeline: "app-pipeline", Stage: "Production", Order: 1, Prod: true, ReadOnly: true, Repos: []string{"acme/app"},
			ApprovalStage: "Test", ApprovalAction: "ApproveProd"},
	}
	for i, w := range want {
		if !reflect.DeepEqual(envs[i], w) {
			t.Errorf("envs[%d] = %+v, want %+v", i, envs[i], w)
		}
	}
}

func TestStageSpecs(t *testing.T) {
	got := load(t, "grouped.toml").StageSpecs()
	// Pipelines in order of first appearance, specs in file order.
	want := []PipelineSpecs{
		{Key: PipelineKey{Account: "stage-acct", Pipeline: "app-pipeline"}, Specs: []aws.StageSpec{
			// approval_stage defaults to the env's own stage.
			{Stage: "Test", DeployAction: "Deploy", ApprovalStage: "Test", ApprovalAction: "ApproveTest"},
			{Stage: "Staging", DeployAction: "Deploy"},
		}},
		{Key: PipelineKey{Account: "prod-acct", Pipeline: "app-pipeline"}, Specs: []aws.StageSpec{
			{Stage: "Production", DeployAction: "deploy", ApprovalStage: "Test", ApprovalAction: "ApproveProd"},
		}},
		{Key: PipelineKey{Account: "stage-acct", Pipeline: "other-pipeline"}, Specs: []aws.StageSpec{
			{Stage: "Test", DeployAction: "Deploy"},
		}},
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
	t.Setenv("HOME", "/home/me")
	for _, xdg := range []string{"", "relative/dir"} { // the XDG spec says ignore a relative path
		t.Setenv("XDG_CONFIG_HOME", xdg)
		if p, err := DefaultPath(); err != nil || p != "/home/me/.config/app-green/config.toml" {
			t.Errorf("DefaultPath() with XDG_CONFIG_HOME=%q = %q, %v", xdg, p, err)
		}
	}
}

func TestPollInterval(t *testing.T) {
	if got := load(t, "ok.toml").PollInterval(); got != 60*time.Second {
		t.Errorf("ok.toml PollInterval() = %v, want default 60s", got)
	}
	if got := load(t, "days.toml").PollInterval(); got != 30*time.Second {
		t.Errorf("days.toml PollInterval() = %v, want 30s", got)
	}
}

func TestWatchedRepos(t *testing.T) {
	tests := []struct {
		file string
		want []string
	}{
		{"ok.toml", []string{"acme/app"}},
		{"grouped.toml", []string{"acme/app", "acme/other"}},
		{"env-repos-only.toml", []string{"acme/app", "acme/lib"}},
	}
	for _, tt := range tests {
		if got := load(t, tt.file).WatchedRepos(); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s WatchedRepos() = %v, want %v", tt.file, got, tt.want)
		}
	}
}

func TestJiraSitePath(t *testing.T) {
	if got := load(t, "site-path.toml").Jira.Site; got != "https://example.atlassian.net/jira" {
		t.Errorf("site = %q, want the path kept and the trailing slash trimmed", got)
	}
}

func TestWhitespaceTrimmed(t *testing.T) {
	c := load(t, "whitespace.toml")
	ref := load(t, "ok.toml")
	if !reflect.DeepEqual(c.AWS.Accounts, ref.AWS.Accounts) {
		t.Errorf("accounts = %+v, want %+v", c.AWS.Accounts, ref.AWS.Accounts)
	}
	if !reflect.DeepEqual(c.Envs(), ref.Envs()) {
		t.Errorf("Envs() = %+v, want %+v", c.Envs(), ref.Envs())
	}
	if !reflect.DeepEqual(c.StageSpecs(), ref.StageSpecs()) {
		t.Errorf("StageSpecs() = %+v, want %+v", c.StageSpecs(), ref.StageSpecs())
	}
}

func TestRepoNames(t *testing.T) {
	for _, r := range []string{"acme/app", "acme_co/app.js", "a1-b/.github", "acme/my_app-2"} {
		if _, err := normalizeRepos("github.repos", []string{r}); err != nil {
			t.Errorf("normalizeRepos(%q) = %v, want ok", r, err)
		}
	}
}

func TestTokenCommandTimeout(t *testing.T) {
	old := tokenCommandTimeout
	tokenCommandTimeout = 100 * time.Millisecond
	t.Cleanup(func() { tokenCommandTimeout = old })
	start := time.Now()
	_, err := load(t, "token-slow.toml").JiraToken()
	if err == nil || !strings.Contains(err.Error(), "jira.token_command timed out after 100ms") {
		t.Errorf("JiraToken() error = %v, want a timeout", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("JiraToken() took %v, want it cut off near the timeout", d)
	}
}

func TestQuoteStderrRuneBoundary(t *testing.T) {
	got := quoteStderr(strings.Repeat("\u00e9", maxStderr)) // 2 bytes each
	if !utf8.ValidString(got) || !strings.HasSuffix(got, "...") || len(got) > maxStderr+len("...") {
		t.Errorf("quoteStderr cut badly: %q (len %d)", got, len(got))
	}
	if got := quoteStderr("  short\n"); got != "short" {
		t.Errorf("quoteStderr(short) = %q", got)
	}
}

func TestEnvsReturnsCopies(t *testing.T) {
	c := load(t, "ok.toml")
	c.Envs()[0].Repos[0] = "changed/repo"
	if got := c.Envs()[0].Repos[0]; got != "acme/app" {
		t.Errorf("Envs() shares Repos with the config: got %q after editing a copy", got)
	}
}
