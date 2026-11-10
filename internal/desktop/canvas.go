package desktop

import (
	"context"
	"image"
	"image/color"
	"math"
	"slices"
	"sync"

	"github.com/digimbyte/photobyte/internal/document"
	"github.com/digimbyte/photobyte/internal/render"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
)

type previewKey struct {
	project string
	version uint64
	frame   document.ID
	bypass  document.ID
	region  image.Rectangle
	step    int
	onion   bool
}

type previewRequest struct {
	key   previewKey
	state *document.State
	ctx   context.Context
}

type previewFrame struct {
	key       previewKey
	tiles     []render.PreviewTile
	neighbors []render.PreviewTile
	opaque    bool
	err       error
}

type gesture struct {
	tool   string
	first  f32.Point
	last   f32.Point
	origin f32.Point
	stroke document.Stroke
}

type viewport struct {
	u              *window
	requests       chan previewRequest
	ready          chan previewFrame
	finished       chan struct{}
	cancel         context.CancelFunc
	cancelJob      context.CancelFunc
	requested      previewKey
	front          previewFrame
	images         map[*image.RGBA]paint.ImageOp
	gesture        *gesture
	size           image.Point
	checkerCell    int
	checkerTexture paint.ImageOp
	strips         previewStrips // owned by the preview worker
	brushPreview   brushPreview
	pinMu          sync.Mutex
	pins           map[*document.State]int
}

func (v *viewport) start(u *window) {
	v.u = u
	v.requests, v.ready, v.finished = make(chan previewRequest, 1), make(chan previewFrame, 1), make(chan struct{})
	ctx, cancel := context.WithCancel(u.ctx)
	v.cancel = cancel
	go func() {
		defer close(v.finished)
		for {
			select {
			case <-ctx.Done():
				return
			case req := <-v.requests:
				f := v.render(req)
				v.pin(req.state, -1)
				select {
				case v.ready <- f:
					u.win.Invalidate()
				case <-req.ctx.Done():
				case <-ctx.Done():
					return
				}
			}
		}
	}()
}

func (v *viewport) close() { v.cancel(); <-v.finished }

func (v *viewport) pin(state *document.State, delta int) {
	v.pinMu.Lock()
	defer v.pinMu.Unlock()
	if n := v.pins[state] + delta; n > 0 {
		if v.pins == nil {
			v.pins = make(map[*document.State]int)
		}
		v.pins[state] = n
	} else {
		delete(v.pins, state)
	}
}

func (v *viewport) pinnedStates() []*document.State {
	v.pinMu.Lock()
	defer v.pinMu.Unlock()
	states := make([]*document.State, 0, len(v.pins))
	for state := range v.pins {
		states = append(states, state)
	}
	return states
}

func (v *viewport) render(req previewRequest) previewFrame {
	key := req.key
	s := req.state
	f := previewFrame{key: key}
	if key.onion {
		for i, frame := range s.Frames {
			if frame.ID != key.frame {
				continue
			}
			for _, n := range []int{i - 1, i + 1} {
				if n < 0 || n >= len(s.Frames) {
					continue
				}
				tiles, err := v.u.pool.Preview(req.ctx, render.NewScene(v.u.store, s, s.Frames[n].ID, document.SRGB, key.bypass), key.region, key.step)
				if err != nil {
					f.err = err
					return f
				}
				for _, t := range tiles {
					ghost := image.NewRGBA(t.Image.Bounds())
					for p := 0; p < len(ghost.Pix); p += 4 {
						alpha := t.Image.Pix[p+3] / 3
						if n < i {
							ghost.Pix[p], ghost.Pix[p+1] = alpha, alpha/3
						} else {
							ghost.Pix[p+1], ghost.Pix[p+2] = alpha*3/4, alpha
						}
						ghost.Pix[p+3] = alpha
					}
					f.neighbors = append(f.neighbors, render.PreviewTile{Area: t.Area, Image: ghost})
				}
			}
			break
		}
	}
	f.tiles, f.err = v.u.pool.Preview(req.ctx, render.NewScene(v.u.store, s, key.frame, document.SRGB, key.bypass), key.region, key.step)
	if f.err == nil {
		if key.step == 1 {
			f.tiles = v.strips.batch(f.tiles)
		} else {
			v.strips = nil
		}
		f.opaque = len(f.tiles) > 0
		for _, part := range f.tiles {
			if !part.Image.Opaque() {
				f.opaque = false
				break
			}
		}
	}
	return f
}

