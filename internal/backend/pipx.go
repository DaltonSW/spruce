package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"go.dalton.dog/spruce/internal/core"
	"go.dalton.dog/spruce/internal/ptyrun"
)

// Pipx manages user-level Python CLI tools installed via `pipx install` —
// isolated venvs on PATH (e.g. httpie, black), never project/directory-local
// dependencies. Its update list comes from `pipx list --json` cross-checked
// against PyPI's latest release per package (pipx has no "outdated" command of
// its own), and each upgrade is `pipx upgrade <pkg>` streamed under a PTY.
type Pipx struct{}

func (Pipx) Name() string  { return "pipx" }
func (Pipx) Icon() string  { return "" }       // nf-dev-python
func (Pipx) Color() string { return "#ffd43b" } // Python yellow

func (Pipx) Available() bool {
	_, err := exec.LookPath("pipx")
	return err == nil
}

// pipxEnv keeps pipx's output parseable. Under the uv backend (pipx's default
// when uv is on PATH), pipx forwards straight to uv's own colored output and
// spinner/progress-bar redraws, which are keyed off the same env vars uv's
// own commands respect (see uvEnv).
func pipxEnv() []string {
	return append(envBase(), "NO_COLOR=1", "UV_NO_PROGRESS=1")
}

// pipxListJSON is the shape of `pipx list --json` that we care about; pipx
// emits far more (injected packages, app paths, python version, ...) but only
// each venv's main package maps onto core.Update.
type pipxListJSON struct {
	Venvs map[string]struct {
		Metadata struct {
			MainPackage struct {
				Package        string `json:"package"`
				PackageVersion string `json:"package_version"`
				Pinned         bool   `json:"pinned"`
			} `json:"main_package"`
		} `json:"metadata"`
	} `json:"venvs"`
}

func (Pipx) Check(ctx context.Context) ([]core.Update, error) {
	cmd := exec.CommandContext(ctx, "pipx", "list", "--json")
	cmd.Env = pipxEnv()
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var lst pipxListJSON
	if err := json.Unmarshal(out, &lst); err != nil {
		return nil, err
	}

	type installed struct {
		name    string
		version string
		pinned  bool
	}
	var pkgs []installed
	for _, v := range lst.Venvs {
		mp := v.Metadata.MainPackage
		if mp.Package == "" || mp.PackageVersion == "" {
			continue
		}
		pkgs = append(pkgs, installed{mp.Package, mp.PackageVersion, mp.Pinned})
	}
	sort.Slice(pkgs, func(i, j int) bool { return pkgs[i].name < pkgs[j].name })

	// Latest-version resolution hits PyPI per package, so fan out with a
	// bounded worker pool (mirrors Go.Check's use of the module proxy). Order
	// is preserved to keep the panel stable.
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
				Source:         "pipx",
				Kind:           "package",
				Pinned:         p.pinned,
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

// pypiHTTPClient is shared across latest-version lookups; a modest timeout
// keeps one slow or unreachable package from stalling the whole Check.
var pypiHTTPClient = &http.Client{Timeout: 10 * time.Second}

// pypiLatestVersion resolves a package's newest release via PyPI's JSON API,
// or "" if it can't be determined (network error, unknown package, bad
// response). Best-effort: a failure just means we don't offer an update for
// that package.
func pypiLatestVersion(ctx context.Context, name string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://pypi.org/pypi/"+url.PathEscape(name)+"/json", nil)
	if err != nil {
		return ""
	}
	resp, err := pypiHTTPClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}

	var body struct {
		Info struct {
			Version string `json:"version"`
		} `json:"info"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return ""
	}
	return body.Info.Version
}

func (p Pipx) Plan(ctx context.Context, selected []core.Update) (core.Plan, error) {
	// `pipx upgrade` writes to the user's pipx home (~/.local/share/pipx) — no
	// root, no dependency preview.
	plan := core.Plan{Backend: p.Name(), Selected: selected, NeedsRoot: false}
	for _, u := range selected {
		if u.Pinned {
			plan.Notes = append(plan.Notes, fmt.Sprintf(
				"%s is pinned (`pipx pin`) — spruce will skip it; run `pipx unpin %s` to allow upgrades.",
				u.Name, u.Name))
		}
	}
	return plan, nil
}

func (p Pipx) Apply(ctx context.Context, plan core.Plan) (<-chan core.ProgressEvent, error) {
	events := make(chan core.ProgressEvent, 64)

	go func() {
		defer close(events)

		if plan.DryRun {
			// pipx has no real dry-run flag, so we must never invoke it here —
			// report what would run and stop. (Same discipline as goinstall.go.)
			for _, u := range plan.Selected {
				if u.Pinned {
					continue
				}
				events <- core.ProgressEvent{Kind: core.EventLog, Source: "pipx",
					Text: fmt.Sprintf("(dry run — would run: pipx upgrade %s)", u.Name)}
			}
			events <- core.ProgressEvent{Kind: core.EventDone, Source: "pipx", OK: true}
			return
		}

		for _, u := range plan.Selected {
			if u.Pinned {
				continue // never touch pinned packages; pipx would refuse anyway
			}
			p.runUpgrade(ctx, events, u)
		}
		events <- core.ProgressEvent{Kind: core.EventDone, Source: "pipx", OK: true}
	}()

	return events, nil
}

// stripCursorVisibility removes ANSI cursor hide/show sequences (\x1b[?25l,
// \x1b[?25h). pipx's Rich-based spinner emits these around the whole run
// regardless of NO_COLOR, and left in place they'd leak raw escape codes into
// the log pane.
func stripCursorVisibility(s string) string {
	s = strings.ReplaceAll(s, "\x1b[?25l", "")
	s = strings.ReplaceAll(s, "\x1b[?25h", "")
	return s
}

// runUpgrade streams one `pipx upgrade <pkg>`, translating its output lines
// into structured events. pipx's install output is sparse, so the phase is
// fixed per package and completion is inferred from a clean exit.
func (Pipx) runUpgrade(ctx context.Context, events chan<- core.ProgressEvent, u core.Update) {
	events <- core.ProgressEvent{Kind: core.EventPhase, Source: "pipx", Item: u.Name, Phase: "Upgrading"}

	argv := []string{"pipx", "upgrade", u.Name}
	chunks, done := ptyrun.Stream(ctx, argv, ptyrun.Options{Env: pipxEnv(), IdleTimeoutMS: 15000})

	var carry string
	emit := func(line string) {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			return
		}
		events <- core.ProgressEvent{Kind: core.EventLog, Source: "pipx", Item: u.Name, Text: line}
	}

	for ch := range chunks {
		if ch.Idle {
			events <- core.ProgressEvent{Kind: core.EventPrompt, Source: "pipx", Item: u.Name,
				Text: "pipx appears to be waiting for input"}
			continue
		}
		// pipx's own spinner (independent of uv's, which UV_NO_PROGRESS already
		// silences) wraps the whole run in a cursor hide/show pair — strip those
		// two control sequences so they never reach the log pane.
		carry += stripCursorVisibility(ch.Data)
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
		events <- core.ProgressEvent{Kind: core.EventError, Source: "pipx", Item: u.Name, Text: err.Error()}
		return
	}
	events <- core.ProgressEvent{Kind: core.EventItemDone, Source: "pipx", Item: u.Name, OK: true}
}
