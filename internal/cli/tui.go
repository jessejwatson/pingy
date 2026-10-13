package cli

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"pingy/internal/monitor"
)

// refreshEvery is how often the panel reads new pings.
const refreshEvery = 500 * time.Millisecond

type viewKind int

const (
	viewPick   viewKind = iota // pingy: search and preview
	viewDetail                 // one host's graph
	viewAll                    // pingy -g: every host's graph
)

// window is the stretch of time a graph shows: one column per step,
// ending now while live, or at end once moved back.
type window struct {
	step time.Duration
	end  time.Time
	live bool
}

func (w window) endAt(now time.Time) time.Time {
	if w.live {
		return now
	}
	return w.end
}

// model is every pingy screen; view says which is showing.
type model struct {
	app  *App
	u    ui
	root string

	hosts []*host
	byID  map[string]*host
	only  map[string]bool // pingy -g with hosts named: just these

	width, height int
	view          viewKind

	query  textinput.Model // the picker's search
	cursor int

	cur      *host // the host whose graph is open
	win      window
	sess     *session // pingy <host>: the host being watched
	fromPick bool
	scroll   int // pingy -g: first panel shown

	mode    string // "", "rename" or "kill"
	target  *host  // what ctrl-x in the picker would kill
	input   textinput.Model
	note    string
	noteBad bool
	busy    bool

	quitting bool
}

type tickMsg struct{}

// opMsg is the result of something done in the background.
type opMsg struct {
	note string
	err  error
	open string // a host to open
	back bool   // go back to the picker
	quit bool
}

func tick() tea.Cmd { return tea.Tick(refreshEvery, func(time.Time) tea.Msg { return tickMsg{} }) }

func newModel(a *App) *model {
	q := textinput.New()
	q.Prompt = ""
	q.Placeholder = "search, or type a host to ping"
	q.CharLimit = 253
	in := textinput.New()
	in.Prompt = ""
	in.CharLimit = 40
	m := &model{
		app: a, u: a.newUI(a.StyleOut), root: a.root(), byID: map[string]*host{},
		width: a.Width, height: a.Height, query: q, input: in,
		win: window{step: time.Second, live: true},
	}
	m.refresh()
	return m
}

func newPickModel(a *App) *model {
	m := newModel(a)
	m.query.Focus()
	return m
}

func newDetailModel(a *App, s *session) *model {
	m := newModel(a)
	m.sess = s
	if h := m.byID[s.meta.ID]; h != nil {
		m.open(h)
	}
	return m
}

func newAllModel(a *App, only map[string]bool) *model {
	m := newModel(a)
	m.view, m.only = viewAll, only
	for _, h := range m.hosts {
		h.hist.LoadAll()
		h.hist.Refresh()
	}
	return m
}

func (a *App) runTUI(m *model) error {
	_, err := tea.NewProgram(m).Run()
	return err
}

func (a *App) cmdPick() error {
	if !a.Interactive {
		return errors.New("the panel needs a terminal; pingy -l lists hosts instead")
	}
	return a.runTUI(newPickModel(a))
}

// cmdGraph graphs every host live, or prints them once when there's no
// terminal.
func (a *App) cmdGraph(filter []string) error {
	hosts, err := a.loadHosts(filter)
	if err != nil {
		return err
	}
	if len(hosts) == 0 {
		a.hint("Nothing is being pinged. Start with: pingy <host> -d")
		return nil
	}
	if !a.Interactive {
		u := a.outUI()
		for _, h := range hosts {
			fmt.Fprintln(a.Stdout, strings.Join(u.hostPanel(h, time.Now(), window{step: time.Second, live: true}, a.Width, 12), "\n"))
		}
		return nil
	}
	var only map[string]bool
	if len(filter) > 0 {
		only = map[string]bool{}
		for _, h := range hosts {
			only[h.meta.ID] = true
		}
	}
	return a.runTUI(newAllModel(a, only))
}

