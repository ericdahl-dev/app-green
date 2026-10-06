# app-green design

Date: 2026-10-06
Status: approved design, not yet built

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
| Existing apps | Keep them. Move their clients from `internal/` to `pkg/` and import those |
| Actions | Flag + quick actions (re-run checks, approve/reject pipeline step, change ticket status, open). Every write asks first. No automatic Jira updates |
| Rows | Tickets assigned to me from In Progress onward, plus tickets with my open PRs, until the change is in prod (fades after ~24h) |
| Architecture | One binary with built-in adapters (option A). A shared background service (option C) stays possible later, since the adapter interfaces are what it would expose |

## Architecture

```
app-green
├── adapters (interfaces; work implementations in v1)
│   ├── Tracker      → jira    (jira-green pkg/jira)
│   ├── CodeHost     → github  (git-green pkg/github)
│   └── DeployTarget → aws     (aws-green pkg/{aws,cfn,ecs})
├── linker   joins the three sources into one Chain per ticket
├── rules    decides each Chain's stage and flag
├── poller   polls each adapter on its own interval, emits snapshots
└── ui       ticket list + detail (Bubble Tea)
```

### Chain

```
Ticket{key, title, status}
  → []PR{repo, number, state, checks, review, mergeSHA}
  → []Deploy{env: test|prod, pipeline, run, deployedSHA, ecsHealth}
```

### Linking

- **Ticket → PR:** the Jira key appears in the PR title or branch name. A
  ticket can have several PRs; a PR can name several keys.
- **PR → deploy:** a merged PR is in an environment when its merge commit is
  an ancestor of that environment's last successful deployed revision
  (GitHub compare API). Results are cached by SHA pair, since they never
  change.
- **Repo → pipeline:** a map in config (`app` → prod-acct and stage-acct
  pipelines), seeded from the aws-green config.

New data the existing clients do not fetch yet: PR merge commit SHAs
(git-green) and pipeline execution source revisions (aws-green).

### Shared code

Each existing app moves its API client from `internal/x` to `pkg/x`, a pure
move with no behavior change, released as a tag. app-green imports those
tags. Adapters convert each client's types into the shared model.

### Config

`~/.config/app-green/config.toml`, using the same credential sources as
today: Jira `token_env`, GitHub tokens or `gh auth token`, AWS SSO profiles.

## Stages

| Stage | Meaning |
|---|---|
| Started | Ticket In Progress, no PR |
| PR open | At least one open PR |
| Merged | All PRs merged, not yet in test |
| In test | Merge commit deployed to test |
| Awaiting prod | In test, prod pipeline waiting (approval or not yet run) |
| In prod | Merge commit live in prod, ECS healthy; row fades after ~24h |

## Flags

🔴 needs me now:
- A check failed on my PR (`f` re-run)
- A pipeline run failed or rolled back for a commit in this Chain
- ECS unhealthy after this Chain's deploy (fewer healthy tasks than desired, crash loop)
- Changes requested on my PR

🟡 waiting:
- Pipeline paused for manual approval (`a` / `x`)
- PR approved and green but not merged
- In review with no reviewer, or no review for `stale_review_after` (default 2d)
- Jira status disagrees with reality (In Progress but merged; Done but not in prod) (`t`)
- Unlinked: PR or deploy that could not be matched (never silently dropped)

No flag: checks running, deploy in progress, review pending within the threshold.

A row shows its worst flag; the detail screen lists all of them. Rules are
plain functions of a Chain in the `rules` package. Only thresholds are
configurable.

## Screens

Main screen, sorted red, yellow, then stage (furthest first):

```
app-green                                    jira ✓ github ✓ aws ✓  · 12s ago
🔴 ABC-2108  Item count on review page     PR #339   CodeQL failing            f
🟡 ABC-1972  Form opt-outs         test ✓    prod awaiting approval    a/x
🟡 ABC-2035  Remove legacy task path       PR #331   no review 4d
🟡 ABC-2096  Uptime monitors /up    in prod   Jira still Code Review    t
   ABC-2042  S3 access logging      started   no PR yet
   ABC-2103  Health checks nginx    PR #338   checks running
   ABC-2091  Item count on add page    in prod ✓ 3h ago
↑/↓ move  enter details  f re-run  a/x approve/reject  t status  o open  r refresh  q quit
```

Detail screen:

```
ABC-1972  Drop the form-level opt-outs        Jira: Code Review
  PR #330  app  merged 2d ago  ✓ checks  ✓ approved
  test     app-test pipeline  ✓ deployed 1d ago (a1b2c3d)  ECS 2/2
  prod     app pipeline       ⏸ awaiting approval since 1d   a/x
```

Keys match the existing apps. Every write action confirms first.

Out of v1: kanban, mute/manage, notifications, webhooks (these stay in the
standalone apps). v2 candidates: desktop notification on red, Coolify target.

## Errors and limits

- Adapters fail independently. A failed poll keeps the last good snapshot,
  marks it stale in the header, and retries with backoff; 429 `Retry-After`
  is honored.
- An expired AWS SSO session gets a specific message naming the login command.
- Poll intervals: Jira 60s, GitHub 60s, AWS 30s. Merged PRs and deployed SHAs
  are cached permanently; only open PRs and in-flight pipelines are re-polled.

## Testing

- `linker` and `rules`: table-driven tests over fixtures modeled on real
  cases (failing CodeQL on an open PR, merged PR awaiting prod approval).
- Adapters: tested against fake interfaces, as jira-green's poller is.
- One manual end-to-end check against the real work board before release.

## Build order

1. Move clients in jira-green, git-green, aws-green to `pkg/`; tag releases.
2. app-green repo: adapter interfaces, Chain model, linker, with tests.
3. Rules and the main screen, read-only (useful from here).
4. Detail screen and actions (re-run, approve/reject, status change).
5. Setup wizard, config, Homebrew cask and Linux build; install on Mac and exo-mini.
