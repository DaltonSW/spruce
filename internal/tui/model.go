// Package tui implements the Bubble Tea (v2) front-end. It depends only on
// core.Backend + the backend registry, never on any specific package manager.
package tui

import (
	"context"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"

	"go.dalton.dog/spruce/internal/core"
)

type state int

const (
	stateDiscovering state = iota
	stateSelecting
	stateReviewing
	stateConfirmInstall
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
	status   string // transaction-wide phase label (EventStatus), e.g. "resolving dependencies…"
	item     string
	done     int
	fraction float64            // 0–1 progress for the current item, if reported
	pkgFrac  map[string]float64 // monotonic per-package progress (0–1), keyed by name
	failed   bool
	finished bool
	errText  string
	seen     map[string]bool // package names that have been the active item at least once
	logs     []string        // tail of this backend's raw output, shown in its panel

	started    time.Time // first event seen for this backend (apply start)
	finishedAt time.Time // when EventDone arrived, to freeze the elapsed timer
}

// elapsed is how long this backend has been (or was) applying. Zero before the
// first event; frozen once finished.
func (st *srcState) elapsed() time.Duration {
	if st.started.IsZero() {
		return 0
	}
	if !st.finishedAt.IsZero() {
		return st.finishedAt.Sub(st.started)
	}
	return time.Since(st.started)
}

