package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/ericdahl-dev/app-green/internal/model"
	"github.com/ericdahl-dev/app-green/internal/resolver"
)

// detailKeys is the key line at the bottom of the detail screen.
const detailKeys = "↑/↓ move  o open  r refresh  esc back  q quit"

// chain is the chain whose ticket key is key, and whether there is one.
func (m App) chain(key string) (model.Chain, bool) {
	if m.snap != nil {
		for _, c := range m.snap.Chains {
			if c.Ticket.Key == key {
				return c, true
			}
		}
	}
	return model.Chain{}, false
}

// detailKey handles a key on the detail screen. Keys it does not know do
// nothing.
func (m App) detailKey(msg tea.KeyMsg) (App, tea.Cmd) {
	switch {
	case key.Matches(msg, keys.quit):
		return m, tea.Quit
	case key.Matches(msg, keys.back):
		m.detail = ""
	case key.Matches(msg, keys.refresh):
		m.requestRefresh()
	case key.Matches(msg, keys.up):
		// Above the first item (or with none) the window scrolls instead.
		if m.dsel == 0 {
			m.doffset--
		} else {
			m.dsel--
			m = m.showSelection()
		}
	case key.Matches(msg, keys.down):
		// Past the last item (or with none) the window scrolls instead, so
		// the flags below can be read on a short screen.
		c, _ := m.chain(m.detail)
		if m.dsel >= len(detailItems(c))-1 {
			m.doffset++
		} else {
			m.dsel++
			m = m.showSelection()
		}
	case key.Matches(msg, keys.open):
		c, _ := m.chain(m.detail)
		sel := m.dsel
		if !m.selectionVisible() {
			sel = -1 // o acts only on a selection you can see: else the ticket
		}
		return m, m.open(detailTarget(c, sel), c.Ticket.URL)
	}
	return m, nil
}

// refreshDetail keeps the detail screen on its ticket after a new snapshot,
// with the selection on the same PR or env as in before, the ticket's chain
// in the last snapshot; when that item is gone the selection stays at its
// index, clamped. When the ticket is gone it returns to the list with a
// note saying so. The window follows the selection only when it was on screen
// (visible) and no longer is; otherwise a poll leaves the scroll alone.
func (m App) refreshDetail(before model.Chain, visible bool) App {
	c, ok := m.chain(m.detail)
	if !ok {
		m.note = m.detail + " is no longer in the list"
		m.detail = ""
		return m
	}
	items := detailItems(c)
	if old := detailItems(before); m.dsel < len(old) {
		id := old[m.dsel].id()
		for i, it := range items {
			if it.id() == id {
				m.dsel = i
				return m.followSelection(visible)
			}
		}
	}
	m.dsel = max(min(m.dsel, len(items)-1), 0)
	return m.followSelection(visible)
}

// followSelection scrolls to the selection only if it was on screen
// (visible) before the change and no longer is. A selection the user
// scrolled away from stays where it is.
func (m App) followSelection(visible bool) App {
	if visible && !m.selectionVisible() {
		return m.showSelection()
	}
	return m
}

// id names the item so the selection can find it in the next snapshot:
// "pr repo#number" (repo in lower case) or "env <Env.ID()>".
func (it detailItem) id() string {
	if it.slot != nil {
		return "env " + it.slot.Env.ID()
	}
	return fmt.Sprintf("pr %s#%d", strings.ToLower(it.pr.Repo), it.pr.Number)
}

// detailItem is one selectable line of the detail screen: a PR or an env
// slot.
type detailItem struct {
	pr   *model.PR
	slot *model.EnvSlot
}

// detailItems is c's selectable lines: its PRs, then its applying slots.
func detailItems(c model.Chain) []detailItem {
	var out []detailItem
	for i := range c.PRs {
		out = append(out, detailItem{pr: &c.PRs[i]})
	}
	for i := range c.Slots {
		if c.Slots[i].Applies {
			out = append(out, detailItem{slot: &c.Slots[i]})
		}
	}
	return out
}

