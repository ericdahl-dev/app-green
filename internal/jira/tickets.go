package jira

import (
	"context"
	"fmt"
	"strings"

	"github.com/ericdahl-dev/app-green/internal/model"
)

// MyTickets returns my tickets in projects that are not To Do: everything in
// progress, plus Done tickets that finished in the last 14 days (their
// changes may still be on the way to prod). The JQL uses Jira's fixed status
// category IDs (4 = In Progress, 3 = Done), so a site that renames or
// translates its categories cannot break it.
func (c *Client) MyTickets(ctx context.Context, projects []string) ([]model.Ticket, error) {
	jql := fmt.Sprintf(`assignee = currentUser() AND project in (%s) AND `+
		`(statusCategory = 4 OR (statusCategory = 3 AND statusCategoryChangedDate >= -14d)) `+
		`ORDER BY updated DESC`, strings.Join(projects, ","))
	return c.tickets(ctx, jql)
}

// TicketsByKey returns the named tickets, whoever they are assigned to. The
// resolver uses it for tickets named on my PRs that MyTickets missed. No keys
// means no request ("key in ()" is invalid JQL).
func (c *Client) TicketsByKey(ctx context.Context, keys []string) ([]model.Ticket, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	return c.tickets(ctx, fmt.Sprintf("key in (%s)", strings.Join(keys, ",")))
}

func (c *Client) tickets(ctx context.Context, jql string) ([]model.Ticket, error) {
	issues, err := c.search(ctx, jql)
	if err != nil {
		return nil, err
	}
	out := make([]model.Ticket, len(issues))
	for i, is := range issues {
		out[i] = model.Ticket{
			Key:            is.Key,
			Title:          is.Summary,
			Status:         is.StatusName,
			StatusCategory: model.StatusCategory(is.StatusCategory),
			URL:            c.BrowseURL(is.Key),
			Updated:        is.Updated.Time,
			StatusSince:    is.StatusCategoryChangedDate.Time,
		}
	}
	return out, nil
}
