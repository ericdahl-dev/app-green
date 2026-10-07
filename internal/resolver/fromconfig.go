package resolver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ericdahl-dev/app-green/internal/aws"
	"github.com/ericdahl-dev/app-green/internal/config"
	"github.com/ericdahl-dev/app-green/internal/github"
	"github.com/ericdahl-dev/app-green/internal/jira"
)

// FromConfig builds the real adapters from cfg and returns a Resolver over
// them. It resolves both tokens once, concurrently (a token_command can take
// seconds), and reports every token error at once. Building the AWS clients
// calls nothing, so an expired SSO session shows up on the first poll as
// that account's status. A nil now is time.Now; a nil log discards.
func FromConfig(ctx context.Context, cfg *config.Config, now func() time.Time, log *slog.Logger) (*Resolver, error) {
	var (
		wg                 sync.WaitGroup
		jiraTok, githubTok string
		jiraErr, githubErr error
	)
	wg.Add(2)
	go func() { defer wg.Done(); jiraTok, jiraErr = cfg.JiraToken() }()
	go func() { defer wg.Done(); githubTok, githubErr = cfg.GitHubToken() }()
	wg.Wait()
	if err := errors.Join(jiraErr, githubErr); err != nil {
		return nil, err
	}

	deployers := map[string]Deployer{}
	for _, a := range cfg.AWS.Accounts {
		c, err := aws.New(ctx, a.Name, a.Profile, a.Region)
		if err != nil {
			return nil, fmt.Errorf("aws account %s: %w", a.Name, err)
		}
		deployers[a.Name] = c
	}
	return New(cfg, Adapters{
		Tracker: jira.New(cfg.Jira.Site, cfg.Jira.Email, jiraTok),
		Code:    github.New(githubTok),
		AWS:     deployers,
	}, now, log), nil
}
