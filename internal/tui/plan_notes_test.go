package tui

import (
	"strings"
	"testing"

	"go.dalton.dog/spruce/internal/core"
)

// The review modal must surface a backend's Plan notes (e.g. brew's pulled-in
// dependents) so the user sees them before committing.
func TestReviewModalShowsPlanNotes(t *testing.T) {
	m := gridModel(map[string]int{"brew": 2})
	m.state = stateReviewing
	m.plans = map[string]core.Plan{
		"brew": {Backend: "brew", Notes: []string{
			"brew will also upgrade 1 dependent package:",
			"vim 9.2.0700 -> 9.2.0750 (15.2MB)",
		}},
	}

	body := m.viewReviewing()
	for _, want := range []string{"also upgrade 1 dependent", "vim 9.2.0700 -> 9.2.0750"} {
		if !strings.Contains(body, want) {
			t.Errorf("review modal missing %q:\n%s", want, body)
		}
	}
}

// While plans are still resolving, the modal shows a busy line instead of notes.
func TestModalShowsResolvingWhilePlanning(t *testing.T) {
	m := gridModel(map[string]int{"brew": 2})
	m.state = stateReviewing
	m.planning = true

	if body := m.viewReviewing(); !strings.Contains(body, "resolving") {
		t.Errorf("expected a resolving indicator while planning:\n%s", body)
	}
}

// onPlansResolved derives the apply selection from each resolved plan's Selected
// set, and (when already parked in Applying) hands those plans to the apply run.
func TestPlansResolvedDerivesApplying(t *testing.T) {
	m := gridModel(map[string]int{"brew": 1})
	u := core.Update{Name: "acl", Source: "brew", Kind: "formula"}
	m.planning = true
	m.state = stateReviewing

	tm, _ := m.onPlansResolved(plansResolvedMsg{plans: map[string]core.Plan{
		"brew": {Backend: "brew", Selected: []core.Update{u}},
	}})
	m = tm.(Model)

	if m.planning {
		t.Error("planning should be cleared after plans resolve")
	}
	if got := m.applying["brew"]; len(got) != 1 || got[0].Name != "acl" {
		t.Errorf("applying not derived from plan.Selected: %+v", m.applying)
	}
}
