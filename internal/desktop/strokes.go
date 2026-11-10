package desktop

import (
	"context"
	"errors"
	"maps"
	"slices"

	"github.com/digimbyte/photobyte/internal/document"
)

const maxQueuedStrokes = 16

var errStrokeQueueFull = errors.New("brush queue is full; wait for the pending strokes to finish")

type strokeWork struct {
	tab     *tab
	stroke  document.Stroke
	preview brushPreview
	version uint64
}

func (u *window) paint(t *tab, stroke document.Stroke) {
	if len(u.strokeQueue) >= maxQueuedStrokes {
		u.setMessage("", errStrokeQueueFull)
		return
	}
	u.strokeQueue = append(u.strokeQueue, &strokeWork{tab: t, stroke: stroke})
	u.startStroke()
}

func (u *window) startStroke() {
	if u.stopEdit != nil || len(u.strokeQueue) == 0 || u.ctx.Err() != nil {
		return
	}
	work := u.strokeQueue[0]
	u.strokeQueue = slices.Delete(u.strokeQueue, 0, 1)
	u.activeStroke = work
	t := work.tab
	s := t.editor.Snapshot()
	u.runJob("Applying stroke", editLane, func(ctx context.Context) completion {
		cmd, err := document.PrepareStroke(ctx, u.store, s.State(), work.stroke)
		base := s.Version
		if err == nil {
			err = t.editor.Apply(ctx, base, cmd)
			if errors.Is(err, document.ErrStale) {
				latest := t.editor.Snapshot()
				if sameStrokeSource(s.State(), latest.State(), work.stroke) {
					base = latest.Version
					err = t.editor.Apply(ctx, base, cmd)
				}
			}
		}
		return completion{err: err, message: "Stroke applied", apply: func() {
			work.version = base + 1
			if len(u.finishedStrokes) >= maxQueuedStrokes {
				u.finishedStrokes = slices.Delete(u.finishedStrokes, 0, 1)
			}
			u.finishedStrokes = append(u.finishedStrokes, work)
			u.schedule(t)
		}}
	})
}

// A prepared raster can be reused after an unrelated edit only when it would
// read and replace the same pixels with the same coordinate mapping.
func sameStrokeSource(before, after *document.State, stroke document.Stroke) bool {
	a, foundA := before.Layer(stroke.Layer)
	b, foundB := after.Layer(stroke.Layer)
	_, frame := after.Frame(stroke.Frame)
	if !foundA || !foundB || !frame || before.Bounds != after.Bounds || a.Kind != b.Kind || a.Transform != b.Transform {
		return false
	}
	ga, gb := before.Grid(a, stroke.Frame, stroke.Shared), after.Grid(b, stroke.Frame, stroke.Shared)
	if stroke.Mask {
		if a.Mask == nil || b.Mask == nil {
			return false
		}
		ga, gb = *a.Mask, *b.Mask
	}
	return ga.Default == gb.Default && maps.Equal(ga.Blocks, gb.Blocks)
}

func (u *window) strokesPending(t *tab) bool {
	if u.activeStroke != nil && (t == nil || u.activeStroke.tab == t) {
		return true
	}
	for _, work := range u.strokeQueue {
		if t == nil || work.tab == t {
			return true
		}
	}
	return false
}

func (u *window) forgetStrokePreviews(t *tab) {
	u.finishedStrokes = slices.DeleteFunc(u.finishedStrokes, func(work *strokeWork) bool {
		return work.tab == t
	})
}
