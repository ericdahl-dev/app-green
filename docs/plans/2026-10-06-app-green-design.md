# app-green design

Date: 2026-10-06
Status: approved design, revised after review; not yet built

## Problem

Work moves through three systems: it is filed as a Jira ticket, built in a
GitHub PR, and deployed by an AWS pipeline to ECS. Each has its own green app
(jira-green, git-green, aws-green), so answering "where is ABC-2108, and does
it need me?" means reading three screens and joining them by hand.

app-green shows one row per ticket, the stage its work has reached, and
whether that row needs action.

## Decisions

| Question | Decision |
|---|---|
| Main view | One row per ticket (a Chain), drill-down for detail |
| v1 scope | The day-job chain: Jira (example) → acme PRs → AWS CodePipeline/ECS |
| Later targets | Coolify (personal apps), Azure (possible future employer). Hence pluggable adapters |
| Existing apps | Keep them unchanged. v1 copies the small pieces it needs; a shared library is extracted only once a second adapter shows what is really shared |
| Actions | Flag + quick actions (re-run checks, approve/reject pipeline step, change ticket status, open). Every write asks first. No automatic Jira updates |
| Rows | Tickets assigned to me from In Progress onward, plus tickets with my open PRs, until the change is in prod (fades after ~24h) |
| Environments | Both work accounts are tracked: stage-acct and prod-acct each have their own pipeline, each with Test and Production stages, so a ticket has four environment slots |
| Architecture | One binary with built-in adapters. A shared background service stays possible later, since the adapter interfaces are what it would expose |
| work time | Unknown. Do the linking spike first, while access exists |
| Test fixtures | Synthetic only: real response shapes, invented IDs and names. No work data leaves work |

## What the review found (2026-10-06)

Four reviewers (architecture, backend/API, UX, scope) checked the first draft
against the sibling repos and live read-only AWS calls. Confirmed by hand:

- Each work account runs one app pipeline with stages Source →
  ClusterCreation → Test (Deploy, SmokeTests, ManualApprovalOfTestEnvironment)
  → Production. Test and prod are stages, not separate pipelines.
- Each pipeline has three GitHub sources: AppCode (app), InfraCode
  (infra), ReportsCode (reports).
- git-green lists only open PRs (50, no pagination) and has no merge SHAs,
  branch names or compare, so it cannot supply ticket → PR data.

The sections below incorporate these findings.

## Architecture

```
app-green
├── adapters (interfaces; work implementations in v1)
│   ├── Tracker      → jira    (copied from jira-green internal/jira)
│   ├── CodeHost     → github  (new GraphQL query; ETag transport, rate limiter
│   │                           and rerun copied from git-green)
│   └── DeployTarget → aws     (CodePipeline action executions, ECS health)
├── resolver  polls adapters, runs compare calls, keeps the SHA-pair cache
├── link      pure: snapshots + compare results → []Chain
├── rules     pure: Chain → stage + flags
└── ui        ticket list + detail (Bubble Tea)
```

`resolver` does all I/O. `link` and `rules` are pure functions, so they are
table-tested without fakes. app-green has its own poller; the sibling pollers
pull in webhooks and app config and are not reused.

### Chain

```
Ticket{key, title, status, statusCategory}
  → []PR{repo, number, state, merged, mergeSHA, headRef, checks, review}
  → []EnvSlot{env, state, deployedSHA, finishedAt, health}
```

An environment is `(account, pipeline, stage, source action)`, kept as an
ordered list in config, not a test/prod enum. For work:

```
stage-acct / app / Test        stage-acct / app / Production
prod-acct     / app / Test        prod-acct     / app / Production
```

