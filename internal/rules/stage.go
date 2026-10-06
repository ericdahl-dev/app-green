// Package rules decides a chain's stage and flags. Pure functions only; the
// clock comes in as an argument.
package rules

import "github.com/ericdahl-dev/app-green/internal/model"

// Stage is the furthest point the chain's work has reached: the least
// advanced PR decides among PR states (any open PR means PR open; any
// stack-pending PR means merged), then the slots decide. Only slots that apply
// (the Env deploys one of the chain's merged repos) count. In prod means every
// applicable prod slot is Deployed; awaiting prod means at least one is
// Deployed, awaiting approval or in progress; in test means a test slot is
// Deployed. Unknown and failed slots never advance the stage; flags report
// what is wrong.
func Stage(c model.Chain) model.Stage {
	live, stackPending := 0, false
	for _, p := range c.PRs {
		if p.State == model.PRClosed {
			continue // closed without merging: not part of the work
		}
		live++
		if p.State == model.PROpen {
			return model.StagePROpen
		}
		stackPending = stackPending || p.StackPending
	}
	if live == 0 {
		return model.StageStarted
	}
	if stackPending {
		// Merged into a stack branch that has not reached the default branch:
		// nothing a slot says about sibling PRs can move the chain past merged.
		return model.StageMerged
	}
	best := model.StageMerged
	prod, prodDeployed, prodMoving := 0, 0, false
	for _, s := range c.Slots {
		if !s.Applies {
			continue
		}
		if !s.Env.Prod {
			if s.State == model.SlotDeployed {
				best = model.StageInTest
			}
			continue
		}
		prod++
		switch s.State {
		case model.SlotDeployed:
			prodDeployed++
			prodMoving = true
		case model.SlotAwaitingApproval, model.SlotInProgress:
			prodMoving = true
		}
	}
	switch {
	case prod > 0 && prodDeployed == prod:
		return model.StageInProd
	case prodMoving:
		return model.StageAwaitingProd
	}
	return best
}
