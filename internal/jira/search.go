// Copied from jira-green internal/jira/search.go (2026-10-06).

package jira

import (
	"context"
	"encoding/json"
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
	Key    string                     `json:"key"`
	Fields map[string]json.RawMessage `json:"fields"`
}

type apiStatus struct {
	Name           string `json:"name"`
	StatusCategory struct {
		Key string `json:"key"`
	} `json:"statusCategory"`
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

// decodeField decodes fields[name] into a fresh T, so one malformed field
// does not blank the others. Missing, null and malformed fields all give the
// zero value (never a partial one).
func decodeField[T any](fields map[string]json.RawMessage, name string) T {
	var v T
	raw, ok := fields[name]
	if !ok || string(raw) == "null" {
		return v
	}
	if json.Unmarshal(raw, &v) != nil {
		var zero T
		return zero
	}
	return v
}

func convert(ai apiIssue) issue {
	st := decodeField[apiStatus](ai.Fields, "status")
	return issue{
		Key:                       ai.Key,
		Summary:                   decodeField[string](ai.Fields, "summary"),
		StatusName:                st.Name,
		StatusCategory:            st.StatusCategory.Key,
		Updated:                   decodeField[Time](ai.Fields, "updated"),
		StatusCategoryChangedDate: decodeField[Time](ai.Fields, "statuscategorychangedate"),
	}
}
