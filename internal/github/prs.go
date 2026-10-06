package github

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ericdahl-dev/app-green/internal/model"
)

// prFields is every PR field the adapter maps, shared by the recent-PR and
// base-branch queries so the two cannot drift.
const prFields = `
fragment pr on PullRequest {
  number title url headRefName baseRefName state isDraft createdAt mergedAt
  isCrossRepository
  author{login}
  mergeCommit{oid}
  reviewDecision
  reviewRequests{totalCount}
  reviews{totalCount}
  commits(last:1){nodes{commit{statusCheckRollup{state
    contexts(first:50){nodes{
      __typename
      ... on CheckRun{name conclusion detailsUrl checkSuite{app{slug} workflowRun{databaseId}}}
      ... on StatusContext{context state targetUrl}
    }}}}}}
}`

const recentPRsQuery = `query($owner:String!,$name:String!,$after:String){
  repository(owner:$owner,name:$name){
    nameWithOwner
    defaultBranchRef{name}
    pullRequests(first:50,after:$after,states:[OPEN,MERGED],orderBy:{field:UPDATED_AT,direction:DESC}){
      pageInfo{hasNextPage endCursor}
      nodes{...pr}
    }
  }
}` + prFields

type repoInfo struct {
	NameWithOwner    string `json:"nameWithOwner"`
	DefaultBranchRef *struct {
		Name string `json:"name"`
	} `json:"defaultBranchRef"`
}

func (r repoInfo) defaultBranch() string {
	if r.DefaultBranchRef == nil {
		return ""
	}
	return r.DefaultBranchRef.Name
}

