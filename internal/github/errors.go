package github

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// APIError is a non-2xx response.
type APIError struct {
	Status  int
	Message string // GitHub's "message", or the trimmed body when there is none
	// RetryAfter is how long to back off: set on a 429, and on a 403 that
	// GitHub marks as a rate limit. Zero otherwise.
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return strings.TrimSpace(fmt.Sprintf("github: HTTP %d %s", e.Status, http.StatusText(e.Status)))
	}
	return fmt.Sprintf("github: HTTP %d: %s", e.Status, e.Message)
}

func newAPIError(resp *http.Response, body []byte, now time.Time) *APIError {
	var env struct {
		Message string `json:"message"`
	}
	msg := strings.TrimSpace(string(body))
	if json.Unmarshal(body, &env) == nil {
		msg = env.Message
	}
	if r := []rune(msg); len(r) > 200 {
		msg = string(r[:199]) + "…"
	}
	return &APIError{Status: resp.StatusCode, Message: msg, RetryAfter: retryAfter(resp, now)}
}

// defaultRetryAfter is the backoff for a 429 with no usable Retry-After.
const defaultRetryAfter = 60 * time.Second

// maxRetryAfter caps the backoff, so a bogus header cannot stop polling for
// hours.
const maxRetryAfter = 15 * time.Minute

// retryAfter reads GitHub's rate-limit signals: Retry-After (whole seconds,
// sent with 429s and secondary-limit 403s), then an exhausted primary limit
// (X-RateLimit-Remaining: 0, back off until X-RateLimit-Reset). A 429 with
// neither backs off defaultRetryAfter. Anything else is 0.
func retryAfter(resp *http.Response, now time.Time) time.Duration {
	var d time.Duration
	if n, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After"))); err == nil {
		d = time.Duration(min(n, int(maxRetryAfter/time.Second))) * time.Second
	} else if resp.Header.Get("X-RateLimit-Remaining") == "0" {
		if reset, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			d = time.Unix(reset, 0).Sub(now)
		}
	}
	if d <= 0 && resp.StatusCode == http.StatusTooManyRequests {
		d = defaultRetryAfter
	}
	return min(max(d, 0), maxRetryAfter)
}

// IsAuth reports whether err is a 401 Unauthorized: the token is bad, so
// polling should stop rather than retry. A 403 is not an auth error: it
// usually means one repo or action is off-limits (a read-only token asked to
// re-run a job) while the token still works.
func IsAuth(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == http.StatusUnauthorized
}