func (v *viewport) layout(gtx layout.Context, t *tab) layout.Dimensions {
	dims := layout.Dimensions{Size: gtx.Constraints.Max}
	v.size = dims.Size
	gtx.Constraints.Min = dims.Size
	bounds := clip.Rect{Max: dims.Size}.Push(gtx.Ops)
	defer bounds.Pop()
	backdrop(gtx, workspaceBG)
	if t == nil {
		return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return column(gtx, v.u.text("Start with an image", 24, false),
				layout.Spacer{Height: 8}.Layout, v.u.text("Open a file or create a new project.", 14, true))
		})
	}
	s := t.editor.Snapshot()
	state := s.State()
	if t.fit {
		b := state.Bounds
		w, h := max(1, dims.Size.X-gtx.Dp(36)), max(1, dims.Size.Y-gtx.Dp(36))
		t.zoom = min(1, float32(w)/float32(b.Dx()), float32(h)/float32(b.Dy()))
		t.origin = f32.Pt((float32(dims.Size.X)-float32(b.Dx())*t.zoom)/2-float32(b.Min.X)*t.zoom,
			(float32(dims.Size.Y)-float32(b.Dy())*t.zoom)/2-float32(b.Min.Y)*t.zoom)
		t.fit = false
	}
	event.Op(gtx.Ops, v)
	if v.u.form == nil {
		v.input(gtx, t)
	}
	canvas := screenRect(t, state.Bounds)
	visible := canvas.Intersect(image.Rectangle{Max: dims.Size})
	world := state.Bounds.Intersect(v.rect(t, f32.Point{}, f32.Pt(float32(dims.Size.X), float32(dims.Size.Y))))
	step := 1
	for t.zoom*float32(step) < 0.65 && step < 128 {
		step *= 2
	}
	// Snap requests to the same tile boundaries used by the compositor.
	// Small pan deltas then reuse the outstanding request and front buffer.
	if !world.Empty() {
		block := step * 256
		a, b := world.Min.Sub(state.Bounds.Min), world.Max.Sub(state.Bounds.Min)
		world = image.Rect(a.X/block*block, a.Y/block*block, (b.X+block-1)/block*block,
			(b.Y+block-1)/block*block).Add(state.Bounds.Min).Intersect(state.Bounds)
	}
	v.request(previewKey{project: s.Project, version: s.Version, frame: t.frame, bypass: t.bypass,
		region: world, step: step, onion: t.onion && v.u.playAt.IsZero()}, state)
	v.receive()
	if v.front.key.project != s.Project || !v.front.opaque || world.Empty() || !world.In(v.front.key.region) {
		v.checker(gtx, canvas, visible)
	}
	if v.front.key.project == s.Project {
		art := clip.Rect(visible).Push(gtx.Ops)
		v.drawTiles(gtx, t, v.front.neighbors)
		v.drawTiles(gtx, t, v.front.tiles)
		art.Pop()
	}
	if !t.selection.Empty() {
		strokeRect(gtx, screenRect(t, t.selection), accent)
	}
	v.drawGesture(gtx, t)
	return dims
}

func (v *viewport) request(key previewKey, state *document.State) {
	if key == v.requested {
		return
	}
	v.requested = key
	if v.cancelJob != nil {
		v.cancelJob()
	}
	ctx, cancel := context.WithCancel(v.u.ctx)
	v.cancelJob = cancel
	select {
	case old := <-v.requests:
		v.pin(old.state, -1)
	default:
	}
	v.pin(state, 1)
	v.requests <- previewRequest{key: key, state: state, ctx: ctx}
}

func (v *viewport) receive() {
	select {
	case f := <-v.ready:
		if f.key != v.requested {
			return
		}
		if f.err != nil {
			v.u.setMessage("", f.err)
			return
		}
		images := make(map[*image.RGBA]paint.ImageOp)
		for _, list := range [][]render.PreviewTile{f.neighbors, f.tiles} {
			for _, t := range list {
				img, ok := v.images[t.Image]
				if !ok {
					img = paint.NewImageOp(t.Image)
					img.Filter = paint.FilterNearest
				}
				images[t.Image] = img
			}
		}
		v.front, v.images = f, images
		v.u.finishedStrokes = slices.DeleteFunc(v.u.finishedStrokes, func(work *strokeWork) bool {
			return work.stroke.Frame == f.key.frame && work.version <= f.key.version &&
				work.tab.editor.Snapshot().Project == f.key.project
		})
	default:
	}
}

func (v *viewport) checker(gtx layout.Context, canvas, visible image.Rectangle) {
	if visible.Empty() {
		return
	}
	cell := max(12, gtx.Dp(18))
	period := cell * 2
	want := visible.Size().Add(image.Pt(period, period))
	current := v.checkerTexture.Size()
	if v.checkerCell != cell || current.X < want.X || current.Y < want.Y {
		img := image.NewRGBA(image.Rectangle{Max: want})
		var rows [2][]byte
		for i := range rows {
			rows[i] = make([]byte, img.Stride)
			for x := range want.X {
				shade := [4]byte{82, 87, 94, 255}
				if (x/cell+i)&1 != 0 {
					shade = [4]byte{111, 116, 122, 255}
				}
				copy(rows[i][x*4:], shade[:])
			}
		}
		for y := range want.Y {
			copy(img.Pix[y*img.Stride:], rows[y/cell&1])
		}
		v.checkerTexture = paint.NewImageOp(img)
		v.checkerTexture.Filter = paint.FilterNearest
		v.checkerCell = cell
	}
	phase := visible.Min.Sub(canvas.Min)
	offset := visible.Min.Sub(image.Pt(phase.X%period, phase.Y%period))
	area := clip.Rect(visible).Push(gtx.Ops)
	tr := op.Offset(offset).Push(gtx.Ops)
	v.checkerTexture.Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)
	tr.Pop()
	area.Pop()
}

