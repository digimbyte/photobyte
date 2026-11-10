package project

import (
	"crypto/sha256"
	"path/filepath"
	"testing"

	"github.com/digimbyte/photobyte/internal/document"
	"github.com/digimbyte/photobyte/internal/tile"
)

func TestFailedSaveRetainsPreviousRevision(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	store, err := tile.NewStore(filepath.Join(dir, "tiles"), 0)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "image.dphoto")
	initial := document.NewSnapshot(document.NewState(16, 16, document.SRGB), "Blank")
	if err := Save(ctx, path, store, initial, 0); err != nil {
		t.Fatal(err)
	}
	changed := document.NewState(16, 16, document.SRGB)
	changed.Layers[0].Image = tile.Grid{Blocks: map[tile.Coord]tile.Hash{
		{}: sha256.Sum256([]byte("missing spool tile")),
	}}
	failed := initial
	failed.Version++
	failed.Cursor++
	failed.History = append(failed.History, document.Checkpoint{Label: "Paint", State: changed})
	if err := Save(ctx, path, store, failed, initial.Version); err == nil {
		t.Fatal("save accepted a missing tile")
	}
	actual, info, err := Load(ctx, path, store)
	if err != nil || actual.Version != initial.Version || info.Manual != initial.Version || len(actual.History) != 1 {
		t.Fatalf("prior project revision was not retained: %+v, %v", info, err)
	}
}
