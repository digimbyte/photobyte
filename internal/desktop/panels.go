package desktop

import (
	"errors"
	"fmt"
	"image"
	"strconv"
	"strings"
	"time"

	"digiphoto/internal/document"
	"digiphoto/internal/tile"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
)

func (u *window) panelContent(gtx layout.Context, panel string) layout.Dimensions {
	t := u.active()
	if t == nil {
		return u.text("Open a document to use this panel.", 13, true)(gtx)
	}
	switch panel {
	case panelProperties:
		return u.properties(gtx, t)
	case panelHistory:
		return u.history(gtx, t)
	case panelAssets:
		return u.assets(gtx, t)
	case panelTimeline:
		return u.timePanel(gtx, t)
	default:
		return u.layers(gtx, t)
	}
}

func (u *window) layers(gtx layout.Context, t *tab) layout.Dimensions {
	s := t.editor.Snapshot().State()
	add := func(kind document.Kind, name string) {
		parent := document.ID(0)
		if l, ok := s.Layer(t.layer); ok {
			parent = l.Parent
			if l.Kind == document.Group {
				parent = l.ID
			}
		}
		if u.apply(t, document.AddLayer{Kind: kind, Name: name, Parent: parent, Levels: document.NeutralLevels()}) {
			t.layer, u.mask = s.NextID, false
		}
	}
	items := []layout.Widget{func(gtx layout.Context) layout.Dimensions {
		return wrapRow(gtx, u.workspaceButton("add-layer", "Add Layer", false, func() { add(document.Raster, "Raster layer") }),
			u.workspaceButton("add-group", "Group", false, func() { add(document.Group, "Group") }),
			u.workspaceButton("add-levels", "Levels", false, func() { add(document.Adjustment, "Levels") }))
	}, u.workspaceButton("place-image", "Place Image", false, func() {
		u.show("Place image", "Embed a still image as a new layer. Animated images use the poster or first frame.",
			[]fieldSpec{{"Image path", ""}}, func(v []string) bool {
				if v[0] == "" {
					u.setMessage("", errors.New("choose an image to place"))
					return false
				}
				u.place(t, v[0])
				return true
			})
		u.form.browse = "image"
	})}
	if l, ok := s.Layer(t.layer); ok {
		items = append(items, func(gtx layout.Context) layout.Dimensions {
			return wrapRow(gtx, u.workspaceButton("layer-up", "Up", false, func() { u.apply(t, document.MoveLayer{ID: l.ID, Up: true}) }),
				u.workspaceButton("layer-down", "Down", false, func() { u.apply(t, document.MoveLayer{ID: l.ID}) }),
				u.workspaceButton("layer-delete", "Delete", false, func() { u.apply(t, document.DeleteLayer{ID: l.ID}) }),
				u.workspaceButton("layer-mask", "Mask", l.Mask != nil, func() {
					if u.apply(t, document.SetMask{ID: l.ID, Enable: l.Mask == nil}) {
						u.mask = l.Mask == nil
					}
				}))
		})
	}
	var visit func(document.ID, int)
	visit = func(parent document.ID, depth int) {
		for i := len(s.Layers) - 1; i >= 0; i-- {
			l := s.Layers[i]
			if l.Parent != parent {
				continue
			}
			items = append(items, func(gtx layout.Context) layout.Dimensions {
				visible := "Hide"
				if !l.Visible {
					visible = "Show"
				}
				return layout.Inset{Left: unit.Dp(min(depth, 6) * 12)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return wrapRow(gtx,
						u.workspaceButton(fmt.Sprintf("visible-%d", l.ID), visible, false, func() {
							l.Visible = !l.Visible
							u.apply(t, document.LayerProperties{Layer: l})
						}),
						u.workspaceButton(fmt.Sprintf("layer-%d", l.ID), l.Name, t.layer == l.ID, func() {
							t.layer, u.mask = l.ID, false
						}))
				})
			})
			if l.Kind == document.Group {
				visit(l.ID, depth+1)
			}
		}
	}
	visit(0, 0)
	return column(gtx, items...)
}

