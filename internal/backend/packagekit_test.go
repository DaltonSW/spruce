package backend

import (
	"context"
	"testing"
	"time"

	"go.dalton.dog/spruce/internal/core"
)

// TestPackageKitDryRunIsPreviewOnly guards the dry-run fix: PackageKit's
// SIMULATE transaction flag was ignored by the dnf5 backend and a "dry run"
// upgraded the system for real. A dry run must now stay entirely in the
// early-return preview branch — it must never open the system bus or call the
// mutating UpdatePackages.
//
// We assert the event stream has the exact preview shape. Only the dry-run
// branch emits "Would update" phases (the real path emits "Updating" and needs
// a D-Bus connection), so matching that shape proves we never reached the
// mutating path. The timeout guards against a regression where the real path
// leaks through and blocks on polkit/the daemon.
func TestPackageKitDryRunIsPreviewOnly(t *testing.T) {
	sel := []core.Update{
		{Name: "bash", NewVersion: "5.2.1", Source: "system", Ref: "bash;5.2.1;x86_64;updates", Kind: "package"},
		{Name: "coreutils", NewVersion: "9.4", Source: "system", Ref: "coreutils;9.4;x86_64;updates", Kind: "package"},
	}
	plan := core.Plan{Backend: "system", Selected: sel, NeedsRoot: true, DryRun: true}

	ch, err := PackageKit{}.Apply(context.Background(), plan)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	var (
		phaseItems []string
		items      int
		dones      int
		doneOK     bool
	)
	timeout := time.After(5 * time.Second)
collect:
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				break collect
			}
			switch ev.Kind {
			case core.EventError:
				t.Fatalf("dry run emitted an error event (it should never touch the system): %q", ev.Text)
			case core.EventPhase:
				if ev.Phase != "Would update" {
					t.Errorf("dry run phase = %q, want %q — the real apply path leaked through", ev.Phase, "Would update")
				}
				phaseItems = append(phaseItems, ev.Item)
			case core.EventItemDone:
				items++
			case core.EventDone:
				dones++
				doneOK = ev.OK
			}
		case <-timeout:
			t.Fatal("dry run did not finish promptly — the real D-Bus path may have leaked through")
		}
	}

	if got, want := len(phaseItems), len(sel); got != want {
		t.Fatalf("got %d \"Would update\" phases, want %d", got, want)
	}
	for i, u := range sel {
		if phaseItems[i] != u.Name {
			t.Errorf("phase %d item = %q, want %q", i, phaseItems[i], u.Name)
		}
	}
	if items != len(sel) {
		t.Errorf("got %d ItemDone events, want %d", items, len(sel))
	}
	if dones != 1 || !doneOK {
		t.Errorf("got %d EventDone (ok=%v), want exactly one successful done", dones, doneOK)
	}
}
