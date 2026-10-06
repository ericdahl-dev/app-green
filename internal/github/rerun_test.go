package github_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/ericdahl-dev/app-green/internal/github"
)

func TestRerunFailedJobs(t *testing.T) {
	var calls []string
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, `{}`)
	})
	if err := c.RerunFailedJobs(context.Background(), "acme/app", 77); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0] != "POST /repos/acme/app/actions/runs/77/rerun-failed-jobs" {
		t.Errorf("calls = %v", calls)
	}
}

func TestRerunFallsBackToWholeRunWhenNoJobFailed(t *testing.T) {
	var calls []string
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		if r.URL.Path == "/repos/acme/app/actions/runs/77/rerun-failed-jobs" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = fmt.Fprint(w, `{"message":"This workflow run has No Failed Jobs to re-run."}`)
			return
		}
		w.WriteHeader(http.StatusCreated)
	})
	if err := c.RerunFailedJobs(context.Background(), "acme/app", 77); err != nil {
		t.Fatal(err)
	}
	want := []string{"POST /repos/acme/app/actions/runs/77/rerun-failed-jobs", "POST /repos/acme/app/actions/runs/77/rerun"}
	if fmt.Sprint(calls) != fmt.Sprint(want) {
		t.Errorf("calls = %v, want %v", calls, want)
	}
}

func TestRerunReturnsOtherErrorsTyped(t *testing.T) {
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprint(w, `{"message":"Resource not accessible by integration"}`)
	})
	err := c.RerunFailedJobs(context.Background(), "acme/app", 77)
	var ae *github.APIError
	if !errors.As(err, &ae) || ae.Status != http.StatusForbidden || github.IsAuth(err) {
		t.Errorf("err = %v, want a non-auth 403 *APIError", err)
	}
}
