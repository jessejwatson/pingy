package cli

import (
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"pingy/internal/monitor"
)

// loginPopup is the panel over the others for logging in to an origin:
// why, then ssh's own prompts while it runs. It opens by itself when a
// host needing it is entered from the picker, once when a graph shown
// starts needing it, and with ctrl-l.
type loginPopup struct {
	origin, dest string
	kind         string // a monitor Login kind, or "" when asked for
	link         string // what went wrong, as the host's status says
	sess         *loginSession
	text         termText
	failed       bool // the last try didn't log in
}

// needsLogin says whether h is stuck at something logging in could get
// past, or that someone should look into.
func needsLogin(h *host) bool {
	return h.running && h.meta.Origin != "" && h.state.Link != "" && h.state.Login != ""
}

// checkLogins opens the popup by itself for an origin whose hosts have
// started needing a login, once until they stop needing it, but only for
// a host whose graph is shown: others wait in loginNote, and the picker
// leaves it until the host is entered.
func (m *model) checkLogins() {
	stuck := map[string]*host{}
	for _, h := range m.hosts {
		if needsLogin(h) {
			dest := monitor.Dest(m.app.Home, h.meta.Origin)
			if stuck[dest] == nil {
				stuck[dest] = h
			}
		}
	}
	for dest := range m.loginSeen {
		if stuck[dest] == nil {
			delete(m.loginSeen, dest)
		}
	}
	if m.login != nil || m.mode != "" || m.busy || m.quitting {
		return
	}
	for _, h := range m.hosts {
		if needsLogin(h) && m.onScreen(h) {
			dest := monitor.Dest(m.app.Home, h.meta.Origin)
			if !m.loginSeen[dest] {
				m.loginSeen[dest] = true
				m.openLogin(h)
				return
			}
		}
	}
}

// onScreen says whether h's graph is shown: open by itself, or one of
// pingy -g's panels.
func (m *model) onScreen(h *host) bool {
	switch m.view {
	case viewDetail:
		return m.cur == h
	case viewAll:
		return slices.Contains(m.panels(), h)
	}
	return false
}

// enterLogin opens the popup on entering h's graph if h needs a login.
func (m *model) enterLogin(h *host) {
	if needsLogin(h) && m.login == nil {
		m.loginSeen[monitor.Dest(m.app.Home, h.meta.Origin)] = true
		m.openLogin(h)
	}
}

// offScreenLogins are the hosts needing a login that aren't on screen,
// pingy -g's others included, one per origin, unless a host on screen
// needs that origin too.
func (m *model) offScreenLogins() []*host {
	var hs []*host
	seen := map[string]bool{}
	for _, h := range m.hosts {
		if needsLogin(h) && m.onScreen(h) {
			seen[monitor.Dest(m.app.Home, h.meta.Origin)] = true
		}
	}
	for _, h := range append(m.hosts[:len(m.hosts):len(m.hosts)], m.outside...) {
		if dest := monitor.Dest(m.app.Home, h.meta.Origin); needsLogin(h) && !seen[dest] {
			seen[dest] = true
			hs = append(hs, h)
		}
	}
	return hs
}

// loginNote says, over graphs, that hosts not shown are waiting on a
// login, and how to give it. The picker marks them in its list instead.
func (m *model) loginNote() string {
	if m.view == viewPick {
		return ""
	}
	hs := m.offScreenLogins()
	if len(hs) == 0 {
		return ""
	}
	who := hs[0].meta.Origin + " needs"
	if len(hs) > 1 {
		who = fmt.Sprintf("%s and %d other origins need", hs[0].meta.Origin, len(hs)-1)
	}
	return "Hosts not shown are waiting: " + who + " you to log in · ctrl-l to answer"
}

func (m *model) openLogin(h *host) {
	p := &loginPopup{origin: h.meta.Origin, dest: monitor.Dest(m.app.Home, h.meta.Origin)}
	if needsLogin(h) {
		p.kind, p.link = h.state.Login, h.state.Link
	}
	m.login = p
}

// chosenHost is the host ctrl-l would log in for.
func (m *model) chosenHost() *host {
	switch m.view {
	case viewDetail:
		return m.cur
	case viewAll:
		if hs := m.panels(); m.focus >= 0 && m.focus < len(hs) {
			return hs[m.focus]
		}
	case viewPick:
		if rows := m.rows(); m.cursor >= 0 && m.cursor < len(rows) {
			return rows[m.cursor].h
		}
	}
	return nil
}

