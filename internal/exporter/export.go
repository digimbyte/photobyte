// Package exporter renders pinned document revisions to staged image assets.
package exporter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/digimbyte/photobyte/internal/document"
	"github.com/digimbyte/photobyte/internal/media"
	"github.com/digimbyte/photobyte/internal/publish"
	"github.com/digimbyte/photobyte/internal/render"
	"github.com/digimbyte/photobyte/internal/tile"
)

type Exporter struct {
	Store   *tile.Store
	Codec   media.Codec
	Scratch string
}

func (e Exporter) write(ctx context.Context, dest string, h media.Header, r document.Recipe,
	row func(int, int, []tile.Pixel) error) error {
	if err := h.Validate(); err != nil {
		return err
	}
	if _, err := os.Lstat(dest); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return fmt.Errorf("%s already exists", filepath.Base(dest))
		}
		return err
	}
	raw, err := os.CreateTemp(e.Scratch, "export-*")
	if err != nil {
		return err
	}
	defer os.Remove(raw.Name())
	defer raw.Close()
	if err := media.WriteRaster(ctx, raw, h, row); err != nil {
		return err
	}
	if err := raw.Close(); err != nil {
		return err
	}
	input, err := os.Open(raw.Name())
	if err != nil {
		return err
	}
	defer input.Close()
	return publish.File(ctx, dest, func(out *os.File) error {
		return e.Codec.Encode(ctx, input, out, r.Format, r.Depth)
	})
}

func (e Exporter) Image(ctx context.Context, dest string, s *document.State, frame document.ID, area image.Rectangle, r document.Recipe) error {
	if !r.Valid() {
		return errors.New("invalid export recipe")
	}
	if r.Space == document.Data && s.Space != document.Data {
		return errors.New("raw sample export requires a texture data project")
	}
	area = area.Intersect(s.Bounds)
	if area.Empty() {
		return errors.New("export area is outside the canvas")
	}
	h := media.Header{Width: area.Dx() * r.Scale, Height: area.Dy() * r.Scale, Space: target(s, r), Plays: s.Plays}
	var samples []*render.Sampler
	if r.Format == "apng" || r.Format == "gif" {
		if r.Format == "apng" && s.Poster != nil {
			h.Poster = true
			samples = append(samples, render.PosterScene(e.Store, s, h.Space).Sampler())
		}
		for _, f := range s.Frames {
			h.Times = append(h.Times, f.Time)
			samples = append(samples, render.NewScene(e.Store, s, f.ID, h.Space, 0).Sampler())
		}
	} else {
		f, ok := s.Frame(frame)
		if !ok {
			return errors.New("select a frame to export")
		}
		h.Times = []document.Delay{f.Time}
		samples = []*render.Sampler{render.NewScene(e.Store, s, frame, h.Space, 0).Sampler()}
	}
	if h.Space != document.SRGB && r.Format != "png" && r.Format != "apng" {
		return errors.New("choose PNG/APNG for Display P3 or texture samples")
	}
	return e.write(ctx, dest, h, r, func(frame, y int, dst []tile.Pixel) error {
		sample := samples[frame]
		for x := range dst {
			dst[x] = sample.Resample(area, x, y, r.Scale, r.Nearest)
		}
		if y == h.Height-1 {
			sample.DropTiles()
		}
		return sample.Err()
	})
}

func target(s *document.State, r document.Recipe) document.Space {
	if s.Space == document.Data {
		return document.Data
	}
	if r.Space == document.Data {
		return s.Space
	}
	return r.Space
}

func (e Exporter) Slices(ctx context.Context, dest string, s *document.State, frame document.ID, recipe document.Recipe) error {
	if len(s.Slices) == 0 {
		return errors.New("add at least one named slice in the Assets panel")
	}
	names := make(map[string]bool, len(s.Slices))
	for _, slice := range s.Slices {
		name := strings.ToLower(slice.Name)
		if !safeName(name) || names[name] || slice.Bounds.Intersect(s.Bounds).Empty() {
			return fmt.Errorf("invalid, duplicate or empty slice %q", slice.Name)
		}
		names[name] = true
	}
	return batch(ctx, dest, func(stage string) error {
		for _, slice := range s.Slices {
			name := slice.Name
			if recipe.Scale != 1 {
				name += fmt.Sprintf("@%dx", recipe.Scale)
			}
			if err := e.Image(ctx, filepath.Join(stage, name+"."+recipe.Format), s, frame, slice.Bounds, recipe); err != nil {
				return fmt.Errorf("slice %q: %w", slice.Name, err)
			}
		}
		return nil
	})
}

func safeName(s string) bool {
	if s == "" || s == "." || s == ".." || strings.TrimSpace(s) != s || strings.HasSuffix(s, ".") {
		return false
	}
	for _, c := range s {
		if c != ' ' && c != '-' && c != '_' && c != '.' && !unicode.IsLetter(c) && !unicode.IsNumber(c) {
			return false
		}
	}
	base, _, _ := strings.Cut(s, ".")
	switch base {
	case "con", "prn", "aux", "nul":
		return false
	}
	return !(len(base) == 4 && (strings.HasPrefix(base, "com") || strings.HasPrefix(base, "lpt")) && base[3] >= '0' && base[3] <= '9')
}

