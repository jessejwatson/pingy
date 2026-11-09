package cli

import (
	"bytes"
	"io"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"pingy/internal/monitor"
)

// testApp is an App over a fresh PINGY_HOME holding stopped hosts.
func testApp(t *testing.T, targets ...string) (*App, []*monitor.Meta) {
	t.Helper()
	a := &App{Home: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard, Width: 120, Height: 40}
	var metas []*monitor.Meta
	for _, target := range targets {
		m, err := monitor.Create(a.root(), target, "", time.Second)
		if err != nil {
			t.Fatal(err)
		}
		metas = append(metas, m)
	}
	return a, metas
}

func hostsOf(metas ...*monitor.Meta) []*host {
	var hs []*host
	for _, m := range metas {
		hs = append(hs, loadHost(m, 0))
	}
	return hs
}

func targetsOf(metas []*monitor.Meta) []string {
	var ts []string
	for _, m := range metas {
		ts = append(ts, m.Target)
	}
	return ts
}

func TestViewFindsHostsByIDThenTarget(t *testing.T) {
	a, ms := testApp(t, "1.1.1.1", "8.8.8.8", "9.9.9.9")
	if err := a.saveView("office", "", hostsOf(ms[2], ms[0], ms[1])); err != nil {
		t.Fatal(err)
	}
	// 8.8.8.8 is deleted, then pinged again under a new ID: the view finds
	// it by its target rather than wanting a second monitor for it.
	if err := ms[1].Remove(); err != nil {
		t.Fatal(err)
	}
	vs, _ := a.loadViews()
	found, gone := vs[0].resolve(monitor.List(a.root()))
	if want := []string{"9.9.9.9", "1.1.1.1"}; !slices.Equal(targetsOf(found), want) || gone != 1 {
		t.Fatalf("after delete: %v, %d gone; want %v, 1 gone", targetsOf(found), gone, want)
	}
	again, err := monitor.Create(a.root(), "8.8.8.8", "", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if again.ID == ms[1].ID {
		t.Fatal("the new monitor reused the old ID; the test needs a fresh one")
	}
	found, gone = vs[0].resolve(monitor.List(a.root()))
	if want := []string{"9.9.9.9", "1.1.1.1", "8.8.8.8"}; !slices.Equal(targetsOf(found), want) || gone != 0 {
		t.Fatalf("after pinging again: %v, %d gone; want %v", targetsOf(found), gone, want)
	}
}

func TestExpandNeverRepeatsAHost(t *testing.T) {
	a, ms := testApp(t, "1.1.1.1", "8.8.8.8", "9.9.9.9")
	if err := a.saveView("office", "", hostsOf(ms[0], ms[1])); err != nil {
		t.Fatal(err)
	}
	if err := a.saveView("dns", "", hostsOf(ms[1], ms[2])); err != nil {
		t.Fatal(err)
	}
	list, views, err := a.expand([]string{"office", "8.8.8.8", "DNS"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"1.1.1.1", "8.8.8.8", "9.9.9.9"}; !slices.Equal(targetsOf(list), want) {
		t.Fatalf("hosts = %v, want %v", targetsOf(list), want)
	}
	if !slices.Equal(views, []string{"office", "dns"}) {
		t.Fatalf("views = %v", views)
	}
	if _, _, err := a.expand([]string{"nope"}); err == nil {
		t.Fatal("expected an error for an unknown name")
	}
}

func TestViewNames(t *testing.T) {
	a, ms := testApp(t, "1.1.1.1", "8.8.8.8")
	if err := a.rename(ms[0], "cloudflare"); err != nil {
		t.Fatal(err)
	}
	hs := hostsOf(ms...)
	for _, name := range []string{"", "-x", "my view", "tab\there", "cloudflare", "8.8.8.8", "1.1.1.1", ms[0].ID} {
		if err := a.saveView(name, "", hs); err == nil {
			t.Errorf("saving a view called %q worked; want an error", name)
		}
	}
	if err := a.saveView("office", "", hs); err != nil {
		t.Fatal(err)
	}
	if err := a.saveView("Office", "", hs); err == nil {
		t.Error("two views called office")
	}
	if err := a.rename(ms[1], "office"); err == nil {
		t.Error("a host was named after a view")
	}
	// Saving over itself, or under a new name, replaces the view.
	if err := a.saveView("office", "office", hs[:1]); err != nil {
		t.Fatal(err)
	}
	if err := a.saveView("work", "office", hs); err != nil {
		t.Fatal(err)
	}
	vs, _ := a.loadViews()
	if len(vs) != 1 || vs[0].Name != "work" || len(vs[0].Hosts) != 2 {
		t.Fatalf("views = %+v", vs)
	}
	if err := a.deleteView("work"); err != nil {
		t.Fatal(err)
	}
	if vs, _ := a.loadViews(); len(vs) != 0 {
		t.Fatalf("after delete: %+v", vs)
	}
}

func key(k string) tea.KeyPressMsg {
	switch k {
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "ctrl+t":
		return tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl}
	case "ctrl+x":
		return tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl}
	case "ctrl+p":
		return tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl}
	case "ctrl+s":
		return tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl}
	case "shift+up":
		return tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModShift}
	case "shift+down":
		return tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModShift}
	}
	r := []rune(k)[0]
	return tea.KeyPressMsg{Code: r, Text: k}
}

