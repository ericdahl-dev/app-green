package github_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/ericdahl-dev/app-green/internal/link"
	"github.com/ericdahl-dev/app-green/internal/model"
)

func TestBasePRsFetchesAnotherAuthorsBasePR(t *testing.T) {
	calls := 0
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		req := decodeGQL(t, r)
		if req.Variables["owner"] != "acme" || req.Variables["name"] != "app" || req.Variables["b0"] != "s1" || len(req.Variables) != 3 {
			t.Errorf("variables = %v, want owner, name and b0=s1", req.Variables)
		}
		if !strings.Contains(req.Query, "b0:pullRequests(headRefName:$b0") {
			t.Errorf("query has no b0 alias:\n%s", req.Query)
		}
		http.ServeFile(w, r, "testdata/base_prs.json")
	})
	mine := []model.PR{
		{Repo: "acme/app", DefaultBranch: "main", Number: 2, HeadRef: "abc-2", BaseRef: "s1", State: model.PRMerged},
		{Repo: "acme/app", DefaultBranch: "main", Number: 4, HeadRef: "abc-4", BaseRef: "main", State: model.PROpen},
	}
	got, warns, err := c.BasePRs(context.Background(), "acme", "app", mine)
	if err != nil || len(warns) != 0 {
		t.Fatalf("err %v, warnings %v", err, warns)
	}
	want := model.PR{
		Repo: "acme/app", DefaultBranch: "main", Number: 1, Title: "Stack base", HeadRef: "s1", BaseRef: "main",
		URL: "https://github.com/Acme/App/pull/1", State: model.PRMerged, MergeSHA: "aaaa111",
		MergedAt: ts("2026-09-22T09:00:00Z"), OpenedAt: ts("2026-09-20T09:00:00Z"),
		Checks: model.ChecksPassing, Review: model.ReviewApproved, Reviewers: 1,
	}
	if len(got) != 1 || !equalPR(got[0], want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
}

// baseNode is a PR node for a base-branch response.
func baseNode(number int, head, base, login string, fork bool) map[string]any {
	return map[string]any{"number": number, "headRefName": head, "baseRefName": base, "state": "OPEN",
		"isCrossRepository": fork, "author": map[string]any{"login": login},
		"reviewRequests": map[string]any{"totalCount": 0}, "reviews": map[string]any{"totalCount": 0}, "commits": map[string]any{"nodes": []any{}}}
}

func TestBasePRsWalksDownTheStack(t *testing.T) {
	// mine: #3 on s2. s2's PR (#2, someone else) is on s1, s1's (#1) on main.
	// "gone" has no PR at all, and a fork's PR on s2 is skipped.
	var asked [][]string
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		req := decodeGQL(t, r)
		var branches []string
		repo := map[string]any{"nameWithOwner": "acme/app", "defaultBranchRef": map[string]any{"name": "main"}}
		for i := 0; ; i++ {
			b, ok := req.Variables[fmt.Sprintf("b%d", i)].(string)
			if !ok {
				break
			}
			branches = append(branches, b)
			var nodes []any
			switch b {
			case "s2":
				nodes = []any{baseNode(9, "s2", "main", "forker", true), baseNode(2, "s2", "s1", "other", false)}
			case "s1":
				nodes = []any{baseNode(1, "s1", "main", "other", false)}
			}
			repo[fmt.Sprintf("b%d", i)] = map[string]any{"nodes": nodes}
		}
		asked = append(asked, branches)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"repository": repo}})
	})
	mine := []model.PR{
		{Repo: "acme/app", DefaultBranch: "main", Number: 3, HeadRef: "abc-3", BaseRef: "s2", State: model.PROpen},
		{Repo: "acme/app", DefaultBranch: "main", Number: 5, HeadRef: "abc-5", BaseRef: "gone", State: model.PRMerged},
		{Repo: "acme/app", DefaultBranch: "main", Number: 6, HeadRef: "abc-6", BaseRef: "s2", State: model.PROpen},
		{Repo: "acme/app", DefaultBranch: "main", Number: 7, HeadRef: "abc-7", BaseRef: "dead", State: model.PRClosed},
	}
	got, _, err := c.BasePRs(context.Background(), "acme", "app", mine)
	if err != nil {
		t.Fatal(err)
	}
	var nums []int
	for _, p := range got {
		nums = append(nums, p.Number)
	}
	if fmt.Sprint(nums) != "[2 1]" {
		t.Errorf("numbers = %v, want [2 1]", nums)
	}
	if fmt.Sprint(asked) != "[[s2 gone] [s1]]" {
		t.Errorf("asked = %v, want [[s2 gone] [s1]]", asked)
	}
}

func TestBasePRsAsksNothingWhenNoBaseIsMissing(t *testing.T) {
	c := serve(t, func(w http.ResponseWriter, r *http.Request) { t.Errorf("unexpected request") })
	mine := []model.PR{
		{Repo: "acme/app", DefaultBranch: "main", Number: 2, HeadRef: "s1", BaseRef: "main", State: model.PRMerged},
		{Repo: "acme/app", DefaultBranch: "main", Number: 3, HeadRef: "abc-3", BaseRef: "s1", State: model.PROpen},
	}
	got, _, err := c.BasePRs(context.Background(), "acme", "app", mine)
	if err != nil || got != nil {
		t.Errorf("got %v, %v; want nothing", got, err)
	}
}

func TestBasePRsKeepALaterStackPRFromLookingStranded(t *testing.T) {
	c := serve(t, fixture(t, "base_prs.json"))
	mine := []model.PR{{Repo: "acme/app", DefaultBranch: "main", Number: 2, Title: "ABC-2 on s1", HeadRef: "abc-2", BaseRef: "s1",
		State: model.PRMerged, MergeSHA: "bbbb222", MergedAt: ts("2026-09-21T09:00:00Z")}}
	bases, _, err := c.BasePRs(context.Background(), "acme", "app", mine)
	if err != nil {
		t.Fatal(err)
	}
	chains, _ := link.Link([]model.Ticket{{Key: "ABC-2"}}, mine, bases, []string{"ABC"})
	p := chains[0].PRs[0]
	if p.Stranded || p.EffectiveSHA != "aaaa111" {
		t.Errorf("#2 = %+v, want carried to main by #1's merge aaaa111", p)
	}
}
