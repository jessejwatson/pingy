// Package monitor keeps hosts pinged in the background and records every
// reply, so pingy can list and graph them later.
//
//	~/.config/pingy/settings.json   how much history each host keeps
//	~/.config/pingy/hosts/
//	  .seq                   last monitor number handed out
//	  <id>/meta.json         what to ping and what it's called (pingy's side)
//	  <id>/state.json        the monitor's pid, address and error (its side)
//	  <id>/samples           one "<unix ms> <rtt µs>" line per ping, -1 if lost
//	  <id>/lock              held by the monitor while it runs
//	  <id>/monitor.log       what the monitor printed, for when it fails
//	~/.config/pingy/origins.json    names for the machines hosts are pinged from
//
// Each host is pinged by its own monitor process (pingy __monitor <dir>).
// A host with an origin is pinged from another machine instead: its
// monitor runs ssh there and copies the pings it hears back.
package monitor

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// DefaultInterval is how often a host is pinged unless told otherwise.
const DefaultInterval = time.Second

// MinInterval is the shortest interval allowed.
const MinInterval = 200 * time.Millisecond

// Meta is a monitor as pingy set it up. Only pingy writes it; the monitor
// reads it again when it changes.
type Meta struct {
	ID     string `json:"id"`
	Name   string `json:"name,omitempty"` // empty shows the target
	Target string `json:"target"`         // host name or address as given
	// Origin is the machine the host is pinged from: an origin's name or
	// an ssh destination, e.g. office or jesse@office.lan. Empty is here.
	Origin   string    `json:"origin,omitempty"`
	Interval int64     `json:"interval_ms"`
	Created  time.Time `json:"created"`
	// Foreground is a monitor started by pingy <host> without -d: it stops
	// when OwnerPID, the pingy watching it, goes away.
	Foreground bool `json:"foreground,omitempty"`
	OwnerPID   int  `json:"owner_pid,omitempty"`
	// Ephemeral is deleted, not just stopped, when its owner goes away.
	Ephemeral bool `json:"ephemeral,omitempty"`
	// Paused is a monitor someone stopped, as opposed to one a restart or
	// logging out stopped: autostart leaves it be.
	Paused bool `json:"paused,omitempty"`

	dir string
}

// State is what the monitor reports about itself. Only the monitor writes it.
type State struct {
	PID     int       `json:"pid"`
	Started time.Time `json:"started"`
	Addr    string    `json:"addr,omitempty"`  // the address being pinged
	Error   string    `json:"error,omitempty"` // why pings can't be sent, if they can't
	// For a host with an origin: how it's pinged there, "pingy" or
	// "script", and why it can't be reached, if it can't.
	Via  string `json:"via,omitempty"`
	Link string `json:"link,omitempty"`
	// Login is why ssh couldn't log in there, when it's something someone
	// can answer at ssh's prompts: LoginHostKey, LoginAuth or LoginChanged.
	Login string `json:"login,omitempty"`
}

// Label is the monitor's name, or where it pings when it has none.
func (m *Meta) Label() string {
	if m.Name != "" {
		return m.Name
	}
	return m.Where()
}

// Where is the target, and the origin it's pinged from if it isn't here,
// as pingy is given them: 1.1.1.1 or 1.1.1.1@office.
func (m *Meta) Where() string { return JoinSpec(m.Target, m.Origin) }

// SplitSpec reads a host as given, target or target@origin. Host names and
// addresses never have an @, so the first one starts the origin, which
// may be an ssh destination with its own, like 1.1.1.1@jesse@office.lan.
func SplitSpec(spec string) (target, origin string) {
	target, origin, _ = strings.Cut(spec, "@")
	return target, origin
}

// JoinSpec is SplitSpec's other half.
func JoinSpec(target, origin string) string {
	if origin == "" {
		return target
	}
	return target + "@" + origin
}

// Every is the ping interval.
func (m *Meta) Every() time.Duration {
	d := time.Duration(m.Interval) * time.Millisecond
	if d < MinInterval {
		return DefaultInterval
	}
	return d
}

func (m *Meta) Dir() string         { return m.dir }
func (m *Meta) SamplesPath() string { return filepath.Join(m.dir, "samples") }
func (m *Meta) statePath() string   { return filepath.Join(m.dir, "state.json") }
func (m *Meta) lockPath() string    { return filepath.Join(m.dir, "lock") }
func (m *Meta) LogPath() string     { return filepath.Join(m.dir, "monitor.log") }

// Save writes meta.json atomically.
func (m *Meta) Save() error { return writeJSON(filepath.Join(m.dir, "meta.json"), m) }

// Reload reads meta.json again.
func (m *Meta) Reload() (*Meta, error) { return Load(m.dir) }

// State reads state.json; a monitor that never started has an empty one.
func (m *Meta) State() State {
	var s State
	if b, err := os.ReadFile(m.statePath()); err == nil {
		json.Unmarshal(b, &s)
	}
	return s
}

func (m *Meta) saveState(s State) error { return writeJSON(m.statePath(), s) }

