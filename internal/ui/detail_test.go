package ui_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ericdahl-dev/app-green/internal/model"
	"github.com/ericdahl-dev/app-green/internal/resolver"
	"github.com/ericdahl-dev/app-green/internal/ui"
)

// detailKeys is the detail screen's key line.
const detailKeys = "↑/↓ move  o open  r refresh  esc back  q quit"

var (
	stageTest = model.Env{Account: "stage-acct", Region: "us-east-1", Pipeline: "app-pipeline", Stage: "Test", Order: 0}
	stageProd = model.Env{Account: "stage-acct", Region: "us-east-1", Pipeline: "app-pipeline", Stage: "Production", Order: 1, Prod: true}
	prodTest  = model.Env{Account: "prod-acct", Region: "us-west-2", Pipeline: "app-pipeline", Stage: "Test", Order: 2}
	prodProd  = model.Env{Account: "prod-acct", Region: "us-west-2", Pipeline: "app-pipeline", Stage: "Production", Order: 3, Prod: true}
)

// fxDetail is ABC-19, the design's detail example: two PRs, four envs that
// apply and one that does not, and two flags.
func fxDetail() model.Chain {
	day := 24 * time.Hour
	c := model.Chain{
		Ticket: model.Ticket{Key: "ABC-19", Title: "Drop the form-level opt-outs", Status: "Code Review",
			URL: "https://jira.example.com/browse/ABC-19"},
		PRs: []model.PR{
			{Repo: "acme/app", Number: 330, State: model.PRMerged, MergedAt: now.Add(-2 * day),
				URL: "https://github.com/acme/app/pull/330", Checks: model.ChecksPassing, Review: model.ReviewApproved},
			{Repo: "acme/app", Number: 331, State: model.PROpen, IsDraft: true,
				URL: "https://github.com/acme/app/pull/331", Checks: model.ChecksFailing,
				Failing: []model.Check{{Name: "rspec", RunID: 7}, {Name: "CodeQL", CodeScanning: true, URL: "https://github.com/acme/app/security/1"}},
				Review:  model.ReviewChangesRequested},
		},
		Slots: []model.EnvSlot{
			{Env: stageTest, State: model.SlotDeployed, SHA: "aaaa1111deadbeef", At: now.Add(-day), Applies: true,
				Health: model.Health{Known: true, Desired: 2, Healthy: 2}},
			{Env: stageProd, State: model.SlotAwaitingApproval, At: now.Add(-day), Applies: true},
			{Env: model.Env{Account: "other-acct", Pipeline: "web-pipeline", Stage: "Test"}, State: model.SlotNotYet},
			{Env: prodTest, State: model.SlotFailed, SHA: "bbbb2222", At: now.Add(-3 * time.Hour), Applies: true},
			{Env: prodProd, State: model.SlotNotYet, Applies: true},
		},
		Stage: model.StageInTest,
	}
	c.Flags = []model.Flag{
		{Level: model.Red, Kind: model.FlagPipelineFailed, Reason: "prod-acct Test failed", Slot: &c.Slots[3]},
		{Level: model.Yellow, Kind: model.FlagAwaitingApproval, Reason: "stage-acct Production awaiting approval", Slot: &c.Slots[1]},
	}
	return c
}

// fxDetailSnapshot is fxSnapshot with ABC-19 second.
func fxDetailSnapshot() resolver.Snapshot {
	snap := fxSnapshot()
	snap.Chains = append([]model.Chain{snap.Chains[0], fxDetail()}, snap.Chains[1:]...)
	return snap
}

// inDetail is the App on fxDetailSnapshot with ABC-19's detail open.
func inDetail(t *testing.T) ui.App {
	t.Helper()
	return press(t, update(t, newApp(t), fxDetailSnapshot()), "j", "enter")
}

// trimmed is the view's lines with trailing spaces cut.
func trimmed(m ui.App) []string {
	ls := viewLines(m)
	for i, l := range ls {
		ls[i] = strings.TrimRight(l, " ")
	}
	return ls
}

