package tile

import (
	"context"
	"errors"
	"os"
	"testing"
)

func TestSweepKeepsLiveAndRecentlyReusedTiles(t *testing.T) {
	s, err := NewStore(t.TempDir(), 4*RawBytes)
	if err != nil {
		t.Fatal(err)
	}
	put := func(v uint16) Hash {
		t.Helper()
		block := new(Block)
		block[0] = Pixel{R: v, A: 65535}
		hash, err := s.Put(block)
		if err != nil {
			t.Fatal(err)
		}
		return hash
	}
	live, reused, discarded := put(1), put(2), put(3)
	if _, err := s.Get(discarded); err != nil {
		t.Fatal(err)
	}
	before, _ := s.SpoolUsage()
	s.spool.limit = before
	epoch := s.BeginSweep()
	if put(2) != reused {
		t.Fatal("deduplicated write changed its hash")
	}
	pending := put(4)
	if _, pressure := s.SpoolUsage(); !pressure {
		t.Fatal("budget pressure did not use the emergency buffer")
	}
	ctx := context.Background()
	if err := s.Sweep(ctx, epoch, map[Hash]struct{}{live: {}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(discarded); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unreachable tile remains readable: %v", err)
	}
	s.FlushPending(ctx)
	if bytes, pressure := s.SpoolUsage(); bytes > before || pressure {
		t.Fatalf("scratch usage %d, pending %v after flushing", bytes, pressure)
	}
	for _, hash := range []Hash{live, reused, pending} {
		if _, err := s.Get(hash); err != nil {
			t.Fatalf("retained tile is unreadable: %v", err)
		}
	}
}
