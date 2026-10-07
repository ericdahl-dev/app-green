package ui_test

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/ericdahl-dev/app-green/internal/model"
	"github.com/ericdahl-dev/app-green/internal/resolver"
	"github.com/ericdahl-dev/app-green/internal/ui"
	"github.com/muesli/termenv"
)

// env is a fake process environment for a renderer.
type env map[string]string

func (e env) Getenv(k string) string { return e[k] }
func (e env) Environ() []string {
	var out []string
	for k, v := range e {
		out = append(out, k+"="+v)
	}
	return out
}

// terminal renders through a color terminal with the given environment, the
// way the app renders to a real one: the renderer detects the profile.
func terminal(e env) ui.Styles {
	return ui.NewStyles(lipgloss.NewRenderer(io.Discard, termenv.WithTTY(true), termenv.WithEnvironment(e)))
}

func TestNoColor(t *testing.T) {
	c := model.Chain{Ticket: ticket("ABC-1", "Short title"), Stage: model.StagePROpen, Stale: true}
	c.Flags = []model.Flag{{Level: model.Red, Kind: model.FlagStatusMismatch, Reason: "Jira still In Progress, PR merged"}}
	st := []resolver.AdapterStatus{{Name: "jira", OK: true, At: now.Add(-time.Second)}, {Name: "aws prod-acct", SSO: true}}
	render := func(s ui.Styles) string { return s.Header(st, now, 100) + "\n" + s.Row(c, layout, now) }

	// Without NO_COLOR the same terminal gets color, so the test below is
	// not passing only because nothing is ever colored.
	got := render(terminal(env{"TERM": "xterm-256color"}))
	for _, want := range []string{"\x1b[32m✓\x1b[0m", "\x1b[31m✗ sso expired\x1b[0m"} {
		if !strings.Contains(got, want) {
			t.Fatalf("a color terminal is missing %q: %q", want, got)
		}
	}
	if got := render(terminal(env{"TERM": "xterm-256color", "NO_COLOR": "1"})); strings.Contains(got, "\x1b[") {
		t.Errorf("NO_COLOR output has escape codes: %q", got)
	}
}
