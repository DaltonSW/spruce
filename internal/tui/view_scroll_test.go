package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"go.dalton.dog/spruce/internal/core"
)

// gridModel builds a Selecting model with a large System backend plus a few
// small ones, mirroring the real "200+ packages" case.
func gridModel(counts map[string]int) Model {
	m := New(context.TODO(), func() {}, Options{})
	m.state = stateSelecting
	m.width, m.height = 100, 30
	for _, src := range []string{"system", "brew", "flatpak", "snap"} {
		n, ok := counts[src]
		if !ok {
			continue // not discovered in this scenario
		}
		m.discovered = append(m.discovered, src) // detected, even with 0 updates
		for i := range n {
			u := core.Update{
				Name:           fmt.Sprintf("%s-pkg-%03d", src, i),
				CurrentVersion: "1.2.3",
				NewVersion:     "1.2.4",
				Source:         src,
			}
			m.rows = append(m.rows, row{source: src, update: u})
			m.selected[u.ID()] = true
		}
	}
	m.syncAllPanels()
	return m
}

// Every backend panel must stay visible no matter where the cursor is in the
// big System list — that's the whole point of the grid.
func TestPanelsAlwaysVisible(t *testing.T) {
	m := gridModel(map[string]int{"system": 220, "brew": 6, "flatpak": 3, "snap": 2})

	// Drive the System cursor to the bottom.
	m.tableFor("system").GotoBottom()
	m.syncTable("system")
	body := m.viewSelecting()

	for _, want := range []string{"SYSTEM", "BREW", "FLATPAK", "SNAP"} {
		if !strings.Contains(body, want) {
			t.Errorf("panel %q not visible:\n%s", want, body)
		}
	}
}

// The full render must fit inside the terminal: no line wider than width, no
// more lines than height.
func TestSelectingFitsTerminal(t *testing.T) {
	m := gridModel(map[string]int{"system": 220, "brew": 6, "flatpak": 3, "snap": 2})

	for _, cur := range []int{0, 100, 219} {
		m.tableFor("system").SetCursor(cur)
		m.syncTable("system")
		full := headerView(m.width) + "\n\n" + m.viewSelecting() // mirrors View()'s wrapper

		lines := strings.Split(full, "\n")
		if len(lines) > m.height {
			t.Errorf("cur=%d: %d lines exceeds height %d", cur, len(lines), m.height)
		}
		for i, ln := range lines {
			if w := lipgloss.Width(ln); w > m.width {
				t.Errorf("cur=%d line %d width %d exceeds %d: %q", cur, i, w, m.width, ln)
			}
		}
	}
}

// panelLayout sizes each panel to its content; only when the naturals overflow
// does the tallest (the big system list) shrink, leaving the small panels whole.
func TestPanelLayoutContentSized(t *testing.T) {
	// Everything fits: each panel is exactly border(2)+header(1)+content.
	if got := panelLayout([]int{8, 1, 3}, 40); !equalInts(got, []int{11, 4, 6}) {
		t.Errorf("fitting layout = %v, want [11 4 6]", got)
	}

	// Overflow: small panels stay at their natural height, the big one absorbs
	// the shrink, and the heights sum to exactly availH.
	got := panelLayout([]int{200, 1, 4}, 30)
	if sum(got) != 30 {
		t.Errorf("overflow layout %v sums to %d, want 30", got, sum(got))
	}
	if got[1] != 4 || got[2] != 7 {
		t.Errorf("small panels shrank: got %v, want brew=4 flatpak=7", got)
	}
	if got[0] <= got[2] {
		t.Errorf("system panel %d should still be the tallest in %v", got[0], got)
	}

	// Tight fit: a small panel stays whole even when the system list has to shrink
	// *below* it. The system drains all the way to the floor first, so an 8-update
	// backend keeps its full 11 lines while system gives up its rows and scrolls —
	// the old layout equalized the two and clipped the small panel instead.
	tight := panelLayout([]int{100, 8}, 20)
	if sum(tight) != 20 {
		t.Errorf("tight layout %v sums to %d, want 20", tight, sum(tight))
	}
	if tight[1] != 11 {
		t.Errorf("small panel should stay whole at 11, got %v", tight)
	}
	if tight[0] >= tight[1] {
		t.Errorf("system should shrink below the whole small panel, got %v", tight)
	}
}

