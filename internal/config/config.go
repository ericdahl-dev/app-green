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
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/BurntSushi/toml"
	"github.com/ericdahl-dev/app-green/internal/aws"
	"github.com/ericdahl-dev/app-green/internal/model"
	"github.com/ericdahl-dev/app-green/internal/rules"
)

// Settings is the optional [settings] table of durations written like "2d",
// "48h" or "90m".
// Empty means "use the default"; read them through Thresholds and
// PollInterval.
type Settings struct {
	StaleReviewAfter string `toml:"stale_review_after"`
	DoneGrace        string `toml:"done_grace"`
	FadeAfter        string `toml:"fade_after"`
	PartialProdAfter string `toml:"partial_prod_after"`
	PollInterval     string `toml:"poll_interval"` // read through PollInterval
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
	Author       string `toml:"author"`
	TokenEnv     string `toml:"token_env"`
	TokenCommand string `toml:"token_command"`
	// Repos are extra "owner/name" repos to watch, lowercased by Load.
	// Optional: WatchedRepos adds every env's repos.
	Repos []string `toml:"repos"`
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
	Account      string `toml:"account"`
	Pipeline     string `toml:"pipeline"`
	Stage        string `toml:"stage"`
	DeployAction string `toml:"deploy_action"`
	// ApprovalStage and ApprovalAction name the manual approval that gates
	// this env. ApprovalAction has no default. ApprovalStage defaults to
	// Stage (an approval inside the env's own stage); set it when the
	// approval sits in another stage, usually the one before. Setting
	// ApprovalStage without ApprovalAction is an error.
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

	thresholds   rules.Thresholds
	pollInterval time.Duration
	warnings     []string
}

// Default thresholds, used when [settings] leaves a key unset.
const (
	DefaultStaleReview = 48 * time.Hour
	DefaultDoneGrace   = 2 * time.Hour
	DefaultFadeAfter   = 24 * time.Hour
	DefaultPartialProd = 4 * time.Hour
)

// DefaultPollInterval is how often the resolver polls when
// settings.poll_interval is unset; MinPollInterval is the lowest allowed.
const (
	DefaultPollInterval = 60 * time.Second
	MinPollInterval     = 15 * time.Second
)

// DefaultPath is $XDG_CONFIG_HOME/app-green/config.toml, or
// ~/.config/app-green/config.toml when XDG_CONFIG_HOME is unset or relative
// (the XDG spec says to ignore a relative value). It returns an error when
// neither is available rather than guessing a relative path.
func DefaultPath() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if !filepath.IsAbs(base) {
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
	poll, err := durationOr(c.Settings.PollInterval, DefaultPollInterval)
	if err != nil {
		return fmt.Errorf("settings.poll_interval: %w", err)
	}
	if poll < MinPollInterval {
		return fmt.Errorf("settings.poll_interval must be at least %v, got %q", MinPollInterval, c.Settings.PollInterval)
	}
	c.pollInterval = poll
	if err := c.validateJira(); err != nil {
		return err
	}
	if err := c.validateGitHub(); err != nil {
		return err
	}
	if err := c.validateAccounts(); err != nil {
		return err
	}
	if err := c.validateEnvs(); err != nil {
		return err
	}
	if len(c.WatchedRepos()) == 0 {
		return errors.New("no repos to watch: set github.repos or envs[].repos")
	}
	return nil
}

// WatchedRepos are the repos whose PRs are fetched: github.repos and every
// env's repos, lowercased, deduplicated and sorted.
func (c *Config) WatchedRepos() []string {
	all := slices.Clone(c.GitHub.Repos)
	for _, e := range c.EnvList {
		all = append(all, e.Repos...)
	}
	slices.Sort(all)
	return slices.Compact(all)
}

func (c *Config) validateAccounts() error {
	seen := map[string]bool{}
	for i := range c.AWS.Accounts {
		a := &c.AWS.Accounts[i]
		a.Name = strings.TrimSpace(a.Name)
		a.Profile = strings.TrimSpace(a.Profile)
		a.Region = strings.TrimSpace(a.Region)
		if a.Name == "" {
			return fmt.Errorf("aws.accounts[%d].name is required", i)
		}
		field := fmt.Sprintf("aws.accounts[%d] (%s)", i, a.Name)
		if seen[a.Name] {
			return fmt.Errorf("%s.name: duplicate account %q", field, a.Name)
		}
		seen[a.Name] = true
		if a.Profile == "" {
			return fmt.Errorf("%s.profile is required", field)
		}
		if a.Region == "" {
			return fmt.Errorf("%s.region is required", field)
		}
	}
	return nil
}

