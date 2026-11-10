package document

import (
	"errors"
	"fmt"
	"image"
	"slices"
	"strings"

	"github.com/digimbyte/photobyte/internal/tile"
)

type AddLayer struct {
	Name   string
	Kind   Kind
	Parent ID
	Levels Levels
}

func (c AddLayer) label() string { return "Add " + string(c.Kind) + " layer" }

func (c AddLayer) change(s *State) error {
	layer := NewLayer(c.Name, c.Kind)
	layer.ID, layer.Parent = s.next(), c.Parent
	if c.Kind == Adjustment {
		if s.Space == Data {
			return errors.New("Levels is available for color projects")
		}
		layer.Levels = c.Levels
	}
	s.Layers = append(s.Layers, layer)
	return nil
}

type PlaceRaster struct {
	Name string
	Grid tile.Grid
}

func (c PlaceRaster) label() string { return "Place image" }

func (c PlaceRaster) change(s *State) error {
	if err := c.Grid.Validate(); err != nil {
		return err
	}
	layer := NewLayer(c.Name, Raster)
	layer.ID, layer.Image = s.next(), c.Grid
	layer.Transform.X, layer.Transform.Y = float64(s.Bounds.Min.X), float64(s.Bounds.Min.Y)
	s.Layers = append(s.Layers, layer)
	return nil
}

// LayerProperties replaces metadata while preserving the current raster,
// mask and per-frame content.
type LayerProperties struct{ Layer Layer }

func (c LayerProperties) label() string { return "Layer properties" }

func (c LayerProperties) change(s *State) error {
	i := s.layerIndex(c.Layer.ID)
	if i < 0 {
		return errors.New("select a layer")
	}
	l := &s.Layers[i]
	l.Name, l.Parent, l.Visible = c.Layer.Name, c.Layer.Parent, c.Layer.Visible
	l.Blend, l.Opacity, l.Transform, l.Levels = c.Layer.Blend, c.Layer.Opacity, c.Layer.Transform, c.Layer.Levels
	return nil
}

type SetMask struct {
	ID     ID
	Enable bool
}

func (c SetMask) label() string { return "Layer mask" }

func (c SetMask) change(s *State) error {
	i := s.layerIndex(c.ID)
	if i < 0 {
		return errors.New("select a layer")
	}
	l := &s.Layers[i]
	if !c.Enable {
		l.Mask = nil
	} else if l.Mask == nil {
		l.Mask = &tile.Grid{Default: tile.Pixel{A: 65535}}
	}
	return nil
}

type DeleteLayer struct{ ID ID }

func (c DeleteLayer) label() string { return "Delete layer" }

func (c DeleteLayer) change(s *State) error {
	if s.layerIndex(c.ID) < 0 {
		return errors.New("select a layer")
	}
	remove := map[ID]bool{c.ID: true}
	for _, l := range s.Layers {
		for parent := l.Parent; parent != 0; {
			if parent == c.ID {
				remove[l.ID] = true
				break
			}
			p, _ := s.Layer(parent)
			parent = p.Parent
		}
	}
	s.Layers = slices.DeleteFunc(s.Layers, func(l Layer) bool { return remove[l.ID] })
	for i := range s.Frames {
		f := &s.Frames[i]
		cloneCels(f)
		for id := range remove {
			delete(f.Cels, id)
		}
	}
	return nil
}

type MoveLayer struct {
	ID ID
	Up bool
}

func (c MoveLayer) label() string { return "Reorder layer" }

func (c MoveLayer) change(s *State) error {
	i := s.layerIndex(c.ID)
	if i < 0 {
		return errors.New("select a layer")
	}
	step := -1
	if c.Up {
		step = 1
	}
	for j := i + step; j >= 0 && j < len(s.Layers); j += step {
		if s.Layers[i].Parent == s.Layers[j].Parent {
			s.Layers[i], s.Layers[j] = s.Layers[j], s.Layers[i]
			return nil
		}
	}
	return errors.New("layer is already at the edge of its group")
}