// refresh reads the monitors again and whatever they've pinged since.
func (m *model) refresh() {
	var hosts []*host
	seen := map[string]*host{}
	for _, meta := range monitor.List(m.root) {
		if m.only != nil && !m.only[meta.ID] {
			continue
		}
		h := m.byID[meta.ID]
		if h == nil {
			tail := int64(previewTail)
			if m.view == viewAll {
				tail = 0
			}
			h = loadHost(meta, tail)
		} else {
			h.refresh(meta)
		}
		hosts = append(hosts, h)
		seen[meta.ID] = h
	}
	m.hosts, m.byID = hosts, seen
	if m.cur != nil && seen[m.cur.meta.ID] == nil {
		gone := m.cur.meta.Label()
		m.cur = nil
		if m.sess != nil {
			m.sess.killed = true
			m.quitting = true
			return
		}
		m.view, m.mode = viewPick, ""
		m.note, m.noteBad = gone+" was stopped and deleted elsewhere", true
		m.query.Focus()
	}
}

func (m *model) Init() tea.Cmd {
	if m.quitting {
		return tea.Quit
	}
	return tea.Batch(tick(), m.query.Focus())
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tickMsg:
		m.refresh()
		if m.quitting {
			return m, tea.Quit
		}
		return m, tick()
	case opMsg:
		m.busy = false
		m.note, m.noteBad = msg.note, false
		if msg.err != nil {
			m.note, m.noteBad = msg.err.Error(), true
		}
		if msg.quit {
			m.quitting = true
			return m, tea.Quit
		}
		m.refresh()
		if msg.back && m.view == viewDetail {
			m.view, m.cur = viewPick, nil
			m.query.Focus()
		}
		if h := m.byID[msg.open]; h != nil {
			m.fromPick = true
			m.open(h)
		}
		return m, nil
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			m.quitting = true
			return m, tea.Quit
		}
		if m.busy {
			return m, nil
		}
		switch m.view {
		case viewPick:
			return m.keyPick(msg)
		case viewDetail:
			return m.keyDetail(msg)
		case viewAll:
			return m.keyAll(msg)
		}
	}
	var cmd tea.Cmd
	if m.mode == "rename" {
		m.input, cmd = m.input.Update(msg)
	} else if m.view == viewPick {
		m.query, cmd = m.query.Update(msg)
	}
	return m, cmd
}

// open shows h's graph, reading all its history.
func (m *model) open(h *host) {
	h.hist.LoadAll()
	h.hist.Refresh()
	m.cur, m.view, m.mode = h, viewDetail, ""
	m.win = window{step: fitStep(time.Second, h.meta.Every()), live: true}
	m.query.Blur()
}

// fitStep is the first zoom step at least step and every, so a column
// never stands for less time than there is between pings.
func fitStep(step, every time.Duration) time.Duration {
	for _, s := range zoomSteps {
		if s >= step && s >= every {
			return s
		}
	}
	return zoomSteps[len(zoomSteps)-1]
}

// ---- picker ----

// pickRow is a host, or, with add set, the offer to start pinging it.
type pickRow struct {
	h   *host
	add string
}

func (m *model) rows() []pickRow {
	q := strings.ToLower(strings.TrimSpace(m.query.Value()))
	// Hosts containing what's typed come first, then those with its
	// letters in order, as in fzf.
	var rows, loose []pickRow
	exact := false
	for _, h := range m.hosts {
		var fields []string
		for _, f := range []string{h.meta.Name, h.meta.Target, h.state.Addr} {
			if f != "" {
				fields = append(fields, strings.ToLower(f))
			}
		}
		switch {
		case strings.Contains(strings.Join(fields, " "), q):
			rows = append(rows, pickRow{h: h})
		case slices.ContainsFunc(fields, func(f string) bool { return fuzzy(f, q) }):
			loose = append(loose, pickRow{h: h})
		}
		if strings.EqualFold(h.meta.Target, q) || strings.EqualFold(h.meta.Name, q) {
			exact = true
		}
	}
	rows = append(rows, loose...)
	if q != "" && !exact && !strings.ContainsAny(q, " \t") {
		rows = append(rows, pickRow{add: strings.TrimSpace(m.query.Value())})
	}
	return rows
}

