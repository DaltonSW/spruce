package tui

import (
	"testing"

	"go.dalton.dog/spruce/internal/core"
)

// Downloading a package mustn't mark it done: a staged backend fetches
// everything first, so that would show every row ✓ before anything installs.
func TestStagedDownloadIsNotDone(t *testing.T) {
	m := &Model{progress: map[string]*srcState{}}
	for _, n := range []string{"a", "b", "c"} {
		m.applyEvent(core.ProgressEvent{Kind: core.EventPhase, Source: "system", Item: n, Phase: "Downloading", Stage: core.StageDownload})
	}
	m.applyEvent(core.ProgressEvent{Kind: core.EventPhase, Source: "system", Stage: core.StageInstall})
	st := m.progress["system"]
	pkgs := []core.Update{{Name: "a"}, {Name: "b"}, {Name: "c"}}
	for i, u := range pkgs {
		if got := pkgRowStatus(i, u.Name, st); got != statFetched {
			t.Errorf("%s after download = %v, want statFetched", u.Name, got)
		}
	}
	if f := applyOverallFraction(pkgs, st); f != 0.5 {
		t.Errorf("overall after downloads = %v, want 0.5", f)
	}

	// Install order differs from selection order; completion is by name.
	m.applyEvent(core.ProgressEvent{Kind: core.EventPhase, Source: "system", Item: "c", Phase: "Updating", Stage: core.StageInstall})
	m.applyEvent(core.ProgressEvent{Kind: core.EventItemDone, Source: "system", Item: "c", OK: true})
	m.applyEvent(core.ProgressEvent{Kind: core.EventPhase, Source: "system", Item: "a", Phase: "Updating", Stage: core.StageInstall})
	want := []pkgStat{statActive, statFetched, statDone}
	for i, u := range pkgs {
		if got := pkgRowStatus(i, u.Name, st); got != want[i] {
			t.Errorf("%s = %v, want %v", u.Name, got, want[i])
		}
	}

	// The backend's own whole-transaction figure wins over the row average.
	m.applyEvent(core.ProgressEvent{Kind: core.EventOverall, Source: "system", Fraction: 0.7})
	if f := applyOverallFraction(pkgs, st); f != 0.7 {
		t.Errorf("overall = %v, want 0.7", f)
	}
}
