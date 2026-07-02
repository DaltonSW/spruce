package backend

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/mod/semver"

	"go.dalton.dog/spruce/internal/core"
	"go.dalton.dog/spruce/internal/ptyrun"
)

// Go wraps the `go` toolchain to manage binaries installed via `go install
// <pkg>@<version>`. There is no native "outdated" for these, so we reconstruct
// it: enumerate the install dir, read each binary's embedded module info with
// `go version -m`, resolve the latest version via `go list -m ...@latest`, and
// diff the two with semver. Upgrades are `go install <pkg>@latest` under a PTY.
type Go struct{}

func (Go) Name() string  { return "go" }
func (Go) Icon() string  { return "" }       // nf-seti-go
func (Go) Color() string { return "#00add8" } // gopher cyan — the Go brand

func (Go) Available() bool {
	_, err := exec.LookPath("go")
	return err == nil
}

// goEnv appends toolchain settings that keep child `go` invocations predictable.
// GOTOOLCHAIN=local stops `go` from downloading a newer toolchain just because a
// binary (or module) declares one — we only want to query and install, not
// bootstrap a compiler.
func goEnv() []string {
	return append(envBase(), "GOTOOLCHAIN=local")
}

// goBin holds the parsed build info for one installed Go binary.
type goBin struct {
	name    string // binary basename, e.g. "gopls"
	module  string // module path, for version resolution, e.g. "golang.org/x/tools/gopls"
	pkg     string // package path, for `go install`, e.g. "golang.org/x/tools/gopls"
	version string // installed version, e.g. "v0.14.2"
}

func (Go) Check(ctx context.Context) ([]core.Update, error) {
	dir, err := goBinDir(ctx)
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		// No bin dir means nothing was ever `go install`ed — not an error.
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	// Read each binary's build info. Non-Go files, unreadable binaries, and
	// locally-built (`(devel)`) binaries have no upstream version to compare and
	// are skipped silently.
	var bins []goBin
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		path := filepath.Join(dir, e.Name())
		out, err := exec.CommandContext(ctx, "go", "version", "-m", path).Output()
		if err != nil {
			continue
		}
		if b, ok := parseGoVersionM(e.Name(), string(out)); ok {
			bins = append(bins, b)
		}
	}

	// Resolving each module's latest version hits the network, so fan out with a
	// bounded worker pool. Order is preserved to keep the panel stable.
	ups := make([]core.Update, len(bins))
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for i, b := range bins {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, b goBin) {
			defer wg.Done()
			defer func() { <-sem }()
			latest := goLatestVersion(ctx, b.module)
			if latest == "" || semver.Compare(latest, b.version) <= 0 {
				return // no resolvable newer version
			}
			ups[i] = core.Update{
				Name:           b.name,
				CurrentVersion: b.version,
				NewVersion:     latest,
				Source:         "go",
				Kind:           "binary",
				Repo:           b.module,
				Ref:            b.pkg,
			}
		}(i, b)
	}
	wg.Wait()

	// Compact away the slots that produced no update (Name stays "").
	out := ups[:0]
	for _, u := range ups {
		if u.Name != "" {
			out = append(out, u)
		}
	}
	return out, nil
}

// goBinDir returns the directory `go install` writes to: $GOBIN if set, else
// $GOPATH/bin.
func goBinDir(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "go", "env", "GOBIN", "GOPATH").Output()
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) > 0 && strings.TrimSpace(lines[0]) != "" {
		return strings.TrimSpace(lines[0]), nil
	}
	if len(lines) > 1 && strings.TrimSpace(lines[1]) != "" {
		return filepath.Join(strings.TrimSpace(lines[1]), "bin"), nil
	}
	return "", fmt.Errorf("go: neither GOBIN nor GOPATH is set")
}

