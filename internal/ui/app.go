package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/ericdahl-dev/app-green/internal/resolver"
)

// App is the top Bubble Tea model: the ticket list, fed by the resolver's
// snapshots.
type App struct {
	styles  Styles
	now     func() time.Time
	snaps   <-chan resolver.Snapshot
	refresh chan<- struct{}
	openURL func(string) error

	snap          *resolver.Snapshot // nil until the first one arrives
	cursor        int                // index into items()
	err           string             // the last open's error, until the next key
	offset        int                // the first body line shown
	width, height int
}

// NewApp builds the App. snaps is the resolver's out channel, refresh its
// refresh channel (sent without blocking), and openURL opens a page in the
// browser.
func NewApp(snaps <-chan resolver.Snapshot, refresh chan<- struct{}, openURL func(string) error) App {
	return App{styles: DefaultStyles(), now: time.Now, snaps: snaps, refresh: refresh, openURL: openURL}
}

// WithStyles is m rendering with s.
func (m App) WithStyles(s Styles) App { m.styles = s; return m }

// WithClock is m reading the time from now, for header and stage ages.
func (m App) WithClock(now func() time.Time) App { m.now = now; return m }

// waitForSnapshot delivers the resolver's next snapshot. When the resolver
// has stopped the channel is closed and it delivers nothing, so the list
// keeps its last state.
// Adapted from jira-green main.go waitForSnapshot.
func waitForSnapshot(ch <-chan resolver.Snapshot) tea.Cmd {
	return func() tea.Msg {
		snap, ok := <-ch
		if !ok {
			return nil
		}
		return snap
	}
}

func (m App) Init() tea.Cmd { return waitForSnapshot(m.snaps) }

func (m App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	m, cmd := m.update(msg)
	return m.scroll(), cmd
}

func (m App) update(msg tea.Msg) (App, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case resolver.Snapshot:
		id := m.itemID(m.cursor)
		m.snap = &msg
		m.cursor = m.find(id)
		return m, waitForSnapshot(m.snaps)
	case tea.KeyMsg:
		return m.key(msg)
	}
	return m, nil
}

// keys are the list's key bindings.
var keys = struct {
	up, down, refresh, quit, open key.Binding
}{
	up:      key.NewBinding(key.WithKeys("up", "k")),
	down:    key.NewBinding(key.WithKeys("down", "j")),
	refresh: key.NewBinding(key.WithKeys("r")),
	open:    key.NewBinding(key.WithKeys("o")),
	quit:    key.NewBinding(key.WithKeys("q", "ctrl+c")),
}

func (m App) key(msg tea.KeyMsg) (App, tea.Cmd) {
	m.err = ""
	switch {
	case key.Matches(msg, keys.quit):
		return m, tea.Quit
	case key.Matches(msg, keys.up):
		m.cursor = max(m.cursor-1, 0)
	case key.Matches(msg, keys.down):
		m.cursor = min(m.cursor+1, max(m.items()-1, 0))
	case key.Matches(msg, keys.refresh):
		select {
		case m.refresh <- struct{}{}:
		default: // a refresh is already pending
		}
	case key.Matches(msg, keys.open):
		if u := m.target(); u != "" {
			if err := m.openURL(u); err != nil {
				m.err = "open failed: " + err.Error()
			}
		}
	}
	return m, nil
}

// items is how many lines the cursor can select: every chain, then every
// unlinked PR.
func (m App) items() int {
	if m.snap == nil {
		return 0
	}
	return len(m.snap.Chains) + len(m.snap.Unlinked)
}

// itemID names item i so the cursor can find it in the next snapshot: the
// ticket key for a chain, "repo#number" (lower case) for an unlinked PR, ""
// for no item.
func (m App) itemID(i int) string {
	switch {
	case m.snap == nil || i < 0:
		return ""
	case i < len(m.snap.Chains):
		return "ticket " + m.snap.Chains[i].Ticket.Key
	case i < m.items():
		p := m.snap.Unlinked[i-len(m.snap.Chains)]
		return fmt.Sprintf("pr %s#%d", strings.ToLower(p.Repo), p.Number)
	}
	return ""
}

// find is the index of the item named id, or else the cursor clamped to the
// items there are.
func (m App) find(id string) int {
	for i := range m.items() {
		if id != "" && m.itemID(i) == id {
			return i
		}
	}
	return max(min(m.cursor, m.items()-1), 0)
}

// target is the page o opens for the item under the cursor: OpenTarget for
// a chain, the PR for an unlinked PR.
func (m App) target() string {
	switch i := m.cursor; {
	case m.snap == nil:
		return ""
	case i < len(m.snap.Chains):
		return OpenTarget(m.snap.Chains[i], resolver.PipelineURL)
	case i < m.items():
		return m.snap.Unlinked[i-len(m.snap.Chains)].URL
	}
	return ""
}
