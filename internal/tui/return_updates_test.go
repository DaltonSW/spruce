package tui

import (
	"testing"

	"go.dalton.dog/spruce/internal/core"
)

// returnToUpdates is the fast return from the Done screen: it reuses the cached
// rows, pruning only what a backend applied cleanly. A backend that failed keeps
// its rows so the user can retry, and other backends' selections survive.
func TestReturnToUpdatesPrunesAppliedKeepsFailed(t *testing.T) {
	m := gridModel(map[string]int{"brew": 2, "flatpak": 1})

	// Identify the seeded rows so we can target them by ID.
	var brewApplied, brewKept, flatpak core.Update
	for _, r := range m.rows {
		switch {
		case r.source == "brew" && brewApplied.Name == "":
			brewApplied = r.update
		case r.source == "brew":
			brewKept = r.update
		case r.source == "flatpak":
			flatpak = r.update
		}
	}

	// brew finished cleanly (its one applied package should drop); flatpak failed
	// (its package must survive so it can be retried).
	m.applying = map[string][]core.Update{
		"brew":    {brewApplied},
		"flatpak": {flatpak},
	}
	m.progress = map[string]*srcState{
		"brew":    {finished: true},
		"flatpak": {finished: true, failed: true},
	}
	m.state = stateDone

	tm, cmd := m.returnToUpdates()
	m = tm.(Model)

	if cmd != nil {
		t.Error("returnToUpdates should not issue a command (no re-check)")
	}
	if m.state != stateSelecting {
		t.Errorf("state = %v, want stateSelecting", m.state)
	}
	if inRows(m.rows, brewApplied) {
		t.Errorf("applied brew package %q should have been pruned", brewApplied.Name)
	}
	if m.selected[brewApplied.ID()] {
		t.Errorf("applied brew package should be dropped from selection")
	}
	if !inRows(m.rows, brewKept) {
		t.Errorf("un-applied brew package %q should remain", brewKept.Name)
	}
	if !inRows(m.rows, flatpak) {
		t.Errorf("failed flatpak package %q should be kept for retry", flatpak.Name)
	}

	// Per-run state is reset.
	if m.applying != nil || len(m.progress) != 0 || m.installTarget != nil {
		t.Errorf("per-apply-run state not reset: applying=%v progress=%v target=%v",
			m.applying, m.progress, m.installTarget)
	}
}

func inRows(rows []row, u core.Update) bool {
	for _, r := range rows {
		if r.update.ID() == u.ID() {
			return true
		}
	}
	return false
}
