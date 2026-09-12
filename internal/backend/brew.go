package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"go.dalton.dog/spruce/internal/core"
	"go.dalton.dog/spruce/internal/ptyrun"
)

// Brew wraps Homebrew. There is no embedding API, so we use its machine-readable
// JSON for the update list and PTY-wrap `brew upgrade` for live progress.
type Brew struct{}

func (Brew) Name() string  { return "brew" }
func (Brew) Icon() string  { return "" }       // nf-fa-beer
func (Brew) Color() string { return "#f6b552" } // amber — the Homebrew mug

func (Brew) Available() bool {
	_, err := exec.LookPath("brew")
	return err == nil
}

// brewEnv keeps brew's output predictable and avoids surprise work mid-run.
func brewEnv() []string {
	return append(envBase(),
		"HOMEBREW_NO_AUTO_UPDATE=1",
		"HOMEBREW_NO_COLOR=1",
		"HOMEBREW_NO_ENV_HINTS=1",
		"HOMEBREW_NO_INSTALL_UPGRADE=1",
	)
}

// JSON shape of `brew outdated --json=v2`.
type brewOutdated struct {
	Formulae []struct {
		Name              string   `json:"name"`
		InstalledVersions []string `json:"installed_versions"`
		CurrentVersion    string   `json:"current_version"`
		Pinned            bool     `json:"pinned"`
	} `json:"formulae"`
	Casks []struct {
		Name              string   `json:"name"`
		InstalledVersions []string `json:"installed_versions"`
		CurrentVersion    string   `json:"current_version"`
	} `json:"casks"`
}

func (Brew) Check(ctx context.Context) ([]core.Update, error) {
	// Refresh metadata first; a failure here shouldn't abort the check.
	_ = exec.CommandContext(ctx, "brew", "update", "--quiet").Run()

	cmd := exec.CommandContext(ctx, "brew", "outdated", "--json=v2", "--greedy-auto-updates")
	cmd.Env = brewEnv()
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var od brewOutdated
	if err := json.Unmarshal(out, &od); err != nil {
		return nil, err
	}

	var ups []core.Update
	for _, f := range od.Formulae {
		ups = append(ups, core.Update{
			Name:           f.Name,
			CurrentVersion: lastOf(f.InstalledVersions),
			NewVersion:     f.CurrentVersion,
			Source:         "brew",
			Kind:           "formula",
			Pinned:         f.Pinned,
		})
	}
	if len(od.Casks) > 0 {
		// `brew outdated` reports only a cask's bare token, which is ambiguous
		// across taps; tap-qualify via the installed-cask inventory, falling
		// back to the bare token on a lookup miss.
		fullTokens := brewCaskFullTokens(ctx)
		for _, c := range od.Casks {
			name := c.Name
			if full, ok := fullTokens[c.Name]; ok {
				name = full
			}
			ups = append(ups, core.Update{
				Name:           name,
				CurrentVersion: lastOf(c.InstalledVersions),
				NewVersion:     c.CurrentVersion,
				Source:         "brew",
				Kind:           "cask",
			})
		}
	}
	return ups, nil
}

// brewCaskFullTokens maps installed casks' bare tokens to their tap-qualified
// full tokens. Best-effort: a failure yields a nil map.
func brewCaskFullTokens(ctx context.Context) map[string]string {
	cmd := exec.CommandContext(ctx, "brew", "info", "--json=v2", "--cask", "--installed")
	cmd.Env = brewEnv()
	out, err := cmd.Output()
	if err != nil {
		return nil
	}

	var info struct {
		Casks []struct {
			Token     string `json:"token"`
			FullToken string `json:"full_token"`
		} `json:"casks"`
	}
	if err := json.Unmarshal(out, &info); err != nil {
		return nil
	}

	tokens := make(map[string]string, len(info.Casks))
	for _, c := range info.Casks {
		tokens[c.Token] = c.FullToken
	}
	return tokens
}

func (b Brew) Plan(ctx context.Context, selected []core.Update) (core.Plan, error) {
	plan := core.Plan{Backend: b.Name(), Selected: selected, NeedsRoot: false}

	// Upgrading a formula also pulls in any outdated dependents (e.g. `acl`
	// pulls in `vim`). Ask brew via --dry-run rather than recompute its
	// resolver, and surface anything unrequested as a Note before applying.
	requested := map[string]bool{}
	var formulae, casks []string
	for _, u := range selected {
		if u.Pinned {
			continue // mirrors Apply: pinned formulae are never touched
		}
		requested[u.Name] = true
		if u.Kind == "cask" {
			casks = append(casks, u.Name)
		} else {
			formulae = append(formulae, u.Name)
		}
	}

	var extras []string
	add := func(argv []string) {
		for _, line := range brewDryRunUpgrades(ctx, argv) {
			if name := firstField(line); name != "" && !requested[name] {
				extras = append(extras, line)
			}
		}
	}
	if len(formulae) > 0 {
		add(append([]string{"brew", "upgrade", "--dry-run"}, formulae...))
	}
	if len(casks) > 0 {
		add(append([]string{"brew", "upgrade", "--dry-run", "--cask"}, casks...))
	}

	if len(extras) > 0 {
		s := "s"
		if len(extras) == 1 {
			s = ""
		}
		plan.Notes = append(plan.Notes,
			fmt.Sprintf("brew will also upgrade %d dependent package%s:", len(extras), s))
		plan.Notes = append(plan.Notes, extras...)
	}
	return plan, nil
}

