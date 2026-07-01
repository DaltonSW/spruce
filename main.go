// Command spruce is a pretty TUI front-end over the package upgrade
// workflows that already exist on the system (system packages via PackageKit,
// brew, flatpak, snap). It does not reimplement any of them — each backend
// drives the real tool and streams progress back to the UI.
//
// Command handling (help, version, completions) is wired in cmd; main just
// hands off to it.
package main

import (
	"os"

	"go.dalton.dog/spruce/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
