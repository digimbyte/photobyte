package desktop

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"gioui.org/app"
	"github.com/digimbyte/photobyte/internal/document"
	"github.com/digimbyte/photobyte/internal/project"
	"github.com/digimbyte/photobyte/internal/tile"
)

func TestBrushQueueAppliesStrokesInOrderAfterBusyEdit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	dir := t.TempDir()
	store, err := tile.NewStore(filepath.Join(dir, "tiles"), 2*tile.RawBytes)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := project.NewJournal(filepath.Join(dir, "recovery"), store, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	s := document.NewSnapshot(document.NewState(32, 32, document.SRGB), "New project")
	target := &tab{editor: document.Open(s)}
	defer target.editor.Close()
	u := &window{win: new(app.Window), ctx: ctx, store: store, journal: journal, results: make(chan completion, 4)}
	defer func() {
		cancel()
		u.workers.Wait()
	}()
	unblock := make(chan struct{})
	u.runJob("Busy edit", editLane, func(ctx context.Context) completion {
		select {
		case <-unblock:
			return completion{}
		case <-ctx.Done():
			return completion{err: ctx.Err()}
		}
	})
	red := tile.Pixel{R: 65535, A: 65535}
	blue := tile.Pixel{B: 65535, A: 65535}
	for _, color := range []tile.Pixel{red, blue} {
		u.paint(target, document.Stroke{Layer: 1, Frame: 2, Points: []document.Point{{X: 10.5, Y: 10.5}},
			Radius: 3, Color: color, Shared: true})
	}
	if len(u.strokeQueue) != 2 || target.editor.Snapshot().Version != s.Version {
		t.Fatal("released strokes did not wait for the active edit")
	}
	close(unblock)
	for u.strokesPending(target) {
		if err := ctx.Err(); err != nil {
			t.Fatal(err)
		}
		u.receive()
		time.Sleep(time.Millisecond)
	}
	got := target.editor.Snapshot()
	if got.Version != s.Version+2 || len(got.History) != 3 {
		t.Fatalf("revision %d has %d checkpoints", got.Version, len(got.History))
	}
	for i, want := range []tile.Pixel{red, blue} {
		grid := got.History[i+1].State.Layers[0].Image
		block, err := store.Get(grid.Blocks[tile.At(10, 10)])
		if err != nil {
			t.Fatal(err)
		}
		if pixel := block[tile.Index(10, 10)]; pixel != want {
			t.Fatalf("checkpoint %d has pixel %v, want %v", i+1, pixel, want)
		}
	}
}
