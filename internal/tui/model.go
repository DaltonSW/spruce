// Package tui implements the Bubble Tea (v2) front-end. It depends only on
// core.Backend + the backend registry, never on any specific package manager.
package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"go.dalton.dog/spruce/internal/core"
)

type state int

const (
	stateDiscovering state = iota
	stateSelecting
	stateReviewing
	stateApplying
	stateDone
)

// row is one selectable line in the Selecting list.
type row struct {
	source string
	update core.Update
}

// srcState tracks live progress for one backend during Applying.
type srcState struct {
	phase    string
	item     string
	done     int
	fraction float64 // 0–1 progress for the current item, if reported
	failed   bool
	finished bool
	errText  string
	logs     []string // tail of this backend's raw output, shown in its panel
}

// appendLog keeps a bounded tail of the backend's output for its panel.
func (st *srcState) appendLog(line string) {
	const max = 200
	st.logs = append(st.logs, line)
	if len(st.logs) > max {
		st.logs = st.logs[len(st.logs)-max:]
	}
}

// Model is the whole application state.
type Model struct {
	ctx    context.Context
	cancel context.CancelFunc

	state         state
	width, height int

	// Discovery results, keyed for Apply routing.
	byName map[string]core.Backend
	errs   map[string]string // backend name -> Check error

	// Selecting. Each backend ("source") is rendered as its own always-visible
	// panel; navigation is panel-local so a 200-package System list can't bury
	// the smaller backends.
	rows        []row
	discovered  []string        // every detected backend, in Available() order; gets a panel even while still checking or empty
	checking    map[string]bool // backends whose Check() hasn't returned yet (panel shows a spinner)
	checkCh     <-chan checkResult
	selected    map[string]bool // keyed by Update.ID()
	focus       int             // index into panels()
	panelCursor map[string]int  // per-source cursor (row index within that source)
	panelOffset map[string]int  // per-source scroll offset

	// Applying
	applyCh  <-chan core.ProgressEvent
	progress map[string]*srcState

	// Flags from the CLI.
	autoYes bool // -y: skip the gates and apply the default selection at once
	demo    bool // --demo: use fake backends, no real system access
	dryRun  bool // --dry-run: simulate the apply, mutating nothing

	spinner int
	ticking bool // whether a spinner tick loop is currently running
}

// Options carries the CLI flags that shape a run.
type Options struct {
	AutoYes bool
	Demo    bool
	DryRun  bool
}

// New builds the initial model. ctx is cancelled when the user quits, which
// aborts any in-flight backend work.
func New(ctx context.Context, cancel context.CancelFunc, opts Options) Model {
	return Model{
		ctx:         ctx,
		cancel:      cancel,
		state:       stateDiscovering,
		byName:      map[string]core.Backend{},
		errs:        map[string]string{},
		checking:    map[string]bool{},
		selected:    map[string]bool{},
		progress:    map[string]*srcState{},
		panelCursor: map[string]int{},
		panelOffset: map[string]int{},
		autoYes:     opts.AutoYes,
		demo:        opts.Demo,
		dryRun:      opts.DryRun,
		ticking:     true, // Init starts a tick loop
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(availableCmd(m.demo), tickCmd())
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.clampAllPanels()
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case tickMsg:
		m.spinner++
		if m.state == stateDiscovering || m.state == stateApplying || len(m.checking) > 0 {
			return m, tickCmd()
		}
		m.ticking = false
		return m, nil

	case availableMsg:
		return m.onAvailable(msg)

	case checkStreamMsg:
		m.checkCh = msg.ch
		return m, waitForCheck(m.checkCh)

	case checkedMsg:
		return m.onChecked(msg)

	case checkDoneMsg:
		return m.onCheckDone()

	case applyReadyMsg:
		m.applyCh = msg.ch
		return m, waitForEvent(m.applyCh)

	case applyEventMsg:
		m.applyEvent(msg.ev)
		return m, waitForEvent(m.applyCh)

	case applyDoneMsg:
		m.state = stateDone
		return m, nil
	}

	return m, nil
}

func (m Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		m.cancel()
		return m, tea.Quit
	}

	switch m.state {
	case stateSelecting:
		return m.keySelecting(msg)
	case stateReviewing:
		return m.keyReviewing(msg)
	case stateDone:
		switch msg.String() {
		case "q", "enter", "esc":
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m Model) keySelecting(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q":
		m.cancel()
		return m, tea.Quit
	case "up", "k":
		m.moveCursor(-1)
	case "down", "j":
		m.moveCursor(1)
	case "left", "h":
		m.focus = 0
	case "right", "l":
		if len(m.panels()) > 1 && m.focus == 0 {
			m.focus = 1
		}
	case "tab":
		if n := len(m.panels()); n > 0 {
			m.focus = (m.focus + 1) % n
		}
	case "shift+tab":
		if n := len(m.panels()); n > 0 {
			m.focus = (m.focus - 1 + n) % n
		}
	case " ":
		m.toggleCurrent()
	case "a":
		m.setAll(true)
	case "N":
		m.setAll(false)
	case "d":
		m.dryRun = !m.dryRun
	case "enter":
		if m.anySelected() {
			m.state = stateReviewing
		}
	}
	return m, nil
}

