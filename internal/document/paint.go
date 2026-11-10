package document

import (
	"context"
	"errors"
	"image"
	"math"

	"github.com/digimbyte/photobyte/internal/tile"
)

type Point struct{ X, Y float64 }

type Stroke struct {
	Layer, Frame ID
	Points       []Point
	Radius       float64
	Color        tile.Pixel
	Erase        bool
	Mask         bool
	Shared       bool
	Selection    image.Rectangle
}

type Paint struct {
	stroke Stroke
	grid   tile.Grid
}

func (c Paint) label() string {
	if c.stroke.Erase {
		return "Erase"
	}
	return "Brush stroke"
}

func (c Paint) change(s *State) error {
	i := s.layerIndex(c.stroke.Layer)
	f := s.frameIndex(c.stroke.Frame)
	if i < 0 || f < 0 {
		return errors.New("the stroke target disappeared")
	}
	switch {
	case c.stroke.Mask:
		s.Layers[i].Mask = &c.grid
	case c.stroke.Shared:
		s.Layers[i].Image = c.grid
	default:
		cloneCels(&s.Frames[f])
		s.Frames[f].Cels[c.stroke.Layer] = c.grid
	}
	return nil
}

func PrepareStroke(ctx context.Context, store *tile.Store, s *State, stroke Stroke) (Paint, error) {
	if _, ok := s.Frame(stroke.Frame); !ok {
		return Paint{}, errors.New("the stroke frame is no longer available")
	}
	layer, ok := s.Layer(stroke.Layer)
	if !ok || (stroke.Mask && layer.Mask == nil) || (!stroke.Mask && layer.Kind != Raster) {
		return Paint{}, errors.New("select a raster layer or mask to paint")
	}
	if len(stroke.Points) == 0 || len(stroke.Points) > 16384 || !(stroke.Radius >= 0.5 && stroke.Radius <= 512) {
		return Paint{}, errors.New("invalid brush stroke")
	}
	grid := s.Grid(layer, stroke.Frame, stroke.Shared).Clone()
	if stroke.Mask {
		grid = layer.Mask.Clone()
	}
	inverse := layer.Transform.Inverse()
	rx, ry := stroke.Radius/math.Abs(layer.Transform.SX), stroke.Radius/math.Abs(layer.Transform.SY)
	selection := s.Bounds
	if !stroke.Selection.Empty() {
		selection = selection.Intersect(stroke.Selection)
	}
	buckets := make(map[tile.Coord][]Point)
	dabCount := 0
	add := func(p Point) error {
		if !finite(p.X) || !finite(p.Y) || math.Abs(p.X) > MaxSide*4 || math.Abs(p.Y) > MaxSide*4 {
			return errors.New("brush path is outside the supported canvas")
		}
		x, y := inverse.At(p.X, p.Y)
		a := tile.At(int(math.Floor(x-rx)), int(math.Floor(y-ry)))
		b := tile.At(int(math.Ceil(x+rx)), int(math.Ceil(y+ry)))
		if a.X < -1024 || a.Y < -1024 || b.X > 1024 || b.Y > 1024 {
			return errors.New("the transformed brush exceeds the raster limit")
		}
		dabCount += (b.X - a.X + 1) * (b.Y - a.Y + 1)
		if dabCount > 262144 {
			return errors.New("brush stroke is too large; use shorter strokes")
		}
		for ty := a.Y; ty <= b.Y; ty++ {
			for tx := a.X; tx <= b.X; tx++ {
				key := tile.Coord{X: tx, Y: ty}
				buckets[key] = append(buckets[key], Point{x, y})
			}
		}
		return nil
	}
	for i, p := range stroke.Points {
		if err := ctx.Err(); err != nil {
			return Paint{}, err
		}
		if i == 0 {
			if err := add(p); err != nil {
				return Paint{}, err
			}
			continue
		}
		last := stroke.Points[i-1]
		steps := max(1, int(math.Ceil(math.Hypot(p.X-last.X, p.Y-last.Y)/max(0.5, stroke.Radius*0.4))))
		if steps > 262144 {
			return Paint{}, errors.New("brush path segment is too long")
		}
		for step := 1; step <= steps; step++ {
			t := float64(step) / float64(steps)
			if err := add(Point{last.X + (p.X-last.X)*t, last.Y + (p.Y-last.Y)*t}); err != nil {
				return Paint{}, err
			}
		}
	}
	sin, cos := math.Sincos(layer.Transform.Rotation * math.Pi / 180)
	for coord, dabs := range buckets {
		if err := ctx.Err(); err != nil {
			return Paint{}, err
		}
		block := new(tile.Block)
		if hash, found := grid.Blocks[coord]; found {
			source, err := store.Get(hash)
			if err != nil {
				return Paint{}, err
			}
			*block = *source
		} else if grid.Default != (tile.Pixel{}) {
			for i := range block {
				block[i] = grid.Default
			}
		}
		touched := false
		for _, p := range dabs {
			rect := image.Rect(int(math.Floor(p.X-rx)), int(math.Floor(p.Y-ry)),
				int(math.Ceil(p.X+rx)), int(math.Ceil(p.Y+ry))).Intersect(coord.Rect())
			for y := rect.Min.Y; y < rect.Max.Y; y++ {
				for x := rect.Min.X; x < rect.Max.X; x++ {
					dx, dy := (float64(x)+0.5-p.X)/rx, (float64(y)+0.5-p.Y)/ry
					if dx*dx+dy*dy > 1 {
						continue
					}
					lx := (float64(x) + 0.5) * layer.Transform.SX
					ly := (float64(y) + 0.5) * layer.Transform.SY
					wx := int(math.Floor(lx*cos - ly*sin + layer.Transform.X))
					wy := int(math.Floor(lx*sin + ly*cos + layer.Transform.Y))
					if !image.Pt(wx, wy).In(selection) {
						continue
					}
					i := tile.Index(x, y)
					switch {
					case stroke.Mask && stroke.Erase:
						block[i] = tile.Pixel{A: 65535}
					case stroke.Mask:
						block[i] = tile.Pixel{}
					case stroke.Erase:
						block[i] = tile.Pixel{}
					default:
						block[i] = stroke.Color
					}
					touched = true
				}
			}
		}
		if touched {
			hash, err := store.Put(block)
			if err != nil {
				return Paint{}, err
			}
			grid.Blocks[coord] = hash
		}
	}
	return Paint{stroke: stroke, grid: grid}, nil
}
