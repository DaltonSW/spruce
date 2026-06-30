package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"go.dalton.dog/spruce/internal/core"
	"go.dalton.dog/spruce/internal/ptyrun"
)

// Brew wraps Homebrew. There is no embedding API, so we use its machine-readable
// JSON for the update list and PTY-wrap `brew upgrade` for live progress.
type Brew struct{}

func (Brew) Name() string { return "brew" }

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
	for _, c := range od.Casks {
		ups = append(ups, core.Update{
			Name:           c.Name,
			CurrentVersion: lastOf(c.InstalledVersions),
			NewVersion:     c.CurrentVersion,
			Source:         "brew",
			Kind:           "cask",
		})
	}
	return ups, nil
}

func (b Brew) Plan(ctx context.Context, selected []core.Update) (core.Plan, error) {
	// brew doesn't expose reliable download sizes up front; the review screen
	// shows the item list and we leave DownloadBytes unknown.
	plan := core.Plan{Backend: b.Name(), Selected: selected, NeedsRoot: false}

	// brew upgrade of a formula also upgrades any *outdated formulae that depend
	// on it* (e.g. naming `acl` pulls in `vim`). The only reliable way to learn
	// that full set is to ask brew itself via --dry-run — we never recompute its
	// resolver. Anything it would touch that the user didn't explicitly pick is
	// surfaced as a Note so the review/confirm screens can warn before applying.
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

// brewDryRunUpgrades runs `brew upgrade --dry-run …` and returns each package
// listed under "Would upgrade N outdated packages", normalized to a single
// "name old -> new (size)" line. Best-effort: a non-zero exit or unparseable
// chatter (tap-trust notices, download lines) is tolerated and skipped.
func brewDryRunUpgrades(ctx context.Context, argv []string) []string {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = brewEnv()
	out, _ := cmd.Output()
	return parseBrewUpgrades(string(out))
}

// parseBrewUpgrades extracts the package lines from `brew upgrade --dry-run`
// output — everything under "==> Would upgrade N outdated packages" up to the
// next blank line or header — normalized to single-spaced "name old -> new
// (size)" lines. All the surrounding chatter (tap-trust notices, download
// progress) is ignored.
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

func (b Brew) Apply(ctx context.Context, plan core.Plan) (<-chan core.ProgressEvent, error) {
	events := make(chan core.ProgressEvent, 64)

	var formulae, casks []string
	for _, u := range plan.Selected {
		if u.Pinned {
			continue // never touch pinned formulae
		}
		if u.Kind == "cask" {
			casks = append(casks, u.Name)
		} else {
			formulae = append(formulae, u.Name)
		}
	}

	base := []string{"brew", "upgrade"}
	if plan.DryRun {
		base = append(base, "--dry-run")
	}

	go func() {
		defer close(events)
		if plan.DryRun {
			events <- core.ProgressEvent{Kind: core.EventLog, Source: "brew",
				Text: "(dry run — nothing will be upgraded)"}
		}
		if len(formulae) > 0 {
			b.runUpgrade(ctx, events, append(append([]string{}, base...), formulae...))
		}
		if len(casks) > 0 {
			b.runUpgrade(ctx, events, append(append(append([]string{}, base...), "--cask"), casks...))
		}
		events <- core.ProgressEvent{Kind: core.EventDone, Source: "brew", OK: true}
	}()

	return events, nil
}

// runUpgrade streams one `brew upgrade` invocation, translating output lines
// into structured events. brew's prefixes ("==> Upgrading", "==> Downloading",
// "==> Pouring", "🍺") give us enough to drive phases without a real API.
func (b Brew) runUpgrade(ctx context.Context, events chan<- core.ProgressEvent, argv []string) {
	chunks, done := ptyrun.Stream(ctx, argv, ptyrun.Options{Env: brewEnv(), IdleTimeoutMS: 4000})

	var carry string
	emit := func(line string) {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			return
		}
		events <- core.ProgressEvent{Kind: core.EventLog, Source: "brew", Text: line}
		switch {
		case strings.HasPrefix(line, "==> Upgrading ") && !strings.Contains(line, "outdated"):
			name := firstField(strings.TrimPrefix(line, "==> Upgrading "))
			events <- core.ProgressEvent{Kind: core.EventPhase, Source: "brew", Item: name, Phase: "Upgrading"}
		case strings.HasPrefix(line, "==> Downloading"):
			events <- core.ProgressEvent{Kind: core.EventPhase, Source: "brew", Phase: "Downloading"}
		case strings.HasPrefix(line, "==> Pouring") || strings.HasPrefix(line, "==> Installing"):
			events <- core.ProgressEvent{Kind: core.EventPhase, Source: "brew", Phase: "Installing"}
		case strings.HasPrefix(line, "🍺"):
			events <- core.ProgressEvent{Kind: core.EventItemDone, Source: "brew", OK: true}
		}
	}

	for ch := range chunks {
		if ch.Idle {
			// Non-interactive flags mean we shouldn't be at a prompt; surface
			// it rather than hang silently.
			events <- core.ProgressEvent{Kind: core.EventPrompt, Source: "brew",
				Text: "brew appears to be waiting for input"}
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
		events <- core.ProgressEvent{Kind: core.EventError, Source: "brew", Text: err.Error()}
	}
}
