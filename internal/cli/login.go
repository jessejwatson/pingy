package cli

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/creack/pty"

	"pingy/internal/monitor"
)

// A login runs ssh to an origin for someone to answer its prompts, e.g.
// to accept a new host key or type a password, in the popup over pingy's
// panels. ssh then stays logged in in the background (monitor.Login), and
// the origin's hosts connect through it.

// loginSession is ssh logging in on a pseudo-terminal: its prompts come
// and go through tty, and anything else it prints through out.
type loginSession struct {
	cmd  *exec.Cmd
	tty  *os.File
	out  *os.File
	msgs chan tea.Msg
	quit chan struct{}
}

// loginOutMsg is something ssh printed; loginEndMsg says it finished.
type loginOutMsg struct {
	s    *loginSession
	data []byte
}

type loginEndMsg struct {
	s   *loginSession
	err error
}

// startLogin runs ssh to dest on a terminal cols wide.
func startLogin(home, dest string, cols int) (*loginSession, error) {
	cmd := monitor.Login(home, dest)
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	// Errors go to a pipe rather than the terminal: once logged in, ssh
	// carries on in the background holding them, and a pipe can be given
	// up on while it does.
	cmd.Stdout, cmd.Stderr = w, w
	tty, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: uint16(max(cols, 20))})
	w.Close()
	if err != nil {
		r.Close()
		return nil, err
	}
	s := &loginSession{cmd: cmd, tty: tty, out: r, msgs: make(chan tea.Msg, 64), quit: make(chan struct{})}
	for _, f := range []*os.File{tty, r} {
		go func() {
			buf := make([]byte, 4096)
			for {
				n, err := f.Read(buf)
				if n > 0 {
					s.send(loginOutMsg{s, append([]byte(nil), buf[:n]...)})
				}
				if err != nil {
					return
				}
			}
		}()
	}
	go func() {
		err := cmd.Wait()
		// Let what it printed last arrive, then stop reading.
		deadline := time.Now().Add(300 * time.Millisecond)
		r.SetReadDeadline(deadline)
		tty.SetReadDeadline(deadline)
		time.Sleep(300 * time.Millisecond)
		s.send(loginEndMsg{s, err})
	}()
	return s, nil
}

func (s *loginSession) send(msg tea.Msg) {
	select {
	case s.msgs <- msg:
	case <-s.quit:
	}
}

// next waits for what the session does next.
func (s *loginSession) next() tea.Cmd {
	return func() tea.Msg {
		select {
		case msg := <-s.msgs:
			return msg
		case <-s.quit:
			return nil
		}
	}
}

// write types b into ssh's terminal.
func (s *loginSession) write(b string) { s.tty.Write([]byte(b)) }

// close stops the session, ending ssh if it's still running.
func (s *loginSession) close() {
	select {
	case <-s.quit:
		return
	default:
	}
	close(s.quit)
	if s.cmd.ProcessState == nil && s.cmd.Process != nil {
		s.cmd.Process.Kill()
	}
	s.tty.Close()
	s.out.Close()
}

// termText keeps what a terminal shows as plain lines: escape sequences
// dropped, a carriage return starting its line again and a backspace
// taking back a character, as ssh's prompts and the terminal's echo need.
type termText struct {
	lines []string
	cur   []rune
	esc   int // 0 text, 1 after ESC, 2 in a CSI sequence, 3 in an OSC string
	cr    bool
}

func (t *termText) write(b []byte) {
	for _, r := range string(b) {
		switch t.esc {
		case 1:
			switch r {
			case '[':
				t.esc = 2
			case ']':
				t.esc = 3
			default:
				t.esc = 0
			}
			continue
		case 2:
			if r >= 0x40 && r <= 0x7e {
				t.esc = 0
			}
			continue
		case 3:
			if r == 0x07 || r == 0x1b {
				t.esc = 0
			}
			continue
		}
		if t.cr && r != '\n' {
			t.cur = t.cur[:0]
		}
		t.cr = false
		switch {
		case r == 0x1b:
			t.esc = 1
		case r == '\n':
			t.lines = append(t.lines, string(t.cur))
			t.cur = t.cur[:0]
		case r == '\r':
			t.cr = true
		case r == '\b':
			if len(t.cur) > 0 {
				t.cur = t.cur[:len(t.cur)-1]
			}
		case r == '\t':
			t.cur = append(t.cur, ' ', ' ')
		case r < 0x20 || r == 0x7f:
		default:
			t.cur = append(t.cur, r)
		}
	}
	if len(t.lines) > 200 {
		t.lines = t.lines[len(t.lines)-200:]
	}
}

// tail is the last n lines, the one being written included.
func (t *termText) tail(n int) []string {
	all := append(append([]string(nil), t.lines...), string(t.cur))
	for len(all) > 0 && strings.TrimSpace(all[0]) == "" {
		all = all[1:]
	}
	return all[max(len(all)-n, 0):]
}

// loginHere runs ssh to dest in this terminal, for when pingy isn't
// showing its panels, and says whether it logged in.
func (a *App) loginHere(dest string) bool {
	cmd := monitor.Login(a.Home, dest)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stderr, os.Stderr
	err := cmd.Run()
	var ee *exec.ExitError
	if err != nil && !errors.As(err, &ee) {
		a.notef("can't run ssh: %v", err)
	}
	return err == nil
}

// retryOrigin tells the hosts pinged through dest to connect again now.
func (a *App) retryOrigin(dest string) {
	for _, m := range monitor.List(a.root()) {
		if m.Origin != "" && monitor.Dest(a.Home, m.Origin) == dest {
			m.Retry()
		}
	}
}

// remove stops m and deletes it with its history, and ends the login
// its origin was reached through, once nothing else is pinged from there.
func (a *App) remove(m *monitor.Meta) error {
	if err := m.Remove(); err != nil {
		return err
	}
	if m.Origin != "" {
		dest := monitor.Dest(a.Home, m.Origin)
		for _, o := range monitor.List(a.root()) {
			if o.Origin != "" && monitor.Dest(a.Home, o.Origin) == dest {
				return nil
			}
		}
		monitor.CloseLogin(a.Home, dest)
	}
	return nil
}