// fuzzy says whether q's letters all appear in s, in order.
func fuzzy(s, q string) bool {
	for _, r := range q {
		i := strings.IndexRune(s, r)
		if i < 0 {
			return false
		}
		s = s[i+utf8.RuneLen(r):]
	}
	return true
}

func (m *model) keyPick(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	rows := m.rows()
	m.cursor = min(max(m.cursor, 0), max(len(rows)-1, 0))
	k := msg.String()
	if m.mode == "kill" {
		m.mode = ""
		if (k == "y" || k == "Y") && m.target != nil {
			h := m.target
			m.busy, m.note = true, "Stopping "+h.meta.Label()+"…"
			return m, func() tea.Msg {
				return opMsg{note: "Stopped pinging " + h.meta.Label() + " and deleted its history", err: h.meta.Remove()}
			}
		}
		m.note = ""
		return m, nil
	}
	switch k {
	case "esc":
		if m.query.Value() != "" {
			m.query.SetValue("")
			m.cursor = 0
			return m, nil
		}
		m.quitting = true
		return m, tea.Quit
	case "up", "ctrl+p", "ctrl+k", "shift+tab":
		m.cursor = max(m.cursor-1, 0)
		return m, nil
	case "down", "ctrl+n", "ctrl+j", "tab":
		m.cursor = min(m.cursor+1, max(len(rows)-1, 0))
		return m, nil
	case "pgup":
		m.cursor = max(m.cursor-10, 0)
		return m, nil
	case "pgdown":
		m.cursor = min(m.cursor+10, max(len(rows)-1, 0))
		return m, nil
	case "enter":
		if len(rows) == 0 {
			return m, nil
		}
		r := rows[m.cursor]
		if r.add != "" {
			m.busy, m.note, m.noteBad = true, "Starting "+r.add+"…", false
			m.query.SetValue("")
			m.cursor = 0
			return m, m.startCmd(r.add)
		}
		m.note, m.fromPick = "", true
		m.open(r.h)
		return m, nil
	case "ctrl+x":
		if len(rows) > 0 && rows[m.cursor].h != nil {
			m.mode, m.target = "kill", rows[m.cursor].h
		}
		return m, nil
	case "ctrl+s":
		if len(rows) > 0 && rows[m.cursor].h != nil {
			m.busy = true
			return m, m.toggleCmd(rows[m.cursor].h)
		}
		return m, nil
	}
	before := m.query.Value()
	var cmd tea.Cmd
	m.query, cmd = m.query.Update(msg)
	if m.query.Value() != before {
		m.cursor, m.note = 0, ""
	}
	return m, cmd
}

func (m *model) startCmd(target string) tea.Cmd {
	a := m.app
	return func() tea.Msg {
		meta, already, _, err := a.start(target, startOpts{})
		if err != nil {
			return opMsg{err: err}
		}
		note := "Pinging " + meta.Label() + " in the background"
		if already {
			note = "Already pinging " + meta.Label()
		}
		return opMsg{note: note, open: meta.ID}
	}
}

// toggleCmd pauses a running host or starts a stopped one again.
func (m *model) toggleCmd(h *host) tea.Cmd {
	a := m.app
	return func() tea.Msg {
		meta, err := h.meta.Reload()
		if err != nil {
			return opMsg{err: err}
		}
		if meta.Running() {
			if err := meta.Stop(); err != nil {
				return opMsg{err: err}
			}
			return opMsg{note: "Paused " + meta.Label() + "; its history is kept"}
		}
		if !meta.Foreground {
			meta.OwnerPID, meta.Ephemeral = 0, false
		}
		if err := meta.Save(); err != nil {
			return opMsg{err: err}
		}
		if err := a.spawn(meta); err != nil {
			return opMsg{err: err}
		}
		return opMsg{note: "Pinging " + meta.Label() + " again"}
	}
}

// ---- one host's graph ----