func batch(ctx context.Context, dest string, write func(string) error) error {
	if _, err := os.Lstat(dest); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return fmt.Errorf("output folder %s already exists", filepath.Base(dest))
		}
		return err
	}
	dir, err := os.MkdirTemp(filepath.Dir(dest), ".photobyte-export-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if err := write(dir); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return publish.New(dir, dest)
}

type AtlasEntry struct {
	ID       document.ID     `json:"id"`
	Name     string          `json:"name"`
	Tag      string          `json:"tag,omitempty"`
	Duration document.Delay  `json:"duration"`
	Rect     image.Rectangle `json:"rect"`
	Trim     image.Rectangle `json:"trim"`
	Pivot    image.Point     `json:"pivot"`
}

type Atlas struct {
	Version int          `json:"version"`
	Width   int          `json:"width"`
	Height  int          `json:"height"`
	Plays   uint32       `json:"plays"`
	Frames  []AtlasEntry `json:"frames"`
}

func (e Exporter) Atlas(ctx context.Context, dest string, s *document.State, recipe document.Recipe) error {
	if !recipe.Valid() {
		return errors.New("invalid export recipe")
	}
	if recipe.Space == document.Data && s.Space != document.Data {
		return errors.New("raw sample export requires a texture data project")
	}
	recipe.Format = "png"
	space := target(s, recipe)
	atlas := Atlas{Version: 1, Plays: s.Plays, Frames: make([]AtlasEntry, len(s.Frames))}
	samplers := make([]*render.Sampler, len(s.Frames))
	for i, f := range s.Frames {
		sample := render.NewScene(e.Store, s, f.ID, space, 0).Sampler()
		samplers[i] = sample
		trim := s.Bounds
		if recipe.Trim {
			trim = image.Rectangle{}
			for y := s.Bounds.Min.Y; y < s.Bounds.Max.Y; y++ {
				if err := ctx.Err(); err != nil {
					return err
				}
				for x := s.Bounds.Min.X; x < s.Bounds.Max.X; x++ {
					if sample.Pixel(x, y).A > 0 {
						trim = trim.Union(image.Rect(x, y, x+1, y+1))
					}
				}
				if sample.Err() != nil {
					return sample.Err()
				}
			}
			if trim.Empty() {
				p := s.Bounds.Min
				trim = image.Rect(p.X, p.Y, p.X+1, p.Y+1)
			}
		}
		atlas.Frames[i] = AtlasEntry{ID: f.ID, Name: f.Name, Tag: f.Tag, Duration: f.Time,
			Trim: trim.Sub(s.Bounds.Min), Pivot: image.Pt(s.Bounds.Dx()/2, s.Bounds.Dy()/2)}
		sample.DropTiles()
	}
	if err := pack(&atlas, recipe); err != nil {
		return err
	}
	h := media.Header{Width: atlas.Width, Height: atlas.Height, Space: space, Times: []document.Delay{{}}, Plays: 1}
	return batch(ctx, dest, func(stage string) error {
		if err := e.write(ctx, filepath.Join(stage, "atlas.png"), h, recipe, func(_ int, y int, dst []tile.Pixel) error {
			clear(dst)
			for i, f := range atlas.Frames {
				extruded := f.Rect.Inset(-recipe.Extrude)
				if y < extruded.Min.Y || y >= extruded.Max.Y {
					continue
				}
				sample := samplers[i]
				sy := min(f.Rect.Dy()-1, max(0, y-f.Rect.Min.Y))
				for x := extruded.Min.X; x < extruded.Max.X; x++ {
					sx := min(f.Rect.Dx()-1, max(0, x-f.Rect.Min.X))
					dst[x] = sample.Resample(f.Trim.Add(s.Bounds.Min), sx, sy, recipe.Scale, recipe.Nearest)
				}
				sample.DropTiles()
				if sample.Err() != nil {
					return sample.Err()
				}
			}
			return nil
		}); err != nil {
			return err
		}
		return publish.File(ctx, filepath.Join(stage, "atlas.json"), func(out *os.File) error {
			enc := json.NewEncoder(out)
			enc.SetIndent("", "  ")
			return enc.Encode(atlas)
		})
	})
}

func pack(atlas *Atlas, r document.Recipe) error {
	order := make([]int, len(atlas.Frames))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := atlas.Frames[order[i]].Trim, atlas.Frames[order[j]].Trim
		if a.Dy() == b.Dy() {
			return a.Dx() > b.Dx()
		}
		return a.Dy() > b.Dy()
	})
	gutter := r.Padding + r.Extrude
	x, y, row := 0, 0, 0
	for _, i := range order {
		f := &atlas.Frames[i]
		w, h := f.Trim.Dx()*r.Scale+2*gutter, f.Trim.Dy()*r.Scale+2*gutter
		if x+w > r.AtlasMax {
			y += row
			x, row = 0, 0
		}
		if w > r.AtlasMax || y+h > r.AtlasMax {
			return fmt.Errorf("frame %q does not fit in a %d px atlas; increase Atlas size or reduce Scale", f.Name, r.AtlasMax)
		}
		f.Rect = image.Rect(x+gutter, y+gutter, x+w-gutter, y+h-gutter)
		x += w
		row = max(row, h)
		atlas.Width, atlas.Height = max(atlas.Width, x), max(atlas.Height, y+row)
	}
	return nil
}
