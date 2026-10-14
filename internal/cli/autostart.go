package cli

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"pingy/internal/monitor"
)

// pingy --autostart on has the hosts that a restart or logging out
// stopped start again when you log in: a LaunchAgent on macOS, a systemd
// user service on Linux, each running pingy __autostart. Hosts someone
// paused stay paused.

const (
	autostartLabel = "com.jessejwatson.pingy.autostart"
	autostartUnit  = "pingy-autostart.service"
)

// systemctl runs systemctl --user; tests replace it.
var systemctl = func(args ...string) error {
	out, err := exec.Command("systemctl", append([]string{"--user"}, args...)...).CombinedOutput()
	if err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return fmt.Errorf("systemctl --user %s: %s", strings.Join(args, " "), msg)
		}
		return fmt.Errorf("systemctl --user %s: %v", strings.Join(args, " "), err)
	}
	return nil
}

// autostartPath is the file that starts pingy at login.
func autostartPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "LaunchAgents", autostartLabel+".plist"), nil
	case "linux":
		config := os.Getenv("XDG_CONFIG_HOME")
		if config == "" {
			config = filepath.Join(home, ".config")
		}
		return filepath.Join(config, "systemd", "user", autostartUnit), nil
	}
	return "", fmt.Errorf("pingy can't start by itself on %s", runtime.GOOS)
}

// cmdAutostart turns autostart on or off, or says whether it's on.
func (a *App) cmdAutostart(args []string) error {
	path, err := autostartPath()
	if err != nil {
		return err
	}
	u := a.errUI()
	switch {
	case len(args) == 0:
		if _, err := os.Stat(path); err != nil {
			a.notef("Autostart is off")
			a.hint("  pingy --autostart on to start stopped hosts again when you log in")
			return nil
		}
		a.notef("Autostart is on: stopped hosts start again when you log in")
		a.hint("  pingy --autostart off to turn it off")
		return nil
	case len(args) == 1 && args[0] == "on":
		exe, err := monitor.StableExe()
		if err != nil {
			return err
		}
		if err := a.writeAutostart(path, exe); err != nil {
			return err
		}
		a.done("Autostart is on: stopped hosts start again when you log in")
		a.hint("  Hosts you've paused stay paused · %s", u.bold("pingy --autostart off")+" to turn it off")
		return nil
	case len(args) == 1 && args[0] == "off":
		if _, err := os.Stat(path); err != nil {
			a.notef("Autostart is already off")
			return nil
		}
		if runtime.GOOS == "linux" {
			if err := systemctl("disable", autostartUnit); err != nil {
				return err
			}
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		a.done("Autostart is off; after a restart, pingy -r starts hosts again")
		return nil
	}
	return errors.New("pingy --autostart takes on or off, or nothing to say whether it's on")
}

// writeAutostart sets this machine to run exe __autostart at login.
func (a *App) writeAutostart(path, exe string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var content []byte
	switch runtime.GOOS {
	case "darwin":
		content = launchAgent(exe, a.Home)
	case "linux":
		content = systemdUnit(exe, a.Home)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return err
	}
	if runtime.GOOS == "linux" {
		if err := systemctl("daemon-reload"); err != nil {
			return err
		}
		return systemctl("enable", autostartUnit)
	}
	return nil
}

func xmlText(s string) string {
	var b bytes.Buffer
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

// launchAgent runs pingy __autostart once, when you log in, and leaves
// the hosts it starts running once it's done.
func launchAgent(exe, home string) []byte {
	return []byte(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + autostartLabel + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>` + xmlText(exe) + `</string>
		<string>__autostart</string>
	</array>
	<key>EnvironmentVariables</key>
	<dict>
		<key>PINGY_HOME</key>
		<string>` + xmlText(home) + `</string>
	</dict>
	<key>RunAtLoad</key>
	<true/>
	<key>AbandonProcessGroup</key>
	<true/>
	<key>StandardOutPath</key>
	<string>` + xmlText(filepath.Join(home, "autostart.log")) + `</string>
	<key>StandardErrorPath</key>
	<string>` + xmlText(filepath.Join(home, "autostart.log")) + `</string>
</dict>
</plist>
`)
}

func unitQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, `%`, `%%`).Replace(s) + `"`
}

// systemdUnit runs pingy __autostart once, when you log in. KillMode
// process leaves the hosts it starts running once it's done.
func systemdUnit(exe, home string) []byte {
	return []byte(`[Unit]
Description=Start the hosts pingy was pinging again
Wants=network-online.target
After=network-online.target

[Service]
Type=oneshot
ExecStart=` + unitQuote(exe) + ` __autostart
Environment=` + unitQuote("PINGY_HOME="+home) + `
KillMode=process

[Install]
WantedBy=default.target
`)
}

// unpausedStopped are the hosts that stopped without being paused, e.g.
// by a restart. A host only being watched stopped with its pingy, so
// isn't one.
func unpausedStopped(root string) []*monitor.Meta {
	var list []*monitor.Meta
	for _, m := range monitor.List(root) {
		if !m.Paused && !m.Foreground && !m.Running() {
			list = append(list, m)
		}
	}
	return list
}

// cmdAutostartRun starts the unpausedStopped hosts again; it's what
// autostart runs at login.
func (a *App) cmdAutostartRun() error {
	started, err := a.resumeAll(unpausedStopped(a.root()))
	u := a.errUI()
	for _, m := range started {
		if m.Running() {
			a.done("Pinging %s again", u.bold(m.Label()))
		}
	}
	return err
}