func (v *viewport) drawTiles(gtx layout.Context, t *tab, tiles []render.PreviewTile) {
	for _, part := range tiles {
		img := v.images[part.Image]
		b := part.Image.Bounds()
		screen := screenRect(t, part.Area)
		if screen.Empty() {
			continue
		}
		w, h := float32(screen.Dx())/float32(b.Dx()), float32(screen.Dy())/float32(b.Dy())
		area := clip.Rect(screen).Push(gtx.Ops)
		tr := op.Affine(f32.Affine2D{}.Scale(f32.Point{}, f32.Pt(w, h)).Offset(f32.Pt(float32(screen.Min.X), float32(screen.Min.Y)))).Push(gtx.Ops)
		img.Add(gtx.Ops)
		paint.PaintOp{}.Add(gtx.Ops)
		tr.Pop()
		area.Pop()
	}
}

func screenRect(t *tab, r image.Rectangle) image.Rectangle {
	return image.Rect(
		int(math.Round(float64(t.origin.X+float32(r.Min.X)*t.zoom))),
		int(math.Round(float64(t.origin.Y+float32(r.Min.Y)*t.zoom))),
		int(math.Round(float64(t.origin.X+float32(r.Max.X)*t.zoom))),
		int(math.Round(float64(t.origin.Y+float32(r.Max.Y)*t.zoom))),
	)
}

func worldPoint(t *tab, p f32.Point) document.Point {
	return document.Point{X: float64((p.X - t.origin.X) / t.zoom), Y: float64((p.Y - t.origin.Y) / t.zoom)}
}

func (v *viewport) rect(t *tab, a, b f32.Point) image.Rectangle {
	p, q := worldPoint(t, a), worldPoint(t, b)
	return image.Rect(int(math.Floor(min(p.X, q.X))), int(math.Floor(min(p.Y, q.Y))),
		int(math.Ceil(max(p.X, q.X))), int(math.Ceil(max(p.Y, q.Y))))
}

func (v *viewport) input(gtx layout.Context, t *tab) {
	if v.u.tool == "Hand" {
		pointer.CursorGrab.Add(gtx.Ops)
	} else if v.u.tool != "Move" {
		pointer.CursorCrosshair.Add(gtx.Ops)
	}
	for {
		e, ok := gtx.Event(pointer.Filter{Target: v, Kinds: pointer.Press | pointer.Drag | pointer.Release | pointer.Cancel | pointer.Scroll,
			ScrollY: pointer.ScrollRange{Min: -4096, Max: 4096}})
		if !ok {
			break
		}
		v.handlePointer(gtx, t, e.(pointer.Event))
	}
}

func (v *viewport) handlePointer(gtx layout.Context, t *tab, p pointer.Event) {
	switch p.Kind {
	case pointer.Scroll:
		factor := float32(math.Exp(float64(-p.Scroll.Y) / 640))
		z := min(float32(64), max(float32(0.015625), t.zoom*factor))
		t.origin = p.Position.Sub(p.Position.Sub(t.origin).Mul(z / t.zoom))
		t.zoom = z
	case pointer.Press:
		if p.Source == pointer.Mouse && !p.Buttons.Contain(pointer.ButtonPrimary) && !p.Buttons.Contain(pointer.ButtonTertiary) {
			return
		}
		tool := v.u.tool
		if p.Buttons.Contain(pointer.ButtonTertiary) {
			tool = "Hand"
		}
		g := &gesture{tool: tool, first: p.Position, last: p.Position, origin: t.origin}
		if tool == "Brush" || tool == "Eraser" {
			if len(v.u.strokeQueue) >= maxQueuedStrokes {
				v.u.setMessage("", errStrokeQueueFull)
				return
			}
			s := t.editor.Snapshot().State()
			g.stroke = document.Stroke{Layer: t.layer, Frame: t.frame, Points: []document.Point{worldPoint(t, p.Position)},
				Radius: v.u.radius, Color: v.u.ink, Erase: tool == "Eraser", Mask: v.u.mask, Selection: t.selection,
				Shared: len(s.Frames) == 1 && len(s.Frames[0].Cels) == 0}
		}
		v.gesture = g
		gtx.Execute(pointer.GrabCmd{Tag: v, ID: p.PointerID})
	case pointer.Drag:
		if g := v.gesture; g != nil {
			g.last = p.Position
			if g.tool == "Hand" {
				t.origin = g.origin.Add(p.Position.Sub(g.first))
			} else if len(g.stroke.Points) > 0 && len(g.stroke.Points) < 16384 {
				g.stroke.Points = append(g.stroke.Points, worldPoint(t, p.Position))
			}
		}
	case pointer.Cancel:
		v.cancelGesture()
	case pointer.Release:
		if v.gesture != nil {
			v.finish(t, p.Position)
		}
	}
}

