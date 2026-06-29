# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`spruce` is a TUI front-end over the package-upgrade workflows already present on
the system. It **never reimplements a package manager** — each backend drives the
real tool (PackageKit/D-Bus, `brew`, `flatpak`, snapd REST) and streams structured
progress back to the UI. Built with Go + the Charm **v2** stack
(`charm.land/bubbletea/v2`, `charm.land/lipgloss/v2`), with fang/cobra for the
command tree.

## Commands

```sh
go run .                # launch the TUI: show updates, confirm to apply
go run . -y             # apply all available updates without prompting
go run . --dry-run      # simulate; never mutates the system
go run . --demo         # fake backends to preview the UI (no system access)
go build .              # build the binary
go vet ./...            # vet
gofmt -l internal/      # list unformatted files
go test ./...           # all tests
go test ./internal/tui -run TestScroll   # a single test by name
```

`--demo` is the fastest way to exercise UI changes without a real system; it drives
the backends from `internal/backend/demo.go`, which cover every panel state (big
scrolling list, small lists, an up-to-date backend, and an apply that fails partway).

## Architecture

The whole design hinges on one boundary: **the TUI depends only on
`internal/core` (the `Backend` interface + data types) and the backend registry —
never on any specific package manager.** Every per-tool difference stays isolated
inside a backend implementation.

- `internal/core` — the contract. `Backend` interface and the three data types
  that flow across it: `Update` (one normalized upgradable item), `Plan` (what
  Apply would do, fuel for the review screen), and `ProgressEvent` (the single
  currency `Apply` streams back). Read this first.
- `internal/backend` — one file per manager (`packagekit.go`, `brew.go`,
  `flatpak.go`, `snap.go`) plus `registry.go`. New backends are registered in
  `all()` in `registry.go` **and nowhere else**. `Available()` filters to what
  exists on the machine; `CheckAll()` runs every backend's `Check` concurrently.
- `internal/ptyrun` — PTY streaming helper for CLI-wrapped backends (brew, flatpak)
  so their progress output can be parsed into `ProgressEvent`s.
- `internal/tui` — Bubble Tea v2 model/view. State machine:
  `Discovering → Selecting → Reviewing → Applying → Done` (`state` enum in
  `model.go`). Each backend renders as its own always-visible panel with
  panel-local navigation, so a 200-package system list can't bury smaller backends.
- `internal/cli` — fang/cobra command tree; root launches the TUI. `version` is
  stamped via `-ldflags "-X go.dalton.dog/spruce/internal/cli.version=..."`.

### The flow (and the one safety gate)

Check all backends → select in the TUI → resolve a `Plan` → **review/confirm (the
single gate before anything mutates)** → run each backend non-interactively,
streaming `ProgressEvent`s. `Check` and `Plan` are strictly read-only; only `Apply`
mutates. Backends needing root satisfy it via polkit/snapd, never raw sudo.

### The `Backend` contract that matters

- `Apply` runs work in its own goroutine, returns a channel it closes when done,
  and must respect `ctx` cancellation.
- A backend that hits an interactive prompt emits `EventPrompt` rather than
  blocking silently.
- `ProgressEvent.Kind` selects which fields are meaningful; the UI routes events to
  the right panel via `Source`.

## Critical gotchas

- **Dry-run must never call a mutating method.** The dnf5 PackageKit backend has
  been observed to **ignore the SIMULATE transaction flag and apply for real**, so
  dry runs in `packagekit.go` skip `UpdatePackages` entirely rather than relying on
  SIMULATE. Do not "simplify" dry-run to just pass a simulate flag. (See the
  `pkFlagNone` comment in `packagekit.go`.)
- PackageKit runs one transaction and never emits a per-package `EventItemDone`; the
  TUI infers completion via `srcState.markSeen` (a later active package implies the
  previous one finished). Keep this in mind when touching apply-progress logic.
- Charm imports are the **v2** module paths (`charm.land/...`), not the older
  `github.com/charmbracelet/bubbletea` paths.

## Out of scope

AppImage is intentionally unsupported — no central registry to query.
