package github_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/ericdahl-dev/app-green/internal/github"
)

func serve(t *testing.T, h http.HandlerFunc) *github.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := github.New("tok")
	c.SetAPI(srv.URL)
	return c
}

func TestErrorsAreTyped(t *testing.T) {
	reset := time.Now().Add(90 * time.Second).Unix()
	cases := []struct {
		name     string
		status   int
		header   map[string]string
		auth     bool
		retryMin time.Duration
		retryMax time.Duration
		wantMsg  string
	}{
		{"401 is an auth error", 401, nil, true, 0, 0, "Bad credentials"},
		{"403 is not an auth error", 403, nil, false, 0, 0, "Bad credentials"},
		{"429 with Retry-After backs off that long", 429, map[string]string{"Retry-After": "30"}, false, 30 * time.Second, 30 * time.Second, "Bad credentials"},
		{"429 without Retry-After backs off a minute", 429, nil, false, time.Minute, time.Minute, "Bad credentials"},
		{"403 rate limit backs off until the reset", 403, map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": strconv.FormatInt(reset, 10)}, false, 80 * time.Second, 91 * time.Second, "Bad credentials"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := serve(t, func(w http.ResponseWriter, r *http.Request) {
				for k, v := range tc.header {
					w.Header().Set(k, v)
				}
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprint(w, `{"message":"Bad credentials"}`)
			})
			_, err := c.Compare(context.Background(), "acme/app", "aaaa", "bbbb")
			var ae *github.APIError
			if !errors.As(err, &ae) {
				t.Fatalf("err = %v, want *APIError", err)
			}
			if ae.Status != tc.status || ae.Message != tc.wantMsg || github.IsAuth(err) != tc.auth {
				t.Errorf("err = %+v, IsAuth %v; want status %d auth %v", ae, github.IsAuth(err), tc.status, tc.auth)
			}
			if ae.RetryAfter < tc.retryMin || ae.RetryAfter > tc.retryMax {
				t.Errorf("RetryAfter = %v, want %v..%v", ae.RetryAfter, tc.retryMin, tc.retryMax)
			}
		})
	}
}