The same shape covers Coolify later (one application per environment,
deployed SHA from the deployment's commit).

### Linking

- **Ticket → PR:** the Jira key in the PR title or branch name. One GitHub
  GraphQL query per repo, `states: [OPEN, MERGED]`, returning `headRefName`,
  `mergeCommit`, `merged` and `statusCheckRollup`. A ticket can have several
  PRs; a PR can name several keys.
- **Repo → pipeline source:** read from `GetPipeline` at startup: each source
  action's repo name maps to (pipeline, source action). Not hand-written.
- **What an environment is running:** `ListActionExecutions` for the stage's
  deploy action (Test/Deploy, Production/deploy). Take the latest succeeded
  execution, ordered by finish time, not the latest pipeline execution:
  executions awaiting approval stay InProgress, others are SUPERSEDED, and
  rollbacks redeploy older revisions. The deployed SHA for a repo comes from
  that execution's `sourceRevisions` entry for the repo's source action
  (via `ListPipelineExecutions`).
- **PR → environment:** use `mergeSHA` only when `merged = true`. Compare
  `mergeSHA...deployedSHA` in the PR's own repo. `ahead` or `identical` means
  included. A 404 (for example a force-pushed or deleted commit) means
  "unlinked" and is never cached. Results are cached in memory by SHA pair.
- **In prod since:** the Production deploy action's finish time. No disk cache
  in v1.
- **Health:** ECS task definitions use CDK asset hashes, not git SHAs, so ECS
  is used only for the health flag (healthy tasks vs desired), never to decide
  what is deployed.

### Rows (Jira query)

My tickets in In Progress or later statuses, plus recently finished ones so a
row survives until its change reaches prod:
`statusCategory != Done OR (statusCategory = Done AND statusCategoryChangedDate >= -14d)`,
plus any ticket key found on my open PRs.

### Config

`~/.config/app-green/config.toml`, using the same credential sources as
today: Jira `token_env`, GitHub tokens or `gh auth token`, AWS SSO profiles.
Environments are listed in order; pipeline sources are discovered.

## Stages

| Stage | Meaning |
|---|---|
| Started | Ticket In Progress, no PR |
| PR open | At least one open PR |
| Merged | All PRs merged, not in any environment yet (or a PR is still in an unmerged stack) |
| In test | Merge commit running in a Test stage |
| Awaiting prod | Running in prod somewhere, or prod waiting for approval / in progress, but not yet in every prod environment |
| In prod | Running in **every** applicable Production environment; fades after ~24h with no flags |

With PRs in mixed states, the row shows the least advanced PR's stage. Closed
PRs are ignored. "Running" means the environment's newest successful deploy
contains the change (exact commit, else GitHub compare); an older deploy that
has since been rolled back does not count. A prod deploy with an unhealthy
service is still "in prod", with a red flag. The detail screen shows every
environment slot.

An environment applies to a chain when it deploys one of the chain's repos:
from the environment's `repos` list in config, or, when that is empty, from
its deploy history. A configured environment with no history for the repo
shows "deploy unknown", never a silent pass.

## Flags

Within each color, flags have a fixed order (listed top to bottom below). A
row shows its first flag; the detail screen lists all of them.

Red, needs me now:
1. A pipeline stage failed for a commit in this Chain (a failure is superseded
   by a newer run that contains the change)
2. Rolled back: the change was running, and the newest deploy no longer has it
3. ECS unhealthy after this Chain's deploy (fewer healthy tasks than desired, crash loop)
4. A check failed on my PR, including GitHub's ERROR and EXPECTED states (`f`
   re-run; code-scanning results are shown as an alert with `o` open, since a
   re-run cannot fix them)
5. Changes requested on my PR

Yellow, waiting:
1. Pipeline paused for manual approval (`a` / `x`)
2. Partial prod: running in some prod environments but not all for longer
   than `partial_prod_after` (default 4h); targets the first missing one
3. PR approved and green (or with no checks) but not merged
4. In review with no reviewer, or no review for `stale_review_after` (default 2d)
5. Jira status disagrees with reality: To Do or In Progress but merged, or
   Done but not in prod after a grace period (`t`). A Done ticket with no
   status-change time uses its last-updated time
6. Deploy unknown: a compare call failed or the environment has no history
   for the repo

When one environment has both a failure and an unknown, the failure shows.
A prod deploy with no timestamp never fades.

Unlinked PRs and deploys go in a pinned "Unlinked (n)" group at the bottom,
never silently dropped.

No flag: checks running, deploy in progress, review pending within the threshold.

