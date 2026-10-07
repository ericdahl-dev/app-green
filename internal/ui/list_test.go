package ui_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/ericdahl-dev/app-green/internal/model"
	"github.com/ericdahl-dev/app-green/internal/resolver"
	"github.com/ericdahl-dev/app-green/internal/ui"
)

// newApp is an App on the default (ASCII) styles and the fixed clock, sized
// 100x30, with no snapshot yet.
func newApp(t *testing.T) ui.App {
	t.Helper()
	m := ui.NewApp(make(chan resolver.Snapshot), make(chan struct{}, 1), noOpen(t)).
		WithClock(func() time.Time { return now })
	return update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
}

// noOpen fails the test if the app tries to open a browser.
func noOpen(t *testing.T) func(string) error {
	return func(u string) error { t.Errorf("unexpected open %q", u); return nil }
}

// update sends msg and returns the App, dropping the command.
func update(t *testing.T, m ui.App, msg tea.Msg) ui.App {
	t.Helper()
	got, _ := m.Update(msg)
	app, ok := got.(ui.App)
	if !ok {
		t.Fatalf("Update returned %T, want ui.App", got)
	}
	return app
}

func viewLines(m ui.App) []string { return strings.Split(m.View(), "\n") }

func TestAppLoading(t *testing.T) {
	m := newApp(t)
	if v := m.View(); !strings.Contains(v, "loading…") {
		t.Errorf("before the first snapshot the view says loading…:\n%s", v)
	}
}

func TestAppNothingInFlight(t *testing.T) {
	m := update(t, newApp(t), resolver.Snapshot{At: now})
	v := m.View()
	if !strings.Contains(v, "nothing in flight") || strings.Contains(v, "loading…") {
		t.Errorf("an empty snapshot says nothing in flight:\n%s", v)
	}
}

// footerKeys is the footer's key line.
const footerKeys = "↑/↓ move  enter details  f re-run  a/x approve/reject  t status  o open  r refresh  q quit"

// fxSnapshot has three chains (a failing check, an approval to open, a plain
// ticket) and one unlinked PR.
func fxSnapshot() resolver.Snapshot {
	failing := model.Chain{
		Ticket: model.Ticket{Key: "ABC-1", Title: "Fix login", URL: "https://jira.example.com/browse/ABC-1"},
		PRs: []model.PR{{Repo: "acme/app", Number: 3, State: model.PROpen, URL: "https://github.com/acme/app/pull/3",
			Checks: model.ChecksFailing, Failing: []model.Check{{Name: "lint", URL: "https://ci.example.com/lint/1"}}}},
		Stage: model.StagePROpen,
	}
	failing.Flags = []model.Flag{{Level: model.Red, Kind: model.FlagCheckFailed, Reason: "PR #3 lint failing", PR: &failing.PRs[0]}}
	env := model.Env{Account: "prod-acct", Region: "us-east-1", Pipeline: "app-pipeline", Stage: "Production", Prod: true, ReadOnly: true}
	waiting := model.Chain{
		Ticket: model.Ticket{Key: "ABC-2", Title: "Add search", URL: "https://jira.example.com/browse/ABC-2"},
		Slots:  []model.EnvSlot{{Env: env, State: model.SlotAwaitingApproval, Applies: true}},
		Stage:  model.StageAwaitingProd,
	}
	waiting.Flags = []model.Flag{{Level: model.Yellow, Kind: model.FlagAwaitingApproval, Reason: "prod-acct waiting for approval", Slot: &waiting.Slots[0]}}
	plain := model.Chain{
		Ticket: model.Ticket{Key: "ABC-3", Title: "Tidy docs", URL: "https://jira.example.com/browse/ABC-3"},
		Stage:  model.StageStarted,
	}
	return resolver.Snapshot{
		Chains:   []model.Chain{failing, waiting, plain},
		Unlinked: []model.PR{{Repo: "acme/app", Number: 9, Title: "Bump deps", URL: "https://github.com/acme/app/pull/9", State: model.PROpen}},
		Status:   []resolver.AdapterStatus{{Name: "jira", OK: true, At: now.Add(-12 * time.Second)}, {Name: "github", OK: true}},
		At:       now,
	}
}