// parseGoVersionM extracts the module path, package path, and installed version
// from `go version -m <binary>` output. It reports ok=false for output that
// isn't a Go binary, lacks a mod line, or was built from local source
// (version "(devel)").
func parseGoVersionM(name, out string) (goBin, bool) {
	b := goBin{name: name}
	for _, raw := range strings.Split(out, "\n") {
		fields := strings.Fields(raw)
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "path":
			b.pkg = fields[1]
		case "mod":
			// mod\t<module>\t<version>\t<hash>
			if len(fields) >= 3 {
				b.module = fields[1]
				b.version = fields[2]
			}
		}
	}
	if b.module == "" || b.version == "" || b.version == "(devel)" {
		return goBin{}, false
	}
	if b.pkg == "" {
		b.pkg = b.module // fall back to the module path if no explicit package path
	}
	return b, true
}

// goLatestVersion resolves the newest version of a module via the module proxy,
// or "" if it can't be determined (network error, no versions). Best-effort:
// a failure just means we don't offer an update for that binary.
func goLatestVersion(ctx context.Context, module string) string {
	cmd := exec.CommandContext(ctx, "go", "list", "-m", "-f", "{{.Version}}", module+"@latest")
	cmd.Env = goEnv()
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (g Go) Plan(ctx context.Context, selected []core.Update) (core.Plan, error) {
	// `go install` writes to the user's GOBIN — no root, no dependency preview.
	return core.Plan{Backend: g.Name(), Selected: selected, NeedsRoot: false}, nil
}

func (g Go) Apply(ctx context.Context, plan core.Plan) (<-chan core.ProgressEvent, error) {
	events := make(chan core.ProgressEvent, 64)

	go func() {
		defer close(events)

		if plan.DryRun {
			// `go install` has no dry-run flag, so we must never invoke it here —
			// report what would run and stop. (Same discipline as packagekit.go.)
			for _, u := range plan.Selected {
				events <- core.ProgressEvent{Kind: core.EventLog, Source: "go",
					Text: fmt.Sprintf("(dry run — would run: go install %s@latest)", g.installTarget(u))}
			}
			events <- core.ProgressEvent{Kind: core.EventDone, Source: "go", OK: true}
			return
		}

		for _, u := range plan.Selected {
			g.runInstall(ctx, events, u)
		}
		events <- core.ProgressEvent{Kind: core.EventDone, Source: "go", OK: true}
	}()

	return events, nil
}

// installTarget is the package path passed to `go install`, from the update's
// opaque Ref (set in Check); it falls back to the name if Ref is empty.
func (Go) installTarget(u core.Update) string {
	if u.Ref != "" {
		return u.Ref
	}
	return u.Name
}

// runInstall streams one `go install <pkg>@latest`, translating its output lines
// into structured events. go's output is sparse ("go: downloading ..."), so the
// phase is fixed per binary and completion is inferred from a clean exit.
func (g Go) runInstall(ctx context.Context, events chan<- core.ProgressEvent, u core.Update) {
	target := g.installTarget(u)
	events <- core.ProgressEvent{Kind: core.EventPhase, Source: "go", Item: u.Name, Phase: "Installing"}

	argv := []string{"go", "install", target + "@latest"}
	chunks, done := ptyrun.Stream(ctx, argv, ptyrun.Options{Env: goEnv(), IdleTimeoutMS: 15000})

	var carry string
	emit := func(line string) {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			return
		}
		events <- core.ProgressEvent{Kind: core.EventLog, Source: "go", Item: u.Name, Text: line}
	}

	for ch := range chunks {
		if ch.Idle {
			events <- core.ProgressEvent{Kind: core.EventPrompt, Source: "go", Item: u.Name,
				Text: "go install appears to be waiting for input"}
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
		events <- core.ProgressEvent{Kind: core.EventError, Source: "go", Item: u.Name, Text: err.Error()}
		return
	}
	events <- core.ProgressEvent{Kind: core.EventItemDone, Source: "go", Item: u.Name, OK: true}
}
