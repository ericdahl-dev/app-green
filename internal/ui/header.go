package ui

import (
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/ericdahl-dev/app-green/internal/resolver"
)

// RenderHeader renders the header line on the default styles.
func RenderHeader(statuses []resolver.AdapterStatus, now time.Time, width int) string {
	return DefaultStyles().Header(statuses, now, width)
}

// Header renders "app-green" and one short segment per source, in the order
// given (Snapshot.Status order).
func (s Styles) Header(statuses []resolver.AdapterStatus, now time.Time, width int) string {
	line := s.header(statuses, now, true)
	if ansi.StringWidth(line) <= width {
		return line
	}
	// Too wide: OK sources drop their ages first, then the line is cut.
	line = s.header(statuses, now, false)
	if ansi.StringWidth(line) <= width {
		return line
	}
	return ansi.Truncate(line, max(width, 0), "…")
}

func (s Styles) header(statuses []resolver.AdapterStatus, now time.Time, ages bool) string {
	parts := []string{s.bold.Render("app-green")}
	for _, st := range statuses {
		parts = append(parts, s.segment(st, now, ages))
	}
	return strings.Join(parts, "  ")
}

// errWidth caps a generic error in the header, in cells.
const errWidth = 30

// segment is one source's status: "jira ✓ 12s", "github ✗ token rejected",
// "aws prod-acct ✗ sso expired", "aws stage-acct ⏸ throttled 2m", or
// "jira ✗ <error>". A source that has not answered yet shows "…". With ages
// false an OK source shows no age.
func (s Styles) segment(st resolver.AdapterStatus, now time.Time, ages bool) string {
	switch {
	case st.OK && (st.At.IsZero() || !ages):
		return st.Name + " " + s.green.Render("✓")
	case st.OK:
		return st.Name + " " + s.green.Render("✓") + " " + age(now.Sub(st.At))
	case st.Auth:
		return st.Name + " " + s.red.Render("✗ token rejected")
	case st.SSO:
		return st.Name + " " + s.red.Render("✗ sso expired")
	case st.Throttled && now.Before(st.RetryAt):
		return st.Name + " " + s.yellow.Render("⏸ throttled "+age(st.RetryAt.Sub(now)))
	case st.Throttled:
		return st.Name + " " + s.yellow.Render("⏸ throttled")
	case st.Err != "":
		msg, _, _ := strings.Cut(st.Err, "\n")
		return st.Name + " " + s.red.Render("✗ "+ansi.Truncate(msg, errWidth, "…"))
	}
	return st.Name + " " + s.dim.Render("…")
}
