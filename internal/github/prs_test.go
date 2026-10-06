package github_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ericdahl-dev/app-green/internal/github"
	"github.com/ericdahl-dev/app-green/internal/model"
)

var since = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

type gqlRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

func decodeGQL(t *testing.T, r *http.Request) gqlRequest {
	t.Helper()
	if r.Method != http.MethodPost || r.URL.Path != "/graphql" {
		t.Errorf("got %s %s, want POST /graphql", r.Method, r.URL.Path)
	}
	var req gqlRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		t.Errorf("decode request: %v", err)
	}
	return req
}

func fixture(t *testing.T, name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		decodeGQL(t, r)
		http.ServeFile(w, r, "testdata/"+name)
	}
}

func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestRecentPRsMapsEveryField(t *testing.T) {
	c := serve(t, fixture(t, "prs.json"))
	prs, warns, err := c.RecentPRs(context.Background(), "acme", "app", "me-dev", since)
	if err != nil || len(warns) != 0 {
		t.Fatalf("err %v, warnings %v", err, warns)
	}
	if len(prs) < 2 {
		t.Fatalf("prs = %+v", prs)
	}
	want3 := model.PR{
		Repo: "acme/app", DefaultBranch: "main", Number: 3, Title: "ABC-1 add widget",
		HeadRef: "abc-1-widget", BaseRef: "main", URL: "https://github.com/Acme/App/pull/3",
		State: model.PROpen, IsDraft: true, OpenedAt: ts("2026-10-01T10:00:00Z"),
		Checks: model.ChecksFailing, Review: model.ReviewRequired, Reviewers: 3,
		Failing: []model.Check{
			{Name: "CodeQL", CodeScanning: true, URL: "https://example.test/scan"},
			{Name: "rspec", RunID: 77, URL: "https://example.test/run/77"},
			{Name: "CodeQL / Analyze (go)", RunID: 78, URL: "https://example.test/run/78"},
			{Name: "Code scanning results / Semgrep", CodeScanning: true, URL: "https://example.test/semgrep"},
			{Name: "deploy-preview", RunID: 79, URL: "https://example.test/run/79"},
			{Name: "ci/legacy", URL: "https://example.test/legacy"},
			{Name: "ci/other", URL: "https://example.test/other"},
		},
	}
	if !reflect.DeepEqual(prs[0], want3) {
		t.Errorf("#3 =\n%+v\nwant\n%+v", prs[0], want3)
	}
	want2 := model.PR{
		Repo: "acme/app", DefaultBranch: "main", Number: 2, Title: "ABC-2 fix thing",
		HeadRef: "abc-2-fix", BaseRef: "main", URL: "https://github.com/Acme/App/pull/2",
		State: model.PRMerged, MergeSHA: "bbbb222", MergedAt: ts("2026-09-29T15:30:00Z"),
		OpenedAt: ts("2026-09-28T09:00:00Z"), Checks: model.ChecksPassing,
		Review: model.ReviewApproved, Reviewers: 1,
	}
	if !reflect.DeepEqual(prs[1], want2) {
		t.Errorf("#2 =\n%+v\nwant\n%+v", prs[1], want2)
	}
}

func TestRecentPRsKeepsOnlyAuthorsPRs(t *testing.T) {
	c := serve(t, fixture(t, "prs.json"))
	prs, _, err := c.RecentPRs(context.Background(), "acme", "app", "ME-DEV", since)
	if err != nil {
		t.Fatal(err)
	}
	var nums []int
	for _, p := range prs {
		nums = append(nums, p.Number)
	}
	// #1 is someone else's and #7's author was deleted; logins match ignoring case.
	if !reflect.DeepEqual(nums, []int{3, 2, 5, 6}) {
		t.Errorf("numbers = %v, want [3 2 5 6]", nums)
	}
}

func TestRecentPRsPassesRollupAndReviewStatesThrough(t *testing.T) {
	c := serve(t, fixture(t, "prs.json"))
	prs, _, err := c.RecentPRs(context.Background(), "acme", "app", "me-dev", since)
	if err != nil || len(prs) != 4 {
		t.Fatalf("prs %+v, err %v", prs, err)
	}
	if p := prs[2]; p.Checks != "EXPECTED" || p.Review != model.ReviewChangesRequested || p.Failing != nil {
		t.Errorf("#5 = %+v, want Checks EXPECTED, changes requested", p)
	}
	if p := prs[3]; p.Checks != model.ChecksNone || p.Review != model.ReviewNone {
		t.Errorf("#6 = %+v, want no checks, no review", p)
	}
}

