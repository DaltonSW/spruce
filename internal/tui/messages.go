package tui

import (
	"context"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"go.dalton.dog/spruce/internal/backend"
	"go.dalton.dog/spruce/internal/core"
)

// tickCmd drives the spinner animation while discovering/applying.
func tickCmd() tea.Cmd {
	return tea.Tick(time.Second/10, func(time.Time) tea.Msg { return tickMsg{} })
}

// --- message types ---------------------------------------------------------

type discoveredMsg struct{ results []backend.CheckResult }
type applyReadyMsg struct{ ch <-chan core.ProgressEvent }
type applyEventMsg struct{ ev core.ProgressEvent }
type applyDoneMsg struct{}
type tickMsg struct{}

// --- commands --------------------------------------------------------------

// checkCmd runs discovery + Check across all available backends off the UI
// goroutine and reports the results.
func checkCmd(ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		return discoveredMsg{results: backend.CheckAll(ctx)}
	}
}

// waitForEvent blocks for the next aggregated progress event. Re-issued after
// each event to pull the following one — the standard Bubble Tea streaming idiom.
func waitForEvent(ch <-chan core.ProgressEvent) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return applyDoneMsg{}
		}
		return applyEventMsg{ev: ev}
	}
}

// startApplyCmd resolves a Plan per backend, starts Apply on each, and fans all
// their event channels into one aggregated channel. Plan/Apply may block, so
// this all runs inside the command goroutine, not the UI loop.
func startApplyCmd(ctx context.Context, sel map[string][]core.Update, byName map[string]core.Backend) tea.Cmd {
	return func() tea.Msg {
		agg := make(chan core.ProgressEvent, 128)
		var wg sync.WaitGroup

		for name, ups := range sel {
			b := byName[name]
			if b == nil || len(ups) == 0 {
				continue
			}
			plan, err := b.Plan(ctx, ups)
			if err != nil {
				agg <- core.ProgressEvent{Kind: core.EventError, Source: name, Text: err.Error()}
				continue
			}
			ch, err := b.Apply(ctx, plan)
			if err != nil {
				agg <- core.ProgressEvent{Kind: core.EventError, Source: name, Text: err.Error()}
				continue
			}
			wg.Add(1)
			go func(ch <-chan core.ProgressEvent) {
				defer wg.Done()
				for ev := range ch {
					agg <- ev
				}
			}(ch)
		}

		go func() { wg.Wait(); close(agg) }()
		return applyReadyMsg{ch: agg}
	}
}