// markSeen records that a package name has been (or is) the active item, so the
// apply view can mark it done once a later package takes over — important for
// backends (PackageKit) that run one transaction and never emit EventItemDone.
func (st *srcState) markSeen(name string) {
	if name == "" {
		return
	}
	if st.seen == nil {
		st.seen = map[string]bool{}
	}
	st.seen[name] = true
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

	keys    keyMap
	spinner spinner.Model // braille activity spinner (bubbles)

	// Discovery results, keyed for Apply routing.
	byName map[string]core.Backend
	errs   map[string]string // backend name -> Check error

	// Selecting. Each backend ("source") is rendered as its own always-visible
	// panel; navigation is panel-local so a 200-package System list can't bury
	// the smaller backends.
	rows       []row
	discovered []string        // every detected backend, in Available() order; gets a panel even while still checking or empty
	checking   map[string]bool // backends whose Check() hasn't returned yet (panel shows a spinner)
	checkCh    <-chan checkResult
	selected   map[string]bool // keyed by Update.ID()
	focus      int             // index into panels()

	// installTarget is the single package to install when in stateConfirmInstall
	// (the (i) flow). Independent of the m.selected multi-selection.
	installTarget *row

	// One table per backend owns that panel's cursor + scroll; spruce keeps
	// selection (m.selected) and styling external. Pointers, so all value-copies
	// of Model share the same table state.
	tables map[string]*table.Model

	// Reviewing / ConfirmInstall
	// plans holds the resolved Plan per backend for the current confirm screen,
	// reused as the input to Apply so each backend is planned once. planning is
	// true while resolvePlansCmd is in flight (the modal shows a spinner).
	plans    map[string]core.Plan
	planning bool
	// planCache memoizes resolved single-package plans (keyed by Update.ID()) so
	// re-opening a package's install-one modal skips the brew dry-run. Cleared on
	// restartChecks so versions can't go stale across runs.
	planCache map[string]core.Plan

	// Applying
	applyCh  <-chan core.ProgressEvent
	progress map[string]*srcState
	// applying is the exact selection handed to this apply run, grouped by
	// backend. The apply view reads this (not m.selected) so the (i) single-package
	// flow shows only the package it's installing, not the whole default selection.
	applying map[string][]core.Update

	// Flags from the CLI.
	autoYes bool // -y: skip the gates and apply the default selection at once
	demo    bool // --demo: use fake backends, no real system access
	dryRun  bool // --dry-run: simulate the apply, mutating nothing

	tick     int  // monotonic 30fps counter driving the gradient border phase
	ticking  bool // whether the gradient tick loop is currently running
	spinning bool // whether the braille spinner's tick loop is currently running
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
	sp := spinner.New(spinner.WithSpinner(spinner.Spinner{
		Frames: []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"},
		FPS:    time.Second / 10,
	}))

	return Model{
		ctx:       ctx,
		cancel:    cancel,
		state:     stateDiscovering,
		keys:      defaultKeys(),
		spinner:   sp,
		byName:    map[string]core.Backend{},
		errs:      map[string]string{},
		checking:  map[string]bool{},
		selected:  map[string]bool{},
		progress:  map[string]*srcState{},
		tables:    map[string]*table.Model{},
		planCache: map[string]core.Plan{},
		autoYes:   opts.AutoYes,
		demo:      opts.Demo,
		dryRun:    opts.DryRun,
		ticking:   true, // Init starts the gradient tick loop
		spinning:  true, // Init starts the spinner tick loop
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(availableCmd(m.demo), tickCmd(), m.spinner.Tick)
}

// animating reports whether anything on screen needs the spinner/gradient
// ticking: discovery, an in-flight Check, or an apply in progress.
func (m Model) animating() bool {
	return m.state == stateDiscovering || m.state == stateApplying ||
		m.planning || len(m.checking) > 0
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.syncAllPanels()
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case tickMsg:
		m.tick++
		if m.animating() {
			return m, tickCmd()
		}
		m.ticking = false
		return m, nil

	case spinner.TickMsg:
		if !m.animating() {
			m.spinning = false // let the spinner loop die while idle
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

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

	case plansResolvedMsg:
		return m.onPlansResolved(msg)
	}

	return m, nil
}

// onPlansResolved stores the resolved plans for the confirm screens and derives
// the apply selection from them. If the user already committed to apply (the -y
// fast path, or Apply pressed before planning finished), it launches the apply
// now that plans are in hand.
func (m Model) onPlansResolved(msg plansResolvedMsg) (tea.Model, tea.Cmd) {
	m.plans = msg.plans
	m.planning = false
	m.applying = map[string][]core.Update{}
	for name, p := range m.plans {
		m.applying[name] = p.Selected
		// Cache single-package plans so re-opening that package's install-one
		// modal is instant (its brew dry-run won't re-run). Keyed by Update.ID().
		if len(p.Selected) == 1 {
			m.planCache[p.Selected[0].ID()] = p
		}
	}
	if m.state == stateApplying && m.applyCh == nil {
		m.seedApplyProgress()
		return m, tea.Batch(startApplyCmd(m.ctx, m.plans, m.byName, m.dryRun), m.ensureTick())
	}
	return m, nil
}

// beginApply transitions into Applying. When plans are still resolving it parks
// in Applying and lets onPlansResolved launch the run; otherwise it starts
// immediately from the already-resolved plans.
func (m Model) beginApply() (tea.Model, tea.Cmd) {
	m.state = stateApplying
	if m.planning {
		return m, m.ensureTick()
	}
	m.seedApplyProgress()
	return m, tea.Batch(startApplyCmd(m.ctx, m.plans, m.byName, m.dryRun), m.ensureTick())
}

// seedApplyProgress marks the apply start time for every backend about to run, so
// the panel's elapsed timer counts the full wall-clock — including a backend's
// silent prep phase (dnf5's metadata/depsolve can run many seconds before it
// emits its first progress signal) — rather than starting only at the first
// event. applyEvent won't reset an already-started clock, so the count is stable.
func (m *Model) seedApplyProgress() {
	for name, p := range m.plans {
		if len(p.Selected) == 0 {
			continue
		}
		if m.progress[name] == nil {
			m.progress[name] = &srcState{started: time.Now(), status: "preparing…"}
		}
	}
}

func (m Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if key.Matches(msg, m.keys.Cancel) {
		m.cancel()
		return m, tea.Quit
	}

	switch m.state {
	case stateSelecting:
		return m.keySelecting(msg)
	case stateReviewing:
		return m.keyReviewing(msg)
	case stateConfirmInstall:
		return m.keyConfirmInstall(msg)
	case stateDone:
		if key.Matches(msg, m.keys.More) {
			return m.returnToUpdates()
		}
		if key.Matches(msg, m.keys.QuitDone) {
			return m, tea.Quit
		}
	}
	return m, nil
}

// restartChecks returns from the Done screen to a fresh Selecting list, re-running
// Check across every discovered backend so the just-applied updates drop off and
// the user sees what's still available to update.
func (m Model) restartChecks() (tea.Model, tea.Cmd) {
	backends := make([]core.Backend, 0, len(m.discovered))
	for _, name := range m.discovered {
		if b := m.byName[name]; b != nil {
			backends = append(backends, b)
			m.checking[name] = true
		}
	}

	// Drop the previous run's results so nothing stale lingers; checks repopulate.
	m.rows = nil
	m.selected = map[string]bool{}
	m.errs = map[string]string{}
	m.progress = map[string]*srcState{}
	m.applying = nil
	m.plans = nil
	m.planning = false
	m.planCache = map[string]core.Plan{}
	m.applyCh = nil
	m.focus = 0
	m.tables = map[string]*table.Model{} // fresh tables: cursor/scroll start at top
	m.syncAllPanels()

	m.state = stateSelecting
	if len(backends) == 0 {
		return m, nil
	}
	return m, tea.Batch(startCheckCmd(m.ctx, backends), m.ensureTick())
}

// returnToUpdates is the fast return from the Done screen: it reuses the cached
// rows (still in the model — apply never clears them) instead of re-running Check
// on every backend, which on a real system stalls for seconds. It prunes only the
// packages a backend applied cleanly; a backend that failed keeps its items so the
// user can retry. Genuinely-fresh data is one Rescan (ctrl+r) away.
func (m Model) returnToUpdates() (tea.Model, tea.Cmd) {
	// Collect the IDs successfully applied, from backends that finished without
	// failing. Failed/unfinished backends are skipped so their rows survive.
	remove := map[string]bool{}
	for name, ups := range m.applying {
		st := m.progress[name]
		if st == nil || !st.finished || st.failed {
			continue
		}
		for _, u := range ups {
			remove[u.ID()] = true
		}
	}

	if len(remove) > 0 {
		kept := m.rows[:0:0]
		for _, r := range m.rows {
			id := r.update.ID()
			if remove[id] {
				delete(m.selected, id)
				delete(m.planCache, id)
				continue
			}
			kept = append(kept, r)
		}
		m.rows = kept
	}

	// Reset only the per-apply-run state; keep discovery (byName/discovered/errs)
	// and the surviving selection intact.
	m.progress = map[string]*srcState{}
	m.applying = nil
	m.plans = nil
	m.planning = false
	m.applyCh = nil
	m.installTarget = nil

	// Fresh tables so cursors can't dangle past the now-shorter row slices.
	m.tables = map[string]*table.Model{}
	m.focus = 0
	m.syncAllPanels()

	m.state = stateSelecting
	return m, nil
}

func (m Model) keySelecting(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Quit):
		m.cancel()
		return m, tea.Quit
	case key.Matches(msg, m.keys.Rescan):
		if len(m.checking) == 0 {
			return m.restartChecks()
		}
	case key.Matches(msg, m.keys.Up):
		m.moveInPanel(func(t *table.Model) { t.MoveUp(1) })
	case key.Matches(msg, m.keys.Down):
		m.moveInPanel(func(t *table.Model) { t.MoveDown(1) })
	case key.Matches(msg, m.keys.PageUp):
		m.moveInPanel(func(t *table.Model) { t.MoveUp(t.Height()) })
	case key.Matches(msg, m.keys.PageDown):
		m.moveInPanel(func(t *table.Model) { t.MoveDown(t.Height()) })
	case key.Matches(msg, m.keys.Home):
		m.moveInPanel(func(t *table.Model) { t.GotoTop() })
	case key.Matches(msg, m.keys.End):
		m.moveInPanel(func(t *table.Model) { t.GotoBottom() })
	case key.Matches(msg, m.keys.Left):
		if m.focus > 0 {
			m.setFocus(m.focus - 1)
		}
	case key.Matches(msg, m.keys.Right):
		if m.focus < len(m.panels())-1 {
			m.setFocus(m.focus + 1)
		}
	case key.Matches(msg, m.keys.Tab):
		if n := len(m.panels()); n > 0 {
			m.setFocus((m.focus + 1) % n)
		}
	case key.Matches(msg, m.keys.ShiftTab):
		if n := len(m.panels()); n > 0 {
			m.setFocus((m.focus - 1 + n) % n)
		}
	case key.Matches(msg, m.keys.Toggle):
		m.toggleCurrent()
	case key.Matches(msg, m.keys.All):
		m.setAll(true)
	case key.Matches(msg, m.keys.None):
		m.setAll(false)
	case key.Matches(msg, m.keys.DryRun):
		m.dryRun = !m.dryRun
	case key.Matches(msg, m.keys.Review):
		if m.anySelected() {
			m.state = stateReviewing
			return m.startPlanning(m.selectionByBackend())
		}
	case key.Matches(msg, m.keys.Install):
		if r, ok := m.currentRow(); ok {
			rr := r
			m.installTarget = &rr
			m.state = stateConfirmInstall
			// Clear Pinned on the copy so backends that skip pinned items in
			// Apply (e.g. brew) honor this explicit per-package override.
			u := rr.update
			u.Pinned = false
			return m.startPlanning(map[string][]core.Update{rr.source: {u}})
		}
	}
	return m, nil
}

