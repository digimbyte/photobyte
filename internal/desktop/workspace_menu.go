package desktop

import (
	"fmt"
	"maps"
	"slices"

	"gioui.org/layout"
	"gioui.org/widget"
)

func (u *window) saveWorkspaceLayout() {
	u.workspaceWriter.save(u.workspace)
}

func (u *window) activatePanel(panel string) {
	if u.workspace.Current.activate(panel) {
		u.saveWorkspaceLayout()
	}
}

func (u *window) restoreWorkspace(name string) {
	u.cancelDockDrag()
	u.workspace.restore(name)
	u.saveWorkspaceLayout()
	u.form = nil
}

func (u *window) showWorkspaces() {
	u.cancelDockDrag()
	hint := "Drag tabs to group them. Drag a group by its handle or drag a divider to resize a dock."
	if u.workspaceWriter == nil {
		hint += " Layout changes are temporary in Safe Mode."
	}
	u.show("Workspace", hint, nil, nil)
	list := &widget.List{List: layout.List{Axis: layout.Vertical}}
	u.form.body = func(gtx layout.Context) layout.Dimensions {
		names := append([]string{defaultWorkspace}, slices.Sorted(maps.Keys(u.workspace.Saved))...)
		controls := []layout.Widget{
			u.text("Saved workspaces", 14, false),
			func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Max.Y = min(gtx.Constraints.Max.Y, gtx.Dp(200))
				return list.Layout(gtx, len(names), func(gtx layout.Context, i int) layout.Dimensions {
					name := names[i]
					id := fmt.Sprint(i)
					return layout.Flex{}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							if i == 0 {
								return layout.Dimensions{}
							}
							return u.button("workspace-remove-"+id, "Remove", false, func() { u.removeWorkspace(name) })(gtx)
						}),
						layout.Flexed(1, u.button("workspace-open-"+id, name, name == u.workspace.Preset, func() { u.restoreWorkspace(name) })))
				})
			},
			func(gtx layout.Context) layout.Dimensions {
				return wrapRow(gtx,
					u.button("workspace-save", "Save Workspace…", true, u.nameWorkspace),
					u.button("workspace-reset", "Reset Layout", false, func() {
						u.restoreWorkspace(u.workspace.Preset)
						if t := u.active(); t != nil {
							t.fit = true
						}
					}))
			},
			layout.Spacer{Height: 8}.Layout,
			u.text("Panels", 14, false),
			func(gtx layout.Context) layout.Dimensions {
				var buttons []layout.Widget
				for _, panel := range panelNames {
					_, g, i := u.workspace.Current.panel(panel)
					if g == nil {
						continue
					}
					visible := !g.Tabs[i].Hidden
					buttons = append(buttons, u.button("workspace-toggle-"+panel, panel, visible, func() {
						if visible {
							u.workspace.Current.hide(panel)
							u.saveWorkspaceLayout()
						} else {
							u.activatePanel(panel)
						}
					}))
				}
				return wrapRow(gtx, buttons...)
			},
		}
		if u.workspaceErr != nil {
			controls = append(controls, u.text(u.workspaceErr.Error(), 12, true))
		}
		return column(gtx, controls...)
	}
}

func (u *window) nameWorkspace() {
	name := u.workspace.Preset
	if name == defaultWorkspace {
		name = ""
	}
	u.show("Save workspace", "Save the current arrangement under a name. Reusing a name updates its layout.",
		[]fieldSpec{{"Workspace name", name}}, func(values []string) bool {
			if err := u.workspace.save(values[0]); err != nil {
				u.setMessage("", err)
				return false
			}
			u.saveWorkspaceLayout()
			u.setMessage("Workspace layout updated", nil)
			return true
		})
}

func (u *window) removeWorkspace(name string) {
	u.show("Remove workspace", fmt.Sprintf("Remove the saved layout %q?", name), nil, func(_ []string) bool {
		delete(u.workspace.Saved, name)
		if u.workspace.Preset == name {
			u.workspace.Preset = defaultWorkspace
		}
		u.saveWorkspaceLayout()
		return true
	})
}

func (u *window) showPanelGroup(id int) {
	u.cancelDockDrag()
	side, g := u.workspace.Current.group(id)
	if g == nil {
		return
	}
	u.show("Panel group", dockNames[side]+" dock", nil, nil)
	u.form.body = func(gtx layout.Context) layout.Dimensions {
		side, g := u.workspace.Current.group(id)
		if g == nil {
			return layout.Dimensions{}
		}
		choose := func(gtx layout.Context) layout.Dimensions {
			var buttons []layout.Widget
			for _, t := range g.Tabs {
				buttons = append(buttons, u.button("group-select-"+t.Name, t.Name, t.Name == g.Active, func() {
					u.activatePanel(t.Name)
					u.form = nil
				}))
			}
			return wrapRow(gtx, buttons...)
		}
		toggle := "Collapse Group"
		if g.Collapsed {
			toggle = "Expand Group"
		}
		options := func(gtx layout.Context) layout.Dimensions {
			return wrapRow(gtx, u.button("group-collapse", toggle, false, func() {
				g.Collapsed = !g.Collapsed
				u.saveWorkspaceLayout()
				u.form = nil
			}), u.button("group-hide-active", "Hide "+g.Active, false, func() {
				u.workspace.Current.hide(g.Active)
				u.saveWorkspaceLayout()
				u.form = nil
			}))
		}
		move := func(panel string, target dockDrop) {
			if u.workspace.Current.move(id, panel, target) {
				u.saveWorkspaceLayout()
				u.form = nil
			}
		}
		docks := func(panel string) layout.Widget {
			return func(gtx layout.Context) layout.Dimensions {
				var buttons []layout.Widget
				for dock := dockLeft; dock < dockCount; dock++ {
					if dock == side && panel == "" {
						continue
					}
					buttons = append(buttons, u.button(fmt.Sprintf("group-dock-%s-%d", panel, dock), dockNames[dock], false,
						func() { move(panel, dockDrop{side: dock}) }))
				}
				return wrapRow(gtx, buttons...)
			}
		}
		items := []layout.Widget{choose, options, u.text("Dock group", 13, true), docks("")}
		if len(g.Tabs) > 1 {
			items = append(items, u.text("Separate "+g.Active+" into a new group", 13, true), docks(g.Active))
		}
		var merge []layout.Widget
		for dock, d := range u.workspace.Current {
			for _, target := range d.Groups {
				if target.ID != id && target.Active != "" {
					merge = append(merge, u.button(fmt.Sprintf("group-join-%d", target.ID), target.Active, false,
						func() { move("", dockDrop{side: dockSide(dock), group: target.ID}) }))
				}
			}
		}
		if len(merge) > 0 {
			items = append(items, u.text("Group with", 13, true),
				func(gtx layout.Context) layout.Dimensions { return wrapRow(gtx, merge...) })
		}
		return column(gtx, items...)
	}
}