func press(m *model, keys ...string) {
	for _, k := range keys {
		m.Update(key(k))
	}
}

func typeText(m *model, s string) {
	for _, r := range s {
		press(m, string(r))
	}
}

func TestSaveAndOpenAViewInThePanel(t *testing.T) {
	a, _ := testApp(t, "1.1.1.1", "8.8.8.8", "9.9.9.9")
	m := newPickModel(a)
	press(m, "ctrl+t", "tab", "down", "down", "tab", "enter", "s")
	typeText(m, "office")
	press(m, "enter")
	if m.mode != "" || m.viewName != "office" || m.noteBad {
		t.Fatalf("after saving: mode %q view %q note %q", m.mode, m.viewName, m.note)
	}
	vs, _ := a.loadViews()
	if len(vs) != 1 || vs[0].Name != "office" || len(vs[0].Hosts) != 2 {
		t.Fatalf("views = %+v", vs)
	}

	// Back out of picking, then open the view from the panel's list.
	press(m, "esc", "esc")
	if m.multi || m.view != viewPick {
		t.Fatalf("not back in the panel: view %v multi %v", m.view, m.multi)
	}
	rows := m.rows()
	if len(rows) == 0 || rows[0].v == nil {
		t.Fatalf("the view isn't listed first: %+v", rows)
	}
	if s := m.renderPick(); !strings.Contains(s, "▦ office") || !strings.Contains(s, "2 hosts") {
		t.Fatalf("panel doesn't show the view:\n%s", s)
	}
	m.cursor = 0
	press(m, "enter")
	var got []string
	for _, h := range m.panels() {
		got = append(got, h.meta.Target)
	}
	if m.view != viewAll || !slices.Equal(got, []string{"1.1.1.1", "9.9.9.9"}) {
		t.Fatalf("opened %v showing %v", m.view, got)
	}
	if s := m.renderAll(); !strings.Contains(s, "▦ office") {
		t.Fatalf("graphs don't say which view:\n%s", s)
	}

	// Leaving it forgets what was picked; ctrl-x then deletes the view.
	press(m, "esc")
	if len(m.picked) != 0 || m.viewName != "" {
		t.Fatalf("after esc: picked %v view %q", m.picked, m.viewName)
	}
	m.cursor = 0
	press(m, "ctrl+x", "y")
	if vs, _ := a.loadViews(); len(vs) != 0 || m.noteBad {
		t.Fatalf("after deleting: %+v, note %q", vs, m.note)
	}
	if len(monitor.List(a.root())) != 3 {
		t.Fatal("deleting the view deleted hosts")
	}
}

