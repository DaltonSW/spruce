package tui

import (
	"context"
	"testing"

	"go.dalton.dog/spruce/internal/config"
	"go.dalton.dog/spruce/internal/core"
)

// A backend ignored in config should never get a panel or a Check, but should
// still show up in allNames for the manage-backends screen.
func TestOnAvailableSkipsIgnoredBackend(t *testing.T) {
	m := New(context.TODO(), func() {}, Options{
		Config: config.Config{IgnoredBackends: map[string]bool{"npm": true}},
	})
	m.width, m.height = 100, 30

	backends := []core.Backend{fakeBackend{"brew"}, fakeBackend{"npm"}}
	tm, _ := m.onAvailable(availableMsg{backends: backends})
	m = tm.(Model)

	if len(m.discovered) != 1 || m.discovered[0] != "brew" {
		t.Errorf("discovered = %v, want just [brew]", m.discovered)
	}
	if m.checking["npm"] {
		t.Error("ignored backend should not be marked checking")
	}
	if len(m.allNames) != 2 {
		t.Errorf("allNames = %v, want both backends listed", m.allNames)
	}
}

// Ignoring a visible backend drops its panel and rows immediately and
// persists the choice; un-ignoring restores the panel and re-checks it.
func TestToggleIgnoredRoundTrip(t *testing.T) {
	m := New(context.TODO(), func() {}, Options{})
	m.width, m.height = 100, 30

	backends := []core.Backend{fakeBackend{"brew"}, fakeBackend{"npm"}}
	tm, _ := m.onAvailable(availableMsg{backends: backends})
	m = tm.(Model)
	m.rows = append(m.rows, row{source: "npm", update: core.Update{Name: "left-pad", Source: "npm"}})
	m.selected["npm/left-pad"] = true

	m.toggleIgnored("npm")

	if !m.cfg.IsIgnored("npm") {
		t.Error("npm should be marked ignored in config")
	}
	for _, s := range m.discovered {
		if s == "npm" {
			t.Error("npm should be removed from discovered once ignored")
		}
	}
	if len(m.rows) != 0 {
		t.Errorf("npm's rows should be dropped once ignored, got %v", m.rows)
	}
	if m.selected["npm/left-pad"] {
		t.Error("npm's selection should be cleared once ignored")
	}

	m.toggleIgnored("npm")

	if m.cfg.IsIgnored("npm") {
		t.Error("npm should no longer be ignored after toggling back")
	}
	found := false
	for _, s := range m.discovered {
		if s == "npm" {
			found = true
		}
	}
	if !found {
		t.Error("npm should be back in discovered after un-ignoring")
	}
	if !m.checking["npm"] {
		t.Error("un-ignoring should re-trigger a Check for npm")
	}
}
