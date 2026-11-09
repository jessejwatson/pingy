package cli

import (
	"fmt"
	"strings"

	"pingy/internal/monitor"
)

// config is a setting pingy config shows and turns on or off.
type config struct {
	name, about string
	get         func(monitor.Settings) bool
	set         func(*monitor.Settings, bool)
}

var configs = []config{{
	name:  "ask-to-keep",
	about: "On quitting pingy <host> with hosts added to it, ask whether to keep them pinging; off stops them, as q stops the host",
	get:   func(s monitor.Settings) bool { return !s.NoKeepAsk },
	set:   func(s *monitor.Settings, on bool) { s.NoKeepAsk = !on },
}}

func findConfig(name string) (config, error) {
	for _, c := range configs {
		if c.name == name {
			return c, nil
		}
	}
	var names []string
	for _, c := range configs {
		names = append(names, c.name)
	}
	return config{}, fmt.Errorf("there's no setting %q; there's %s", name, strings.Join(names, ", "))
}

func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

// cmdConfig shows pingy's settings, or one, or turns one on or off.
func (a *App) cmdConfig(args []string) error {
	s, err := monitor.LoadSettings(a.Home)
	if err != nil {
		return err
	}
	switch len(args) {
	case 0:
		u := a.outUI()
		w := 0
		for _, c := range configs {
			w = max(w, len(c.name))
		}
		for _, c := range configs {
			fmt.Fprintf(a.Stdout, "%s  %-3s  %s\n", u.fg(colCmd, fmt.Sprintf("%-*s", w, c.name)), onOff(c.get(s)), u.faint(c.about))
		}
		a.hint("  pingy config <name> on|off to change one")
		return nil
	case 1:
		c, err := findConfig(args[0])
		if err != nil {
			return err
		}
		fmt.Fprintln(a.Stdout, onOff(c.get(s)))
		return nil
	case 2:
		c, err := findConfig(args[0])
		if err != nil {
			return err
		}
		if args[1] != "on" && args[1] != "off" {
			return fmt.Errorf("%s is on or off", c.name)
		}
		if err := a.setConfig(c.name, args[1] == "on"); err != nil {
			return err
		}
		a.done("%s is %s", c.name, args[1])
		return nil
	}
	return fmt.Errorf("pingy config takes a setting's name, then on or off")
}

// setConfig turns the setting called name on or off.
func (a *App) setConfig(name string, on bool) error {
	c, err := findConfig(name)
	if err != nil {
		return err
	}
	s, err := monitor.LoadSettings(a.Home)
	if err != nil {
		return err
	}
	c.set(&s, on)
	return monitor.SaveSettings(a.Home, s)
}
