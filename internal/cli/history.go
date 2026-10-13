package cli

import (
	"errors"
	"fmt"
	"time"

	"pingy/internal/monitor"
)

// historyUse is how much disk the hosts' history takes, and how much
// each may take.
type historyUse struct {
	used  int64
	hosts int
	limit monitor.Settings
}

func (a *App) historyUse() (historyUse, error) {
	s, err := monitor.LoadSettings(a.Home)
	hu := historyUse{limit: s}
	for _, m := range monitor.List(a.root()) {
		hu.used += m.HistorySize()
		hu.hosts++
	}
	return hu, err
}

// setHistory saves how many MB each host keeps, trimming paused hosts'
// history now; running ones trim their own when they next ping.
func (a *App) setHistory(mb int) error {
	s, err := monitor.LoadSettings(a.Home)
	if err != nil {
		return err
	}
	s.HistoryMB = mb
	if err := monitor.SaveSettings(a.Home, s); err != nil {
		return err
	}
	for _, m := range monitor.List(a.root()) {
		m.TrimHistory(s.HistoryBytes())
	}
	return nil
}

// fmtKeeps says about how far back mb of history reaches at one ping a
// second, e.g. "about 14–19 days".
func fmtKeeps(s monitor.Settings) string {
	least, most := s.HistorySpan(time.Second)
	if most >= 72*time.Hour {
		return fmt.Sprintf("about %d–%d days", int(least.Hours()/24), int(most.Hours()/24))
	}
	return fmt.Sprintf("about %d–%d hours", int(least.Hours()), int(most.Hours()))
}

// cmdHistory shows how much disk history takes, or with a size, sets how
// much each host keeps.
func (a *App) cmdHistory(args []string) error {
	u := a.errUI()
	switch len(args) {
	case 0:
		hu, err := a.historyUse()
		if err != nil {
			return err
		}
		fmt.Fprintf(a.Stdout, "%s of history across %d %s\n",
			a.outUI().bold(monitor.FormatBytes(hu.used)), hu.hosts, plural(hu.hosts, "host", "hosts"))
		fmt.Fprintf(a.Stdout, "Each host keeps up to %s, %s at one ping a second\n",
			a.outUI().bold(monitor.FormatMB(hu.limit.HistoryMB)), fmtKeeps(hu.limit))
		a.hint("  pingy --history <size> to change it, e.g. 64M or 1G")
		return nil
	case 1:
		mb, err := monitor.ParseSize(args[0])
		if err != nil {
			return err
		}
		if err := a.setHistory(mb); err != nil {
			return err
		}
		s := monitor.Settings{HistoryMB: mb}
		a.done("Each host now keeps up to %s of history, %s at one ping a second",
			u.bold(monitor.FormatMB(mb)), fmtKeeps(s))
		return nil
	}
	return errors.New("give one size, e.g. pingy --history 64M")
}
