package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"go.dalton.dog/spruce/internal/core"
)

// snapSocket is snapd's control socket. We speak its REST API directly rather
// than shelling out, which gives structured task progress for free.
const snapSocket = "/run/snapd.socket"

// Snap talks to snapd over its unix socket.
type Snap struct{}

func (Snap) Name() string { return "snap" }

func (Snap) Available() bool {
	_, err := os.Stat(snapSocket)
	return err == nil
}

// snapClient is an HTTP client whose transport dials the snapd unix socket.
// The host in the URL is ignored by snapd; we use a placeholder.
func snapClient() *http.Client {
	return &http.Client{
		Timeout: 0, // long-running refresh changes; we poll instead
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", snapSocket)
			},
		},
	}
}

type snapResp struct {
	Type       string          `json:"type"`
	StatusCode int             `json:"status-code"`
	Change     string          `json:"change"`
	Result     json.RawMessage `json:"result"`
}

func snapGet(ctx context.Context, path string, out *snapResp) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://snapd"+path, nil)
	if err != nil {
		return err
	}
	resp, err := snapClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(out)
}

func (Snap) Check(ctx context.Context) ([]core.Update, error) {
	var r snapResp
	if err := snapGet(ctx, "/v2/find?select=refresh", &r); err != nil {
		return nil, err
	}
	var list []struct {
		Name    string `json:"name"`
		Version string `json:"version"`
		Channel string `json:"channel"`
	}
	if err := json.Unmarshal(r.Result, &list); err != nil {
		return nil, err
	}
	var ups []core.Update
	for _, s := range list {
		ups = append(ups, core.Update{
			Name:       s.Name,
			NewVersion: s.Version,
			Source:     "snap",
			Repo:       s.Channel,
			Kind:       "snap",
		})
	}
	return ups, nil
}

func (s Snap) Plan(ctx context.Context, selected []core.Update) (core.Plan, error) {
	return core.Plan{Backend: s.Name(), Selected: selected, NeedsRoot: true}, nil
}

func (s Snap) Apply(ctx context.Context, plan core.Plan) (<-chan core.ProgressEvent, error) {
	events := make(chan core.ProgressEvent, 64)

	names := make([]string, 0, len(plan.Selected))
	for _, u := range plan.Selected {
		names = append(names, u.Name)
	}

	go func() {
		defer close(events)

		// Kick off an async refresh of the selected snaps.
		body, _ := json.Marshal(map[string]any{"action": "refresh", "snaps": names})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			"http://snapd/v2/snaps", bytes.NewReader(body))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
		}
		var r snapResp
		if err == nil {
			var resp *http.Response
			resp, err = snapClient().Do(req)
			if err == nil {
				defer resp.Body.Close()
				err = json.NewDecoder(resp.Body).Decode(&r)
			}
		}
		if err != nil {
			events <- core.ProgressEvent{Kind: core.EventError, Source: "snap", Text: err.Error()}
			events <- core.ProgressEvent{Kind: core.EventDone, Source: "snap"}
			return
		}
		if r.Change == "" {
			events <- core.ProgressEvent{Kind: core.EventError, Source: "snap",
				Text: "snapd did not return a change id (need root/authorization?)"}
			events <- core.ProgressEvent{Kind: core.EventDone, Source: "snap"}
			return
		}

		s.pollChange(ctx, events, r.Change)
		events <- core.ProgressEvent{Kind: core.EventDone, Source: "snap", OK: true}
	}()

	return events, nil
}

// pollChange follows a snapd change to completion, translating task progress
// into events.
func (Snap) pollChange(ctx context.Context, events chan<- core.ProgressEvent, changeID string) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	lastSummary := ""

	for {
		select {
		case <-ctx.Done():
			events <- core.ProgressEvent{Kind: core.EventError, Source: "snap", Text: "cancelled"}
			return
		case <-ticker.C:
		}

		var r snapResp
		if err := snapGet(ctx, "/v2/changes/"+changeID, &r); err != nil {
			events <- core.ProgressEvent{Kind: core.EventError, Source: "snap", Text: err.Error()}
			return
		}
		var ch struct {
			Status string `json:"status"`
			Tasks  []struct {
				Summary  string `json:"summary"`
				Status   string `json:"status"`
				Progress struct {
					Done  int64 `json:"done"`
					Total int64 `json:"total"`
				} `json:"progress"`
			} `json:"tasks"`
		}
		if err := json.Unmarshal(r.Result, &ch); err != nil {
			events <- core.ProgressEvent{Kind: core.EventError, Source: "snap", Text: err.Error()}
			return
		}

		for _, t := range ch.Tasks {
			if t.Status == "Doing" && t.Summary != lastSummary {
				lastSummary = t.Summary
				events <- core.ProgressEvent{Kind: core.EventPhase, Source: "snap", Phase: t.Summary}
			}
			if t.Progress.Total > 0 {
				events <- core.ProgressEvent{Kind: core.EventProgress, Source: "snap",
					Fraction: float64(t.Progress.Done) / float64(t.Progress.Total)}
			}
		}

		switch ch.Status {
		case "Done":
			events <- core.ProgressEvent{Kind: core.EventItemDone, Source: "snap", OK: true}
			return
		case "Error", "Undone", "Hold", "Abort":
			events <- core.ProgressEvent{Kind: core.EventError, Source: "snap",
				Text: fmt.Sprintf("snap refresh %s", ch.Status)}
			return
		}
	}
}
