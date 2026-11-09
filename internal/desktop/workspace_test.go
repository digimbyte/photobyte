package desktop

import (
	"image"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget"
)

func TestWorkspaceRoundTrip(t *testing.T) {
	state := defaultWorkspaceState()
	if !state.Current.move(2, panelHistory, dockDrop{side: dockLeft}) ||
		!state.Current.move(3, "", dockDrop{side: dockRight, group: 2, beforeTab: panelLayers}) {
		t.Fatal("could not customize layout")
	}
	state.Current.hide(panelAssets)
	_, group, _ := state.Current.panel(panelHistory)
	group.Collapsed, group.Weight = true, 80
	state.Current[dockLeft].Extent = 333
	if err := state.save("  Sprites  "); err != nil {
		t.Fatal(err)
	}
	saved := state.Current.clone()
	state.Current.activate(panelAssets)
	state.Current[dockLeft].Extent = 372
	if !reflect.DeepEqual(state.Saved["Sprites"], saved) {
		t.Fatal("current layout modified the saved preset")
	}
	path := filepath.Join(t.TempDir(), "workspace.json")
	writer := startWorkspaceWriter(path, func() {})
	for extent := 373; extent <= 400; extent++ {
		state.Current[dockLeft].Extent = extent
		writer.save(state)
	}
	if err := writer.close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := readWorkspace(path)
	if err != nil || !reflect.DeepEqual(restarted, state) {
		t.Fatalf("workspace after restart = %#v, %v", restarted, err)
	}
	restarted.restore("Sprites")
	if !reflect.DeepEqual(restarted.Current, saved) {
		t.Fatal("restoring preset lost its tab placement, visibility, or sizes")
	}
	restarted.restore(defaultWorkspace)
	if !reflect.DeepEqual(restarted.Current, defaultLayout()) {
		t.Fatal("default layout changed")
	}
	if err := os.WriteFile(path, []byte(`{"version":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	restarted, err = readWorkspace(path)
	if err == nil || !reflect.DeepEqual(restarted.Current, defaultLayout()) {
		t.Fatalf("unsupported workspace fallback = %#v, %v", restarted, err)
	}
}

func TestWorkspaceDockInput(t *testing.T) {
	u := &window{theme: newTheme(), buttons: make(map[string]*widget.Clickable), workspace: defaultWorkspaceState(), tool: "Move"}
	u.canvas.u = u
	var router input.Router
	var ops op.Ops
	gtx := layout.Context{Constraints: layout.Exact(image.Pt(1150, 650)), Ops: &ops,
		Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Source: router.Source()}
	frame := func(events ...event.Event) {
		router.Queue(events...)
		ops.Reset()
		gtx.Now = time.Now()
		u.shortcuts(gtx)
		u.workArea(gtx)
		router.Frame(&ops)
	}
	send := func(kind pointer.Kind, at image.Point) {
		var buttons pointer.Buttons
		if kind != pointer.Release {
			buttons = pointer.ButtonPrimary
		}
		frame(pointer.Event{Kind: kind, Buttons: buttons, PointerID: 1, Source: pointer.Mouse,
			Position: f32.Pt(float32(at.X), float32(at.Y))})
	}
	grab := func(kind dockHandleKind, group int, panel string) image.Point {
		for _, r := range u.docking.regions {
			if r.handle.kind == kind && r.handle.group == group && r.handle.panel == panel {
				return r.rect.Min.Add(r.rect.Size().Div(2))
			}
		}
		t.Fatalf("missing handle for group %d, panel %q", group, panel)
		return image.Point{}
	}
	drop := func(target dockDrop) image.Point {
		for _, r := range u.docking.drops {
			if r.target == target {
				if target.group != 0 && target.beforeTab == "" {
					// The right side of the header has no tab insertion zones.
					return image.Pt(r.hit.Max.X-4, r.hit.Min.Y+r.hit.Dy()/2)
				}
				return r.hit.Min.Add(r.hit.Size().Div(2))
			}
		}
		t.Fatalf("missing drop target %+v", target)
		return image.Point{}
	}
	drag := func(from, to image.Point) {
		send(pointer.Press, from)
		send(pointer.Move, to)
		if !u.docking.drag.moved {
			t.Fatalf("drag did not capture %v → %v", from, to)
		}
		send(pointer.Release, to)
	}
	frame()
	pos := grab(movePanel, 2, panelHistory)
	send(pointer.Press, pos)
	send(pointer.Release, pos)
	if _, g, _ := u.workspace.Current.panel(panelHistory); g.Active != panelHistory {
		t.Fatal("tab click did not select the panel")
	}
	drag(grab(movePanel, 2, panelHistory), drop(dockDrop{side: dockRight, group: 2, beforeTab: panelLayers}))
	_, g, _ := u.workspace.Current.panel(panelHistory)
	if !slices.EqualFunc(g.Tabs, []string{panelHistory, panelLayers, panelAssets}, func(tab panelTab, name string) bool { return tab.Name == name }) {
		t.Fatalf("tab reorder = %+v", g.Tabs)
	}
	drag(grab(movePanel, 2, panelHistory), drop(dockDrop{side: dockRight, group: 1}))
	if _, g, _ := u.workspace.Current.panel(panelHistory); g.ID != 1 || len(g.Tabs) != 2 {
		t.Fatal("tab did not move between groups")
	}
	drag(grab(moveGroup, 1, ""), drop(dockDrop{side: dockLeft}))
	if side, g := u.workspace.Current.group(1); side != dockLeft || len(g.Tabs) != 2 {
		t.Fatal("group did not dock on the left")
	}
	frame()
	start := grab(resizeDock, 0, "")
	send(pointer.Press, start)
	send(pointer.Move, start.Add(image.Pt(42, 0)))
	if got := u.workspace.Current[dockLeft].Extent; got != 342 {
		t.Fatalf("resize preview width = %d", got)
	}
	frame(key.Event{Name: key.NameEscape, State: key.Press})
	send(pointer.Release, start.Add(image.Pt(42, 0)))
	if got := u.workspace.Current[dockLeft].Extent; got != 300 {
		t.Fatalf("cancelled resize width = %d", got)
	}
	drag(grab(resizeDock, 0, ""), start.Add(image.Pt(26, 0)))
	if got := u.workspace.Current[dockLeft].Extent; got != 326 || !u.workspace.Current.valid() {
		t.Fatalf("final resize width = %d", got)
	}
	drag(grab(moveGroup, 1, ""), drop(dockDrop{side: dockRight, beforeGroup: 2}))
	right := &u.workspace.Current[dockRight]
	if len(right.Groups) != 2 || right.Groups[0].ID != 1 || right.Groups[1].ID != 2 {
		t.Fatalf("group stack = %+v", right.Groups)
	}
	split := grab(resizeGroups, 1, "")
	before := right.Groups[0].Weight
	total := before + right.Groups[1].Weight
	drag(split, split.Add(image.Pt(0, 36)))
	if right.Groups[0].Weight <= before || right.Groups[0].Weight+right.Groups[1].Weight != total {
		t.Fatalf("group splitter weights = %+v", right.Groups)
	}
}