type AddFrame struct {
	After     ID
	Duplicate bool
}

func (c AddFrame) label() string { return "Add frame" }

func (c AddFrame) change(s *State) error {
	if len(s.Frames) >= MaxFrames {
		return errors.New("frame limit reached")
	}
	i := s.frameIndex(c.After)
	if i < 0 {
		return errors.New("select a frame")
	}
	id := s.next()
	f := Frame{ID: id, Name: fmt.Sprintf("Frame %d", id), Time: Delay{Num: 1, Den: 10}}
	if c.Duplicate {
		f.Time, f.Tag, f.Cels = s.Frames[i].Time, s.Frames[i].Tag, s.Frames[i].Cels
	}
	s.Frames = slices.Insert(s.Frames, i+1, f)
	return nil
}

type EditFrame struct {
	ID       ID
	Name     string
	Tag      string
	Duration Delay
}

func (c EditFrame) label() string { return "Frame properties" }

func (c EditFrame) change(s *State) error {
	i := s.frameIndex(c.ID)
	if i < 0 {
		return errors.New("select a frame")
	}
	f := &s.Frames[i]
	f.Name, f.Tag, f.Time = c.Name, c.Tag, c.Duration
	return nil
}

type MoveFrame struct {
	ID      ID
	Forward bool
}

func (c MoveFrame) label() string { return "Reorder frame" }

func (c MoveFrame) change(s *State) error {
	i := s.frameIndex(c.ID)
	j := i - 1
	if c.Forward {
		j = i + 1
	}
	if i < 0 || j < 0 || j >= len(s.Frames) {
		return errors.New("frame cannot move further")
	}
	s.Frames[i], s.Frames[j] = s.Frames[j], s.Frames[i]
	return nil
}

type DeleteFrame struct{ ID ID }

func (c DeleteFrame) label() string { return "Delete frame" }

func (c DeleteFrame) change(s *State) error {
	i := s.frameIndex(c.ID)
	if len(s.Frames) < 2 || i < 0 {
		return errors.New("the project needs at least one frame")
	}
	s.Frames = slices.Delete(s.Frames, i, i+1)
	return nil
}

type SetPlays uint32

func (c SetPlays) label() string { return "Animation loop" }

func (c SetPlays) change(s *State) error {
	s.Plays = uint32(c)
	return nil
}

type Crop struct{ Bounds image.Rectangle }

func (c Crop) label() string { return "Crop canvas" }

func (c Crop) change(s *State) error {
	bounds := c.Bounds.Intersect(s.Bounds)
	if bounds.Empty() {
		return errors.New("select an area of the canvas to crop")
	}
	s.Bounds = bounds
	return nil
}

type SetSlice struct {
	Slice  Slice
	Delete bool
}

func (c SetSlice) label() string { return "Set export slice" }

func (c SetSlice) change(s *State) error {
	i := slices.IndexFunc(s.Slices, func(slice Slice) bool { return strings.EqualFold(slice.Name, c.Slice.Name) })
	switch {
	case c.Delete && i >= 0:
		s.Slices = slices.Delete(s.Slices, i, i+1)
	case c.Delete:
		return errors.New("slice does not exist")
	case i >= 0:
		s.Slices[i] = c.Slice
	default:
		s.Slices = append(s.Slices, c.Slice)
	}
	return nil
}

type SetRecipe struct{ Recipe Recipe }

func (c SetRecipe) label() string { return "Save export recipe" }

func (c SetRecipe) change(s *State) error {
	i := slices.IndexFunc(s.Recipes, func(r Recipe) bool { return strings.EqualFold(r.Name, c.Recipe.Name) })
	if i >= 0 {
		s.Recipes[i] = c.Recipe
	} else {
		s.Recipes = append(s.Recipes, c.Recipe)
	}
	return nil
}
