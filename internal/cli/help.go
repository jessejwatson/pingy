package cli

import (
	"regexp"
	"strings"
)

var (
	helpRow  = regexp.MustCompile(`^(\s+)(\S.*?)(\s{2,})(\S.*)$`)
	helpArgs = regexp.MustCompile(`<[^>]*>|\[[^\]]*\]`)
)

// help colours the help text: headings, then commands in the left column
// with their <args> and [options] dimmed.
func (u ui) help(text string) string {
	if !u.on {
		return text
	}
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		trimmed := strings.TrimSpace(l)
		indent := len(l) - len(strings.TrimLeft(l, " "))
		switch {
		case i == 0:
			name, rest, _ := strings.Cut(l, " ")
			lines[i] = u.accent(name) + " " + u.bold(rest)
		case indent == 0 && strings.HasSuffix(l, ":"):
			lines[i] = u.accent(l)
		case strings.HasPrefix(trimmed, "pingy") || strings.HasPrefix(trimmed, "-"):
			if m := helpRow.FindStringSubmatch(l); m != nil {
				lines[i] = m[1] + u.helpCmd(m[2]) + m[3] + m[4]
			}
		}
	}
	return strings.Join(lines, "\n")
}

func (u ui) helpCmd(cmd string) string {
	var b strings.Builder
	last := 0
	for _, m := range helpArgs.FindAllStringIndex(cmd, -1) {
		b.WriteString(u.fg(colCmd, cmd[last:m[0]]))
		b.WriteString(u.faint(cmd[m[0]:m[1]]))
		last = m[1]
	}
	b.WriteString(u.fg(colCmd, cmd[last:]))
	return b.String()
}

const helpText = `pingy — keep an eye on many pings at once

Usage:
  pingy                         Open the panel: search hosts, see their graphs
  pingy <host>                  Ping a host and watch its graph; d leaves it
                                running in the background, q stops it
  pingy <host> -d               Ping a host in the background
  pingy <host>... -d            Several at once
  pingy -l                      List the hosts being pinged, and saved views
  pingy -g [host...]            Graph every host (or just these), live
  pingy -g <view>               Graph a saved view's hosts; -l, -p and -r
                                take views too
  pingy -k <host>...            Stop pinging hosts and delete their history
  pingy -k --all                Stop them all
  pingy -p [host...]            Pause hosts (every host by default), keeping
                                their history; pingy -r pings them again
  pingy -r [host...]            Ping paused hosts again, e.g. after a restart
  pingy --history [size]        Show how much disk history takes, or set how
                                much each host keeps, e.g. 64M or 1G; default
                                32M, about two weeks at one ping a second
  pingy <host>@<origin> -d      Ping a host from another machine, over ssh
  pingy <host> --from <a,b> -d  From several; pingy -g <host> graphs it from
                                everywhere it's pinged from
  pingy --origin <name> <ssh>   Name a machine to ping from, e.g. office
                                jesse@office.lan, and check pingy can reach it
  pingy --origin [name]         List them, or check one
  pingy --origin -k <name>      Delete one

Options:
  -d, --detach                  Run in the background
  -n, --name <name>             Name the host (it shows its address otherwise)
  -i, --interval <secs>         Time between pings, e.g. 0.5 or 2s; default 1
      --from <origin,...>       Ping from these machines, not this one
  -h, --help                    Show this help
  -v, --version                 Show the version

A host is a name or address, e.g. 1.1.1.1, router.lan or example.com. Hosts
can also be given by their pingy name.

An origin is a machine a host is pinged from: a name saved with --origin,
or anything ssh takes, e.g. 1.1.1.1@jesse@office.lan. When ssh can't get
in by itself, e.g. it doesn't know the machine yet or wants a password,
pingy asks you to log in, and keeps that login open for every host pinged
from there until the last is stopped. Where pingy is installed, it pings the host there in
the background, so it carries on while you're away and catches up when
you're back; pingy -k here leaves it running there. Anywhere else, a
script pings it, only while you're connected.

A view is a group of hosts graphed together under a one-word name: pick
them in the panel with ctrl-t, or run pingy -g with them, then press s to
save it. A view finds a host again if it's deleted and later pinged again,
and never starts pinging anything by itself.

In the panel:
  type                          Search; enter opens the chosen host or view
  enter on "+ Ping …"           Start pinging what you typed
  ctrl-x · ctrl-p               Stop and delete (a view: just the view) · pause
                                or resume (a view: all its hosts)
  ctrl-a                        Pause every host, or resume them all if none
                                is running
  ctrl-o                        Set how much history each host keeps
  ctrl-l                        Log in to the chosen host's origin, e.g. to
                                accept its key or type a password
  ctrl-t                        Pick several hosts: tab picks the chosen one
                                (or a view's), enter graphs them together, esc
                                stops picking
  esc                           Quit

In a graph:
  ← → (h l)                     Move back and forward in time
  shift-← → (H L) · pgup pgdn   A whole screen at a time
  home · end                    Oldest · now, following new pings
  + -                           Zoom in and out
  n · p · x                     Rename · pause or resume · stop and delete
  ctrl-l                        Log in to the host's origin
  esc                           Back

In pingy -g, or hosts picked together, as in a graph, plus:
  ↑ ↓ (k j)                     Choose the next or previous host
  shift-↑ ↓ (K J)               Move the chosen host up or down; a saved view
                                keeps the new order
  ctrl-u ctrl-d                 A screen of hosts at a time
  a · r                         Add or remove hosts · remove the chosen one;
                                a saved view keeps the change
  p                             Pause these hosts, or resume them if all are
                                paused
  s                             Save these hosts as a view, or rename this one

Environment:
  PINGY_HOME                    Where pingy keeps hosts; default ~/.config/pingy
  PINGY_SSH                     The ssh to run, e.g. "ssh -F ~/.ssh/other"
  PINGY_ICONS=nerd              Nerd Font icons
  PINGY_NO_UPDATE_CHECK=1       Don't look once a day for a newer pingy
  NO_COLOR                      Plain output
`
