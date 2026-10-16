package cli

import (
	"io"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"pingy/internal/monitor"
)

func TestTermTextFollowsPromptsAndEcho(t *testing.T) {
	var tt termText
	tt.write([]byte("\x1b[1mThe authenticity\x1b[0m of host\r\nAre you sure? "))
	tt.write([]byte("yex\b \bs\r\n\x1b]0;title\x07Password: "))
	tt.write([]byte("\r\nprogress 10%\rprogress 100%\r\n"))
	got := strings.Join(tt.tail(10), "|")
	if want := "The authenticity of host|Are you sure? yes|Password: |progress 100%|"; got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

// TestLoginPopupFits draws the popup over the panel with more of ssh's
// output than fits, and an error that wraps, to check it stays whole, on
// screen and in the middle.
func TestLoginPopupFits(t *testing.T) {
	a, _ := testApp(t)
	a.Width, a.Height = 100, 30
	m := newPickModel(a)
	m.login = &loginPopup{origin: "office", dest: "jesse@office.lan", kind: monitor.LoginHostKey,
		link: "can't reach office: jesse@office.lan: Permission denied (publickey,password), and more besides.", failed: true}
	for i := 0; i < 40; i++ {
		m.login.text.write([]byte("a line of ssh's output that is long enough to need wrapping in the popup, twice over\r\n"))
	}
	out := ansi.Strip(m.View().Content)
	lines := strings.Split(out, "\n")
	if len(lines) > 30 {
		t.Fatalf("%d lines, over the screen's 30:\n%s", len(lines), out)
	}
	top, bottom, keys := -1, -1, -1
	for i, l := range lines {
		if lipgloss.Width(l) > 100 {
			t.Errorf("line %d is %d wide", i, lipgloss.Width(l))
		}
		switch {
		case strings.Contains(l, "Log in to office"):
			top = i
		case strings.Contains(l, "enter try again"):
			keys = i
		case top >= 0 && bottom < 0 && strings.Contains(l, "╰──────────"):
			bottom = i
		}
	}
	if top < 0 || keys != bottom-1 || top > 30-1-bottom+1 {
		t.Fatalf("popup from %d to %d, keys at %d:\n%s", top, bottom, keys, out)
	}
	edge := []rune(lines[top])
	left, right := slices.Index(edge, '╭'), slices.Index(edge, '╮')
	for i := top + 1; i < bottom; i++ {
		if r := []rune(lines[i]); len(r) <= right || r[left] != '│' || r[right] != '│' {
			t.Fatalf("line %d has lost its border:\n%s", i, out)
		}
	}
}

// TestLoginPopupTakesPaste pastes into the popup while ssh is asking for
// a password, which should reach ssh's terminal as if typed.
func TestLoginPopupTakesPaste(t *testing.T) {
	a, _ := testApp(t)
	m := newPickModel(a)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	m.login = &loginPopup{origin: "office", dest: "jesse@office.lan",
		sess: &loginSession{tty: w, quit: make(chan struct{})}}
	m.Update(tea.PasteMsg{Content: "s3cr€t pass"})
	w.Close()
	got, _ := io.ReadAll(r)
	if string(got) != "s3cr€t pass" {
		t.Fatalf("ssh got %q", got)
	}
}

// TestLoginPopupWaitsForItsHost has a host needing a login: the picker
// leaves the popup until it's entered, and another host's graph notes it
// and lets ctrl-l answer it.
func TestLoginPopupWaitsForItsHost(t *testing.T) {
	a, _ := testApp(t)
	for _, origin := range []string{"office", "lab"} {
		if _, err := monitor.Create(a.root(), "10.0.0.1", origin, time.Second); err != nil {
			t.Fatal(err)
		}
	}
	m := newPickModel(a)
	var office, lab *host
	for _, h := range m.hosts {
		h.running = true
		if h.meta.Origin == "office" {
			office = h
		} else {
			lab = h
		}
	}
	office.state.Link, office.state.Login = "can't reach office: Permission denied (password).", monitor.LoginAuth
	view := func() string { return ansi.Strip(m.View().Content) }

	m.checkLogins()
	if m.login != nil {
		t.Fatalf("popup opened by itself in the picker")
	}
	if strings.Contains(view(), "not shown") {
		t.Fatalf("the picker has a note about hosts not shown:\n%s", view())
	}

	m.open(lab)
	m.checkLogins()
	if m.login != nil {
		t.Fatalf("popup opened for %s while lab's graph is open", m.login.origin)
	}
	if !strings.Contains(view(), "office needs you to log in · ctrl-l") {
		t.Fatalf("no note about office:\n%s", view())
	}

	// pingy -g with lab's panel: the note goes above the keys, not over them.
	m.view, m.cur, m.only = viewAll, nil, map[string]bool{lab.meta.ID: true}
	m.refresh()
	lab.running = true
	if len(m.outside) != 1 || m.outside[0].meta.Origin != "office" {
		t.Fatalf("outside pingy -g: %v", m.outside)
	}
	office = m.outside[0]
	office.running = true
	office.state.Link, office.state.Login = "can't reach office: Permission denied (password).", monitor.LoginAuth
	if out := view(); !strings.Contains(out, "office needs you to log in") || !strings.Contains(out, "per column") {
		t.Fatalf("note and keys aren't both shown:\n%s", out)
	}
	if out := view(); strings.Count(out, "\n")+1 > m.h() {
		t.Fatalf("%d lines, over the screen's %d:\n%s", strings.Count(out, "\n")+1, m.h(), out)
	}
	m.Update(tea.KeyPressMsg{Code: 'l', Mod: tea.ModCtrl})
	if m.login == nil || m.login.origin != "office" {
		t.Fatalf("ctrl-l opened %+v, want office", m.login)
	}

	// Entering office from the picker opens it, each time.
	m.only = nil
	m.refresh()
	for _, h := range m.hosts {
		if h.meta.Origin == "office" {
			office = h
		}
	}
	office.running = true
	office.state.Link, office.state.Login = "can't reach office: Permission denied (password).", monitor.LoginAuth
	for range 2 {
		m.login = nil
		m.view, m.cur = viewPick, nil
		m.open(office)
		if m.login == nil || m.login.origin != "office" {
			t.Fatalf("popup didn't open on entering office: %+v", m.login)
		}
	}
}
