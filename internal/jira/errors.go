// Copied from jira-green internal/jira/errors.go (2026-10-06).

package jira

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// APIError is a non-2xx response.
type APIError struct {
	Status int
	// Messages is Jira's error envelope flattened: errorMessages first, then
	// message, then "field: msg" for each entry of errors, sorted by field.
	Messages []string
	// Body is the raw (size-limited) response body, kept for debugging.
	Body       string
	RetryAfter time.Duration
}

// maxErrorLen caps Error() so a verbose Jira response fits a status line.
const maxErrorLen = 200

// Error is "jira: HTTP 400: msg1; field: msg2", cut to about 200
// characters, or "jira: HTTP 502 Bad Gateway" when the body had no Jira
// error envelope (an HTML proxy page, say).
func (e *APIError) Error() string {
	if len(e.Messages) == 0 {
		return strings.TrimSpace(fmt.Sprintf("jira: HTTP %d %s", e.Status, http.StatusText(e.Status)))
	}
	s := fmt.Sprintf("jira: HTTP %d: %s", e.Status, strings.Join(e.Messages, "; "))
	if r := []rune(s); len(r) > maxErrorLen {
		s = string(r[:maxErrorLen-1]) + "…"
	}
	return s
}

// errorMessages parses Jira's {"errorMessages":[...],"errors":{...}}
// envelope, and the {"message":"..."} shape some endpoints (the Agile API,
// the gateway) use. It returns nil when body is neither.
func errorMessages(body []byte) []string {
	var env struct {
		ErrorMessages []string          `json:"errorMessages"`
		Errors        map[string]string `json:"errors"`
		Message       string            `json:"message"`
	}
	if json.Unmarshal(body, &env) != nil {
		return nil
	}
	out := slices.Clone(env.ErrorMessages)
	if env.Message != "" {
		out = append(out, env.Message)
	}
	for _, k := range slices.Sorted(maps.Keys(env.Errors)) {
		out = append(out, k+": "+env.Errors[k])
	}
	return out
}

// IsAuth reports whether err is a 401 Unauthorized: the credentials are bad,
// so polling should stop rather than retry. A 403 Forbidden is deliberately
// not an auth error: it usually means one resource (a board, an issue) is
// off-limits while the token still works, so polling keeps going.
func IsAuth(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == http.StatusUnauthorized
}

// defaultRetryAfter is the backoff for a 429 with no usable Retry-After.
const defaultRetryAfter = 60 * time.Second

// maxRetryAfter caps a server's Retry-After, so a bogus or hostile value
// cannot stop the dashboard for hours.
const maxRetryAfter = 15 * time.Minute

// parseRetryAfter reads a Retry-After header: whole seconds or an HTTP-date,
// measured from now.
// It returns 0 for a missing, unparseable, or negative value, and at most
// maxRetryAfter.
func parseRetryAfter(h string, now time.Time) time.Duration {
	h = strings.TrimSpace(h)
	var d time.Duration
	if n, err := strconv.Atoi(h); err == nil {
		d = time.Duration(min(n, int(maxRetryAfter/time.Second))) * time.Second // no overflow
	} else if t, err := http.ParseTime(h); err == nil {
		d = t.Sub(now)
	}
	return min(max(d, 0), maxRetryAfter)
}