func sum(xs []int) int {
	t := 0
	for _, x := range xs {
		t += x
	}
	return t
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Panels stack vertically at full width: the render fits the terminal, keeps
// every panel visible, and lets the cursor move continuously from the system
// panel down through the smaller ones (spill across all panels).
func TestStackedLayout(t *testing.T) {
	m := gridModel(map[string]int{"system": 220, "brew": 6, "flatpak": 3, "snap": 2})
	// Resize through Update so the help footer width is set like the real app.
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 50, Height: 40})
	m = updated.(Model)

	full := headerView(m.width) + "\n\n" + m.viewSelecting()
	lines := strings.Split(full, "\n")
	if len(lines) > m.height {
		t.Errorf("%d lines exceeds height %d", len(lines), m.height)
	}
	for i, ln := range lines {
		if w := lipgloss.Width(ln); w > m.width {
			t.Errorf("line %d width %d exceeds %d: %q", i, w, m.width, ln)
		}
	}
	for _, want := range []string{"SYSTEM", "BREW", "FLATPAK", "SNAP"} {
		if !strings.Contains(full, want) {
			t.Errorf("panel %q not visible in stacked layout:\n%s", want, full)
		}
	}

	// Navigation is panel-local: moving down past the end of the system list must
	// NOT leave the system panel — the cursor sticks and Tab is how you switch.
	m.focus = 0
	sys := m.tableFor("system")
	sys.GotoBottom()
	bottom := sys.Cursor()
	sys.MoveDown(1)
	if m.focus != 0 {
		t.Errorf("focus should stay on the system panel; navigation is panel-local")
	}
	if sys.Cursor() != bottom {
		t.Errorf("cursor should stick at the last row, got %d want %d", sys.Cursor(), bottom)
	}

	// Tab advances focus to the next panel.
	m.setFocus(0)
	tm2, _ := m.keySelecting(tea.KeyPressMsg{Code: tea.KeyTab})
	m = tm2.(Model)
	if m.focus != 1 {
		t.Errorf("Tab should move focus to the next panel, got focus=%d", m.focus)
	}
}

// A backend that was detected but has no updates still gets a panel, showing
// the up-to-date note.
func TestEmptyBackendShowsPanel(t *testing.T) {
	m := gridModel(map[string]int{"system": 30, "brew": 4, "snap": 0})

	if got := len(m.panels()); got != 3 {
		t.Fatalf("expected 3 panels (incl. empty snap), got %d", got)
	}
	body := m.viewSelecting()
	if !strings.Contains(body, "SNAP") {
		t.Errorf("empty backend panel missing:\n%s", body)
	}
	if !strings.Contains(body, "Everything up-to-date!") {
		t.Errorf("empty backend should show up-to-date note:\n%s", body)
	}
}

// fakeBackend is a minimal core.Backend; only Name() matters for the view.
type fakeBackend struct{ name string }

func (f fakeBackend) Name() string    { return f.name }
func (f fakeBackend) Available() bool { return true }
func (fakeBackend) Check(context.Context) ([]core.Update, error) {
	return nil, nil
}
func (fakeBackend) Plan(context.Context, []core.Update) (core.Plan, error) {
	return core.Plan{}, nil
}
func (fakeBackend) Apply(context.Context, core.Plan) (<-chan core.ProgressEvent, error) {
	return nil, nil
}

// Streaming discovery: panels appear (as spinners) on availableMsg, then a
// backend's panel fills in only once its checkedMsg arrives.
func TestStreamingDiscovery(t *testing.T) {
	m := New(context.TODO(), func() {}, Options{})
	m.width, m.height = 100, 30

	backends := []core.Backend{fakeBackend{"system"}, fakeBackend{"brew"}}
	tm, _ := m.onAvailable(availableMsg{backends: backends})
	m = tm.(Model)

	if m.state != stateSelecting {
		t.Fatalf("expected stateSelecting after availableMsg, got %v", m.state)
	}
	body := m.viewSelecting()
	if !strings.Contains(body, "SYSTEM") || !strings.Contains(body, "BREW") {
		t.Fatalf("both panels should show immediately:\n%s", body)
	}
	if !strings.Contains(body, "checking for updates") {
		t.Errorf("panels should show a checking spinner while pending:\n%s", body)
	}

	// brew reports two updates; its panel should fill in, system still checking.
	res := checkResult{
		Backend: fakeBackend{"brew"},
		Updates: []core.Update{
			{Name: "ripgrep", CurrentVersion: "14.0", NewVersion: "14.1", Source: "brew"},
			{Name: "fd", CurrentVersion: "9", NewVersion: "10", Source: "brew"},
		},
	}
	tm, _ = m.onChecked(checkedMsg{result: res})
	m = tm.(Model)

	if m.checking["brew"] {
		t.Error("brew should no longer be checking")
	}
	if !m.checking["system"] {
		t.Error("system should still be checking")
	}
	body = m.viewSelecting()
	if !strings.Contains(body, "ripgrep") {
		t.Errorf("brew's updates should have popped in:\n%s", body)
	}
}

