# Linking spike (build step 0)

Date: 2026-10-06
Result: **the linking holds.** Build can proceed with the rules below.

Run by hand with `gh api` (GraphQL and REST) and the AWS CLI against the real
work repo and both accounts' pipelines, read-only. Identifiers are left out on
purpose (fixtures are synthetic only).

## What was checked

| Question | Result |
|---|---|
| Ticket → PR by key | Every recent merged PR has the key in both title and branch (`ABC-2099: ...`, `abc-2099-...`) |
| Merge style | Squash, merge and rebase are all allowed. Each merged PR has a distinct `mergeCommit.oid` |
| Does the pipeline record the deployed commit? | Yes. `ListPipelineExecutions.sourceRevisions[actionName=AppCode].revisionId` is the PR's merge commit, exactly |
| One run per merge? | Yes, in both accounts. Each merge to `main` starts a V2 execution in each account at the same moment |
| Stage history | `ListActionExecutions` gives Test/Deploy, Test/ManualApprovalOfTestEnvironment and Production/deploy per execution, with status and `lastUpdateTime` |
| Compare, included | `compare/<mergeSHA>...<deployedSHA>` → `ahead` when the PR is in the deploy |
| Compare, not yet | Reversed pair → `behind` |
| Superseded execution | Its merge commit compares `ahead` against the next succeeded execution's commit, so it still resolves as deployed |
| Other sources | Infra-only changes start runs that redeploy the same AppCode SHA. Harmless: the SHA is already linked |
| Rollbacks | None in the last 100 executions of either account. Covered by synthetic fixtures only |
| Failed / cancelled runs | Present (mostly in stage-acct). Need a red flag only when the failed run carries a commit from a live Chain |

## Rules this adds to the design

1. **Exact match first, compare second.** Look for the PR's merge SHA in the
   recent action history of each stage. Only when it is not there (superseded
   or batched) call compare against that stage's latest succeeded SHA. Most
   rows then need no compare call at all.
2. **Count only PRs merged into the deploying branch.** Stacked PRs merge into
   each other's branches and can share one merge SHA. A PR whose base is not
   `main` is linked through the PR that finally merged the stack into `main`
   (follow `baseRefName` down the stack), or shown under its ticket as
   "merged into stack, waiting for `main`".
3. **Approval waits are visible in the timing.** Test/Deploy finishing and the
   ManualApproval action still InProgress is the "awaiting approval" state.
   The gap between Test/Deploy and Production/deploy was 30 to 60 minutes
   today, so "Awaiting prod" is a real, common stage worth its own row state.
4. **Both accounts move together.** stage-acct and prod-acct track the same `main`
   and ran the same merges today. The detail screen shows both; the main row
   shows the furthest stage reached, as designed.

## Not answered by the spike

- Rollback behavior (no real cases). Synthetic fixture: a Production execution
  whose revision is older than the previous one; rows already past that SHA
  must drop back a stage.
- Repos other than app (the other repos). Same pipeline pattern
  is expected from the aws-green config but not checked here.