func (u *window) properties(gtx layout.Context, t *tab) layout.Dimensions {
	s := t.editor.Snapshot().State()
	l, ok := s.Layer(t.layer)
	items := []layout.Widget{
		u.text("Tool options", 14, false),
		u.workspaceButton("brush-options", fmt.Sprintf("Brush  %.0f px", u.radius*2), false, u.brushSettings),
		u.workspaceButton("clear-selection", "Clear selection", !t.selection.Empty(), func() { t.selection = image.Rectangle{} }),
	}
	if ok {
		items = append(items, layout.Spacer{Height: 8}.Layout, u.text(l.Name, 17, false),
			u.text(fmt.Sprintf("%s · %s · %.0f%% opacity", l.Kind, l.Blend, l.Opacity*100), 12, true),
			u.workspaceButton("layer-appearance", "Appearance and group", false, func() { u.layerAppearance(t, l, s) }))
		if l.Kind == document.Raster {
			items = append(items, u.workspaceButton("layer-transform", "Transform", false, func() { u.layerTransform(t, l) }))
		}
		if l.Kind == document.Adjustment {
			items = append(items, u.workspaceButton("layer-levels", "Levels curve", false, func() { u.layerLevels(t, l) }))
		}
		if l.Mask != nil {
			items = append(items, u.workspaceButton("paint-mask", "Paint mask", u.mask, func() { u.mask = !u.mask }),
				u.text("Brush hides. Eraser restores.", 12, true))
		}
	}
	return column(gtx, items...)
}

func (u *window) brushSettings() {
	ink := u.ink
	u.show("Brush options", "Set a brush diameter and RGB color.", []fieldSpec{
		{"Diameter in pixels", number(u.radius * 2)},
		{"Hex RGB", fmt.Sprintf("%02X%02X%02X", ink.R>>8, ink.G>>8, ink.B>>8)},
	}, func(v []string) bool {
		d, de := strconv.ParseFloat(v[0], 64)
		hex := strings.TrimPrefix(v[1], "#")
		rgb, ce := strconv.ParseUint(hex, 16, 24)
		if de != nil || ce != nil || len(hex) != 6 || !(d >= 1 && d <= 1024) {
			u.setMessage("", errors.New("enter a diameter from 1 to 1024 and a six-digit RGB color"))
			return false
		}
		u.radius = d / 2
		u.ink = tile.Pixel{R: uint16(byte(rgb>>16)) * 257, G: uint16(byte(rgb>>8)) * 257, B: uint16(byte(rgb)) * 257, A: 65535}
		return true
	})
}

func (u *window) layerAppearance(t *tab, l document.Layer, s *document.State) {
	groups := []string{"0 = canvas"}
	for _, group := range s.Layers {
		if group.Kind == document.Group && group.ID != l.ID {
			groups = append(groups, fmt.Sprintf("%d = %s", group.ID, group.Name))
		}
	}
	u.show("Layer appearance", "Available groups  "+strings.Join(groups, " · "), []fieldSpec{
		{"Layer name", l.Name}, {"Opacity  (0 to 100)", number(l.Opacity * 100)},
		{"Blend  (normal, multiply, screen, overlay)", string(l.Blend)}, {"Parent group ID", fmt.Sprint(l.Parent)},
	}, func(v []string) bool {
		opacity, oe := strconv.ParseFloat(v[1], 64)
		parent, pe := strconv.ParseUint(v[3], 10, 64)
		if oe != nil || pe != nil {
			u.setMessage("", errors.New("enter a numeric opacity and group ID"))
			return false
		}
		l.Name, l.Opacity, l.Blend, l.Parent = v[0], opacity/100, document.Blend(v[2]), document.ID(parent)
		return u.apply(t, document.LayerProperties{Layer: l})
	})
}

func (u *window) layerTransform(t *tab, l document.Layer) {
	tr := l.Transform
	u.show("Layer transform", "Scale and rotate around the layer origin. A negative scale flips the layer.", []fieldSpec{
		{"X offset", number(tr.X)}, {"Y offset", number(tr.Y)}, {"Scale X", number(tr.SX)},
		{"Scale Y", number(tr.SY)}, {"Rotation in degrees", number(tr.Rotation)},
	}, func(v []string) bool {
		val, ok := u.floats(v)
		if !ok {
			return false
		}
		l.Transform = document.Transform{X: val[0], Y: val[1], SX: val[2], SY: val[3], Rotation: val[4]}
		return u.apply(t, document.LayerProperties{Layer: l})
	})
}

func (u *window) layerLevels(t *tab, l document.Layer) {
	u.show("Levels curve", "Set linear-light black and white points and the midpoint gamma.", []fieldSpec{
		{"Black point  (0 to 1)", number(l.Levels.Black)}, {"White point  (0 to 1)", number(l.Levels.White)},
		{"Gamma  (0.1 to 10)", number(l.Levels.Gamma)},
	}, func(v []string) bool {
		val, ok := u.floats(v)
		if !ok {
			return false
		}
		l.Levels = document.Levels{Black: val[0], White: val[1], Gamma: val[2]}
		return u.apply(t, document.LayerProperties{Layer: l})
	})
}

