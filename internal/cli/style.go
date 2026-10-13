package cli

import (
	"fmt"
	"image/color"
	"os"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
)

// Colours are the terminal's own 16 ANSI colours, so pingy follows the
// user's theme instead of fighting it. They match macro-master's.
var (
	colAccent = lipgloss.Color("5") // magenta: names, prompts, headings
	colCmd    = lipgloss.Color("6") // cyan: commands and addresses
	colOK     = lipgloss.Color("2")
	colWarn   = lipgloss.Color("3")
	colBad    = lipgloss.Color("1")
	colBorder = lipgloss.Color("8")
)

// icons are the glyphs pingy decorates output with.
type icons struct {
	ok, warn, err, up, down, stopped string
}

var (
	unicodeIcons = icons{ok: "✓", warn: "!", err: "✗", up: "●", down: "●", stopped: "○"}
	// PINGY_ICONS=nerd, for terminals with a Nerd Font.
	nerdIcons = icons{ok: "", warn: "", err: "", up: "", down: "", stopped: ""}
)

// ui renders output for one stream. When off, every helper returns its
// input unchanged, so pipes, NO_COLOR, TERM=dumb and tests get plain text.
type ui struct {
	on    bool
	icons icons
	width int
}

// styleWanted says whether f should get colours and panels.
func styleWanted(f *os.File) bool {
	return isTerminal(f) && os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// termSize is the terminal's size, or 80×24 when it can't be told.
func termSize() (w, h int) {
	for _, f := range []*os.File{os.Stdout, os.Stderr} {
		if w, h, err := term.GetSize(f.Fd()); err == nil && w > 0 {
			return w, h
		}
	}
	return 80, 24
}

func (a *App) newUI(on bool) ui {
	u := ui{on: on, icons: unicodeIcons, width: a.Width}
	if a.Icons == "nerd" {
		u.icons = nerdIcons
	}
	if u.width <= 0 {
		u.width = 80
	}
	return u
}

func (a *App) outUI() ui { return a.newUI(a.StyleOut) }
func (a *App) errUI() ui { return a.newUI(a.StyleErr) }

func (u ui) render(s lipgloss.Style, text string) string {
	if !u.on || text == "" {
		return text
	}
	return s.Render(text)
}

func (u ui) fg(c color.Color, text string) string {
	return u.render(lipgloss.NewStyle().Foreground(c), text)
}

func (u ui) bold(text string) string  { return u.render(lipgloss.NewStyle().Bold(true), text) }
func (u ui) faint(text string) string { return u.render(lipgloss.NewStyle().Faint(true), text) }
func (u ui) accent(text string) string {
	return u.render(lipgloss.NewStyle().Foreground(colAccent).Bold(true), text)
}

// icon returns glyph followed by a space, or "" when styling is off.
func (u ui) icon(glyph string, c color.Color) string {
	if !u.on || glyph == "" {
		return ""
	}
	return u.fg(c, glyph) + " "
}

// box draws lines in a rounded box exactly w wide and h tall (h <= 0 fits
// the lines), with title set into the top border as fzf labels are.
func (u ui) box(title string, lines []string, w, h int, border color.Color) string {
	inner := max(w-4, 1)
	if h <= 0 {
		h = len(lines) + 2
	}
	edge := func(s string) string { return u.fg(border, s) }
	var b strings.Builder
	// The title keeps its own styling; only the rule is the border colour.
	if title != "" {
		title = ansi.Truncate(" "+title+" ", max(w-5, 0), "…")
		b.WriteString(edge("╭─") + title + edge(strings.Repeat("─", max(w-3-lipgloss.Width(title), 0))+"╮"))
	} else {
		b.WriteString(edge("╭" + strings.Repeat("─", max(w-2, 0)) + "╮"))
	}
	for i := 0; i < h-2; i++ {
		l := ""
		if i < len(lines) {
			l = lines[i]
		}
		b.WriteString("\n" + edge("│") + " " + fit(l, inner) + " " + edge("│"))
	}
	b.WriteString("\n" + edge("╰"+strings.Repeat("─", max(w-2, 0))+"╯"))
	return b.String()
}

// fit cuts or pads s to exactly w columns.
func fit(s string, w int) string {
	if lipgloss.Width(s) > w {
		s = ansi.Truncate(s, w, "…")
	}
	return s + strings.Repeat(" ", max(w-lipgloss.Width(s), 0))
}

// spread puts left and right at either end of w columns, cutting left if
// they don't both fit.
func spread(left, right string, w int) string {
	rw := lipgloss.Width(right)
	if rw >= w {
		return fit(right, w)
	}
	return fit(left, w-rw-1) + " " + right
}

// done reports a finished action on stderr.
func (a *App) done(format string, args ...any) {
	u := a.errUI()
	fmt.Fprintf(a.Stderr, "%s%s\n", u.icon(u.icons.ok, colOK), fmt.Sprintf(format, args...))
}

// hint prints a dimmed follow-up line on stderr.
func (a *App) hint(format string, args ...any) {
	fmt.Fprintln(a.Stderr, a.errUI().faint(fmt.Sprintf(format, args...)))
}

// notef prints a "pingy: ..." notice on stderr.
func (a *App) notef(format string, args ...any) {
	u := a.errUI()
	msg := fmt.Sprintf(format, args...)
	if u.on {
		fmt.Fprintf(a.Stderr, "%s%s\n", u.fg(colCmd, "› "), msg)
		return
	}
	fmt.Fprintf(a.Stderr, "pingy: %s\n", msg)
}

// keyLines lays hints out over as many lines w wide as they need, breaking
// only between hints. Plain hints are dimmed; styled ones keep their style.
func (u ui) keyLines(w int, hints ...string) []string {
	sep := u.faint(" · ")
	var lines []string
	cur, curW := "", 0
	for _, h := range hints {
		if !strings.Contains(h, "\x1b") {
			h = u.faint(h)
		}
		hw := lipgloss.Width(h)
		if cur != "" && curW+3+hw > w {
			lines, cur, curW = append(lines, cur), "", 0
		}
		if cur != "" {
			cur, curW = cur+sep, curW+3
		}
		cur, curW = cur+h, curW+hw
	}
	return append(lines, cur)
}
