package desktop

import (
	"context"
	"reflect"
	"time"

	"github.com/digimbyte/photobyte/internal/document"
	"github.com/digimbyte/photobyte/internal/tile"
)

type tileMaintenance struct {
	done  chan error
	next  time.Time
	usage int64
	force bool
}

func (u *window) maintainTiles(now time.Time) {
	m := &u.tileMaintenance
	if m.done != nil {
		select {
		case err := <-m.done:
			m.done = nil
			m.usage, _ = u.store.SpoolUsage()
			if err != nil && u.ctx.Err() == nil {
				u.setMessage("", err)
			}
		default:
			return
		}
	}
	// The I/O, save and edit lanes can be between producing tiles and
	// publishing a snapshot. Capture roots only after those jobs return.
	if u.stopJob != nil || u.stopEdit != nil || u.stopSave != nil || u.canvas.gesture != nil {
		return
	}
	if !m.force && now.Before(m.next) {
		return
	}
	m.next = now.Add(10 * time.Second)
	usage, pressure := u.store.SpoolUsage()
	if !m.force && !pressure && usage < m.usage+(32<<20) {
		return
	}
	m.force = false
	epoch := u.store.BeginSweep()
	roots := u.canvas.pinnedStates()
	for _, t := range u.tabs {
		for _, entry := range t.editor.Snapshot().History {
			roots = append(roots, entry.State)
		}
	}
	for _, s := range u.journal.PendingSnapshots() {
		for _, entry := range s.History {
			roots = append(roots, entry.State)
		}
	}
	done := make(chan error, 1)
	m.done = done
	u.workers.Go(func() {
		hashes, err := collectTileRoots(u.ctx, roots)
		if err == nil {
			err = u.store.Sweep(u.ctx, epoch, hashes)
		}
		done <- err
		if u.ctx.Err() == nil {
			u.win.Invalidate()
		}
	})
}

func collectTileRoots(ctx context.Context, roots []*document.State) (map[tile.Hash]struct{}, error) {
	hashes := make(map[tile.Hash]struct{})
	grids := make(map[uintptr]bool)
	for _, state := range roots {
		err := state.VisitGrids(func(grid tile.Grid) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			key := reflect.ValueOf(grid.Blocks).Pointer()
			if key == 0 || grids[key] {
				return nil
			}
			grids[key] = true
			for _, hash := range grid.Blocks {
				hashes[hash] = struct{}{}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return hashes, nil
}
