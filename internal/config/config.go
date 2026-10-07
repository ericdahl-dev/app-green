// Package config loads and validates ~/.config/app-green/config.toml.
// Style follows ../jira-green/internal/config/config.go.
package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/ericdahl-dev/app-green/internal/aws"
	"github.com/ericdahl-dev/app-green/internal/model"
	"github.com/ericdahl-dev/app-green/internal/rules"
)

// Settings is the optional [settings] table of durations written like "48h".
// Empty means "use the default"; read them through Thresholds.
type Settings struct {
	StaleReviewAfter string `toml:"stale_review_after"`
	DoneGrace        string `toml:"done_grace"`
	FadeAfter        string `toml:"fade_after"`
	PartialProdAfter string `toml:"partial_prod_after"`
}

// Jira is the [jira] table.
type Jira struct {
	Site         string   `toml:"site"`
	Email        string   `toml:"email"`
	TokenEnv     string   `toml:"token_env"`
	TokenCommand string   `toml:"token_command"`
	Projects     []string `toml:"projects"`
}

// GitHub is the [github] table.
type GitHub struct {
	Author       string   `toml:"author"`
	TokenEnv     string   `toml:"token_env"`
	TokenCommand string   `toml:"token_command"`
	Repos        []string `toml:"repos"` // "owner/name", lowercased by Load
}

// Account is one [[aws.accounts]] entry.
type Account struct {
	Name     string `toml:"name"`
	Profile  string `toml:"profile"`
	Region   string `toml:"region"`
	ReadOnly bool   `toml:"read_only"`
}

// AWS is the [aws] table.
type AWS struct {
	Accounts []Account `toml:"accounts"`
}

// ECS is one entry of an env's ecs list: a cluster and services in it.
// Names or ARNs are accepted for both.
type ECS struct {
	Cluster  string   `toml:"cluster"`
	Services []string `toml:"services"`
}

// EnvConfig is one [[envs]] entry as the user wrote it.
type EnvConfig struct {
	Account        string   `toml:"account"`
	Pipeline       string   `toml:"pipeline"`
	Stage          string   `toml:"stage"`
	DeployAction   string   `toml:"deploy_action"`
	ApprovalStage  string   `toml:"approval_stage"`
	ApprovalAction string   `toml:"approval_action"`
	Prod           bool     `toml:"prod"`
	Repos          []string `toml:"repos"`
	ECS            []ECS    `toml:"ecs"`
}

// Config is config.toml after Load.
type Config struct {
	Settings Settings    `toml:"settings"`
	Jira     Jira        `toml:"jira"`
	GitHub   GitHub      `toml:"github"`
	AWS      AWS         `toml:"aws"`
	EnvList  []EnvConfig `toml:"envs"`

	thresholds rules.Thresholds
	warnings   []string
}

// Default thresholds, used when [settings] leaves a key unset.
const (
	DefaultStaleReview = 48 * time.Hour
	DefaultDoneGrace   = 2 * time.Hour
	DefaultFadeAfter   = 24 * time.Hour
	DefaultPartialProd = 4 * time.Hour
)

// DefaultPath is $XDG_CONFIG_HOME/app-green/config.toml, or
// ~/.config/app-green/config.toml when XDG_CONFIG_HOME is unset. It returns
// an error when neither is available rather than guessing a relative path.
func DefaultPath() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("find config path: %w", err)
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "app-green", "config.toml"), nil
}

// Load reads and validates the config at path. Unknown keys are an error, so
// a typo never silently falls back to a default.
func Load(path string) (*Config, error) {
	var c Config
	md, err := toml.DecodeFile(path, &c)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, len(undecoded))
		for i, k := range undecoded {
			keys[i] = k.String()
		}
		return nil, fmt.Errorf("%s: unknown key(s): %s", path, strings.Join(keys, ", "))
	}
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