// page builds one recent-PRs response page of PRs by me-dev.
func page(next bool, cursor string, prs ...map[string]any) map[string]any {
	nodes := []map[string]any{}
	for _, p := range prs {
		n := map[string]any{"headRefName": "h", "baseRefName": "main", "state": "OPEN", "author": map[string]any{"login": "me-dev"},
			"reviewRequests": map[string]any{"totalCount": 1}, "reviews": map[string]any{"totalCount": 0}, "commits": map[string]any{"nodes": []any{}}}
		for k, v := range p {
			n[k] = v
		}
		nodes = append(nodes, n)
	}
	return map[string]any{"data": map[string]any{"repository": map[string]any{
		"nameWithOwner": "acme/app", "defaultBranchRef": map[string]any{"name": "main"},
		"pullRequests": map[string]any{"pageInfo": map[string]any{"hasNextPage": next, "endCursor": cursor}, "nodes": nodes},
	}}}
}

func TestRecentPRsPagesUntilUpdatesPredateSince(t *testing.T) {
	var afters []any
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		req := decodeGQL(t, r)
		if req.Variables["owner"] != "acme" || req.Variables["name"] != "app" {
			t.Errorf("variables = %v", req.Variables)
		}
		after := req.Variables["after"]
		afters = append(afters, after)
		var body map[string]any
		switch after {
		case nil:
			// created long before since but updated recently: keep paging
			body = page(true, "p1", map[string]any{"number": 10, "createdAt": "2025-01-01T00:00:00Z", "updatedAt": "2026-09-20T00:00:00Z"})
		case "p1":
			// #9 was created recently; #8 was last updated before since: stop, though GitHub has more
			body = page(true, "p2", map[string]any{"number": 9, "createdAt": "2026-09-10T00:00:00Z", "updatedAt": "2026-09-12T00:00:00Z"},
				map[string]any{"number": 8, "createdAt": "2026-07-01T00:00:00Z", "updatedAt": "2026-08-01T00:00:00Z"})
		default:
			t.Errorf("fetched page after %v", after)
			body = page(false, "")
		}
		_ = json.NewEncoder(w).Encode(body)
	})
	prs, _, err := c.RecentPRs(context.Background(), "acme", "app", "me-dev", since)
	if err != nil {
		t.Fatal(err)
	}
	if len(prs) != 3 || prs[1].Number != 9 || prs[2].Number != 8 {
		t.Errorf("prs = %+v, want #10, #9, #8", prs)
	}
	if !reflect.DeepEqual(afters, []any{nil, "p1"}) {
		t.Errorf("after = %v, want [nil p1]", afters)
	}
}

func TestRecentPRsStopsWhenGitHubHasNoMore(t *testing.T) {
	calls := 0
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		decodeGQL(t, r)
		calls++
		_ = json.NewEncoder(w).Encode(page(false, "p1", map[string]any{"number": 10, "createdAt": "2026-09-20T00:00:00Z", "updatedAt": "2026-09-20T00:00:00Z"}))
	})
	if _, _, err := c.RecentPRs(context.Background(), "acme", "app", "me-dev", since); err != nil || calls != 1 {
		t.Errorf("err %v, calls %d, want 1", err, calls)
	}
}

func TestRecentPRsGraphQLErrors(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr bool
		warns   int
	}{
		{"errors and no data fail", `{"data":null,"errors":[{"message":"Something went wrong"}]}`, true, 0},
		{"an unknown repository fails", `{"data":{"repository":null},"errors":[{"type":"NOT_FOUND","message":"Could not resolve to a Repository"}]}`, true, 1},
		{"errors beside data are warnings", `{"data":{"repository":{"nameWithOwner":"acme/app","defaultBranchRef":{"name":"main"},"pullRequests":{"pageInfo":{"hasNextPage":false},"nodes":[]}}},"errors":[{"message":"partial"}]}`, false, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := serve(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(tc.body)) })
			_, warns, err := c.RecentPRs(context.Background(), "acme", "app", "me-dev", since)
			if (err != nil) != tc.wantErr || len(warns) != tc.warns {
				t.Errorf("err %v, warnings %v; want error %v, %d warnings", err, warns, tc.wantErr, tc.warns)
			}
		})
	}
}