func TestDetailEnvStates(t *testing.T) {
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	tests := []struct {
		name string
		slot model.EnvSlot
		want string
	}{
		{"deployed", model.EnvSlot{State: model.SlotDeployed, SHA: "aaaa1111", At: ago(time.Hour),
			Health: model.Health{Known: true, Desired: 3, Healthy: 1}}, "✓ deployed 1h ago (aaaa111)  ECS 1/3"},
		{"deployed, health unknown", model.EnvSlot{State: model.SlotDeployed, SHA: "aaaa1111", At: ago(time.Hour)},
			"✓ deployed 1h ago (aaaa111)"},
		{"deployed, no time or SHA", model.EnvSlot{State: model.SlotDeployed}, "✓ deployed"},
		{"awaiting approval", model.EnvSlot{State: model.SlotAwaitingApproval, At: ago(5 * time.Minute)},
			"‖ awaiting approval since 5m"},
		{"awaiting approval, no time", model.EnvSlot{State: model.SlotAwaitingApproval}, "‖ awaiting approval"},
		{"in progress", model.EnvSlot{State: model.SlotInProgress, SHA: "cccc3333", At: ago(2 * time.Minute)},
			"in progress since 2m (cccc333)"},
		{"failed", model.EnvSlot{State: model.SlotFailed, SHA: "bbbb2222", At: ago(3 * time.Hour)}, "✗ failed 3h ago (bbbb222)"},
		{"rejected", model.EnvSlot{State: model.SlotFailed, SHA: "bbbb2222", At: ago(3 * time.Hour),
			Deploy: &model.Deploy{Status: model.DeployRejected}}, "✗ rejected or expired 3h ago (bbbb222)"},
		{"rolled back", model.EnvSlot{State: model.SlotRolledBack, SHA: "dddd4444", At: ago(48 * time.Hour)},
			"✗ rolled back 2d ago (now dddd444)"}, // SHA is the commit that replaced it
		{"unknown", model.EnvSlot{State: model.SlotUnknown}, "deploy unknown"},
		{"not yet", model.EnvSlot{State: model.SlotNotYet}, "not yet"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := model.Chain{Ticket: model.Ticket{Key: "ABC-5", Title: "One env"}}
			tt.slot.Env, tt.slot.Applies = stageTest, true
			c.Slots = []model.EnvSlot{tt.slot}
			snap := resolver.Snapshot{Chains: []model.Chain{c}, At: now}
			ls := trimmed(press(t, update(t, newApp(t), snap), "enter"))
			if want := " >stage-acct  Test  " + tt.want; ls[2] != want {
				t.Errorf("\n got %q\nwant %q", ls[2], want)
			}
		})
	}
}

