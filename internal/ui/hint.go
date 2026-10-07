package ui

import (
	"fmt"

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
// flag has no single target the action can work on. Other flags never count.
func RowAction(c model.Chain) Action {
	if len(c.Flags) == 0 {
		return ActionNone
	}
	f := c.Flags[0]
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
		if s := f.Slot; s != nil && !s.Env.ReadOnly && s.Deploy != nil && s.Deploy.ApprovalToken != "" {
			return ActionApprove
		}
	case model.FlagStatusMismatch:
		return ActionStatus
	}
	return ActionNone
}

// failedCheckAction is f when every failing check is a re-runnable Actions
// run, o when any is code scanning (a re-run cannot fix it), else none.
func failedCheckAction(cs []model.Check) Action {
	if len(cs) == 0 {
		return ActionNone
	}
	rerun := true
	for _, c := range cs {
		if c.CodeScanning {
			return ActionOpen
		}
		rerun = rerun && c.RunID > 0
	}
	if rerun {
		return ActionRerun
	}
	return ActionNone
}
