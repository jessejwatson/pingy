package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"pingy/internal/monitor"
)

// streamPoll is how often pingy __stream looks for new pings to send.
const streamPoll = 250 * time.Millisecond

// cmdStream is what another pingy runs here, over ssh, to see a host from
// this machine: it pings the host in the background, as pingy <host> -d
// would, so it carries on when the other pingy goes, then sends its pings
// since sinceMs and every new one as it comes. See monitor/remote.go.
func (a *App) cmdStream(args []string) error {
	if len(args) == 1 && args[0] == "--check" {
		fmt.Fprintln(a.Stdout, monitor.StreamHeader("pingy"))
		return nil
	}
	if len(args) != 3 {
		return errors.New("usage: pingy __stream <host> <interval ms> <since ms>")
	}
	iv, err1 := strconv.ParseInt(args[1], 10, 64)
	sinceMs, err2 := strconv.ParseInt(args[2], 10, 64)
	if err1 != nil || err2 != nil {
		return errors.New("usage: pingy __stream <host> <interval ms> <since ms>")
	}
	interval := max(time.Duration(iv)*time.Millisecond, monitor.MinInterval)
	if monitor.FindExact(a.root(), args[0]) != nil {
		interval = 0 // keep the one it's pinged at here
	}
	w := bufio.NewWriter(a.Stdout)
	fmt.Fprintln(w, monitor.StreamHeader("pingy"))
	m, _, _, err := a.start(args[0], startOpts{interval: interval})
	if err != nil {
		fmt.Fprintf(w, "! %v\n", err)
		return w.Flush()
	}
	since := time.Time{}
	if sinceMs > 0 {
		since = time.UnixMilli(sinceMs)
	}
	addr, problem, beat := "", "", time.Now()
	keep := func() bool {
		if _, err := os.Stat(m.Dir()); err != nil {
			fmt.Fprintln(w, "! no longer pinged there")
			return false
		}
		st := m.State()
		p := st.Link
		switch {
		case !m.Running():
			p = "paused there"
		case p == "" && st.Addr == "":
			p = st.Error
		}
		if st.Addr != addr && st.Addr != "" {
			addr = st.Addr
			fmt.Fprintln(w, "= "+addr)
		}
		if p != problem {
			problem = p
			fmt.Fprintln(w, strings.TrimSpace("! "+p))
		}
		if time.Since(beat) >= monitor.StreamBeat {
			beat = time.Now()
			fmt.Fprintln(w, ".")
		}
		return true
	}
	return monitor.Follow(m.SamplesPath(), since, w, streamPoll, keep)
}