// plotW is how many columns the open graph has.
func (m *model) plotW() int { return max(m.w()-4-gutter-1, 4) }

func (m *model) w() int {
	if m.width > 0 {
		return m.width
	}
	return 80
}

func (m *model) h() int {
	if m.height > 0 {
		return m.height
	}
	return 24
}

// pan moves the open window by cols columns, back when negative. Going
// past now follows new pings again.
func (m *model) pan(cols int, oldest time.Time) {
	now := time.Now()
	end := m.win.endAt(now).Add(time.Duration(cols) * m.win.step)
	if !end.Before(now) {
		m.win.live = true
		return
	}
	if !oldest.IsZero() {
		if floor := oldest.Add(m.win.step); end.Before(floor) {
			end = floor
		}
	}
	m.win.live, m.win.end = false, end
}

func (m *model) zoom(by int, every time.Duration) {
	i := 0
	for n, s := range zoomSteps {
		if s == m.win.step {
			i = n
		}
	}
	i = min(max(i+by, 0), len(zoomSteps)-1)
	if zoomSteps[i] < every {
		return
	}
	m.win.step = zoomSteps[i]
}

func oldestOf(hosts ...*host) time.Time {
	var t time.Time
	for _, h := range hosts {
		if s := h.samples(); len(s) > 0 && (t.IsZero() || s[0].At.Before(t)) {
			t = s[0].At
		}
	}
	return t
}

// keyTime handles moving and zooming, shared by the graph views.
func (m *model) keyTime(k string, cols int, every time.Duration, hosts ...*host) bool {
	switch k {
	case "left", "h":
		m.pan(-max(cols/4, 1), oldestOf(hosts...))
	case "right", "l":
		m.pan(max(cols/4, 1), oldestOf(hosts...))
	case "shift+left", "H", "pgup":
		m.pan(-cols, oldestOf(hosts...))
	case "shift+right", "L", "pgdown":
		m.pan(cols, oldestOf(hosts...))
	case "home":
		if t := oldestOf(hosts...); !t.IsZero() {
			m.win.live, m.win.end = false, t.Add(-time.Second)
			m.pan(cols, t)
		}
	case "end", "G":
		m.win.live = true
	case "+", "=":
		m.zoom(-1, every)
	case "-", "_":
		m.zoom(1, every)
	default:
		return false
	}
	return true
}

