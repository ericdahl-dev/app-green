package ui_test

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/ericdahl-dev/app-green/internal/model"
	"github.com/ericdahl-dev/app-green/internal/ui"
	"github.com/muesli/termenv"
)

func TestMain(m *testing.M) {
	lipgloss.SetColorProfile(termenv.Ascii)
	os.Exit(m.Run())
}

var now = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

// layout is the fixed layout the exact-output row tests use: an 80-cell row
// leaves 80 - (6 + 12 + 13) - 20 = 29 cells for the reason.
var layout = ui.Layout{Width: 80, KeyWidth: 6, TitleWidth: 20, StageWidth: 12}

// row renders c and checks the row fills the width exactly; it returns the
// row with trailing padding trimmed so expectations stay readable.
func row(t *testing.T, c model.Chain, l ui.Layout) string {
	t.Helper()
	got := ui.RenderRow(c, l, now)
	if w := ansi.StringWidth(got); w != l.Width {
		t.Errorf("row is %d cells wide, want %d: %q", w, l.Width, got)
	}
	return strings.TrimRight(got, " ")
}

func ticket(key, title string) model.Ticket {
	return model.Ticket{Key: key, Title: title, Status: "In Progress", StatusCategory: model.StatusInProgress}
}

func TestRowStartedHasNoMarker(t *testing.T) {
	c := model.Chain{Ticket: ticket("ABC-1", "Short title"), Stage: model.StageStarted}
	want := "  ABC-1   Short title           started"
	if got := row(t, c, layout); got != want {
		t.Errorf("row\n got %q\nwant %q", got, want)
	}
}

func TestRowRedFailingCheck(t *testing.T) {
	c := model.Chain{
		Ticket: ticket("ABC-1", "Short title"),
		PRs: []model.PR{{Repo: "acme/app", Number: 3, State: model.PROpen, Checks: model.ChecksFailing,
			Failing: []model.Check{{Name: "rspec", RunID: 42}}}},
		Stage: model.StagePROpen,
	}
	c.Flags = []model.Flag{{Level: model.Red, Kind: model.FlagCheckFailed, Reason: "PR #3 rspec failing", PR: &c.PRs[0]}}
	want := "● ABC-1   Short title           PR #3         PR #3 rspec failing            f"
	if got := row(t, c, layout); got != want {
		t.Errorf("row\n got %q\nwant %q", got, want)
	}
}