// detailTarget is the page o opens on item i of c's detail: a PR's failing
// checks (checksTarget) or else the PR, an env's pipeline console. With no
// item, or an item whose page is unknown, it is the ticket.
func detailTarget(c model.Chain, i int) string {
	items := detailItems(c)
	var u string
	if i >= 0 && i < len(items) {
		switch it := items[i]; {
		case it.slot != nil:
			u = resolver.PipelineURL(it.slot.Env)
		case it.pr.Checks == model.ChecksFailing || len(it.pr.Failing) > 0:
			u = checksTarget(*it.pr)
		default:
			u = it.pr.URL
		}
	}
	if u == "" {
		return c.Ticket.URL
	}
	return u
}

// detailView is the header, the detail of the open ticket, and the footer.
// When the detail is taller than the screen it scrolls; the header and
// footer stay put.
func (m App) detailView(width int) string {
	body := m.detailBody(width)
	if budget := m.bodyHeight(); budget >= 0 && len(body) > budget {
		body = body[m.doffset:min(m.doffset+budget, len(body))]
	}
	lines := append([]string{m.styles.Header(m.snap.Status, m.now(), width)}, body...)
	return strings.Join(append(lines, m.footer(detailKeys, width)), "\n")
}

// detailBody is the detail screen's lines, each width cells: the ticket
// line, the stale line, one line per PR and per applying env, then the flags.
func (m App) detailBody(width int) []string {
	c, _ := m.chain(m.detail)
	now := m.now()
	body := []string{ticketLine(c, width)}
	if c.Stale {
		body = append(body, m.styles.dim.Render(fit("~ stale: "+clean(c.StaleReason), width)))
	}
	items := detailItems(c)
	acctW, stageW := 0, 0
	for _, it := range items {
		if it.slot != nil {
			acctW = max(acctW, ansi.StringWidth(clean(it.slot.Env.Account)))
			stageW = max(stageW, ansi.StringWidth(clean(it.slot.Env.Stage)))
		}
	}
	for i, it := range items {
		var line string
		if it.slot != nil {
			line = "  " + fit(clean(it.slot.Env.Account), acctW) + "  " + fit(clean(it.slot.Env.Stage), stageW) + "  " + slotText(*it.slot, now)
		} else {
			line = "  " + prLine(*it.pr, now)
		}
		line = fit(line, width)
		if i == m.dsel {
			line = m.styles.selected(line)
		}
		body = append(body, line)
	}
	if len(c.Flags) > 0 {
		body = append(body, fit("Flags", width))
		for _, f := range c.Flags {
			body = append(body, fit("  "+m.styles.marker(f.Level)+" "+clean(f.Reason), width))
		}
	}
	return body
}

// showSelection scrolls the detail just enough to show the selected line,
// and to the top on the first item so the ticket line shows.
func (m App) showSelection() App {
	budget := m.bodyHeight()
	sel := m.selectedLine()
	switch {
	case budget < 0 || sel < 0:
	case m.dsel == 0:
		m.doffset = max(sel-budget+1, 0)
	case sel < m.doffset:
		m.doffset = sel
	case sel >= m.doffset+budget:
		m.doffset = sel - budget + 1
	}
	return m
}

// selectedLine is the index of the selected item's line in the detail body
// (see detailBody), -1 with nothing to select.
func (m App) selectedLine() int {
	c, _ := m.chain(m.detail)
	if m.dsel >= len(detailItems(c)) {
		return -1
	}
	line := 1 + m.dsel // the ticket line, then the items
	if c.Stale {
		line++
	}
	return line
}

// selectionVisible reports whether the selected line is on screen.
func (m App) selectionVisible() bool {
	sel := m.selectedLine()
	budget := m.bodyHeight()
	return sel >= 0 && (budget < 0 || sel >= m.doffset && sel < m.doffset+budget)
}

// detailScroll keeps the detail's window within its lines.
func (m App) detailScroll() App {
	budget := m.bodyHeight()
	if budget < 0 {
		m.doffset = 0
		return m
	}
	body := m.detailBody(defaultWidth)
	m.doffset = min(max(m.doffset, 0), max(len(body)-budget, 0))
	return m
}

