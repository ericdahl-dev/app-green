// Package model holds the types every other package shares. It has no logic
// beyond small helpers and imports nothing from the project.
package model

import (
	"fmt"
	"time"
)

// Ticket is one tracker issue (a Jira ticket in v1).
type Ticket struct {
	Key            string // "ABC-1"
	Title          string
	Status         string // display name, e.g. "Code Review"
	StatusCategory string // "To Do" | "In Progress" | "Done"
	URL            string
	Updated        time.Time
	StatusSince    time.Time // when it entered Status; zero if unknown
}

type PRState string

const (
	PROpen   PRState = "OPEN"
	PRMerged PRState = "MERGED"
	PRClosed PRState = "CLOSED"
)

type ChecksState string

const (
	ChecksNone    ChecksState = ""
	ChecksPending ChecksState = "PENDING"
	ChecksPassing ChecksState = "SUCCESS"
	ChecksFailing ChecksState = "FAILURE"
)

type ReviewState string

const (
	ReviewNone             ReviewState = ""
	ReviewRequired         ReviewState = "REVIEW_REQUIRED"
	ReviewApproved         ReviewState = "APPROVED"
	ReviewChangesRequested ReviewState = "CHANGES_REQUESTED"
)

// Check is one failing check on a PR.
type Check struct {
	Name         string
	RunID        int64 // GitHub Actions run, 0 if not an Actions run
	CodeScanning bool  // a re-run cannot fix it
	URL          string
}

// PR is one pull request.
type PR struct {
	Repo          string // "owner/name", lowercased by the adapter; must equal Deploy.Revisions keys
	DefaultBranch string // the branch pipelines deploy, usually "main"
	Number        int
	Title         string
	HeadRef       string
	BaseRef       string
	URL           string
	State         PRState
	MergeSHA      string // set only when State == PRMerged
	MergedAt      time.Time
	OpenedAt      time.Time
	Checks        ChecksState
	Failing       []Check
	Review        ReviewState
	Reviewers     int // requested reviewers + submitted reviews
	// EffectiveSHA is the commit that carries this PR onto DefaultBranch.
	// link sets it: MergeSHA for a PR merged into DefaultBranch, the stack
	// bottom's MergeSHA for a stacked PR, "" while the stack is unmerged.
	EffectiveSHA string
	StackPending bool // merged into a stack branch that has not reached DefaultBranch
}

// Env is one deploy environment: a stage of a pipeline in an account.
type Env struct {
	Account  string // config name, e.g. "prod-acct"
	Pipeline string
	Stage    string // "Test" | "Production"
	Order    int    // position in config; higher is further along
	Prod     bool
	ReadOnly bool // profile cannot approve
	// Repos are the lowercased "owner/name" repos this Env deploys, filled
	// from config. When set, link trusts it over what history shows; when
	// empty, link falls back to the repos seen in the Env's deploys. Env is
	// not comparable with == because of this field; compare ID() instead.
	Repos []string
}

func (e Env) ID() string { return e.Account + "/" + e.Pipeline + "/" + e.Stage }

type DeployStatus string

const (
	DeploySucceeded        DeployStatus = "Succeeded"
	DeployFailed           DeployStatus = "Failed"
	DeployInProgress       DeployStatus = "InProgress"
	DeployAwaitingApproval DeployStatus = "AwaitingApproval"
)

// Deploy is one execution of an Env's deploy action.
type Deploy struct {
	ExecutionID   string
	Status        DeployStatus
	Revisions     map[string]string // repo "owner/name" → commit SHA; keys lowercased by the adapter, must equal PR.Repo
	FinishedAt    time.Time         // last update time of the action
	ApprovalToken string            // set when Status == DeployAwaitingApproval
}

// Health is the runtime health of an Env's service.
type Health struct {
	Known   bool
	Desired int
	Healthy int
}

// OK reports whether the service is healthy. Unknown health counts as OK so a
// missing service never raises FlagUnhealthy; a service scaled to zero is OK.
func (h Health) OK() bool { return !h.Known || h.Healthy >= h.Desired }

