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
	m.clampAllPanels()
	return m
}

// Every backend panel must stay visible no matter where the cursor is in the
// big System list — that's the whole point of the grid.
func TestPanelsAlwaysVisible(t *testing.T) {
	m := gridModel(map[string]int{"system": 220, "brew": 6, "flatpak": 3, "snap": 2})

	// Drive the System cursor to the bottom.
	for range 230 {
		m.moveCursor(1)
	}
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
		m.panelCursor["system"] = cur
		m.clampPanel("system")
		full := "spruce\n\n" + m.viewSelecting() // mirrors View()'s wrapper

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

	full := "spruce\n\n" + m.viewReviewing()
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

// The Applying view lists each selected package with a live status icon, scrolls
// to keep the active item in view, and still fits the terminal.
func TestApplyPanelListsPackages(t *testing.T) {
	m := gridModel(map[string]int{"system": 40, "brew": 3})
	m.state = stateApplying

	// System is mid-apply: 5 done, currently working on #7; brew not started.
	sysPkgs := m.selectionByBackend()["system"]
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
	full := "spruce\n\n" + body
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
// (per backend + grand total), and the live apply footer (downloaded/total +
// rate + ETA), and the apply header carries an elapsed timer.
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

	// Apply: elapsed timer in the header, rate/ETA in the footer.
	m.state = stateApplying
	st := &srcState{done: 3, fraction: 0.5}
	st.started = time.Now().Add(-10 * time.Second)
	m.progress["system"] = st
	app := m.viewApplying()
	if !strings.Contains(app, "ETA") {
		t.Errorf("apply footer should show an ETA when sizes are known:\n%s", app)
	}
	if !strings.Contains(app, "/s") {
		t.Errorf("apply footer should show a download rate:\n%s", app)
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