// awaitRemote waits a little for m, just started, to reach its origin,
// and returns its state once it has, or has failed to and failing counts.
func awaitRemote(m *monitor.Meta, failing bool) monitor.State {
	deadline := time.Now().Add(15 * time.Second)
	for {
		st := m.State()
		if st.Via != "" || failing && st.Link != "" || time.Now().After(deadline) {
			return st
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// loginWhy explains, for someone about to answer ssh, why it's asking:
// kind is one of monitor's Login kinds.
func loginWhy(kind, origin, dest string) string {
	host := dest
	if i := strings.LastIndexByte(host, '@'); i >= 0 {
		host = host[i+1:]
	}
	switch kind {
	case monitor.LoginHostKey:
		return "ssh hasn't connected to " + origin + " before. Check the key it shows is " + origin +
			"'s, then type yes to trust it from now on."
	case monitor.LoginAuth:
		return origin + " wants a password or a code. Once you've logged in, pingy keeps the login open for every host pinged from there. " +
			"A key would save logging in again after a disconnect: ssh-copy-id " + dest
	case monitor.LoginChanged:
		return origin + "'s host key has CHANGED since ssh last connected. A reinstalled machine does that, but so does someone " +
			"intercepting the connection, so check with whoever runs it first. Once you're sure, remove the old key with " +
			"ssh-keygen -R " + host + " and log in again."
	}
	return "Log in to " + origin + " (ssh " + dest + "); pingy keeps the login open for every host pinged from there."
}

// canAnswer says whether logging in by hand would get past kind.
func canAnswer(kind string) bool { return kind == monitor.LoginHostKey || kind == monitor.LoginAuth }

// reportRemote says how m, just started, is being pinged from its origin.
func (a *App) reportRemote(m *monitor.Meta) {
	u := a.errUI()
	what := u.bold(m.Target)
	if m.Name != "" {
		what = u.bold(m.Name) + " " + u.faint("("+m.Target+")")
	}
	dest := monitor.Dest(a.Home, m.Origin)
	st := awaitRemote(m, true)
	if st.Link != "" && st.Login != "" && a.Interactive {
		a.notef("%s", st.Link)
		a.hint("  %s", loginWhy(st.Login, m.Origin, dest))
		if canAnswer(st.Login) && a.loginHere(dest) {
			a.retryOrigin(dest)
			st = awaitRemote(m, false)
		}
	}
	switch {
	case st.Link != "":
		a.notef("Started %s, but %s", u.bold(m.Label()), st.Link)
		if st.Login != "" {
			a.hint("  pingy keeps trying. Open pingy and press ctrl-l on it to log in.")
		} else {
			a.hint("  pingy keeps trying.")
		}
	case st.Via == "pingy":
		a.done("Pinging %s from %s with pingy there, which carries on while you're away", what, m.Origin)
	case st.Via == "script":
		a.done("Pinging %s from %s while you're connected; pingy isn't installed there, so a script pings it", what, m.Origin)
	default:
		a.notef("Started %s; still connecting to %s", u.bold(m.Label()), m.Origin)
	}
}

// remoteKillHint says how to stop a host on its origin as well, where
// pingy there carries on pinging it.
func (a *App) remoteKillHint(m *monitor.Meta, st monitor.State) {
	if m.Origin != "" && st.Via == "pingy" {
		a.hint("  pingy on %s still pings it; to stop it there too: ssh %s pingy -k %s",
			m.Origin, monitor.Dest(a.Home, m.Origin), m.Target)
	}
}

// cmdOrigin lists the origins (no args), shows one (a name), saves one
// (a name and where ssh reaches it), or with -k deletes them.
func (a *App) cmdOrigin(args []string, kill bool) error {
	list, err := monitor.LoadOrigins(a.Home)
	if err != nil {
		return err
	}
	u := a.errUI()
	switch {
	case kill:
		if len(args) == 0 {
			return errors.New("say which origin to delete, e.g. pingy --origin -k office")
		}
		for _, name := range args {
			i := monitor.FindOrigin(list, name)
			if i < 0 {
				return fmt.Errorf("there's no origin called %q; pingy --origin lists them", name)
			}
			if n := a.pingedFrom(list[i].Name); n > 0 {
				return fmt.Errorf("%d %s pinged from %s; pingy -k %s first",
					n, plural(n, "host is", "hosts are"), list[i].Name, plural(n, "it", "them"))
			}
			a.done("Deleted the origin %s", u.bold(list[i].Name))
			monitor.CloseLogin(a.Home, list[i].Dest)
			list = append(list[:i], list[i+1:]...)
		}
		return monitor.SaveOrigins(a.Home, list)
	case len(args) == 0:
		return a.listOrigins(list)
	case len(args) == 1:
		name := args[0]
		if i := monitor.FindOrigin(list, name); i >= 0 {
			return a.probeOrigin(list[i].Name, list[i].Dest)
		}
		if err := monitor.CheckDest(name); err != nil {
			return err
		}
		return a.probeOrigin(name, name)
	case len(args) == 2:
		name, dest := args[0], args[1]
		if err := monitor.CheckOriginName(name); err != nil {
			return err
		}
		if err := monitor.CheckDest(dest); err != nil {
			return err
		}
		if i := monitor.FindOrigin(list, name); i >= 0 {
			list[i] = monitor.Origin{Name: list[i].Name, Dest: dest}
		} else {
			list = append(list, monitor.Origin{Name: name, Dest: dest})
		}
		if err := monitor.SaveOrigins(a.Home, list); err != nil {
			return err
		}
		a.done("Saved %s as %s", u.bold(name), dest)
		return a.probeOrigin(name, dest)
	}
	return errors.New("use pingy --origin <name> <user@host> to name a machine to ping from")
}

// pingedFrom counts the hosts pinged from origin.
func (a *App) pingedFrom(origin string) int {
	n := 0
	for _, m := range monitor.List(a.root()) {
		if strings.EqualFold(m.Origin, origin) {
			n++
		}
	}
	return n
}

func (a *App) listOrigins(list []monitor.Origin) error {
	if len(list) == 0 {
		a.hint("No origins saved. Name a machine to ping from with: pingy --origin <name> <user@host>")
		a.hint("  or ping from one without naming it: pingy 1.1.1.1@user@host -d")
		return nil
	}
	u := a.outUI()
	var rows [][]string
	for _, o := range list {
		n := a.pingedFrom(o.Name)
		hosts := u.faint("-")
		if n > 0 {
			hosts = strconv.Itoa(n)
		}
		rows = append(rows, []string{u.accent(o.Name), o.Dest, hosts})
	}
	fmt.Fprintln(a.Stdout, table2(u, []string{"ORIGIN", "SSH", "HOSTS"}, rows))
	a.hint("pingy <host>@<origin> -d to ping from one · pingy --origin <name> to check it")
	return nil
}

// probeOrigin connects to an origin to say whether pingy can ping from it,
// and how, logging in there first if ssh needs someone to.
func (a *App) probeOrigin(name, dest string) error {
	login, err := a.probe(name, dest)
	if err != nil && login != "" && a.Interactive {
		a.notef("%v", err)
		a.hint("  %s", loginWhy(login, name, dest))
		if !canAnswer(login) || !a.loginHere(dest) {
			return exitCode(1)
		}
		a.retryOrigin(dest)
		_, err = a.probe(name, dest)
	}
	return err
}

func (a *App) probe(name, dest string) (login string, err error) {
	u := a.errUI()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	remote, in := monitor.RemoteCommand("--probe", time.Second, time.Time{})
	cmd := monitor.SSH(a.Home, dest, remote, in)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			if cmd.Process != nil {
				cmd.Process.Kill()
			}
		case <-done:
		}
	}()
	out, err := cmd.Output()
	close(done)
	if err != nil {
		why := strings.TrimSpace(stderr.String())
		if i := strings.LastIndexByte(why, '\n'); i >= 0 {
			why = why[i+1:]
		}
		if why == "" {
			why = err.Error()
		}
		if ctx.Err() != nil {
			return "", fmt.Errorf("can't reach %s: no answer after 20 seconds", name)
		}
		login = monitor.LoginNeeded(stderr.String())
		if login == "" {
			return "", fmt.Errorf("can't reach %s: %s", name, why)
		}
		return login, fmt.Errorf("can't log in to %s: %s", name, why)
	}
	got := strings.Fields(string(out))
	switch {
	case len(got) >= 2 && got[0] == "pingy":
		a.done("%s has pingy %s: hosts pinged from there carry on while you're away, and catch up when you're back",
			u.bold(name), got[1])
	case len(got) >= 2 && got[0] == "old":
		a.notef("%s has pingy %s, which is too old to stream; until it's updated, hosts are pinged there by a script, only while you're connected",
			u.bold(name), got[1])
	default:
		a.done("%s is reachable, without pingy: hosts are pinged there by a script, only while you're connected",
			u.bold(name))
	}
	return "", nil
}
