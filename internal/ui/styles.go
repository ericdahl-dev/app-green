// Package ui renders app-green's screens with Bubble Tea and Lip Gloss.
package ui

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// Styles holds every style the screens use, bound to one renderer, so a test
// can render through a renderer with a known terminal and environment.
type Styles struct {
	red    lipgloss.Style
	yellow lipgloss.Style
	green  lipgloss.Style
	dim    lipgloss.Style
	bold   lipgloss.Style
	// plain: the renderer prints no escape codes (no color terminal, or
	// NO_COLOR), so the cursor cannot be reverse video.
	plain bool
}

// NewStyles builds the styles on r. The renderer decides the color profile;
// it honors NO_COLOR.
func NewStyles(r *lipgloss.Renderer) Styles {
	return Styles{
		red:    r.NewStyle().Foreground(lipgloss.Color("1")),
		yellow: r.NewStyle().Foreground(lipgloss.Color("3")),
		green:  r.NewStyle().Foreground(lipgloss.Color("2")),
		dim:    r.NewStyle().Faint(true),
		bold:   r.NewStyle().Bold(true),
		plain:  r.ColorProfile() == termenv.Ascii,
	}
}

// withDim is s with the marker colors also faint, for a stale row.
func (s Styles) withDim() Styles {
	s.red = s.red.Faint(true)
	s.yellow = s.yellow.Faint(true)
	return s
}

// DefaultStyles builds the styles on Lip Gloss's default renderer (stdout).
func DefaultStyles() Styles { return NewStyles(lipgloss.DefaultRenderer()) }

// clean makes untrusted text (anything from Jira, GitHub or AWS) safe to
// print on one line: tabs, newlines and carriage returns become spaces,
// escape sequences are stripped (an OSC 52 sequence could write the
// clipboard), and any other C0 or C1 control or invalid byte is dropped.
func clean(s string) string {
	s = strings.NewReplacer("\t", " ", "\n", " ", "\r", " ").Replace(s)
	s = ansi.Strip(s)
	var b strings.Builder
	for i, r := range s {
		if r == utf8.RuneError {
			if _, size := utf8.DecodeRuneInString(s[i:]); size == 1 {
				continue // an invalid byte, such as a raw C1 CSI (0x9b)
			}
		}
		if unicode.IsControl(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// fit cuts s to w cells, ending in "…" when cut, and pads it with spaces to
// exactly w cells. w at or below zero gives "".
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = ansi.Truncate(s, w, "…")
	return s + strings.Repeat(" ", w-ansi.StringWidth(s))
}

// SGR codes for the cursor. Every profile but ASCII supports reverse video.
const (
	sgrReverse = "\x1b[7m"
	sgrReset   = "\x1b[0m"
)

// selected marks line as the cursor's: reverse video, turned back on after
// every reset the line's own styles end with. Without escape codes the
// line's first cell (its marker) becomes ">".
func (s Styles) selected(line string) string {
	if s.plain {
		return ">" + ansi.TruncateLeft(line, 1, "")
	}
	return sgrReverse + strings.ReplaceAll(line, sgrReset, sgrReset+sgrReverse) + sgrReset
}
