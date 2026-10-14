package cli

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestParseVersion(t *testing.T) {
	for in, want := range map[string]bool{"0.1.9": true, "v0.1.10": true, "dev": false, "0.1.9-2-gabc1234": false, "0.1.9-dirty": false, "": false} {
		if _, ok := parseVersion(in); ok != want {
			t.Errorf("%q read: %v, want %v", in, ok, want)
		}
	}
	a, _ := parseVersion("0.1.10")
	b, _ := parseVersion("0.1.9")
	if !newer(a, b) || newer(b, a) || newer(a, a) {
		t.Error("0.1.10 should be newer than 0.1.9, and only that")
	}
}

func TestLatestIn(t *testing.T) {
	formula := `  url "https://github.com/jessejwatson/pingy/archive/refs/tags/v0.1.12.tar.gz"`
	if got := latestIn(tapFormulaURL, []byte(formula)); got != "0.1.12" {
		t.Errorf("formula: %q", got)
	}
	tags := `[{"name":"v0.1.9"},{"name":"v0.1.10"},{"name":"v0.1.2"},{"name":"nightly"}]`
	if got := latestIn(tagsURL, []byte(tags)); got != "0.1.10" {
		t.Errorf("tags: %q", got)
	}
}

func TestAvailableUpdate(t *testing.T) {
	a := &App{Home: t.TempDir(), Version: "0.1.9"}
	if latest, stale := a.availableUpdate(tapFormulaURL); latest != "" || !stale {
		t.Fatalf("never looked: %q stale %v", latest, stale)
	}
	writeFileAtomic(updatePath(a.Home), updateFile{Checked: time.Now(), Source: tapFormulaURL, Latest: "0.1.10"})
	if latest, stale := a.availableUpdate(tapFormulaURL); latest != "0.1.10" || stale {
		t.Fatalf("just looked: %q stale %v", latest, stale)
	}
	if latest, stale := a.availableUpdate(tagsURL); latest != "" || !stale {
		t.Fatalf("looked elsewhere: %q stale %v", latest, stale)
	}
	a.Version = "0.1.10"
	if latest, _ := a.availableUpdate(tapFormulaURL); latest != "" {
		t.Fatalf("up to date, but offered %q", latest)
	}
	writeFileAtomic(updatePath(a.Home), updateFile{Checked: time.Now().Add(-4 * time.Hour), Source: tapFormulaURL, Latest: "0.1.11"})
	if latest, stale := a.availableUpdate(tapFormulaURL); latest != "0.1.11" || !stale {
		t.Fatalf("hours old: %q stale %v", latest, stale)
	}
}

func TestUpdateCheckWanted(t *testing.T) {
	a := &App{Home: t.TempDir(), Version: "dev"}
	if a.updateCheckWanted() {
		t.Error("a dev build looks for updates")
	}
	a.Version = "0.1.9"
	t.Setenv("PINGY_NO_UPDATE_CHECK", "1")
	if a.updateCheckWanted() {
		t.Error("PINGY_NO_UPDATE_CHECK is ignored")
	}
	os.Unsetenv("PINGY_NO_UPDATE_CHECK")
	if !a.updateCheckWanted() {
		t.Error("a release doesn't look for updates")
	}
}

func TestUpdateShowsInThePanel(t *testing.T) {
	a, _ := testApp(t, "1.1.1.1")
	m := newPickModel(a)
	if strings.Contains(m.renderPick(), "is out") {
		t.Fatal("shows an update before one's found")
	}
	m.Update(updateMsg{"0.1.10", "brew upgrade pingy"})
	if top, _, _ := strings.Cut(m.renderPick(), "\n"); !strings.Contains(top, "● pingy 0.1.10 is out: brew upgrade pingy") {
		t.Fatalf("title: %s", top)
	}
}
