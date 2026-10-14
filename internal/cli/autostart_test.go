package cli

import (
	"encoding/xml"
	"io"
	"os"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestAutostartLeavesPausedAndWatchedHostsBe(t *testing.T) {
	a, metas := testApp(t, "1.1.1.1", "8.8.8.8", "9.9.9.9")
	if err := pause(metas[1]); err != nil {
		t.Fatal(err)
	}
	fresh, _ := metas[2].Reload()
	fresh.Foreground, fresh.OwnerPID = true, 1
	fresh.Save()
	if got := targetsOf(unpausedStopped(a.root())); !slices.Equal(got, []string{"1.1.1.1"}) {
		t.Fatalf("would start %v", got)
	}
	if paused, _ := metas[1].Reload(); !paused.Paused {
		t.Fatal("pausing doesn't say it's paused")
	}
}

func TestLaunchAgentIsXML(t *testing.T) {
	b := launchAgent("/opt/homebrew/opt/pingy/bin/pingy", "/Users/a & b/.config/pingy")
	d := xml.NewDecoder(strings.NewReader(string(b)))
	d.Strict = true
	for {
		if _, err := d.Token(); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("%v\n%s", err, b)
		}
	}
	for _, want := range []string{"<string>__autostart</string>", "<key>RunAtLoad</key>", "a &amp; b"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("missing %s:\n%s", want, b)
		}
	}
}

func TestSystemdUnitQuotes(t *testing.T) {
	b := string(systemdUnit("/home/a b/pingy", "/home/a b/50%"))
	if !strings.Contains(b, `ExecStart="/home/a b/pingy" __autostart`) || !strings.Contains(b, `Environment="PINGY_HOME=/home/a b/50%%"`) {
		t.Fatal(b)
	}
}

func TestAutostartOnAndOff(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("no autostart here")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	var calls []string
	systemctl = func(args ...string) error { calls = append(calls, strings.Join(args, " ")); return nil }
	a, _ := testApp(t)
	path, _ := autostartPath()
	if err := a.run([]string{"--autostart", "on"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(b), a.Home) {
		t.Fatalf("%v: %s", err, b)
	}
	if err := a.run([]string{"--autostart=off"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("still there after turning it off")
	}
	if runtime.GOOS == "linux" && !slices.Equal(calls, []string{"daemon-reload", "enable " + autostartUnit, "disable " + autostartUnit}) {
		t.Fatalf("systemctl %v", calls)
	}
	if err := a.run([]string{"--autostart", "maybe"}); err == nil {
		t.Fatal("took maybe")
	}
}
