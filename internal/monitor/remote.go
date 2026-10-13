package monitor

import (
	"bufio"
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// A host with an origin is pinged from another machine. Its monitor runs
//
//	ssh <dest> sh -s -- <target> <interval ms> <since ms>
//
// with remote.sh on ssh's stdin. Where pingy is installed, the script
// hands over to pingy __stream, which pings in the background there and
// sends its history since <since ms>, so a connection that drops loses
// nothing; anywhere else the script pings by itself, for as long as the
// connection lasts. Either way, what comes back is lines of:
//
//	pingy-stream 1 <via>    first: "pingy" or "script"
//	<unix ms> <rtt µs>      a ping, as in a samples file; "-" for the
//	                        time means now, and -1 for the round trip lost
//	= <address>             the address being pinged
//	! <problem>             why pings can't be sent; just "!" when they can
//
//go:embed remote.sh
var remoteScript string

// StreamVersion is the version of the lines above.
const StreamVersion = 1

// StreamHeader is the first line pingy __stream sends.
func StreamHeader(via string) string { return fmt.Sprintf("pingy-stream %d %s", StreamVersion, via) }

// Origin names a machine hosts are pinged from.
type Origin struct {
	Name string `json:"name"`
	Dest string `json:"dest"` // what ssh is given, e.g. jesse@office.lan
}

type originsFile struct {
	Origins []Origin `json:"origins"`
}

func originsPath(home string) string { return filepath.Join(home, "origins.json") }

// LoadOrigins reads home's origins; none saved yet is no error.
func LoadOrigins(home string) ([]Origin, error) {
	b, err := os.ReadFile(originsPath(home))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f originsFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("can't read %s: %v", originsPath(home), err)
	}
	return f.Origins, nil
}

// SaveOrigins replaces home's origins.
func SaveOrigins(home string, list []Origin) error {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	return writeJSON(originsPath(home), originsFile{Origins: list})
}

// FindOrigin is the index of the origin called name, or -1.
func FindOrigin(list []Origin, name string) int {
	return slices.IndexFunc(list, func(o Origin) bool { return strings.EqualFold(o.Name, name) })
}

// Dest is what ssh is given to reach origin: its saved destination, or
// origin itself when it isn't a saved name.
func Dest(home, origin string) string {
	list, _ := LoadOrigins(home)
	if i := FindOrigin(list, origin); i >= 0 {
		return list[i].Dest
	}
	return origin
}

// CheckOriginName reports why name can't name an origin.
func CheckOriginName(name string) error {
	switch {
	case name == "":
		return errors.New("give the origin a name")
	case strings.HasPrefix(name, "-"):
		return errors.New("an origin's name can't start with -")
	case strings.ContainsAny(name, "@:/ \t"):
		return errors.New("an origin's name is one word, without @, : or /")
	}
	return checkPrintable(name, "an origin's name")
}

// CheckDest reports why dest can't be given to ssh.
func CheckDest(dest string) error {
	switch {
	case dest == "":
		return errors.New("say which machine to ping from, e.g. jesse@office.lan")
	case strings.HasPrefix(dest, "-"):
		return fmt.Errorf("%q isn't a machine ssh can reach", dest)
	case strings.ContainsAny(dest, " \t"):
		return fmt.Errorf("%q can't have spaces", dest)
	}
	return checkPrintable(dest, "a machine's address")
}

// CheckRemoteTarget reports why target can't be pinged from another
// machine. It can't be looked up here, since the other machine may know
// names this one doesn't, so it must at least look like a host.
func CheckRemoteTarget(target string) error {
	if target == "" || strings.HasPrefix(target, "-") || strings.IndexFunc(target, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune(".-_:%", r))
	}) >= 0 {
		return fmt.Errorf("%q isn't a host name or address", target)
	}
	return nil
}

func checkPrintable(s, what string) error {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%s can't have control characters", what)
		}
	}
	return nil
}

