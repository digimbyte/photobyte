package desktop

import (
	"image"
	"slices"

	"gioui.org/f32"
	giogesture "gioui.org/gesture"
	"gioui.org/io/pointer"
	"gioui.org/io/semantic"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

const dockHeader = 30 // dp

type dockHandleKind uint8

const (
	movePanel dockHandleKind = iota
	moveGroup
	resizeDock
	resizeGroups
)

type dockHandle struct {
	kind        dockHandleKind
	side        dockSide
	group, next int
	panel       string
}

type dockRegion struct {
	handle       dockHandle
	rect         image.Rectangle
	span, offset int
}

type dropRegion struct {
	target    dockDrop
	hit, mark image.Rectangle
}

type dockDrag struct {
	region         dockRegion
	start          f32.Point
	point          image.Point
	held, moved    bool
	extent, weight int
	nextWeight     int
}

type dockPointer struct {
	gesture giogesture.Drag
	visible bool
}

type dockWidgets struct {
	handles map[dockHandle]*dockPointer
	scroll  map[string]*widget.List
	regions []dockRegion
	drops   []dropRegion
	drag    dockDrag
}

func within(gtx layout.Context, r image.Rectangle, w layout.Widget) layout.Dimensions {
	if r.Empty() {
		return layout.Dimensions{}
	}
	defer op.Offset(r.Min).Push(gtx.Ops).Pop()
	defer clip.Rect{Max: r.Size()}.Push(gtx.Ops).Pop()
	gtx.Constraints = layout.Exact(r.Size())
	return w(gtx)
}

func (u *window) workArea(gtx layout.Context) layout.Dimensions {
	d := &u.docking
	d.update(u, gtx)
	d.regions, d.drops = d.regions[:0], d.drops[:0]
	size := gtx.Constraints.Max
	toolWidth := min(gtx.Dp(82), size.X)
	seam := max(1, gtx.Dp(5))
	left, right := d.extent(gtx, u.workspace.Current[dockLeft]), d.extent(gtx, u.workspace.Current[dockRight])
	room := max(0, size.X-toolWidth-gtx.Dp(270))
	if left+right > room {
		minWidth := 0
		if left > 0 && right > 0 {
			minWidth = min(room/2, gtx.Dp(210))
		}
		left = min(room-minWidth, max(minWidth, left*room/(left+right)))
		right = room - left
	}
	bottom := min(d.extent(gtx, u.workspace.Current[dockBottom]), max(0, size.Y-gtx.Dp(170)))
	if bottom > 0 && !slices.ContainsFunc(u.workspace.Current[dockBottom].Groups, func(g panelGroup) bool {
		return g.Active != "" && !g.Collapsed
	}) {
		bottom = min(bottom, gtx.Dp(dockHeader)+seam)
	}
	center := image.Rect(toolWidth+left, 0, size.X-right, size.Y-bottom)
	within(gtx, image.Rect(0, 0, toolWidth, size.Y), u.toolRail)
	within(gtx, center, func(gtx layout.Context) layout.Dimensions { return u.canvas.layout(gtx, u.active()) })

	edge := gtx.Dp(16)
	d.addDrop(dockDrop{side: dockLeft}, image.Rect(center.Min.X, 0, min(center.Max.X, center.Min.X+edge), size.Y))
	d.addDrop(dockDrop{side: dockRight}, image.Rect(max(center.Min.X, center.Max.X-edge), 0, center.Max.X, size.Y))
	d.addDrop(dockDrop{side: dockBottom}, image.Rect(center.Min.X, max(0, center.Max.Y-edge), center.Max.X, center.Max.Y))
	if left > seam {
		split := center.Min.X - seam
		u.drawDock(gtx, dockLeft, image.Rect(toolWidth, 0, split, size.Y), seam)
		d.handle(gtx, dockRegion{handle: dockHandle{kind: resizeDock, side: dockLeft}, rect: image.Rect(split, 0, center.Min.X, size.Y), span: left})
	}
	if right > seam {
		split := center.Max.X + seam
		u.drawDock(gtx, dockRight, image.Rect(split, 0, size.X, size.Y), seam)
		d.handle(gtx, dockRegion{handle: dockHandle{kind: resizeDock, side: dockRight}, rect: image.Rect(center.Max.X, 0, split, size.Y), span: right})
	}
	if bottom > seam {
		split := center.Max.Y + seam
		u.drawDock(gtx, dockBottom, image.Rect(center.Min.X, split, center.Max.X, size.Y), seam)
		if bottom > gtx.Dp(dockHeader)+seam {
			d.handle(gtx, dockRegion{handle: dockHandle{kind: resizeDock, side: dockBottom}, rect: image.Rect(center.Min.X, center.Max.Y, center.Max.X, split), span: bottom})
		}
	}
	for handle, handler := range d.handles {
		if !handler.visible {
			if d.drag.held && d.drag.region.handle == handle {
				u.cancelDockDrag()
			}
			delete(d.handles, handle)
		}
	}
	if d.drag.moved && d.drag.region.handle.kind <= moveGroup {
		if drop, ok := d.dropAt(d.drag.point); ok {
			if drop.target.group != 0 && drop.mark == drop.hit {
				strokeRect(gtx, drop.mark, accent)
			} else {
				paint.FillShape(gtx.Ops, accent, clip.Rect(drop.mark).Op())
			}
		}
	}
	return layout.Dimensions{Size: size}
}

func (d *dockWidgets) extent(gtx layout.Context, dock workspaceDock) int {
	if slices.ContainsFunc(dock.Groups, func(g panelGroup) bool { return g.Active != "" }) {
		return gtx.Dp(unit.Dp(dock.Extent))
	}
	return 0
}

// Partition the dock axis. Collapsed side groups keep just their tab strip.
func (u *window) drawDock(gtx layout.Context, side dockSide, bounds image.Rectangle, seam int) {
	d := &u.docking
	groups := make([]*panelGroup, 0, len(panelNames))
	for i := range u.workspace.Current[side].Groups {
		g := &u.workspace.Current[side].Groups[i]
		if g.Active != "" {
			groups = append(groups, g)
		}
	}
	if len(groups) == 0 || bounds.Empty() {
		return
	}
	horizontal := side == dockBottom
	length, base := bounds.Dy(), gtx.Dp(dockHeader)
	if horizontal {
		length, base = bounds.Dx(), gtx.Dp(130)
	}
	seam = min(seam, length/(2*len(groups)))
	available := length - seam*(len(groups)-1)
	base = min(base, available/len(groups))
	flex, weight := available-base*len(groups), 0
	for _, g := range groups {
		if horizontal || !g.Collapsed {
			weight += g.Weight
		}
	}
	band := func(start, end int) image.Rectangle {
		if horizontal {
			return image.Rect(bounds.Min.X+start, bounds.Min.Y, bounds.Min.X+end, bounds.Max.Y)
		}
		return image.Rect(bounds.Min.X, bounds.Min.Y+start, bounds.Max.X, bounds.Min.Y+end)
	}
	pos, prior := 0, 0
	for i, g := range groups {
		extra := 0
		if (horizontal || !g.Collapsed) && weight > 0 {
			extra = flex * g.Weight / weight
			flex, weight = flex-extra, weight-g.Weight
		}
		if i > 0 {
			prev := groups[i-1]
			r := band(pos, pos+seam)
			d.addDrop(dockDrop{side: side, beforeGroup: g.ID}, r)
			if horizontal || (!prev.Collapsed && !g.Collapsed) {
				d.handle(gtx, dockRegion{
					handle: dockHandle{kind: resizeGroups, side: side, group: prev.ID, next: g.ID},
					rect:   r, span: prior + extra, offset: prior,
				})
			}
			pos += seam
		}
		u.drawPanelGroup(gtx, side, g, band(pos, pos+base+extra))
		if i == 0 {
			d.addDrop(dockDrop{side: side, beforeGroup: g.ID}, band(0, min(seam, base+extra)))
		}
		pos += base + extra
		prior = extra
	}
	d.addDrop(dockDrop{side: side}, band(max(0, pos-seam), length))
}

func (u *window) drawPanelGroup(gtx layout.Context, side dockSide, g *panelGroup, bounds image.Rectangle) {
	d := &u.docking
	paint.FillShape(gtx.Ops, panelBG, clip.Rect(bounds).Op())
	head := bounds
	head.Max.Y = min(head.Max.Y, head.Min.Y+gtx.Dp(dockHeader))
	paint.FillShape(gtx.Ops, buttonBG, clip.Rect(head).Op())
	d.addDrop(dockDrop{side: side, group: g.ID}, head)
	grip := head
	grip.Max.X = min(grip.Max.X, grip.Min.X+gtx.Dp(22))
	within(gtx, grip, func(gtx layout.Context) layout.Dimensions {
		center := gtx.Constraints.Max.Div(2)
		for _, y := range []int{-gtx.Dp(2), gtx.Dp(2)} {
			line := image.Rect(center.X-gtx.Dp(4), center.Y+y, center.X+gtx.Dp(4), center.Y+y+max(1, gtx.Dp(1)))
			paint.FillShape(gtx.Ops, textMuted, clip.Rect(line).Op())
		}
		return layout.Dimensions{Size: gtx.Constraints.Max}
	})
	d.handle(gtx, dockRegion{handle: dockHandle{kind: moveGroup, side: side, group: g.ID}, rect: grip})
	menu := head
	menu.Min.X = max(grip.Max.X, head.Max.X-gtx.Dp(35))
	within(gtx, menu, func(gtx layout.Context) layout.Dimensions {
		return u.panelTabButton(gtx, "panel-group-"+g.Tabs[0].Name, "…", "Panel group options", false, func() {
			u.showPanelGroup(g.ID)
		})
	})
	tabs := make([]string, 0, len(g.Tabs))
	widths := 0
	for _, tab := range g.Tabs {
		if !tab.Hidden {
			tabs = append(tabs, tab.Name)
			widths += tabWidth(tab.Name)
		}
	}
	available := min(menu.Min.X-grip.Max.X, gtx.Dp(unit.Dp(widths)))
	x := grip.Max.X
	for i, name := range tabs {
		width := available * tabWidth(name) / widths
		r := image.Rect(x, head.Min.Y, x+width, head.Max.Y)
		within(gtx, r, func(gtx layout.Context) layout.Dimensions {
			return u.panelTabButton(gtx, "panel-tab-"+name, name, name, name == g.Active, func() { u.activatePanel(name) })
		})
		d.handle(gtx, dockRegion{handle: dockHandle{kind: movePanel, side: side, group: g.ID, panel: name}, rect: r})
		if width > 0 {
			left, right := r, r
			left.Max.X = x + width/2
			right.Min.X = left.Max.X
			next := ""
			if i+1 < len(tabs) {
				next = tabs[i+1]
			}
			for _, item := range []struct {
				name string
				hit  image.Rectangle
				x    int
			}{{name, left, x}, {next, right, r.Max.X}} {
				mark := image.Rect(item.x-1, head.Min.Y, item.x+max(2, gtx.Dp(2)), head.Max.Y).Intersect(head)
				d.drops = append(d.drops, dropRegion{dockDrop{side: side, group: g.ID, beforeTab: item.name}, item.hit, mark})
			}
		}
		x, available, widths = x+width, available-width, widths-tabWidth(name)
	}
	if g.Collapsed {
		return
	}
	body := bounds
	body.Min.Y = head.Max.Y
	within(gtx, body, func(gtx layout.Context) layout.Dimensions {
		return layout.UniformInset(7).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			if d.scroll == nil {
				d.scroll = make(map[string]*widget.List)
			}
			scroll := d.scroll[g.Active]
			if scroll == nil {
				scroll = &widget.List{List: layout.List{Axis: layout.Vertical}}
				d.scroll[g.Active] = scroll
			}
			return scroll.Layout(gtx, 1, func(gtx layout.Context, _ int) layout.Dimensions {
				return u.panelContent(gtx, g.Active)
			})
		})
	})
}

