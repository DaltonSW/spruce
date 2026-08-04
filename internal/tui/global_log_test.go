package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"go.dalton.dog/spruce/internal/core"
)

// applyEvent should feed the cross-backend log only from the "signal" kinds
// (errors, prompts, raw tool output) — the per-package progress chatter would
// just be noise in a log meant for diagnosing failures.
func TestApplyEventGlobalLogFiltering(t *testing.T) {
	m := New(context.TODO(), func() {}, Options{})

	m.applyEvent(core.ProgressEvent{Kind: core.EventPhase, Source: "npm", Phase: "Installing", Item: "typescript"})
	m.applyEvent(core.ProgressEvent{Kind: core.EventProgress, Source: "npm", Fraction: 0.5, Item: "typescript"})
	m.applyEvent(core.ProgressEvent{Kind: core.EventStatus, Source: "npm", Phase: "resolving…"})
	m.applyEvent(core.ProgressEvent{Kind: core.EventItemDone, Source: "npm"})
	m.applyEvent(core.ProgressEvent{Kind: core.EventLog, Source: "npm", Text: "npm error code E423"})
	m.applyEvent(core.ProgressEvent{Kind: core.EventPrompt, Source: "npm", Text: "overwrite existing file?"})
	m.applyEvent(core.ProgressEvent{Kind: core.EventError, Source: "npm", Text: "exit status 1"})
	m.applyEvent(core.ProgressEvent{Kind: core.EventDone, Source: "npm"})

	if got, want := len(m.globalLog), 3; got != want {
		t.Fatalf("globalLog has %d entries, want %d (Phase/Progress/Status/ItemDone/Done should be skipped): %+v", got, want, m.globalLog)
	}
	if m.globalLog[0].text != "npm error code E423" {
		t.Errorf("EventLog entry = %q, want raw text unprefixed", m.globalLog[0].text)
	}
	if m.globalLog[1].text != "⏸ overwrite existing file?" {
		t.Errorf("EventPrompt entry = %q, want ⏸-prefixed", m.globalLog[1].text)
	}
	if m.globalLog[2].text != "✗ exit status 1" {
		t.Errorf("EventError entry = %q, want ✗-prefixed", m.globalLog[2].text)
	}
	for _, e := range m.globalLog {
		if e.source != "npm" {
			t.Errorf("entry source = %q, want npm", e.source)
		}
	}
	if m.globalLog[0].isError || m.globalLog[1].isError {
		t.Errorf("EventLog/EventPrompt entries should not be tagged isError: %+v", m.globalLog[:2])
	}
	if !m.globalLog[2].isError {
		t.Errorf("EventError entry should be tagged isError")
	}
}

// keys.LogFilter toggles the log between every message and just the ones
// tagged isError (EventError); logScroll resets so switching views can't land
// on an out-of-range offset counted against the other list's length.
func TestLogFilterTogglesFailuresOnly(t *testing.T) {
	m := New(context.TODO(), func() {}, Options{})
	m.state = stateApplying
	m.applyEvent(core.ProgressEvent{Kind: core.EventLog, Source: "npm", Text: "resolving dependencies"})
	m.applyEvent(core.ProgressEvent{Kind: core.EventError, Source: "npm", Item: "typescript", Text: "exit status 1"})
	m.applyEvent(core.ProgressEvent{Kind: core.EventLog, Source: "brew", Text: "downloading"})

	if got, want := len(m.visibleLog()), 3; got != want {
		t.Fatalf("visibleLog() unfiltered = %d entries, want %d", got, want)
	}

	filter := tea.KeyPressMsg{Code: 'f', Text: "f"}
	tm, _ := m.keyLog(filter)
	m = tm.(Model)
	if !m.logFailuresOnly {
		t.Fatalf("LogFilter should toggle logFailuresOnly on")
	}
	visible := m.visibleLog()
	if len(visible) != 1 || !visible[0].isError {
		t.Fatalf("visibleLog() filtered = %+v, want just the one isError entry", visible)
	}

	tm, _ = m.keyLog(filter)
	m = tm.(Model)
	if m.logFailuresOnly {
		t.Fatalf("second LogFilter press should toggle logFailuresOnly back off")
	}
	if len(m.visibleLog()) != 3 {
		t.Fatalf("visibleLog() after toggling off = %d entries, want 3", len(m.visibleLog()))
	}
}