func TestGraphAndListAView(t *testing.T) {
	a, ms := testApp(t, "1.1.1.1", "8.8.8.8")
	if err := a.saveView("office", "", hostsOf(ms[1])); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	a.Stdout = &out
	if code := a.Run([]string{"-g", "office"}); code != 0 {
		t.Fatalf("pingy -g office exited %d", code)
	}
	if s := out.String(); !strings.Contains(s, "8.8.8.8") || strings.Contains(s, "1.1.1.1") {
		t.Fatalf("pingy -g office printed:\n%s", s)
	}
	out.Reset()
	if code := a.Run([]string{"-l"}); code != 0 {
		t.Fatalf("pingy -l exited %d", code)
	}
	if s := out.String(); !strings.Contains(s, "VIEW") || !strings.Contains(s, "office") {
		t.Fatalf("pingy -l printed:\n%s", s)
	}
	// A view whose hosts have all gone says so, rather than graphing nothing.
	if err := ms[1].Remove(); err != nil {
		t.Fatal(err)
	}
	var errOut bytes.Buffer
	a.Stderr = &errOut
	if code := a.Run([]string{"-g", "office"}); code == 0 || !strings.Contains(errOut.String(), "office") {
		t.Fatalf("exit %d, stderr %q", code, errOut.String())
	}
}

func savedTargets(t *testing.T, a *App) []string {
	t.Helper()
	vs, err := a.loadViews()
	if err != nil || len(vs) != 1 {
		t.Fatalf("views = %+v, %v", vs, err)
	}
	var ts []string
	for _, vh := range vs[0].Hosts {
		ts = append(ts, vh.Target)
	}
	return ts
}

func shownTargets(m *model) []string {
	var ts []string
	for _, h := range m.panels() {
		ts = append(ts, h.meta.Target)
	}
	return ts
}

func TestReorderAddAndRemoveInAView(t *testing.T) {
	a, ms := testApp(t, "1.1.1.1", "8.8.8.8", "9.9.9.9", "4.4.4.4")
	// The view is saved out of the hosts' own order, and one of its hosts
	// has gone, which every change below must keep.
	if err := a.saveView("office", "", hostsOf(ms[2], ms[0], ms[3])); err != nil {
		t.Fatal(err)
	}
	if err := ms[3].Remove(); err != nil {
		t.Fatal(err)
	}
	m := newPickModel(a)
	m.cursor = 0
	press(m, "enter")
	if got, want := shownTargets(m), []string{"9.9.9.9", "1.1.1.1"}; !slices.Equal(got, want) {
		t.Fatalf("opened in order %v, want the view's %v", got, want)
	}

	press(m, "shift+down")
	if got, want := shownTargets(m), []string{"1.1.1.1", "9.9.9.9"}; !slices.Equal(got, want) || m.focus != 1 {
		t.Fatalf("after moving down: %v, focus %d", got, m.focus)
	}
	if got, want := savedTargets(t, a), []string{"1.1.1.1", "9.9.9.9", "4.4.4.4"}; !slices.Equal(got, want) {
		t.Fatalf("saved %v, want %v", got, want)
	}
	press(m, "shift+down") // already last: nothing moves
	press(m, "up", "shift+up")
	if got := shownTargets(m); !slices.Equal(got, []string{"1.1.1.1", "9.9.9.9"}) || m.focus != 0 {
		t.Fatalf("at the top: %v, focus %d", got, m.focus)
	}

	// a: add 8.8.8.8 in the picker, then enter saves it into the view.
	press(m, "a")
	if m.view != viewPick || !m.adding || len(m.picked) != 2 {
		t.Fatalf("a: view %v adding %v picked %v", m.view, m.adding, m.picked)
	}
	typeText(m, "8.8")
	press(m, "tab", "enter")
	if got, want := shownTargets(m), []string{"1.1.1.1", "9.9.9.9", "8.8.8.8"}; !slices.Equal(got, want) || m.view != viewAll {
		t.Fatalf("after adding: %v in view %v", got, m.view)
	}
	if got, want := savedTargets(t, a), []string{"1.1.1.1", "9.9.9.9", "8.8.8.8", "4.4.4.4"}; !slices.Equal(got, want) {
		t.Fatalf("saved %v, want %v", got, want)
	}

	// esc in the picker goes back without changing anything.
	press(m, "a", "tab", "esc")
	if got := shownTargets(m); len(got) != 3 || m.view != viewAll || m.multi {
		t.Fatalf("after a, esc: %v in view %v, multi %v", got, m.view, m.multi)
	}

	// r takes the chosen host out of the view, never stopping it.
	press(m, "down", "r")
	if got, want := savedTargets(t, a), []string{"1.1.1.1", "8.8.8.8", "4.4.4.4"}; !slices.Equal(got, want) {
		t.Fatalf("after removing: saved %v, want %v", got, want)
	}
	if len(monitor.List(a.root())) != 3 {
		t.Fatal("removing a host from a view deleted it")
	}
	press(m, "r", "r")
	if got := savedTargets(t, a); len(got) != 2 || !m.noteBad {
		t.Fatalf("the last host was removed: %v, note %q", got, m.note)
	}
}

