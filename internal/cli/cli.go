// Package cli implements the pingy commands.
package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"pingy/internal/monitor"
)

// App holds everything a command needs.
type App struct {
	Version string
	Stdout  io.Writer
	Stderr  io.Writer
	// Interactive is true when stdin and stdout are both terminals.
	Interactive        bool
	StyleOut, StyleErr bool
	Icons              string
	Width, Height      int
	// Home is where pingy keeps its monitors: $PINGY_HOME, or
	// ~/.config/pingy.
	Home string
}

// exitCode carries a specific exit status up to Run without a message.
type exitCode int

func (e exitCode) Error() string { return fmt.Sprintf("exit status %d", int(e)) }

// NewApp wires an App to the real process environment.
func NewApp(version string) *App {
	a := &App{
		Version:  version,
		Stdout:   os.Stdout,
		Stderr:   os.Stderr,
		StyleOut: styleWanted(os.Stdout),
		StyleErr: styleWanted(os.Stderr),
		Icons:    os.Getenv("PINGY_ICONS"),
	}
	a.Interactive = isTerminal(os.Stdin) && isTerminal(os.Stdout)
	a.Width, a.Height = termSize()
	if !isTerminal(os.Stdout) {
		a.Height = 0
	}
	a.Home = os.Getenv("PINGY_HOME")
	if a.Home == "" {
		if h, err := os.UserHomeDir(); err == nil {
			a.Home = filepath.Join(h, ".config", "pingy")
		}
	}
	return a
}

func (a *App) root() string { return filepath.Join(a.Home, "hosts") }

// Run executes pingy with args and returns the exit code.
func (a *App) Run(args []string) int {
	err := a.run(args)
	var code exitCode
	switch {
	case err == nil:
		return 0
	case errors.As(err, &code):
		return int(code)
	}
	if u := a.errUI(); u.on {
		fmt.Fprintf(a.Stderr, "%s%s\n", u.icon(u.icons.err, colBad), u.fg(colBad, "error:")+" "+err.Error())
	} else {
		fmt.Fprintf(a.Stderr, "pingy: %v\n", err)
	}
	return 1
}

// opts is the parsed command line.
type opts struct {
	detach, list, graph, kill, pause, resume, all, history bool
	origin                                                 bool
	name                                                   string
	interval                                               time.Duration
	targets                                                []string
	// from is the origins --from gave, to pair with every target.
	from []string
}

func parseArgs(args []string) (opts, error) {
	var o opts
	value := func(i *int, flag string) (string, error) {
		if *i+1 >= len(args) {
			return "", fmt.Errorf("%s needs a value", flag)
		}
		*i++
		return args[*i], nil
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		flag, inline, hasInline := strings.Cut(arg, "=")
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			o.targets = append(o.targets, arg)
			continue
		}
		switch flag {
		case "-d", "--detach":
			o.detach = true
		case "-l", "--list", "--ls":
			o.list = true
		case "-g", "--graph":
			o.graph = true
		case "-k", "--kill":
			o.kill = true
		case "-p", "--pause":
			o.pause = true
		case "-r", "--resume":
			o.resume = true
		case "-a", "--all":
			o.all = true
		case "--history":
			o.history = true
			if hasInline {
				o.targets = append(o.targets, inline)
			}
		case "--origin", "--origins":
			o.origin = true
		case "--from":
			v := inline
			if !hasInline {
				var err error
				if v, err = value(&i, flag); err != nil {
					return o, err
				}
			}
			for _, f := range strings.Split(v, ",") {
				if f = strings.TrimSpace(f); f != "" {
					o.from = append(o.from, f)
				}
			}
		case "-n", "--name", "-i", "--interval":
			v := inline
			if !hasInline {
				var err error
				if v, err = value(&i, flag); err != nil {
					return o, err
				}
			}
			if flag == "-n" || flag == "--name" {
				o.name = v
				continue
			}
			d, err := parseInterval(v)
			if err != nil {
				return o, err
			}
			o.interval = d
		case "--":
			o.targets = append(o.targets, args[i+1:]...)
			i = len(args)
		default:
			return o, fmt.Errorf("unknown option %s; see pingy --help", arg)
		}
	}
	return o, nil
}

