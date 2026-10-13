// Package monitor keeps hosts pinged in the background and records every
// reply, so pingy can list and graph them later.
//
//	~/.config/pingy/hosts/
//	  .seq                   last monitor number handed out
//	  <id>/meta.json         what to ping and what it's called (pingy's side)
//	  <id>/state.json        the monitor's pid, address and error (its side)
//	  <id>/samples           one "<unix ms> <rtt µs>" line per ping, -1 if lost
//	  <id>/lock              held by the monitor while it runs
//	  <id>/monitor.log       what the monitor printed, for when it fails
//
// Each host is pinged by its own monitor process (pingy __monitor <dir>).
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
	ID       string    `json:"id"`
	Name     string    `json:"name,omitempty"` // empty shows the target
	Target   string    `json:"target"`         // host name or address as given
	Interval int64     `json:"interval_ms"`
	Created  time.Time `json:"created"`
	// Foreground is a monitor started by pingy <host> without -d: it stops
	// when OwnerPID, the pingy watching it, goes away.
	Foreground bool `json:"foreground,omitempty"`
	OwnerPID   int  `json:"owner_pid,omitempty"`
	// Ephemeral is deleted, not just stopped, when its owner goes away.
	Ephemeral bool `json:"ephemeral,omitempty"`

	dir string
}

// State is what the monitor reports about itself. Only the monitor writes it.
type State struct {
	PID     int       `json:"pid"`
	Started time.Time `json:"started"`
	Addr    string    `json:"addr,omitempty"`  // the address being pinged
	Error   string    `json:"error,omitempty"` // why pings can't be sent, if they can't
}

// Label is the monitor's name, or its target when it has none.
func (m *Meta) Label() string {
	if m.Name != "" {
		return m.Name
	}
	return m.Target
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

// Create makes a new monitor directory under root with the next number.
func Create(root string, target string, interval time.Duration) (*Meta, error) {
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
		ID: strconv.Itoa(n), Target: target, Interval: interval.Milliseconds(),
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

// Find picks the monitor called arg: by name, then target, then number.
func Find(root, arg string) *Meta {
	list := List(root)
	for _, by := range []func(*Meta) bool{
		func(m *Meta) bool { return m.Name == arg },
		func(m *Meta) bool { return strings.EqualFold(m.Target, arg) },
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
		if m.ID != id && name != "" && (m.Name == name || (m.Name == "" && strings.EqualFold(m.Target, name))) {
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
