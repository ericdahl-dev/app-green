package github_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
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
				if r.Method != http.MethodGet || r.URL.Path != "/repos/acme/app/compare/aaaa...bbbb" || r.URL.Query().Get("per_page") != "1" {
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
			got, err := c.Compare(context.Background(), "acme/app", "aaaa", "bbbb")
			if err != nil || got != tc.want {
				t.Errorf("Compare = %v, %v; want %v, nil", got, err, tc.want)
			}
		})
	}
}

func TestCompareReusesCachedBodyOn304(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits == 2 {
			if r.Header.Get("If-None-Match") != `"v1"` {
				t.Errorf("If-None-Match = %q", r.Header.Get("If-None-Match"))
			}
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		_, _ = fmt.Fprint(w, `{"status":"ahead"}`)
	}))
	defer srv.Close()
	c := github.New("tok")
	c.SetAPI(srv.URL)
	for i := range 2 {
		got, err := c.Compare(context.Background(), "acme/app", "aaaa", "bbbb")
		if err != nil || got != model.Included {
			t.Errorf("call %d: Compare = %v, %v; want Included", i+1, got, err)
		}
	}
	if hits != 2 {
		t.Errorf("hits = %d, want 2", hits)
	}
}