func TestAppRows(t *testing.T) {
	snap := fxSnapshot()
	m := update(t, newApp(t), snap)
	ls := viewLines(m)
	l := ui.NewLayout(snap.Chains, now, 100)
	want := []string{
		ui.RenderHeader(snap.Status, now, 100),
		"●>" + ansi.TruncateLeft(ui.RenderRow(snap.Chains[0], l, now), 2, ""), // the cursor row, next to its marker
		ui.RenderRow(snap.Chains[1], l, now),
		ui.RenderRow(snap.Chains[2], l, now),
		"── Unlinked (1) ──",
		"  acme/app#9  Bump deps",
	}
	if len(ls) < len(want)+1 {
		t.Fatalf("view has %d lines, want at least %d:\n%s", len(ls), len(want)+1, m.View())
	}
	for i, w := range want {
		if got := strings.TrimRight(ls[i], " "); got != strings.TrimRight(w, " ") {
			t.Errorf("line %d\n got %q\nwant %q", i, got, w)
		}
	}
	if got := ls[len(ls)-1]; got != footerKeys {
		t.Errorf("footer\n got %q\nwant %q", got, footerKeys)
	}
}

func key(s string) tea.KeyMsg {
	switch s {
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// press sends each key, runs the command it returns (an open, say) and
// feeds that command's message back, as Bubble Tea would.
func press(t *testing.T, m ui.App, keys ...string) ui.App {
	t.Helper()
	for _, k := range keys {
		got, cmd := m.Update(key(k))
		m = got.(ui.App)
		if cmd != nil {
			if msg := cmd(); msg != nil {
				m = update(t, m, msg)
			}
		}
	}
	return m
}

// isCursor reports whether l carries the ASCII cursor: ">" in the second
// cell, next to the marker.
func isCursor(l string) bool { return ansi.Cut(l, 1, 2) == ">" }

// cursorLine is the line the ASCII cursor (">" in the second cell) is on,
// failing unless exactly one line has it.
func cursorLine(t *testing.T, m ui.App) string {
	t.Helper()
	var found []string
	for _, l := range viewLines(m) {
		if isCursor(l) {
			found = append(found, l)
		}
	}
	if len(found) != 1 {
		t.Fatalf("want one cursor line, got %d:\n%s", len(found), m.View())
	}
	return found[0]
}

func TestAppCursorMoves(t *testing.T) {
	m := update(t, newApp(t), fxSnapshot())
	steps := []struct {
		key  string
		want string // text on the cursor line after the key
	}{
		{"", "ABC-1"},
		{"up", "ABC-1"}, // stays at the top
		{"j", "ABC-2"},
		{"down", "ABC-3"},
		{"j", "acme/app#9"},    // skips the Unlinked header
		{"down", "acme/app#9"}, // stays at the bottom
		{"k", "ABC-3"},         // skips the header going up too
		{"up", "ABC-2"},
	}
	for _, s := range steps {
		if s.key != "" {
			m = press(t, m, s.key)
		}
		if got := cursorLine(t, m); !strings.Contains(got, s.want) {
			t.Errorf("after %q the cursor is on %q, want %s", s.key, got, s.want)
		}
	}
}

func TestAppCursorReverseVideo(t *testing.T) {
	snap := fxSnapshot()
	snap.Chains[0].Stale = true // a red marker and dimmed text: two resets mid-row
	styles := terminal(env{"TERM": "xterm-256color"})
	m := update(t, newApp(t).WithStyles(styles), snap)
	ls := viewLines(m)
	got, plain := ls[1], styles.Row(snap.Chains[0], ui.NewLayout(snap.Chains, now, 100), now)

	const rev, reset = "\x1b[7m", "\x1b[0m"
	if !strings.HasPrefix(got, rev) || !strings.HasSuffix(got, reset) {
		t.Fatalf("the cursor row is wrapped in reverse video: %q", got)
	}
	inner := strings.TrimSuffix(got, reset)
	if strings.Count(inner, reset) == 0 || strings.Count(inner, reset) != strings.Count(inner, reset+rev) {
		t.Errorf("every reset inside the cursor row turns reverse back on: %q", got)
	}
	if ansi.Strip(got) != ansi.Strip(plain) {
		t.Errorf("the cursor row keeps its text\n got %q\nwant %q", ansi.Strip(got), ansi.Strip(plain))
	}
	if strings.Contains(ls[2], rev) {
		t.Errorf("only the cursor row is reversed: %q", ls[2])
	}
}

func TestAppCursorFollowsTicket(t *testing.T) {
	snap := fxSnapshot()
	m := press(t, update(t, newApp(t), snap), "j") // on ABC-2

	reordered := fxSnapshot()
	c := reordered.Chains
	reordered.Chains = []model.Chain{c[2], c[0], c[1]}
	m = update(t, m, reordered)
	if got := cursorLine(t, m); !strings.Contains(got, "ABC-2") {
		t.Errorf("a reordered snapshot keeps the cursor on ABC-2: %q", got)
	}

	// The selected ticket leaves: the cursor stays at its index, clamped.
	gone := fxSnapshot()
	gone.Chains = []model.Chain{c[2], c[0]}
	gone.Unlinked = nil
	m = update(t, m, gone)
	if got := cursorLine(t, m); !strings.Contains(got, "ABC-1") {
		t.Errorf("with ABC-2 gone the cursor clamps to the last row: %q", got)
	}

	// An unlinked PR is followed by repo and number, ignoring case.
	m = press(t, update(t, m, fxSnapshot()), "j", "j", "j")
	moved := fxSnapshot()
	moved.Unlinked = append([]model.PR{{Repo: "acme/web", Number: 1, Title: "New"}}, moved.Unlinked...)
	moved.Unlinked[1].Repo = "Acme/App"
	m = update(t, m, moved)
	if got := cursorLine(t, m); !strings.Contains(got, "Acme/App#9") {
		t.Errorf("the cursor stays on the unlinked PR: %q", got)
	}
}

func TestAppRefresh(t *testing.T) {
	refresh := make(chan struct{}, 1)
	m := ui.NewApp(make(chan resolver.Snapshot), refresh, noOpen(t)).WithClock(func() time.Time { return now })
	m = press(t, m, "r")
	if len(refresh) != 1 {
		t.Fatalf("r sends on the refresh channel")
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		press(t, m, "r") // the buffer is full: this must not block
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("r blocked on a full refresh channel")
	}
	if len(refresh) != 1 {
		t.Errorf("a press while one is pending is coalesced")
	}
}

func TestAppQuit(t *testing.T) {
	for _, k := range []string{"q", "ctrl+c"} {
		_, cmd := newApp(t).Update(key(k))
		if cmd == nil {
			t.Fatalf("%s returns no command", k)
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Errorf("%s does not quit", k)
		}
	}
}

func fakePipelineURL(e model.Env) string { return "pipeline:" + e.ID() }

func TestOpenTarget(t *testing.T) {
	const ticketURL = "https://jira.example.com/browse/ABC-1"
	pr := func(url string, failing ...model.Check) *model.PR {
		return &model.PR{Repo: "acme/app", Number: 3, URL: url, State: model.PROpen, Failing: failing}
	}
	env := model.Env{Account: "prod-acct", Pipeline: "app-pipeline", Stage: "Production", Prod: true}
	slot := func(readOnly bool, token string) *model.EnvSlot {
		e := env
		e.ReadOnly = readOnly
		return &model.EnvSlot{Env: e, State: model.SlotAwaitingApproval, Deploy: &model.Deploy{ApprovalToken: token}}
	}
	other := model.Env{Account: "stage-acct", Pipeline: "app-pipeline", Stage: "Test"}
	tests := []struct {
		name  string
		flags []model.Flag
		want  string
	}{
		{"no flags opens the ticket", nil, ticketURL},
		{"failing check opens its own page",
			[]model.Flag{{Kind: model.FlagCheckFailed, PR: pr("https://github.com/acme/app/pull/3",
				model.Check{Name: "scan", CodeScanning: true}, model.Check{Name: "lint", URL: "https://ci.example.com/lint"})}},
			"https://ci.example.com/lint"},
		{"failing check without a URL opens the checks tab",
			[]model.Flag{{Kind: model.FlagCheckFailed, PR: pr("https://github.com/acme/app/pull/3", model.Check{Name: "lint"})}},
			"https://github.com/acme/app/pull/3/checks"},
		{"re-runnable check (f) opens the checks tab",
			[]model.Flag{{Kind: model.FlagCheckFailed, PR: pr("https://github.com/acme/app/pull/3", model.Check{Name: "rspec", RunID: 7})}},
			"https://github.com/acme/app/pull/3/checks"},
		{"re-runnable check (f) with a URL opens it",
			[]model.Flag{{Kind: model.FlagCheckFailed, PR: pr("https://github.com/acme/app/pull/3", model.Check{Name: "rspec", RunID: 7, URL: "https://ci.example.com/rspec"})}},
			"https://ci.example.com/rspec"},
		{"PR flag opens the PR",
			[]model.Flag{{Kind: model.FlagReadyToMerge, PR: pr("https://github.com/acme/app/pull/3")}},
			"https://github.com/acme/app/pull/3"},
		{"PR flag with no URL opens the ticket",
			[]model.Flag{{Kind: model.FlagReadyToMerge, PR: pr("")}},
			ticketURL},
		{"pipeline failure opens the pipeline",
			[]model.Flag{{Kind: model.FlagPipelineFailed, Slot: &model.EnvSlot{Env: env, State: model.SlotFailed}}},
			"pipeline:prod-acct/app-pipeline/Production"},
		{"approval the profile cannot make opens the pipeline",
			[]model.Flag{{Kind: model.FlagAwaitingApproval, Slot: slot(true, "tok")}},
			"pipeline:prod-acct/app-pipeline/Production"},
		{"approval (a/x) opens the pipeline",
			[]model.Flag{{Kind: model.FlagAwaitingApproval, Slot: slot(false, "tok")}},
			"pipeline:prod-acct/app-pipeline/Production"},
		{"status mismatch (t) opens the ticket",
			[]model.Flag{{Kind: model.FlagStatusMismatch, Reason: "Jira still In Progress"}},
			ticketURL},
		{"repo no env deploys has no target: the ticket",
			[]model.Flag{{Kind: model.FlagDeployUnknown}},
			ticketURL},
		{"two pipelines: the first flag's",
			[]model.Flag{
				{Kind: model.FlagPipelineFailed, Slot: &model.EnvSlot{Env: env}},
				{Kind: model.FlagUnhealthy, Slot: &model.EnvSlot{Env: other}},
			},
			"pipeline:prod-acct/app-pipeline/Production"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := model.Chain{Ticket: model.Ticket{Key: "ABC-1", URL: ticketURL}, Flags: tt.flags}
			if got := ui.OpenTarget(c, fakePipelineURL); got != tt.want {
				t.Errorf("OpenTarget = %q, want %q", got, tt.want)
			}
		})
	}
}

// opened records the URLs the app opens, failing each with err.
type opened struct {
	urls []string
	err  error
}

func (o *opened) open(u string) error { o.urls = append(o.urls, u); return o.err }

func TestAppOpen(t *testing.T) {
	snap := fxSnapshot()
	snap.Chains[2].Ticket.URL = "" // ABC-3 has nowhere to go
	// ABC-2's approval can be made here, so its key is a/x; o still opens
	// the pipeline.
	snap.Chains[1].Slots[0].Env.ReadOnly = false
	snap.Chains[1].Slots[0].Deploy = &model.Deploy{Status: model.DeployAwaitingApproval, ApprovalToken: "tok"}
	if a := ui.RowAction(snap.Chains[1]); a != ui.ActionApprove {
		t.Fatalf("ABC-2 offers %v, want a/x", a)
	}
	o := &opened{}
	m := ui.NewApp(make(chan resolver.Snapshot), make(chan struct{}, 1), o.open).WithClock(func() time.Time { return now })
	m = update(t, update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30}), snap)

	m = press(t, m, "o", "j", "o", "j", "o", "j", "o")
	want := []string{
		"https://ci.example.com/lint/1",                   // ABC-1: the failing check's page
		resolver.PipelineURL(snap.Chains[1].Slots[0].Env), // ABC-2: the approval's pipeline
		// ABC-3: no URL, nothing opened
		"https://github.com/acme/app/pull/9", // the unlinked PR
	}
	if strings.Join(o.urls, " ") != strings.Join(want, " ") {
		t.Errorf("opened\n got %q\nwant %q", o.urls, want)
	}

	o.err = errors.New("no browser")
	m = press(t, m, "o")
	if foot := viewLines(m)[len(viewLines(m))-1]; !strings.Contains(foot, "open failed: no browser") {
		t.Errorf("a failed open shows in the footer: %q", foot)
	}
	m = press(t, m, "k")
	if foot := viewLines(m)[len(viewLines(m))-1]; strings.Contains(foot, "open failed") {
		t.Errorf("the next key clears the error: %q", foot)
	}
}