// parseInterval reads seconds ("0.5") or a duration ("500ms").
func parseInterval(v string) (time.Duration, error) {
	d, err := time.ParseDuration(v)
	if err != nil {
		f, ferr := strconv.ParseFloat(v, 64)
		if ferr != nil {
			return 0, fmt.Errorf("can't read interval %q; give seconds, e.g. 2 or 0.5, or a duration like 500ms", v)
		}
		d = time.Duration(f * float64(time.Second))
	}
	if d < monitor.MinInterval {
		return 0, fmt.Errorf("the interval can't be shorter than %s", monitor.MinInterval)
	}
	return d, nil
}

func (a *App) run(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "help", "-h", "--help":
			fmt.Fprint(a.Stdout, a.outUI().help(helpText))
			return nil
		case "version", "-v", "--version":
			fmt.Fprintf(a.Stdout, "%s %s\n", a.outUI().accent("pingy"), a.Version)
			return nil
		case "__monitor":
			if len(args) != 2 {
				return errors.New("usage: pingy __monitor <dir>")
			}
			return monitor.Run(args[1])
		case "__stream":
			if a.Home == "" {
				return errors.New("can't tell where to keep pingy's data; set PINGY_HOME")
			}
			return a.cmdStream(args[1:])
		}
	}
	if a.Home == "" {
		return errors.New("can't tell where to keep pingy's data; set PINGY_HOME")
	}
	o, err := parseArgs(args)
	if err != nil {
		return err
	}
	if o.origin {
		if o.list || o.graph || o.pause || o.resume || o.history || o.detach || len(o.from) > 0 {
			return errors.New("use pingy --origin on its own, or with -k to delete one")
		}
		return a.cmdOrigin(o.targets, o.kill)
	}
	if len(o.from) > 0 {
		if len(o.targets) == 0 {
			return errors.New("say which host to ping from there, e.g. pingy 1.1.1.1 --from office -d")
		}
		var specs []string
		for _, t := range o.targets {
			if _, origin := monitor.SplitSpec(t); origin != "" {
				return fmt.Errorf("%s already says where it's pinged from; leave out --from", t)
			}
			for _, f := range o.from {
				specs = append(specs, monitor.JoinSpec(t, f))
			}
		}
		o.targets = specs
	}
	modes := 0
	for _, on := range []bool{o.list, o.graph, o.kill, o.pause, o.resume, o.history} {
		if on {
			modes++
		}
	}
	if modes > 1 {
		return errors.New("use one of -l, -g, -k, -p, -r and --history at a time")
	}
	switch {
	case o.list:
		return a.cmdList(o.targets)
	case o.graph:
		return a.cmdGraph(o.targets)
	case o.kill:
		return a.cmdKill(o.targets, o.all)
	case o.pause:
		return a.cmdPause(o.targets)
	case o.resume:
		return a.cmdResume(o.targets)
	case o.history:
		return a.cmdHistory(o.targets)
	case len(o.targets) == 0:
		if o.detach || o.name != "" || o.interval != 0 {
			return errors.New("say which host to ping, e.g. pingy 1.1.1.1 -d")
		}
		return a.cmdPick()
	case len(o.targets) > 1 && !o.detach:
		return errors.New("pingy watches one host at a time; add -d to start several in the background")
	case len(o.targets) > 1 && o.name != "":
		return errors.New("-n names one host; start them one at a time to name each")
	case o.detach:
		for _, t := range o.targets {
			if err := a.cmdDetach(t, o.name, o.interval); err != nil {
				return err
			}
		}
		return nil
	}
	return a.cmdWatch(o.targets[0], o.name, o.interval)
}

// startOpts says how to start a monitor.
type startOpts struct {
	name       string
	interval   time.Duration
	foreground bool // stop when this pingy exits
}

