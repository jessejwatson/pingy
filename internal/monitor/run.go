package monitor

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"syscall"
	"time"
)

const (
	// resolveEvery is how often a host name is looked up again.
	resolveEvery = 5 * time.Minute
	// maxTimeout caps how long a ping waits for its reply.
	maxTimeout = 2 * time.Second
)

// Run is the monitor process for the host in dir: it pings until it's
// told to stop, or, for a foreground monitor, until its owner goes away.
// A host with an origin is pinged there instead, by runRemote. When
// pingy is updated, it carries on as the new one; see restart.go.
func Run(dir string) error {
	m, err := Load(dir)
	if err != nil {
		return err
	}
	lock := inheritedLock(m.lockPath())
	if lock == nil {
		if lock, err = os.OpenFile(m.lockPath(), os.O_RDWR|os.O_CREATE, 0o600); err != nil {
			return err
		}
		if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			lock.Close()
			return fmt.Errorf("%s is already being pinged", m.Label())
		}
	}
	defer lock.Close()
	signal.Ignore(syscall.SIGHUP, syscall.SIGPIPE)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	// Meta.Retry's signal, before anything could send it.
	retry := make(chan os.Signal, 1)
	signal.Notify(retry, syscall.SIGUSR1)

	exe := newExeWatch()
	for {
		up := newUpkeep(dir)
		up.exe = exe
		if err := run(dir, up, stop, retry); err != nil || !up.restart {
			return err
		}
		err := restart(exe.path, dir, lock)
		fmt.Fprintf(os.Stderr, "%s: can't restart: %v\n", time.Now().Format(time.RFC3339), err)
		exe = newExeWatch() // and carry on as this one
	}
}

// run pings for Run until it's stopped, or until pingy has been updated
// (up.restart).
func run(dir string, up *upkeep, stop, retry <-chan os.Signal) error {
	m, err := Load(dir)
	if err != nil {
		return err
	}
	out, err := openSamples(m.SamplesPath(), up.limit)
	if err != nil {
		return err
	}
	defer out.close()

	st := State{PID: os.Getpid(), Started: time.Now().UTC()}
	if err := m.saveState(st); err != nil {
		return err
	}
	if m.Origin != "" {
		return runRemote(m, up, out, st, stop, retry)
	}

	var addr net.IP
	var resolved time.Time
	next := time.Now()
	for {
		if up.check(&m, out) {
			return nil
		}

		if addr == nil || time.Since(resolved) > resolveEvery {
			ip, err := resolve(m.Target)
			switch {
			case err == nil:
				addr, resolved = ip, time.Now()
				if st.Addr != ip.String() || st.Error != "" {
					st.Addr, st.Error = ip.String(), ""
					m.saveState(st)
				}
			case addr == nil:
				if st.Error != err.Error() {
					st.Error = err.Error()
					m.saveState(st)
				}
			}
		}

		every := m.Every()
		sent := time.Now()
		rtt := time.Duration(-1)
		if addr != nil {
			rtt = pingOnce(addr, min(every, maxTimeout))
		}
		if err := out.add(Sample{At: sent, RTT: rtt}); err != nil {
			return err
		}

		next = next.Add(every)
		if wait := time.Until(next); wait > 0 {
			select {
			case <-stop:
				return nil
			case <-time.After(wait):
			}
		} else {
			next = time.Now() // fell behind; don't try to catch up
			select {
			case <-stop:
				return nil
			default:
			}
		}
	}
}

// upkeep notices what changes around a running monitor: pingy editing
// its meta.json, to rename or detach it; settings.json, for how much
// history it keeps; and, for a foreground monitor, its owner going away.
type upkeep struct {
	dir, home            string
	limit                int64
	metaMod, settingsMod time.Time
	exe                  *exeWatch // pingy, to notice it being updated
	restart              bool      // it was: stop, to restart as the new one
}

func newUpkeep(dir string) *upkeep {
	// The monitor lives in <home>/hosts/<id>.
	home := filepath.Dir(filepath.Dir(dir))
	return &upkeep{
		dir: dir, home: home, limit: historyLimit(home),
		metaMod:     modTime(filepath.Join(dir, "meta.json")),
		settingsMod: modTime(settingsPath(home)),
	}
}

// check reloads *m and the history limit if they changed, and says
// whether the monitor should stop because its owner has gone.
func (u *upkeep) check(m **Meta, out *sampleFile) bool {
	if mt := modTime(filepath.Join(u.dir, "meta.json")); !mt.Equal(u.metaMod) {
		u.metaMod = mt
		if fresh, err := (*m).Reload(); err == nil {
			*m = fresh
		}
	}
	if mt := modTime(settingsPath(u.home)); !mt.Equal(u.settingsMod) {
		u.settingsMod = mt
		u.limit = historyLimit(u.home)
		out.limit = u.limit
	}
	if (*m).Foreground && !alive((*m).OwnerPID) {
		if (*m).Ephemeral {
			os.RemoveAll(u.dir)
		}
		return true
	}
	if u.exe.changed() {
		u.restart = true
		return true
	}
	return false
}

