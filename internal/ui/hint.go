package ui

import (
	"fmt"
	"strings"

	"github.com/ericdahl-dev/app-green/internal/model"
)

// Action is what a row's key does for its first flag.
type Action int

const (
	ActionNone    Action = iota
	ActionRerun          // f: re-run the PR's failed Actions runs
	ActionOpen           // o: open the target in the browser
	ActionApprove        // a/x: approve or reject the waiting approval
	ActionStatus         // t: change the Jira status
)

// actionUndecided is flagAction's answer for a FlagKind it has no case for;
// RowAction shows it as ActionNone, and a test fails on it.
const actionUndecided Action = -1

// Key is the hint shown at the end of the row, "" for ActionNone.
func (a Action) Key() string {
	switch a {
	case ActionRerun:
		return "f"
	case ActionOpen:
		return "o"
	case ActionApprove:
		return "a/x"
	case ActionStatus:
		return "t"
	}
	return ""
}

func (a Action) String() string {
	if a == ActionNone {
		return "none"
	}
	if k := a.Key(); k != "" {
		return k
	}
	return fmt.Sprintf("Action(%d)", int(a))
}

// RowAction is the action a row offers for c.Flags[0], or ActionNone when the
// flag has no target the action can work on, or when the row's flags give
// that action more than one target (two approvals waiting, failing re-runnable
// checks in two PRs): then the detail screen chooses.
func RowAction(c model.Chain) Action {
	if len(c.Flags) == 0 {
		return ActionNone
	}
	a := flagAction(c.Flags[0])
	if a == ActionNone || a == actionUndecided {
		return ActionNone
	}
	first := target(c.Flags[0])
	for _, f := range c.Flags[1:] {
		if flagAction(f) == a && target(f) != first {
			return ActionNone
		}
	}
	return a
}

// flagAction is the action f alone offers, ActionNone without a target.
// Every FlagKind has its own case; a kind added later without one gives
// actionUndecided.
func flagAction(f model.Flag) Action {
	slot := func() Action {
		if f.Slot != nil {
			return ActionOpen
		}
		return ActionNone
	}
	pr := func() Action {
		if f.PR != nil {
			return ActionOpen
		}
		return ActionNone
	}
	switch f.Kind {
	case model.FlagPipelineFailed, model.FlagRolledBack, model.FlagUnhealthy, model.FlagPartialProd:
		return slot() // open the pipeline
	case model.FlagDeployUnknown:
		// A repo no configured Env deploys has no Slot, so no key.
		return slot()
	case model.FlagStranded, model.FlagCheckExpected, model.FlagChangesRequested, model.FlagReadyToMerge, model.FlagStaleReview:
		return pr()
	case model.FlagCheckFailed:
		if f.PR == nil {
			return ActionNone
		}
		return failedCheckAction(f.PR)
	case model.FlagAwaitingApproval:
		s := f.Slot
		if s == nil {
			return ActionNone
		}
		if !s.Env.ReadOnly && s.Deploy != nil && s.Deploy.ApprovalToken != "" {
			return ActionApprove
		}
		return ActionOpen // cannot approve here: open the pipeline console
	case model.FlagStatusMismatch:
		return ActionStatus
	}
	return actionUndecided
}

// target names what f's action works on: its Env, its PR, or else the ticket.
func target(f model.Flag) string {
	switch {
	case f.Slot != nil:
		return "env " + f.Slot.Env.ID()
	case f.PR != nil:
		return fmt.Sprintf("pr %s#%d", strings.ToLower(f.PR.Repo), f.PR.Number)
	}
	return "ticket"
}

// failedCheckAction is f when any failing check of pr is a re-runnable
// Actions run (the re-run acts on those; code scanning stays for the detail
// screen). Otherwise it is o when there is a page to open: a failing check's
// own URL, else ChecksPage(pr). Code scanning and checks outside Actions
// follow the same rule. With nothing to open it is none.
func failedCheckAction(pr *model.PR) Action {
	open := ChecksPage(*pr) != ""
	for _, c := range pr.Failing {
		if c.RunID > 0 && !c.CodeScanning {
			return ActionRerun
		}
		open = open || c.URL != ""
	}
	if open {
		return ActionOpen
	}
	return ActionNone
}

// ChecksPage is the PR's checks tab, "" when the PR's URL is unknown. o opens
// it for a failing check that has no URL of its own.
func ChecksPage(pr model.PR) string {
	if pr.URL == "" {
		return ""
	}
	return strings.TrimSuffix(pr.URL, "/") + "/checks"
}