func TestStageText(t *testing.T) {
	prodSlot := func(at time.Time, state model.SlotState, applies bool) model.EnvSlot {
		return model.EnvSlot{Env: model.Env{Account: "prod-acct", Stage: "Production", Prod: true}, State: state, At: at, Applies: applies}
	}
	testSlot := model.EnvSlot{Env: model.Env{Account: "stage-acct", Stage: "Test"}, State: model.SlotDeployed, At: now.Add(-time.Minute), Applies: true}
	tests := []struct {
		name string
		c    model.Chain
		want string
	}{
		{"started", model.Chain{Stage: model.StageStarted}, "started"},
		{"first open PR", model.Chain{Stage: model.StagePROpen, PRs: []model.PR{{Number: 2, State: model.PRClosed}, {Number: 7, State: model.PROpen}, {Number: 9, State: model.PROpen}}}, "PR #7"},
		{"PR open with no open PR", model.Chain{Stage: model.StagePROpen}, "PR open"},
		{"merged", model.Chain{Stage: model.StageMerged}, "merged"},
		{"in test", model.Chain{Stage: model.StageInTest}, "test ✓"},
		{"awaiting prod", model.Chain{Stage: model.StageAwaitingProd}, "prod ‖"},
		{"in prod, latest prod deploy", model.Chain{Stage: model.StageInProd, Slots: []model.EnvSlot{
			testSlot,
			prodSlot(now.Add(-5*time.Hour), model.SlotDeployed, true),
			prodSlot(now.Add(-3*time.Hour), model.SlotDeployed, true),
			prodSlot(now.Add(-time.Hour), model.SlotDeployed, false), // deploys none of the chain's repos
		}}, "in prod ✓ 3h ago"},
		{"in prod minutes ago", model.Chain{Stage: model.StageInProd, Slots: []model.EnvSlot{prodSlot(now.Add(-90*time.Second), model.SlotDeployed, true)}}, "in prod ✓ 1m ago"},
		{"in prod seconds ago", model.Chain{Stage: model.StageInProd, Slots: []model.EnvSlot{prodSlot(now.Add(-12*time.Second), model.SlotDeployed, true)}}, "in prod ✓ 12s ago"},
		{"in prod days ago", model.Chain{Stage: model.StageInProd, Slots: []model.EnvSlot{prodSlot(now.Add(-50*time.Hour), model.SlotDeployed, true)}}, "in prod ✓ 2d ago"},
		{"in prod with no time", model.Chain{Stage: model.StageInProd, Slots: []model.EnvSlot{prodSlot(time.Time{}, model.SlotDeployed, true)}}, "in prod ✓"},
		{"in prod clock skew", model.Chain{Stage: model.StageInProd, Slots: []model.EnvSlot{prodSlot(now.Add(time.Minute), model.SlotDeployed, true)}}, "in prod ✓ 0s ago"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ui.StageText(tt.c, now); got != tt.want {
				t.Errorf("StageText = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRowYellowAwaitingApproval(t *testing.T) {
	c := model.Chain{
		Ticket: ticket("ABC-22", "Form opt-outs"),
		Slots: []model.EnvSlot{{
			Env:     model.Env{Account: "prod-acct", Stage: "Production", Prod: true},
			State:   model.SlotAwaitingApproval,
			Applies: true,
			Deploy:  &model.Deploy{Status: model.DeployAwaitingApproval, ApprovalToken: "tok"},
		}},
		Stage: model.StageAwaitingProd,
	}
	c.Flags = []model.Flag{{Level: model.Yellow, Kind: model.FlagAwaitingApproval, Reason: "prod-acct Production awaiting approval", Slot: &c.Slots[0]}}
	want := "◐ ABC-22  Form opt-outs         prod ‖        prod-acct Production awaitin…  a/x"
	if got := row(t, c, layout); got != want {
		t.Errorf("row\n got %q\nwant %q", got, want)
	}
}

func TestRowInProdWithAge(t *testing.T) {
	c := model.Chain{
		Ticket: ticket("ABC-3", "Item count"),
		Slots: []model.EnvSlot{{Env: model.Env{Account: "prod-acct", Stage: "Production", Prod: true},
			State: model.SlotDeployed, At: now.Add(-3 * time.Hour), Applies: true}},
		Stage: model.StageInProd,
	}
	want := "  ABC-3   Item count            in prod ✓ 3…"
	if got := row(t, c, layout); got != want {
		t.Errorf("row\n got %q\nwant %q", got, want)
	}
	wide := layout
	wide.StageWidth = 16
	want = "  ABC-3   Item count            in prod ✓ 3h ago"
	if got := row(t, c, wide); got != want {
		t.Errorf("row\n got %q\nwant %q", got, want)
	}
}

func TestRowTruncatesLongTitle(t *testing.T) {
	c := model.Chain{Ticket: ticket("ABC-4", "Remove the legacy task path from the worker"), Stage: model.StageMerged}
	want := "  ABC-4   Remove the legacy t…  merged"
	if got := row(t, c, layout); got != want {
		t.Errorf("row\n got %q\nwant %q", got, want)
	}
}

func TestRowStaleHasTildePrefix(t *testing.T) {
	c := model.Chain{Ticket: ticket("ABC-5", "Health checks"), Stage: model.StageInTest, Stale: true, StaleReason: "acme/app failed to load"}
	want := "  ABC-5   Health checks         ~ test ✓"
	if got := row(t, c, layout); got != want {
		t.Errorf("row\n got %q\nwant %q", got, want)
	}
}

func TestRowStaleIsDimmed(t *testing.T) {
	color := terminal(env{"TERM": "xterm-256color"})
	c := model.Chain{Ticket: ticket("ABC-6", "Dim me"), Stage: model.StageMerged, Stale: true}
	c.Flags = []model.Flag{{Level: model.Red, Kind: model.FlagStatusMismatch, Reason: "Jira still In Progress, PR merged"}}
	got := color.Row(c, layout, now)
	// The marker is red and faint; everything after it is one faint run, so a
	// reset inside the row cannot end the dimming early.
	marker, rest, ok := strings.Cut(got, "●\x1b[0m")
	if !ok || (marker != "\x1b[2;31m" && marker != "\x1b[31;2m") {
		t.Fatalf("marker is not red and faint: %q", got)
	}
	want := "\x1b[2m" + strings.TrimPrefix(ansi.Strip(got), "●") + "\x1b[0m"
	if rest != want {
		t.Errorf("rest of the row\n got %q\nwant %q", rest, want)
	}

	c.Stale = false
	if got := color.Row(c, layout, now); strings.Contains(got, "\x1b[2m") || strings.Contains(got, ";2m") {
		t.Errorf("a fresh row is dimmed: %q", got)
	}
}

// board is a list with one row of each kind the tests cover.
func board() []model.Chain {
	inProd := model.Chain{
		Ticket: ticket("ABC-1003", "Item count on the add page"),
		Slots: []model.EnvSlot{{Env: model.Env{Account: "prod-acct", Stage: "Production", Prod: true},
			State: model.SlotDeployed, At: now.Add(-3 * time.Hour), Applies: true}},
		Stage: model.StageInProd,
		Stale: true,
	}
	failing := model.Chain{
		Ticket: ticket("ABC-1", "Short title"),
		PRs: []model.PR{{Repo: "acme/app", Number: 3, State: model.PROpen, Checks: model.ChecksFailing,
			Failing: []model.Check{{Name: "rspec", RunID: 42}}}},
		Stage: model.StagePROpen,
	}
	failing.Flags = []model.Flag{{Level: model.Red, Kind: model.FlagCheckFailed, Reason: "PR #3 rspec failing", PR: &failing.PRs[0]}}
	started := model.Chain{Ticket: ticket("ABC-22", "S3 access logging"), Stage: model.StageStarted}
	return []model.Chain{failing, started, inProd}
}

func TestNewLayout(t *testing.T) {
	got := ui.NewLayout(board(), now, 100)
	// fixed = 8 (key) + 18 ("~ in prod ✓ 3h ago") + 13 = 39, leaving 61; the
	// title takes half, capped at the longest title (26).
	want := ui.Layout{Width: 100, KeyWidth: 8, TitleWidth: 26, StageWidth: 18}
	if got != want {
		t.Errorf("NewLayout = %+v, want %+v", got, want)
	}
	got = ui.NewLayout(board(), now, 80)
	want = ui.Layout{Width: 80, KeyWidth: 8, TitleWidth: 20, StageWidth: 18}
	if got != want {
		t.Errorf("NewLayout = %+v, want %+v", got, want)
	}
	if got := ui.NewLayout(nil, now, 80); got != (ui.Layout{Width: 80}) {
		t.Errorf("NewLayout(nil) = %+v", got)
	}
}

func TestRowsAtEveryWidth(t *testing.T) {
	chains := board()
	for w := 0; w <= 120; w++ {
		l := ui.NewLayout(chains, now, w)
		for _, c := range chains {
			got := ui.RenderRow(c, l, now)
			if ansi.StringWidth(got) != w {
				t.Fatalf("width %d: row is %d cells: %q", w, ansi.StringWidth(got), got)
			}
			// From the marker, a cell of text and the hint column up, the
			// hint always shows.
			if k := ui.RowAction(c).Key(); k != "" && w >= 1+1+2+3 && !strings.HasSuffix(strings.TrimRight(got, " "), k) {
				t.Errorf("width %d: hint %q is cut: %q", w, k, got)
			}
		}
	}
}

func TestRowsAtBoardWidth(t *testing.T) {
	chains := board()
	l := ui.NewLayout(chains, now, 100)
	want := []string{
		"● ABC-1     Short title                 PR #3               PR #3 rspec failing" + strings.Repeat(" ", 16+2) + "f",
		"  ABC-22    S3 access logging           started",
		"  ABC-1003  Item count on the add page  ~ in prod ✓ 3h ago",
	}
	for i, c := range chains {
		if got := row(t, c, l); got != want[i] {
			t.Errorf("row %d\n got %q\nwant %q", i, got, want[i])
		}
	}
}

func TestRowNarrowCutsAtTheRightEdge(t *testing.T) {
	chains := board()
	l := ui.NewLayout(chains, now, 30)
	// The hint stays; the stage keeps its place and the reason is dropped.
	want := "● ABC-1     PR #3" + strings.Repeat(" ", 8+2) + "f  "
	if got := ui.RenderRow(chains[0], l, now); got != want {
		t.Errorf("row\n got %q\nwant %q", got, want)
	}
	// Narrower still: the key and stage are cut, never the hint.
	l = ui.NewLayout(chains, now, 12)
	want = "● ABC-…  f  "
	if got := ui.RenderRow(chains[0], l, now); got != want {
		t.Errorf("row\n got %q\nwant %q", got, want)
	}
}

// TestRowCleansUntrustedText: titles, keys and reasons come from Jira, GitHub
// and AWS, so tabs, newlines and escape sequences (an OSC 52 clipboard write
// here) must not reach the terminal or break the layout.
func TestRowCleansUntrustedText(t *testing.T) {
	osc52 := "\x1b]52;c;ZXZpbA==\x07"
	c := model.Chain{
		Ticket: ticket("ABC-7\r", "Tab\there\nnew"+osc52+"line\x1b[31mred\x9b"),
		Stage:  model.StageStarted,
	}
	c.Flags = []model.Flag{{Level: model.Red, Kind: model.FlagCheckFailed, Reason: "PR #3 \x1b[2Jlint\tfailing"}}
	chains := []model.Chain{c}
	for _, w := range []int{20, 50, 80} {
		got := ui.RenderRow(c, ui.NewLayout(chains, now, w), now)
		if strings.ContainsAny(got, "\x1b\t\n\r\x07") || strings.Contains(got, "\x9b") {
			t.Errorf("width %d: control characters reach the terminal: %q", w, got)
		}
		if ansi.StringWidth(got) != w {
			t.Errorf("width %d: row is %d cells: %q", w, ansi.StringWidth(got), got)
		}
	}
	want := "● ABC-7   Tab here newlinered   started       PR #3 lint failing"
	if got := row(t, c, layout); got != want {
		t.Errorf("row\n got %q\nwant %q", got, want)
	}
}