// SSH runs remote, a command for a POSIX shell, on dest with stdin from
// in. $PINGY_SSH replaces ssh, e.g. PINGY_SSH="ssh -F ~/.ssh/other".
func SSH(dest, remote string, in io.Reader) *exec.Cmd {
	prog := strings.Fields(os.Getenv("PINGY_SSH"))
	if len(prog) == 0 {
		prog = []string{"ssh"}
	}
	args := append(prog[1:],
		"-T",
		// Nobody is there to type a password or accept a host key.
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
		// Notice a connection that's gone quiet within about 15 seconds.
		"-o", "ServerAliveInterval=5", "-o", "ServerAliveCountMax=3",
		"--", dest, remote)
	cmd := exec.Command(prog[0], args...)
	cmd.Stdin = in
	return cmd
}

// RemoteCommand is the command that streams target's pings from another
// machine: remote.sh, run by sh with its arguments quoted for the shell
// at the other end.
func RemoteCommand(target string, every time.Duration, since time.Time) (remote string, stdin io.Reader) {
	sinceMs := int64(0)
	if !since.IsZero() {
		sinceMs = since.UnixMilli()
	}
	args := []string{target, strconv.FormatInt(every.Milliseconds(), 10), strconv.FormatInt(sinceMs, 10)}
	for i, a := range args {
		args[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return "sh -s -- " + strings.Join(args, " "), strings.NewReader(remoteScript)
}

// streamLine is one line a remote sent.
type streamLine struct {
	kind   byte // 'h' header, 's' sample, '=' address, '!' problem
	sample Sample
	now    bool // the sample has no time of its own
	text   string
}

func parseStreamLine(line string) (streamLine, bool) {
	line = strings.TrimRight(line, "\r")
	switch {
	case strings.HasPrefix(line, "pingy-stream "):
		f := strings.Fields(line)
		if len(f) < 3 || f[1] != strconv.Itoa(StreamVersion) {
			return streamLine{}, false
		}
		return streamLine{kind: 'h', text: f[2]}, true
	case strings.HasPrefix(line, "="):
		return streamLine{kind: '=', text: strings.TrimSpace(line[1:])}, true
	case strings.HasPrefix(line, "!"):
		return streamLine{kind: '!', text: strings.TrimSpace(line[1:])}, true
	}
	when, rtt, ok := strings.Cut(line, " ")
	if !ok {
		return streamLine{}, false
	}
	us, err := strconv.ParseInt(rtt, 10, 64)
	if err != nil {
		return streamLine{}, false
	}
	l := streamLine{kind: 's', sample: Sample{RTT: -1}}
	if us >= 0 {
		l.sample.RTT = time.Duration(us) * time.Microsecond
	}
	if when == "-" {
		l.now = true
		return l, true
	}
	ms, err := strconv.ParseInt(when, 10, 64)
	if err != nil {
		return streamLine{}, false
	}
	l.sample.At = time.UnixMilli(ms)
	return l, true
}

// remoteConn is one ssh connection streaming a host's pings.
type remoteConn struct {
	cmd    *exec.Cmd
	lines  chan string
	stderr *bytes.Buffer
}

func dialRemote(home string, m *Meta, since time.Time) (*remoteConn, error) {
	remote, in := RemoteCommand(m.Target, m.Every(), since)
	cmd := SSH(Dest(home, m.Origin), remote, in)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	c := &remoteConn{cmd: cmd, lines: make(chan string, 64), stderr: &bytes.Buffer{}}
	cmd.Stderr = c.stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go func() {
		defer close(c.lines)
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			c.lines <- sc.Text()
		}
	}()
	return c, nil
}

func (c *remoteConn) kill() { c.cmd.Process.Kill() }

// end waits for ssh to finish and says why the connection ended, in
// words for the host's status.
func (c *remoteConn) end(origin string) string {
	for range c.lines {
	}
	err := c.cmd.Wait()
	var last string
	for _, l := range strings.Split(c.stderr.String(), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			last = l
		}
	}
	switch {
	case last != "":
		return "can't reach " + origin + ": " + last
	case err != nil:
		return "can't reach " + origin + ": " + err.Error()
	}
	return "lost the connection to " + origin
}