func TestDetailView(t *testing.T) {
	snap := fxDetailSnapshot()
	want := []string{
		ui.RenderHeader(snap.Status, now, 100),
		"ABC-19  Drop the form-level opt-outs" + strings.Repeat(" ", 100-36-17) + "Jira: Code Review",
		" >PR #330  acme/app  merged 2d ago  ✓ checks  ✓ approved",
		"  PR #331  acme/app  open  ✗ rspec, CodeQL  changes requested  draft",
		"  stage-acct  Test        ✓ deployed 1d ago (aaaa111)  ECS 2/2",
		"  stage-acct  Production  ‖ awaiting approval since 1d",
		"  prod-acct   Test        ✗ failed 3h ago (bbbb222)",
		"  prod-acct   Production  not yet",
		"Flags",
		"  ● prod-acct Test failed",
		"  ◐ stage-acct Production awaiting approval",
		detailKeys,
	}
	// other-acct's env does not apply, so it is left out.
	got := trimmed(inDetail(t))
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("view\n got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestDetailStale(t *testing.T) {
	snap := fxDetailSnapshot()
	snap.Chains[1].Stale, snap.Chains[1].StaleReason = true, "acme/app not refreshed since 11:58:00"
	ls := trimmed(press(t, update(t, newApp(t), snap), "j", "enter"))
	if want := "~ stale: acme/app not refreshed since 11:58:00"; ls[2] != want {
		t.Errorf("stale line\n got %q\nwant %q", ls[2], want)
	}
	if !strings.HasPrefix(ls[3], " >PR #330") {
		t.Errorf("the PRs follow the stale line: %q", ls[3])
	}

	styles := terminal(env{"TERM": "xterm-256color"})
	colored := viewLines(press(t, update(t, newApp(t).WithStyles(styles), snap), "j", "enter"))
	if !strings.HasPrefix(colored[2], "\x1b[2m") {
		t.Errorf("the stale line is dimmed: %q", colored[2])
	}
}

func TestDetailBack(t *testing.T) {
	for _, k := range []string{"esc", "backspace"} {
		m := press(t, inDetail(t), "j", "j", k) // moving in detail leaves the list cursor alone
		if got := cursorLine(t, m); !strings.Contains(got, "ABC-19") {
			t.Errorf("%s returns to the list on ABC-19: %q", k, got)
		}
		if ls := viewLines(m); ls[len(ls)-1] != footerKeys {
			t.Errorf("%s shows the list footer: %q", k, ls[len(ls)-1])
		}
	}
	for _, k := range []string{"q", "ctrl+c"} {
		_, cmd := inDetail(t).Update(key(k))
		if cmd == nil {
			t.Fatalf("%s in detail returns no command", k)
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Errorf("%s in detail does not quit", k)
		}
	}
}

func TestDetailNotForUnlinked(t *testing.T) {
	m := press(t, update(t, newApp(t), fxDetailSnapshot()), "j", "j", "j", "j") // the unlinked PR
	before := m.View()
	if m = press(t, m, "enter"); m.View() != before {
		t.Errorf("enter on an unlinked PR changes nothing:\n%s", m.View())
	}
}

// detailWith is the App, opening URLs with o, on fxDetailSnapshot with
// ABC-19's detail open.
func detailWith(t *testing.T, o *opened, snap resolver.Snapshot) ui.App {
	t.Helper()
	m := ui.NewApp(make(chan resolver.Snapshot), make(chan struct{}, 1), o.open).WithClock(func() time.Time { return now })
	m = update(t, update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30}), snap)
	return press(t, m, "j", "enter")
}

func TestDetailSelect(t *testing.T) {
	m := inDetail(t)
	steps := []struct {
		key  string
		want string
	}{
		{"", "PR #330"},
		{"up", "PR #330"}, // stays at the top
		{"j", "PR #331"},
		{"down", "stage-acct  Test"},
		{"j", "stage-acct  Production"},
		{"j", "prod-acct   Test"},
		{"j", "prod-acct   Production"},
		{"j", "prod-acct   Production"}, // stays at the bottom: flags are not selectable
		{"k", "prod-acct   Test"},
	}
	for _, s := range steps {
		if s.key != "" {
			m = press(t, m, s.key)
		}
		if got := cursorLine(t, m); !strings.Contains(got, s.want) {
			t.Errorf("after %q the selection is %q, want %s", s.key, got, s.want)
		}
	}
}

func TestDetailOpen(t *testing.T) {
	o := &opened{}
	snap := fxDetailSnapshot()
	m := detailWith(t, o, snap)
	press(t, m, "o", "j", "o", "j", "o", "j", "j", "o")
	want := []string{
		"https://github.com/acme/app/pull/330",   // a PR: its page
		"https://github.com/acme/app/security/1", // failing checks: the first with a page
		resolver.PipelineURL(stageTest),          // an env: its pipeline
		resolver.PipelineURL(prodTest),
	}
	if strings.Join(o.urls, " ") != strings.Join(want, " ") {
		t.Errorf("opened\n got %q\nwant %q", o.urls, want)
	}

	// Failing checks with no page of their own: the checks tab.
	o = &opened{}
	snap.Chains[1].PRs[1].Failing[1].URL = ""
	press(t, detailWith(t, o, snap), "j", "o")
	if want := "https://github.com/acme/app/pull/331/checks"; len(o.urls) != 1 || o.urls[0] != want {
		t.Errorf("opened %q, want %q", o.urls, want)
	}

	// Nothing selectable: the ticket.
	o = &opened{}
	snap.Chains[1].PRs, snap.Chains[1].Slots = nil, nil
	press(t, detailWith(t, o, snap), "o")
	if want := "https://jira.example.com/browse/ABC-19"; len(o.urls) != 1 || o.urls[0] != want {
		t.Errorf("opened %q, want %q", o.urls, want)
	}

	// A failed open shows in the footer.
	o = &opened{err: errors.New("no browser")}
	m = press(t, detailWith(t, o, fxDetailSnapshot()), "o")
	if ls := viewLines(m); !strings.Contains(ls[len(ls)-1], "open failed: no browser") {
		t.Errorf("a failed open shows in the footer: %q", ls[len(ls)-1])
	}
}