func TestGraphAViewThenChangeIt(t *testing.T) {
	a, ms := testApp(t, "1.1.1.1", "8.8.8.8", "9.9.9.9")
	if err := a.saveView("office", "", hostsOf(ms[2], ms[0])); err != nil {
		t.Fatal(err)
	}
	// pingy -g office, as cmdGraph sets it up.
	list, views, err := a.expand([]string{"office"})
	if err != nil {
		t.Fatal(err)
	}
	only, order := map[string]bool{}, []string{}
	for _, meta := range list {
		only[meta.ID] = true
		order = append(order, meta.ID)
	}
	m := newAllModel(a, only, views[0], order)
	if got := shownTargets(m); !slices.Equal(got, []string{"9.9.9.9", "1.1.1.1"}) {
		t.Fatalf("pingy -g office shows %v", got)
	}
	press(m, "r")
	if got := savedTargets(t, a); !slices.Equal(got, []string{"1.1.1.1"}) {
		t.Fatalf("saved %v", got)
	}
	press(m, "a")
	typeText(m, "8.8")
	press(m, "tab", "enter")
	if got := savedTargets(t, a); !slices.Equal(got, []string{"1.1.1.1", "8.8.8.8"}) {
		t.Fatalf("saved %v", got)
	}
}

// Every key hint shows at ordinary terminal sizes, wrapping rather than
// being cut off, and every screen stays exactly the terminal's size.
func TestKeyHintsAllShow(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {120, 40}, {200, 50}} {
		a, ms := testApp(t, "1.1.1.1", "8.8.8.8", "9.9.9.9")
		a.Width, a.Height = size[0], size[1]
		if err := a.saveView("office", "", hostsOf(ms...)); err != nil {
			t.Fatal(err)
		}
		check := func(name, screen string, hints ...string) {
			t.Helper()
			lines := strings.Split(screen, "\n")
			if len(lines) != a.Height {
				t.Errorf("%dx%d %s: %d lines", a.Width, a.Height, name, len(lines))
			}
			for _, l := range lines {
				if w := lipgloss.Width(l); w > a.Width {
					t.Errorf("%dx%d %s: a line is %d wide: %q", a.Width, a.Height, name, w, l)
				}
			}
			for _, h := range hints {
				if !strings.Contains(screen, h) {
					t.Errorf("%dx%d %s: %q isn't shown:\n%s", a.Width, a.Height, name, h, screen)
				}
			}
		}
		m := newPickModel(a)
		check("panel", m.renderPick(), "enter open", "ctrl-t pick several", "ctrl-x kill", "ctrl-p pause", "esc quit")
		press(m, "down", "enter")
		check("graph", m.renderDetail(), "shift-←→ page", "home/end", "a add", "n rename", "p pause", "x kill", "esc back")
		press(m, "esc")
		m.cursor = 0
		press(m, "enter")
		check("view", m.renderAll(), "▦ office", "↑↓ choose", "shift-↑↓ reorder", "a add", "r remove", "s save", "+/- zoom", "esc back")
		press(m, "a")
		check("adding", m.renderPick(), "tab add or remove", "enter save", "esc cancel")
	}
}

