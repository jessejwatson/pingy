package cli

import (
	"strings"
	"testing"

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
// output than fits, to check it stays whole, on screen and in the middle.
func TestLoginPopupFits(t *testing.T) {
	a, _ := testApp(t)
	a.Width, a.Height = 100, 30
	m := newPickModel(a)
	m.login = &loginPopup{origin: "office", dest: "jesse@office.lan", kind: monitor.LoginHostKey,
		link: "can't reach office: Host key verification failed.", failed: true}
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
}