func TestDetailRefresh(t *testing.T) {
	m := press(t, inDetail(t), "j", "j", "j") // on stage-acct Production

	// ABC-19 moves to the top, its approval goes through, and a new PR
	// appears above the selection.
	next := fxDetailSnapshot()
	c := next.Chains[1]
	c.Slots[1].State, c.Slots[1].SHA, c.Slots[1].At = model.SlotDeployed, "aaaa1111", now
	c.PRs = append([]model.PR{{Repo: "acme/app", Number: 340, State: model.PROpen}}, c.PRs...)
	next.Chains = append([]model.Chain{c, next.Chains[0]}, next.Chains[2:]...)
	m = update(t, m, next)

	ls := trimmed(m)
	if !strings.HasPrefix(ls[1], "ABC-19") || ls[len(ls)-1] != detailKeys {
		t.Fatalf("still on ABC-19's detail:\n%s", m.View())
	}
	if got := cursorLine(t, m); !strings.Contains(got, "stage-acct  Production  ✓ deployed 0s ago (aaaa111)") {
		t.Errorf("the selection follows its env and shows the new state: %q", got)
	}
	if !strings.Contains(m.View(), "PR #340") {
		t.Errorf("the new PR shows:\n%s", m.View())
	}
	m = press(t, m, "esc")
	if got := cursorLine(t, m); !strings.Contains(got, "ABC-19") {
		t.Errorf("back on the list, the cursor followed ABC-19: %q", got)
	}
}

func TestDetailTicketGone(t *testing.T) {
	m := update(t, inDetail(t), fxSnapshot()) // without ABC-19
	ls := viewLines(m)
	if !strings.Contains(m.View(), "ABC-1  ") || strings.Contains(m.View(), "Jira: Code Review") {
		t.Fatalf("back on the list:\n%s", m.View())
	}
	if want := "  ABC-19 is no longer in the list"; !strings.HasSuffix(ls[len(ls)-1], want) {
		t.Errorf("the footer says why\n got %q\nwant suffix %q", ls[len(ls)-1], want)
	}
	if got := cursorLine(t, m); !strings.Contains(got, "ABC-2") {
		t.Errorf("the cursor stays at its index: %q", got)
	}
	m = press(t, m, "j")
	if ls := viewLines(m); strings.Contains(ls[len(ls)-1], "no longer") {
		t.Errorf("the next key clears the note: %q", ls[len(ls)-1])
	}
}

func TestDetailScroll(t *testing.T) {
	// 8 rows: the header, 6 body lines and the footer, for 10 body lines.
	m := update(t, inDetail(t), tea.WindowSizeMsg{Width: 100, Height: 8})
	body := func(m ui.App) string {
		ls := trimmed(m)
		if len(ls) != 8 {
			t.Fatalf("the view is %d lines, want 8:\n%s", len(ls), m.View())
		}
		if ls[0] != ui.RenderHeader(fxDetailSnapshot().Status, now, 100) || ls[7] != detailKeys {
			t.Errorf("the header and footer stay put:\n%s", m.View())
		}
		return strings.Join(ls[1:7], "\n")
	}
	first := func(m ui.App) string { return trimmed(m)[1] }

	if !strings.HasPrefix(first(m), "ABC-19") {
		t.Errorf("opens at the top:\n%s", m.View())
	}
	m = press(t, m, "j", "j", "j", "j", "j") // to prod-acct Production, the last item
	if got := cursorLine(t, m); !strings.Contains(got, "prod-acct   Production") {
		t.Fatalf("selection: %q", got)
	}
	if !strings.HasPrefix(first(m), " >PR #330") && !strings.HasPrefix(first(m), "  PR #330") {
		t.Errorf("scrolled just enough to show the selection:\n%s", body(m))
	}
	m = press(t, m, "j", "j", "j", "j", "j") // past the last item: the flags scroll in
	if b := body(m); !strings.HasSuffix(b, "◐ stage-acct Production awaiting approval") {
		t.Errorf("the last flag can be reached:\n%s", b)
	}
	cursorLine(t, m) // the selection is still on screen
	m = press(t, m, "k", "k", "k", "k", "k")
	if got := cursorLine(t, m); !strings.Contains(got, "PR #330") || !strings.HasPrefix(first(m), "ABC-19") {
		t.Errorf("back at the first item the ticket line shows:\n%s", body(m))
	}
}

