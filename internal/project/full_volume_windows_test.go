package project

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/digimbyte/photobyte/internal/document"
	"github.com/digimbyte/photobyte/internal/tile"

	"golang.org/x/sys/windows"
)

func TestRecoveryOnFullVolume(t *testing.T) {
	root := os.Getenv("PHOTOBYTE_FULL_VOLUME")
	expectedID := os.Getenv("PHOTOBYTE_FULL_VOLUME_ID")
	if root == "" && expectedID == "" {
		t.Skip("use scripts/test-full-volume.ps1 to mount a disposable test volume")
	}
	drive, err := windows.UTF16PtrFromString(root)
	if err != nil {
		t.Fatal(err)
	}
	var actual [128]uint16
	if err := windows.GetVolumeNameForVolumeMountPoint(drive, &actual[0], uint32(len(actual))); err != nil {
		t.Fatal(err)
	}
	var total, free uint64
	if err := windows.GetDiskFreeSpaceEx(drive, &free, &total, nil); err != nil {
		t.Fatal(err)
	}
	if expectedID == "" || !strings.EqualFold(windows.UTF16ToString(actual[:]), expectedID) || total < 32<<20 || total > 80<<20 {
		t.Fatal("refusing to fill a volume outside the verified disposable VHD")
	}
	ctx := t.Context()
	spool := filepath.Join(root, "spool")
	store, err := tile.NewStore(spool, tile.RawBytes)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := document.NewSnapshot(document.NewState(tile.Size, tile.Size, document.SRGB), "Before disk full")
	doc := document.Open(snapshot)
	defer doc.Close()
	path := filepath.Join(root, "saved.dphoto")
	if err := Save(ctx, path, store, snapshot, 0); err != nil {
		t.Fatal(err)
	}
	journal, err := NewJournal(filepath.Join(root, "recovery"), store, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	journal.Submit(snapshot, snapshot.Version, path)
	awaitJournal(t, journal, snapshot.Project, func(s Status) bool { return s.Version == snapshot.Version })

	fill, err := os.CreateTemp(root, "fill-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(fill.Name())
	defer fill.Close()
	chunk := make([]byte, 1<<20)
	var diskErr error
	for n := uint64(0); n < total+1<<20; {
		written, err := fill.Write(chunk)
		n += uint64(written)
		if err != nil {
			diskErr = err
			break
		}
		if err := windows.GetDiskFreeSpaceEx(drive, &free, nil, nil); err != nil {
			t.Fatal(err)
		}
		if free < uint64(len(chunk)) {
			chunk = chunk[:min(uint64(len(chunk)), max(free, 4096))]
		}
	}
	if !errors.Is(diskErr, windows.ERROR_DISK_FULL) && !errors.Is(diskErr, windows.ERROR_HANDLE_DISK_FULL) {
		t.Fatalf("expected a real Windows disk-full error, got %v", diskErr)
	}
	t.Logf("Windows denied a write to the filled disposable volume: %v", diskErr)
	fill.Close()

	block := new(tile.Block)
	seed := uint64(0x9e3779b97f4a7c15)
	for i := range block {
		seed ^= seed << 13
		seed ^= seed >> 7
		seed ^= seed << 17
		block[i] = tile.Pixel{R: uint16(seed), G: uint16(seed >> 16), B: uint16(seed >> 32), A: uint16(seed >> 48)}
	}
	hash, err := store.Put(block)
	if err != nil {
		t.Fatalf("the pending edit should fit in memory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(spool, hash.String())); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected the pending tile in memory while the spool is full: %v", err)
	}
	if err := doc.Apply(ctx, snapshot.Version, document.PlaceRaster{Name: "Pending pixels",
		Grid: tile.Grid{Blocks: map[tile.Coord]tile.Hash{{}: hash}}}); err != nil {
		t.Fatal(err)
	}
	dirty := doc.Snapshot()
	journal.Submit(dirty, snapshot.Version, path)
	status := awaitJournal(t, journal, dirty.Project, func(s Status) bool { return s.Err != nil })
	if status.Version != snapshot.Version {
		t.Fatalf("failed write advanced the recovery checkpoint: %+v", status)
	}
	if err := Save(ctx, path, store, dirty, snapshot.Version); err == nil {
		t.Fatal("manual save unexpectedly succeeded on the full disk")
	}
	if info, err := Inspect(ctx, path); err != nil || info.Version != snapshot.Version {
		t.Fatalf("full disk changed the committed project: %+v, %v", info, err)
	}

	host := t.TempDir()
	copyPath := filepath.Join(host, "copy.dphoto")
	if err := Save(ctx, copyPath, store, dirty, 0); err != nil {
		t.Fatalf("Save Copy to a volume with room: %v", err)
	}
	if err := os.Remove(fill.Name()); err != nil {
		t.Fatal(err)
	}
	journal.Submit(dirty, snapshot.Version, path)
	awaitJournal(t, journal, dirty.Project, func(s Status) bool { return s.Version == dirty.Version && s.Err == nil })
	readStore, err := tile.NewStore(filepath.Join(host, "verified"), tile.RawBytes)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{copyPath, filepath.Join(root, "recovery", dirty.Project+".dphoto")} {
		got, _, err := Load(ctx, candidate, readStore)
		if err != nil || got.Version != dirty.Version {
			t.Fatalf("failed to reopen the pending revision: %v", err)
		}
		actual, err := readStore.Get(hash)
		if err != nil || *actual != *block {
			t.Fatalf("recovery did not preserve pending pixels: %v", err)
		}
	}
}

func awaitJournal(t *testing.T, j *Journal, id string, ready func(Status) bool) Status {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		status := j.Status(id)
		if ready(status) {
			return status
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
			t.Fatalf("recovery status did not reach the expected state: %+v", status)
		}
	}
}