// Running says whether the monitor holds its lock.
func (m *Meta) Running() bool {
	f, err := os.OpenFile(m.lockPath(), os.O_RDONLY|os.O_CREATE, 0o600)
	if err != nil {
		return false
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.Is(err, syscall.EWOULDBLOCK)
	}
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false
}

// Stop tells a running monitor to stop and waits for it to.
func (m *Meta) Stop() error {
	if !m.Running() {
		return nil
	}
	pid := m.State().PID
	if pid <= 0 {
		return fmt.Errorf("%s is running but its process isn't known", m.Label())
	}
	syscall.Kill(pid, syscall.SIGTERM)
	for i := 0; i < 60; i++ {
		if !m.Running() {
			return nil
		}
		if i == 40 {
			syscall.Kill(pid, syscall.SIGKILL)
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("%s is still running after being told to stop", m.Label())
}

// HistorySize is how much disk the monitor's samples take.
func (m *Meta) HistorySize() int64 {
	fi, err := os.Stat(m.SamplesPath())
	if err != nil {
		return 0
	}
	return fi.Size()
}

// TrimHistory cuts a stopped monitor's samples to fit limit; a running
// monitor trims its own when it next pings.
func (m *Meta) TrimHistory(limit int64) {
	f, err := os.OpenFile(m.lockPath(), os.O_RDONLY|os.O_CREATE, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	trim(m.SamplesPath(), limit)
}

// Remove stops the monitor and deletes it with its history.
func (m *Meta) Remove() error {
	if err := m.Stop(); err != nil {
		return err
	}
	return os.RemoveAll(m.dir)
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Load reads the monitor in dir.
func Load(dir string) (*Meta, error) {
	b, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return nil, err
	}
	m := &Meta{}
	if err := json.Unmarshal(b, m); err != nil {
		return nil, fmt.Errorf("%s: %w", dir, err)
	}
	m.dir = dir
	return m, nil
}

// Create makes a new monitor directory under root with the next number,
// pinging target from origin ("" for here).
func Create(root, target, origin string, interval time.Duration) (*Meta, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(root, ".seq"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return nil, err
	}
	b, _ := os.ReadFile(lock.Name())
	n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	for {
		n++
		if _, err := os.Stat(filepath.Join(root, strconv.Itoa(n))); errors.Is(err, os.ErrNotExist) {
			break
		}
	}
	if err := os.WriteFile(lock.Name(), []byte(strconv.Itoa(n)+"\n"), 0o600); err != nil {
		return nil, err
	}
	dir := filepath.Join(root, strconv.Itoa(n))
	if err := os.Mkdir(dir, 0o700); err != nil {
		return nil, err
	}
	m := &Meta{
		ID: strconv.Itoa(n), Target: target, Origin: origin, Interval: interval.Milliseconds(),
		Created: time.Now().UTC(), dir: dir,
	}
	if err := m.Save(); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	return m, nil
}

// List returns every monitor under root, oldest first.
func List(root string) []*Meta {
	ents, _ := os.ReadDir(root)
	var list []*Meta
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		if m, err := Load(filepath.Join(root, e.Name())); err == nil {
			list = append(list, m)
		}
	}
	sort.Slice(list, func(i, j int) bool {
		a, _ := strconv.Atoi(list[i].ID)
		b, _ := strconv.Atoi(list[j].ID)
		return a < b
	})
	return list
}

// Find picks the monitor called arg: by name, then target (pinged from
// here unless arg says target@origin), then number, and last a target
// pinged from anywhere.
func Find(root, arg string) *Meta {
	if m := FindExact(root, arg); m != nil {
		return m
	}
	if _, origin := SplitSpec(arg); origin != "" {
		return nil
	}
	for _, m := range List(root) {
		if strings.EqualFold(m.Target, arg) {
			return m
		}
	}
	return nil
}

// FindExact is Find without the last step: a target without an origin
// means the one pinged from here.
func FindExact(root, arg string) *Meta {
	list := List(root)
	target, origin := SplitSpec(arg)
	for _, by := range []func(*Meta) bool{
		func(m *Meta) bool { return m.Name == arg },
		func(m *Meta) bool { return strings.EqualFold(m.Target, target) && m.Origin == origin },
		func(m *Meta) bool { return m.ID == arg },
	} {
		for _, m := range list {
			if by(m) {
				return m
			}
		}
	}
	return nil
}

// CheckName reports why name can't name the monitor id under root.
func CheckName(root, name, id string) error {
	if strings.TrimSpace(name) != name {
		return errors.New("a name can't start or end with spaces")
	}
	if len(name) > 40 {
		return errors.New("a name can be at most 40 characters")
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return errors.New("a name can't have control characters")
		}
	}
	for _, m := range List(root) {
		if m.ID != id && name != "" && (m.Name == name || (m.Name == "" && strings.EqualFold(m.Where(), name))) {
			return fmt.Errorf("%q is already taken by monitor %s", name, m.ID)
		}
	}
	return nil
}

// alive says whether process pid exists.
func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
