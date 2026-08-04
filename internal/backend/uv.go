package backend

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"

	"go.dalton.dog/spruce/internal/core"
	"go.dalton.dog/spruce/internal/ptyrun"
)

// Uv manages user-level Python CLI tools installed via `uv tool install` —
// isolated venvs on PATH (e.g. ruff, httpie), never project/directory-local
// dependencies. This is the same niche pipx fills; uv is offered as a
// separate backend (rather than folded into Pipx) because the two tools are
// commonly installed side by side and track disjoint sets of packages. Its
// update list comes straight from `uv tool list --outdated`, and each
// upgrade is `uv tool upgrade <pkg>` streamed under a PTY.
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

// uvOutdatedRe matches the header line of `uv tool list --outdated`, e.g.
// "ruff v0.5.0 [latest: 0.16.1]". The lines that follow each header (one per
// exposed executable, "- <name>") carry no version info and are ignored.
var uvOutdatedRe = regexp.MustCompile(`^(\S+) v(\S+) \[latest: (\S+)\]$`)

func (Uv) Check(ctx context.Context) ([]core.Update, error) {
	cmd := exec.CommandContext(ctx, "uv", "tool", "list", "--outdated", "--color=never")
	cmd.Env = uvEnv()
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return parseUvOutdated(out), nil
}

func parseUvOutdated(out []byte) []core.Update {
	var ups []core.Update
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		m := uvOutdatedRe.FindStringSubmatch(line)
		if m == nil {
			continue // an exposed-executable line ("- ruff") or other chatter
		}
		ups = append(ups, core.Update{
			Name:           m[1],
			CurrentVersion: m[2],
			NewVersion:     m[3],
			Source:         "uv",
			Kind:           "tool",
		})
	}
	return ups
}

func (u Uv) Plan(ctx context.Context, selected []core.Update) (core.Plan, error) {
	// `uv tool upgrade` writes to the user's uv tool dir (~/.local/share/uv/tools)
	// — no root, no dependency preview.
	return core.Plan{Backend: u.Name(), Selected: selected, NeedsRoot: false}, nil
}

func (u Uv) Apply(ctx context.Context, plan core.Plan) (<-chan core.ProgressEvent, error) {
	events := make(chan core.ProgressEvent, 64)

	go func() {
		defer close(events)

		if plan.DryRun {
			// `uv tool upgrade` has no dry-run flag, so we must never invoke it
			// here — report what would run and stop. (Same discipline as
			// goinstall.go and pipx.go.)
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

// runUpgrade streams one `uv tool upgrade <pkg>`, translating its output lines
// into structured events. A tool installed with an explicit version
// specifier (e.g. `uv tool install ruff==0.5.0`) is pinned in uv's own sense
// and uv declines the upgrade with an explanatory line rather than erroring —
// that line surfaces to the user as a normal log entry, same as any other uv
// output.
func (Uv) runUpgrade(ctx context.Context, events chan<- core.ProgressEvent, up core.Update) {
	events <- core.ProgressEvent{Kind: core.EventPhase, Source: "uv", Item: up.Name, Phase: "Upgrading"}

	argv := []string{"uv", "tool", "upgrade", up.Name, "--color=never", "--no-progress"}
	chunks, done := ptyrun.Stream(ctx, argv, ptyrun.Options{Env: uvEnv(), IdleTimeoutMS: 15000})

	var carry string
	emit := func(line string) {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			return
		}
		events <- core.ProgressEvent{Kind: core.EventLog, Source: "uv", Item: up.Name, Text: line}
	}

	for ch := range chunks {
		if ch.Idle {
			events <- core.ProgressEvent{Kind: core.EventPrompt, Source: "uv", Item: up.Name,
				Text: "uv appears to be waiting for input"}
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
		events <- core.ProgressEvent{Kind: core.EventError, Source: "uv", Item: up.Name, Text: err.Error()}
		return
	}
	events <- core.ProgressEvent{Kind: core.EventItemDone, Source: "uv", Item: up.Name, OK: true}
}
