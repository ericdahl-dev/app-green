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

var actionKeys = [...]string{"", "f", "o", "a/x", "t"}

// Key is the hint shown at the end of the row, "" for ActionNone.
func (a Action) Key() string {
	if a < 0 || int(a) >= len(actionKeys) {
		return ""
	}
	return actionKeys[a]
}

func (a Action) String() string {
	if a == ActionNone {
		return "none"
	}
	if a < 0 || int(a) >= len(actionKeys) {
		return fmt.Sprintf("Action(%d)", int(a))
	}
	return actionKeys[a]
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
	if a == ActionNone {
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
func flagAction(f model.Flag) Action {
	switch f.Kind {
	case model.FlagCheckFailed:
		if f.PR != nil {
			return failedCheckAction(f.PR.Failing)
		}
	case model.FlagPipelineFailed, model.FlagRolledBack, model.FlagDeployUnknown:
		if f.Slot != nil {
			return ActionOpen
		}
	case model.FlagStranded, model.FlagCheckExpected:
		if f.PR != nil {
			return ActionOpen
		}
	case model.FlagAwaitingApproval:
		if s := f.Slot; s != nil {
			if !s.Env.ReadOnly && s.Deploy != nil && s.Deploy.ApprovalToken != "" {
				return ActionApprove
			}
			return ActionOpen // cannot approve here: open the pipeline console
		}
	case model.FlagStatusMismatch:
		return ActionStatus
	}
	return ActionNone
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

// failedCheckAction is f when any failing check is a re-runnable Actions run
// (the re-run acts on those; code scanning stays for the detail screen), o
// when any is code scanning or has a URL to open, else none.
func failedCheckAction(cs []model.Check) Action {
	open := false
	for _, c := range cs {
		if c.RunID > 0 && !c.CodeScanning {
			return ActionRerun
		}
		open = open || c.CodeScanning || c.URL != ""
	}
	if open {
		return ActionOpen
	}
	return ActionNone
}