func TestPauseArgs(t *testing.T) {
	o, err := parseArgs([]string{"-p", "office", "1.1.1.1"})
	if err != nil || !o.pause || !slices.Equal(o.targets, []string{"office", "1.1.1.1"}) {
		t.Fatalf("parseArgs = %+v, %v", o, err)
	}
	a, _ := testApp(t, "1.1.1.1")
	var errOut bytes.Buffer
	a.Stderr = &errOut
	if code := a.Run([]string{"-p"}); code != 0 || !strings.Contains(errOut.String(), "Nothing is being pinged") {
		t.Fatalf("pingy -p with nothing running: exit %d, %q", code, errOut.String())
	}
	if code := a.Run([]string{"-p", "-k", "1.1.1.1"}); code == 0 {
		t.Fatal("pingy -p -k worked")
	}
}

// p pauses everywhere, as pingy -p does; s and ctrl-s no longer do.
func TestPIsPause(t *testing.T) {
	a, _ := testApp(t, "1.1.1.1", "8.8.8.8")
	m := newPickModel(a)
	press(m, "down", "ctrl+s")
	if m.busy {
		t.Fatal("ctrl-s still pauses in the panel")
	}
	// The commands returned aren't run, so nothing is really started.
	press(m, "ctrl+p")
	if !m.busy || m.cursor != 1 {
		t.Fatalf("ctrl-p in the panel: busy %v, cursor %d (it shouldn't move up)", m.busy, m.cursor)
	}
	m.busy = false
	press(m, "enter", "s")
	if m.view != viewDetail || m.busy {
		t.Fatalf("s in a graph: view %v busy %v", m.view, m.busy)
	}
	press(m, "p")
	if !m.busy {
		t.Fatal("p doesn't pause in a graph")
	}
	m.busy = false
	press(m, "esc", "ctrl+t", "tab", "tab", "enter", "s")
	if m.view != viewAll || m.busy || m.mode != "save" {
		t.Fatalf("s in the graphs: view %v busy %v mode %q (it should save)", m.view, m.busy, m.mode)
	}
	press(m, "esc", "p")
	if !m.busy {
		t.Fatal("p doesn't pause in the graphs")
	}
}

func TestHistorySizeInThePanel(t *testing.T) {
	a, ms := testApp(t, "1.1.1.1")
	os.WriteFile(ms[0].SamplesPath(), bytes.Repeat([]byte("1700000000000 1000\n"), 1000), 0o600)
	m := newPickModel(a)
	if out := m.renderPick(); !strings.Contains(out, "19 KB of history · up to 32 MB per host") {
		t.Fatalf("panel doesn't show history use:\n%s", out)
	}
	press(m, "ctrl+o")
	if m.mode != "history" {
		t.Fatal("ctrl-o doesn't ask for the history size")
	}
	press(m, "esc")
	if m.mode != "" {
		t.Fatal("esc doesn't cancel")
	}
	press(m, "ctrl+o", "ctrl+u")
	typeText(m, "1G")
	_, cmd := m.Update(key("enter"))
	if cmd == nil || !m.busy {
		t.Fatalf("enter didn't save: note %q", m.note)
	}
	m.busy, m.note = false, ""
	// The command isn't run here, so set it as it would.
	if err := a.setHistory(1024); err != nil {
		t.Fatal(err)
	}
	m.refresh()
	if out := m.renderPick(); !strings.Contains(out, "up to 1 GB per host") {
		t.Fatalf("panel doesn't show the new limit:\n%s", out)
	}
}

