package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"go.dalton.dog/spruce/internal/core"
	"go.dalton.dog/spruce/internal/ptyrun"
)

// Npm manages user/global-level npm packages — the CLI tools installed with
// `npm install -g` (e.g. typescript, eslint) — never project/directory-local
// dependencies. Its update list comes from `npm outdated -g --json`, and each
// upgrade is `npm install -g <pkg>@latest` streamed under a PTY.
type Npm struct{}

func (Npm) Name() string  { return "npm" }
func (Npm) Icon() string  { return "" }       // nf-dev-npm
func (Npm) Color() string { return "#cb3837" } // npm red

func (Npm) Available() bool {
	_, err := exec.LookPath("npm")
	return err == nil
}

// npmEnv keeps npm's output predictable: no color, banner, fund/audit noise,
// or progress spinner.
func npmEnv() []string {
	return append(envBase(),
		"NPM_CONFIG_COLOR=false",
		"NO_UPDATE_NOTIFIER=1",
		"NPM_CONFIG_FUND=false",
		"NPM_CONFIG_AUDIT=false",
		"NPM_CONFIG_PROGRESS=false",
	)
}

func (Npm) Check(ctx context.Context) ([]core.Update, error) {
	// `npm outdated` exits non-zero when there are outdated packages, so
	// ignore the exit code and parse stdout regardless.
	cmd := exec.CommandContext(ctx, "npm", "outdated", "-g", "--json")
	cmd.Env = npmEnv()
	out, _ := cmd.Output()
	return parseNpmOutdated(out)
}

// npmOutdatedEntry is one value of the `npm outdated --json` object, which is
// keyed by package name.
type npmOutdatedEntry struct {
	Current  string `json:"current"`
	Wanted   string `json:"wanted"`
	Latest   string `json:"latest"`
	Location string `json:"location"`
}

// parseNpmOutdated turns `npm outdated -g --json` output into updates.
// Entries with no installed version, or already at latest, are skipped.
// Results are sorted by name since map order is random.
func parseNpmOutdated(data []byte) ([]core.Update, error) {
	data = []byte(strings.TrimSpace(string(data)))
	if len(data) == 0 {
		return nil, nil
	}

	var raw map[string]npmOutdatedEntry
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}

	var ups []core.Update
	for name, e := range raw {
		if e.Current == "" || e.Latest == "" || e.Latest == e.Current {
			continue
		}
		ups = append(ups, core.Update{
			Name:           name,
			CurrentVersion: e.Current,
			NewVersion:     e.Latest,
			Source:         "npm",
			Kind:           "package",
		})
	}
	sort.Slice(ups, func(i, j int) bool { return ups[i].Name < ups[j].Name })
	return ups, nil
}

func (n Npm) Plan(ctx context.Context, selected []core.Update) (core.Plan, error) {
	// A root-owned global prefix (common with system-wide Node) fails without
	// elevation; we never use raw sudo, so surface it as a Note instead.
	plan := core.Plan{Backend: n.Name(), Selected: selected, NeedsRoot: false}
	if prefix, ok := npmRootOwnedPrefix(ctx); ok {
		plan.Notes = append(plan.Notes, fmt.Sprintf(
			"npm's global prefix (%s) is root-owned, so spruce will skip these installs rather than "+
				"use sudo. Fix with `npm config set prefix ~/.npm-global` (and add its bin/ to PATH), "+
				"or switch to a user-level Node manager like nvm/fnm.", prefix))
	}
	return plan, nil
}

// npmRootOwnedPrefix reports whether npm's global install target is
// root-owned. Best-effort: any failure returns ("", false).
func npmRootOwnedPrefix(ctx context.Context) (string, bool) {
	cmd := exec.CommandContext(ctx, "npm", "prefix", "-g")
	cmd.Env = npmEnv()
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	prefix := strings.TrimSpace(string(out))
	if prefix == "" {
		return "", false
	}

	// Packages land under <prefix>/lib/node_modules; stat that if it exists,
	// otherwise fall back to the prefix itself.
	target := filepath.Join(prefix, "lib", "node_modules")
	info, err := os.Stat(target)
	if err != nil {
		target = prefix
		info, err = os.Stat(target)
		if err != nil {
			return "", false
		}
	}

	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", false
	}
	if st.Uid == 0 && int(st.Uid) != os.Geteuid() {
		return target, true
	}
	return "", false
}

func (n Npm) Apply(ctx context.Context, plan core.Plan) (<-chan core.ProgressEvent, error) {
	events := make(chan core.ProgressEvent, 64)

	go func() {
		defer close(events)

		if plan.DryRun {
			events <- core.ProgressEvent{Kind: core.EventLog, Source: "npm",
				Text: "(dry run — nothing will be installed)"}
		}

		// A root-owned prefix fails every install with EACCES; check once and
		// skip rather than let each package fail individually.
		if !plan.DryRun {
			if prefix, ok := npmRootOwnedPrefix(ctx); ok {
				events <- core.ProgressEvent{Kind: core.EventError, Source: "npm", Text: fmt.Sprintf(
					"npm's global prefix (%s) is root-owned — skipping, spruce never uses sudo. "+
						"Fix with `npm config set prefix ~/.npm-global` (add its bin/ to PATH) or "+
						"switch to a user-level Node manager like nvm/fnm.", prefix)}
				events <- core.ProgressEvent{Kind: core.EventDone, Source: "npm", OK: false}
				return
			}
		}

		for _, u := range plan.Selected {
			n.runInstall(ctx, events, u, plan.DryRun)
		}
		events <- core.ProgressEvent{Kind: core.EventDone, Source: "npm", OK: true}
	}()

	return events, nil
}

// runInstall streams one `npm install -g <pkg>@latest`. npm's install output
// is sparse, so the phase is fixed per package and completion is inferred
// from a clean exit. npm supports a real --dry-run, so dry runs invoke it
// directly.
func (Npm) runInstall(ctx context.Context, events chan<- core.ProgressEvent, u core.Update, dryRun bool) {
	events <- core.ProgressEvent{Kind: core.EventPhase, Source: "npm", Item: u.Name, Phase: "Installing"}

	// -g scopes to the global prefix regardless of cwd, so no project
	// package.json can leak in (ptyrun.Options.Dir left empty).
	argv := []string{"npm", "install", "-g", u.Name + "@latest"}
	if dryRun {
		argv = append(argv, "--dry-run")
	}
	chunks, done := ptyrun.Stream(ctx, argv, ptyrun.Options{Env: npmEnv()})

	var carry string
	emit := func(line string) {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			return
		}
		events <- core.ProgressEvent{Kind: core.EventLog, Source: "npm", Item: u.Name, Text: line}
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
		events <- core.ProgressEvent{Kind: core.EventError, Source: "npm", Item: u.Name, Text: err.Error()}
		return
	}
	events <- core.ProgressEvent{Kind: core.EventItemDone, Source: "npm", Item: u.Name, OK: true}
}