Rules are plain functions of a Chain in the `rules` package. Only thresholds
are configurable.

## Screens

Main screen, sorted red, yellow, then stage (furthest first):

```
app-green                       jira ✓ 12s  github ✓ 8s  aws prod-acct ✓ stage-acct ✓
● ABC-2108  Item count on review page     PR #339   CodeQL failing            f
◐ ABC-1972  Form opt-outs         test ✓    prod awaiting approval    a/x
◐ ABC-2035  Remove legacy task path       PR #331   no review 4d
◐ ABC-2096  Uptime monitors /up    in prod   Jira still Code Review    t
  ABC-2042  S3 access logging      started   no PR yet
  ABC-2103  Health checks nginx    PR #338   checks running
  ABC-2091  Item count on add page    in prod ✓ 3h ago
── Unlinked (1) ──
  app PR #341  no ticket key
↑/↓ move  enter details  f re-run  a/x approve/reject  t status  o open  r refresh  q quit
```

Status markers are single-width glyphs, not emoji, so layout is stable on
every terminal (the exo-mini font issue showed emoji widths and colors vary).
`NO_COLOR` is honored. The header shows staleness per adapter and per AWS
account, including SSO expiry.

Detail screen:

```
ABC-1972  Drop the form-level opt-outs        Jira: Code Review
  PR #330  app  merged 2d ago  ✓ checks  ✓ approved
  stage-acct  Test        ✓ deployed 1d ago (a1b2c3d)  ECS 2/2
  stage-acct  Production  ⏸ awaiting approval since 1d   a/x
  prod-acct      Test        ✓ deployed 1d ago (a1b2c3d)  ECS 2/2
  prod-acct      Production  not yet
```

### Write safety

- Confirm with `y`; cancel with `n` or `esc`. `enter` never confirms a write,
  because in app-green it opens the detail screen.
- A row action runs only when it has exactly one target (one failing PR, one
  waiting approval). Otherwise the key opens the detail screen to choose.
- The approve dialog shows the ticket, the SHA, other tickets bundled in that
  SHA, the AWS account, and the Test stage's health.
- If an account's profile is read-only (prod-acct uses `prod-view`), approve is
  not offered there and the dialog says why.
- Jira status changes pick from the ticket's available `Transitions()`.
- `f` on a red pipeline row shows a hint and `o` opens the console; no
  automatic fix in v1.

### States

Defined screens for: first load, no rows, an adapter failing (rows keep their
last data, marked stale), and expired AWS SSO (header names the command:
`aws sso login --sso-session <session>`).

## Errors and limits

- Adapters fail independently. A failed poll keeps the last good snapshot,
  marks it stale in the header, and retries with backoff; 429 `Retry-After`
  is honored.
- Poll intervals: Jira 60s, GitHub 60s, AWS 30s. Compare results are cached
  by SHA pair; only open PRs and in-flight pipelines are re-polled.

## Testing

- `link` and `rules`: table-driven tests over synthetic fixtures built from
  real response shapes (failing CodeQL on an open PR, merged PR awaiting
  prod approval, squash merge, rollback, superseded execution, three-source
  pipeline). Invented IDs and names only.
- `resolver` and adapters: tested against fake interfaces, as jira-green's
  poller is.
- One manual end-to-end check against the real work board before release.

## Build order

0. **Spike. Done 2026-10-06: the linking holds.** See
   `2026-10-06-linking-spike.md`. It adds: exact SHA match before compare,
   stacked PRs linked through the PR that reached `main`, and approval waits
   as a common stage. Rollbacks had no real cases; synthetic fixtures cover
   them.
1. Model, `link` and `rules` with table tests.
2. Adapters and `resolver`, read-only (Jira copy, new GitHub query, AWS
   action executions, ECS health).
3. Main and detail screens, read-only. Useful from here.
4. Actions with the write-safety rules above.
5. Config, release build (Homebrew cask, Linux), install on the Mac and
   exo-mini (both work SSO profiles work there).

Later: extract a shared client library once Coolify is the second adapter;
coolify-green already has a working client.
