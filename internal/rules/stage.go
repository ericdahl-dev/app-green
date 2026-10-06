// Package rules decides a chain's stage and flags. Pure functions only; the
// clock comes in as an argument.
package rules

import "github.com/ericdahl-dev/app-green/internal/model"

// Stage is the furthest point the chain's work has reached: the least
// advanced PR decides among PR states (any open PR means PR open; any
// stack-pending PR means merged), then the furthest slot in any Env decides.
// Unknown and failed slots, and test slots still in progress or awaiting
// approval, never advance the stage; flags report what is wrong.
func Stage(c model.Chain) model.Stage {
	if len(c.PRs) == 0 {
		return model.StageStarted
	}
	stackPending := false
	for _, p := range c.PRs {
		if p.State == model.PROpen {
			return model.StagePROpen
		}
		stackPending = stackPending || p.StackPending
	}
	if stackPending {
		// Merged into a stack branch that has not reached the default branch:
		// nothing a slot says about sibling PRs can move the chain past merged.
		return model.StageMerged
	}
	best := model.StageMerged
	for _, s := range c.Slots {
		var st model.Stage
		switch {
		case s.Env.Prod && s.State == model.SlotDeployed:
			st = model.StageInProd
		case s.Env.Prod && (s.State == model.SlotAwaitingApproval || s.State == model.SlotInProgress):
			st = model.StageAwaitingProd
		case !s.Env.Prod && s.State == model.SlotDeployed:
			st = model.StageInTest
		default:
			continue
		}
		if st > best {
			best = st
		}
	}
	return best
}
