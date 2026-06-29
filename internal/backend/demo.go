package backend

import (
	"context"
	"fmt"
	"time"

	"go.dalton.dog/spruce/internal/core"
)

// DemoBackends returns a set of fake backends for exercising the UI without
// touching the system (spruce --demo). They simulate realistic discovery delays
// and a scripted, animated apply, and cover every panel state: a big scrolling
// list, small lists, an up-to-date backend, and an apply that fails partway.
func DemoBackends() []core.Backend {
	return []core.Backend{
		demoBackend{name: "system", count: 42, checkDelay: 1300 * time.Millisecond},
		demoBackend{name: "brew", count: 6, checkDelay: 400 * time.Millisecond},
		demoBackend{name: "flatpak", count: 3, checkDelay: 650 * time.Millisecond, failApply: true},
		demoBackend{name: "snap", count: 0, checkDelay: 300 * time.Millisecond},
	}
}

type demoBackend struct {
	name       string
	count      int
	checkDelay time.Duration
	failApply  bool // emit an error partway through Apply, to exercise that path
}

func (d demoBackend) Name() string  { return d.name }
func (demoBackend) Available() bool { return true }

func (d demoBackend) Check(ctx context.Context) ([]core.Update, error) {
	if !sleep(ctx, d.checkDelay) {
		return nil, ctx.Err()
	}
	ups := make([]core.Update, 0, d.count)
	for i := range d.count {
		pinned := d.name == "system" && i%13 == 0
		ups = append(ups, core.Update{
			Name:           fmt.Sprintf("%s-package-%02d", d.name, i),
			CurrentVersion: fmt.Sprintf("1.%d.0", i%9),
			NewVersion:     fmt.Sprintf("1.%d.1", i%9),
			Source:         d.name,
			Kind:           "package",
			Pinned:         pinned,
			// Spread a believable range of sizes (tens of kB to ~200 MB) so the
			// size column and totals have something to show.
			SizeBytes: int64(40_000+i*97_000) * int64(1+i%7),
		})
	}
	return ups, nil
}

func (d demoBackend) Plan(_ context.Context, selected []core.Update) (core.Plan, error) {
	return core.Plan{Backend: d.name, Selected: selected}, nil
}

func (d demoBackend) Apply(ctx context.Context, plan core.Plan) (<-chan core.ProgressEvent, error) {
	events := make(chan core.ProgressEvent, 64)

	go func() {
		defer close(events)
		emit := func(ev core.ProgressEvent) bool {
			ev.Source = d.name
			select {
			case <-ctx.Done():
				return false
			case events <- ev:
				return true
			}
		}

		if plan.DryRun {
			emit(core.ProgressEvent{Kind: core.EventLog, Text: "(demo dry run — nothing changes)"})
		}

		for i, u := range plan.Selected {
			if !emit(core.ProgressEvent{Kind: core.EventPhase, Item: u.Name, Phase: "Downloading"}) {
				return
			}
			for f := 0.0; f < 1.0; f += 0.34 {
				if !sleep(ctx, 90*time.Millisecond) ||
					!emit(core.ProgressEvent{Kind: core.EventProgress, Item: u.Name, Fraction: f}) {
					return
				}
			}
			if !emit(core.ProgressEvent{Kind: core.EventPhase, Item: u.Name, Phase: "Installing"}) {
				return
			}
			sleep(ctx, 120*time.Millisecond)

			if d.failApply && i == len(plan.Selected)/2 {
				emit(core.ProgressEvent{Kind: core.EventError, Text: "simulated failure during install"})
				emit(core.ProgressEvent{Kind: core.EventDone})
				return
			}
			if !emit(core.ProgressEvent{Kind: core.EventItemDone, OK: true}) {
				return
			}
		}
		emit(core.ProgressEvent{Kind: core.EventDone, OK: true})
	}()

	return events, nil
}

// sleep waits for d or until ctx is cancelled, reporting whether it slept fully.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
