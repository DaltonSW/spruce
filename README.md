# spruce

Spruce up your system. A pretty TUI front-end over the package-upgrade
workflows that already exist on
your system. It does **not** reimplement any package manager — each backend
drives the real tool and streams structured progress back to the UI.

Built with Go + the Charm **v2** stack (`charm.land/bubbletea/v2`, `charm.land/lipgloss/v2`).

## Backends

Each is discovered at runtime; only the ones present on the machine appear.

| Source | Integration |
|---|---|
| **system** (dnf/apt/zypper) | PackageKit over D-Bus — structured signals, polkit auth, no sudo parsing |
| **brew** | `brew outdated --json=v2` for the list; `brew upgrade` under a PTY for progress |
| **flatpak** | per-remote `flatpak remote-ls --updates`; `flatpak update -y` to apply |
| **snap** | snapd REST API over `/run/snapd.socket`; polls the change for progress |

AppImage is intentionally out of scope (no central registry to query).

## Architecture

```
main.go                     entrypoint
internal/
  cli/         single fang/cobra command: root → TUI; -y applies immediately
  core/        the only thing the TUI depends on: Update, Plan, ProgressEvent, Backend
  backend/     one file per manager + registry (runtime discovery, concurrent CheckAll)
  ptyrun/      PTY streaming helper for CLI-wrapped backends (brew, flatpak)
  tui/         Bubble Tea v2 model/view: Discovering → Selecting → Reviewing → Applying → Done
```

Command handling uses [`charmbracelet/fang`](https://github.com/charmbracelet/fang)
over cobra, which supplies the styled help/error/version output and shell
completions for free.

The flow: check all backends → select in the TUI → resolve a `Plan` → review/confirm
(the single gate before anything mutates) → run each backend non-interactively,
streaming `ProgressEvent`s into the UI.

## Run

```sh
go run .              # show available updates, then confirm to apply
go run . -y           # apply all available updates without prompting
go run . --help       # styled help; --version, completion also available
```

## Status

Read-only `Check` paths are verified live for system (PackageKit/D-Bus), brew,
and flatpak. The `Apply` paths are implemented but should be exercised against a
real upgrade (they mutate the system / trigger polkit) before relying on them.
