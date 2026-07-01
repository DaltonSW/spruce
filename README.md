## Welcome to `spruce`!

Spruce up your system! 🌲 Keeping everything up to date shouldn't mean juggling five different package managers and squinting at five different progress spinners.

`spruce` is a pretty TUI front-end over the package-upgrade workflows that already exist on your system. It does **not** reimplement any package manager — each backend drives the real tool (PackageKit/D-Bus, `brew`, `flatpak`, snapd) and streams structured progress back to the UI. One screen, every update.

![spruce demo](./assets/demo.gif)

## Installation

### Homebrew 🍺

- Have `brew` installed ([brew.sh](https://brew.sh))
- Run the following:
```sh
brew install --cask daltonsw/tap/spruce
```

### Go 🖥️

- Have `Go`
- Have your `Go` install location on your `$PATH`
- Run the following:
```sh
go install go.dalton.dog/spruce@latest
```

### Github Releases 🐙

- Go to the `Releases` tab of the repo [here](https://github.com/DaltonSW/spruce/releases)
- Download the latest archive for your OS/architecture
- Extract it and place the resulting binary on your `$PATH` and ensure it is executable

```sh
cd ~/Downloads # Assuming you downloaded it here
tar -xvf spruce_[whatever].tar.gz # x: Extract; v: Verbose output; f: Specify filename
chmod +x spruce # Make file executable
mv spruce [somewhere on your $PATH] # Move the file to somewhere on your path for easy execution
```

## Usage

Just run `spruce` in your terminal. It checks every available backend, shows you what's upgradable, and waits for you to confirm before touching anything.

```sh
spruce            # show available updates, then confirm to apply
spruce -y         # apply all available updates without prompting
spruce --dry-run  # simulate; never mutates the system
spruce --demo     # fake backends to preview the UI (no system access)
spruce --help     # styled help; --version, completion also available
```

Nothing mutates your system until you pass the single confirmation gate on the review screen. `Check` and `Plan` are strictly read-only; only `Apply` makes changes, and backends needing root satisfy it via polkit/snapd — never raw `sudo`.

## Backends

Each is discovered at runtime; only the ones present on the machine appear.

| Source | Integration |
|---|---|
| **system** | PackageKit* over D-Bus — structured signals, polkit auth, no sudo parsing |
| **brew** | `brew outdated --json=v2` for the list; `brew upgrade` under a PTY for progress |
| **flatpak** | per-remote `flatpak remote-ls --updates`; `flatpak update -y` to apply |
| **snap** | snapd REST API over `/run/snapd.socket`; polls the change for progress |

AppImage is intentionally out of scope (no central registry to query).

*PackageKit is an abstraction over system-level package managers, so (theoretically) this will work with apt, dnf, pacman, zypper, or anything else listed as a supported back-end [here](https://en.wikipedia.org/wiki/PackageKit)

### Next Up

- Go
- pipx
- gem
- npm -g
- ...?

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

Built with Go + the Charm **v2** stack (`charm.land/bubbletea/v2`, `charm.land/lipgloss/v2`).

## Status

Read-only `Check` paths are verified live for system (PackageKit/D-Bus), brew,
and flatpak. The `Apply` paths are implemented but should be exercised against a
real upgrade (they mutate the system / trigger polkit) before relying on them.