// start begins pinging spec, a target or target@origin, reusing a
// stopped monitor for it (found by name, target or number) so its history
// carries on. It reports whether the monitor was already running and
// whether it's new.
func (a *App) start(spec string, so startOpts) (m *monitor.Meta, already, created bool, err error) {
	root := a.root()
	target, origin := monitor.SplitSpec(spec)
	if m = monitor.FindExact(root, spec); m != nil {
		if m.Running() {
			return m, true, false, nil
		}
	} else {
		switch {
		case strings.HasSuffix(spec, "@"):
			return nil, false, false, fmt.Errorf("say where to ping %s from after the @, e.g. %soffice", target, spec)
		case origin != "":
			// The origin may know names this machine doesn't, so the
			// target is left for it to look up.
			if err := monitor.CheckRemoteTarget(target); err != nil {
				return nil, false, false, err
			}
			if err := monitor.CheckDest(monitor.Dest(a.Home, origin)); err != nil {
				return nil, false, false, err
			}
		default:
			if _, err := monitor.Resolve(target); err != nil {
				return nil, false, false, fmt.Errorf("%v; check the address, or that you're online", err)
			}
		}
		if so.name != "" {
			if err := a.checkHostName(so.name, ""); err != nil {
				return nil, false, false, err
			}
		}
		interval := so.interval
		if interval == 0 {
			interval = monitor.DefaultInterval
		}
		if m, err = monitor.Create(root, target, origin, interval); err != nil {
			return nil, false, false, err
		}
		created = true
	}
	if so.name != "" && !created {
		if err := a.checkHostName(so.name, m.ID); err != nil {
			return nil, false, false, err
		}
	}
	if so.name != "" {
		m.Name = so.name
	}
	if so.interval != 0 {
		m.Interval = so.interval.Milliseconds()
	}
	m.Foreground, m.Ephemeral, m.OwnerPID = so.foreground, so.foreground && created, 0
	if so.foreground {
		m.OwnerPID = os.Getpid()
	}
	if err := m.Save(); err != nil {
		return nil, false, false, err
	}
	if err := a.spawn(m); err != nil {
		if created {
			os.RemoveAll(m.Dir())
		}
		return nil, false, false, err
	}
	return m, false, created, nil
}