func TestAppOpenIsACommand(t *testing.T) {
	o := &opened{err: errors.New("no browser")}
	m := ui.NewApp(make(chan resolver.Snapshot), make(chan struct{}, 1), o.open).WithClock(func() time.Time { return now })
	m = update(t, update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30}), fxSnapshot())
	for _, mm := range []ui.App{m, press(t, m, "enter")} { // the list, then the detail
		o.urls = nil
		got, cmd := mm.Update(key("o"))
		if len(o.urls) != 0 {
			t.Fatalf("Update opened %q itself; the open belongs in a command", o.urls)
		}
		if cmd == nil {
			t.Fatal("o returns no command")
		}
		msg := cmd()
		if len(o.urls) != 1 {
			t.Fatalf("the command opens one page, opened %q", o.urls)
		}
		after := update(t, got.(ui.App), msg)
		if ls := viewLines(after); !strings.Contains(ls[len(ls)-1], "open failed: no browser") {
			t.Errorf("the command's failure shows in the footer: %q", ls[len(ls)-1])
		}
	}
}

func TestAppOpensOnlyWebPages(t *testing.T) {
	snap := fxSnapshot()
	snap.Chains[0].PRs[0].Failing[0].URL = "file:///etc/passwd" // ABC-1's flag target
	snap.Unlinked[0].URL = "-x"                                 // no ticket to fall back to
	snap.Chains[2].Ticket.URL = "file:///tmp/x"                 // ABC-3: the ticket is no good either
	o := &opened{}
	m := ui.NewApp(make(chan resolver.Snapshot), make(chan struct{}, 1), o.open).WithClock(func() time.Time { return now })
	m = update(t, update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30}), snap)
	m = press(t, m, "o", "j", "j", "o", "j", "o")
	// Detail: a PR whose URL is not a web page falls back to the ticket.
	snap.Chains[0].PRs[0].URL = "-x"
	snap.Chains[0].PRs[0].Checks, snap.Chains[0].PRs[0].Failing = model.ChecksPassing, nil
	m = press(t, update(t, press(t, m, "k", "k", "k"), snap), "enter", "o")
	// A failing check whose URL is not a web page falls to the PR's checks tab.
	want := []string{"https://github.com/acme/app/pull/3/checks", "https://jira.example.com/browse/ABC-1"}
	if strings.Join(o.urls, " ") != strings.Join(want, " ") {
		t.Errorf("opened\n got %q\nwant %q", o.urls, want)
	}
	if foot := viewLines(m)[len(viewLines(m))-1]; foot != "↑/↓ move  o open  r refresh  esc back  q quit" {
		t.Errorf("no error for a page that was not opened: %q", foot)
	}
}

