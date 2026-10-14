package cli

import (
	"errors"
	"fmt"
	"maps"
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
	viewAll                    // pingy -g, or the hosts picked in pingy: their graphs
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

	hosts    []*host
	byID     map[string]*host
	settings monitor.Settings
	only     map[string]bool // pingy -g with hosts named: just these
	views    []savedView

	width, height int
	view          viewKind

	query    textinput.Model // the picker's search
	cursor   int
	multi    bool            // picking several hosts to graph together
	picked   map[string]bool // the hosts picked, by ID
	viewName string          // the saved view the picked hosts came from

	cur      *host // the host whose graph is open
	win      window
	sess     *session        // pingy <host>: the host being watched
	fromPick bool            // opened from the picker, so esc goes back to it
	scroll   int             // pingy -g: first panel shown
	focus    int             // pingy -g: the panel chosen, which shift-↑↓ moves
	order    []string        // pingy -g: host IDs in the order to show them
	adding   bool            // in the picker from a's "add or remove hosts"
	before   map[string]bool // what was picked then, for esc to go back to
	wasMulti bool            // and whether the picker was picking several

	mode       string // "", "rename", "save", "kill" or "history"
	target     *host  // what ctrl-x in the picker would kill
	targetView string // or the view it would delete
	input      textinput.Model
	note       string
	noteBad    bool
	busy       bool

	login     *loginPopup
	loginSeen map[string]bool // origins, by ssh destination, whose popup has opened by itself

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
		app: a, u: a.newUI(a.StyleOut), root: a.root(), byID: map[string]*host{}, picked: map[string]bool{},
		width: a.Width, height: a.Height, query: q, input: in,
		win: window{step: time.Second, live: true}, loginSeen: map[string]bool{},
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

func newAllModel(a *App, only map[string]bool, viewName string, order []string) *model {
	m := newModel(a)
	m.view, m.only, m.viewName, m.order = viewAll, only, viewName, order
	m.refresh() // drop the hosts not asked for
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
			fmt.Fprintln(a.Stdout, strings.Join(u.hostPanel(h, time.Now(), window{step: time.Second, live: true}, a.Width, 12, false), "\n"))
		}
		return nil
	}
	var only map[string]bool
	var order []string
	viewName := ""
	if len(filter) > 0 {
		only = map[string]bool{}
		for _, h := range hosts {
			only[h.meta.ID] = true
			order = append(order, h.meta.ID)
		}
		// Saving pingy -g <view> updates that view.
		if _, views, _ := a.expand(filter); len(filter) == 1 && len(views) == 1 {
			viewName = views[0]
		}
	}
	return a.runTUI(newAllModel(a, only, viewName, order))
}

