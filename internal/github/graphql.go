package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// graphql POSTs query to /graphql and decodes its data into out. GitHub
// answers most query problems with HTTP 200 and an errors list: with no data
// that is an error, and next to partial data each message is a warning. A
// RATE_LIMITED error is a 429 *APIError, so callers back off as for REST:
// until the data's rateLimit.resetAt when the query asked for it, else
// defaultRetryAfter.
func (c *Client) graphql(ctx context.Context, query string, vars map[string]any, out any) ([]string, error) {
	var resp struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := c.do(ctx, http.MethodPost, "/graphql", map[string]any{"query": query, "variables": vars}, &resp); err != nil {
		return nil, err
	}
	var msgs []string
	for _, e := range resp.Errors {
		if e.Type == "RATE_LIMITED" {
			return nil, c.graphqlRateLimit(resp.Data, e.Message)
		}
		msgs = append(msgs, "github: graphql: "+e.Message)
	}
	if len(resp.Data) == 0 || string(resp.Data) == "null" {
		if len(msgs) == 0 {
			return nil, errors.New("github: graphql: empty response")
		}
		return nil, errors.New(strings.Join(msgs, "; "))
	}
	if err := json.Unmarshal(resp.Data, out); err != nil {
		return msgs, fmt.Errorf("github: graphql: decode: %w", err)
	}
	return msgs, nil
}

// graphqlRateLimit builds the 429 for a RATE_LIMITED GraphQL error.
func (c *Client) graphqlRateLimit(data json.RawMessage, msg string) *APIError {
	retry := defaultRetryAfter
	var rl struct {
		RateLimit *struct {
			ResetAt time.Time `json:"resetAt"`
		} `json:"rateLimit"`
	}
	if json.Unmarshal(data, &rl) == nil && rl.RateLimit != nil && !rl.RateLimit.ResetAt.IsZero() {
		retry = min(max(rl.RateLimit.ResetAt.Sub(c.now()), 0), maxRetryAfter)
	}
	return &APIError{Status: http.StatusTooManyRequests, Message: msg, RetryAfter: retry}
}