// Regression: a small panel must show every one of its rows once its check
// arrives, even though earlier checks (for other backends) synced it while it
// still had zero rows. That empty sync used to leave the table's cursor at -1,
// which clipped the last row until the user navigated into the panel.
func TestStreamingFillShowsAllRows(t *testing.T) {
	m := New(context.TODO(), func() {}, Options{})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = updated.(Model)

	backends := []core.Backend{
		fakeBackend{"system"}, fakeBackend{"brew"}, fakeBackend{"flatpak"}, fakeBackend{"snap"},
	}
	tm, _ := m.onAvailable(availableMsg{backends: backends})
	m = tm.(Model)

	// Checks stream in over time: an empty backend and a couple of others land
	// before brew/flatpak, so their panels are synced while still empty first.
	stream := []struct {
		name string
		n    int
	}{{"snap", 0}, {"system", 12}, {"brew", 6}, {"flatpak", 3}}
	for _, s := range stream {
		ups := make([]core.Update, s.n)
		for i := range ups {
			ups[i] = core.Update{
				Name:           fmt.Sprintf("%s-pkg-%03d", s.name, i),
				CurrentVersion: "1.0.0", NewVersion: "1.0.1", Source: s.name,
			}
		}
		tm, _ = m.onChecked(checkedMsg{result: checkResult{Backend: fakeBackend{s.name}, Updates: ups}})
		m = tm.(Model)
	}

	body := m.viewSelecting()
	for _, p := range []struct {
		name string
		n    int
	}{{"brew", 6}, {"flatpak", 3}} {
		for i := 0; i < p.n; i++ {
			want := fmt.Sprintf("%s-pkg-%03d", p.name, i)
			if !strings.Contains(body, want) {
				t.Errorf("%s row %q missing — last rows clipped after streaming fill:\n%s", p.name, want, body)
			}
		}
	}
}

// Review is a floating modal summarizing counts per backend; backends with
// nothing selected are omitted, and the composited view still fits the terminal.
func TestReviewModal(t *testing.T) {
	m := gridModel(map[string]int{"system": 50, "brew": 4, "snap": 0})
	m.state = stateReviewing
	// Deselect everything in brew so it shouldn't appear in the modal.
	for _, r := range m.sourceRows("brew") {
		m.selected[r.update.ID()] = false
	}

	modal := m.reviewModal()
	if !strings.Contains(modal, "Apply updates?") {
		t.Errorf("modal missing title:\n%s", modal)
	}
	if !strings.Contains(modal, "SYSTEM") {
		t.Errorf("modal missing system count:\n%s", modal)
	}
	if strings.Contains(modal, "BREW") {
		t.Errorf("brew has no selections and should be absent from the modal:\n%s", modal)
	}
	if !strings.Contains(modal, "across") {
		t.Errorf("modal should summarize the total across managers:\n%s", modal)
	}

	full := headerView(m.width) + "\n\n" + m.viewReviewing()
	lines := strings.Split(full, "\n")
	if len(lines) > m.height {
		t.Errorf("composited review is %d lines, exceeds height %d", len(lines), m.height)
	}
	for i, ln := range lines {
		if w := lipgloss.Width(ln); w > m.width {
			t.Errorf("review line %d width %d exceeds %d", i, w, m.width)
		}
	}
}

// Dry run is toggleable from the UI and reflected with a badge.
func TestDryRunToggle(t *testing.T) {
	m := gridModel(map[string]int{"system": 5, "brew": 2})
	if m.dryRun {
		t.Fatal("dry run should default off")
	}
	d := tea.KeyPressMsg{Code: 'd', Text: "d"}

	tm, _ := m.keySelecting(d)
	m = tm.(Model)
	if !m.dryRun {
		t.Fatal("d should enable dry run")
	}
	if !strings.Contains(m.viewSelecting(), "DRY RUN") {
		t.Errorf("badge should appear when dry run is on:\n%s", m.viewSelecting())
	}

	tm, _ = m.keySelecting(d)
	m = tm.(Model)
	if m.dryRun {
		t.Fatal("d should toggle dry run back off")
	}
}

