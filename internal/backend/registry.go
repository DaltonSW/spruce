package backend

import (
	"context"
	"sync"

	"go.dalton.dog/spruce/internal/core"
)

// all returns one instance of every known backend, in display order. New
// backends get added here and nowhere else.
func all() []core.Backend {
	return []core.Backend{
		PackageKit{},
		Brew{},
		Flatpak{},
		Snap{},
		Go{},
	}
}

// Available returns the backends that actually exist on this system.
func Available() []core.Backend {
	var out []core.Backend
	for _, b := range all() {
		if b.Available() {
			out = append(out, b)
		}
	}
	return out
}

// CheckResult pairs a backend with the outcome of its Check.
type CheckResult struct {
	Backend core.Backend
	Updates []core.Update
	Err     error
}

// CheckAll runs Check on every available backend concurrently and returns the
// results once all have finished. Order matches Available().
func CheckAll(ctx context.Context) []CheckResult {
	backends := Available()
	results := make([]CheckResult, len(backends))
	var wg sync.WaitGroup
	for i, b := range backends {
		wg.Add(1)
		go func(i int, b core.Backend) {
			defer wg.Done()
			ups, err := b.Check(ctx)
			results[i] = CheckResult{Backend: b, Updates: ups, Err: err}
		}(i, b)
	}
	wg.Wait()
	return results
}
