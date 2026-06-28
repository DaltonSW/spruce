package backend

import (
	"context"
	"encoding/json"
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
	return core.Plan{Backend: b.Name(), Selected: selected, NeedsRoot: false}, nil
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

	go func() {
		defer close(events)
		if len(formulae) > 0 {
			b.runUpgrade(ctx, events, append([]string{"brew", "upgrade"}, formulae...))
		}
		if len(casks) > 0 {
			b.runUpgrade(ctx, events, append([]string{"brew", "upgrade", "--cask"}, casks...))
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
