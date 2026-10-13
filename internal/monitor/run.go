package monitor

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"runtime"
	"strconv"
	"syscall"
	"time"
)

const (
	// trimAt is when the samples file is cut back to its newest half:
	// about 15 days at one ping a second.
	trimAt = 32 << 20
	// resolveEvery is how often a host name is looked up again.
	resolveEvery = 5 * time.Minute
	// maxTimeout caps how long a ping waits for its reply.
	maxTimeout = 2 * time.Second
)

// Run is the monitor process for the host in dir: it pings until it's
// told to stop, or, for a foreground monitor, until its owner goes away.
func Run(dir string) error {
	m, err := Load(dir)
	if err != nil {
		return err
	}
	lock, err := os.OpenFile(m.lockPath(), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("%s is already being pinged", m.Label())
	}
	signal.Ignore(syscall.SIGHUP, syscall.SIGPIPE)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)

	trim(m.SamplesPath())
	out, err := os.OpenFile(m.SamplesPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { out.Close() }()

	st := State{PID: os.Getpid(), Started: time.Now().UTC()}
	if err := m.saveState(st); err != nil {
		return err
	}

	var addr net.IP
	var resolved time.Time
	metaMod := modTime(dir)
	written := int64(0)
	if fi, err := out.Stat(); err == nil {
		written = fi.Size()
	}
	next := time.Now()
	for {
		// pingy changes meta.json to rename or detach a monitor.
		if mt := modTime(dir); !mt.Equal(metaMod) {
			metaMod = mt
			if fresh, err := m.Reload(); err == nil {
				m = fresh
			}
		}
		if m.Foreground && !alive(m.OwnerPID) {
			if m.Ephemeral {
				os.RemoveAll(dir)
			}
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
		n, _ := out.Write(formatSample(Sample{At: sent, RTT: rtt}))
		written += int64(n)
		if written > trimAt {
			out.Close()
			trim(m.SamplesPath())
			if out, err = os.OpenFile(m.SamplesPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err != nil {
				return err
			}
			written = 0
			if fi, err := out.Stat(); err == nil {
				written = fi.Size()
			}
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

func modTime(dir string) time.Time {
	fi, err := os.Stat(dir + "/meta.json")
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}

// trim cuts the samples file at path back to its newest half once it
// reaches trimAt.
func trim(path string) {
	b, err := os.ReadFile(path)
	if err != nil || len(b) < trimAt {
		return
	}
	b = b[len(b)/2:]
	for i, c := range b {
		if c == '\n' {
			b = b[i+1:]
			break
		}
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		os.Rename(tmp, path)
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