func (u *window) floats(text []string) ([]float64, bool) {
	out := make([]float64, len(text))
	for i, v := range text {
		n, err := strconv.ParseFloat(v, 64)
		if err != nil {
			u.setMessage("", errors.New("enter a number in each field"))
			return nil, false
		}
		out[i] = n
	}
	return out, true
}

func (u *window) history(gtx layout.Context, t *tab) layout.Dimensions {
	s := t.editor.Snapshot()
	items := make([]layout.Widget, 0, len(s.History)+1)
	items = append(items, u.text("Undo checkpoints", 14, false))
	for i := len(s.History) - 1; i >= 0; i-- {
		items = append(items, u.workspaceButton(fmt.Sprintf("history-%d", i), s.History[i].Label, i == s.Cursor, func() {
			u.goHistory(t, i)
		}))
	}
	return column(gtx, items...)
}

func (u *window) assets(gtx layout.Context, t *tab) layout.Dimensions {
	s := t.editor.Snapshot().State()
	items := []layout.Widget{u.text("Export recipes", 15, false)}
	for i, r := range s.Recipes {
		items = append(items, u.workspaceButton(fmt.Sprintf("recipe-%d", i), fmt.Sprintf("%s · %s · %dx", r.Name, r.Format, r.Scale), i == t.recipe,
			func() { t.recipe = i }))
	}
	items = append(items, func(gtx layout.Context) layout.Dimensions {
		return wrapRow(gtx,
			u.workspaceButton("recipe-new", "New recipe", false, func() {
				r := document.DefaultRecipe()
				r.Name, r.Space = "Export", s.Space
				u.recipeSettings(t, r)
			}), u.workspaceButton("recipe-edit", "Edit recipe", false, func() { u.recipeSettings(t, s.Recipes[t.recipe]) }))
	}, layout.Spacer{Height: 8}.Layout, u.text("Slices", 15, false))
	for _, slice := range s.Slices {
		items = append(items, func(gtx layout.Context) layout.Dimensions {
			return wrapRow(gtx, u.workspaceButton("slice-"+slice.Name, slice.Name, t.selection == slice.Bounds, func() { t.selection = slice.Bounds }),
				u.workspaceButton("slice-remove-"+slice.Name, "Remove", false, func() {
					u.apply(t, document.SetSlice{Slice: slice, Delete: true})
				}))
		})
	}
	items = append(items, u.workspaceButton("slice-new", "Name selection", false, func() {
		area := t.selection
		if area.Empty() {
			u.setMessage("", errors.New("select an area of the canvas first"))
			return
		}
		u.show("Name slice", "Export this area using the selected recipe.", []fieldSpec{{"Slice name", "slice"}}, func(v []string) bool {
			return u.apply(t, document.SetSlice{Slice: document.Slice{Name: v[0], Bounds: area}})
		})
	}), u.workspaceButton("slice-export", "Export slices", false, func() { u.chooseExport(t, "slices") }),
		u.workspaceButton("atlas-export", "Export sprite sheet", false, func() { u.chooseExport(t, "atlas") }))
	return column(gtx, items...)
}

func (u *window) recipeSettings(t *tab, r document.Recipe) {
	u.show("Export recipe", "Use PNG or APNG for 16-bit, Display P3, or raw texture output.", []fieldSpec{
		{"Recipe name", r.Name}, {"Format  (png, apng, gif, jpg, webp)", r.Format},
		{"Scale and depth  (1–4, 8 or 16)", fmt.Sprintf("%d %d", r.Scale, r.Depth)},
		{"Resampling and color  (nearest / smooth, srgb / display-p3 / data)", fmt.Sprintf("%s %s", resampling(r), r.Space)},
		{"Atlas size, padding, extrusion, and trim  (yes / no)", fmt.Sprintf("%d %d %d %s", r.AtlasMax, r.Padding, r.Extrude, yesNo(r.Trim))},
	}, func(v []string) bool {
		var sample, space, trim string
		_, a := fmt.Sscan(v[2], &r.Scale, &r.Depth)
		_, b := fmt.Sscan(v[3], &sample, &space)
		_, c := fmt.Sscan(v[4], &r.AtlasMax, &r.Padding, &r.Extrude, &trim)
		if a != nil || b != nil || c != nil || (sample != "nearest" && sample != "smooth") || (trim != "yes" && trim != "no") {
			u.setMessage("", errors.New("fill in all recipe options in the displayed order"))
			return false
		}
		r.Name, r.Format, r.Space, r.Nearest, r.Trim = v[0], v[1], document.Space(space), sample == "nearest", trim == "yes"
		if !u.apply(t, document.SetRecipe{Recipe: r}) {
			return false
		}
		for i, recipe := range t.editor.Snapshot().State().Recipes {
			if strings.EqualFold(recipe.Name, r.Name) {
				t.recipe = i
				break
			}
		}
		return true
	})
}

