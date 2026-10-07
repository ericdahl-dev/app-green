// Copied from jira-green internal/jira/transitions.go (2026-10-06).

package jira

import (
	"context"
	"net/http"
	"net/url"
)

// Transition is a workflow move available from an issue's current status.
type Transition struct {
	ID     string
	Name   string
	ToName string
}

// Transitions lists the transitions Jira allows from the issue's current status.
func (c *Client) Transitions(ctx context.Context, key string) ([]Transition, error) {
	var r struct {
		Transitions []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			To   struct {
				Name string `json:"name"`
			} `json:"to"`
		} `json:"transitions"`
	}
	if err := c.do(ctx, http.MethodGet, "/rest/api/3/issue/"+url.PathEscape(key)+"/transitions", nil, &r); err != nil {
		return nil, err
	}
	out := make([]Transition, 0, len(r.Transitions))
	for _, t := range r.Transitions {
		out = append(out, Transition{ID: t.ID, Name: t.Name, ToName: t.To.Name})
	}
	return out, nil
}

// DoTransition moves the issue through the given transition.
func (c *Client) DoTransition(ctx context.Context, key, transitionID string) error {
	body := map[string]any{"transition": map[string]string{"id": transitionID}}
	return c.do(ctx, http.MethodPost, "/rest/api/3/issue/"+url.PathEscape(key)+"/transitions", body, nil)
}