// keyLoginOpen is ctrl-l: the popup for the chosen host's origin, or
// for one off screen that's waiting when the chosen host isn't.
func (m *model) keyLoginOpen() {
	h := m.chosenHost()
	if waiting := m.offScreenLogins(); len(waiting) > 0 && (h == nil || !needsLogin(h)) {
		h = waiting[0]
	}
	switch {
	case h == nil:
	case h.meta.Origin == "":
		m.note, m.noteBad = h.meta.Label()+" is pinged from here, so there's nowhere to log in to", true
	default:
		m.openLogin(h)
	}
}

// loginKeys are what keys type into ssh's terminal.
var loginKeys = map[string]string{
	"enter": "\r", "backspace": "\x7f", "tab": "\t", "space": " ",
	"ctrl+c": "\x03", "ctrl+d": "\x04", "ctrl+u": "\x15", "ctrl+w": "\x17",
}

func (m *model) keyLogin(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	p := m.login
	k := msg.String()
	if p.sess != nil {
		if k == "esc" {
			p.sess.close()
			p.sess = nil
			p.text.write([]byte("\r\n[cancelled]\r\n"))
			return m, nil
		}
		if b, ok := loginKeys[k]; ok {
			p.sess.write(b)
		} else if msg.Text != "" {
			p.sess.write(msg.Text)
		}
		return m, nil
	}
	switch k {
	case "ctrl+c":
		m.quitting = true
		return m, tea.Quit
	case "esc":
		m.login = nil
	case "enter":
		p.text, p.failed = termText{}, false
		s, err := startLogin(m.app.Home, p.dest, m.loginWidth()-4)
		if err != nil {
			p.failed = true
			p.text.write([]byte("can't run ssh: " + err.Error() + "\n"))
			return m, nil
		}
		p.sess = s
		return m, s.next()
	}
	return m, nil
}

// loginMsg handles what a login session does.
func (m *model) loginMsg(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case loginOutMsg:
		if msg.s == nil || m.login == nil || m.login.sess != msg.s {
			return m, nil
		}
		m.login.text.write(msg.data)
		return m, msg.s.next()
	case loginEndMsg:
		msg.s.close()
		p := m.login
		if p == nil || p.sess != msg.s {
			return m, nil
		}
		p.sess = nil
		if msg.err == nil {
			m.app.retryOrigin(p.dest)
			m.login = nil
			m.note, m.noteBad = "Logged in to "+p.origin+"; its hosts are connecting again", false
			return m, nil
		}
		p.failed = true
	}
	return m, nil
}

func (m *model) loginWidth() int { return min(m.w()-4, 80) }

// overLogin draws the popup over the middle of the screen s.
func (m *model) overLogin(s string) string {
	p, u := m.login, m.u
	w := m.loginWidth()
	inner := w - 4
	wrap := func(s string) []string { return strings.Split(ansi.Wrap(s, inner, ""), "\n") }

	border := colWarn
	if p.kind == monitor.LoginChanged {
		border = colBad
	}
	var lines []string
	if p.link != "" {
		for _, l := range wrap(p.link) {
			lines = append(lines, u.fg(border, l))
		}
	}
	lines = append(lines, wrap(loginWhy(p.kind, p.origin, p.dest))...)
	lines = append(lines, "")
	keys := []string{"enter log in", "esc later"}
	switch {
	case p.sess != nil:
		keys = []string{"type to answer ssh", "esc cancel"}
	case p.failed:
		keys = []string{"enter try again", "esc later"}
	}
	keyLines := u.keyLines(inner, keys...)
	room := max(m.h()-2-len(lines)-len(keyLines)-3, 3)
	if p.sess != nil || p.failed {
		var out []string
		for _, l := range p.text.tail(room) {
			out = append(out, wrap(l)...)
		}
		out = out[max(len(out)-room, 0):]
		if p.sess != nil && len(out) > 0 {
			out[len(out)-1] += u.fg(colAccent, "▌")
		}
		lines = append(lines, out...)
		lines = append(lines, "")
	} else {
		lines = append(lines, u.faint("ssh "+p.dest), "")
	}
	lines = append(lines, keyLines...)

	box := u.frame(u.accent("Log in to "+p.origin), lines, w, 0, border, roundEdges)
	x := max((m.w()-lipgloss.Width(box))/2, 0)
	y := max((m.h()-lipgloss.Height(box))/2, 0)
	return lipgloss.NewCompositor(lipgloss.NewLayer(s), lipgloss.NewLayer(box).X(x).Y(y).Z(1)).Render()
}