func TestAppTinyHeight(t *testing.T) {
	snap := fxSnapshot()
	for _, m := range []ui.App{update(t, newApp(t), snap), press(t, update(t, newApp(t), snap), "enter")} {
		m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 2})
		ls := viewLines(m)
		if len(ls) != 2 || ls[0] != ui.RenderHeader(snap.Status, now, 100) {
			t.Errorf("2 rows hold the header and the footer only:\n%s", m.View())
		}
	}
}

func TestAppNilOpener(t *testing.T) {
	m := ui.NewApp(make(chan resolver.Snapshot), make(chan struct{}, 1), nil).WithClock(func() time.Time { return now })
	m = press(t, update(t, m, fxSnapshot()), "o", "enter", "o") // must not panic
	if ls := viewLines(m); strings.Contains(ls[len(ls)-1], "failed") {
		t.Errorf("a nil opener opens nothing, quietly: %q", ls[len(ls)-1])
	}
}

func TestAppUnlinkedLabelDim(t *testing.T) {
	m := update(t, newApp(t).WithStyles(terminal(env{"TERM": "xterm-256color"})), fxSnapshot())
	label := viewLines(m)[4]
	if !strings.HasPrefix(label, "\x1b[2m") || ansi.Strip(label) != "── Unlinked (1) ──" {
		t.Errorf("the Unlinked label is dimmed: %q", label)
	}
}

