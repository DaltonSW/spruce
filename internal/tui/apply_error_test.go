package tui

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"go.dalton.dog/spruce/internal/core"
)

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// normalize strips ANSI styling and all whitespace/box-drawing so wrapped text
// can be matched regardless of where the line breaks fell.
func normalize(s string) string {
	s = ansiRE.ReplaceAllString(s, "")
	var b strings.Builder
	for _, r := range s {
		switch r {
		case ' ', '\n', '\t', '│', '╭', '╮', '╰', '╯', '─':
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// A failed single-package apply must render the backend's whole error, wrapped
// across the panel, not truncate it to one clipped line — the panel grows to
// fit so the reason is legible.
func TestFailedApplyShowsFullError(t *testing.T) {
	m := New(context.TODO(), func() {}, Options{})
	m.state = stateApplying
	m.width, m.height = 80, 30

	u := core.Update{Name: "amd-gpu-firmware", CurrentVersion: "1", NewVersion: "2", Source: "system"}
	m.discovered = append(m.discovered, "system")
	m.rows = append(m.rows, row{source: "system", update: u})
	m.applying = map[string][]core.Update{"system": {u}}
	m.syncAllPanels()

	longErr := "could not depsolve transaction: nothing provides libfoo.so.6 " +
		"needed by amd-gpu-firmware from updates, and the package id was rejected outright"
	m.applyEvent(core.ProgressEvent{Kind: core.EventError, Source: "system", Text: longErr})

	body := m.viewApplying()

	if !strings.Contains(normalize(body), normalize(longErr)) {
		t.Fatalf("full error not present in panel (was truncated):\n%s", body)
	}
	// And it must still fit the terminal width.
	for _, line := range strings.Split(body, "\n") {
		if w := lipgloss.Width(line); w > m.width {
			t.Fatalf("line exceeds width %d (%d): %q", m.width, w, line)
		}
	}
}

// A single-package apply panel is only one content row tall, so the transaction
// status (dnf5's silent depsolve phase) must surface on the package row itself —
// not just the bottom line, which there's no room for.
func TestPrepStatusShowsOnRow(t *testing.T) {
	m := New(context.TODO(), func() {}, Options{})
	m.state = stateApplying
	m.width, m.height = 80, 24

	u := core.Update{Name: "amd-gpu-firmware", CurrentVersion: "1", NewVersion: "2", Source: "system"}
	m.discovered = append(m.discovered, "system")
	m.rows = append(m.rows, row{source: "system", update: u})
	m.applying = map[string][]core.Update{"system": {u}}
	m.syncAllPanels()

	// Transaction is resolving deps; no package has gone active yet (item == "").
	m.applyEvent(core.ProgressEvent{Kind: core.EventStatus, Source: "system", Phase: "resolving dependencies…"})

	body := m.viewApplying()
	if !strings.Contains(normalize(body), normalize("resolving dependencies…")) {
		t.Fatalf("prep status not shown on the package row:\n%s", body)
	}
}
