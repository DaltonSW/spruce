// Package cmd wires spruce's command tree. The root command launches the TUI;
// fang supplies the styled help/error/version chrome around cobra.
package cmd

import (
	"context"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/fang"
	"github.com/spf13/cobra"

	"go.dalton.dog/spruce/internal/tui"
	"go.dalton.dog/spruce/internal/version"
)

// Version is stamped into the styled `--version` output. Override at build time
// with -ldflags "-X go.dalton.dog/spruce/cmd.Version=...". When left at the
// default "dev", Execute resolves a richer label from the git working tree
// (tag-commit-dev) so local builds show provenance instead of a bare "dev".
var Version = "dev"

// Execute builds the command tree and runs it through fang. Returns any error
// bubbled up from the program so main can set the exit code.
func Execute() error {
	if Version == "dev" {
		Version = version.ResolveDev()
	}
	return fang.Execute(context.Background(), newRootCmd(),
		fang.WithoutCompletions(), fang.WithVersion(Version))
}

// newRootCmd assembles the root command, wires its flags, and hands control to
// the TUI on run.
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

			opts.Version = Version
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
