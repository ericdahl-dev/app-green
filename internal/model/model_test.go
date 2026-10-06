package model_test

import (
	"strings"
	"testing"

	"github.com/ericdahl-dev/app-green/internal/model"
)

func TestEnvID(t *testing.T) {
	e := model.Env{Account: "prod-acct", Pipeline: "app-pipe", Stage: "Production"}
	if got, want := e.ID(), "prod-acct/app-pipe/Production"; got != want {
		t.Fatalf("ID() = %q, want %q", got, want)
	}
}

func TestLevelWorse(t *testing.T) {
	if !model.Red.Worse(model.Yellow) || model.Yellow.Worse(model.Red) || model.None.Worse(model.Yellow) {
		t.Fatal("Red > Yellow > None")
	}
}

func TestStageString(t *testing.T) {
	for s := model.StageStarted; s <= model.StageInProd; s++ {
		if got := s.String(); got == "" || strings.HasPrefix(got, "Stage(") {
			t.Errorf("Stage %d has no label: %q", int(s), got)
		}
	}
	if got := model.Stage(99).String(); got != "Stage(99)" {
		t.Errorf("Stage(99).String() = %q", got)
	}
}

func TestSlotStateString(t *testing.T) {
	for s := model.SlotNotYet; s <= model.SlotRolledBack; s++ {
		if got := s.String(); got == "" || strings.HasPrefix(got, "SlotState(") {
			t.Errorf("SlotState %d has no label: %q", int(s), got)
		}
	}
	if got := model.SlotState(99).String(); got != "SlotState(99)" {
		t.Errorf("SlotState(99).String() = %q", got)
	}
}

func TestFlagKindString(t *testing.T) {
	for k := model.FlagPipelineFailed; k <= model.FlagDeployUnknown; k++ {
		if got := k.String(); got == "" || strings.HasPrefix(got, "FlagKind(") {
			t.Errorf("FlagKind %d has no label: %q", int(k), got)
		}
	}
	if got := model.FlagKind(99).String(); got != "FlagKind(99)" {
		t.Errorf("FlagKind(99).String() = %q", got)
	}
}

func TestHealthOK(t *testing.T) {
	cases := []struct {
		name string
		h    model.Health
		want bool
	}{
		{"unknown", model.Health{Known: false}, true},
		{"all healthy", model.Health{Known: true, Desired: 2, Healthy: 2}, true},
		{"one down", model.Health{Known: true, Desired: 2, Healthy: 1}, false},
		{"scaled to zero", model.Health{Known: true, Desired: 0, Healthy: 0}, true},
	}
	for _, c := range cases {
		if got := c.h.OK(); got != c.want {
			t.Errorf("%s: OK() = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestChainLevel(t *testing.T) {
	c := model.Chain{Flags: []model.Flag{{Level: model.Yellow}, {Level: model.Red}}}
	if got := c.Level(); got != model.Red {
		t.Errorf("unsorted flags: Level() = %v, want Red", got)
	}
	if got := (model.Chain{}).Level(); got != model.None {
		t.Errorf("no flags: Level() = %v, want None", got)
	}
}

func TestFlagDeployUnknownIsLastAndLabeled(t *testing.T) {
	if model.FlagDeployUnknown <= model.FlagStatusMismatch {
		t.Errorf("FlagDeployUnknown = %d, want it after FlagStatusMismatch (%d)", model.FlagDeployUnknown, model.FlagStatusMismatch)
	}
	if got := model.FlagDeployUnknown.String(); got != "deploy unknown" {
		t.Errorf("FlagDeployUnknown.String() = %q, want %q", got, "deploy unknown")
	}
}

func TestRolledBackLabels(t *testing.T) {
	if model.SlotRolledBack <= model.SlotUnknown || model.SlotRolledBack.String() != "rolled back" {
		t.Errorf("SlotRolledBack = %d %q, want after SlotUnknown, labeled %q", model.SlotRolledBack, model.SlotRolledBack, "rolled back")
	}
	if model.FlagRolledBack != model.FlagPipelineFailed+1 || model.FlagRolledBack.String() != "rolled back" {
		t.Errorf("FlagRolledBack = %d %q, want right after FlagPipelineFailed, labeled %q", model.FlagRolledBack, model.FlagRolledBack, "rolled back")
	}
}

func TestFlagPartialProdFollowsAwaitingApproval(t *testing.T) {
	if model.FlagPartialProd != model.FlagAwaitingApproval+1 || model.FlagPartialProd.String() != "partial prod" {
		t.Errorf("FlagPartialProd = %d %q, want right after FlagAwaitingApproval, labeled %q", model.FlagPartialProd, model.FlagPartialProd, "partial prod")
	}
}

func TestFlagStrandedFollowsRolledBack(t *testing.T) {
	if model.FlagStranded != model.FlagRolledBack+1 || model.FlagStranded.String() != "stranded" {
		t.Errorf("FlagStranded = %d %q, want right after FlagRolledBack, labeled %q", model.FlagStranded, model.FlagStranded, "stranded")
	}
	if model.FlagDeployUnknown.String() != "deploy unknown" {
		t.Errorf("labels shifted: FlagDeployUnknown.String() = %q", model.FlagDeployUnknown)
	}
}
