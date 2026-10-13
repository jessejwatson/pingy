package cli

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"pingy/internal/monitor"
)

// loginPopup is the panel over the others for logging in to an origin:
// why, then ssh's own prompts while it runs. It opens by itself once
// when an origin's hosts first need it, and with ctrl-l on one of them.
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
// started needing a login, once until they stop needing it.
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
		if needsLogin(h) {
			dest := monitor.Dest(m.app.Home, h.meta.Origin)
			if !m.loginSeen[dest] {
				m.loginSeen[dest] = true
				m.openLogin(h)
				return
			}
		}
	}
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

// keyLoginOpen is ctrl-l: the popup for the chosen host's origin.
func (m *model) keyLoginOpen() {
	h := m.chosenHost()
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
		lines = append(lines, u.fg(border, ansi.Wrap(p.link, inner, "")))
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