type prNode struct {
	Number            int        `json:"number"`
	Title             string     `json:"title"`
	URL               string     `json:"url"`
	HeadRefName       string     `json:"headRefName"`
	BaseRefName       string     `json:"baseRefName"`
	State             string     `json:"state"`
	IsDraft           bool       `json:"isDraft"`
	CreatedAt         time.Time  `json:"createdAt"`
	MergedAt          *time.Time `json:"mergedAt"`
	IsCrossRepository bool       `json:"isCrossRepository"`
	Author            *struct {
		Login string `json:"login"`
	} `json:"author"`
	MergeCommit *struct {
		OID string `json:"oid"`
	} `json:"mergeCommit"`
	ReviewDecision *string    `json:"reviewDecision"`
	ReviewRequests totalCount `json:"reviewRequests"`
	Reviews        totalCount `json:"reviews"`
	Commits        struct {
		Nodes []struct {
			Commit struct {
				StatusCheckRollup *struct {
					State    string `json:"state"`
					Contexts struct {
						Nodes []checkNode `json:"nodes"`
					} `json:"contexts"`
				} `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
}

type totalCount struct {
	TotalCount int `json:"totalCount"`
}

// checkNode is a CheckRun or a StatusContext, told apart by Typename.
type checkNode struct {
	Typename   string  `json:"__typename"`
	Name       string  `json:"name"`
	Conclusion *string `json:"conclusion"`
	DetailsURL *string `json:"detailsUrl"`
	CheckSuite *struct {
		App *struct {
			Slug string `json:"slug"`
		} `json:"app"`
		WorkflowRun *struct {
			DatabaseID int64 `json:"databaseId"`
		} `json:"workflowRun"`
	} `json:"checkSuite"`
	Context   string  `json:"context"`
	State     string  `json:"state"`
	TargetURL *string `json:"targetUrl"`
}

func (n prNode) login() string {
	if n.Author == nil {
		return ""
	}
	return n.Author.Login
}

// toPR maps a node into model.PR for the repo it came from.
func (n prNode) toPR(repo repoInfo) model.PR {
	p := model.PR{
		Repo:          strings.ToLower(repo.NameWithOwner),
		DefaultBranch: repo.defaultBranch(),
		Number:        n.Number,
		Title:         n.Title,
		HeadRef:       n.HeadRefName,
		BaseRef:       n.BaseRefName,
		URL:           n.URL,
		State:         model.PRState(n.State),
		IsDraft:       n.IsDraft,
		OpenedAt:      n.CreatedAt,
		Reviewers:     n.ReviewRequests.TotalCount + n.Reviews.TotalCount,
	}
	if n.ReviewDecision != nil {
		p.Review = model.ReviewState(*n.ReviewDecision)
	}
	if p.State == model.PRMerged {
		if n.MergeCommit != nil {
			p.MergeSHA = n.MergeCommit.OID
		}
		if n.MergedAt != nil {
			p.MergedAt = *n.MergedAt
		}
	}
	if len(n.Commits.Nodes) > 0 {
		if r := n.Commits.Nodes[len(n.Commits.Nodes)-1].Commit.StatusCheckRollup; r != nil {
			// GitHub's rollup state passes straight through: rules treats
			// anything but SUCCESS, PENDING or none (ERROR, EXPECTED, FAILURE)
			// as failing.
			p.Checks = model.ChecksState(r.State)
			for _, c := range r.Contexts.Nodes {
				if chk, ok := c.failing(); ok {
					p.Failing = append(p.Failing, chk)
				}
			}
		}
	}
	return p
}

// failing returns the check as a model.Check when it has failed: a CheckRun
// that concluded FAILURE, TIMED_OUT, CANCELLED or ACTION_REQUIRED, or a
// StatusContext in FAILURE or ERROR.
func (c checkNode) failing() (model.Check, bool) {
	switch c.Typename {
	case "CheckRun":
		if c.Conclusion == nil {
			return model.Check{}, false
		}
		switch *c.Conclusion {
		case "FAILURE", "TIMED_OUT", "CANCELLED", "ACTION_REQUIRED":
		default:
			return model.Check{}, false
		}
		chk := model.Check{Name: c.Name, URL: deref(c.DetailsURL)}
		slug := ""
		if s := c.CheckSuite; s != nil {
			if s.WorkflowRun != nil {
				chk.RunID = s.WorkflowRun.DatabaseID
			}
			if s.App != nil {
				slug = s.App.Slug
			}
		}
		// A code scanning alert is not fixed by a re-run. The CodeQL Actions
		// job ("CodeQL / Analyze") is an ordinary run and can be re-run.
		chk.CodeScanning = slug == "github-code-scanning" || strings.HasPrefix(c.Name, "Code scanning")
		return chk, true
	case "StatusContext":
		if c.State != "FAILURE" && c.State != "ERROR" {
			return model.Check{}, false
		}
		return model.Check{Name: c.Context, URL: deref(c.TargetURL)}, true
	}
	return model.Check{}, false
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

type prPage struct {
	Repository *struct {
		repoInfo
		PullRequests struct {
			PageInfo struct {
				HasNextPage bool   `json:"hasNextPage"`
				EndCursor   string `json:"endCursor"`
			} `json:"pageInfo"`
			Nodes []prNode `json:"nodes"`
		} `json:"pullRequests"`
	} `json:"repository"`
}

// maxPages caps one RecentPRs call at 1,000 PRs, so a busy repo with a
// distant since cannot page forever.
const maxPages = 20

// RecentPRs returns author's open and merged PRs in owner/name, most
// recently updated first, with logins compared ignoring case (an empty author
// matches nothing). It pages while GitHub has more and the last PR on the
// page was created after since; since only stops paging and filters nothing.
// Warnings are GraphQL errors that came with usable data.
func (c *Client) RecentPRs(ctx context.Context, owner, name, author string, since time.Time) ([]model.PR, []string, error) {
	var out []model.PR
	var warns []string
	var after any // nil asks for the first page
	for range maxPages {
		var page prPage
		w, err := c.graphql(ctx, recentPRsQuery, map[string]any{"owner": owner, "name": name, "after": after}, &page)
		warns = append(warns, w...)
		if err != nil {
			return nil, warns, err
		}
		if page.Repository == nil {
			return nil, warns, fmt.Errorf("github: repository %s/%s not found", owner, name)
		}
		prs := page.Repository.PullRequests
		for _, n := range prs.Nodes {
			if author != "" && strings.EqualFold(n.login(), author) {
				out = append(out, n.toPR(page.Repository.repoInfo))
			}
		}
		if !prs.PageInfo.HasNextPage || len(prs.Nodes) == 0 || !prs.Nodes[len(prs.Nodes)-1].CreatedAt.After(since) {
			break
		}
		after = prs.PageInfo.EndCursor
	}
	return out, warns, nil
}
