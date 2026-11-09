package cli

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// pingy <host> with hosts added to it pings them only while it's open, as
// it does the host. Quitting asks whether to keep them pinging in the
// background, unless pingy config ask-to-keep is off.

// quit ends pingy, first asking, in pingy <host> with hosts added to it,
// whether to keep the hosts it started pinging. Hosts added that were
// already pinged in the background aren't asked about, but still ask for
// the host itself.
func (m *model) quit() (tea.Model, tea.Cmd) {
	if s := m.sess; s != nil && s.grouped && len(s.started()) > 0 && !s.detached && !m.settings.NoKeepAsk && m.mode != "keep" {
		m.mode, m.note, m.keepNever = "keep", "", false
		m.input.Blur()
		return m, nil
	}
	m.quitting = true
	return m, tea.Quit
}

// detachQuit leaves the hosts pingy <host> started pinging, and quits.
func (m *model) detachQuit() (tea.Model, tea.Cmd) {
	if err := m.sess.detach(); err != nil {
		m.mode, m.note, m.noteBad = "", err.Error(), true
		return m, nil
	}
	m.quitting = true
	return m, tea.Quit
}

// keyKeep answers keep's popup.
func (m *model) keyKeep(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	k := strings.ToLower(msg.String())
	switch k {
	case "space":
		m.keepNever = !m.keepNever
		return m, nil
	case "esc":
		m.mode = ""
		return m, nil
	case "y", "n":
	default:
		return m, nil
	}
	if m.keepNever {
		if err := m.app.setConfig("ask-to-keep", false); err != nil {
			m.note, m.noteBad = err.Error(), true
			return m, nil
		}
	}
	m.mode = ""
	if k == "y" {
		return m.detachQuit()
	}
	m.quitting = true
	return m, tea.Quit
}

// overKeep draws keep's popup over the middle of the screen s.
func (m *model) overKeep(s string) string {
	u := m.u
	w := min(m.w()-4, 64)
	inner := w - 4
	wrap := func(s string) []string { return strings.Split(ansi.Wrap(s, inner, ""), "\n") }
	var names []string
	for _, o := range m.sess.started() {
		names = append(names, o.meta.Label())
	}
	n := len(names)
	lines := wrap(plural(n, "This is", "These are") + " pinged only while pingy is open:")
	lines = append(lines, wrap(u.bold(strings.Join(names, ", ")))...)
	lines = append(lines, "")
	lines = append(lines, wrap("Keep pinging "+plural(n, "it", "them")+" in the background? Otherwise "+plural(n, "it stops", "they stop")+" as pingy closes.")...)
	lines = append(lines, "")
	box := "[ ]"
	if m.keepNever {
		box = u.fg(colAccent, "[x]")
	}
	lines = append(lines, wrap(box+" Don't ask again; quitting stops them, and d keeps them")...)
	if m.note != "" {
		lines = append(lines, "", m.noteLine())
	}
	lines = append(lines, "")
	lines = append(lines, u.keyLines(inner, "y keep", "n stop", "space don't ask again", "esc stay")...)
	popup := u.frame(u.accent("Keep pinging?"), lines, w, 0, colAccent, roundEdges)
	x := max((m.w()-lipgloss.Width(popup))/2, 0)
	y := max((m.h()-lipgloss.Height(popup))/2, 0)
	return lipgloss.NewCompositor(lipgloss.NewLayer(s), lipgloss.NewLayer(popup).X(x).Y(y).Z(1)).Render()
}