// startPlanning resolves the Plan for the given selection. A fully-cached
// selection (the common case once an install-one modal has been opened before)
// is served instantly with no spinner; otherwise it marks the confirm screen
// busy and resolves in the background until plansResolvedMsg lands.
func (m Model) startPlanning(sel map[string][]core.Update) (tea.Model, tea.Cmd) {
	if plans, ok := m.cachedPlans(sel); ok {
		m.plans = plans
		m.planning = false
		return m, nil
	}
	m.plans = nil
	m.planning = true
	return m, tea.Batch(resolvePlansCmd(m.ctx, sel, m.byName), m.ensureTick())
}

// cachedPlans returns the cached Plans for a selection, succeeding only when
// every backend's selection is a single package whose plan is already cached.
// This targets the install-one modal (one package, one backend); multi-package
// review selections miss and resolve fresh.
func (m Model) cachedPlans(sel map[string][]core.Update) (map[string]core.Plan, bool) {
	out := make(map[string]core.Plan, len(sel))
	for name, ups := range sel {
		if len(ups) != 1 {
			return nil, false
		}
		p, ok := m.planCache[ups[0].ID()]
		if !ok {
			return nil, false
		}
		out[name] = p
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

// moveInPanel applies a movement to the focused panel's table, then resyncs that
// panel so the ▶ cursor marker follows. Navigation is panel-local: the cursor
// never leaves the focused panel (use Tab / ←→ to switch panels).
func (m *Model) moveInPanel(move func(*table.Model)) {
	src := m.focusedSource()
	if src == "" {
		return
	}
	move(m.tableFor(src))
	m.syncTable(src)
}

// setFocus moves the panel focus, resyncing the panels that gained and lost it so
// the ▶ marker and border highlight track the change.
func (m *Model) setFocus(i int) {
	old := m.focusedSource()
	m.focus = i
	m.syncTable(old)
	m.syncTable(m.focusedSource())
}

func (m Model) keyReviewing(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Back):
		m.state = stateSelecting
	case key.Matches(msg, m.keys.DryRun):
		m.dryRun = !m.dryRun
	case key.Matches(msg, m.keys.Apply):
		return m.beginApply()
	case key.Matches(msg, m.keys.Quit):
		m.cancel()
		return m, tea.Quit
	}
	return m, nil
}

