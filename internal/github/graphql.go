package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// graphql POSTs query to /graphql and decodes its data into out. GitHub
// answers most query problems with HTTP 200 and an errors list: with no data
// that is an error, and next to partial data each message is a warning.
func (c *Client) graphql(ctx context.Context, query string, vars map[string]any, out any) ([]string, error) {
	var resp struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := c.do(ctx, http.MethodPost, "/graphql", map[string]any{"query": query, "variables": vars}, &resp); err != nil {
		return nil, err
	}
	var msgs []string
	for _, e := range resp.Errors {
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