func resampling(r document.Recipe) string {
	if r.Nearest {
		return "nearest"
	}
	return "smooth"
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func (u *window) timePanel(gtx layout.Context, t *tab) layout.Dimensions {
	s := t.editor.Snapshot().State()
	add := func(duplicate bool) {
		if u.apply(t, document.AddFrame{After: t.frame, Duplicate: duplicate}) {
			t.frame = s.NextID
		}
	}
	return column(gtx, func(gtx layout.Context) layout.Dimensions {
		return wrapRow(gtx, u.workspaceButton("play", "Play / Stop", !u.playAt.IsZero(), func() {
			if u.playAt.IsZero() {
				u.playAt, u.playIndex = gtx.Now, 0
			} else {
				u.stopPlayback()
			}
		}), u.workspaceButton("onion", "Onion skin", t.onion, func() { t.onion = !t.onion }),
			u.workspaceButton("frame-add", "Add", false, func() { add(false) }),
			u.workspaceButton("frame-duplicate", "Duplicate", false, func() { add(true) }),
			u.workspaceButton("frame-prev", "Earlier", false, func() { u.apply(t, document.MoveFrame{ID: t.frame}) }),
			u.workspaceButton("frame-next", "Later", false, func() { u.apply(t, document.MoveFrame{ID: t.frame, Forward: true}) }),
			u.workspaceButton("frame-delete", "Delete", false, func() { u.apply(t, document.DeleteFrame{ID: t.frame}) }),
			u.workspaceButton("frame-edit", "Timing and tag", false, func() { u.frameProperties(t, s) }),
			u.workspaceButton("frame-loop", "Loop", s.Plays == 0, func() { u.loopSettings(t, s.Plays) }))
	}, func(gtx layout.Context) layout.Dimensions {
		return u.frameList.Layout(gtx, len(s.Frames), func(gtx layout.Context, i int) layout.Dimensions {
			f := s.Frames[i]
			return u.workspaceButton(fmt.Sprintf("frame-%d", f.ID), fmt.Sprintf("%s · %s", f.Name, f.Time.Duration()), t.frame == f.ID, func() {
				u.stopPlayback()
				t.frame = f.ID
				u.canvas.cancelGesture()
			})(gtx)
		})
	})
}

func (u *window) frameProperties(t *tab, s *document.State) {
	f, _ := s.Frame(t.frame)
	u.show("Frame timing and tag", "A fraction of a second preserves APNG timing precisely.", []fieldSpec{
		{"Frame name", f.Name}, {"Tag", f.Tag}, {"Duration  (numerator / denominator)", fmt.Sprintf("%d/%d", f.Time.Num, f.Time.Den)},
	}, func(v []string) bool {
		var d document.Delay
		if _, err := fmt.Sscanf(v[2], "%d/%d", &d.Num, &d.Den); err != nil {
			u.setMessage("", errors.New("enter a duration such as 1/10"))
			return false
		}
		return u.apply(t, document.EditFrame{ID: f.ID, Name: v[0], Tag: v[1], Duration: d})
	})
}

func (u *window) loopSettings(t *tab, plays uint32) {
	u.show("Animation loop", "Enter 0 for continuous playback or a positive number of plays.", []fieldSpec{
		{"Play count", fmt.Sprint(plays)},
	}, func(v []string) bool {
		p, err := strconv.ParseUint(v[0], 10, 32)
		if err != nil {
			u.setMessage("", errors.New("enter a non-negative play count"))
			return false
		}
		return u.apply(t, document.SetPlays(p))
	})
}

func (u *window) playback(gtx layout.Context) {
	t := u.active()
	if t == nil || u.playAt.IsZero() {
		return
	}
	s := t.editor.Snapshot().State()
	i := 0
	for j, f := range s.Frames {
		if f.ID == t.frame {
			i = j
			break
		}
	}
	delay := func() time.Duration { return max(16*time.Millisecond, s.Frames[i].Time.Duration()) }
	for range len(s.Frames) {
		end := u.playAt.Add(delay())
		if gtx.Now.Before(end) {
			break
		}
		u.playAt = end
		i++
		if i == len(s.Frames) {
			u.playIndex++
			if s.Plays > 0 && uint32(u.playIndex) >= s.Plays {
				u.stopPlayback()
				return
			}
			i = 0
		}
		t.frame = s.Frames[i].ID
	}
	gtx.Execute(op.InvalidateCmd{At: u.playAt.Add(delay())})
}
