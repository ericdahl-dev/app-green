package ui

import (
	"fmt"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/ericdahl-dev/app-green/internal/model"
)

// Layout is the column widths shared by every row of the list.
type Layout struct {
	Width      int // the whole row, in cells
	KeyWidth   int // the longest ticket key
	TitleWidth int
	StageWidth int
}

// hintWidth fits the widest key hint, "a/x".
const hintWidth = 3

// gaps is the cells a row spends outside its key, title, stage and reason
// columns: the marker, five gaps and the hint.
const gaps = 1 + 1 + 2 + 2 + 2 + 2 + hintWidth

// NewLayout sizes the columns for chains in a list width cells wide. The key
// and stage columns fit their longest entry; the title gets half of what is
// left, at most its longest entry, and the reason the rest.
func NewLayout(chains []model.Chain, now time.Time, width int) Layout {
	l := Layout{Width: width}
	var title int
	for _, c := range chains {
		l.KeyWidth = max(l.KeyWidth, ansi.StringWidth(clean(c.Ticket.Key)))
		l.StageWidth = max(l.StageWidth, ansi.StringWidth(stageColumn(c, now)))
		title = max(title, ansi.StringWidth(clean(c.Ticket.Title)))
	}
	l.TitleWidth = max(0, min(title, (width-l.KeyWidth-l.StageWidth-gaps)/2))
	return l
}

// RenderRow renders c as one list row on the default styles.
func RenderRow(c model.Chain, l Layout, now time.Time) string {
	return DefaultStyles().Row(c, l, now)
}

// Row renders c as one row exactly l.Width cells wide: marker, key, title,
// stage, reason and key hint.
func (s Styles) Row(c model.Chain, l Layout, now time.Time) string {
	if l.Width <= 0 {
		return ""
	}
	reasonW := l.Width - (l.KeyWidth + l.StageWidth + gaps) - l.TitleWidth
	reason := ""
	if len(c.Flags) > 0 {
		reason = clean(c.Flags[0].Reason)
	}
	title := ""
	if l.TitleWidth > 0 {
		title = fit(clean(c.Ticket.Title), l.TitleWidth) + "  "
	} else {
		reasonW += 2 // no title column, so no gap after it
	}
	rest := " " + fit(clean(c.Ticket.Key), l.KeyWidth) + "  " + title +
		fit(stageColumn(c, now), l.StageWidth) + "  " + fit(reason, reasonW) + "  " + fit(RowAction(c).Key(), hintWidth)
	// Too narrow for every column: cut at the right edge, before styling.
	rest = fit(rest, l.Width-1)
	if c.Stale {
		// Dim the marker and the rest separately: wrapping the colored marker
		// in a faint style would let its reset end the dimming.
		return s.withDim().marker(c.Level()) + s.dim.Render(rest)
	}
	return s.marker(c.Level()) + rest
}

// stageColumn is StageText, after "~ " when the row may be behind.
func stageColumn(c model.Chain, now time.Time) string {
	if c.Stale {
		return "~ " + StageText(c, now)
	}
	return StageText(c, now)
}

// marker is the row's one-cell status glyph: red ●, yellow ◐, or a space.
func (s Styles) marker(l model.Level) string {
	switch l {
	case model.Red:
		return s.red.Render("●")
	case model.Yellow:
		return s.yellow.Render("◐")
	}
	return " "
}

// StageText is the row's stage column: how far c's work has got.
func StageText(c model.Chain, now time.Time) string {
	switch c.Stage {
	case model.StagePROpen:
		for _, p := range c.PRs {
			if p.State == model.PROpen {
				return fmt.Sprintf("PR #%d", p.Number)
			}
		}
	case model.StageInTest:
		return "test ✓"
	case model.StageAwaitingProd:
		return "prod ⏸"
	case model.StageInProd:
		if at := lastProd(c); !at.IsZero() {
			return "in prod ✓ " + age(now.Sub(at)) + " ago"
		}
		return "in prod ✓"
	}
	return c.Stage.String()
}

// lastProd is when the latest applicable prod deploy of c went live; zero if
// none has a time.
func lastProd(c model.Chain) time.Time {
	var last time.Time
	for _, s := range c.Slots {
		if s.Applies && s.Env.Prod && s.State == model.SlotDeployed && s.At.After(last) {
			last = s.At
		}
	}
	return last
}

// age is d in its largest whole unit: 12s, 5m, 3h, 2d. Negative d (clock
// skew) is 0s.
func age(d time.Duration) string {
	switch {
	case d < 0:
		return "0s"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}
