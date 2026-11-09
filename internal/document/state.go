// Package document owns editing commands and immutable project revisions.
package document

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"maps"
	"math"
	"slices"
	"strings"
	"time"

	"digiphoto/internal/tile"
)

const (
	MaxSide   = 16384
	MaxFrames = 1000
	MaxLayers = 256
	UndoLimit = 100
)

type ID uint64

type Space string

const (
	SRGB Space = "srgb"
	P3   Space = "display-p3"
	Data Space = "data"
)

type Kind string

const (
	Raster     Kind = "raster"
	Group      Kind = "group"
	Adjustment Kind = "levels"
)

type Blend string

const (
	Normal   Blend = "normal"
	Multiply Blend = "multiply"
	Screen   Blend = "screen"
	Overlay  Blend = "overlay"
)

type Transform struct {
	X, Y     float64
	SX, SY   float64
	Rotation float64
}

func Identity() Transform { return Transform{SX: 1, SY: 1} }

func (t Transform) Valid() bool {
	return finite(t.X) && finite(t.Y) && math.Abs(t.Rotation) <= 360000 &&
		math.Abs(t.X) <= MaxSide*4 && math.Abs(t.Y) <= MaxSide*4 &&
		math.Abs(t.SX) >= 0.01 && math.Abs(t.SX) <= 100 &&
		math.Abs(t.SY) >= 0.01 && math.Abs(t.SY) <= 100
}

func (t Transform) Inverse() Inverse {
	sin, cos := math.Sincos(t.Rotation * math.Pi / 180)
	return Inverse{cos / t.SX, sin / t.SX, -sin / t.SY, cos / t.SY, t.X, t.Y}
}

type Inverse struct{ xx, xy, yx, yy, x, y float64 }

func (i Inverse) At(x, y float64) (float64, float64) {
	x, y = x-i.x, y-i.y
	return x*i.xx + y*i.xy, x*i.yx + y*i.yy
}

func finite(n float64) bool { return !math.IsNaN(n) && !math.IsInf(n, 0) }

type Levels struct{ Black, White, Gamma float64 }

func NeutralLevels() Levels { return Levels{White: 1, Gamma: 1} }

func (l Levels) Valid() bool {
	return l.Black >= 0 && l.White <= 1 && l.Black < l.White && l.Gamma >= 0.1 && l.Gamma <= 10
}

type Layer struct {
	ID, Parent ID
	Name       string
	Kind       Kind
	Blend      Blend
	Visible    bool
	Opacity    float64
	Transform  Transform
	Image      tile.Grid
	Mask       *tile.Grid
	Levels     Levels
}

func NewLayer(name string, kind Kind) Layer {
	return Layer{Name: name, Kind: kind, Blend: Normal, Visible: true, Opacity: 1,
		Transform: Identity(), Levels: NeutralLevels()}
}

type Delay struct {
	Num, Den uint16
}

func (d Delay) Duration() time.Duration {
	den := uint64(d.Den)
	if den == 0 {
		den = 100
	}
	return time.Duration(uint64(d.Num) * uint64(time.Second) / den)
}

type Frame struct {
	ID   ID
	Name string
	Tag  string
	Time Delay
	Cels map[ID]tile.Grid
}

type Slice struct {
	Name   string
	Bounds image.Rectangle
}

type Recipe struct {
	Name     string
	Format   string
	Scale    int
	Depth    int
	Nearest  bool
	Padding  int
	Extrude  int
	Trim     bool
	AtlasMax int
	Space    Space
}

func DefaultRecipe() Recipe {
	return Recipe{Name: "Default", Format: "png", Scale: 1, Depth: 8, Padding: 2, Extrude: 1, Trim: true, AtlasMax: 2048, Space: SRGB}
}

func (r Recipe) Valid() bool {
	return validName(r.Name) && slices.Contains([]string{"png", "apng", "gif", "jpg", "webp"}, r.Format) &&
		r.Scale >= 1 && r.Scale <= 4 && (r.Depth == 8 || r.Depth == 16) &&
		r.Padding >= 0 && r.Padding <= 32 && r.Extrude >= 0 && r.Extrude <= 32 &&
		r.AtlasMax >= 64 && r.AtlasMax <= MaxSide && validSpace(r.Space)
}

type State struct {
	Bounds  image.Rectangle
	Space   Space
	Layers  []Layer // ordered bottom to top within each parent
	Frames  []Frame
	Plays   uint32 // zero loops indefinitely
	Poster  *tile.Grid
	Slices  []Slice
	Recipes []Recipe
	NextID  ID
}

func NewState(w, h int, space Space) *State {
	layer := NewLayer("Layer 1", Raster)
	layer.ID = 1
	recipe := DefaultRecipe()
	recipe.Space = space
	return &State{
		Bounds: image.Rect(0, 0, w, h), Space: space, Layers: []Layer{layer},
		Frames:  []Frame{{ID: 2, Name: "Frame 1", Time: Delay{Num: 1, Den: 10}}},
		Recipes: []Recipe{recipe}, NextID: 3,
	}
}

func (s *State) clone() *State {
	out := *s
	out.Layers = slices.Clone(s.Layers)
	out.Frames = slices.Clone(s.Frames)
	out.Slices = slices.Clone(s.Slices)
	out.Recipes = slices.Clone(s.Recipes)
	return &out
}

func (s *State) Layer(id ID) (Layer, bool) {
	i := s.layerIndex(id)
	if i < 0 {
		return Layer{}, false
	}
	return s.Layers[i], true
}

func (s *State) layerIndex(id ID) int {
	return slices.IndexFunc(s.Layers, func(l Layer) bool { return l.ID == id })
}

