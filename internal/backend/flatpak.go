package backend

import (
	"context"
	"os/exec"
	"strings"

	"github.com/dustin/go-humanize"

	"go.dalton.dog/spruce/internal/core"
	"go.dalton.dog/spruce/internal/ptyrun"
)

// Flatpak wraps the flatpak CLI. The list comes from `remote-ls --updates` with
// machine-readable columns; the upgrade is `flatpak update -y` under a PTY.
type Flatpak struct{}

func (Flatpak) Name() string { return "flatpak" }

func (Flatpak) Available() bool {
	_, err := exec.LookPath("flatpak")
	return err == nil
}

func (Flatpak) Check(ctx context.Context) ([]core.Update, error) {
	// Query each remote separately: a single broken remote (missing summary
	// file) makes the global `remote-ls --updates` exit non-zero, but per-remote
	// it only fails that one remote and we keep the rest.
	remotes, err := flatpakRemotes(ctx)
	if err != nil {
		return nil, err
	}

	// remote-ls --updates only reports the candidate (new) version. Pull the
	// installed versions from `flatpak list` and join them on by app id.
	// Best-effort: a failure just leaves CurrentVersion empty.
	installed := flatpakInstalledVersions(ctx)

	seen := map[string]bool{}
	var ups []core.Update
	for _, remote := range remotes {
		cmd := exec.CommandContext(ctx, "flatpak", "remote-ls", remote, "--updates",
			"--columns=application,version,origin,commit,download-size")
		cmd.Env = envBase()
		out, _ := cmd.Output() // tolerate a failing remote
		for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
			if line == "" {
				continue
			}
			f := strings.Split(line, "\t")
			cur := installed[f[0]]
			u := core.Update{Source: "flatpak", Kind: "app", Name: f[0],
				CurrentVersion: cur.version}
			if len(f) > 1 {
				u.NewVersion = f[1]
			}
			if len(f) > 2 {
				u.Repo = f[2]
			}
			var newCommit string
			if len(f) > 3 {
				newCommit = f[3]
			}
			if len(f) > 4 {
				// flatpak prints human sizes, e.g. "138.9 MB"; tolerate unparseable.
				if n, err := humanize.ParseBytes(f[4]); err == nil {
					u.SizeBytes = int64(n)
				}
			}
			// Flatpak's version field often doesn't change between releases —
			// the real difference is the commit. When the version strings would
			// be identical (or are absent), fall back to the commit so the two
			// columns differ. To avoid burning width repeating the same version
			// on both sides, show it once (with the from-commit) on the left and
			// only the to-commit on the right: "1.2 (aaaaaaa) → bbbbbbb".
			if u.NewVersion == u.CurrentVersion {
				from, to := shortCommit(cur.commit), shortCommit(newCommit)
				u.CurrentVersion = joinVersionCommit(u.CurrentVersion, from)
				if to != "" {
					u.NewVersion = to
				}
			}
			key := u.Name + "@" + u.Repo
			if seen[key] {
				continue
			}
			seen[key] = true
			ups = append(ups, u)
		}
	}
	return ups, nil
}

// shortCommit truncates a flatpak commit hash to a git-like short form.
func shortCommit(c string) string {
	if len(c) > 7 {
		return c[:7]
	}
	return c
}

// joinVersionCommit renders a version with its disambiguating commit. With no
// version (common for runtimes) it shows just the commit.
func joinVersionCommit(version, commit string) string {
	if version == "" {
		return commit
	}
	return version + " (" + commit + ")"
}

// flatpakRemotes lists configured remote names.
func flatpakRemotes(ctx context.Context) ([]string, error) {
	cmd := exec.CommandContext(ctx, "flatpak", "remotes", "--columns=name")
	cmd.Env = envBase()
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var names []string
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			names = append(names, line)
		}
	}
	return names, nil
}

// flatpakInstall is the installed version plus its active commit, used to
// disambiguate updates whose version string doesn't change.
type flatpakInstall struct {
	version string
	commit  string
}

// flatpakInstalledVersions returns installed app versions keyed by application
// id. Returns nil on error; lookups against a nil map yield the zero value.
func flatpakInstalledVersions(ctx context.Context) map[string]flatpakInstall {
	// No --app filter: the updates list includes runtimes, so we need their
	// installed versions too.
	cmd := exec.CommandContext(ctx, "flatpak", "list",
		"--columns=application,version,active")
	cmd.Env = envBase()
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	versions := map[string]flatpakInstall{}
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < 2 {
			continue
		}
		inst := flatpakInstall{version: f[1]}
		if len(f) > 2 {
			inst.commit = f[2]
		}
		versions[f[0]] = inst
	}
	return versions
}

func (f Flatpak) Plan(ctx context.Context, selected []core.Update) (core.Plan, error) {
	return core.Plan{Backend: f.Name(), Selected: selected, NeedsRoot: false}, nil
}

func (f Flatpak) Apply(ctx context.Context, plan core.Plan) (<-chan core.ProgressEvent, error) {
	events := make(chan core.ProgressEvent, 64)

	argv := []string{"flatpak", "update", "-y", "--noninteractive"}
	if plan.DryRun {
		// --no-deploy fetches the update but never deploys it: safe & repeatable.
		argv = append(argv, "--no-deploy")
	}
	for _, u := range plan.Selected {
		argv = append(argv, u.Name)
	}

	go func() {
		defer close(events)
		if plan.DryRun {
			events <- core.ProgressEvent{Kind: core.EventLog, Source: "flatpak",
				Text: "(dry run — fetching only, not deploying)"}
		}
		chunks, done := ptyrun.Stream(ctx, argv, ptyrun.Options{Env: envBase(), IdleTimeoutMS: 5000})

		var carry string
		emit := func(line string) {
			line = strings.TrimRight(line, "\r")
			if line == "" {
				return
			}
			events <- core.ProgressEvent{Kind: core.EventLog, Source: "flatpak", Text: line}
			switch {
			case strings.HasPrefix(line, "Updating") || strings.HasPrefix(line, "Installing"):
				events <- core.ProgressEvent{Kind: core.EventPhase, Source: "flatpak", Phase: "Updating", Item: firstField(strings.TrimSpace(line[strings.IndexByte(line, ' ')+1:]))}
			case strings.Contains(line, "Changes complete") || strings.HasPrefix(line, "Updates complete"):
				events <- core.ProgressEvent{Kind: core.EventItemDone, Source: "flatpak", OK: true}
			}
		}

		for ch := range chunks {
			if ch.Idle {
				events <- core.ProgressEvent{Kind: core.EventPrompt, Source: "flatpak",
					Text: "flatpak appears to be waiting for input"}
				continue
			}
			carry += ch.Data
			for {
				i := strings.IndexByte(carry, '\n')
				if i < 0 {
					break
				}
				emit(carry[:i])
				carry = carry[i+1:]
			}
		}
		emit(carry)

		if err := <-done; err != nil {
			events <- core.ProgressEvent{Kind: core.EventError, Source: "flatpak", Text: err.Error()}
		}
		events <- core.ProgressEvent{Kind: core.EventDone, Source: "flatpak", OK: true}
	}()

	return events, nil
}