func (v *viewport) finish(t *tab, point f32.Point) {
	g := v.gesture
	v.gesture = nil
	s := t.editor.Snapshot().State()
	switch g.tool {
	case "Select":
		t.selection = v.rect(t, g.first, point).Intersect(s.Bounds)
	case "Crop":
		if r := v.rect(t, g.first, point); !r.Empty() && v.u.apply(t, document.Crop{Bounds: r}) {
			t.fit = true
		}
	case "Move":
		delta := point.Sub(g.first)
		if l, ok := s.Layer(t.layer); ok && l.Kind == document.Raster && (delta.X != 0 || delta.Y != 0) {
			l.Transform.X += float64(delta.X / t.zoom)
			l.Transform.Y += float64(delta.Y / t.zoom)
			v.u.apply(t, document.LayerProperties{Layer: l})
		}
	case "Brush", "Eraser":
		if points := g.stroke.Points; point != g.last && len(points) < 16384 {
			g.stroke.Points = append(points, worldPoint(t, point))
		}
		v.u.paint(t, g.stroke)
	}
}

func (v *viewport) cancelGesture() {
	v.gesture = nil
}

func strokeRect(gtx layout.Context, r image.Rectangle, c color.NRGBA) {
	if r.Empty() {
		return
	}
	for _, line := range []image.Rectangle{
		image.Rect(r.Min.X, r.Min.Y, r.Max.X, r.Min.Y+1),
		image.Rect(r.Min.X, r.Max.Y-1, r.Max.X, r.Max.Y),
		image.Rect(r.Min.X, r.Min.Y, r.Min.X+1, r.Max.Y),
		image.Rect(r.Max.X-1, r.Min.Y, r.Max.X, r.Max.Y),
	} {
		paint.FillShape(gtx.Ops, c, clip.Rect(line).Op())
	}
}

func (v *viewport) drawGesture(gtx layout.Context, t *tab) {
	draw := func(work *strokeWork) {
		if work != nil && work.tab == t && work.stroke.Frame == t.frame {
			drawStroke(gtx, t, work.stroke, &work.preview)
		}
	}
	for _, work := range v.u.finishedStrokes {
		draw(work)
	}
	draw(v.u.activeStroke)
	for _, work := range v.u.strokeQueue {
		draw(work)
	}
	if g := v.gesture; g != nil {
		switch g.tool {
		case "Select", "Crop":
			strokeRect(gtx, screenRect(t, v.rect(t, g.first, g.last)), textPrimary)
		case "Brush", "Eraser":
			if g.stroke.Frame == t.frame {
				drawStroke(gtx, t, g.stroke, &v.brushPreview)
			}
		}
	}
}

func drawStroke(gtx layout.Context, t *tab, stroke document.Stroke, preview *brushPreview) {
	points := stroke.Points
	if len(points) == 0 {
		return
	}
	toScreen := func(p document.Point) f32.Point {
		return f32.Pt(float32(p.X)*t.zoom+t.origin.X, float32(p.Y)*t.zoom+t.origin.Y)
	}
	c := color.NRGBA{R: byte(stroke.Color.R >> 8), G: byte(stroke.Color.G >> 8), B: byte(stroke.Color.B >> 8), A: 215}
	if stroke.Mask || stroke.Erase {
		c = color.NRGBA{R: 168, G: 192, B: 221, A: 150}
	}
	width := max(1, float32(stroke.Radius*2)*t.zoom)
	if preview.draw(gtx, t, points, width, c) {
		return
	}
	var path clip.Path
	path.Begin(gtx.Ops)
	first := toScreen(points[0])
	path.MoveTo(first)
	for _, p := range points[1:] {
		path.LineTo(toScreen(p))
	}
	if len(points) > 1 {
		paint.FillShape(gtx.Ops, c, clip.Stroke{Path: path.End(), Width: width}.Op())
	} else {
		path.End()
		r := int(math.Ceil(float64(width / 2)))
		x, y := int(first.X), int(first.Y)
		paint.FillShape(gtx.Ops, c, clip.Ellipse(image.Rect(x-r, y-r, x+r, y+r)).Op(gtx.Ops))
	}
}