// moveCursor moves the cursor within the focused panel. In the right-hand
// column (focus >= 1) it spills into the adjacent stacked panel at the edges, so
// the right side reads as one continuous list.
func (m *Model) moveCursor(d int) {
	ps := m.panels()
	if len(ps) == 0 {
		return
	}
	if m.focus >= len(ps) {
		m.focus = len(ps) - 1
	}
	src := ps[m.focus]
	n := len(m.sourceRows(src))
	c := m.panelCursor[src] + d

	switch {
	case c < 0:
		if m.focus > 1 { // spill up to previous right-column panel
			m.focus--
			prev := ps[m.focus]
			m.panelCursor[prev] = len(m.sourceRows(prev)) - 1
			m.clampPanel(prev)
			return
		}
		c = 0
	case c >= n:
		if m.focus >= 1 && m.focus < len(ps)-1 { // spill down to next right panel
			m.focus++
			next := ps[m.focus]
			m.panelCursor[next] = 0
			m.clampPanel(next)
			return
		}
		c = n - 1
	}
	if c < 0 {
		c = 0
	}
	m.panelCursor[src] = c
	m.clampPanel(src)
}

func (m Model) keyReviewing(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "b", "n":
		m.state = stateSelecting
	case "d":
		m.dryRun = !m.dryRun
	case "y", "enter":
		m.state = stateApplying
		return m, tea.Batch(startApplyCmd(m.ctx, m.selectionByBackend(), m.byName, m.dryRun), m.ensureTick())
	case "q", "ctrl+c":
		m.cancel()
		return m, tea.Quit
	}
	return m, nil
}

// ensureTick starts the spinner tick loop if it isn't already running, so we
// never stack two loops (which would animate at double speed).
func (m *Model) ensureTick() tea.Cmd {
	if m.ticking {
		return nil
	}
	m.ticking = true
	return tickCmd()
}

// onAvailable records every detected backend and shows their panels right away
// (each as a spinner), then kicks off the streaming Check across all of them.
func (m *Model) onAvailable(msg availableMsg) (tea.Model, tea.Cmd) {
	for _, b := range msg.backends {
		name := b.Name()
		m.byName[name] = b
		m.discovered = append(m.discovered, name)
		m.checking[name] = true
	}
	m.state = stateSelecting
	if len(msg.backends) == 0 {
		return *m, nil // nothing detected
	}
	return *m, tea.Batch(startCheckCmd(m.ctx, msg.backends), m.ensureTick())
}

// onChecked folds one backend's Check result into the model as it arrives, so
// its panel fills in (or shows an error) without waiting for the others.
func (m *Model) onChecked(msg checkedMsg) (tea.Model, tea.Cmd) {
	r := msg.result
	name := r.Backend.Name()
	delete(m.checking, name)
	if r.Err != nil {
		m.errs[name] = r.Err.Error()
	} else {
		for _, u := range r.Updates {
			m.rows = append(m.rows, row{source: name, update: u})
			// Default: everything selectable is selected; pinned stays off.
			m.selected[u.ID()] = !u.Pinned
		}
	}
	m.clampAllPanels()
	return *m, waitForCheck(m.checkCh)
}

// onCheckDone fires once every backend has reported. For -y this is where we
// finally have the full selection and can apply it.
func (m *Model) onCheckDone() (tea.Model, tea.Cmd) {
	m.checkCh = nil
	if m.autoYes && m.anySelected() {
		m.state = stateApplying
		return *m, tea.Batch(startApplyCmd(m.ctx, m.selectionByBackend(), m.byName, m.dryRun), m.ensureTick())
	}
	return *m, nil
}

// --- selection helpers -----------------------------------------------------

func (m *Model) toggleCurrent() {
	r, ok := m.currentRow()
	if !ok || r.update.Pinned {
		return // nothing focused, or pinned items can't be selected
	}
	id := r.update.ID()
	m.selected[id] = !m.selected[id]
}

// currentRow returns the row under the cursor in the focused panel.
func (m Model) currentRow() (row, bool) {
	ps := m.panels()
	if len(ps) == 0 {
		return row{}, false
	}
	f := m.focus
	if f >= len(ps) {
		f = len(ps) - 1
	}
	rs := m.sourceRows(ps[f])
	i := m.panelCursor[ps[f]]
	if i < 0 || i >= len(rs) {
		return row{}, false
	}
	return rs[i], true
}

func (m *Model) setAll(v bool) {
	for _, r := range m.rows {
		if r.update.Pinned {
			continue
		}
		m.selected[r.update.ID()] = v
	}
}

func (m Model) anySelected() bool {
	for _, v := range m.selected {
		if v {
			return true
		}
	}
	return false
}

// selectionByBackend groups the currently-selected updates by backend name.
func (m Model) selectionByBackend() map[string][]core.Update {
	out := map[string][]core.Update{}
	for _, r := range m.rows {
		if m.selected[r.update.ID()] {
			out[r.source] = append(out[r.source], r.update)
		}
	}
	return out
}

// --- apply event handling --------------------------------------------------

func (m *Model) applyEvent(ev core.ProgressEvent) {
	st := m.progress[ev.Source]
	if st == nil {
		st = &srcState{}
		m.progress[ev.Source] = st
	}
	switch ev.Kind {
	case core.EventPhase:
		st.phase = ev.Phase
		st.fraction = 0
		if ev.Item != "" {
			st.item = ev.Item
		}
	case core.EventProgress:
		st.fraction = ev.Fraction
	case core.EventItemDone:
		st.done++
		st.fraction = 0
	case core.EventError:
		st.failed = true
		st.errText = ev.Text
		st.appendLog("✗ " + ev.Text)
	case core.EventPrompt:
		st.appendLog("⏸ " + ev.Text)
	case core.EventDone:
		st.finished = true
		st.phase = "Done"
	case core.EventLog:
		st.appendLog(ev.Text)
	}
}