func TestDetailCleansText(t *testing.T) {
	const evil = "\x1b]52;c;aGk=\x07x\ny\x9b"
	snap := fxDetailSnapshot()
	c := &snap.Chains[1]
	c.Ticket.Title, c.Ticket.Status = "Title"+evil, "Review"+evil
	c.Stale, c.StaleReason = true, "acme/app"+evil
	c.PRs[0].Repo = "acme/app" + evil
	c.PRs[1].Failing[0].Name = "rspec" + evil
	c.Slots[0].Env.Account, c.Slots[0].Env.Stage, c.Slots[0].SHA = "stage-acct"+evil, "Test"+evil, "\x1b[31maaaa1111"
	c.Flags[0].Reason = "failed" + evil
	m := press(t, update(t, newApp(t), snap), "j", "enter")
	v := m.View()
	if strings.ContainsAny(v, "\x1b\x07") || strings.Contains(v, "\x9b") {
		t.Errorf("escape sequences and controls are stripped: %q", v)
	}
	if n := len(viewLines(m)); n != 13 {
		t.Errorf("newlines in the data do not add lines: %d lines\n%s", n, v)
	}
	for _, want := range []string{"Titlex y", "Jira: Reviewx y", "~ stale: acme/appx y", "acme/appx y", "rspecx y",
		"stage-acctx y  Testx y", "✓ deployed 1d ago (aaaa111)", "● failedx y"} {
		if !strings.Contains(v, want) {
			t.Errorf("missing %q in\n%s", want, v)
		}
	}
}

func TestDetailRefreshKey(t *testing.T) {
	refresh := make(chan struct{}, 1)
	m := ui.NewApp(make(chan resolver.Snapshot), refresh, noOpen(t)).WithClock(func() time.Time { return now })
	m = press(t, update(t, m, fxDetailSnapshot()), "j", "enter", "r")
	if len(refresh) != 1 {
		t.Fatalf("r on the detail screen sends on the refresh channel")
	}
	m = press(t, m, "r") // the buffer is full: this must not block
	if ls := trimmed(m); !strings.HasPrefix(ls[1], "ABC-19") {
		t.Errorf("r stays on the detail screen:\n%s", m.View())
	}
}

func TestDetailOpenHiddenSelection(t *testing.T) {
	snap := fxDetailSnapshot()
	c := &snap.Chains[1]
	for range 4 {
		c.Flags = append(c.Flags, model.Flag{Level: model.Yellow, Kind: model.FlagStaleReview, Reason: "more"})
	}
	o := &opened{}
	m := update(t, detailWith(t, o, snap), tea.WindowSizeMsg{Width: 100, Height: 8})
	m = press(t, m, "j", "j", "j", "j", "j") // the last item, still on screen
	m = press(t, m, "o")
	m = press(t, m, "j", "j", "j", "j", "j", "j") // scroll on until it is hidden
	for _, l := range viewLines(m) {
		if isCursor(l) {
			t.Fatalf("the selection should be scrolled out:\n%s", m.View())
		}
	}
	press(t, m, "o")
	want := []string{resolver.PipelineURL(prodProd), "https://jira.example.com/browse/ABC-19"}
	if strings.Join(o.urls, " ") != strings.Join(want, " ") {
		t.Errorf("opened\n got %q\nwant %q", o.urls, want)
	}
}

func TestDetailResizeKeepsSelectionOnScreen(t *testing.T) {
	m := update(t, inDetail(t), tea.WindowSizeMsg{Width: 100, Height: 30})
	m = press(t, m, "j", "j", "j", "j", "j") // the last item
	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 8})
	if got := cursorLine(t, m); !strings.Contains(got, "prod-acct   Production") {
		t.Errorf("after the window shrinks the selection is still on screen: %q", got)
	}
}

