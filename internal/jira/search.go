// Copied from jira-green internal/jira/search.go (2026-10-06).

package jira

import (
	"context"
	"fmt"
	"net/http"
)

// maxPages caps every paginated call. Hitting it is an error, never a
// silently truncated result.
const maxPages = 50

var searchFields = []string{"summary", "status", "updated", "statuscategorychangedate"}

// issue holds only the fields app-green reads from a search hit.
type issue struct {
	Key                       string
	Summary                   string
	StatusName                string
	StatusCategory            string // statusCategory.key: "new", "indeterminate", "done"
	Updated                   Time
	StatusCategoryChangedDate Time
}

type apiIssue struct {
	Key    string `json:"key"`
	Fields struct {
		Summary string `json:"summary"`
		Status  struct {
			Name           string `json:"name"`
			StatusCategory struct {
				Key string `json:"key"`
			} `json:"statusCategory"`
		} `json:"status"`
		Updated                  Time `json:"updated"`
		StatusCategoryChangeDate Time `json:"statuscategorychangedate"`
	} `json:"fields"`
}

type searchResp struct {
	Issues        []apiIssue `json:"issues"`
	NextPageToken string     `json:"nextPageToken"`
	IsLast        bool       `json:"isLast"`
}

// search runs JQL and returns every matching issue, following
// nextPageToken.
func (c *Client) search(ctx context.Context, jql string) ([]issue, error) {
	var out []issue
	token := ""
	seen := map[string]bool{}
	for range maxPages {
		body := map[string]any{"jql": jql, "fields": searchFields, "maxResults": 100}
		if token != "" {
			body["nextPageToken"] = token
		}
		var r searchResp
		if err := c.do(ctx, http.MethodPost, "/rest/api/3/search/jql", body, &r); err != nil {
			return nil, err
		}
		for _, ai := range r.Issues {
			out = append(out, convert(ai))
		}
		if r.IsLast || r.NextPageToken == "" {
			return out, nil
		}
		if seen[r.NextPageToken] {
			return nil, fmt.Errorf("jira: search: nextPageToken %q repeated", r.NextPageToken)
		}
		seen[r.NextPageToken] = true
		token = r.NextPageToken
	}
	return nil, fmt.Errorf("jira: search: more than %d pages", maxPages)
}

func convert(ai apiIssue) issue {
	f := ai.Fields
	return issue{
		Key:                       ai.Key,
		Summary:                   f.Summary,
		StatusName:                f.Status.Name,
		StatusCategory:            f.Status.StatusCategory.Key,
		Updated:                   f.Updated,
		StatusCategoryChangedDate: f.StatusCategoryChangeDate,
	}
}
