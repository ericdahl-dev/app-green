package ui

import (
	"fmt"
	"testing"

	"github.com/ericdahl-dev/app-green/internal/model"
)

// TestEveryFlagKindHasADecision fails when a FlagKind is added to model
// without an explicit case in flagAction.
func TestEveryFlagKindHasADecision(t *testing.T) {
	n := 0
	for k := model.FlagKind(0); k.String() != fmt.Sprintf("FlagKind(%d)", int(k)); k++ {
		n++
		f := model.Flag{Kind: k, PR: &model.PR{Number: 3}, Slot: &model.EnvSlot{}}
		if flagAction(f) == actionUndecided {
			t.Errorf("flagAction has no case for %v", k)
		}
	}
	if n == 0 {
		t.Fatal("found no flag kinds")
	}
	if flagAction(model.Flag{Kind: model.FlagKind(n)}) != actionUndecided {
		t.Errorf("an unknown kind is not reported as undecided")
	}
}