func tabWidth(panel string) int {
	switch panel {
	case panelProperties:
		return 77
	case panelTimeline:
		return 68
	default:
		return 58
	}
}

func (u *window) panelTabButton(gtx layout.Context, id, title, label string, selected bool, action func()) layout.Dimensions {
	c := u.click(id)
	for c.Clicked(gtx) {
		action()
		gtx.Execute(op.InvalidateCmd{})
	}
	return c.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		semantic.Button.Add(gtx.Ops)
		semantic.LabelOp(label).Add(gtx.Ops)
		semantic.SelectedOp(selected).Add(gtx.Ops)
		if selected || gtx.Focused(c) {
			backdrop(gtx, accent)
		}
		return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			l := material.Label(u.theme, 12, title)
			l.MaxLines = 1
			return layout.Inset{Left: 3, Right: 3}.Layout(gtx, l.Layout)
		})
	})
}

func (d *dockWidgets) handle(gtx layout.Context, region dockRegion) {
	if region.rect.Empty() {
		return
	}
	if d.handles == nil {
		d.handles = make(map[dockHandle]*dockPointer)
	}
	handler := d.handles[region.handle]
	if handler == nil {
		handler = new(dockPointer)
		handler.gesture.Update(gtx.Metric, gtx.Source, giogesture.Both)
		d.handles[region.handle] = handler
	}
	handler.visible = true
	d.regions = append(d.regions, region)
	within(gtx, region.rect, func(gtx layout.Context) layout.Dimensions {
		if region.handle.kind == movePanel {
			defer pointer.PassOp{}.Push(gtx.Ops).Pop()
		}
		switch region.handle.kind {
		case moveGroup, movePanel:
			pointer.CursorGrab.Add(gtx.Ops)
		default:
			if (region.handle.side == dockBottom) == (region.handle.kind == resizeDock) {
				pointer.CursorRowResize.Add(gtx.Ops)
			} else {
				pointer.CursorColResize.Add(gtx.Ops)
			}
		}
		handler.gesture.Add(gtx.Ops)
		return layout.Dimensions{Size: region.rect.Size()}
	})
}