func TestHistoryCommand(t *testing.T) {
	a, ms := testApp(t, "1.1.1.1")
	os.WriteFile(ms[0].SamplesPath(), bytes.Repeat([]byte("1700000000000 1000\n"), 100_000), 0o600)
	var out bytes.Buffer
	a.Stdout = &out
	if code := a.Run([]string{"--history"}); code != 0 || !strings.Contains(out.String(), "1.8 MB of history across 1 host") ||
		!strings.Contains(out.String(), "up to 32 MB, about 14–19 days") {
		t.Fatalf("pingy --history: exit %d, %q", code, out.String())
	}
	// Lowering it trims a paused host straight away.
	if code := a.Run([]string{"--history=1"}); code != 0 {
		t.Fatal("pingy --history=1 failed")
	}
	if n := ms[0].HistorySize(); n > 3<<18 || n < 3<<18-30 {
		t.Fatalf("paused host's history is %d bytes after lowering the limit", n)
	}
	if code := a.Run([]string{"--history", "lots"}); code == 0 {
		t.Fatal("pingy --history lots worked")
	}
}

// The chosen graph is heavy and marked, not just coloured, so it shows on
// a light theme and with NO_COLOR.
func TestChosenGraphStandsOut(t *testing.T) {
	a, _ := testApp(t, "1.1.1.1", "8.8.8.8")
	m := newAllModel(a, nil, "", nil)
	chosen := func() string {
		for _, l := range strings.Split(m.renderAll(), "\n") {
			if strings.HasPrefix(l, "┏━ ▸ ") {
				return strings.Fields(l)[2]
			}
		}
		return ""
	}
	if got := chosen(); got != "1.1.1.1" {
		t.Fatalf("chosen panel = %q; want 1.1.1.1", got)
	}
	press(m, "down")
	if got := chosen(); got != "8.8.8.8" {
		t.Fatalf("after down, chosen panel = %q; want 8.8.8.8", got)
	}
	if n := strings.Count(m.renderAll(), "┏"); n != 1 {
		t.Fatalf("%d heavy panels; want 1", n)
	}
}

// With hosts picked, ctrl-p and ctrl-x act on them all, not the chosen row.
func TestPickedHostsPauseAndKillTogether(t *testing.T) {
	a, _ := testApp(t, "1.1.1.1", "8.8.8.8", "9.9.9.9")
	m := newPickModel(a)
	press(m, "ctrl+t", "tab", "tab")
	if s := m.renderPick(); !strings.Contains(s, "ctrl-x kill them") {
		t.Fatalf("keys don't offer killing the picked hosts:\n%s", s)
	}
	// The commands returned aren't run, so nothing is really started.
	press(m, "ctrl+p")
	if !m.busy || !strings.Contains(m.note, "2 picked hosts") {
		t.Fatalf("ctrl-p with hosts picked: busy %v note %q", m.busy, m.note)
	}
	m.busy = false
	press(m, "ctrl+x")
	if m.mode != "kill" || len(m.targets) != 2 || m.target != nil {
		t.Fatalf("ctrl-x with hosts picked: mode %q targets %d target %v", m.mode, len(m.targets), m.target)
	}
	if s := m.renderPick(); !strings.Contains(s, "Stop pinging 2 hosts") {
		t.Fatalf("no confirmation for the picked hosts:\n%s", s)
	}
	_, cmd := m.Update(key("y"))
	m.Update(cmd())
	got := targetsOf(monitor.List(a.root()))
	if m.noteBad || !slices.Equal(got, []string{"9.9.9.9"}) || len(m.picked) != 0 {
		t.Fatalf("after y: %v left, picked %v, note %q", got, m.picked, m.note)
	}
}