// Space toggles the focused package's selection. In Bubble Tea v2 a space
// keypress stringifies to "space" (not " "), so the binding must register
// "space"; this guards against the binding regressing to a literal space.
func TestSpaceToggle(t *testing.T) {
	m := gridModel(map[string]int{"system": 3})
	r, ok := m.currentRow()
	if !ok {
		t.Fatal("expected a focused row")
	}
	id := r.update.ID()
	if !m.selected[id] {
		t.Fatal("row should start selected in gridModel")
	}

	// A real space keypress from the runtime: Code KeySpace, Text " ",
	// whose String() reports "space".
	space := tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	if got := space.String(); got != "space" {
		t.Fatalf("space keypress stringifies to %q, want %q", got, "space")
	}

	tm, _ := m.keySelecting(space)
	m = tm.(Model)
	if m.selected[id] {
		t.Fatal("space should deselect the focused row")
	}

	tm, _ = m.keySelecting(space)
	m = tm.(Model)
	if !m.selected[id] {
		t.Fatal("space should re-select the focused row")
	}
}

// The Applying view lists each selected package with a live status icon, scrolls
// to keep the active item in view, and still fits the terminal.
func TestApplyPanelListsPackages(t *testing.T) {
	m := gridModel(map[string]int{"system": 40, "brew": 3})
	m.applying = m.selectionByBackend()
	m.state = stateApplying

	// System is mid-apply: 5 done, currently working on #7; brew not started.
	sysPkgs := m.applying["system"]
	m.progress["system"] = &srcState{
		done:     5,
		item:     sysPkgs[7].Name,
		phase:    "Updating",
		fraction: 0.5,
		seen:     map[string]bool{sysPkgs[7].Name: true},
	}

	body := m.viewApplying()

	// The active package's name is visible (the list scrolled to it).
	if !strings.Contains(body, sysPkgs[7].Name) {
		t.Errorf("active package %q not shown:\n%s", sysPkgs[7].Name, body)
	}
	// Status icons for done and pending packages are present.
	if !strings.Contains(body, "✓") {
		t.Errorf("expected a done check mark:\n%s", body)
	}
	if !strings.Contains(body, "○") {
		t.Errorf("expected a pending marker:\n%s", body)
	}

	// Must still fit the terminal.
	full := headerView(m.width) + "\n\n" + body
	for i, ln := range strings.Split(full, "\n") {
		if w := lipgloss.Width(ln); w > m.width {
			t.Errorf("apply line %d width %d exceeds %d: %q", i, w, m.width, ln)
		}
	}
}

// pkgRowStatus classifies packages from whatever a backend reported, including
// the PackageKit case (no per-item Done, only named active item + seen-set).
func TestPkgRowStatus(t *testing.T) {
	// Sequential backend: 2 finished via done count, #2 active by name.
	st := &srcState{done: 2, item: "c", seen: map[string]bool{"a": true, "b": true, "c": true}}
	cases := []struct {
		i    int
		name string
		want pkgStat
	}{
		{0, "a", statDone},
		{1, "b", statDone},
		{2, "c", statActive},
		{3, "d", statPending},
	}
	for _, c := range cases {
		if got := pkgRowStatus(c.i, c.name, st); got != c.want {
			t.Errorf("pkgRowStatus(%d,%q)=%v, want %v", c.i, c.name, got, c.want)
		}
	}

	// PackageKit-style: no done count, but seen-set marks finished ones.
	pk := &srcState{done: 0, item: "c", seen: map[string]bool{"a": true, "b": true, "c": true}}
	if got := pkgRowStatus(0, "a", pk); got != statDone {
		t.Errorf("seen package should be done, got %v", got)
	}
	if got := pkgRowStatus(2, "c", pk); got != statActive {
		t.Errorf("active package should be active, got %v", got)
	}
	if got := pkgRowStatus(3, "d", pk); got != statPending {
		t.Errorf("unseen package should be pending, got %v", got)
	}

	// Failure marks the active item failed but keeps finished ones done.
	f := &srcState{done: 1, item: "b", failed: true, finished: true, seen: map[string]bool{"a": true, "b": true}}
	if got := pkgRowStatus(0, "a", f); got != statDone {
		t.Errorf("pre-failure package should stay done, got %v", got)
	}
	if got := pkgRowStatus(1, "b", f); got != statFailed {
		t.Errorf("active-at-failure package should be failed, got %v", got)
	}
}