// validate checks the values the user wrote and fills the derived fields.
func (c *Config) validate() error {
	durations := []struct {
		key  string
		val  string
		def  time.Duration
		into *time.Duration
	}{
		{"stale_review_after", c.Settings.StaleReviewAfter, DefaultStaleReview, &c.thresholds.StaleReview},
		{"done_grace", c.Settings.DoneGrace, DefaultDoneGrace, &c.thresholds.DoneGrace},
		{"fade_after", c.Settings.FadeAfter, DefaultFadeAfter, &c.thresholds.FadeAfter},
		{"partial_prod_after", c.Settings.PartialProdAfter, DefaultPartialProd, &c.thresholds.PartialProd},
	}
	for _, d := range durations {
		v, err := durationOr(d.val, d.def)
		if err != nil {
			return fmt.Errorf("settings.%s: %w", d.key, err)
		}
		if v <= 0 {
			return fmt.Errorf("settings.%s must be positive, got %q", d.key, d.val)
		}
		*d.into = v
	}
	if err := c.validateJira(); err != nil {
		return err
	}
	if err := c.validateGitHub(); err != nil {
		return err
	}
	if err := c.validateAccounts(); err != nil {
		return err
	}
	return c.validateEnvs()
}

func (c *Config) validateAccounts() error {
	seen := map[string]bool{}
	for i, a := range c.AWS.Accounts {
		field := fmt.Sprintf("aws.accounts[%d]", i)
		if strings.TrimSpace(a.Name) == "" {
			return fmt.Errorf("%s.name is required", field)
		}
		if seen[a.Name] {
			return fmt.Errorf("%s.name: duplicate account %q", field, a.Name)
		}
		seen[a.Name] = true
		if strings.TrimSpace(a.Region) == "" {
			return fmt.Errorf("%s.region is required", field)
		}
	}
	return nil
}

func (c *Config) validateEnvs() error {
	if len(c.EnvList) == 0 {
		return errors.New("envs: no envs configured")
	}
	seen := map[string]bool{}
	for i := range c.EnvList {
		e := &c.EnvList[i]
		field := fmt.Sprintf("envs[%d]", i)
		for _, req := range []struct{ key, val string }{
			{"account", e.Account},
			{"pipeline", e.Pipeline},
			{"stage", e.Stage},
			{"deploy_action", e.DeployAction},
		} {
			if strings.TrimSpace(req.val) == "" {
				return fmt.Errorf("%s.%s is required", field, req.key)
			}
		}
		if _, ok := c.Account(e.Account); !ok {
			return fmt.Errorf("%s.account: unknown account %q (not in aws.accounts)", field, e.Account)
		}
		id := c.env(i).ID()
		if seen[id] {
			return fmt.Errorf("%s: duplicate env %s", field, id)
		}
		seen[id] = true
		if e.ApprovalStage != "" && e.ApprovalAction == "" {
			return fmt.Errorf("%s.approval_action is required when approval_stage is set", field)
		}
		for j, ecs := range e.ECS {
			f := fmt.Sprintf("%s.ecs[%d]", field, j)
			if strings.TrimSpace(ecs.Cluster) == "" {
				return fmt.Errorf("%s.cluster is required", f)
			}
			if len(ecs.Services) == 0 {
				return fmt.Errorf("%s.services: no services listed", f)
			}
			for _, svc := range ecs.Services {
				if strings.TrimSpace(svc) == "" {
					return fmt.Errorf("%s.services: empty service name", f)
				}
			}
		}
		repos, err := normalizeRepos(field+".repos", e.Repos)
		if err != nil {
			return err
		}
		e.Repos = repos
		if len(repos) == 0 {
			c.warnings = append(c.warnings, fmt.Sprintf("%s (%s) lists no repos: app-green will guess them from deploy history, a last resort that a missing or truncated history defeats - add repos = [\"owner/name\"]", field, c.env(i).ID()))
		}
	}
	return nil
}

// Warnings are problems Load found that do not stop the app, for main to
// print.
func (c *Config) Warnings() []string { return slices.Clone(c.warnings) }

// Account returns the [[aws.accounts]] entry with this name.
func (c *Config) Account(name string) (Account, bool) {
	for _, a := range c.AWS.Accounts {
		if a.Name == name {
			return a, true
		}
	}
	return Account{}, false
}