// The global log aggregates every backend, so it's capped higher than a
// single backend's own tail, but it must still be bounded.
func TestGlobalLogBounded(t *testing.T) {
	m := New(context.TODO(), func() {}, Options{})
	for i := 0; i < 1005; i++ {
		m.appendGlobalLog("npm", "line", false)
	}
	if len(m.globalLog) != 1000 {
		t.Fatalf("globalLog len = %d, want 1000 (bounded)", len(m.globalLog))
	}
}

// Scrolling up and then receiving new events shouldn't yank the view back to
// the tail — logScroll should grow to keep the same lines on screen, the same
// way `less +F` behaves once you scroll away from the end.
func TestGlobalLogScrollPreservesPositionOnAppend(t *testing.T) {
	m := New(context.TODO(), func() {}, Options{})
	for i := 0; i < 20; i++ {
		m.appendGlobalLog("npm", "line", false)
	}
	m.logScroll = 5
	m.appendGlobalLog("npm", "new line", false)
	if m.logScroll != 6 {
		t.Fatalf("logScroll after append while scrolled = %d, want 6 (grew to hold position)", m.logScroll)
	}

	m.logScroll = 0
	m.appendGlobalLog("npm", "another line", false)
	if m.logScroll != 0 {
		t.Fatalf("logScroll after append while pinned to tail = %d, want 0 (stays following)", m.logScroll)
	}
}

// keyLog must clamp logScroll within [0, logScrollMax()] regardless of how
// many times a scroll key fires.
func TestLogScrollClamping(t *testing.T) {
	m := New(context.TODO(), func() {}, Options{})
	m.state = stateApplying
	for i := 0; i < 20; i++ {
		m.appendGlobalLog("npm", "line", false)
	}

	up := tea.KeyPressMsg{Code: 'k', Text: "k"}
	down := tea.KeyPressMsg{Code: 'j', Text: "j"}
	home := tea.KeyPressMsg{Code: 'g', Text: "g"}
	end := tea.KeyPressMsg{Code: 'G', Text: "G"}

	tm, _ := m.keyLog(up)
	m = tm.(Model)
	if m.logScroll != 1 {
		t.Fatalf("logScroll after one Up = %d, want 1", m.logScroll)
	}

	// Home should jump straight to the max (can't scroll past the oldest line).
	tm, _ = m.keyLog(home)
	m = tm.(Model)
	want := m.logScrollMax()
	if m.logScroll != want {
		t.Fatalf("logScroll after Home = %d, want %d", m.logScroll, want)
	}
	// One more Up must not exceed that max.
	tm, _ = m.keyLog(up)
	m = tm.(Model)
	if m.logScroll != want {
		t.Fatalf("logScroll after Up past max = %d, want clamped at %d", m.logScroll, want)
	}

	// End jumps back to 0, and Down from there must not go negative.
	tm, _ = m.keyLog(end)
	m = tm.(Model)
	if m.logScroll != 0 {
		t.Fatalf("logScroll after End = %d, want 0", m.logScroll)
	}
	tm, _ = m.keyLog(down)
	m = tm.(Model)
	if m.logScroll != 0 {
		t.Fatalf("logScroll after Down past 0 = %d, want clamped at 0", m.logScroll)
	}
}

// This is the concrete regression case for the motivating bug: a backend's
// EventError text alone (e.g. npm's "exit status 1") is too terse to diagnose
// anything, but the raw EventLog lines streamed just before it — npm's actual
// registry error — are what the activity log panel exists to surface.
func TestActivityLogShowsRawOutputBeforeError(t *testing.T) {
	m := New(context.TODO(), func() {}, Options{})
	m.state = stateApplying
	m.width, m.height = 100, 30

	u := core.Update{Name: "typescript", CurrentVersion: "5.0.0", NewVersion: "5.4.0", Source: "npm"}
	m.discovered = append(m.discovered, "npm")
	m.rows = append(m.rows, row{source: "npm", update: u})
	m.applying = map[string][]core.Update{"npm": {u}}
	m.syncAllPanels()

	m.applyEvent(core.ProgressEvent{Kind: core.EventLog, Source: "npm", Text: "npm error code E423"})
	m.applyEvent(core.ProgressEvent{Kind: core.EventLog, Source: "npm", Text: "npm error 423 Locked"})
	m.applyEvent(core.ProgressEvent{Kind: core.EventError, Source: "npm", Text: "exit status 1"})

	body := m.viewApplying()
	for _, want := range []string{"npm error code E423", "npm error 423 Locked", "exit status 1"} {
		if !strings.Contains(normalize(body), normalize(want)) {
			t.Fatalf("activity log missing %q:\n%s", want, body)
		}
	}
}