// brewDryRunUpgrades runs `brew upgrade --dry-run …` and returns each listed
// package as a "name old -> new (size)" line. Best-effort: errors and
// unparseable chatter are skipped.
func brewDryRunUpgrades(ctx context.Context, argv []string) []string {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = brewEnv()
	out, _ := cmd.Output()
	return parseBrewUpgrades(string(out))
}

// parseBrewUpgrades extracts the package lines under "==> Would upgrade N
// outdated packages" from `brew upgrade --dry-run` output, normalized to
// single-spaced lines.
func parseBrewUpgrades(out string) []string {
	var lines []string
	inBlock := false
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimRight(raw, "\r")
		if strings.HasPrefix(line, "==> Would upgrade") {
			inBlock = true
			continue
		}
		if !inBlock {
			continue
		}
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "==>") {
			break // the package list ends at the next blank line or header
		}
		lines = append(lines, strings.Join(strings.Fields(line), " "))
	}
	return lines
}

// brewPercentRe matches brew's download meter, e.g. "###   64.2%".
var brewPercentRe = regexp.MustCompile(`(\d+(?:\.\d+)?)%\s*$`)

// brewDownloadPercent returns the meter's percentage as a 0-1 fraction.
func brewDownloadPercent(line string) (float64, bool) {
	m := brewPercentRe.FindStringSubmatch(line)
	if m == nil {
		return 0, false
	}
	pct, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, false
	}
	return pct / 100, true
}

func (b Brew) Apply(ctx context.Context, plan core.Plan) (<-chan core.ProgressEvent, error) {
	events := make(chan core.ProgressEvent, 64)

	go func() {
		defer close(events)
		if plan.DryRun {
			events <- core.ProgressEvent{Kind: core.EventLog, Source: "brew",
				Text: "(dry run — nothing will be upgraded)"}
		}
		for _, u := range plan.Selected {
			if u.Pinned {
				continue // never touch pinned formulae
			}
			b.runUpgrade(ctx, events, u, plan.DryRun, plan.AutoConfirmPrompts)
		}
		events <- core.ProgressEvent{Kind: core.EventDone, Source: "brew", OK: true}
	}()

	return events, nil
}

// runUpgrade streams a single `brew upgrade <name>` (or `--cask <name>`). One
// package per invocation bounds brew's silent pre-flight and lets us announce
// the active item up front.
//
// autoConfirm suppresses brew's "ask mode" prompt (consent already secured by
// the TUI's gate); otherwise stdin is /dev/null over the PTY, so an unanswered
// prompt reads as EOF and brew declines rather than hanging.
func (b Brew) runUpgrade(ctx context.Context, events chan<- core.ProgressEvent, u core.Update, dryRun, autoConfirm bool) {
	argv := []string{"brew", "upgrade"}
	if dryRun {
		argv = append(argv, "--dry-run")
	}
	if u.Kind == "cask" {
		argv = append(argv, "--cask")
	}
	argv = append(argv, u.Name)

	events <- core.ProgressEvent{Kind: core.EventPhase, Source: "brew", Item: u.Name, Phase: "Upgrading"}

	env := brewEnv()
	if autoConfirm {
		env = append(env, "HOMEBREW_NO_ASK=1")
	}
	chunks, done := ptyrun.Stream(ctx, argv, ptyrun.Options{Env: env})

	var carry string
	emit := func(line string) {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			return
		}
		events <- core.ProgressEvent{Kind: core.EventLog, Source: "brew", Item: u.Name, Text: line}
		switch {
		case strings.HasPrefix(line, "==> Downloading"):
			events <- core.ProgressEvent{Kind: core.EventPhase, Source: "brew", Item: u.Name, Phase: "Downloading"}
		case strings.HasPrefix(line, "==> Pouring") || strings.HasPrefix(line, "==> Installing"):
			events <- core.ProgressEvent{Kind: core.EventPhase, Source: "brew", Item: u.Name, Phase: "Installing"}
		}
	}

	for ch := range chunks {
		data := ch.Data
		for {
			// Brew redraws its download meter in place via '\r'; parse it as
			// progress and drop it instead of spamming EventLog.
			i := strings.IndexAny(data, "\r\n")
			if i < 0 {
				carry += data
				break
			}
			carry += data[:i]
			nl := data[i] == '\n'
			data = data[i+1:]
			if nl {
				emit(carry)
			} else if pct, ok := brewDownloadPercent(carry); ok {
				events <- core.ProgressEvent{Kind: core.EventProgress, Source: "brew", Item: u.Name, Fraction: pct}
			}
			carry = ""
		}
	}
	emit(carry)

	if err := <-done; err != nil {
		events <- core.ProgressEvent{Kind: core.EventError, Source: "brew", Item: u.Name, Text: err.Error()}
		return
	}
	events <- core.ProgressEvent{Kind: core.EventItemDone, Source: "brew", Item: u.Name, OK: true}
}