func (d *dockWidgets) addDrop(target dockDrop, rect image.Rectangle) {
	if !rect.Empty() {
		d.drops = append(d.drops, dropRegion{target, rect, rect})
	}
}

func (d *dockWidgets) dropAt(point image.Point) (dropRegion, bool) {
	for i := len(d.drops) - 1; i >= 0; i-- {
		if point.In(d.drops[i].hit) {
			return d.drops[i], true
		}
	}
	return dropRegion{}, false
}

func (d *dockWidgets) update(u *window, gtx layout.Context) {
	for _, region := range d.regions {
		h := region.handle
		handler := d.handles[h]
		handler.visible = false
		for {
			e, ok := handler.gesture.Update(gtx.Metric, gtx.Source, giogesture.Both)
			if !ok {
				break
			}
			point := e.Position.Add(f32.Pt(float32(region.rect.Min.X), float32(region.rect.Min.Y)))
			if e.Kind == pointer.Press && u.form == nil && !d.drag.held {
				d.drag = dockDrag{region: region, start: point, held: true, extent: u.workspace.Current[h.side].Extent}
				if h.kind == resizeGroups {
					_, a := u.workspace.Current.group(h.group)
					_, b := u.workspace.Current.group(h.next)
					if a != nil && b != nil {
						d.drag.weight, d.drag.nextWeight = a.Weight, b.Weight
					}
				}
			}
			if !d.drag.held || d.drag.region.handle != h {
				continue
			}
			switch e.Kind {
			case pointer.Drag:
				delta := point.Sub(d.drag.start)
				slop := float32(gtx.Dp(4))
				if d.drag.moved || delta.X*delta.X+delta.Y*delta.Y > slop*slop {
					d.drag.moved, d.drag.point = true, point.Round()
					d.resize(u, gtx, delta.Round())
					gtx.Execute(op.InvalidateCmd{})
				}
			case pointer.Release:
				if d.drag.moved {
					if h.kind >= resizeDock {
						d.resize(u, gtx, point.Sub(d.drag.start).Round())
						u.saveWorkspaceLayout()
					} else if drop, ok := d.dropAt(point.Round()); ok && u.workspace.Current.move(h.group, h.panel, drop.target) {
						u.saveWorkspaceLayout()
					}
					gtx.Execute(op.InvalidateCmd{})
				}
				d.drag = dockDrag{}
			case pointer.Cancel:
				u.cancelDockDrag()
			}
		}
	}
}