func TestAppSnapshotShorterThanOffset(t *testing.T) {
	long := fxSnapshot()
	long.Unlinked = nil
	for i := range 10 {
		long.Chains = append(long.Chains, model.Chain{Ticket: model.Ticket{Key: fmt.Sprintf("ABC-%d", 10+i), Title: "More"}})
	}
	m := update(t, newApp(t), tea.WindowSizeMsg{Width: 100, Height: 6}) // 4 body lines
	m = update(t, m, long)
	m = press(t, m, "j", "j", "j", "j", "j", "j", "j", "j", "j", "j", "j", "j") // scrolled to the bottom
	short := fxSnapshot()
	short.Chains, short.Unlinked = short.Chains[:2], nil
	m = update(t, m, short)
	ls := viewLines(m)
	if len(ls) != 4 || !strings.Contains(ls[1], "ABC-1") || !strings.Contains(ls[2], "ABC-2") {
		t.Errorf("a shorter snapshot scrolls back to show its rows:\n%s", m.View())
	}
	if got := cursorLine(t, m); !strings.Contains(got, "ABC-2") {
		t.Errorf("the cursor clamps to the last row: %q", got)
	}
}

func TestAppCleansUnlinkedTitle(t *testing.T) {
	snap := fxSnapshot()
	snap.Unlinked[0].Title = "Bump\x1b]52;c;aGk=\x07 deps\nnow"
	snap.Unlinked[0].Repo = "acme/\x1b[31mapp"
	m := update(t, newApp(t), snap)
	v := m.View()
	if strings.ContainsAny(v, "\x1b\x07") || len(viewLines(m)) != 7 {
		t.Errorf("the unlinked line is cleaned and stays one line: %q", v)
	}
	if !strings.Contains(v, "  acme/app#9  Bump deps now") {
		t.Errorf("cleaned text shows:\n%s", v)
	}
}