// ticketLine is the key and title, with "Jira: <status>" at the right edge.
// The title gives way first.
func ticketLine(c model.Chain, width int) string {
	left := clean(c.Ticket.Key) + "  " + clean(c.Ticket.Title)
	if c.Ticket.Status == "" {
		return fit(left, width)
	}
	right := "Jira: " + clean(c.Ticket.Status)
	avail := width - ansi.StringWidth(right) - 2
	if avail < 1 {
		return fit(right, width)
	}
	return fit(fit(left, avail)+"  "+right, width)
}

// prLine is "PR #330  acme/app  merged 2d ago  ✓ checks  ✓ approved": the
// PR's state, checks and review, and "draft" for a draft.
func prLine(p model.PR, now time.Time) string {
	parts := []string{fmt.Sprintf("PR #%d", p.Number), clean(p.Repo)}
	switch p.State {
	case model.PRMerged:
		parts = append(parts, "merged"+ago(p.MergedAt, now))
	case model.PROpen:
		parts = append(parts, "open")
	case model.PRClosed:
		parts = append(parts, "closed")
	}
	switch p.Checks {
	case model.ChecksPassing:
		parts = append(parts, "✓ checks")
	case model.ChecksFailing:
		var names []string
		for _, ch := range p.Failing {
			names = append(names, clean(ch.Name))
		}
		if len(names) == 0 {
			names = []string{"checks"}
		}
		parts = append(parts, "✗ "+strings.Join(names, ", "))
	case model.ChecksPending:
		parts = append(parts, "checks running")
	case model.ChecksExpected:
		parts = append(parts, "checks expected")
	}
	switch p.Review {
	case model.ReviewApproved:
		parts = append(parts, "✓ approved")
	case model.ReviewChangesRequested:
		parts = append(parts, "changes requested")
	case model.ReviewRequired:
		parts = append(parts, "review required")
	}
	if p.IsDraft {
		parts = append(parts, "draft")
	}
	return strings.Join(parts, "  ")
}

// ago is " 2d ago" for at, "" when at is unknown.
func ago(at, now time.Time) string {
	if at.IsZero() {
		return ""
	}
	return " " + age(now.Sub(at)) + " ago"
}

// slotText is where the work stands in s's env: "✓ deployed 1d ago
// (aaaa111)  ECS 2/2", "‖ awaiting approval since 1d", "in progress since
// 2m (cccc333)", "✗ failed 3h ago (bbbb222)", "✗ rejected or expired …",
// "✗ rolled back 2d ago (now dddd444)", "deploy unknown" or "not yet". Unknown times and SHAs
// are left out.
func slotText(s model.EnvSlot, now time.Time) string {
	switch s.State {
	case model.SlotDeployed:
		t := "✓ deployed" + ago(s.At, now) + sha(s.SHA)
		if s.Health.Known {
			t += fmt.Sprintf("  ECS %d/%d", s.Health.Healthy, s.Health.Desired)
		}
		return t
	case model.SlotAwaitingApproval:
		if s.At.IsZero() {
			return "‖ awaiting approval"
		}
		return "‖ awaiting approval since " + age(now.Sub(s.At))
	case model.SlotInProgress:
		t := "in progress"
		if !s.At.IsZero() {
			t += " since " + age(now.Sub(s.At))
		}
		return t + sha(s.SHA)
	case model.SlotFailed:
		if s.Deploy != nil && s.Deploy.Status == model.DeployRejected {
			return "✗ rejected or expired" + ago(s.At, now) + sha(s.SHA)
		}
		return "✗ failed" + ago(s.At, now) + sha(s.SHA)
	case model.SlotRolledBack:
		// Here SHA is the commit that replaced the chain's (link sets it from
		// the newer deploy), so it is marked "now".
		t := "✗ rolled back" + ago(s.At, now)
		if h := shortSHA(s.SHA); h != "" {
			t += " (now " + h + ")"
		}
		return t
	case model.SlotUnknown:
		return "deploy unknown"
	}
	return "not yet"
}

// sha is " (aaaa111)", the commit cut to 7 cells, or "" when unknown.
func sha(s string) string {
	if h := shortSHA(s); h != "" {
		return " (" + h + ")"
	}
	return ""
}

// shortSHA is the commit, cleaned and cut to 7 cells.
func shortSHA(s string) string { return ansi.Truncate(clean(s), 7, "") }
