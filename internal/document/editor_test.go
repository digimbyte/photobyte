package document

import (
	"errors"
	"sync"
	"testing"
)

func TestConcurrentCommandsRejectStaleRevision(t *testing.T) {
	original := NewSnapshot(NewState(32, 32, SRGB), "Blank")
	editor := Open(original)
	defer editor.Close()
	var wg sync.WaitGroup
	results := make(chan error, 16)
	for range 16 {
		wg.Go(func() {
			results <- editor.Apply(t.Context(), original.Version, AddLayer{Kind: Raster, Name: "Brush layer"})
		})
	}
	wg.Wait()
	close(results)
	applied := 0
	for err := range results {
		if err == nil {
			applied++
		} else if !errors.Is(err, ErrStale) {
			t.Fatal(err)
		}
	}
	latest := editor.Snapshot()
	if applied != 1 || len(latest.State().Layers) != 2 || len(original.State().Layers) != 1 {
		t.Fatalf("%d commands applied; pinned and latest layer counts %d and %d",
			applied, len(original.State().Layers), len(latest.State().Layers))
	}
	if err := editor.History(t.Context(), latest.Version, 0); err != nil {
		t.Fatal(err)
	}
	if got := editor.Snapshot(); got.Version != latest.Version+1 || len(got.State().Layers) != 1 {
		t.Fatalf("unexpected undo snapshot: %+v", got)
	}
}