// spawn starts pingy __monitor for m in its own session, so it outlives
// this terminal, and waits for it to be up.
func (a *App) spawn(m *monitor.Meta) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	logf, err := os.OpenFile(m.LogPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	cmd := exec.Command(exe, "__monitor", m.Dir())
	cmd.Env = append(os.Environ(), "PINGY_HOME="+a.Home)
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	pid := cmd.Process.Pid
	cmd.Process.Release()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if m.Running() && m.State().PID == pid {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("the monitor for %s didn't start; see %s", m.Label(), m.LogPath())
}

func (a *App) cmdDetach(target, name string, interval time.Duration) error {
	m, already, _, err := a.start(target, startOpts{name: name, interval: interval})
	if err != nil {
		return err
	}
	u := a.errUI()
	if already {
		a.notef("Already pinging %s", u.bold(m.Label()))
		if name != "" && name != m.Name {
			if err := a.rename(m, name); err != nil {
				return err
			}
			a.done("Renamed it %s", u.bold(name))
		}
		return nil
	}
	if m.Origin != "" {
		a.reportRemote(m)
		a.hint("  pingy -l to list · pingy -g %s to graph it from everywhere · pingy to open", m.Target)
		return nil
	}
	label := u.bold(m.Label())
	if m.Name != "" {
		label += " " + u.faint("("+m.Target+")")
	}
	a.done("Pinging %s in the background, every %s", label, m.Every())
	a.hint("  pingy -l to list · pingy -g to graph · pingy to open")
	return nil
}

// rename gives m a new name; "" goes back to showing its target.
func (a *App) rename(m *monitor.Meta, name string) error {
	if err := a.checkHostName(name, m.ID); err != nil {
		return err
	}
	fresh, err := m.Reload()
	if err != nil {
		return err
	}
	if name == fresh.Where() {
		name = ""
	}
	fresh.Name = name
	m.Name = name
	return fresh.Save()
}

// cmdWatch opens target's graph, pinging it until q, or until d leaves it
// running in the background.
func (a *App) cmdWatch(target, name string, interval time.Duration) error {
	if !a.Interactive {
		return errors.New("watching needs a terminal; add -d to ping in the background")
	}
	m, already, created, err := a.start(target, startOpts{name: name, interval: interval, foreground: true})
	if err != nil {
		return err
	}
	s := &session{meta: m, owned: !already, created: created}
	if err := a.runTUI(newDetailModel(a, s)); err != nil {
		return err
	}
	u := a.errUI()
	switch {
	case s.killed:
		a.done("Stopped pinging %s and deleted its history", u.bold(m.Label()))
	case !s.owned:
	case s.detached:
		a.done("Still pinging %s in the background", u.bold(s.meta.Label()))
		a.hint("  pingy -l to list · pingy to open")
	default:
		fresh, err := s.meta.Reload()
		if err != nil {
			return nil
		}
		if created {
			return fresh.Remove()
		}
		fresh.Foreground, fresh.Ephemeral, fresh.OwnerPID = false, false, 0
		fresh.Save()
		return fresh.Stop()
	}
	return nil
}

// detach makes a foreground monitor carry on after pingy exits.
func (s *session) detach() error {
	fresh, err := s.meta.Reload()
	if err != nil {
		return err
	}
	fresh.Foreground, fresh.Ephemeral, fresh.OwnerPID = false, false, 0
	if err := fresh.Save(); err != nil {
		return err
	}
	s.meta, s.detached = fresh, true
	return nil
}

// session is a pingy <host> run: whether it started the monitor, and how
// it was left.
type session struct {
	meta     *monitor.Meta
	owned    bool // started by this pingy, so q stops it
	created  bool // new, so q deletes it
	detached bool
	killed   bool
}

func (a *App) cmdKill(targets []string, all bool) error {
	root := a.root()
	var list []*monitor.Meta
	switch {
	case all && len(targets) == 0:
		list = monitor.List(root)
		if len(list) == 0 {
			a.hint("Nothing is being pinged.")
			return nil
		}
	case all:
		return errors.New("use -k --all on its own, or name the hosts to stop")
	case len(targets) == 0:
		return errors.New("say which host to stop, e.g. pingy -k 1.1.1.1, or pingy -k --all")
	}
	for _, t := range targets {
		m := monitor.Find(root, t)
		if m == nil {
			return fmt.Errorf("nothing called %q is being pinged; pingy -l lists them", t)
		}
		list = append(list, m)
	}
	u := a.errUI()
	for _, m := range list {
		st := m.State()
		if err := m.Remove(); err != nil {
			return err
		}
		a.done("Stopped pinging %s and deleted its history", u.bold(m.Label()))
		a.remoteKillHint(m, st)
	}
	return nil
}

// cmdResume starts stopped monitors again, e.g. after a restart.
func (a *App) cmdResume(targets []string) error {
	root := a.root()
	var list []*monitor.Meta
	if len(targets) == 0 {
		for _, m := range monitor.List(root) {
			if !m.Running() {
				list = append(list, m)
			}
		}
		if len(list) == 0 {
			a.hint("Nothing to resume; every host is being pinged.")
			return nil
		}
	}
	if len(targets) > 0 {
		var err error
		if list, _, err = a.expand(targets); err != nil {
			return err
		}
	}
	u := a.errUI()
	for _, m := range list {
		if m.Running() {
			a.notef("Already pinging %s", u.bold(m.Label()))
			continue
		}
		m.Foreground, m.Ephemeral, m.OwnerPID = false, false, 0
		if err := m.Save(); err != nil {
			return err
		}
		if err := a.spawn(m); err != nil {
			return err
		}
		a.done("Pinging %s again", u.bold(m.Label()))
	}
	return nil
}
