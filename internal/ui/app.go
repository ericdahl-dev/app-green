package ui

import (
	"fmt"
	"net/url"
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

	detail  string // the ticket key whose detail screen is open, "" for the list
	dsel    int    // the selected item of the detail screen
	doffset int    // the first detail line shown
	note    string // why the detail screen closed, until the next key
}

// NewApp builds the App. snaps is the resolver's out channel, refresh its
// refresh channel (sent without blocking), and openURL opens a page in the
// browser (nil opens nothing).
func NewApp(snaps <-chan resolver.Snapshot, refresh chan<- struct{}, openURL func(string) error) App {
	if openURL == nil {
		openURL = func(string) error { return nil }
	}
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
		visible := m.detail != "" && m.selectionVisible()
		m.width, m.height = msg.Width, msg.Height
		if m.detail != "" {
			m = m.followSelection(visible)
		}
	case resolver.Snapshot:
		id := m.itemID(m.cursor)
		before, _ := m.chain(m.detail)
		visible := m.detail != "" && m.selectionVisible()
		m.snap = &msg
		m.cursor = m.find(id)
		if m.detail != "" {
			m = m.refreshDetail(before, visible)
		}
		return m, waitForSnapshot(m.snaps)
	case openFailedMsg:
		m.err = "open failed: " + msg.err.Error()
	case tea.KeyMsg:
		return m.key(msg)
	}
	return m, nil
}

// keys are the list's key bindings.
var keys = struct {
	up, down, refresh, quit, open, enter, back key.Binding
}{
	up:      key.NewBinding(key.WithKeys("up", "k")),
	down:    key.NewBinding(key.WithKeys("down", "j")),
	refresh: key.NewBinding(key.WithKeys("r")),
	open:    key.NewBinding(key.WithKeys("o")),
	enter:   key.NewBinding(key.WithKeys("enter")),
	back:    key.NewBinding(key.WithKeys("esc", "backspace")),
	quit:    key.NewBinding(key.WithKeys("q", "ctrl+c")),
}

func (m App) key(msg tea.KeyMsg) (App, tea.Cmd) {
	m.err, m.note = "", ""
	if m.detail != "" {
		return m.detailKey(msg)
	}
	switch {
	case key.Matches(msg, keys.quit):
		return m, tea.Quit
	case key.Matches(msg, keys.up):
		m.cursor = max(m.cursor-1, 0)
	case key.Matches(msg, keys.down):
		m.cursor = min(m.cursor+1, max(m.items()-1, 0))
	case key.Matches(msg, keys.refresh):
		m.requestRefresh()
	case key.Matches(msg, keys.enter):
		if m.snap != nil && m.cursor < len(m.snap.Chains) {
			m.detail, m.dsel, m.doffset = m.snap.Chains[m.cursor].Ticket.Key, 0, 0
		}
	case key.Matches(msg, keys.open):
		if m.items() > 0 { // with nothing selected there is nothing to say
			return m.open(m.target(), m.cursorTicketURL())
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

// WebPage parses u and reports whether it is safe to hand to a browser
// launcher: an absolute http or https URL with a host and none of the bytes
// a sloppy launcher could mishandle (space and other controls, DEL, quote,
// backtick, <, > and backslash). Anything else (a file: URL, or "-x" that a
// launcher could take for a flag) is never opened: the URLs come from Jira,
// GitHub, AWS and third-party CI.
func WebPage(u string) (*url.URL, bool) {
	if strings.ContainsFunc(u, func(r rune) bool { return r <= ' ' || r == 0x7f || strings.ContainsRune("\"`<>\\", r) }) {
		return nil, false
	}
	p, err := url.Parse(u)
	if err != nil || (p.Scheme != "http" && p.Scheme != "https") || p.Host == "" {
		return nil, false
	}
	return p, true
}

// cursorTicketURL is the ticket URL of the chain under the cursor, "" on an
// unlinked PR or with no snapshot.
func (m App) cursorTicketURL() string {
	if m.snap == nil || m.cursor >= len(m.snap.Chains) {
		return ""
	}
	return m.snap.Chains[m.cursor].Ticket.URL
}

// requestRefresh asks the resolver to poll now, without blocking.
func (m App) requestRefresh() {
	select {
	case m.refresh <- struct{}{}:
	default: // a refresh is already pending
	}
}

// openFailedMsg reports that the browser could not open a page.
// Adapted from jira-green internal/ui/dashboard.go.
type openFailedMsg struct{ err error }

// open is a command that opens the first of urls that is a web page (see
// WebPage) in the browser, reporting a failure as openFailedMsg. When none
// is, there is no command and the footer says "nothing to open". Update never
// opens a page itself.
func (m App) open(urls ...string) (App, tea.Cmd) {
	var u string
	for _, c := range urls {
		if p, ok := WebPage(c); ok {
			u = p.String() // what was checked is what is opened
			break
		}
	}
	if u == "" {
		m.err = "nothing to open"
		return m, nil
	}
	openURL := m.openURL
	return m, func() tea.Msg {
		if err := openURL(u); err != nil {
			return openFailedMsg{err}
		}
		return nil
	}
}
