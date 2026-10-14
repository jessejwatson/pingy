package monitor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// A monitor carries on with the pingy it started as until pingy is
// updated (brew upgrade, make install): then it runs the new one in its
// place, as the same process holding the same lock, so it's never seen
// to stop.

// lockFDEnv hands the lock to the new pingy across the exec.
const lockFDEnv = "PINGY_LOCK_FD"

// exeCheckEvery is how often a monitor looks at whether pingy has changed.
var exeCheckEvery = 10 * time.Second

func init() {
	// For tests, which can't wait that long for a pingy they've replaced.
	if ms, err := strconv.Atoi(os.Getenv("PINGY_EXE_CHECK_MS")); err == nil && ms > 0 {
		exeCheckEvery = time.Duration(ms) * time.Millisecond
	}
}

var cellarRe = regexp.MustCompile(`^(.*)/Cellar/pingy/[^/]+/bin/pingy$`)

// StableExe is the path to run pingy by that still finds it once it's
// updated: for Homebrew, its opt link rather than the versioned Cellar
// path that an upgrade removes.
func StableExe() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		if m := cellarRe.FindStringSubmatch(real); m != nil {
			opt := filepath.Join(m[1], "opt", "pingy", "bin", "pingy")
			if _, err := os.Stat(opt); err == nil {
				return opt, nil
			}
		}
	}
	return exe, nil
}

// exeWatch notices pingy at path being replaced.
type exeWatch struct {
	path    string
	running os.FileInfo // what's at path that this process runs
	pending os.FileInfo // a different one seen there last look
	next    time.Time
	ok      func(path string) bool
}

// newExeWatch watches the pingy this monitor was started as: its first
// argument, as pingy starts it by a stable path.
func newExeWatch() *exeWatch {
	path := os.Args[0]
	if !filepath.IsAbs(path) {
		var err error
		if path, err = StableExe(); err != nil {
			return nil
		}
	}
	fi, err := os.Stat(path)
	if err != nil {
		return nil
	}
	return &exeWatch{path: path, running: fi, next: time.Now().Add(exeCheckEvery), ok: runsOK}
}

func sameExe(a, b os.FileInfo) bool {
	return os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

// changed says whether pingy has been replaced by one that runs: seen
// the same, and different, two looks running so it's finished being
// written.
func (w *exeWatch) changed() bool {
	if w == nil || time.Now().Before(w.next) {
		return false
	}
	w.next = time.Now().Add(exeCheckEvery)
	fi, err := os.Stat(w.path)
	if err != nil || sameExe(fi, w.running) {
		w.pending = nil
		return false
	}
	if w.pending == nil || !sameExe(fi, w.pending) {
		w.pending = fi
		return false
	}
	w.pending = nil
	if !w.ok(w.path) {
		w.running = fi // broken: stay on this one until it's replaced again
		fmt.Fprintf(os.Stderr, "%s: not restarting into %s, which doesn't run\n", time.Now().Format(time.RFC3339), w.path)
		return false
	}
	return true
}

// runsOK says whether the pingy at path runs at all.
func runsOK(path string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--version").Output()
	return err == nil && strings.Contains(string(out), "pingy")
}

// restart runs the pingy at path in this process's place, monitoring dir
// and keeping lock held. It only returns if that fails.
func restart(path, dir string, lock *os.File) error {
	fd := int(lock.Fd())
	if _, _, e := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_SETFD, 0); e != 0 {
		return e
	}
	fmt.Fprintf(os.Stderr, "%s: pingy changed, restarting into %s\n", time.Now().Format(time.RFC3339), path)
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, lockFDEnv+"=") {
			env = append(env, kv)
		}
	}
	env = append(env, lockFDEnv+"="+strconv.Itoa(fd))
	err := syscall.Exec(path, []string{path, "__monitor", dir}, env)
	syscall.CloseOnExec(fd)
	return err
}

// inheritedLock is the lock a monitor's previous pingy handed over, if
// it's the one at path and still held.
func inheritedLock(path string) *os.File {
	v := os.Getenv(lockFDEnv)
	if v == "" {
		return nil
	}
	os.Unsetenv(lockFDEnv)
	fd, err := strconv.Atoi(v)
	if err != nil || fd < 3 {
		return nil
	}
	syscall.CloseOnExec(fd)
	f := os.NewFile(uintptr(fd), path)
	have, err1 := f.Stat()
	want, err2 := os.Stat(path)
	// Locking again through the same open file is a no-op while it's held.
	if err1 != nil || err2 != nil || !os.SameFile(have, want) || syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		f.Close()
		return nil
	}
	return f
}
