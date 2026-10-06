package github

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/ericdahl-dev/app-green/internal/model"
)

// basePRsQuery builds one query with an aliased pullRequests lookup per
// branch, b0..bN, each bound to its own variable.
func basePRsQuery(n int) string {
	var vars, aliases strings.Builder
	for i := range n {
		fmt.Fprintf(&vars, ",$b%d:String!", i)
		fmt.Fprintf(&aliases, "\n    b%d:pullRequests(headRefName:$b%d,states:[OPEN,MERGED],first:5,orderBy:{field:CREATED_AT,direction:DESC}){nodes{...pr}}", i, i)
	}
	return "query($owner:String!,$name:String!" + vars.String() + "){\n  rateLimit{resetAt}\n  repository(owner:$owner,name:$name){\n    nameWithOwner\n    defaultBranchRef{name}" +
		aliases.String() + "\n  }\n}" + prFields
}

// missingBases lists, once each and in order, the base branches of prs'
// open and merged PRs in repo that are not the default branch and that no
// such PR in prs has as its head.
func missingBases(repo string, prs []model.PR) []string {
	live := func(p model.PR) bool { return p.State != model.PRClosed && strings.EqualFold(p.Repo, repo) }
	heads := map[string]bool{}
	for _, p := range prs {
		if live(p) {
			heads[p.HeadRef] = true
		}
	}
	var out []string
	for _, p := range prs {
		if !live(p) || p.BaseRef == "" || p.BaseRef == p.DefaultBranch || heads[p.BaseRef] {
			continue
		}
		heads[p.BaseRef] = true // once each
		out = append(out, p.BaseRef)
	}
	return out
}

// maxBaseRounds caps how deep BasePRs follows a stack of other people's
// branches.
const maxBaseRounds = 5

// BasePRs returns the PR for each base branch of prs in owner/name that is
// not the default branch and has no PR in prs, whoever wrote it. link needs
// them: without a base branch's PR a stacked PR looks Stranded. It repeats
// for the bases of the PRs it found, so a stack whose lower branches belong
// to someone else resolves down to the default branch (at most
// maxBaseRounds queries, one branch asked once). Each branch gets the newest
// open or merged PR whose head is that branch in this repo (a fork's PR with
// the same branch name is skipped); a branch with none gets nothing.
// Warnings are GraphQL errors that came with usable data.
func (c *Client) BasePRs(ctx context.Context, owner, name string, prs []model.PR) ([]model.PR, []string, error) {
	repo := strings.ToLower(owner + "/" + name)
	asked := map[string]bool{}
	all := slices.Clone(prs)
	var out []model.PR
	var warns []string
	for range maxBaseRounds {
		var branches []string
		for _, b := range missingBases(repo, all) {
			if !asked[b] {
				asked[b] = true
				branches = append(branches, b)
			}
		}
		if len(branches) == 0 {
			break
		}
		found, w, err := c.basePRs(ctx, owner, name, branches)
		warns = append(warns, w...)
		if err != nil {
			return nil, warns, err
		}
		out = append(out, found...)
		all = append(all, found...)
	}
	return out, warns, nil
}

// basePRs runs one aliased query for branches.
func (c *Client) basePRs(ctx context.Context, owner, name string, branches []string) ([]model.PR, []string, error) {
	vars := map[string]any{"owner": owner, "name": name}
	for i, b := range branches {
		vars[fmt.Sprintf("b%d", i)] = b
	}
	var data struct {
		Repository map[string]json.RawMessage `json:"repository"`
	}
	warns, err := c.graphql(ctx, basePRsQuery(len(branches)), vars, &data)
	if err != nil {
		return nil, warns, err
	}
	if data.Repository == nil {
		return nil, warns, fmt.Errorf("github: repository %s/%s not found", owner, name)
	}
	var info repoInfo
	_ = json.Unmarshal(data.Repository["nameWithOwner"], &info.NameWithOwner)
	_ = json.Unmarshal(data.Repository["defaultBranchRef"], &info.DefaultBranchRef)
	var out []model.PR
	for i := range branches {
		var conn struct {
			Nodes []prNode `json:"nodes"`
		}
		raw, ok := data.Repository[fmt.Sprintf("b%d", i)]
		if !ok {
			continue
		}
		if err := json.Unmarshal(raw, &conn); err != nil {
			warns = append(warns, fmt.Sprintf("github: base branch %s: %v", branches[i], err))
			continue
		}
		for _, n := range conn.Nodes {
			if !n.IsCrossRepository {
				out = append(out, n.toPR(info))
				break
			}
		}
	}
	return out, warns, nil
}
