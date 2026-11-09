package desktop

import (
	"image"
	"image/color"
	"math"

	"digiphoto/internal/document"

	"gioui.org/f32"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/paint"
)

type brushPreviewKey struct {
	first  *document.Point
	count  int
	zoom   float32
	origin f32.Point
	size   image.Point
	width  float32
	ink    color.NRGBA
}

type brushPreview struct {
	key   brushPreviewKey
	image paint.ImageOp
	at    image.Point
}

func (p *brushPreview) draw(gtx layout.Context, t *tab, points []document.Point, width float32, ink color.NRGBA) bool {
	key := brushPreviewKey{
		first: &points[0], count: len(points), zoom: t.zoom, origin: t.origin,
		size: gtx.Constraints.Max, width: width, ink: ink,
	}
	if p.key != key {
		img, at, ok := rasterBrushPreview(points, key)
		if !ok {
			return false
		}
		*p = brushPreview{key: key, at: at}
		if img != nil {
			p.image = paint.NewImageOp(img)
		}
	}
	if p.image.Size() != (image.Point{}) {
		defer op.Offset(p.at).Push(gtx.Ops).Pop()
		p.image.Add(gtx.Ops)
		paint.PaintOp{}.Add(gtx.Ops)
	}
	return true
}

type previewSegment struct {
	start, direction f32.Point
	invLengthSquared float32
}

// Small gestures can be drawn as one texture, without a GPU stencil pass.
// Larger strokes retain Gio's path renderer to bound CPU work per frame.
func rasterBrushPreview(points []document.Point, key brushPreviewKey) (*image.RGBA, image.Point, bool) {
	const maxPoints = 32
	const maxWork = 160_000
	if len(points) > maxPoints {
		return nil, image.Point{}, false
	}
	var path [maxPoints]f32.Point
	lo := f32.Pt(float32(math.Inf(1)), float32(math.Inf(1)))
	hi := f32.Pt(-lo.X, -lo.Y)
	for i, pt := range points {
		p := f32.Pt(float32(pt.X), float32(pt.Y)).Mul(key.zoom).Add(key.origin)
		path[i] = p
		lo = f32.Pt(min(lo.X, p.X), min(lo.Y, p.Y))
		hi = f32.Pt(max(hi.X, p.X), max(hi.Y, p.Y))
	}
	radius := key.width / 2
	reach := radius + 0.5
	bounds := image.Rect(
		int(math.Floor(float64(lo.X-reach))), int(math.Floor(float64(lo.Y-reach))),
		int(math.Ceil(float64(hi.X+reach))), int(math.Ceil(float64(hi.Y+reach))),
	).Intersect(image.Rectangle{Max: key.size})
	if bounds.Empty() {
		return nil, image.Point{}, true
	}
	count := max(1, len(points)-1)
	if bounds.Dx()*bounds.Dy() > maxWork/count {
		return nil, image.Point{}, false
	}
	var segments [maxPoints - 1]previewSegment
	segments[0].start = path[0]
	for i := 1; i < len(points); i++ {
		delta := path[i].Sub(path[i-1])
		segments[i-1] = previewSegment{start: path[i-1], direction: delta}
		if length := delta.X*delta.X + delta.Y*delta.Y; length > 0 {
			segments[i-1].invLengthSquared = 1 / length
		}
	}
	img := image.NewRGBA(image.Rectangle{Max: bounds.Size()})
	for y := range bounds.Dy() {
		for x := range bounds.Dx() {
			pt := f32.Pt(float32(bounds.Min.X+x)+0.5, float32(bounds.Min.Y+y)+0.5)
			distance := reach * reach
			for _, segment := range segments[:count] {
				offset := pt.Sub(segment.start)
				progress := (offset.X*segment.direction.X + offset.Y*segment.direction.Y) * segment.invLengthSquared
				delta := offset.Sub(segment.direction.Mul(min(1, max(0, progress))))
				distance = min(distance, delta.X*delta.X+delta.Y*delta.Y)
			}
			coverage := min(1, max(0, reach-float32(math.Sqrt(float64(distance)))))
			a := uint16(float32(key.ink.A)*coverage + 0.5)
			img.SetRGBA(x, y, color.RGBA{
				R: uint8(uint16(key.ink.R) * a / 255),
				G: uint8(uint16(key.ink.G) * a / 255),
				B: uint8(uint16(key.ink.B) * a / 255),
				A: uint8(a),
			})
		}
	}
	return img, bounds.Min, true
}