// refresh reads the monitors again and whatever they've pinged since.
func (m *model) refresh() {
	if vs, err := m.app.loadViews(); err == nil {
		m.views = vs
	}
	m.settings, _ = monitor.LoadSettings(m.app.Home)
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
	for id := range m.picked {
		if seen[id] == nil {
			delete(m.picked, id)
		}
	}
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
	m.checkLogins()
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
	case loginOutMsg, loginEndMsg:
		return m.loginMsg(msg)
	case tea.KeyPressMsg:
		if m.login != nil {
			return m.keyLogin(msg)
		}
		if msg.String() == "ctrl+l" && m.mode == "" && !m.busy {
			m.keyLoginOpen()
			return m, nil
		}
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
	if m.mode == "rename" || m.mode == "save" {
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

// openPicked shows the picked hosts' graphs together, as pingy -g does.
func (m *model) openPicked() {
	m.view, m.mode, m.fromPick, m.scroll, m.focus = viewAll, "", true, 0, 0
	for _, h := range m.panels() {
		h.hist.LoadAll()
		h.hist.Refresh()
	}
	m.win = window{step: time.Second, live: true}
	m.query.Blur()
}

// viewHosts are the hosts of v still being pinged, and how many aren't.
func (m *model) viewHosts(v *savedView) (hs []*host, gone int) {
	metas := make([]*monitor.Meta, len(m.hosts))
	for i, h := range m.hosts {
		metas[i] = h.meta
	}
	found, gone := v.resolve(metas)
	for _, meta := range found {
		hs = append(hs, m.byID[meta.ID])
	}
	return hs, gone
}

// openView graphs a saved view's hosts together.
func (m *model) openView(v *savedView) {
	hs, _ := m.viewHosts(v)
	if len(hs) == 0 {
		m.note, m.noteBad = "None of "+v.Name+"'s hosts are being pinged any more", true
		return
	}
	m.picked, m.order = map[string]bool{}, nil
	for _, h := range hs {
		m.picked[h.meta.ID] = true
		m.order = append(m.order, h.meta.ID)
	}
	m.note, m.viewName = "", v.Name
	m.openPicked()
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

// pickRow is a host, a saved view, or, with add set, the offer to start
// pinging it.
type pickRow struct {
	h   *host
	v   *savedView
	add string
}

func (m *model) rows() []pickRow {
	q := strings.ToLower(strings.TrimSpace(m.query.Value()))
	// Hosts containing what's typed come first, then those with its
	// letters in order, as in fzf.
	var rows, loose []pickRow
	exact := false
	for i := range m.views {
		v := &m.views[i]
		name := strings.ToLower(v.Name)
		switch {
		case strings.Contains(name, q):
			rows = append(rows, pickRow{v: v})
		case fuzzy(name, q):
			loose = append(loose, pickRow{v: v})
		}
		if name == q {
			exact = true
		}
	}
	for _, h := range m.hosts {
		var fields []string
		for _, f := range []string{h.meta.Name, h.meta.Where(), h.state.Addr} {
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
		if strings.EqualFold(h.meta.Where(), q) || strings.EqualFold(h.meta.Name, q) {
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
		if (k == "y" || k == "Y") && m.targetView != "" {
			m.note, m.noteBad = "Deleted the view "+m.targetView+"; its hosts are still pinged", false
			if err := m.app.deleteView(m.targetView); err != nil {
				m.note, m.noteBad = err.Error(), true
			}
			m.refresh()
			return m, nil
		}
		if (k == "y" || k == "Y") && m.target != nil {
			h := m.target
			m.busy, m.note = true, "Stopping "+h.meta.Label()+"…"
			return m, func() tea.Msg {
				return opMsg{note: "Stopped pinging " + h.meta.Label() + " and deleted its history", err: m.app.remove(h.meta)}
			}
		}
		m.note = ""
		return m, nil
	}
	if m.mode == "history" {
		switch k {
		case "esc":
			m.mode = ""
			m.input.Blur()
			return m, m.query.Focus()
		case "enter":
			mb, err := monitor.ParseSize(m.input.Value())
			if err != nil {
				m.note, m.noteBad = err.Error(), true
				return m, nil
			}
			m.mode = ""
			m.input.Blur()
			m.busy, m.note = true, "Saving…"
			a := m.app
			return m, tea.Batch(m.query.Focus(), func() tea.Msg {
				return opMsg{
					note: "Each host now keeps up to " + monitor.FormatMB(mb) + " of history, " +
						fmtKeeps(monitor.Settings{HistoryMB: mb}) + " at one ping a second",
					err: a.setHistory(mb),
				}
			})
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		m.note = ""
		return m, cmd
	}
	if m.adding {
		switch k {
		case "enter":
			if len(m.picked) == 0 {
				m.note, m.noteBad = "Pick at least one host", true
				return m, nil
			}
			m.adding, m.multi, m.before, m.note = false, m.wasMulti, nil, ""
			m.openPicked()
			m.saveOrder()
			return m, nil
		case "esc":
			if m.query.Value() == "" {
				m.adding, m.multi, m.picked, m.before, m.note = false, m.wasMulti, m.before, nil, ""
				m.openPicked()
				return m, nil
			}
		case "ctrl+t":
			return m, nil
		}
	}
	if m.multi {
		switch k {
		case "tab", "shift+tab":
			if len(rows) > 0 {
				m.togglePick(rows[m.cursor])
			}
			if k == "tab" {
				m.cursor = min(m.cursor+1, max(len(rows)-1, 0))
			} else {
				m.cursor = max(m.cursor-1, 0)
			}
			return m, nil
		case "enter":
			if len(m.picked) == 0 && len(rows) > 0 && rows[m.cursor].h != nil {
				m.picked[rows[m.cursor].h.meta.ID] = true
			}
			if len(m.picked) > 0 {
				m.note = ""
				m.openPicked()
				return m, nil
			}
		case "esc":
			if m.query.Value() == "" {
				m.multi = false
				m.clearPicks()
				return m, nil
			}
		}
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
	case "ctrl+t":
		m.multi, m.note = !m.multi, ""
		m.clearPicks()
		return m, nil
	case "up", "ctrl+k", "shift+tab":
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
		if r.v != nil {
			m.openView(r.v)
			return m, nil
		}
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
		if len(rows) > 0 && (rows[m.cursor].h != nil || rows[m.cursor].v != nil) {
			m.mode, m.target, m.targetView = "kill", rows[m.cursor].h, ""
			if v := rows[m.cursor].v; v != nil {
				m.targetView = v.Name
			}
		}
		return m, nil
	case "ctrl+p":
		if len(rows) > 0 && rows[m.cursor].h != nil {
			m.busy = true
			return m, m.toggleCmd(rows[m.cursor].h)
		}
		if len(rows) > 0 && rows[m.cursor].v != nil {
			if hs, _ := m.viewHosts(rows[m.cursor].v); len(hs) > 0 {
				return m, m.toggleAllCmd(hs, rows[m.cursor].v.Name)
			}
		}
		return m, nil
	case "ctrl+a":
		if len(m.hosts) > 0 {
			return m, m.toggleAllCmd(m.hosts, "every host")
		}
		return m, nil
	case "ctrl+o":
		m.mode, m.note = "history", ""
		m.input.SetValue(monitor.FormatMB(m.settings.HistoryMB))
		m.input.Placeholder = "e.g. 64M or 1G"
		m.input.CursorEnd()
		m.query.Blur()
		return m, m.input.Focus()
	}
	before := m.query.Value()
	var cmd tea.Cmd
	m.query, cmd = m.query.Update(msg)
	if m.query.Value() != before {
		m.cursor, m.note = 0, ""
	}
	return m, cmd
}

// clearPicks forgets the hosts picked, and their view and order.
func (m *model) clearPicks() {
	m.picked, m.viewName, m.order = map[string]bool{}, "", nil
}

// togglePick picks or unpicks a host, or a view's hosts all together.
func (m *model) togglePick(r pickRow) {
	hs := []*host{r.h}
	if r.v != nil {
		hs, _ = m.viewHosts(r.v)
	} else if r.h == nil {
		return
	}
	all := len(hs) > 0
	for _, h := range hs {
		all = all && m.picked[h.meta.ID]
	}
	for _, h := range hs {
		if all {
			delete(m.picked, h.meta.ID)
		} else {
			m.picked[h.meta.ID] = true
		}
	}
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

// toggleAllCmd pauses hosts if any of them is running, or else starts
// them all again; what says which hosts they are, e.g. "every host".
func (m *model) toggleAllCmd(hs []*host, what string) tea.Cmd {
	metas := make([]*monitor.Meta, len(hs))
	running := false
	for i, h := range hs {
		metas[i] = h.meta
		running = running || h.running
	}
	m.busy, m.noteBad = true, false
	if running {
		m.note = "Pausing " + what + "…"
		return func() tea.Msg {
			paused, err := pauseAll(metas)
			return opMsg{note: fmt.Sprintf("Paused %d %s; their history is kept", len(paused), plural(len(paused), "host", "hosts")), err: err}
		}
	}
	m.note = "Starting " + what + " again…"
	a := m.app
	return func() tea.Msg {
		started, err := a.resumeAll(metas)
		return opMsg{note: fmt.Sprintf("Pinging %d %s again", len(started), plural(len(started), "host", "hosts")), err: err}
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
				err := m.app.remove(h.meta)
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
		m.input.Placeholder = h.meta.Where()
		m.input.CursorEnd()
		return m, m.input.Focus()
	case "x":
		m.mode, m.note = "kill", ""
	case "p":
		m.busy = true
		return m, m.toggleCmd(h)
	}
	return m, nil
}

// ---- every host ----

// panels are the hosts whose graphs viewAll shows: those picked in the
// picker, or every host for pingy -g. They follow order, with any it
// doesn't mention after, oldest first.
func (m *model) panels() []*host {
	var hs []*host
	for _, h := range m.hosts {
		if !m.fromPick || m.picked[h.meta.ID] {
			hs = append(hs, h)
		}
	}
	if len(m.order) == 0 {
		return hs
	}
	rank := map[string]int{}
	for i, id := range m.order {
		rank[id] = i
	}
	slices.SortStableFunc(hs, func(a, b *host) int {
		ra, aok := rank[a.meta.ID]
		rb, bok := rank[b.meta.ID]
		switch {
		case aok && bok:
			return ra - rb
		case aok:
			return -1
		case bok:
			return 1
		}
		return 0
	})
	return hs
}

// showFocus keeps the chosen panel in range and on screen.
func (m *model) showFocus() {
	_, fits := m.allLayout()
	m.focus = min(max(m.focus, 0), max(len(m.panels())-1, 0))
	m.scroll = max(min(m.scroll, m.focus), m.focus-fits+1)
	m.scrollBy(0)
}

// move shifts the chosen panel by d places. A saved view keeps the new
// order straight away.
func (m *model) move(d int) {
	hs := m.panels()
	i, j := m.focus, m.focus+d
	if i < 0 || i >= len(hs) || j < 0 || j >= len(hs) {
		return
	}
	hs[i], hs[j] = hs[j], hs[i]
	m.order = make([]string, len(hs))
	for n, h := range hs {
		m.order[n] = h.meta.ID
	}
	m.focus = j
	m.showFocus()
	m.saveOrder()
}

// saveOrder keeps what's on screen as the saved view being shown, if any.
func (m *model) saveOrder() {
	if m.viewName == "" {
		return
	}
	if err := m.app.updateView(m.viewName, m.panels()); err != nil {
		m.note, m.noteBad = err.Error(), true
	}
}

// remove takes the chosen panel off the screen, and out of its saved view.
func (m *model) remove() {
	hs := m.panels()
	if m.focus < 0 || m.focus >= len(hs) {
		return
	}
	if len(hs) == 1 {
		m.note, m.noteBad = "That's the only host here; a adds others first", true
		return
	}
	h := hs[m.focus]
	if m.fromPick {
		delete(m.picked, h.meta.ID)
	} else {
		if m.only == nil {
			m.only = map[string]bool{}
			for _, o := range hs {
				m.only[o.meta.ID] = true
			}
		}
		delete(m.only, h.meta.ID)
		m.refresh()
	}
	m.showFocus()
	m.note, m.noteBad = "Took "+h.meta.Label()+" off; it's still pinged", false
	if m.viewName != "" {
		m.note = "Took " + h.meta.Label() + " out of " + m.viewName + "; it's still pinged"
	}
	m.saveOrder()
}

// addMore goes to the picker with these hosts picked, to add or remove some.
func (m *model) addMore() tea.Cmd {
	hs := m.panels()
	m.picked = map[string]bool{}
	m.order = nil
	for _, h := range hs {
		m.picked[h.meta.ID] = true
		m.order = append(m.order, h.meta.ID)
	}
	m.before = maps.Clone(m.picked)
	m.only = nil // the picker lists every host
	m.refresh()
	m.wasMulti = m.multi
	m.view, m.multi, m.adding, m.mode, m.note = viewPick, true, true, "", ""
	m.query.SetValue("")
	m.cursor = 0
	return m.query.Focus()
}

// minPanelH is the shortest a host's panel gets in pingy -g, which leaves
// its graph six rows; hosts that don't fit are scrolled to.
const minPanelH = 12

// allLayout is how tall each panel is in pingy -g and how many fit on the
// screen at once, sharing out the screen so panels fill it.
func (m *model) allLayout() (ph, fits int) {
	n, keys := len(m.panels()), 1
	// The key lines below the panels depend on whether every host fits.
	for range 3 {
		room := max(m.h()-keys, 1)
		fits = min(max(n, 1), max(room/minPanelH, 1))
		ph = max(room/fits, min(room, minPanelH))
		k := len(m.allKeys(fits))
		if k <= keys {
			break
		}
		keys = k
	}
	return ph, fits
}

// allKeys are the lines of keys and position under pingy -g's panels.
func (m *model) allKeys(fits int) []string {
	u := m.u
	hosts := m.panels()
	var hints []string
	if m.viewName != "" {
		hints = append(hints, u.accent("▦ "+m.viewName))
	}
	when := "live"
	if !m.win.live {
		when = fmtAgo(time.Since(m.win.end)) + " ago"
	}
	hints = append(hints, when+" · "+fmtStep(m.win.step)+" per column")
	if len(hosts) > 1 {
		hints = append(hints, "↑↓ choose", "shift-↑↓ reorder")
	}
	hints = append(hints, "a add", "r remove", "s save", "p pause", "←→ time", "+/- zoom")
	if h := m.chosenHost(); h != nil && needsLogin(h) {
		hints = append(hints, u.fg(colWarn, "ctrl-l log in"))
	}
	if m.fromPick {
		hints = append(hints, "esc back")
	} else {
		hints = append(hints, "q quit")
	}
	if len(hosts) > fits {
		scroll := min(max(m.scroll, 0), len(hosts)-fits)
		above, below := scroll, len(hosts)-scroll-fits
		var parts []string
		if above > 0 {
			parts = append(parts, fmt.Sprintf("▲ %d above", above))
		}
		if below > 0 {
			parts = append(parts, fmt.Sprintf("▼ %d below", below))
		}
		shown := fmt.Sprintf("%d–%d", scroll+1, scroll+fits)
		if fits == 1 {
			shown = fmt.Sprint(scroll + 1)
		}
		hints = append(hints, u.fg(colAccent, strings.Join(parts, " · "))+u.faint(fmt.Sprintf("  %s of %d", shown, len(hosts))))
	}
	lines := u.keyLines(m.w(), hints...)
	switch {
	case m.mode == "save":
		lines[0] = u.fg(colAccent, "View name: ") + m.input.View() + u.faint("  enter save · esc cancel")
	case m.note != "":
		lines[0] = m.noteLine()
	}
	return lines
}

// scrollBy moves pingy -g's panels by n hosts, down when positive.
func (m *model) scrollBy(n int) {
	_, fits := m.allLayout()
	m.scroll = min(max(m.scroll+n, 0), max(len(m.panels())-fits, 0))
}

func (m *model) keyAll(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	k := msg.String()
	hosts := m.panels()
	if m.mode == "save" {
		switch k {
		case "esc":
			m.mode = ""
			m.input.Blur()
		case "enter":
			name := strings.TrimSpace(m.input.Value())
			if err := m.app.saveView(name, m.viewName, hosts); err != nil {
				m.note, m.noteBad = err.Error(), true
				return m, nil
			}
			m.mode, m.viewName = "", name
			m.input.Blur()
			m.refresh()
			m.note, m.noteBad = "Saved the view "+name+" · pingy -g "+name+" opens it", false
		default:
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return m, cmd
		}
		return m, nil
	}
	m.note = ""
	every := time.Second
	for _, h := range hosts {
		every = min(every, h.meta.Every())
	}
	if m.keyTime(k, m.w()-4-gutter-1, every, hosts...) {
		return m, nil
	}
	_, fits := m.allLayout()
	switch k {
	case "esc", "q", "backspace":
		if m.fromPick {
			m.view, m.fromPick = viewPick, false
			if !m.multi {
				m.clearPicks()
			}
			return m, m.query.Focus()
		}
		if k == "backspace" {
			return m, nil
		}
		m.quitting = true
		return m, tea.Quit
	case "up", "k":
		m.focus--
		m.showFocus()
	case "down", "j":
		m.focus++
		m.showFocus()
	case "shift+up", "K":
		m.move(-1)
	case "shift+down", "J":
		m.move(1)
	case "ctrl+u":
		m.focus -= fits
		m.showFocus()
	case "ctrl+d", "space":
		m.focus += fits
		m.showFocus()
	case "g":
		m.focus = 0
		m.showFocus()
	case "a":
		return m, m.addMore()
	case "p":
		if len(hosts) > 0 {
			what := "these hosts"
			if m.viewName != "" {
				what = m.viewName
			}
			return m, m.toggleAllCmd(hosts, what)
		}
	case "r":
		m.remove()
		return m, nil
	case "s":
		if len(hosts) > 0 {
			m.mode = "save"
			m.input.SetValue(m.viewName)
			m.input.Placeholder = "a name for this view"
			m.input.CursorEnd()
			return m, m.input.Focus()
		}
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
	if m.login != nil {
		s = m.overLogin(s)
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
	shown := 0
	for _, r := range rows {
		if r.h != nil {
			shown++
		}
	}
	count := u.faint(fmt.Sprintf("%d/%d", shown, len(m.hosts)))
	keys := []string{"enter open", "ctrl-t pick several", "ctrl-x kill", "ctrl-p pause", "ctrl-a pause all", "ctrl-o history", "esc quit"}
	if slices.ContainsFunc(m.hosts, needsLogin) {
		keys = append([]string{u.fg(colWarn, "ctrl-l log in")}, keys...)
	}
	if m.multi {
		count = u.fg(colAccent, fmt.Sprintf("%d picked ", len(m.picked))) + count
		keys = []string{"tab pick", "enter graph them", "esc done"}
		if m.adding {
			keys = []string{"tab add or remove", "enter done", "esc cancel"}
			if m.viewName != "" {
				keys = []string{u.accent("▦ " + m.viewName), "tab add or remove", "enter save", "esc cancel"}
			}
		}
	}
	left = append(left, spread(u.fg(colAccent, "❯ ")+m.query.View(), count, leftW))
	left = append(left, u.keyLines(leftW, keys...)...)
	left = append(left, u.faint(strings.Repeat("─", leftW)))
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
		if m.multi && r.h != nil {
			if m.picked[r.h.meta.ID] {
				pointer += u.fg(colAccent, "✓ ")
			} else {
				pointer += u.faint("· ")
			}
		}
		if r.add != "" {
			label := "Ping " + r.add
			if sel {
				label = u.bold(label)
			}
			left = append(left, pointer+u.fg(colAccent, "+ ")+label)
			continue
		}
		if r.v != nil {
			hs, gone := m.viewHosts(r.v)
			if m.multi {
				all := len(hs) > 0
				for _, h := range hs {
					all = all && m.picked[h.meta.ID]
				}
				if all {
					pointer += u.fg(colAccent, "✓ ")
				} else {
					pointer += u.faint("· ")
				}
			}
			name := r.v.Name
			if sel {
				name = u.bold(name)
			}
			right := fmt.Sprintf("%d %s", len(hs), plural(len(hs), "host", "hosts"))
			if gone > 0 {
				right += fmt.Sprintf(" · %d gone", gone)
			}
			left = append(left, spread(pointer+u.fg(colAccent, "▦ ")+name, u.faint(right), leftW))
			continue
		}
		glyph, word, c := u.status(r.h, now)
		name := r.h.meta.Label()
		if sel {
			name = u.bold(name)
		}
		if r.h.meta.Name != "" {
			name += " " + u.faint(r.h.meta.Where())
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
	switch {
	case m.mode == "history":
		left[len(left)-1] = u.fg(colAccent, "History per host: ") + m.input.View()
	case m.mode == "kill" && m.targetView != "":
		left[len(left)-1] = u.fg(colBad, "Delete the view "+m.targetView+"? Its hosts keep pinging. y/n")
	case m.mode == "kill" && m.target != nil:
		left[len(left)-1] = u.fg(colBad, "Stop pinging "+m.target.meta.Label()+" and delete its history? y/n")
	case m.note == "" && len(m.hosts) > 0:
		left[len(left)-1] = u.faint(m.historyLine())
	}

	lines := left
	if rightW > 0 {
		var preview []string
		switch {
		case len(rows) == 0:
			preview = strings.Split(u.box("", nil, rightW, innerH, colBorder), "\n")
		case rows[m.cursor].v != nil:
			preview = m.viewPreview(rows[m.cursor].v, rightW, innerH)
		case rows[m.cursor].add != "":
			t := rows[m.cursor].add
			preview = strings.Split(u.box(u.accent("+ "+t), []string{
				"Press enter to start pinging " + u.bold(t) + " in the background,",
				"then watch it here.",
			}, rightW, innerH, colBorder), "\n")
		default:
			preview = u.hostPanel(rows[m.cursor].h, now, window{step: time.Second, live: true}, rightW, innerH, false)
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

// historyLine says how much disk the hosts' history takes.
func (m *model) historyLine() string {
	var used int64
	for _, h := range m.hosts {
		used += h.hist.Size()
	}
	return monitor.FormatBytes(used) + " of history · up to " + monitor.FormatMB(m.settings.HistoryMB) + " per host"
}

// viewPreview lists a saved view's hosts, as the picker's preview.
func (m *model) viewPreview(v *savedView, w, hgt int) []string {
	u := m.u
	hs, gone := m.viewHosts(v)
	now := time.Now()
	var lines []string
	for _, h := range hs {
		glyph, _, c := u.status(h, now)
		lines = append(lines, spread(u.fg(c, glyph)+" "+h.meta.Label(), u.lastRTT(h), w-4))
	}
	if gone > 0 {
		lines = append(lines, u.faint(fmt.Sprintf("%d no longer pinged; it comes back if pinged again", gone)))
	}
	lines = append(lines, "", u.faint("enter graphs them together · ctrl-x deletes the view"))
	return strings.Split(u.box(u.accent("▦ "+v.Name), lines, w, hgt, colBorder), "\n")
}

// hostPanel draws h's recent graph in a w×hgt box, as the picker's preview
// and in pingy -g. The chosen one of several is heavier and marked, so it
// stands out whatever the theme.
func (u ui) hostPanel(h *host, now time.Time, win window, w, hgt int, chosen bool) []string {
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
	if chosen {
		return strings.Split(u.frame(u.accent("▸ "+h.meta.Label()), lines, w, hgt, colAccent, heavyEdges), "\n")
	}
	return strings.Split(u.box(u.accent(h.meta.Label()), lines, w, hgt, colBorder), "\n")
}

// statusLine is a host's status, address and interval.
func (u ui) statusLine(h *host, now time.Time) string {
	glyph, word, c := u.status(h, now)
	parts := []string{u.fg(c, strings.TrimSpace(glyph+" "+word))}
	switch {
	case h.state.Link != "" && h.running:
		parts[0] = u.fg(c, glyph+" "+h.state.Link)
	case h.state.Error != "" && (h.state.Addr == "" || h.meta.Origin != "" && h.running):
		parts[0] = u.fg(c, glyph+" "+h.state.Error)
	}
	if h.meta.Name != "" {
		parts = append(parts, h.meta.Where())
	}
	switch {
	case h.meta.Origin == "":
	case h.state.Via == "pingy":
		parts = append(parts, u.faint("by pingy on "+h.meta.Origin))
	case h.state.Via == "script":
		parts = append(parts, u.faint("by a script on "+h.meta.Origin))
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
		spread(u.faint(span+" · "+fmtStep(step)+" per column · "+monitor.FormatBytes(h.hist.Size())+" of history"), when, innerW),
		"",
	}
	keys := []string{"←→ move", "shift-←→ page", "home/end", "+/- zoom", "n rename", "p pause", "x kill"}
	if needsLogin(h) {
		keys = append([]string{u.fg(colWarn, "ctrl-l log in")}, keys...)
	}
	switch {
	case m.mode == "rename":
		keys = []string{"enter save", "esc cancel", "empty for the address"}
	case m.sess != nil && m.sess.owned:
		keys = append(keys, "d detach", "q stop & quit")
	case m.sess != nil:
		keys = append(keys, "q quit")
	case m.fromPick:
		keys = append(keys, "esc back")
	}
	keyLines := u.keyLines(innerW, keys...)
	foot := 3 + len(keyLines) // stats, a gap, the note, then the keys
	gh := innerH - len(lines) - foot
	lines = append(lines, u.graph(h.samples(), end, step, innerW, gh)...)
	for len(lines) < innerH-foot {
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

	lines = append(lines, keyLines...)
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
	hosts := m.panels()
	var lines []string
	switch {
	case len(hosts) > 0:
	case m.fromPick:
		lines = append(lines, u.faint("The hosts picked were stopped and deleted elsewhere. esc goes back."))
	default:
		lines = append(lines, u.faint("Nothing is being pinged. Start with: pingy <host> -d"))
	}
	ph, fits := m.allLayout()
	m.showFocus() // stay in range when hosts go or the screen grows
	for i := m.scroll; i < len(hosts) && i < m.scroll+fits; i++ {
		chosen := i == m.focus && len(hosts) > 1
		lines = append(lines, u.hostPanel(hosts[i], now, m.win, W, ph, chosen)...)
	}
	keys := m.allKeys(fits)
	for len(lines) < H-len(keys) {
		lines = append(lines, "")
	}
	lines = append(lines[:max(H-len(keys), 0)], keys...)
	return strings.Join(lines, "\n")
}
