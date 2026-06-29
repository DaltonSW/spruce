// Package core defines the backend-agnostic data model and interface.
//
// The whole point: the TUI only ever talks to these types and the Backend
// interface. Every messy per-tool difference (system packages via
// PackageKit/D-Bus, brew via JSON+PTY, flatpak via CLI, snap via the snapd
// REST socket) stays isolated inside a backend implementation.
package core

import "context"

// Update is one upgradable item, normalized across package managers.
type Update struct {
	Name           string
	CurrentVersion string
	NewVersion     string

	Source    string // which backend produced it, e.g. "brew", "flatpak"
	Repo      string // backend-specific origin (tap, remote, repo), optional
	Ref       string // opaque backend handle (e.g. PackageKit package_id); empty if N/A
	SizeBytes int64  // download size if known, else 0
	Pinned    bool
	Kind      string // "package", "cask", "formula", "app", "snap", ...
}

// ID is a stable identifier for selection bookkeeping in the UI.
func (u Update) ID() string { return u.Source + "/" + u.Name }

// Plan is what Apply would do — fuel for the review/confirm screen.
type Plan struct {
	Backend       string
	Selected      []Update
	DownloadBytes int64
	NeedsRoot     bool // satisfied via polkit/snapd, not raw sudo
	DryRun        bool // simulate the upgrade without mutating the system
	// Anything the user should see before committing: extra deps pulled in,
	// removals, warnings, etc.
	Notes []string
}

// EventKind classifies a ProgressEvent so the UI knows which fields matter.
type EventKind int

const (
	EventPhase    EventKind = iota // high-level stage ("Downloading", "Installing")
	EventProgress                  // numeric progress for the current item/txn
	EventLog                       // a raw line from the tool, for the detail pane
	EventPrompt                    // tool is blocked asking a question
	EventItemDone                  // one package finished
	EventError                     // something went wrong
	EventDone                      // the whole transaction finished
)

// ProgressEvent is the single currency Apply streams back to the UI.
// Most fields are optional; the UI reads whatever is relevant per Kind.
type ProgressEvent struct {
	Kind     EventKind
	Source   string  // backend name, so the UI can route to the right row
	Item     string  // package/app the event concerns
	Phase    string  // for EventPhase
	Fraction float64 // 0.0–1.0 for EventProgress
	Text     string  // log line / prompt question / error message
	OK       bool    // for EventDone / EventItemDone
}

// Backend is one package manager. Implementations live in internal/backend.
type Backend interface {
	// Name is a stable identifier, e.g. "system", "brew", "flatpak", "snap".
	Name() string

	// Available reports whether this manager exists on the current system
	// (binary on PATH, daemon socket present, D-Bus name owned, ...).
	Available() bool

	// Check refreshes metadata and returns what can be upgraded. Read-only.
	Check(ctx context.Context) ([]Update, error)

	// Plan resolves a selection into a concrete preview. Read-only.
	Plan(ctx context.Context, selected []Update) (Plan, error)

	// Apply executes the upgrade, streaming events on the returned channel
	// until it closes. It runs the work in its own goroutine and must respect
	// ctx cancellation. Backends that hit an interactive prompt should emit an
	// EventPrompt rather than blocking silently.
	Apply(ctx context.Context, plan Plan) (<-chan ProgressEvent, error)
}