// Envs are the configured envs as model.Env, in file order: Order is the
// position in the file and ReadOnly comes from the account.
func (c *Config) Envs() []model.Env {
	out := make([]model.Env, len(c.EnvList))
	for i := range c.EnvList {
		out[i] = c.env(i)
	}
	return out
}

// env is EnvList[i] as a model.Env. Repos is a copy.
func (c *Config) env(i int) model.Env {
	e := c.EnvList[i]
	a, _ := c.Account(e.Account)
	return model.Env{
		Account:  e.Account,
		Pipeline: e.Pipeline,
		Stage:    e.Stage,
		Order:    i,
		Prod:     e.Prod,
		ReadOnly: a.ReadOnly,
		Repos:    slices.Clone(e.Repos),
	}
}

func (c *Config) validateGitHub() error {
	g := &c.GitHub
	if strings.TrimSpace(g.Author) == "" {
		return errors.New("github.author is required")
	}
	if err := checkTokenSource("github", g.TokenEnv, g.TokenCommand); err != nil {
		return err
	}
	if len(g.Repos) == 0 {
		return errors.New("github.repos: no repos configured")
	}
	repos, err := normalizeRepos("github.repos", g.Repos)
	if err != nil {
		return err
	}
	g.Repos = repos
	return nil
}

// repoName is a lowercased GitHub "owner/name".
var repoName = regexp.MustCompile(`^[a-z0-9-]+/[a-z0-9._-]+$`)

// normalizeRepos lowercases each "owner/name" in repos (adapters compare
// repos lowercased) and checks its form. field names the key in errors.
func normalizeRepos(field string, repos []string) ([]string, error) {
	out := make([]string, len(repos))
	for i, r := range repos {
		out[i] = strings.ToLower(strings.TrimSpace(r))
		if !repoName.MatchString(out[i]) {
			return nil, fmt.Errorf("%s: %q is not an owner/name repo", field, r)
		}
	}
	return out, nil
}

// projectKey is the Jira project key rule, copied from
// internal/jira/tickets.go so a bad key fails at Load, not at the first poll.
var projectKey = regexp.MustCompile(`^[A-Z][A-Z0-9_]+$`)

func (c *Config) validateJira() error {
	j := &c.Jira
	j.Site = strings.TrimRight(strings.TrimSpace(j.Site), "/")
	if j.Site == "" {
		return errors.New("jira.site is required")
	}
	u, err := url.Parse(j.Site)
	if err == nil && u.User != nil {
		// Not quoted back: it holds a password.
		return errors.New("jira.site must not contain a user name or password")
	}
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("jira.site must be an https URL such as https://example.atlassian.net, got %q", j.Site)
	}
	if strings.TrimSpace(j.Email) == "" {
		return errors.New("jira.email is required")
	}
	if err := checkTokenSource("jira", j.TokenEnv, j.TokenCommand); err != nil {
		return err
	}
	if len(j.Projects) == 0 {
		return errors.New("jira.projects: no projects configured")
	}
	for _, p := range j.Projects {
		if !projectKey.MatchString(p) {
			return fmt.Errorf("jira.projects: %q is not a Jira project key (uppercase letters, digits and _, starting with a letter)", p)
		}
	}
	return nil
}

// durationOr parses v like "48h" or "90m", or returns def when v is empty.
func durationOr(v string, def time.Duration) (time.Duration, error) {
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q (use a form like 48h or 90m)", v)
	}
	return d, nil
}

// Thresholds are the rules thresholds, defaults filled in.
func (c *Config) Thresholds() rules.Thresholds { return c.thresholds }

// PipelineKey names one pipeline in one account: the unit of one AWS
// History call.
type PipelineKey struct {
	Account  string
	Pipeline string
}

