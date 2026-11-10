package tile

import (
	"context"
	"errors"
	"maps"
	"os"
	"slices"
	"sync"
)

const spoolLimit = 8 << 30

var errSpoolFull = errors.New("tile scratch budget is full; close an unused project to free space")

type spoolFile struct {
	size  int64
	epoch uint64
}

type pendingTile struct {
	data  []byte
	epoch uint64
}

type tileSpool struct {
	mu           sync.Mutex
	files        map[Hash]spoolFile
	pending      map[Hash]pendingTile
	used, limit  int64
	pendingBytes int
	epoch        uint64
}

// Include filesystem allocation and per-file overhead for tiny tiles.
func spoolCost(size int64) int64 { return (size+4095)/4096*4096 + 4096 }

func (s *Store) initSpool() error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	sp := &s.spool
	sp.limit = spoolLimit
	sp.files = make(map[Hash]spoolFile)
	sp.pending = make(map[Hash]pendingTile)
	for _, entry := range entries {
		var hash Hash
		if !entry.Type().IsRegular() || hash.UnmarshalText([]byte(entry.Name())) != nil || entry.Name() != hash.String() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		cost := spoolCost(info.Size())
		sp.files[hash] = spoolFile{size: cost}
		sp.used += cost
	}
	return nil
}

func (s *Store) touch(hash Hash) bool {
	sp := &s.spool
	sp.mu.Lock()
	defer sp.mu.Unlock()
	return sp.touch(hash)
}

func (sp *tileSpool) touch(hash Hash) bool {
	if file, ok := sp.files[hash]; ok {
		file.epoch = sp.epoch
		sp.files[hash] = file
		return true
	}
	if tile, ok := sp.pending[hash]; ok {
		tile.epoch = sp.epoch
		sp.pending[hash] = tile
		return true
	}
	return false
}

func (s *Store) write(hash Hash, data []byte) error {
	sp := &s.spool
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if sp.touch(hash) {
		return nil
	}
	err := s.writeSpoolFile(hash, data)
	if err == nil || (!isStoragePressure(err) && !errors.Is(err, errSpoolFull)) {
		return err
	}
	if sp.pendingBytes+len(data) > pendingLimit {
		return errors.Join(ErrPendingFull, err)
	}
	sp.pending[hash] = pendingTile{data: data, epoch: sp.epoch}
	sp.pendingBytes += len(data)
	return nil
}

// The spool mutex accounts for the staged file before it can be published.
func (s *Store) writeSpoolFile(hash Hash, data []byte) error {
	sp := &s.spool
	cost := spoolCost(int64(len(data)))
	if cost > sp.limit-sp.used {
		return errSpoolFull
	}
	if err := s.writeDisk(hash, data); err != nil {
		return err
	}
	sp.files[hash] = spoolFile{size: cost, epoch: sp.epoch}
	sp.used += cost
	return nil
}

// FlushPending runs after a durable checkpoint, when writing scratch can no
// longer take space away from that checkpoint.
func (s *Store) FlushPending(ctx context.Context) {
	sp := &s.spool
	sp.mu.Lock()
	keys := slices.Collect(maps.Keys(sp.pending))
	sp.mu.Unlock()
	for _, hash := range keys {
		if ctx.Err() != nil {
			return
		}
		sp.mu.Lock()
		tile, ok := sp.pending[hash]
		var err error
		if ok {
			err = s.writeSpoolFile(hash, tile.data)
			if err == nil {
				delete(sp.pending, hash)
				sp.pendingBytes -= len(tile.data)
			}
		}
		sp.mu.Unlock()
		if err != nil {
			return
		}
	}
}

func (s *Store) SpoolUsage() (bytes int64, pressure bool) {
	sp := &s.spool
	sp.mu.Lock()
	defer sp.mu.Unlock()
	return sp.used, sp.pendingBytes > 0
}

// BeginSweep separates existing files from writes and deduplicated writes
// performed after the caller starts capturing its live snapshot roots.
func (s *Store) BeginSweep() uint64 {
	sp := &s.spool
	sp.mu.Lock()
	defer sp.mu.Unlock()
	sp.epoch++
	return sp.epoch
}

// Sweep keeps reachable hashes and anything written or reused since BeginSweep.
// The caller includes both current document histories and outstanding readers.
func (s *Store) Sweep(ctx context.Context, epoch uint64, live map[Hash]struct{}) error {
	sp := &s.spool
	sp.mu.Lock()
	keys := slices.Collect(maps.Keys(sp.files))
	keys = slices.AppendSeq(keys, maps.Keys(sp.pending))
	sp.mu.Unlock()
	var firstErr error
	for _, hash := range keys {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, keep := live[hash]; keep {
			continue
		}
		sp.mu.Lock()
		if file, ok := sp.files[hash]; ok && file.epoch < epoch {
			if err := os.Remove(s.path(hash)); err == nil || errors.Is(err, os.ErrNotExist) {
				delete(sp.files, hash)
				sp.used -= file.size
				s.evict(hash)
			} else if firstErr == nil {
				firstErr = err
			}
		}
		if tile, ok := sp.pending[hash]; ok && tile.epoch < epoch {
			delete(sp.pending, hash)
			sp.pendingBytes -= len(tile.data)
			s.evict(hash)
		}
		sp.mu.Unlock()
	}
	return firstErr
}

func (s *Store) evict(hash Hash) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry := s.cache[hash]; entry != nil {
		delete(s.cache, hash)
		s.lru.Remove(entry)
	}
}
