package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"go.dalton.dog/spruce/internal/core"
)

// gridModel builds a Selecting model with a large System backend plus a few
// small ones, mirroring the real "200+ packages" case.
func gridModel(counts map[string]int) Model {
	m := New(context.TODO(), func() {}, false)
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

func (f fakeBackend) Name() string      { return f.name }
func (f fakeBackend) Available() bool   { return true }
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
	m := New(context.TODO(), func() {}, false)
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
