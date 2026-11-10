// Package render composites immutable document snapshots on the CPU.
package render

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"
	"sort"

	"github.com/digimbyte/photobyte/internal/document"
	"github.com/digimbyte/photobyte/internal/tile"
)

type node struct {
	layer    document.Layer
	inverse  document.Inverse
	grid     tile.Grid
	children []*node
}

type Scene struct {
	bounds image.Rectangle
	space  document.Space
	target document.Space
	nodes  []*node
	store  *tile.Store
}

func NewScene(store *tile.Store, s *document.State, frame document.ID, target document.Space, bypass document.ID) *Scene {
	if s.Space == document.Data {
		target = document.Data
	} else if target == document.Data || target == "" {
		target = s.Space
	}
	byParent := make(map[document.ID][]*node)
	byID := make(map[document.ID]*node)
	f, _ := s.Frame(frame)
	for _, layer := range s.Layers {
		if !layer.Visible || layer.Opacity == 0 || layer.ID == bypass {
			continue
		}
		n := &node{layer: layer, inverse: layer.Transform.Inverse(), grid: layer.Image}
		if g, ok := f.Cels[layer.ID]; ok {
			n.grid = g
		}
		byID[layer.ID] = n
		byParent[layer.Parent] = append(byParent[layer.Parent], n)
	}
	for id, n := range byID {
		n.children = byParent[id]
	}
	return &Scene{bounds: s.Bounds, space: s.Space, target: target, nodes: byParent[0], store: store}
}

func PosterScene(store *tile.Store, s *document.State, target document.Space) *Scene {
	if s.Poster == nil {
		return nil
	}
	copy := document.NewState(s.Bounds.Dx(), s.Bounds.Dy(), s.Space)
	copy.Bounds = s.Bounds
	copy.Layers[0].Image = *s.Poster
	return NewScene(store, copy, copy.Frames[0].ID, target, 0)
}

type blockRef struct {
	hash  tile.Hash
	block *tile.Block
}

type Sampler struct {
	scene *Scene
	cache map[*tile.Grid]blockRef
	err   error
}

func (s *Scene) Sampler() *Sampler { return &Sampler{scene: s, cache: make(map[*tile.Grid]blockRef)} }

func (s *Sampler) Err() error { return s.err }

func (s *Sampler) Bounds() image.Rectangle { return s.scene.bounds }

func (s *Sampler) ColorModel() color.Model { return color.NRGBA64Model }

func (s *Sampler) At(x, y int) color.Color { return s.Pixel(x, y) }

func (s *Sampler) DropTiles() { clear(s.cache) }

func (s *Sampler) sample(g *tile.Grid, x, y int) tile.Pixel {
	h, ok := g.Blocks[tile.At(x, y)]
	if !ok {
		return g.Default
	}
	ref := s.cache[g]
	if ref.block == nil || ref.hash != h {
		block, err := s.scene.store.Get(h)
		if err != nil {
			s.err = err
			return tile.Pixel{}
		}
		ref = blockRef{hash: h, block: block}
		s.cache[g] = ref
	}
	return ref.block[tile.Index(x, y)]
}

func (s *Sampler) composite(nodes []*node, x, y float64) rgba {
	var dst rgba
	for _, n := range nodes {
		l := n.layer
		lx, ly := n.inverse.At(x, y)
		ix, iy := int(math.Floor(lx)), int(math.Floor(ly))
		a := float32(l.Opacity)
		if l.Mask != nil {
			a *= float32(s.sample(l.Mask, ix, iy).A) / 65535
		}
		if a == 0 {
			continue
		}
		switch l.Kind {
		case document.Group:
			src := s.composite(n.children, x, y)
			dst = blend(dst, rgba{src.r * a, src.g * a, src.b * a, src.a * a}, l.Blend)
		case document.Adjustment:
			if s.scene.space != document.Data {
				dst = adjust(dst, l.Levels, a)
			}
		case document.Raster:
			src := premultiply(s.sample(&n.grid, ix, iy), s.scene.space == document.Data, a)
			dst = blend(dst, src, l.Blend)
		}
	}
	return dst
}

func (s *Sampler) Pixel(x, y int) tile.Pixel {
	scene := s.scene
	if s.err != nil || !image.Pt(x, y).In(scene.bounds) {
		return tile.Pixel{}
	}
	if len(scene.nodes) == 1 {
		n := scene.nodes[0]
		if n.layer.Kind == document.Raster && n.layer.Opacity == 1 && n.layer.Mask == nil && scene.space == scene.target {
			lx, ly := n.inverse.At(float64(x)+0.5, float64(y)+0.5)
			return s.sample(&n.grid, int(math.Floor(lx)), int(math.Floor(ly)))
		}
	}
	return straight(s.composite(scene.nodes, float64(x)+0.5, float64(y)+0.5), scene.space, scene.target)
}