func equalPR(a, b model.PR) bool { return reflect.DeepEqual(a, b) }

// failingOf serves one PR by me-dev whose rollup holds contexts, and returns
// its Failing checks.
func failingOf(t *testing.T, contexts ...map[string]any) []model.Check {
	t.Helper()
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		decodeGQL(t, r)
		rollup := map[string]any{"state": "FAILURE", "contexts": map[string]any{"nodes": contexts}}
		_ = json.NewEncoder(w).Encode(page(false, "", map[string]any{"number": 1, "createdAt": "2026-09-20T00:00:00Z",
			"commits": map[string]any{"nodes": []any{map[string]any{"commit": map[string]any{"statusCheckRollup": rollup}}}}}))
	})
	prs, _, err := c.RecentPRs(context.Background(), "acme", "app", "me-dev", since)
	if err != nil || len(prs) != 1 {
		t.Fatalf("prs %+v, err %v", prs, err)
	}
	return prs[0].Failing
}

func checkRun(name, conclusion, slug string, runID int64) map[string]any {
	suite := map[string]any{"app": map[string]any{"slug": slug}, "workflowRun": nil}
	if runID != 0 {
		suite["workflowRun"] = map[string]any{"databaseId": runID}
	}
	return map[string]any{"__typename": "CheckRun", "name": name, "conclusion": conclusion, "checkSuite": suite}
}

func TestAdvancedSecurityCheckIsCodeScanning(t *testing.T) {
	got := failingOf(t, checkRun("CodeQL", "FAILURE", "github-advanced-security", 0), checkRun("CodeQL-Build (go)", "FAILURE", "github-actions", 42))
	want := []model.Check{{Name: "CodeQL", CodeScanning: true}, {Name: "CodeQL-Build (go)", RunID: 42}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("failing = %+v, want %+v", got, want)
	}
}

func TestStartupFailureFailsAndStaleDoesNot(t *testing.T) {
	got := failingOf(t, checkRun("build", "STARTUP_FAILURE", "github-actions", 43), checkRun("old", "STALE", "github-actions", 44))
	want := []model.Check{{Name: "build", RunID: 43}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("failing = %+v, want %+v", got, want)
	}
}

func TestGraphQLRateLimitIsA429(t *testing.T) {
	reset := time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339)
	cases := []struct {
		name     string
		body     string
		min, max time.Duration
	}{
		{"no data backs off a minute", `{"data":null,"errors":[{"type":"RATE_LIMITED","message":"API rate limit exceeded"}]}`, time.Minute, time.Minute},
		{"rateLimit.resetAt sets the backoff", `{"data":{"rateLimit":{"resetAt":"` + reset + `"},"repository":null},"errors":[{"type":"RATE_LIMITED","message":"API rate limit exceeded"}]}`,
			4 * time.Minute, 5 * time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var queries []string
			c := serve(t, func(w http.ResponseWriter, r *http.Request) {
				queries = append(queries, decodeGQL(t, r).Query)
				_, _ = w.Write([]byte(tc.body))
			})
			_, _, errRecent := c.RecentPRs(context.Background(), "acme", "app", "me-dev", since)
			_, _, errBase := c.BasePRs(context.Background(), "acme", "app", []model.PR{{Repo: "acme/app", DefaultBranch: "main", HeadRef: "h", BaseRef: "s1", State: model.PROpen}})
			for _, err := range []error{errRecent, errBase} {
				var ae *github.APIError
				if !errors.As(err, &ae) || ae.Status != http.StatusTooManyRequests || github.IsAuth(err) {
					t.Fatalf("err = %v, want a 429 *APIError", err)
				}
				if ae.RetryAfter < tc.min || ae.RetryAfter > tc.max {
					t.Errorf("RetryAfter = %v, want %v..%v", ae.RetryAfter, tc.min, tc.max)
				}
			}
			for _, q := range queries {
				if !strings.Contains(q, "rateLimit{resetAt}") {
					t.Errorf("query does not ask for rateLimit{resetAt}:\n%s", q)
				}
			}
		})
	}
}