// keyConfirmInstall handles the (i) single-package confirmation modal: apply just
// the hovered package via its backend, or cancel back to Selecting.
func (m Model) keyConfirmInstall(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Back):
		m.state = stateSelecting
		m.installTarget = nil
	case key.Matches(msg, m.keys.DryRun):
		m.dryRun = !m.dryRun
	case key.Matches(msg, m.keys.Apply):
		m.installTarget = nil
		return m.beginApply()
	case key.Matches(msg, m.keys.Quit):
		m.cancel()
		return m, tea.Quit
	}
	return m, nil
}

// ensureTick restarts the animation loops (gradient border + braille spinner) if
// they aren't already running — each loop halts itself when nothing is animating,
// so this is called whenever we re-enter an animating state (e.g. starting an
// apply). The per-loop guards stop us stacking two loops (double-speed animation).
func (m *Model) ensureTick() tea.Cmd {
	var cmds []tea.Cmd
	if !m.ticking {
		m.ticking = true
		cmds = append(cmds, tickCmd())
	}
	if !m.spinning {
		m.spinning = true
		cmds = append(cmds, m.spinner.Tick)
	}
	return tea.Batch(cmds...)
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
	m.syncAllPanels()
	return *m, waitForCheck(m.checkCh)
}

// onCheckDone fires once every backend has reported. For -y this is where we
// finally have the full selection and can apply it.
func (m *Model) onCheckDone() (tea.Model, tea.Cmd) {
	m.checkCh = nil
	if m.autoYes && m.anySelected() {
		// Resolve plans first (onPlansResolved launches the apply); this parks in
		// Applying so the spinner shows while brew's dry-run preview runs.
		m.planning = true
		m.state = stateApplying
		return *m, tea.Batch(resolvePlansCmd(m.ctx, m.selectionByBackend(), m.byName), m.ensureTick())
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
	m.syncTable(m.focusedSource()) // refresh the checkbox cell
}

// currentRow returns the row under the cursor in the focused panel.
func (m Model) currentRow() (row, bool) {
	src := m.focusedSource()
	if src == "" {
		return row{}, false
	}
	rs := m.sourceRows(src)
	i := 0
	if t := m.tables[src]; t != nil {
		i = t.Cursor()
	}
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
	for _, s := range m.panelSources() {
		m.syncTable(s) // refresh every checkbox cell
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
	if st.started.IsZero() {
		st.started = time.Now()
	}
	switch ev.Kind {
	case core.EventPhase:
		st.phase = ev.Phase
		st.fraction = 0
		if ev.Item != "" {
			st.item = ev.Item
			st.markSeen(ev.Item)
		}
	case core.EventProgress:
		st.fraction = ev.Fraction
		if ev.Item != "" {
			st.item = ev.Item
			st.markSeen(ev.Item)
			if st.pkgFrac == nil {
				st.pkgFrac = map[string]float64{}
			}
			if ev.Fraction > st.pkgFrac[ev.Item] {
				st.pkgFrac[ev.Item] = ev.Fraction
			}
		}
	case core.EventItemDone:
		st.done++
		st.fraction = 0
	case core.EventError:
		st.failed = true
		st.errText = ev.Text
		st.appendLog("✗ " + ev.Text)
	case core.EventStatus:
		// Transaction-wide phase label; deliberately touches nothing else so it
		// can't disturb per-package item/progress state.
		st.status = ev.Phase
	case core.EventPrompt:
		st.appendLog("⏸ " + ev.Text)
	case core.EventDone:
		st.finished = true
		st.phase = "Done"
		if st.finishedAt.IsZero() {
			st.finishedAt = time.Now()
		}
	case core.EventLog:
		st.appendLog(ev.Text)
	}
}