// Download sizes flow through to the selecting status line, the review modal
// (per backend + grand total), the per-package apply rows, the active row's live
// downloaded/size note, and the apply footer (rate + ETA); the apply header
// carries an elapsed timer.
func TestSizeAndTimingDisplay(t *testing.T) {
	m := gridModel(map[string]int{"system": 6, "brew": 2})
	for i := range m.rows {
		m.rows[i].update.SizeBytes = 10_000_000 // 10 MB each → 80 MB total
	}
	m.width, m.height = 100, 30

	// Selecting: per-row size and the selection total.
	sel := m.viewSelecting()
	if !strings.Contains(sel, "10 MB") {
		t.Errorf("selecting rows should show per-package size:\n%s", sel)
	}
	if !strings.Contains(sel, "80 MB to download") {
		t.Errorf("selecting status should show the selection total:\n%s", sel)
	}

	// Review modal: total download size.
	m.state = stateReviewing
	if mod := m.reviewModal(); !strings.Contains(mod, "80 MB to download") {
		t.Errorf("review modal should summarize total download size:\n%s", mod)
	}

	// Apply: per-row size, active-row live download note, header timer, footer ETA.
	// Widen so the active row's full note fits (it truncates in narrow panels).
	m.width = 160
	m.applying = m.selectionByBackend()
	m.state = stateApplying
	sysPkgs := m.applying["system"]
	st := &srcState{done: 3, fraction: 0.5, phase: "Downloading", item: sysPkgs[3].Name}
	st.started = time.Now().Add(-10 * time.Second)
	st.markSeen(sysPkgs[3].Name)
	m.progress["system"] = st
	app := m.viewApplying()
	if !strings.Contains(app, "10 MB") {
		t.Errorf("apply rows should show per-package size:\n%s", app)
	}
	if !strings.Contains(app, "5.0 MB/10 MB") { // downloaded(0.5×10MB)/size on the active row
		t.Errorf("active row should show live downloaded/size:\n%s", app)
	}
	if !strings.Contains(app, "ETA") {
		t.Errorf("apply footer should show an ETA when sizes are known:\n%s", app)
	}
	if !strings.Contains(app, "/s") {
		t.Errorf("apply footer should show a download rate:\n%s", app)
	}
	// Nothing may spill past the terminal width — at this wide size and at a
	// narrow two-panel size where the footer cluster and active note must shrink.
	for _, w := range []int{160, 74} {
		m.width = w
		for i, ln := range strings.Split(headerView(m.width)+"\n\n"+m.viewApplying(), "\n") {
			if lw := lipgloss.Width(ln); lw > m.width {
				t.Errorf("w=%d apply line %d width %d exceeds %d: %q", w, i, lw, m.width, ln)
			}
		}
	}
}

func TestFormatBytes(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, ""},
		{-5, ""},
		{512, "512 B"},
		{50_300, "50 kB"},       // humanize.Bytes drops the decimal at values ≥10
		{138_900_000, "139 MB"}, // and rounds
		{1_500_000_000, "1.5 GB"},
	}
	for _, c := range cases {
		if got := formatBytes(c.n); got != c.want {
			t.Errorf("formatBytes(%d)=%q, want %q", c.n, got, c.want)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "0:00"},
		{42 * time.Second, "0:42"},
		{83 * time.Second, "1:23"},
		{3661 * time.Second, "1:01:01"},
	}
	for _, c := range cases {
		if got := formatDuration(c.d); got != c.want {
			t.Errorf("formatDuration(%v)=%q, want %q", c.d, got, c.want)
		}
	}
}

// A single backend should render as one full-width panel without panicking.
func TestSelectingSinglePanel(t *testing.T) {
	m := gridModel(map[string]int{"brew": 5})
	if got := len(m.panels()); got != 1 {
		t.Fatalf("expected 1 panel, got %d", got)
	}
	body := m.viewSelecting()
	if !strings.Contains(body, "BREW") {
		t.Errorf("single panel missing title:\n%s", body)
	}
}