// runRemote is Run for a host with an origin: it streams the host's pings
// from there into its samples, connecting again whenever it's cut off.
func runRemote(m *Meta, up *upkeep, out *sampleFile, st State, stop <-chan os.Signal) error {
	last := lastSampleTime(m.SamplesPath())
	save := func(next State) {
		if next != st {
			st = next
			m.saveState(st)
		}
	}
	wait := time.Duration(0)
	for {
		if wait > 0 {
			select {
			case <-stop:
				return nil
			case <-time.After(wait):
			}
		}
		if up.check(&m, out) {
			return nil
		}
		c, err := dialRemote(up.home, m, last)
		if err != nil {
			next := st
			next.Link = "can't run ssh: " + err.Error()
			save(next)
			wait = min(max(wait*2, 2*time.Second), time.Minute)
			continue
		}
		began, heard := time.Now(), false
		every, origin := m.Every(), m.Origin
		tick := time.NewTicker(time.Second)
	conn:
		for {
			select {
			case <-stop:
				tick.Stop()
				c.kill()
				return nil
			case <-tick.C:
				if up.check(&m, out) {
					tick.Stop()
					c.kill()
					return nil
				}
				if m.Every() != every || m.Origin != origin {
					c.kill() // and connect again to ping the new way
				}
			case line, ok := <-c.lines:
				if !ok {
					break conn
				}
				l, ok := parseStreamLine(line)
				if !ok {
					continue
				}
				next := st
				switch l.kind {
				case 'h':
					heard = true
					next.Via, next.Link = l.text, ""
				case '=':
					next.Addr, next.Error = l.text, ""
				case '!':
					next.Error = l.text
				case 's':
					s := l.sample
					if l.now {
						s.At = time.Now()
						if !s.At.After(last) {
							s.At = last.Add(time.Millisecond)
						}
					} else if !s.At.After(last) {
						continue // already have it
					}
					if err := out.add(s); err != nil {
						c.kill()
						return err
					}
					last = s.At
				}
				save(next)
			}
		}
		tick.Stop()
		next := st
		next.Link = c.end(m.Origin)
		save(next)
		switch {
		case heard && time.Since(began) > 30*time.Second:
			wait = time.Second
		case heard:
			wait = max(wait, 5*time.Second)
		default:
			wait = min(max(wait*2, 2*time.Second), time.Minute)
		}
	}
}

// lastSampleTime is when the last ping in the samples at path was sent.
func lastSampleTime(path string) time.Time {
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return time.Time{}
	}
	buf := make([]byte, min(fi.Size(), 128))
	n, _ := f.ReadAt(buf, fi.Size()-int64(len(buf)))
	buf = buf[:n]
	if i := bytes.LastIndexByte(bytes.TrimRight(buf, "\n"), '\n'); i >= 0 {
		buf = buf[i+1:]
	}
	s, _ := parseSamples(buf, nil)
	if len(s) == 0 {
		return time.Time{}
	}
	return s[len(s)-1].At
}

// Follow writes the lines of the samples at path for pings sent after
// since to w, then keeps writing new ones as they come, every poll, until
// writing fails or keep returns false. A trimmed file is read again from
// the start, skipping what was already written.
func Follow(path string, since time.Time, w *bufio.Writer, poll time.Duration, keep func() bool) error {
	sent := since.UnixMilli()
	var offset int64
	var partial []byte
	for {
		if f, err := os.Open(path); err == nil {
			if fi, err := f.Stat(); err == nil && fi.Size() < offset {
				offset, partial = 0, nil
			}
			f.Seek(offset, io.SeekStart)
			r := bufio.NewReaderSize(f, 64<<10)
			for {
				line, err := r.ReadSlice('\n')
				offset += int64(len(line))
				if err == bufio.ErrBufferFull {
					partial = append(partial, line...)
					continue
				}
				if err != nil {
					partial = append(partial, line...)
					break
				}
				if len(partial) > 0 {
					line = append(partial, line...)
					partial = nil
				}
				sp := bytes.IndexByte(line, ' ')
				if sp < 0 {
					continue
				}
				ms, err := strconv.ParseInt(string(line[:sp]), 10, 64)
				if err != nil || ms <= sent {
					continue
				}
				if _, err := w.Write(line); err != nil {
					f.Close()
					return err
				}
				sent = ms
			}
			f.Close()
		}
		if err := w.Flush(); err != nil {
			return err
		}
		if !keep() {
			return nil
		}
		time.Sleep(poll)
	}
}
