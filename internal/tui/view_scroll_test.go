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
		for i := range counts[src] {
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
