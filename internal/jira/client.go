// Copied from jira-green internal/jira/client.go (2026-10-06).

// Package jira is a thin Jira Cloud REST client covering only what
// app-green needs.
package jira

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

// Client talks to one Jira Cloud site with basic auth (email + API token).
type Client struct {
	site, email, token string
	http               *http.Client
	now                func() time.Time // reads a Retry-After HTTP-date
}

// New returns a Client for site, e.g. "https://example.atlassian.net".
func New(site, email, token string) *Client {
	return &Client{
		site:  strings.TrimRight(site, "/"),
		email: email,
		token: token,
		http:  &http.Client{Timeout: 20 * time.Second},
		now:   time.Now,
	}
}

// Site is the Jira base URL, without a trailing slash.
func (c *Client) Site() string { return c.site }

// BrowseURL is the web URL for an issue key.
func (c *Client) BrowseURL(key string) string { return c.site + "/browse/" + key }

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.site+path, rdr)
	if err != nil {
		return err
	}
	req.SetBasicAuth(c.email, c.token)
	req.Header.Set("Accept", "application/json")
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
		retry := parseRetryAfter(resp.Header.Get("Retry-After"), c.now())
		if retry == 0 && resp.StatusCode == http.StatusTooManyRequests {
			retry = defaultRetryAfter
		}
		return &APIError{
			Status:     resp.StatusCode,
			Messages:   errorMessages(b),
			Body:       strings.TrimSpace(string(b)),
			RetryAfter: retry,
		}
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("jira: decode %s: %w", path, err)
	}
	// Read to the end so the keep-alive connection can be reused.
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// User is a Jira account.
type User struct {
	AccountID   string `json:"accountId"`
	DisplayName string `json:"displayName"`
}

// Myself returns the authenticated user.
func (c *Client) Myself(ctx context.Context) (User, error) {
	var u User
	err := c.do(ctx, http.MethodGet, "/rest/api/3/myself", nil, &u)
	return u, err
}

// API is the subset of Client the resolver and UI use; tests supply fakes.
type API interface {
	Myself(ctx context.Context) (User, error)
	MyTickets(ctx context.Context, projects []string) ([]model.Ticket, []string, error)
	TicketsByKey(ctx context.Context, keys []string) ([]model.Ticket, []string, error)
	Transitions(ctx context.Context, key string) ([]Transition, error)
	DoTransition(ctx context.Context, key, transitionID string) error
}

var _ API = (*Client)(nil)
