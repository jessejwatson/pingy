package cli

import (
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
