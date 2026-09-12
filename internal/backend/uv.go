package backend

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"sync"

	"go.dalton.dog/spruce/internal/core"
	"go.dalton.dog/spruce/internal/ptyrun"
)

// Uv manages CLI tools installed via `uv tool install` (ruff, httpie, etc),
// the same niche pipx fills. `uv tool list --outdated` is version-gated, so
// like Pipx the update list comes from `uv tool list` cross-checked against
// PyPI. Upgrades run `uv tool upgrade <pkg>` under a PTY.
type Uv struct{}

func (Uv) Name() string  { return "uv" }
func (Uv) Icon() string  { return "" }       // nf-dev-python
func (Uv) Color() string { return "#de5fe9" } // uv's magenta/purple mark

func (Uv) Available() bool {
	_, err := exec.LookPath("uv")
	return err == nil
}

// uvEnv keeps uv's output plain text and colorless so it stays parseable.
func uvEnv() []string {
	return append(envBase(), "NO_COLOR=1")
}

// uvListRe matches a `uv tool list` header line, e.g. "ruff v0.5.0".
var uvListRe = regexp.MustCompile(`^(\S+) v(\S+)$`)

func (Uv) Check(ctx context.Context) ([]core.Update, error) {
	cmd := exec.CommandContext(ctx, "uv", "tool", "list", "--color=never")
	cmd.Env = uvEnv()
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	type installed struct{ name, version string }
	var pkgs []installed
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		m := uvListRe.FindStringSubmatch(line)
		if m == nil {
			continue // an exposed-executable line ("- ruff") or other chatter
		}
		pkgs = append(pkgs, installed{m[1], m[2]})
	}

	// Fan out PyPI lookups with a bounded worker pool; order preserved.
	ups := make([]core.Update, len(pkgs))
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for i, p := range pkgs {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, p installed) {
			defer wg.Done()
			defer func() { <-sem }()
			latest := pypiLatestVersion(ctx, p.name)
			if latest == "" || latest == p.version {
				return // up to date, or couldn't resolve — offer nothing
			}
			ups[i] = core.Update{
				Name:           p.name,
				CurrentVersion: p.version,
				NewVersion:     latest,
				Source:         "uv",
				Kind:           "tool",
			}
		}(i, p)
	}
	wg.Wait()

	// Compact away the slots that produced no update (Name stays "").
	out2 := ups[:0]
	for _, u := range ups {
		if u.Name != "" {
			out2 = append(out2, u)
		}
	}
	return out2, nil
}

func (u Uv) Plan(ctx context.Context, selected []core.Update) (core.Plan, error) {
	return core.Plan{Backend: u.Name(), Selected: selected, NeedsRoot: false}, nil
}

func (u Uv) Apply(ctx context.Context, plan core.Plan) (<-chan core.ProgressEvent, error) {
	events := make(chan core.ProgressEvent, 64)

	go func() {
		defer close(events)

		if plan.DryRun {
			// `uv tool upgrade` has no dry-run flag, so never invoke it here —
			// report what would run and stop.
			for _, up := range plan.Selected {
				events <- core.ProgressEvent{Kind: core.EventLog, Source: "uv",
					Text: fmt.Sprintf("(dry run — would run: uv tool upgrade %s)", up.Name)}
			}
			events <- core.ProgressEvent{Kind: core.EventDone, Source: "uv", OK: true}
			return
		}

		for _, up := range plan.Selected {
			u.runUpgrade(ctx, events, up)
		}
		events <- core.ProgressEvent{Kind: core.EventDone, Source: "uv", OK: true}
	}()

	return events, nil
}

// runUpgrade streams one `uv tool upgrade <pkg>`. A tool pinned to an
// explicit version (`uv tool install ruff==0.5.0`) makes uv decline with an
// explanatory line rather than erroring; that line surfaces as a normal log
// entry.
func (Uv) runUpgrade(ctx context.Context, events chan<- core.ProgressEvent, up core.Update) {
	events <- core.ProgressEvent{Kind: core.EventPhase, Source: "uv", Item: up.Name, Phase: "Upgrading"}

	argv := []string{"uv", "tool", "upgrade", up.Name, "--color=never", "--no-progress"}
	chunks, done := ptyrun.Stream(ctx, argv, ptyrun.Options{Env: uvEnv()})

	var carry string
	emit := func(line string) {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			return
		}
		events <- core.ProgressEvent{Kind: core.EventLog, Source: "uv", Item: up.Name, Text: line}
	}

	for ch := range chunks {
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
		events <- core.ProgressEvent{Kind: core.EventError, Source: "uv", Item: up.Name, Text: err.Error()}
		return
	}
	events <- core.ProgressEvent{Kind: core.EventItemDone, Source: "uv", Item: up.Name, OK: true}
}