// sampleFile appends to a monitor's samples, trimming them to limit as
// they grow.
type sampleFile struct {
	path    string
	limit   int64
	out     *os.File
	written int64
}

func openSamples(path string, limit int64) (*sampleFile, error) {
	f := &sampleFile{path: path, limit: limit}
	trim(path, limit)
	return f, f.open()
}

func (f *sampleFile) open() error {
	out, err := os.OpenFile(f.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	f.out, f.written = out, 0
	if fi, err := out.Stat(); err == nil {
		f.written = fi.Size()
	}
	return nil
}

func (f *sampleFile) add(s Sample) error {
	n, _ := f.out.Write(formatSample(s))
	f.written += int64(n)
	if f.written > f.limit {
		f.out.Close()
		trim(f.path, f.limit)
		return f.open()
	}
	return nil
}

func (f *sampleFile) close() { f.out.Close() }

func modTime(path string) time.Time {
	fi, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}

// historyLimit is how big each samples file may grow; a settings file
// that can't be read keeps the default.
func historyLimit(home string) int64 {
	s, err := LoadSettings(home)
	if err != nil {
		return Settings{HistoryMB: DefaultHistoryMB}.HistoryBytes()
	}
	return s.HistoryBytes()
}

// trim cuts the samples file at path back to its newest three quarters of
// limit once it's grown past limit, so a trim needn't happen often.
func trim(path string, limit int64) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.Size() <= limit {
		return
	}
	if _, err := f.Seek(fi.Size()-limit*3/4, io.SeekStart); err != nil {
		return
	}
	r := bufio.NewReader(f)
	if _, err := r.ReadSlice('\n'); err != nil && !errors.Is(err, bufio.ErrBufferFull) {
		return
	}
	tmp, err := os.OpenFile(path+".tmp", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, err = io.Copy(tmp, r)
	if cerr := tmp.Close(); err == nil && cerr == nil {
		os.Rename(tmp.Name(), path)
	} else {
		os.Remove(tmp.Name())
	}
}

// resolve turns a target into the address to ping, preferring IPv4.
func resolve(target string) (net.IP, error) {
	if ip := net.ParseIP(target); ip != nil {
		return ip, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, target)
	if err != nil || len(addrs) == 0 {
		return nil, fmt.Errorf("can't resolve %s", target)
	}
	for _, a := range addrs {
		if a.IP.To4() != nil {
			return a.IP, nil
		}
	}
	return addrs[0].IP, nil
}

// Resolve is resolve for pingy, to check a target before starting it.
func Resolve(target string) (net.IP, error) { return resolve(target) }

var rttRe = regexp.MustCompile(`time[=<] ?([0-9.]+) ?ms`)

// pingOnce sends one echo request with the system's ping, which needs no
// special privileges, and returns the round trip, or -1 if none came.
func pingOnce(ip net.IP, timeout time.Duration) time.Duration {
	// ping's own timeout is loose (macOS waits longer after an ICMP error),
	// so the deadline is what keeps pings on schedule.
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := pingCommand(ctx, ip, timeout).Output()
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			return -1
		}
		// No reply, or killed at the deadline; a reply it printed still counts.
	}
	mm := rttRe.FindSubmatch(out)
	if mm == nil {
		return -1
	}
	ms, err := strconv.ParseFloat(string(mm[1]), 64)
	if err != nil {
		return -1
	}
	return time.Duration(ms * float64(time.Millisecond))
}

func pingCommand(ctx context.Context, ip net.IP, timeout time.Duration) *exec.Cmd {
	v6 := ip.To4() == nil
	addr := ip.String()
	switch runtime.GOOS {
	case "darwin", "freebsd":
		if v6 {
			// ping6 has no reply timeout; the context kills it.
			return exec.CommandContext(ctx, "ping6", "-c", "1", "-n", addr)
		}
		// BSD ping's -W is in milliseconds.
		return exec.CommandContext(ctx, "ping", "-c", "1", "-n", "-W", strconv.FormatInt(max(timeout.Milliseconds(), 100), 10), addr)
	default:
		secs := strconv.Itoa(max(int((timeout+time.Second-1)/time.Second), 1))
		args := []string{"-c", "1", "-n", "-W", secs, addr}
		if v6 {
			args = append([]string{"-6"}, args...)
		}
		return exec.CommandContext(ctx, "ping", args...)
	}
}