func TestAppWarningsCount(t *testing.T) {
	foot := func(warnings ...string) string {
		snap := fxSnapshot()
		snap.Warnings = warnings
		ls := viewLines(update(t, newApp(t), snap))
		return ls[len(ls)-1]
	}
	if got := foot(); strings.Contains(got, "warning") {
		t.Errorf("no warnings, no count: %q", got)
	}
	if got := foot("a"); !strings.HasSuffix(got, "  1 warning") {
		t.Errorf("one warning: %q", got)
	}
	if got := foot("a", "b"); !strings.HasSuffix(got, "  2 warnings") || ansi.StringWidth(got) > 100 {
		t.Errorf("two warnings, within the width: %q", got)
	}
}

func TestAppWarningsDimmed(t *testing.T) {
	snap := fxSnapshot()
	snap.Warnings = []string{"a", "b"}
	m := update(t, newApp(t).WithStyles(terminal(env{"TERM": "xterm-256color"})), snap)
	ls := viewLines(m)
	if foot := ls[len(ls)-1]; !strings.Contains(foot, "\x1b[2m2 warnings\x1b[0m") {
		t.Errorf("the count is dimmed: %q", foot)
	}
}

// manySnapshot has n plain chains ABC-1..ABC-n and two unlinked PRs.
func manySnapshot(n int) resolver.Snapshot {
	var snap resolver.Snapshot
	for i := 1; i <= n; i++ {
		snap.Chains = append(snap.Chains, model.Chain{Ticket: model.Ticket{Key: fmt.Sprintf("ABC-%d", i), Title: "Work"}})
	}
	snap.Unlinked = []model.PR{{Repo: "acme/app", Number: 1, Title: "One"}, {Repo: "acme/app", Number: 2, Title: "Two"}}
	return snap
}

func TestAppScrollKeepsCursorVisible(t *testing.T) {
	const height = 6 // header, 4 body lines, footer
	m := update(t, newApp(t), tea.WindowSizeMsg{Width: 100, Height: height})
	m = update(t, m, manySnapshot(10))
	check := func(want string) {
		t.Helper()
		ls := viewLines(m)
		if len(ls) != height {
			t.Fatalf("view is %d lines, want %d:\n%s", len(ls), height, m.View())
		}
		if !strings.HasPrefix(ls[0], "app-green") || ls[height-1] != footerKeys {
			t.Errorf("header and footer stay put:\n%s", m.View())
		}
		if got := cursorLine(t, m); !strings.Contains(got, want+" ") {
			t.Errorf("cursor on %q, want %s", got, want)
		}
	}
	check("ABC-1")
	for i := 2; i <= 10; i++ {
		m = press(t, m, "j")
		check(fmt.Sprintf("ABC-%d", i))
	}
	m = press(t, m, "j", "j")
	check("acme/app#2") // the last body line, past the Unlinked header
	if ls := viewLines(m); ls[height-2] != cursorLine(t, m) {
		t.Errorf("at the end the cursor is on the last body line:\n%s", m.View())
	}
	for i := 0; i < 12; i++ {
		m = press(t, m, "k")
	}
	check("ABC-1")
	if ls := viewLines(m); ls[1] != cursorLine(t, m) {
		t.Errorf("back at the top the first row shows:\n%s", m.View())
	}
}

