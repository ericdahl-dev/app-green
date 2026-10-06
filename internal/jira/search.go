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
// nextPageToken. A malformed field is left zero and reported in warnings as
// "KEY field: error"; it never fails the search.
func (c *Client) search(ctx context.Context, jql string) (issues []issue, warnings []string, err error) {
	token := ""
	seen := map[string]bool{}
	for range maxPages {
		body := map[string]any{"jql": jql, "fields": searchFields, "maxResults": 100}
		if token != "" {
			body["nextPageToken"] = token
		}
		var r searchResp
		if err := c.do(ctx, http.MethodPost, "/rest/api/3/search/jql", body, &r); err != nil {
			return nil, nil, err
		}
		for _, ai := range r.Issues {
			is, w := convert(ai)
			issues = append(issues, is)
			warnings = append(warnings, w...)
		}
		if r.IsLast || r.NextPageToken == "" {
			return issues, warnings, nil
		}
		if seen[r.NextPageToken] {
			return nil, nil, fmt.Errorf("jira: search: nextPageToken %q repeated", r.NextPageToken)
		}
		seen[r.NextPageToken] = true
		token = r.NextPageToken
	}
	return nil, nil, fmt.Errorf("jira: search: more than %d pages", maxPages)
}

// fieldDecoder decodes one issue's fields one at a time, so one malformed
// field does not blank the others. It collects "KEY field: err" for each
// failure.
type fieldDecoder struct {
	key    string
	fields map[string]json.RawMessage
	errs   []string
}

// decodeField decodes fields[name] into a fresh T. Missing and null fields
// give the zero value with no error; a decode failure gives the zero value
// (never a partial one) and records the error.
func decodeField[T any](d *fieldDecoder, name string) T {
	var v T
	raw, ok := d.fields[name]
	if !ok || string(raw) == "null" {
		return v
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		d.errs = append(d.errs, fmt.Sprintf("%s %s: %v", d.key, name, err))
		var zero T
		return zero
	}
	return v
}

func convert(ai apiIssue) (issue, []string) {
	d := &fieldDecoder{key: ai.Key, fields: ai.Fields}
	st := decodeField[apiStatus](d, "status")
	is := issue{
		Key:                       ai.Key,
		Summary:                   decodeField[string](d, "summary"),
		StatusName:                st.Name,
		StatusCategory:            st.StatusCategory.Key,
		Updated:                   decodeField[Time](d, "updated"),
		StatusCategoryChangedDate: decodeField[Time](d, "statuscategorychangedate"),
	}
	return is, d.errs
}
