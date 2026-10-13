package cli

import (
	"slices"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"pingy/internal/monitor"
)

func TestParseArgs(t *testing.T) {
	o, err := parseArgs([]string{"1.1.1.1", "-d", "-n", "dns", "--interval=0.5"})
	if err != nil {
		t.Fatal(err)
	}
	if !o.detach || o.name != "dns" || o.interval != 500*time.Millisecond || len(o.targets) != 1 {
		t.Fatalf("%+v", o)
	}
	if _, err := parseArgs([]string{"-i", "0.01", "x"}); err == nil {
		t.Fatal("a tiny interval was allowed")
	}
	if _, err := parseArgs([]string{"-z"}); err == nil {
		t.Fatal("an unknown flag was allowed")
	}
	if d, _ := parseInterval("2s"); d != 2*time.Second {
		t.Fatal(d)
	}
}

func TestNiceCeil(t *testing.T) {
	for in, want := range map[float64]float64{0: 1, 0.13: 0.2, 1: 1, 18: 20, 21: 25, 260: 500} {
		if got := niceCeil(in); got != want {
			t.Errorf("niceCeil(%v) = %v, want %v", in, got, want)
		}
	}
}

func TestFuzzy(t *testing.T) {
	if !fuzzy("cloudflare", "cfl") || fuzzy("cloudflare", "fc") || !fuzzy("x", "") {
		t.Fatal("fuzzy")
	}
}

func TestGraphShape(t *testing.T) {
	end := time.Date(2026, 1, 1, 12, 0, 0, 0, time.Local)
	var s []monitor.Sample
	for i := range 30 {
		rtt := time.Duration(10+i) * time.Millisecond
		if i == 20 {
			rtt = -1
		}
		s = append(s, monitor.Sample{At: end.Add(-time.Duration(30-i) * time.Second), RTT: rtt})
	}
	u := ui{}
	lines := u.graph(s, end, time.Second, 60, 10)
	if len(lines) != 10 {
		t.Fatalf("%d lines", len(lines))
	}
	for _, l := range lines {
		if w := lipgloss.Width(l); w != 60 {
			t.Fatalf("line %q is %d wide", l, w)
		}
	}
	if !strings.Contains(strings.Join(lines, "\n"), "░") {
		t.Fatal("the lost ping isn't shown")
	}
	if !strings.HasPrefix(lines[0], "  50ms ┤") {
		t.Fatalf("top label: %q", lines[0])
	}
}

func TestBoxIsExactSize(t *testing.T) {
	u := ui{on: true, icons: unicodeIcons}
	out := u.box(u.accent("title"), []string{"a very long line that will not fit in the box at all"}, 20, 5, colBorder)
	lines := strings.Split(out, "\n")
	if len(lines) != 5 {
		t.Fatalf("%d lines", len(lines))
	}
	for _, l := range lines {
		if w := lipgloss.Width(l); w != 20 {
			t.Fatalf("%q is %d wide", l, w)
		}
	}
}

func TestPickSeveralAndGraphThem(t *testing.T) {
	a, _ := testApp(t, "1.1.1.1", "8.8.8.8", "9.9.9.9")
	m := newPickModel(a)
	// Pick the first and third hosts, skipping the second.
	press(m, "ctrl+t", "tab", "down", "tab", "enter")
	if m.view != viewAll {
		t.Fatalf("view = %v, want viewAll", m.view)
	}
	var got []string
	for _, h := range m.panels() {
		got = append(got, h.meta.Target)
	}
	if want := []string{"1.1.1.1", "9.9.9.9"}; !slices.Equal(got, want) {
		t.Fatalf("panels = %v, want %v", got, want)
	}
	if s := m.renderAll(); !strings.Contains(s, "9.9.9.9") || strings.Contains(s, "8.8.8.8") {
		t.Fatalf("render shows the wrong hosts:\n%s", s)
	}
	// esc goes back to the picker, still picking; esc again stops.
	press(m, "esc")
	if m.view != viewPick || !m.multi || len(m.picked) != 2 || m.quitting {
		t.Fatalf("after esc: view %v multi %v picked %v", m.view, m.multi, m.picked)
	}
	press(m, "esc")
	if m.multi || len(m.picked) != 0 || m.quitting {
		t.Fatalf("after second esc: multi %v picked %v", m.multi, m.picked)
	}
}

func TestFromPairsTargetsWithOrigins(t *testing.T) {
	o, err := parseArgs([]string{"1.1.1.1", "8.8.8.8", "--from", "office, home", "-d"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(o.from, []string{"office", "home"}) {
		t.Fatalf("from = %q", o.from)
	}
	a, _ := testApp(t)
	if err := a.run([]string{"1.1.1.1@office", "--from", "home", "-d"}); err == nil {
		t.Fatal("an origin twice was allowed")
	}
	if err := a.run([]string{"1.1.1.1@", "-d"}); err == nil || !strings.Contains(err.Error(), "after the @") {
		t.Fatalf("an empty origin: %v", err)
	}
	if err := a.run([]string{"x;id@office", "-d"}); err == nil {
		t.Fatal("a target that isn't a host was sent to an origin")
	}
}

func TestExpandTakesATargetFromEverywhere(t *testing.T) {
	a, ms := testApp(t, "8.8.8.8")
	office, _ := monitor.Create(a.root(), "1.1.1.1", "office", time.Second)
	here, _ := monitor.Create(a.root(), "1.1.1.1", "", time.Second)
	home, _ := monitor.Create(a.root(), "1.1.1.1", "home", time.Second)
	home.Name = "cf-home"
	home.Save()
	ids := func(args ...string) []string {
		t.Helper()
		list, _, err := a.expand(args)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, m := range list {
			out = append(out, m.ID)
		}
		return out
	}
	if got := ids("1.1.1.1"); !slices.Equal(got, []string{here.ID, office.ID, home.ID}) {
		t.Fatalf("1.1.1.1 = %v", got)
	}
	if got := ids("1.1.1.1@office", "cf-home", "8.8.8.8"); !slices.Equal(got, []string{office.ID, home.ID, ms[0].ID}) {
		t.Fatalf("by origin and name = %v", got)
	}
	if _, _, err := a.expand([]string{"1.1.1.1@nowhere"}); err == nil {
		t.Fatal("found a host at an origin it isn't pinged from")
	}

	// A view keeps each origin apart, and finds it again by target and
	// origin once it's deleted and pinged again.
	if err := a.saveView("cf", "", hostsOf(office, here)); err != nil {
		t.Fatal(err)
	}
	office.Remove()
	again, _ := monitor.Create(a.root(), "1.1.1.1", "office", time.Second)
	if got := ids("cf"); !slices.Equal(got, []string{again.ID, here.ID}) {
		t.Fatalf("view = %v", got)
	}
}