func (s *Sampler) Resample(area image.Rectangle, x, y, scale int, nearest bool) tile.Pixel {
	if nearest || scale == 1 || s.scene.space == document.Data {
		return s.Pixel(area.Min.X+x/scale, area.Min.Y+y/scale)
	}
	fx := (float64(x)+0.5)/float64(scale) - 0.5
	fy := (float64(y)+0.5)/float64(scale) - 0.5
	ix, iy := int(math.Floor(fx)), int(math.Floor(fy))
	dx, dy := float32(fx-float64(ix)), float32(fy-float64(iy))
	var mixed rgba
	for j, wy := range []float32{1 - dy, dy} {
		for i, wx := range []float32{1 - dx, dx} {
			p := s.Pixel(area.Min.X+min(area.Dx()-1, max(0, ix+i)), area.Min.Y+min(area.Dy()-1, max(0, iy+j)))
			v := premultiply(p, false, wx*wy)
			mixed.r, mixed.g, mixed.b, mixed.a = mixed.r+v.r, mixed.g+v.g, mixed.b+v.b, mixed.a+v.a
		}
	}
	return straight(mixed, s.scene.target, s.scene.target)
}

// Key includes only the source and mask tile hashes that can affect this
// region. A stroke can reuse cached composites outside its changed tiles.
func (s *Scene) key(area image.Rectangle, step int) [32]byte {
	h := sha256.New()
	fmt.Fprintf(h, "%v/%v/%d/%s/%s", s.bounds, area, step, s.space, s.target)
	var visit func([]*node)
	visit = func(nodes []*node) {
		for _, n := range nodes {
			l := n.layer
			fmt.Fprintf(h, "/%d:%d:%s:%s:%v:%v:%v", l.ID, l.Parent, l.Kind, l.Blend, l.Opacity, l.Transform, l.Levels)
			loX, loY := math.Inf(1), math.Inf(1)
			hiX, hiY := math.Inf(-1), math.Inf(-1)
			for _, x := range []int{area.Min.X, area.Max.X} {
				for _, y := range []int{area.Min.Y, area.Max.Y} {
					tx, ty := n.inverse.At(float64(x), float64(y))
					loX, loY = min(loX, tx), min(loY, ty)
					hiX, hiY = max(hiX, tx), max(hiY, ty)
				}
			}
			from := tile.At(int(math.Floor(loX)), int(math.Floor(loY)))
			to := tile.At(int(math.Floor(hiX)), int(math.Floor(hiY)))
			for _, g := range []*tile.Grid{&n.grid, l.Mask} {
				if g == nil || len(g.Blocks) == 0 {
					if g == nil {
						fmt.Fprint(h, "/none")
					} else {
						fmt.Fprintf(h, "/%v", g.Default)
					}
					continue
				}
				fmt.Fprintf(h, "/%v", g.Default)
				write := func(c tile.Coord, hash tile.Hash) {
					fmt.Fprintf(h, "/%d,%d:", c.X, c.Y)
					h.Write(hash[:])
				}
				if (to.X-from.X+1)*(to.Y-from.Y+1) <= len(g.Blocks) {
					for y := from.Y; y <= to.Y; y++ {
						for x := from.X; x <= to.X; x++ {
							c := tile.Coord{X: x, Y: y}
							if hash, found := g.Blocks[c]; found {
								write(c, hash)
							}
						}
					}
				} else {
					coords := make([]tile.Coord, 0, min(64, len(g.Blocks)))
					for c := range g.Blocks {
						if c.X >= from.X && c.X <= to.X && c.Y >= from.Y && c.Y <= to.Y {
							coords = append(coords, c)
						}
					}
					sort.Slice(coords, func(i, j int) bool {
						a, b := coords[i], coords[j]
						return a.Y < b.Y || (a.Y == b.Y && a.X < b.X)
					})
					for _, c := range coords {
						write(c, g.Blocks[c])
					}
				}
			}
			visit(n.children)
		}
	}
	visit(s.nodes)
	var key [32]byte
	copy(key[:], h.Sum(nil))
	return key
}

func AutoLevels(ctx context.Context, scene *Scene) (document.Levels, error) {
	if scene.space == document.Data {
		return document.Levels{}, errors.New("Auto Levels is available for color projects")
	}
	const bins = 4096
	var hist [bins]uint64
	var total uint64
	b := scene.bounds
	s := scene.Sampler()
	weights := [3]float32{0.2126, 0.7152, 0.0722}
	if scene.space == document.P3 {
		weights = [3]float32{0.228975, 0.691739, 0.079287}
	}
	for y := b.Min.Y; y < b.Max.Y; y++ {
		if err := ctx.Err(); err != nil {
			return document.Levels{}, err
		}
		for x := b.Min.X; x < b.Max.X; x++ {
			p := s.Pixel(x, y)
			if p.A == 0 {
				continue
			}
			l := linear[p.R]*weights[0] + linear[p.G]*weights[1] + linear[p.B]*weights[2]
			i := int(unit(l)*(bins-1) + 0.5)
			hist[i] += uint64(p.A)
			total += uint64(p.A)
		}
		if s.Err() != nil {
			return document.Levels{}, s.Err()
		}
	}
	if total == 0 {
		return document.NeutralLevels(), nil
	}
	cut := max(1, (total+199)/200)
	var count uint64
	lo, hi := 0, bins-1
	for ; lo < bins-1; lo++ {
		count += hist[lo]
		if count >= cut {
			break
		}
	}
	count = 0
	for ; hi > 0; hi-- {
		count += hist[hi]
		if count >= cut {
			break
		}
	}
	if hi <= lo {
		return document.NeutralLevels(), nil
	}
	return document.Levels{Black: float64(lo) / (bins - 1), White: float64(hi) / (bins - 1), Gamma: 1}, nil
}