func TestAddToOneHostsGraph(t *testing.T) {
	a, ms := testApp(t, "1.1.1.1", "8.8.8.8", "9.9.9.9")

	// From the panel: a, then esc, goes back to the one graph.
	m := newPickModel(a)
	typeText(m, "1.1")
	press(m, "enter")
	if m.view != viewDetail {
		t.Fatalf("enter opened view %v", m.view)
	}
	press(m, "a")
	if m.view != viewPick || !m.adding || len(m.picked) != 1 || !m.picked[ms[0].ID] {
		t.Fatalf("a: view %v adding %v picked %v", m.view, m.adding, m.picked)
	}
	press(m, "esc")
	if m.view != viewDetail || m.cur == nil || m.cur.meta.ID != ms[0].ID || m.adding {
		t.Fatalf("after a, esc: view %v adding %v", m.view, m.adding)
	}

	// a, picking another, graphs them together; esc then goes to the panel.
	press(m, "a")
	typeText(m, "8.8")
	press(m, "tab", "enter")
	if got, want := shownTargets(m), []string{"1.1.1.1", "8.8.8.8"}; !slices.Equal(got, want) || m.view != viewAll {
		t.Fatalf("after adding: %v in view %v", got, m.view)
	}
	press(m, "esc")
	if m.view != viewPick {
		t.Fatalf("esc from the hosts went to view %v", m.view)
	}

	// From pingy <host>: adding a host already pinged leaves this one tied
	// to the session, and quitting asks about this one alone.
	fg, err := monitor.Create(a.root(), "4.4.4.4", "", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	fg.Foreground = true
	if err := fg.Save(); err != nil {
		t.Fatal(err)
	}
	s := &session{meta: fg, owned: true, created: true}
	m = newDetailModel(a, s)
	press(m, "a")
	typeText(m, "9.9")
	press(m, "tab", "enter")
	if got, want := shownTargets(m), []string{"4.4.4.4", "9.9.9.9"}; !slices.Equal(got, want) || m.view != viewAll {
		t.Fatalf("after adding to a session: %v in view %v", got, m.view)
	}
	if s.detached || m.sess == nil {
		t.Fatalf("session detached %v, held %v", s.detached, m.sess != nil)
	}
	if fresh, _ := fg.Reload(); !fresh.Foreground {
		t.Fatal("adding detached the host")
	}
	press(m, "q")
	if m.quitting || m.mode != "keep" {
		t.Fatalf("q: quitting %v, mode %q", m.quitting, m.mode)
	}
	if screen := m.View().Content; !strings.Contains(screen, "4.4.4.4") || strings.Contains(screen, "4.4.4.4, ") {
		t.Fatalf("the popup should name 4.4.4.4 alone:\n%s", screen)
	}
	press(m, "y")
	if !m.quitting || !s.detached {
		t.Fatalf("y: quitting %v, detached %v", m.quitting, s.detached)
	}

	// Watching a host already pinged, with another added, stops nothing.
	s = &session{meta: ms[1]}
	m = newDetailModel(a, s)
	press(m, "a")
	typeText(m, "9.9")
	press(m, "tab", "enter", "q")
	if !m.quitting || m.mode == "keep" {
		t.Fatalf("q with nothing to stop: quitting %v, mode %q", m.quitting, m.mode)
	}
}

func TestAddANewHostToASession(t *testing.T) {
	a, _ := testApp(t)
	fg, err := monitor.Create(a.root(), "4.4.4.4", "", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	fg.Foreground, fg.Ephemeral = true, true
	if err := fg.Save(); err != nil {
		t.Fatal(err)
	}
	s := &session{meta: fg, owned: true, created: true}
	m := newDetailModel(a, s)
	press(m, "a")
	typeText(m, "10.9.8.7")
	if rows := m.rows(); len(rows) == 0 || rows[m.cursor].add != "10.9.8.7" {
		t.Fatalf("rows %v", rows)
	}
	if _, cmd := m.Update(key("enter")); cmd == nil || !m.adding {
		t.Fatalf("enter on a new host didn't start it, adding %v", m.adding)
	}
	// What starting it sends back, without a real monitor.
	added, err := monitor.Create(a.root(), "10.9.8.7", "", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	m.Update(opMsg{pick: added.ID, done: true, started: &session{meta: added, owned: true, created: true}})
	if got, want := shownTargets(m), []string{"4.4.4.4", "10.9.8.7"}; !slices.Equal(got, want) || m.view != viewAll {
		t.Fatalf("after adding a new host: %v in view %v", got, m.view)
	}
	if s.detached || len(s.started()) != 2 {
		t.Fatalf("session detached %v, started %d", s.detached, len(s.started()))
	}
	if fresh, _ := fg.Reload(); !fresh.Foreground {
		t.Fatal("adding detached the first host")
	}
	check := func(screen string, want ...string) {
		t.Helper()
		for _, w := range want {
			if !strings.Contains(screen, w) {
				t.Errorf("%q isn't shown:\n%s", w, screen)
			}
		}
	}
	check(m.renderAll(), "d detach", "q stop & quit")

	// q asks; esc stays, n stops them.
	press(m, "q")
	if m.mode != "keep" || m.quitting {
		t.Fatalf("q: mode %q, quitting %v", m.mode, m.quitting)
	}
	check(m.View().Content, "Keep pinging?", "4.4.4.4, 10.9.8.7", "y keep", "n stop", "[ ] Don't ask again")
	press(m, "esc")
	if m.mode != "" || m.quitting {
		t.Fatalf("esc: mode %q, quitting %v", m.mode, m.quitting)
	}
	press(m, "q", "n")
	if !m.quitting || s.detached {
		t.Fatalf("n: quitting %v, detached %v", m.quitting, s.detached)
	}

	// y, having ticked don't ask again, keeps them and stops asking.
	m.quitting = false
	press(m, "q", "space")
	check(m.View().Content, "[x] Don't ask again")
	press(m, "y")
	if !m.quitting || !s.detached {
		t.Fatalf("y: quitting %v, detached %v", m.quitting, s.detached)
	}
	for _, meta := range []*monitor.Meta{fg, added} {
		if fresh, _ := meta.Reload(); fresh.Foreground || fresh.Ephemeral {
			t.Fatalf("%s would still stop on quitting", meta.Target)
		}
	}
	if set, _ := monitor.LoadSettings(a.Home); !set.NoKeepAsk {
		t.Fatal("don't ask again wasn't saved")
	}
	s.detached = false
	m.quitting = false
	m.refresh()
	press(m, "q")
	if !m.quitting || m.mode == "keep" {
		t.Fatalf("q after don't ask again: quitting %v, mode %q", m.quitting, m.mode)
	}
}

func TestConfig(t *testing.T) {
	a, _ := testApp(t)
	var out bytes.Buffer
	a.Stdout = &out
	if err := a.cmdConfig(nil); err != nil || !strings.Contains(out.String(), "ask-to-keep  on") {
		t.Fatalf("config: %q, %v", out.String(), err)
	}
	if err := a.cmdConfig([]string{"ask-to-keep", "off"}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := a.cmdConfig([]string{"ask-to-keep"}); err != nil || out.String() != "off\n" {
		t.Fatalf("after off: %q, %v", out.String(), err)
	}
	if s, _ := monitor.LoadSettings(a.Home); !s.NoKeepAsk || s.HistoryMB != monitor.DefaultHistoryMB {
		t.Fatalf("saved %+v", s)
	}
	for _, args := range [][]string{{"nope"}, {"ask-to-keep", "maybe"}, {"a", "b", "c"}} {
		if err := a.cmdConfig(args); err == nil {
			t.Errorf("config %v worked", args)
		}
	}
}