func (s *State) Frame(id ID) (Frame, bool) {
	i := s.frameIndex(id)
	if i < 0 {
		return Frame{}, false
	}
	return s.Frames[i], true
}

func (s *State) frameIndex(id ID) int {
	return slices.IndexFunc(s.Frames, func(f Frame) bool { return f.ID == id })
}

func (s *State) Grid(layer Layer, frame ID, shared bool) tile.Grid {
	if !shared {
		if f, ok := s.Frame(frame); ok {
			if g, ok := f.Cels[layer.ID]; ok {
				return g
			}
		}
	}
	return layer.Image
}

func (s *State) Validate() error {
	b := s.Bounds
	if b.Empty() || b.Dx() > MaxSide || b.Dy() > MaxSide ||
		b.Min.X < -MaxSide*4 || b.Min.Y < -MaxSide*4 || b.Max.X > MaxSide*4 || b.Max.Y > MaxSide*4 {
		return errors.New("invalid canvas dimensions")
	}
	if !validSpace(s.Space) || len(s.Layers) > MaxLayers || len(s.Frames) == 0 || len(s.Frames) > MaxFrames {
		return errors.New("invalid document space, layer count or frame count")
	}
	ids := make(map[ID]bool)
	layers := make(map[ID]Layer)
	register := func(id ID) bool {
		if id == 0 || id >= s.NextID || ids[id] {
			return false
		}
		ids[id] = true
		return true
	}
	for _, l := range s.Layers {
		if !register(l.ID) || !validName(l.Name) || !validBlend(l.Blend) ||
			!finite(l.Opacity) || l.Opacity < 0 || l.Opacity > 1 || !l.Transform.Valid() {
			return errors.New("invalid layer properties")
		}
		switch l.Kind {
		case Group, Raster:
		case Adjustment:
			if !l.Levels.Valid() {
				return errors.New("invalid Levels curve")
			}
		default:
			return errors.New("unknown layer type")
		}
		layers[l.ID] = l
	}
	for _, l := range s.Layers {
		depth := 0
		for id := l.Parent; id != 0; id = layers[id].Parent {
			depth++
			if layers[id].Kind != Group || depth > 32 || id == l.ID {
				return errors.New("invalid layer hierarchy")
			}
		}
	}
	for _, f := range s.Frames {
		if !register(f.ID) || !validName(f.Name) || len(f.Tag) > 128 {
			return errors.New("invalid frame")
		}
		for layer := range f.Cels {
			if layers[layer].Kind != Raster {
				return errors.New("frame references a missing raster layer")
			}
		}
	}
	if len(s.Slices) > 1024 || len(s.Recipes) < 1 || len(s.Recipes) > 256 {
		return errors.New("too many slices or export recipes")
	}
	names := make(map[string]bool)
	for _, slice := range s.Slices {
		name := strings.ToLower(slice.Name)
		if !validName(slice.Name) || names[name] || slice.Bounds.Empty() {
			return errors.New("invalid or duplicate slice")
		}
		names[name] = true
	}
	clear(names)
	for _, recipe := range s.Recipes {
		name := strings.ToLower(recipe.Name)
		if !recipe.Valid() || names[name] {
			return errors.New("invalid or duplicate export recipe")
		}
		names[name] = true
	}
	return nil
}

func (s *State) VisitGrids(visit func(tile.Grid) error) error {
	for _, l := range s.Layers {
		if err := visit(l.Image); err != nil {
			return err
		}
		if l.Mask != nil {
			if err := visit(*l.Mask); err != nil {
				return err
			}
		}
	}
	for _, f := range s.Frames {
		for _, g := range f.Cels {
			if err := visit(g); err != nil {
				return err
			}
		}
	}
	if s.Poster != nil {
		return visit(*s.Poster)
	}
	return nil
}

func validName(s string) bool { return strings.TrimSpace(s) != "" && len(s) <= 128 }
func validSpace(s Space) bool { return s == SRGB || s == P3 || s == Data }
func validBlend(b Blend) bool { return b == Normal || b == Multiply || b == Screen || b == Overlay }

func (s *State) next() ID {
	id := s.NextID
	s.NextID++
	return id
}

type Checkpoint struct {
	Label string
	State *State
}

type Snapshot struct {
	Project   string
	Version   uint64
	Cursor    int
	History   []Checkpoint
	ChangedAt time.Time `json:"-"`
}

func NewSnapshot(s *State, label string) Snapshot {
	var id [16]byte
	rand.Read(id[:])
	return Snapshot{Project: hex.EncodeToString(id[:]), Version: 1, History: []Checkpoint{{Label: label, State: s}}, ChangedAt: time.Now()}
}

func (s Snapshot) State() *State { return s.History[s.Cursor].State }

func (s Snapshot) Validate() error {
	projectID, err := hex.DecodeString(s.Project)
	if err != nil || len(projectID) != 16 || s.Version == 0 || s.Cursor < 0 ||
		s.Cursor >= len(s.History) || len(s.History) > UndoLimit+1 {
		return errors.New("invalid project revision")
	}
	grids := func(g tile.Grid) error { return g.Validate() }
	for _, step := range s.History {
		if step.State == nil || !validName(step.Label) {
			return errors.New("invalid undo checkpoint")
		}
		if err := step.State.Validate(); err != nil {
			return fmt.Errorf("checkpoint %q: %w", step.Label, err)
		}
		if err := step.State.VisitGrids(grids); err != nil {
			return err
		}
	}
	return nil
}

func cloneCels(f *Frame) {
	f.Cels = maps.Clone(f.Cels)
	if f.Cels == nil {
		f.Cels = make(map[ID]tile.Grid)
	}
}