// EnvHistory is everything the AWS adapter knows about one Env.
type EnvHistory struct {
	Env     Env
	Deploys []Deploy // newest first
	Health  Health
}

type SlotState int

const (
	SlotNotYet SlotState = iota
	SlotAwaitingApproval
	SlotInProgress
	SlotFailed
	SlotDeployed
	SlotUnknown    // a compare call failed or returned 404
	SlotRolledBack // was live, then a newer deploy without it replaced it
)

var slotStateNames = [...]string{"not yet", "awaiting approval", "in progress", "failed", "deployed", "unknown", "rolled back"}

func (s SlotState) String() string {
	if s < 0 || int(s) >= len(slotStateNames) {
		return fmt.Sprintf("SlotState(%d)", int(s))
	}
	return slotStateNames[s]
}

// EnvSlot is where a chain's work stands in one Env.
type EnvSlot struct {
	Env    Env
	State  SlotState
	SHA    string // the deployed or pending commit
	At     time.Time
	Health Health
	Deploy *Deploy // the deploy that decided State, if any
	// Applies is true when at least one of the chain's merged PRs is in a
	// repo this Env deploys. rules ignores slots where it is false.
	Applies bool
}

type Stage int

const (
	StageStarted Stage = iota
	StagePROpen
	StageMerged
	StageInTest
	StageAwaitingProd
	StageInProd
)

var stageNames = [...]string{"started", "PR open", "merged", "in test", "awaiting prod", "in prod"}

func (s Stage) String() string {
	if s < 0 || int(s) >= len(stageNames) {
		return fmt.Sprintf("Stage(%d)", int(s))
	}
	return stageNames[s]
}

type Level int

const (
	None Level = iota
	Yellow
	Red
)

func (l Level) Worse(o Level) bool { return l > o }

type FlagKind int

// Order within a level is the order of these constants (design: Flags).
const (
	FlagPipelineFailed FlagKind = iota
	FlagRolledBack
	FlagUnhealthy
	FlagCheckFailed
	FlagChangesRequested
	FlagAwaitingApproval
	FlagPartialProd // live in some prod Envs, not all, for too long
	FlagReadyToMerge
	FlagStaleReview
	FlagStatusMismatch
	FlagDeployUnknown // an Env's deploy status could not be decided
)

var flagKindNames = [...]string{
	"pipeline failed",
	"rolled back",
	"unhealthy",
	"check failed",
	"changes requested",
	"awaiting approval",
	"partial prod",
	"ready to merge",
	"stale review",
	"status mismatch",
	"deploy unknown",
}

func (k FlagKind) String() string {
	if k < 0 || int(k) >= len(flagKindNames) {
		return fmt.Sprintf("FlagKind(%d)", int(k))
	}
	return flagKindNames[k]
}

// Flag is one reason a chain needs attention.
type Flag struct {
	Level  Level
	Kind   FlagKind
	Reason string
	PR     *PR      // target for f / o, when the flag is about a PR
	Slot   *EnvSlot // target for a / x, when the flag is about an Env
}

// Chain is one ticket and everything linked to it.
type Chain struct {
	Ticket Ticket
	PRs    []PR
	Slots  []EnvSlot // one per configured Env, in Env.Order
	Stage  Stage
	Flags  []Flag // ordered; Flags[0] is the row's flag
}

// Level is the chain's worst flag level, the max over all Flags. Flags[0] is
// still the row's flag; Level does not depend on Flags being sorted.
func (c Chain) Level() Level {
	worst := None
	for _, f := range c.Flags {
		if f.Level.Worse(worst) {
			worst = f.Level
		}
	}
	return worst
}

// Inclusion is the answer to "is commit A in commit B?".
type Inclusion int

const (
	InclusionUnknown Inclusion = iota
	Included
	NotIncluded
)

// CompareFunc answers whether base is included in head within repo. The
// resolver implements it with a cache; link only calls it.
type CompareFunc func(repo, base, head string) Inclusion
