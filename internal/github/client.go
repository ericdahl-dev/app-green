// Package github is a small GitHub client covering only what app-green
// needs: recent PRs and their base-branch PRs over GraphQL, commit compare,
// and re-running failed Actions jobs. It uses net/http directly.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ericdahl-dev/app-green/internal/model"
)

// Client talks to the GitHub API with one token.
type Client struct {
	http  *http.Client
	token string
	api   string
	now   func() time.Time // reads X-RateLimit-Reset
}

// New returns a Client for token. Nothing is cached here: the resolver
// caches Compare results by SHA pair.
func New(token string) *Client {
	return &Client{
		http:  &http.Client{Timeout: 20 * time.Second},
		token: token,
		api:   "https://api.github.com",
		now:   time.Now,
	}
}

// SetAPI points the client at another API base URL (a test server, or a
// GitHub Enterprise "https://host/api/v3").
func (c *Client) SetAPI(base string) { c.api = strings.TrimRight(base, "/") }

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.api+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return newAPIError(resp, b, c.now())
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("github: decode %s: %w", path, err)
	}
	// Read to the end so the keep-alive connection can be reused.
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// API is the subset of Client the resolver and UI use; tests supply fakes.
type API interface {
	RecentPRs(ctx context.Context, owner, name, author string, since time.Time) ([]model.PR, []string, error)
	BasePRs(ctx context.Context, owner, name string, prs []model.PR) ([]model.PR, []string, error)
	Compare(ctx context.Context, repo, base, head string) (model.Inclusion, error)
	RerunFailedJobs(ctx context.Context, repo string, runID int64) error
}

var _ API = (*Client)(nil)
