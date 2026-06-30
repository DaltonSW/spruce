package tui

import (
	"testing"

	"go.dalton.dog/spruce/internal/core"
)

// A resolved single-package plan is memoized by Update.ID() so the install-one
// modal can be re-opened without re-running its (slow) brew dry-run.
func TestOnPlansResolvedCachesSingles(t *testing.T) {
	m := gridModel(map[string]int{"brew": 1})
	u := core.Update{Name: "acl", Source: "brew", Kind: "formula"}

	tm, _ := m.onPlansResolved(plansResolvedMsg{plans: map[string]core.Plan{
		"brew": {Backend: "brew", Selected: []core.Update{u}, Notes: []string{"also vim"}},
	}})
	m = tm.(Model)

	if _, ok := m.planCache[u.ID()]; !ok {
		t.Fatalf("single-package plan not cached under %q: %+v", u.ID(), m.planCache)
	}
}

// A multi-package plan must NOT populate the per-package cache — there is no
// single Update.ID() that describes it.
func TestOnPlansResolvedSkipsMultiPackage(t *testing.T) {
	m := gridModel(map[string]int{"brew": 2})
	ups := []core.Update{
		{Name: "acl", Source: "brew", Kind: "formula"},
		{Name: "vim", Source: "brew", Kind: "formula"},
	}

	tm, _ := m.onPlansResolved(plansResolvedMsg{plans: map[string]core.Plan{
		"brew": {Backend: "brew", Selected: ups},
	}})
	m = tm.(Model)

	if len(m.planCache) != 0 {
		t.Errorf("multi-package plan should not be cached: %+v", m.planCache)
	}
}

// A cached single package is served instantly: no spinner, no resolve command.
func TestStartPlanningCacheHit(t *testing.T) {
	m := gridModel(map[string]int{"brew": 1})
	u := core.Update{Name: "acl", Source: "brew", Kind: "formula"}
	cached := core.Plan{Backend: "brew", Selected: []core.Update{u}, Notes: []string{"also vim"}}
	m.planCache[u.ID()] = cached

	tm, cmd := m.startPlanning(map[string][]core.Update{"brew": {u}})
	m = tm.(Model)

	if m.planning {
		t.Error("cache hit should not enter the planning (spinner) state")
	}
	if cmd != nil {
		t.Error("cache hit should not issue a resolve command")
	}
	if got, ok := m.plans["brew"]; !ok || len(got.Notes) != 1 {
		t.Errorf("cache hit did not populate m.plans from the cache: %+v", m.plans)
	}
}

// An uncached package, and any multi-package selection, must still resolve in the
// background (planning + a command), never falsely hit the cache.
func TestStartPlanningCacheMiss(t *testing.T) {
	u := core.Update{Name: "acl", Source: "brew", Kind: "formula"}

	t.Run("uncached single", func(t *testing.T) {
		m := gridModel(map[string]int{"brew": 1})
		tm, cmd := m.startPlanning(map[string][]core.Update{"brew": {u}})
		m = tm.(Model)
		if !m.planning || cmd == nil {
			t.Errorf("uncached single should resolve: planning=%v cmd=%v", m.planning, cmd != nil)
		}
	})

	t.Run("cached but multi-package", func(t *testing.T) {
		m := gridModel(map[string]int{"brew": 2})
		v := core.Update{Name: "vim", Source: "brew", Kind: "formula"}
		m.planCache[u.ID()] = core.Plan{Selected: []core.Update{u}}
		m.planCache[v.ID()] = core.Plan{Selected: []core.Update{v}}
		tm, cmd := m.startPlanning(map[string][]core.Update{"brew": {u, v}})
		m = tm.(Model)
		if !m.planning || cmd == nil {
			t.Errorf("multi-package selection should resolve even if each item is cached")
		}
	})
}

// restartChecks must drop the cache so a fresh run can't reuse stale, just-applied
// version data.
func TestRestartChecksClearsPlanCache(t *testing.T) {
	m := gridModel(map[string]int{"brew": 1})
	m.discovered = []string{"brew"}
	u := core.Update{Name: "acl", Source: "brew", Kind: "formula"}
	m.planCache[u.ID()] = core.Plan{Selected: []core.Update{u}}

	tm, _ := m.restartChecks()
	m = tm.(Model)

	if len(m.planCache) != 0 {
		t.Errorf("restartChecks should clear planCache, got %+v", m.planCache)
	}
}