func TestAppResize(t *testing.T) {
	snap := fxSnapshot()
	widths := func(m ui.App) []int {
		var ws []int
		for _, l := range viewLines(m) {
			ws = append(ws, ansi.StringWidth(l))
		}
		return ws
	}
	m := update(t, newApp(t), snap)
	m = update(t, m, tea.WindowSizeMsg{Width: 60, Height: 30})
	ws := widths(m)
	for i := 1; i <= 3; i++ { // the chain rows
		if ws[i] != 60 {
			t.Errorf("row %d is %d cells after resizing to 60", i, ws[i])
		}
	}
	for i, w := range ws {
		if w > 60 {
			t.Errorf("line %d is %d cells, wider than 60:\n%s", i, w, m.View())
		}
	}
	if got, want := strings.TrimRight(viewLines(m)[2], " "), strings.TrimRight(ui.RenderRow(snap.Chains[1], ui.NewLayout(snap.Chains, now, 60), now), " "); got != want {
		t.Errorf("rows use a layout for the new width\n got %q\nwant %q", got, want)
	}

	// Before any size arrives the list is 80 cells wide.
	fresh := update(t, ui.NewApp(nil, nil, noOpen(t)).WithClock(func() time.Time { return now }), snap)
	if w := widths(fresh)[1]; w != 80 {
		t.Errorf("before the first size, rows are %d cells, want 80", w)
	}
}

func TestAppWaitsForSnapshots(t *testing.T) {
	snaps := make(chan resolver.Snapshot, 2)
	m := ui.NewApp(snaps, make(chan struct{}, 1), noOpen(t)).WithClock(func() time.Time { return now })
	first, second := fxSnapshot(), manySnapshot(2)
	snaps <- first
	snaps <- second

	cmd := m.Init()
	if cmd == nil {
		t.Fatal("Init waits for the first snapshot")
	}
	msg := cmd()
	if s, ok := msg.(resolver.Snapshot); !ok || len(s.Chains) != 3 {
		t.Fatalf("Init delivers the first snapshot, got %T", msg)
	}
	next, cmd := m.Update(msg)
	if cmd == nil {
		t.Fatal("a snapshot waits for the next one")
	}
	if s, ok := cmd().(resolver.Snapshot); !ok || len(s.Chains) != 2 {
		t.Fatalf("the next wait delivers the second snapshot")
	}
	if !strings.Contains(next.View(), "ABC-1") {
		t.Errorf("the first snapshot is shown:\n%s", next.View())
	}

	close(snaps)
	if msg := m.Init()(); msg != nil {
		t.Errorf("a closed channel (the resolver stopped) delivers nothing, got %T", msg)
	}
}

func TestAppInertKeys(t *testing.T) {
	// Keys for later tasks do nothing yet, loaded or not, and enter, o, j
	// and k on an empty list are safe.
	for _, m := range []ui.App{newApp(t), update(t, newApp(t), resolver.Snapshot{}), update(t, newApp(t), fxSnapshot())} {
		before := m.View()
		if m = press(t, m, "f", "a", "x", "t"); m.View() != before {
			t.Errorf("f/a/x/t changed the view:\n%s", m.View())
		}
	}
	for _, m := range []ui.App{newApp(t), update(t, newApp(t), resolver.Snapshot{})} {
		before := m.View()
		if m = press(t, m, "enter", "j", "k", "o"); m.View() != before { // noOpen fails on any open
			t.Errorf("enter/j/k/o changed an empty list:\n%s", m.View())
		}
	}
}

func TestAppNoColor(t *testing.T) {
	m := update(t, newApp(t).WithStyles(terminal(env{"TERM": "xterm-256color", "NO_COLOR": "1"})), fxSnapshot())
	m = press(t, m, "j")
	if v := m.View(); strings.Contains(v, "\x1b[") {
		t.Errorf("NO_COLOR output has escape codes: %q", v)
	}
	if got := cursorLine(t, m); !strings.Contains(got, "ABC-2") {
		t.Errorf("under NO_COLOR the cursor shows as >: %q", got)
	}
}