// arnName reduces an ECS cluster or service ARN to its name (the part after
// the last "/", as aws.serviceName does) and returns anything else as is.
func arnName(s string) string {
	if !strings.HasPrefix(s, "arn:") {
		return s
	}
	return s[strings.LastIndex(s, "/")+1:]
}

// pipelineName is the character set CodePipeline allows in pipeline, stage
// and action names.
var pipelineName = regexp.MustCompile(`^[A-Za-z0-9.@_-]+$`)

func (c *Config) validateEnvs() error {
	if len(c.EnvList) == 0 {
		return errors.New("envs: no envs configured")
	}
	seen := map[string]bool{}
	for i := range c.EnvList {
		e := &c.EnvList[i]
		for _, f := range []*string{&e.Account, &e.Pipeline, &e.Stage, &e.DeployAction, &e.ApprovalStage, &e.ApprovalAction} {
			*f = strings.TrimSpace(*f)
		}
		// field labels errors with the env's ID, e.g.
		// "envs[1] (stage-acct/app-pipeline/Production)".
		field := fmt.Sprintf("envs[%d] (%s/%s/%s)", i, e.Account, e.Pipeline, e.Stage)
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
		for _, n := range []struct{ key, val string }{
			{"pipeline", e.Pipeline},
			{"stage", e.Stage},
			{"deploy_action", e.DeployAction},
			{"approval_stage", e.ApprovalStage},
			{"approval_action", e.ApprovalAction},
		} {
			if n.val != "" && !pipelineName.MatchString(n.val) {
				return fmt.Errorf("%s.%s: %q has characters outside A-Z a-z 0-9 . @ _ - (CodePipeline names allow only these)", field, n.key, n.val)
			}
		}
		if _, ok := c.Account(e.Account); !ok {
			return fmt.Errorf("%s.account: unknown account %q (not in aws.accounts)", field, e.Account)
		}
		id := c.env(i).ID()
		if seen[id] {
			return fmt.Errorf("%s: duplicate env", field)
		}
		seen[id] = true
		if e.ApprovalStage != "" && e.ApprovalAction == "" {
			return fmt.Errorf("%s.approval_action is required when approval_stage is set", field)
		}
		if spec := e.stageSpec(); spec.ApprovalAction != "" && spec.ApprovalStage == spec.Stage && spec.ApprovalAction == spec.DeployAction {
			return fmt.Errorf("%s.approval_action must differ from deploy_action when the approval is in the same stage", field)
		}
		services := map[string]bool{} // "cluster/service" names, ARNs reduced
		for j := range e.ECS {
			ecs := &e.ECS[j]
			f := fmt.Sprintf("%s.ecs[%d]", field, j)
			ecs.Cluster = strings.TrimSpace(ecs.Cluster)
			if ecs.Cluster == "" {
				return fmt.Errorf("%s.cluster is required", f)
			}
			if len(ecs.Services) == 0 {
				return fmt.Errorf("%s.services: no services listed", f)
			}
			for k := range ecs.Services {
				ecs.Services[k] = strings.TrimSpace(ecs.Services[k])
				if ecs.Services[k] == "" {
					return fmt.Errorf("%s.services: empty service name", f)
				}
				name := arnName(ecs.Services[k])
				key := arnName(ecs.Cluster) + "/" + name
				if services[key] {
					return fmt.Errorf("%s.services: duplicate service %q", f, name)
				}
				services[key] = true
			}
		}
		repos, err := normalizeRepos(field+".repos", e.Repos)
		if err != nil {
			return err
		}
		e.Repos = repos
		if len(repos) == 0 {
			c.warnings = append(c.warnings, fmt.Sprintf("%s lists no repos: app-green will guess them from deploy history, a last resort that a missing or truncated history defeats - add repos = [\"owner/name\"]", field))
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
// position in the file, Region and ReadOnly come from the account, and the
// approval fields are the stage spec's (approval_stage defaults to the env's
// own stage).
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
	spec := e.stageSpec()
	return model.Env{
		Account:        e.Account,
		Region:         a.Region,
		Pipeline:       e.Pipeline,
		Stage:          e.Stage,
		Order:          i,
		Prod:           e.Prod,
		ReadOnly:       a.ReadOnly,
		ApprovalStage:  spec.ApprovalStage,
		ApprovalAction: spec.ApprovalAction,
		Repos:          slices.Clone(e.Repos),
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
	repos, err := normalizeRepos("github.repos", g.Repos)
	if err != nil {
		return err
	}
	g.Repos = repos
	return nil
}

// repoName is a lowercased GitHub "owner/name".
var repoName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*/[a-z0-9._-]+$`)

// normalizeRepos lowercases each "owner/name" in repos (adapters compare
// repos lowercased) and checks its form. field names the key in errors.
func normalizeRepos(field string, repos []string) ([]string, error) {
	out := make([]string, len(repos))
	for i, r := range repos {
		out[i] = strings.ToLower(strings.TrimSpace(r))
		_, name, _ := strings.Cut(out[i], "/")
		if !repoName.MatchString(out[i]) || name == "." || name == ".." {
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
	if err == nil && (u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(j.Site, "#")) {
		// Not quoted back: a query may hold a token.
		return errors.New("jira.site must not have a query or fragment")
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
	seen := map[string]bool{}
	for _, p := range j.Projects {
		if !projectKey.MatchString(p) {
			return fmt.Errorf("jira.projects: %q is not a Jira project key (uppercase letters, digits and _, starting with a letter)", p)
		}
		if seen[p] {
			return fmt.Errorf("jira.projects: duplicate project %q", p)
		}
		seen[p] = true
	}
	return nil
}

// dayPart is a leading whole number of days, as in "2d" or "1d12h".
var dayPart = regexp.MustCompile(`^(\d+)d`)

// durationOr parses v, or returns def when v is empty. v is a Go duration
// ("48h", "90m"), optionally led by whole days ("2d", "1d12h"). Adapted from
// ParseAge in ../jira-green/internal/model/threshold.go; the caller checks
// the sign.
func durationOr(v string, def time.Duration) (time.Duration, error) {
	s := strings.TrimSpace(v)
	if s == "" {
		return def, nil
	}
	bad := fmt.Errorf("invalid duration %q (use a form like 2d, 48h or 90m)", v)
	var total time.Duration
	if m := dayPart.FindStringSubmatch(s); m != nil {
		n, err := strconv.Atoi(m[1])
		if err != nil || n > 100000 {
			return 0, bad
		}
		total = time.Duration(n) * 24 * time.Hour
		s = s[len(m[0]):]
		if strings.HasPrefix(s, "-") || strings.HasPrefix(s, "+") {
			return 0, bad // "1d-23h" would quietly mean 1h
		}
	}
	if s != "" {
		d, err := time.ParseDuration(s)
		if err != nil {
			return 0, bad
		}
		total += d
	}
	return total, nil
}

// PollInterval is how often the resolver polls. Default 60s, at least 15s.
func (c *Config) PollInterval() time.Duration { return c.pollInterval }

// Thresholds are the rules thresholds, defaults filled in.
func (c *Config) Thresholds() rules.Thresholds { return c.thresholds }

// PipelineKey names one pipeline in one account: the unit of one AWS
// History call.
type PipelineKey struct {
	Account  string
	Pipeline string
}

// PipelineSpecs is one pipeline and the stage specs of its envs.
type PipelineSpecs struct {
	Key   PipelineKey
	Specs []aws.StageSpec
}

// StageSpecs groups the envs' stage specs by pipeline, for
// aws.Client.History. Pipelines come in order of first appearance in the
// file and each group's specs in file order, so the result is stable.
func (c *Config) StageSpecs() []PipelineSpecs {
	var out []PipelineSpecs
	pos := map[PipelineKey]int{}
	for _, e := range c.EnvList {
		k := PipelineKey{Account: e.Account, Pipeline: e.Pipeline}
		i, ok := pos[k]
		if !ok {
			i = len(out)
			pos[k] = i
			out = append(out, PipelineSpecs{Key: k})
		}
		out[i].Specs = append(out[i].Specs, e.stageSpec())
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

// quoteStderr trims s and cuts it to at most maxStderr bytes on a rune
// boundary, marking a cut with "...".
func quoteStderr(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxStderr {
		return s
	}
	n := maxStderr
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "..."
}

// resolveToken reads the variable env, or runs command with sh -c and
// trims its output.
//
// A command runs at most tokenCommandTimeout (10s) plus a 1s WaitDelay, so
// with both tokens on commands startup can stall about 22s in the worst case.
// Errors never include stdout, where the token is, but a failed command's
// stderr is quoted (trimmed, at most maxStderr bytes) to explain the
// failure: a command that echoes the token to stderr and then fails would
// leak it into the error.
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
			return "", fmt.Errorf("%s.token_command failed: %w: %s", table, err, quoteStderr(stderr.String()))
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
