package render

import (
	"container/list"
	"context"
	"image"
	"runtime"
	"sync"
)

type PreviewTile struct {
	Area  image.Rectangle
	Image *image.RGBA
}

type task struct {
	ctx   context.Context
	scene *Scene
	area  image.Rectangle
	step  int
	reply chan result
}

type result struct {
	tile PreviewTile
	err  error
}

type cached struct {
	key   [32]byte
	image *image.RGBA
}

type Pool struct {
	ctx    context.Context
	cancel context.CancelFunc
	tasks  chan task
	wg     sync.WaitGroup
	mu     sync.Mutex
	index  map[[32]byte]*list.Element
	lru    list.List
	bytes  int
	limit  int
}

func NewPool(limit int) *Pool {
	ctx, cancel := context.WithCancel(context.Background())
	workers := max(1, min(8, runtime.GOMAXPROCS(0)-2))
	p := &Pool{ctx: ctx, cancel: cancel, tasks: make(chan task, workers*2),
		index: make(map[[32]byte]*list.Element), limit: limit}
	for range workers {
		p.wg.Go(p.work)
	}
	return p
}

func (p *Pool) Close() { p.cancel(); p.wg.Wait() }

func (p *Pool) work() {
	for {
		select {
		case <-p.ctx.Done():
			return
		case t := <-p.tasks:
			if t.ctx.Err() != nil {
				continue
			}
			img, err := p.draw(t)
			select {
			case t.reply <- result{tile: PreviewTile{Area: t.area, Image: img}, err: err}:
			case <-t.ctx.Done():
			case <-p.ctx.Done():
			}
		}
	}
}

func (p *Pool) draw(t task) (*image.RGBA, error) {
	key := t.scene.key(t.area, t.step)
	p.mu.Lock()
	if item := p.index[key]; item != nil {
		p.lru.MoveToFront(item)
		img := item.Value.(cached).image
		p.mu.Unlock()
		return img, nil
	}
	p.mu.Unlock()
	w, h := (t.area.Dx()+t.step-1)/t.step, (t.area.Dy()+t.step-1)/t.step
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	s := t.scene.Sampler()
	for y := range h {
		if err := t.ctx.Err(); err != nil {
			return nil, err
		}
		for x := range w {
			px, py := t.area.Min.X+x*t.step, t.area.Min.Y+y*t.step
			value := s.Pixel(px, py)
			dst := img.Pix[y*img.Stride+x*4:]
			a := uint32(value.A)
			dst[0] = byte(((uint32(value.R) * a / 65535) + 128) / 257)
			dst[1] = byte(((uint32(value.G) * a / 65535) + 128) / 257)
			dst[2] = byte(((uint32(value.B) * a / 65535) + 128) / 257)
			dst[3] = byte((a + 128) / 257)
		}
		if s.Err() != nil {
			return nil, s.Err()
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if item := p.index[key]; item != nil {
		return item.Value.(cached).image, nil
	}
	p.index[key] = p.lru.PushFront(cached{key: key, image: img})
	p.bytes += len(img.Pix)
	for p.bytes > p.limit {
		item := p.lru.Back()
		v := item.Value.(cached)
		delete(p.index, v.key)
		p.lru.Remove(item)
		p.bytes -= len(v.image.Pix)
	}
	return img, nil
}

func (p *Pool) Preview(ctx context.Context, scene *Scene, region image.Rectangle, step int) ([]PreviewTile, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	step = max(1, step)
	chunk := step * 256
	region = region.Intersect(scene.bounds)
	if region.Empty() {
		return nil, nil
	}
	origin := region.Min.Sub(scene.bounds.Min)
	start := image.Pt(origin.X/chunk*chunk, origin.Y/chunk*chunk).Add(scene.bounds.Min)
	areas := make([]image.Rectangle, 0)
	for y := start.Y; y < region.Max.Y; y += chunk {
		for x := start.X; x < region.Max.X; x += chunk {
			areas = append(areas, image.Rect(x, y, x+chunk, y+chunk).Intersect(scene.bounds))
		}
	}
	reply := make(chan result, len(areas))
	for _, area := range areas {
		select {
		case p.tasks <- task{ctx: ctx, scene: scene, area: area, step: step, reply: reply}:
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-p.ctx.Done():
			return nil, context.Canceled
		}
	}
	out := make([]PreviewTile, 0, len(areas))
	for range areas {
		select {
		case r := <-reply:
			if r.err != nil {
				return nil, r.err
			}
			out = append(out, r.tile)
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-p.ctx.Done():
			return nil, context.Canceled
		}
	}
	return out, nil
}
