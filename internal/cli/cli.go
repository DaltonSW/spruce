// Package cli wires spruce's command tree. The root command launches the TUI;
// the check subcommand is a read-only verification path over every backend.
// fang supplies the styled help/error/version chrome around cobra.
package cli

import (
	"context"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/fang"
	"github.com/spf13/cobra"

	"go.dalton.dog/spruce/internal/tui"
)

// version is stamped into the styled `--version` output. Override at build time
// with -ldflags "-X go.dalton.dog/spruce/internal/cli.version=...".
var version = "dev"

// Execute builds the command tree and runs it through fang.
func Execute(ctx context.Context) error {
	return fang.Execute(ctx, newRootCmd(), fang.WithVersion(version))
}

func newRootCmd() *cobra.Command {
	var opts tui.Options
	root := &cobra.Command{
		Use:   "spruce",
		Short: "A pretty TUI front-end over your system's package-upgrade workflows",
		Long: "Spruce up your system. A pretty TUI front-end over the package-upgrade\n" +
			"workflows that already exist on your machine (system packages via\n" +
			"PackageKit, brew, flatpak, snap). It does not reimplement any of them.\n\n" +
			"By default spruce shows the available updates and waits for you to\n" +
			"confirm before applying anything. Pass -y to apply them all immediately.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()

			p := tea.NewProgram(tui.New(ctx, cancel, opts))
			_, err := p.Run()
			return err
		},
	}
	f := root.Flags()
	f.BoolVarP(&opts.AutoYes, "yes", "y", false,
		"apply all available updates without prompting")
	f.BoolVar(&opts.DryRun, "dry-run", false,
		"simulate the upgrade without changing anything")
	f.BoolVar(&opts.Demo, "demo", false,
		"run against fake backends to preview the UI (no system access)")
	return root
}