func (d *dockWidgets) resize(u *window, gtx layout.Context, delta image.Point) {
	r := d.drag.region
	h := r.handle
	switch h.kind {
	case resizeDock:
		change := delta.X
		low := 210
		switch h.side {
		case dockRight:
			change = -delta.X
		case dockBottom:
			change, low = -delta.Y, 116
		}
		scale := gtx.Metric.PxPerDp
		if scale == 0 {
			scale = 1
		}
		dp := float32(r.span+change) / scale
		u.workspace.Current[h.side].Extent = min(1400, max(low, int(dp+0.5)))
	case resizeGroups:
		_, a := u.workspace.Current.group(h.group)
		_, b := u.workspace.Current.group(h.next)
		if a == nil || b == nil || r.span == 0 {
			return
		}
		change := delta.Y
		if h.side == dockBottom {
			change = delta.X
		}
		total := d.drag.weight + d.drag.nextWeight
		if total < 2 {
			return
		}
		a.Weight = min(min(1000, total-1), max(max(1, total-1000), (r.offset+change)*total/r.span))
		b.Weight = total - a.Weight
	}
}

func (u *window) cancelDockDrag() bool {
	d := &u.docking
	if !d.drag.held {
		return false
	}
	h := d.drag.region.handle
	if d.drag.moved {
		switch h.kind {
		case resizeDock:
			u.workspace.Current[h.side].Extent = d.drag.extent
		case resizeGroups:
			_, a := u.workspace.Current.group(h.group)
			_, b := u.workspace.Current.group(h.next)
			if a != nil && b != nil {
				a.Weight, b.Weight = d.drag.weight, d.drag.nextWeight
			}
		}
	}
	d.drag = dockDrag{}
	return true
}
