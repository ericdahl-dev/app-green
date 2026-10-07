package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/ericdahl-dev/app-green/internal/model"
	"github.com/ericdahl-dev/app-green/internal/resolver"
)

// defaultWidth is the list width before the first WindowSizeMsg.
const defaultWidth = 80

// footerKeys is the key line at the bottom of the list.
const footerKeys = "↑/↓ move  enter details  f re-run  a/x approve/reject  t status  o open  r refresh  q quit"

// View is the header, the rows (chains, then the Unlinked group), and the
// footer. When the body is taller than the screen it scrolls to keep the
// cursor in view; the header and footer stay put.
func (m App) View() string {
	width := m.width
	if width <= 0 {
		width = defaultWidth
	}
	if m.detail != "" && m.snap != nil { // the detail only opens from a snapshot
		return m.detailView(width)
	}
	now := m.now()
	var status []resolver.AdapterStatus
	var body []string
	switch {
	case m.snap == nil:
		body = append(body, "loading…")
	case len(m.snap.Chains) == 0 && len(m.snap.Unlinked) == 0:
		status = m.snap.Status
		body = append(body, "nothing in flight")
	default:
		status = m.snap.Status
		l := NewLayout(m.snap.Chains, now, width)
		for i, c := range m.snap.Chains {
			body = append(body, m.mark(i, m.styles.Row(c, l, now)))
		}
		if n := len(m.snap.Unlinked); n > 0 {
			body = append(body, m.styles.dim.Render(ansi.Truncate(fmt.Sprintf("── Unlinked (%d) ──", n), width, "…")))
			for i, p := range m.snap.Unlinked {
				// Exactly width cells, so the cursor's reverse video spans it.
				line := " " + fit(fmt.Sprintf(" %s#%d  %s", clean(p.Repo), p.Number, clean(p.Title)), width-1)
				body = append(body, m.mark(len(m.snap.Chains)+i, line))
			}
		}
	}
	if budget := m.bodyHeight(); budget >= 0 && len(body) > budget {
		body = body[m.offset:min(m.offset+budget, len(body))]
	}
	lines := append([]string{m.styles.Header(status, now, width)}, body...)
	return strings.Join(append(lines, m.footer(footerKeys, width)), "\n")
}

// mark is line, selected when item i is under the cursor.
func (m App) mark(i int, line string) string {
	if i == m.cursor {
		return m.styles.selected(line)
	}
	return line
}

// OpenTarget is the page o opens for c's row: what Flags[0] is about,
// whatever the row's main key. That is the pipeline console (pipelineURL)
// for an Env, a failing check's own page (else the PR's checks tab) for a
// failed check, or the PR. With no flag, a flag with no target, or an
// unknown page, it is the ticket. It is "" only when the ticket's URL is
// unknown too.
func OpenTarget(c model.Chain, pipelineURL func(model.Env) string) string {
	if len(c.Flags) == 0 {
		return c.Ticket.URL
	}
	f := c.Flags[0]
	var u string
	switch {
	case f.Slot != nil:
		u = pipelineURL(f.Slot.Env)
	case f.PR != nil && f.Kind == model.FlagCheckFailed:
		u = checksTarget(*f.PR)
	case f.PR != nil:
		u = f.PR.URL
	}
	if u == "" {
		return c.Ticket.URL
	}
	return u
}

// checksTarget is the page for pr's failing checks: the first failing check's
// own page, else ChecksPage(pr).
func checksTarget(pr model.PR) string {
	for _, ch := range pr.Failing {
		if _, ok := WebPage(ch.URL); ok {
			return ch.URL
		}
	}
	return ChecksPage(pr)
}

// footer is keyLine, then the open error, the note and the
// warnings count, if any, cut to width. The key line gives way first.
func (m App) footer(keyLine string, width int) string {
	var extra []string
	if m.err != "" {
		extra = append(extra, m.styles.red.Render(clean(m.err)))
	}
	if m.note != "" {
		extra = append(extra, m.styles.dim.Render(clean(m.note)))
	}
	if m.snap != nil && len(m.snap.Warnings) > 0 {
		n := len(m.snap.Warnings)
		extra = append(extra, m.styles.dim.Render(fmt.Sprintf("%d %s", n, plural(n, "warning"))))
	}
	if len(extra) == 0 {
		return ansi.Truncate(keyLine, width, "…")
	}
	tail := "  " + strings.Join(extra, "  ")
	keys := ansi.Truncate(keyLine, max(width-ansi.StringWidth(tail), 0), "…")
	return ansi.Truncate(keys+tail, width, "…")
}

// plural is word, with an s unless n is 1.
func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

// bodyHeight is how many body lines fit between the header and the footer,
// -1 when the height is not known yet (everything is shown).
func (m App) bodyHeight() int {
	if m.height <= 0 {
		return -1
	}
	return max(m.height-2, 0)
}

// scroll moves the window of body lines just enough to show the cursor. On
// the detail screen it keeps the detail's window within its lines.
func (m App) scroll() App {
	if m.detail != "" {
		return m.detailScroll()
	}
	budget := m.bodyHeight()
	if budget < 0 || m.snap == nil {
		m.offset = 0
		return m
	}
	lines, line := len(m.snap.Chains), m.cursor
	if n := len(m.snap.Unlinked); n > 0 {
		lines += 1 + n
		if m.cursor >= len(m.snap.Chains) {
			line++ // past the Unlinked header
		}
	}
	if line < m.offset {
		m.offset = line
	}
	if line >= m.offset+budget {
		m.offset = line - budget + 1
	}
	m.offset = min(max(m.offset, 0), max(lines-budget, 0))
	return m
}
