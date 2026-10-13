package cli

import (
	"fmt"
	"image/color"
	"strings"
	"text/tabwriter"
	"time"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"

	"pingy/internal/monitor"
)

// statsWindow is how far back the AVG and LOSS figures look.
const statsWindow = 5 * time.Minute

// previewTail is how much history a list or preview reads, about 4½ hours
// at one ping a second; a host's own graph reads all of it.
const previewTail = 256 << 10

// host is a monitor as pingy shows it.
type host struct {
	meta    *monitor.Meta
	state   monitor.State
	running bool
	hist    *monitor.History
}

func loadHost(m *monitor.Meta, tail int64) *host {
	h := &host{meta: m, hist: monitor.NewHistory(m.SamplesPath(), tail)}
	h.refresh(m)
	return h
}

func (h *host) refresh(m *monitor.Meta) {
	h.meta, h.state, h.running = m, m.State(), m.Running()
	h.hist.Refresh()
}

func (h *host) samples() []monitor.Sample { return h.hist.Samples }

// recent is the last statsWindow of samples.
func (h *host) recent(now time.Time) []monitor.Sample {
	return monitor.Since(h.samples(), now.Add(-statsWindow))
}

// status is a host's state in a word, with a colour and its icon.
func (u ui) status(h *host, now time.Time) (glyph, word string, c color.Color) {
	s := h.samples()
	switch {
	case !h.running:
		return u.icons.stopped, "stopped", colBorder
	case h.state.Error != "" && h.state.Addr == "":
		return u.icons.down, "can't resolve", colBad
	case len(s) == 0:
		return u.icons.up, "starting", colBorder
	case now.Sub(s[len(s)-1].At) > 3*h.meta.Every()+3*time.Second:
		return u.icons.warn, "stalled", colWarn
	}
	lost := 0
	for i := len(s) - 1; i >= 0 && s[i].Lost(); i-- {
		lost++
	}
	switch {
	case lost >= 3:
		return u.icons.down, "down", colBad
	case lost > 0:
		return u.icons.down, "dropping", colWarn
	}
	return u.icons.up, "up", colOK
}

// lastRTT is the most recent ping's result.
func (u ui) lastRTT(h *host) string {
	s := h.samples()
	switch {
	case len(s) == 0:
		return u.faint("-")
	case s[len(s)-1].Lost():
		return u.fg(colBad, "lost")
	}
	return fmtMs(s[len(s)-1].Ms())
}

// loadHosts loads the hosts and views named, or every host.
func (a *App) loadHosts(filter []string) ([]*host, error) {
	list := monitor.List(a.root())
	if len(filter) > 0 {
		var err error
		if list, _, err = a.expand(filter); err != nil {
			return nil, err
		}
	}
	hosts := make([]*host, len(list))
	for i, m := range list {
		hosts[i] = loadHost(m, previewTail)
	}
	return hosts, nil
}

// cmdList prints every host as a table.
func (a *App) cmdList(filter []string) error {
	hosts, err := a.loadHosts(filter)
	if err != nil {
		return err
	}
	if len(hosts) == 0 {
		a.hint("Nothing is being pinged. Start with: pingy <host> -d")
		return nil
	}
	u := a.outUI()
	now := time.Now()
	headers := []string{"NAME", "TARGET", "ADDRESS", "STATUS", "LAST", "AVG", "LOSS", "RECENT", "UP"}
	var rows [][]string
	stopped := 0
	for _, h := range hosts {
		glyph, word, c := u.status(h, now)
		if !h.running {
			stopped++
		}
		target := u.faint("-")
		if h.meta.Name != "" {
			target = h.meta.Target
		}
		addr := h.state.Addr
		if addr == "" || addr == h.meta.Target {
			addr = u.faint("-")
		} else {
			addr = u.fg(colCmd, addr)
		}
		st := monitor.Summarise(h.recent(now))
		avg, loss := u.faint("-"), u.faint("-")
		if st.Sent > 0 {
			loss = fmt.Sprintf("%.1f%%", st.LossPct())
			if st.Lost > 0 {
				loss = u.fg(colBad, loss)
			}
		}
		if st.Sent > st.Lost {
			avg = fmtMs(st.Avg)
		}
		up := u.faint("-")
		if h.running && !h.state.Started.IsZero() {
			up = u.faint(fmtAgo(now.Sub(h.state.Started)))
		}
		state := word
		if glyph != "" && u.on {
			state = glyph + " " + word
		}
		rows = append(rows, []string{
			u.accent(h.meta.Label()), target, addr, u.fg(c, state), u.lastRTT(h), avg, loss,
			u.sparkline(h.samples(), now, fitStep(time.Second, h.meta.Every()), 20), up,
		})
	}
	fmt.Fprintln(a.Stdout, table2(u, headers, rows))
	if stopped > 0 {
		a.hint("%d stopped · pingy -r to ping %s again", stopped, plural(stopped, "it", "them"))
	}
	if len(filter) == 0 {
		a.listViews()
	}
	return nil
}

// table2 lays out rows under headers, aligned by what shows.
func table2(u ui, headers []string, rows [][]string) string {
	if u.on {
		t := table.New().
			Border(lipgloss.HiddenBorder()).
			BorderHeader(false).
			Headers(headers...).
			Rows(rows...).
			StyleFunc(func(row, col int) lipgloss.Style {
				s := lipgloss.NewStyle().PaddingRight(2)
				if row == table.HeaderRow {
					s = s.Faint(true)
				}
				return s
			})
		lines := strings.Split(t.String(), "\n")
		if len(lines) > 2 {
			lines = lines[1 : len(lines)-1]
		}
		for i, l := range lines {
			lines[i] = strings.TrimRight(strings.TrimPrefix(l, " "), " ")
		}
		return strings.Join(lines, "\n")
	}
	var b strings.Builder
	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, strings.Join(headers, "\t"))
	for _, r := range rows {
		fmt.Fprintln(w, strings.Join(r, "\t"))
	}
	w.Flush()
	return strings.TrimRight(b.String(), "\n")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// listViews prints the saved views under pingy -l's table.
func (a *App) listViews() {
	vs, err := a.loadViews()
	if err != nil {
		a.hint("%v", err)
		return
	}
	if len(vs) == 0 {
		return
	}
	u := a.outUI()
	all := monitor.List(a.root())
	var rows [][]string
	for _, v := range vs {
		found, gone := v.resolve(all)
		var names []string
		for _, m := range found {
			names = append(names, m.Label())
		}
		if gone > 0 {
			names = append(names, u.faint(fmt.Sprintf("%d no longer pinged", gone)))
		}
		rows = append(rows, []string{u.accent(v.Name), strings.Join(names, ", ")})
	}
	fmt.Fprintln(a.Stdout)
	fmt.Fprintln(a.Stdout, table2(u, []string{"VIEW", "HOSTS"}, rows))
	a.hint("pingy -g <view> to graph one")
}
