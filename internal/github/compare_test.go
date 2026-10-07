package github_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ericdahl-dev/app-green/internal/github"
	"github.com/ericdahl-dev/app-green/internal/model"
)

func TestCompare(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   model.Inclusion
	}{
		{200, `{"status":"ahead"}`, model.Included},
		{200, `{"status":"identical"}`, model.Included},
		{200, `{"status":"behind"}`, model.NotIncluded},
		{200, `{"status":"diverged"}`, model.NotIncluded},
		{404, `{"message":"Not Found"}`, model.InclusionUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.body, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/repos/acme/app/compare/aaaa111...bbbb222" || r.URL.Query().Get("per_page") != "1" {
					t.Errorf("got %s %s", r.Method, r.URL)
				}
				if r.Header.Get("Authorization") != "Bearer tok" {
					t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
				}
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			c := github.New("tok")
			c.SetAPI(srv.URL + "/")
			got, err := c.Compare(context.Background(), "acme/app", "aaaa111", "bbbb222")
			if err != nil || got != tc.want {
				t.Errorf("Compare = %v, %v; want %v, nil", got, err, tc.want)
			}
		})
	}
}

func TestCompareSendsNoConditionalRequest(t *testing.T) {
	// The resolver caches compare results by SHA pair, so Compare never sends
	// If-None-Match and every call reaches the server.
	hits := 0
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		hits++
		if inm := r.Header.Get("If-None-Match"); inm != "" {
			t.Errorf("call %d sent If-None-Match %q", hits, inm)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		_, _ = fmt.Fprint(w, `{"status":"ahead"}`)
	})
	for i := range 2 {
		if got, err := c.Compare(context.Background(), "acme/app", "aaaa111", "bbbb222"); err != nil || got != model.Included {
			t.Errorf("call %d: Compare = %v, %v; want Included", i+1, got, err)
		}
	}
	if hits != 2 {
		t.Errorf("hits = %d, want 2", hits)
	}
}

func TestCompareRejectsNonSHAs(t *testing.T) {
	calls := 0
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = fmt.Fprint(w, `{"status":"ahead"}`)
	})
	for _, pair := range [][2]string{{"main", "bbbb222"}, {"aaaa111", "../../x"}, {"aaa", "bbbb222"}, {"aaaa111", strings.Repeat("b", 41)}, {"aaaa111", "bbbb22g"}} {
		if got, err := c.Compare(context.Background(), "acme/app", pair[0], pair[1]); err == nil || got != model.InclusionUnknown {
			t.Errorf("Compare(%q, %q) = %v, %v; want an error", pair[0], pair[1], got, err)
		}
	}
	if calls != 0 {
		t.Errorf("%d requests for invalid SHAs, want none", calls)
	}
	if got, err := c.Compare(context.Background(), "acme/app", "AAAA111", strings.Repeat("b", 40)); err != nil || got != model.Included {
		t.Errorf("upper-case and 40-character SHAs: %v, %v; want Included", got, err)
	}
}