func (m *model) keyDetail(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	h := m.cur
	k := msg.String()
	switch m.mode {
	case "rename":
		switch k {
		case "esc":
			m.mode, m.note = "", ""
			m.input.Blur()
			return m, nil
		case "enter":
			name := strings.TrimSpace(m.input.Value())
			if err := m.app.rename(h.meta, name); err != nil {
				m.note, m.noteBad = err.Error(), true
				return m, nil
			}
			m.mode = ""
			m.input.Blur()
			m.refresh()
			m.note, m.noteBad = "Renamed to "+h.meta.Label(), false
			return m, nil
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	case "kill":
		m.mode, m.note = "", ""
		if k == "y" || k == "Y" {
			m.busy, m.note = true, "Stopping "+h.meta.Label()+"…"
			sess := m.sess
			return m, func() tea.Msg {
				err := h.meta.Remove()
				if sess != nil && err == nil {
					sess.killed = true
					return opMsg{quit: true}
				}
				return opMsg{note: "Stopped pinging " + h.meta.Label() + " and deleted its history", err: err, back: true}
			}
		}
		return m, nil
	}
	if m.keyTime(k, m.plotW(), h.meta.Every(), h) {
		return m, nil
	}
	switch k {
	case "esc", "q", "backspace":
		if m.sess != nil || !m.fromPick {
			m.quitting = true
			return m, tea.Quit
		}
		m.view, m.cur, m.note = viewPick, nil, ""
		return m, m.query.Focus()
	case "d":
		if m.sess != nil {
			if m.sess.owned {
				if err := m.sess.detach(); err != nil {
					m.note, m.noteBad = err.Error(), true
					return m, nil
				}
			}
			m.quitting = true
			return m, tea.Quit
		}
	case "n":
		m.mode, m.note = "rename", ""
		m.input.SetValue(h.meta.Label())
		m.input.Placeholder = h.meta.Target
		m.input.CursorEnd()
		return m, m.input.Focus()
	case "x":
		m.mode, m.note = "kill", ""
	case "s":
		m.busy = true
		return m, m.toggleCmd(h)
	}
	return m, nil
}

// ---- every host ----

func (m *model) panelH() int {
	n := max(len(m.hosts), 1)
	return min(max((m.h()-1)/n, 7), max(m.h()-1, 7))
}

func (m *model) keyAll(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	k := msg.String()
	every := time.Second
	for _, h := range m.hosts {
		every = min(every, h.meta.Every())
	}
	if m.keyTime(k, m.w()-4-gutter-1, every, m.hosts...) {
		return m, nil
	}
	fits := max((m.h()-1)/m.panelH(), 1)
	switch k {
	case "esc", "q":
		m.quitting = true
		return m, tea.Quit
	case "up", "k":
		m.scroll = max(m.scroll-1, 0)
	case "down", "j":
		m.scroll = min(m.scroll+1, max(len(m.hosts)-fits, 0))
	}
	return m, nil
}

// ---- drawing ----

func (m *model) View() tea.View {
	if m.quitting {
		return tea.NewView("")
	}
	var s string
	switch m.view {
	case viewPick:
		s = m.renderPick()
	case viewDetail:
		if m.cur == nil {
			s = ""
		} else {
			s = m.renderDetail()
		}
	case viewAll:
		s = m.renderAll()
	}
	v := tea.NewView(s)
	v.AltScreen = true
	return v
}

func (m *model) noteLine() string {
	if m.note == "" {
		return ""
	}
	if m.noteBad {
		return m.u.fg(colBad, m.note)
	}
	if m.busy {
		return m.u.faint(m.note)
	}
	return m.u.fg(colOK, m.note)
}

func (m *model) renderPick() string {
	u := m.u
	W, H := m.w(), m.h()
	innerW, innerH := W-4, H-2
	leftW, rightW := innerW, 0
	if innerW >= 70 {
		leftW = min(max(innerW*2/5, 30), 60)
		rightW = innerW - leftW - 1
	}
	rows := m.rows()
	m.cursor = min(max(m.cursor, 0), max(len(rows)-1, 0))

	var left []string
	m.query.SetWidth(max(leftW-12, 10))
	count := fmt.Sprintf("%d/%d", len(rows), len(m.hosts))
	if len(rows) > 0 && rows[len(rows)-1].add != "" {
		count = fmt.Sprintf("%d/%d", len(rows)-1, len(m.hosts))
	}
	left = append(left,
		spread(u.fg(colAccent, "❯ ")+m.query.View(), u.faint(count), leftW),
		u.faint("enter open · ctrl-x kill · ctrl-s pause · esc quit"),
		u.faint(strings.Repeat("─", leftW)),
	)
	listH := innerH - len(left) - 1
	off := max(m.cursor-listH+1, 0)
	now := time.Now()
	for i := off; i < len(rows) && i < off+listH; i++ {
		r := rows[i]
		sel := i == m.cursor
		pointer := "  "
		if sel {
			pointer = u.fg(colAccent, "▸ ")
		}
		if r.add != "" {
			label := "Ping " + r.add
			if sel {
				label = u.bold(label)
			}
			left = append(left, pointer+u.fg(colAccent, "+ ")+label)
			continue
		}
		glyph, word, c := u.status(r.h, now)
		name := r.h.meta.Label()
		if sel {
			name = u.bold(name)
		}
		if r.h.meta.Name != "" {
			name += " " + u.faint(r.h.meta.Target)
		}
		right := u.lastRTT(r.h)
		if !r.h.running {
			right = u.faint(word)
		}
		left = append(left, spread(pointer+u.fg(c, glyph+" ")+name, right, leftW))
	}
	if len(m.hosts) == 0 && len(rows) == 0 {
		left = append(left, "", u.faint("  Nothing is being pinged yet."), u.faint("  Type a host, e.g. 1.1.1.1, and press enter."))
	}
	for len(left) < innerH-1 {
		left = append(left, "")
	}
	left = append(left, m.noteLine())
	if m.mode == "kill" && m.target != nil {
		left[len(left)-1] = u.fg(colBad, "Stop pinging "+m.target.meta.Label()+" and delete its history? y/n")
	}

	lines := left
	if rightW > 0 {
		var preview []string
		switch {
		case len(rows) == 0:
			preview = strings.Split(u.box("", nil, rightW, innerH, colBorder), "\n")
		case rows[m.cursor].add != "":
			t := rows[m.cursor].add
			preview = strings.Split(u.box(u.accent("+ "+t), []string{
				"Press enter to start pinging " + u.bold(t) + " in the background,",
				"then watch it here.",
			}, rightW, innerH, colBorder), "\n")
		default:
			preview = u.hostPanel(rows[m.cursor].h, now, window{step: time.Second, live: true}, rightW, innerH)
		}
		for i := range lines {
			p := ""
			if i < len(preview) {
				p = preview[i]
			}
			lines[i] = fit(lines[i], leftW) + " " + p
		}
	}
	return u.box(u.accent("pingy"), lines, W, H, colBorder)
}

// hostPanel draws h's recent graph in a w×hgt box, as the picker's preview
// and in pingy -g.
func (u ui) hostPanel(h *host, now time.Time, win window, w, hgt int) []string {
	inner := w - 4
	end := win.endAt(now)
	step := fitStep(win.step, h.meta.Every())
	lines := []string{u.statusLine(h, now), u.statsLine(monitor.Summarise(h.recent(now)), "last 5m")}
	if !win.live {
		start := end.Add(-time.Duration(inner-gutter-1) * step)
		lines[1] = u.statsLine(monitor.Summarise(between(h.samples(), start, end)), "shown")
	}
	if g := u.graph(h.samples(), end, step, inner, hgt-2-len(lines)); g != nil {
		lines = append(lines, g...)
	}
	return strings.Split(u.box(u.accent(h.meta.Label()), lines, w, hgt, colBorder), "\n")
}

// statusLine is a host's status, address and interval.
func (u ui) statusLine(h *host, now time.Time) string {
	glyph, word, c := u.status(h, now)
	parts := []string{u.fg(c, strings.TrimSpace(glyph+" "+word))}
	if h.state.Error != "" && h.state.Addr == "" {
		parts[0] = u.fg(c, glyph+" "+h.state.Error)
	}
	if h.meta.Name != "" {
		parts = append(parts, h.meta.Target)
	}
	if a := h.state.Addr; a != "" && a != h.meta.Target {
		parts = append(parts, u.fg(colCmd, a))
	}
	parts = append(parts, u.faint("every "+fmtStep(h.meta.Every())))
	return strings.Join(parts, u.faint(" · "))
}

// statsLine summarises some pings.
func (u ui) statsLine(st monitor.Stats, label string) string {
	if st.Sent == 0 {
		return u.faint("no pings " + label)
	}
	k := func(s string) string { return u.faint(s + " ") }
	parts := []string{}
	if st.Sent > st.Lost {
		parts = append(parts,
			k("avg")+fmtMs(st.Avg), k("min")+fmtMs(st.Min), k("max")+fmtMs(st.Max), k("jitter")+fmtMs(st.Jitter))
	}
	loss := fmt.Sprintf("%.1f%%", st.LossPct())
	if st.Lost > 0 {
		loss = u.fg(colBad, loss) + u.faint(fmt.Sprintf(" (%d of %d)", st.Lost, st.Sent))
	}
	parts = append(parts, k("loss")+loss)
	return strings.Join(parts, "  ") + u.faint("  · "+label)
}

func between(s []monitor.Sample, from, to time.Time) []monitor.Sample {
	return s[monitor.SearchTime(s, from):monitor.SearchTime(s, to)]
}

func (m *model) renderDetail() string {
	u, h := m.u, m.cur
	W, H := m.w(), m.h()
	innerW, innerH := W-4, H-2
	now := time.Now()
	step := fitStep(m.win.step, h.meta.Every())
	end := m.win.endAt(now)
	start := end.Add(-time.Duration(m.plotW()) * step)

	when := u.fg(colOK, "● live")
	if !m.win.live {
		when = u.fg(colWarn, fmtAgo(now.Sub(end))+" ago") + u.faint(" · end for now")
	}
	span := fmtSpan(start, end)
	lines := []string{
		spread(u.accent(h.meta.Label())+"  "+u.statusLine(h, now), u.lastRTT(h), innerW),
		spread(u.faint(span+" · "+fmtStep(step)+" per column"), when, innerW),
		"",
	}
	gh := innerH - len(lines) - 4
	lines = append(lines, u.graph(h.samples(), end, step, innerW, gh)...)
	for len(lines) < innerH-4 {
		lines = append(lines, "")
	}
	lines = append(lines, u.statsLine(monitor.Summarise(between(h.samples(), start, end)), "shown"), "")

	switch m.mode {
	case "rename":
		lines = append(lines, u.fg(colAccent, "Name: ")+m.input.View())
	case "kill":
		lines = append(lines, u.fg(colBad, "Stop pinging "+h.meta.Label()+" and delete its history? y/n"))
	default:
		lines = append(lines, m.noteLine())
	}

	keys := "←→ move · shift-←→ page · home/end · +/- zoom · n rename · s pause · x kill"
	switch {
	case m.mode == "rename":
		keys = "enter save · esc cancel · empty for the address"
	case m.sess != nil && m.sess.owned:
		keys += " · d detach · q stop & quit"
	case m.sess != nil:
		keys += " · q quit"
	case m.fromPick:
		keys += " · esc back"
	}
	lines = append(lines, u.faint(keys))
	return u.box(u.accent("pingy"), lines, W, H, colBorder)
}

// fmtSpan describes the time from start to end.
func fmtSpan(start, end time.Time) string {
	start, end = start.Local(), end.Local()
	layout := "15:04:05"
	if end.Sub(start) >= 6*time.Hour {
		layout = "15:04"
	}
	sy, sm, sd := start.Date()
	ey, em, ed := end.Date()
	now := time.Now()
	ny, nm, nd := now.Date()
	if sy != ey || sm != em || sd != ed || sy != ny || sm != nm || sd != nd {
		return start.Format("Mon 2 Jan "+layout) + " – " + end.Format("Mon 2 Jan "+layout)
	}
	return start.Format(layout) + " – " + end.Format(layout)
}

func (m *model) renderAll() string {
	u := m.u
	W, H := m.w(), m.h()
	now := time.Now()
	var lines []string
	if len(m.hosts) == 0 {
		lines = append(lines, u.faint("Nothing is being pinged. Start with: pingy <host> -d"))
	}
	ph := m.panelH()
	fits := max((H-1)/ph, 1)
	m.scroll = min(m.scroll, max(len(m.hosts)-fits, 0))
	for i := m.scroll; i < len(m.hosts) && i < m.scroll+fits; i++ {
		lines = append(lines, u.hostPanel(m.hosts[i], now, m.win, W, ph)...)
	}
	for len(lines) < H-1 {
		lines = append(lines, "")
	}
	when := "live"
	if !m.win.live {
		when = fmtAgo(now.Sub(m.win.end)) + " ago"
	}
	keys := fmt.Sprintf("%s · %s per column · ←→ move · home/end · +/- zoom", when, fmtStep(m.win.step))
	if len(m.hosts) > fits {
		keys += fmt.Sprintf(" · ↑↓ %d–%d of %d", m.scroll+1, min(m.scroll+fits, len(m.hosts)), len(m.hosts))
	}
	keys += " · q quit"
	lines = append(lines[:H-1], u.faint(fit(keys, W)))
	return strings.Join(lines, "\n")
}
