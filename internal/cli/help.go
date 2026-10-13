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
  pingy -l                      List the hosts being pinged
  pingy -g [host...]            Graph every host (or just these), live
  pingy -k <host>...            Stop pinging hosts and delete their history
  pingy -k --all                Stop them all
  pingy -r [host...]            Ping stopped hosts again, e.g. after a restart

Options:
  -d, --detach                  Run in the background
  -n, --name <name>             Name the host (it shows its address otherwise)
  -i, --interval <secs>         Time between pings, e.g. 0.5 or 2s; default 1
  -h, --help                    Show this help
  -v, --version                 Show the version

A host is a name or address, e.g. 1.1.1.1, router.lan or example.com. Hosts
can also be given by their pingy name.

In the panel:
  type                          Search; enter opens the chosen host's graph
  enter on "+ Ping …"           Start pinging what you typed
  ctrl-x · ctrl-s               Stop and delete · pause or resume
  esc                           Quit

In a graph:
  ← → (h l)                     Move back and forward in time
  shift-← → (H L) · pgup pgdn   A whole screen at a time
  home · end                    Oldest · now, following new pings
  + -                           Zoom in and out
  n · s · x                     Rename · pause or resume · stop and delete
  esc                           Back

In pingy -g, as in a graph, plus:
  ↑ ↓ (k j)                     Scroll to the next or previous host
  ctrl-u ctrl-d (K J)           A screen of hosts at a time

Environment:
  PINGY_HOME                    Where pingy keeps hosts; default ~/.config/pingy
  PINGY_ICONS=nerd              Nerd Font icons
  NO_COLOR                      Plain output
`