func TestDetailPollKeepsScrollPosition(t *testing.T) {
	snap := fxDetailSnapshot()
	c := &snap.Chains[1]
	for range 4 {
		c.Flags = append(c.Flags, model.Flag{Level: model.Yellow, Kind: model.FlagStaleReview, Reason: "more"})
	}
	m := update(t, press(t, update(t, newApp(t), snap), "j", "enter"), tea.WindowSizeMsg{Width: 100, Height: 8})
	m = press(t, m, "j", "j", "j", "j", "j", "j", "j", "j", "j", "j", "j", "j", "j") // last item, then scroll past it
	for _, l := range viewLines(m) {
		if isCursor(l) {
			t.Fatalf("the selection should be scrolled out:\n%s", m.View())
		}
	}
	before := trimmed(m)[1]
	m = update(t, m, snap) // a poll with nothing new
	if got := trimmed(m)[1]; got != before {
		t.Errorf("a poll moves the scrolled window\n got %q\nwant %q", got, before)
	}
}

func TestDetailOpenFailingCheckWithoutWebPage(t *testing.T) {
	// A check whose URL is not a web page falls to the checks tab, not the ticket.
	o := &opened{}
	snap := fxDetailSnapshot()
	snap.Chains[1].PRs[1].Failing[1].URL = "file:///etc/passwd"
	press(t, detailWith(t, o, snap), "j", "o")
	if want := "https://github.com/acme/app/pull/331/checks"; len(o.urls) != 1 || o.urls[0] != want {
		t.Errorf("opened %q, want %q", o.urls, want)
	}
}

func TestOpenPassesTheParsedURL(t *testing.T) {
	o := &opened{}
	snap := fxDetailSnapshot()
	snap.Chains[1].PRs[0].URL = "https://github.com/acme/app/pull/330é"
	press(t, detailWith(t, o, snap), "o")
	if want := "https://github.com/acme/app/pull/330%C3%A9"; len(o.urls) != 1 || o.urls[0] != want {
		t.Errorf("opened %q, want the URL as parsed and re-encoded %q", o.urls, want)
	}
}

func TestDetailPipelineOnlyWhenAmbiguous(t *testing.T) {
	env := func(pipeline string) model.Env {
		return model.Env{Account: "stage-acct", Pipeline: pipeline, Stage: "Test"}
	}
	view := func(slots ...model.EnvSlot) []string {
		c := model.Chain{Ticket: model.Ticket{Key: "ABC-5", Title: "Two pipelines"}, Slots: slots}
		snap := resolver.Snapshot{Chains: []model.Chain{c}, At: now}
		return trimmed(press(t, update(t, newApp(t), snap), "enter"))
	}
	a := model.EnvSlot{Env: env("app-pipeline"), State: model.SlotNotYet, Applies: true}
	b := model.EnvSlot{Env: env("web-pipeline"), State: model.SlotNotYet, Applies: true}

	ls := view(a, b) // the same account and stage in two pipelines
	if want := " >stage-acct  Test  app-pipeline  not yet"; ls[2] != want {
		t.Errorf("\n got %q\nwant %q", ls[2], want)
	}
	if want := "  stage-acct  Test  web-pipeline  not yet"; ls[3] != want {
		t.Errorf("\n got %q\nwant %q", ls[3], want)
	}
	if ls := view(a); strings.Contains(ls[2], "pipeline") {
		t.Errorf("an unambiguous env line has no pipeline: %q", ls[2])
	}
}

func TestDetailOpenSkipsURLsWithUnsafeBytes(t *testing.T) {
	for _, u := range []string{"https://ci.example.com/x?q=$(id) `id`", "https://ci.example.com/a b", "https://ci.example.com/\"x"} {
		o := &opened{}
		snap := fxDetailSnapshot()
		snap.Chains[1].PRs[1].Failing[1].URL = u
		press(t, detailWith(t, o, snap), "j", "o")
		if want := "https://github.com/acme/app/pull/331/checks"; len(o.urls) != 1 || o.urls[0] != want {
			t.Errorf("%q: opened %q, want %q", u, o.urls, want)
		}
	}
}
