package jira

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/ericdahl-dev/app-green/internal/model"
)

// Keys are checked before they go into JQL, so a typo or a stray quote is
// reported instead of building a broken or widened query.
var (
	projectKey = regexp.MustCompile(`^[A-Z][A-Z0-9_]+$`)
	ticketKey  = regexp.MustCompile(`^[A-Z][A-Z0-9_]+-\d+$`)
)

// MyTickets and TicketsByKey return tickets plus warnings: one
// "KEY field: error" line per malformed field, which was left zero. The
// resolver logs them once per poll.

// MyTickets returns my tickets in projects that are not To Do: everything in
// progress, plus Done tickets that finished in the last 14 days (their
// changes may still be on the way to prod). The JQL uses Jira's fixed status
// category IDs (4 = In Progress, 3 = Done), so a site that renames or
// translates its categories cannot break it.
func (c *Client) MyTickets(ctx context.Context, projects []string) ([]model.Ticket, []string, error) {
	if len(projects) == 0 {
		return nil, nil, errors.New("jira: no projects configured")
	}
	for _, p := range projects {
		if !projectKey.MatchString(p) {
			return nil, nil, fmt.Errorf("jira: bad project key %q", p)
		}
	}
	jql := fmt.Sprintf(`assignee = currentUser() AND project in (%s) AND `+
		`(statusCategory = 4 OR (statusCategory = 3 AND statusCategoryChangedDate >= -14d)) `+
		`ORDER BY updated DESC`, strings.Join(projects, ","))
	return c.tickets(ctx, jql)
}

// TicketsByKey returns the named tickets, whoever they are assigned to. The
// resolver uses it for tickets named on my PRs that MyTickets missed. No keys
// means no request ("key in ()" is invalid JQL). A well-formed key that does
// not exist, even in an unknown project, is not an error: Jira returns 200
// and skips it (checked against Jira Cloud, 2026-10-06), so the missing
// ticket just has no row.
func (c *Client) TicketsByKey(ctx context.Context, keys []string) ([]model.Ticket, []string, error) {
	if len(keys) == 0 {
		return nil, nil, nil
	}
	for _, k := range keys {
		if !ticketKey.MatchString(k) {
			return nil, nil, fmt.Errorf("jira: bad ticket key %q", k)
		}
	}
	return c.tickets(ctx, fmt.Sprintf("key in (%s)", strings.Join(keys, ",")))
}

func (c *Client) tickets(ctx context.Context, jql string) ([]model.Ticket, []string, error) {
	issues, warnings, err := c.search(ctx, jql)
	if err != nil {
		return nil, nil, err
	}
	out := make([]model.Ticket, len(issues))
	for i, is := range issues {
		key := strings.ToUpper(is.Key) // the linker and rules match uppercase keys
		out[i] = model.Ticket{
			Key:            key,
			Title:          is.Summary,
			Status:         is.StatusName,
			StatusCategory: model.StatusCategory(is.StatusCategory),
			URL:            c.BrowseURL(key),
			Updated:        is.Updated.Time,
			StatusSince:    is.StatusCategoryChangedDate.Time,
		}
	}
	return out, warnings, nil
}