// StageSpecs groups the envs' stage specs by pipeline, each group in file
// order, for aws.Client.History.
func (c *Config) StageSpecs() map[PipelineKey][]aws.StageSpec {
	out := map[PipelineKey][]aws.StageSpec{}
	for _, e := range c.EnvList {
		k := PipelineKey{Account: e.Account, Pipeline: e.Pipeline}
		out[k] = append(out[k], e.stageSpec())
	}
	return out
}

// StageSpec is env's stage spec, looked up by env.ID().
func (c *Config) StageSpec(env model.Env) (aws.StageSpec, bool) {
	i := c.index(env)
	if i < 0 {
		return aws.StageSpec{}, false
	}
	return c.EnvList[i].stageSpec(), true
}

// stageSpec is e as an aws.StageSpec. An approval_action with no
// approval_stage is an approval inside the env's own stage.
func (e EnvConfig) stageSpec() aws.StageSpec {
	s := aws.StageSpec{
		Stage:          e.Stage,
		DeployAction:   e.DeployAction,
		ApprovalStage:  e.ApprovalStage,
		ApprovalAction: e.ApprovalAction,
	}
	if s.ApprovalAction != "" && s.ApprovalStage == "" {
		s.ApprovalStage = e.Stage
	}
	return s
}

// index is the position in EnvList of the env with env.ID(), or -1.
func (c *Config) index(env model.Env) int {
	for i := range c.EnvList {
		if c.env(i).ID() == env.ID() {
			return i
		}
	}
	return -1
}

// Services are env's ECS services, looked up by env.ID(), for
// aws.Client.Health. Names and ARNs pass through as written. Nil when the
// env lists none or is unknown.
func (c *Config) Services(env model.Env) []aws.Service {
	i := c.index(env)
	if i < 0 {
		return nil
	}
	var out []aws.Service
	for _, ecs := range c.EnvList[i].ECS {
		for _, name := range ecs.Services {
			out = append(out, aws.Service{Cluster: ecs.Cluster, Name: name})
		}
	}
	return out
}

// checkTokenSource requires exactly one token source in a table. The config
// file never holds a token itself.
func checkTokenSource(table, env, command string) error {
	if (strings.TrimSpace(env) == "") == (strings.TrimSpace(command) == "") {
		return fmt.Errorf("%s: set exactly one of token_env or token_command", table)
	}
	return nil
}

// JiraToken resolves the Jira API token from jira.token_env or
// jira.token_command.
func (c *Config) JiraToken() (string, error) {
	return resolveToken("jira", c.Jira.TokenEnv, c.Jira.TokenCommand)
}

// GitHubToken resolves the GitHub token from github.token_env or
// github.token_command.
func (c *Config) GitHubToken() (string, error) {
	return resolveToken("github", c.GitHub.TokenEnv, c.GitHub.TokenCommand)
}

// tokenCommandTimeout bounds how long a token_command may run.
var tokenCommandTimeout = 10 * time.Second

// maxStderr caps how much of a failed token_command's stderr an error quotes.
const maxStderr = 200

// resolveToken reads the variable env, or runs command with sh -c and
// trims its output. Errors never include stdout, where the token would be.
func resolveToken(table, env, command string) (string, error) {
	if command != "" {
		ctx, cancel := context.WithTimeout(context.Background(), tokenCommandTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, "sh", "-c", command)
		cmd.WaitDelay = time.Second // don't wait on a killed shell's children holding stdout
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if ctx.Err() != nil {
			return "", fmt.Errorf("%s.token_command timed out after %v", table, tokenCommandTimeout)
		}
		if err != nil {
			msg := strings.TrimSpace(stderr.String())
			if len(msg) > maxStderr {
				msg = msg[:maxStderr] + "..."
			}
			return "", fmt.Errorf("%s.token_command failed: %w: %s", table, err, msg)
		}
		tok := strings.TrimSpace(string(out))
		if tok == "" {
			return "", fmt.Errorf("%s.token_command produced no output", table)
		}
		return tok, nil
	}
	tok := strings.TrimSpace(os.Getenv(env))
	if tok == "" {
		return "", fmt.Errorf("%s.token_env %q is unset", table, env)
	}
	return tok, nil
}
