package model_test

import (
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
