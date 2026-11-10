package project

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/digimbyte/photobyte/internal/document"
	"github.com/digimbyte/photobyte/internal/tile"
)

func TestLargeUndoHistoryAndLegacyProject(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := tile.NewStore(filepath.Join(dir, "tiles"), tile.RawBytes)
	if err != nil {
		t.Fatal(err)
	}
	block := new(tile.Block)
	block[0] = tile.Pixel{R: 65535, A: 65535}
	hash, err := store.Put(block)
	if err != nil {
		t.Fatal(err)
	}
	const side = 8192
	state := document.NewState(side, side, document.SRGB)
	grid := tile.Grid{Blocks: make(map[tile.Coord]tile.Hash)}
	for y := range side / tile.Size {
		for x := range side / tile.Size {
			grid.Blocks[tile.Coord{X: x, Y: y}] = hash
		}
	}
	state.Layers[0].Image = grid
	for range 7 {
		layer := document.NewLayer("Layer", document.Raster)
		layer.ID, layer.Image = state.NextID, grid
		state.NextID++
		state.Layers = append(state.Layers, layer)
	}
	mask := grid
	mask.Default = tile.Pixel{A: 65535}
	state.Layers[0].Mask = &mask
	state.Poster = &grid
	state.Frames[0].Cels = map[document.ID]tile.Grid{state.Layers[0].ID: grid}
	old := document.NewSnapshot(state, "Legacy")
	dest := filepath.Join(dir, "legacy.dphoto")
	writeLegacyFixture(t, dest, old, store, hash)

	loaded, _, err := Load(ctx, dest, store)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(old.History, loaded.History) {
		t.Fatal("legacy project changed while opening")
	}
	editor := document.Open(loaded)
	defer editor.Close()
	for i := range document.UndoLimit {
		s := editor.Snapshot()
		layer := s.State().Layers[0]
		layer.Opacity = 1 - float64(i+1)/200
		if err := editor.Apply(ctx, s.Version, document.LayerProperties{Layer: layer}); err != nil {
			t.Fatal(err)
		}
	}
	latest := editor.Snapshot()
	if err := Save(ctx, dest, store, latest, old.Version); err != nil {
		t.Fatal(err)
	}
	recovery := filepath.Join(dir, "recovery.dphoto")
	if err := RecoverTo(ctx, recovery, store, latest, old.Version, dest); err != nil {
		t.Fatal(err)
	}
	reopenedStore, err := tile.NewStore(filepath.Join(dir, "reopened"), tile.RawBytes)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{dest, recovery} {
		got, info, err := Load(ctx, path, reopenedStore)
		if err != nil {
			t.Fatal(err)
		}
		if info.Version != latest.Version || got.Cursor != latest.Cursor || !reflect.DeepEqual(got.History, latest.History) {
			t.Fatalf("%s did not retain all %d checkpoints", filepath.Base(path), len(latest.History))
		}
	}
}

func writeLegacyFixture(t *testing.T, path string, s document.Snapshot, store *tile.Store, hash tile.Hash) {
	t.Helper()
	ctx := context.Background()
	db, err := open(path, "rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, fmt.Sprintf("PRAGMA application_id=%d; PRAGMA user_version=%d; ", appID, schema)+ddl); err != nil {
		t.Fatal(err)
	}
	meta, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO project VALUES(1,?,?,?,?,?)", s.Project, s.Version, s.Version, "", meta); err != nil {
		t.Fatal(err)
	}
	compressed, err := store.Compressed(hash)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO tiles VALUES(?,?)", hash.String(), compressed); err != nil {
		t.Fatal(err)
	}
}
