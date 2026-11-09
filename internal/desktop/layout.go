package desktop

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"digiphoto/internal/document"
	"digiphoto/internal/media"
	"digiphoto/internal/project"

	"gioui.org/font/gofont"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

var (
	workspaceBG = color.NRGBA{R: 25, G: 28, B: 32, A: 255}
	panelBG     = color.NRGBA{R: 37, G: 41, B: 47, A: 255}
	buttonBG    = color.NRGBA{R: 48, G: 53, B: 61, A: 255}
	textPrimary = color.NRGBA{R: 240, G: 243, B: 247, A: 255}
	textMuted   = color.NRGBA{R: 171, G: 180, B: 193, A: 255}
	accent      = color.NRGBA{R: 42, G: 95, B: 154, A: 255}
	errorText   = color.NRGBA{R: 255, G: 166, B: 158, A: 255}
)

func newTheme() *material.Theme {
	theme := material.NewTheme()
	theme.Shaper = text.NewShaper(text.WithCollection(gofont.Collection()))
	theme.Palette.Bg, theme.Palette.Fg = panelBG, textPrimary
	theme.Palette.ContrastBg, theme.Palette.ContrastFg = accent, textPrimary
	return theme
}

type fieldSpec struct {
	label, value string
}

type form struct {
	title, hint string
	spec        []fieldSpec
	fields      []*widget.Editor
	submit      func([]string) bool
	browse      string
	entries     []project.Entry
	comparison  *exportPreview
	previewing  bool
	body        layout.Widget
}

func (u *window) show(title, hint string, fields []fieldSpec, submit func([]string) bool) {
	f := &form{title: title, hint: hint, spec: fields, submit: submit}
	for _, spec := range fields {
		e := &widget.Editor{SingleLine: true, Submit: true}
		e.SetText(spec.value)
		f.fields = append(f.fields, e)
	}
	u.form = f
}

func (u *window) click(id string) *widget.Clickable {
	c := u.buttons[id]
	if c == nil {
		c = new(widget.Clickable)
		u.buttons[id] = c
	}
	return c
}

func (u *window) button(id, title string, selected bool, action func()) layout.Widget {
	return u.buttonWithCorners(id, title, selected, 3, action)
}

func (u *window) workspaceButton(id, title string, selected bool, action func()) layout.Widget {
	// Rectangular controls avoid stencil passes when the workspace repaints.
	return u.buttonWithCorners(id, title, selected, 0, action)
}

func (u *window) buttonWithCorners(id, title string, selected bool, radius unit.Dp, action func()) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		c := u.click(id)
		for c.Clicked(gtx) {
			if action != nil {
				action()
			}
		}
		b := material.Button(u.theme, c, title)
		b.TextSize = unit.Sp(12)
		b.CornerRadius = radius
		b.Inset = layout.Inset{Top: 6, Bottom: 6, Left: 9, Right: 9}
		b.Background = buttonBG
		if selected {
			b.Background = accent
		}
		if action == nil {
			b.Color = textMuted
		}
		return layout.Inset{Right: 3, Bottom: 3}.Layout(gtx, b.Layout)
	}
}

func (u *window) text(text string, size unit.Sp, muted bool) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		l := material.Label(u.theme, size, text)
		if muted {
			l.Color = textMuted
		}
		return l.Layout(gtx)
	}
}

func row(gtx layout.Context, widgets ...layout.Widget) layout.Dimensions {
	children := make([]layout.FlexChild, 0, len(widgets))
	for _, w := range widgets {
		children = append(children, layout.Rigid(w))
	}
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx, children...)
}

// wrapRow keeps panel controls reachable when a group is resized or moved.
func wrapRow(gtx layout.Context, widgets ...layout.Widget) layout.Dimensions {
	x, y, lineHeight, width := 0, 0, 0, 0
	child := gtx
	child.Constraints.Min = image.Point{}
	for _, widget := range widgets {
		rec := op.Record(gtx.Ops)
		size := widget(child).Size
		call := rec.Stop()
		if x > 0 && x+size.X > gtx.Constraints.Max.X {
			x, y, lineHeight = 0, y+lineHeight, 0
		}
		shift := op.Offset(image.Pt(x, y)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		shift.Pop()
		x += size.X
		lineHeight, width = max(lineHeight, size.Y), max(width, x)
	}
	return layout.Dimensions{Size: gtx.Constraints.Constrain(image.Pt(width, y+lineHeight))}
}

func column(gtx layout.Context, widgets ...layout.Widget) layout.Dimensions {
	children := make([]layout.FlexChild, 0, len(widgets))
	for _, w := range widgets {
		children = append(children, layout.Rigid(w))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
}

func backdrop(gtx layout.Context, c color.NRGBA) {
	paint.FillShape(gtx.Ops, c, clip.Rect{Max: gtx.Constraints.Max}.Op())
}

func (u *window) entry(gtx layout.Context, editor *widget.Editor, label string) layout.Dimensions {
	return layout.Inset{Bottom: 9}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return column(gtx, u.text(label, 12, true), func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			bounds := image.Rect(0, 0, gtx.Constraints.Max.X, gtx.Dp(32))
			paint.FillShape(gtx.Ops, workspaceBG, clip.Rect(bounds).Op())
			return layout.Inset{Top: 7, Bottom: 7, Left: 7, Right: 7}.Layout(gtx, material.Editor(u.theme, editor, "").Layout)
		})
	})
}

func (u *window) layout(gtx layout.Context) {
	u.shortcuts(gtx)
	u.playback(gtx)
	backdrop(gtx, workspaceBG)
	layout.Stack{}.Layout(gtx, layout.Expanded(func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(u.toolbar),
			layout.Rigid(u.tabBar),
			layout.Flexed(1, u.workArea),
			layout.Rigid(u.statusBar),
		)
	}), layout.Stacked(func(gtx layout.Context) layout.Dimensions {
		if u.form == nil {
			return layout.Dimensions{}
		}
		return u.modal(gtx)
	}))
}

func (u *window) toolbar(gtx layout.Context) layout.Dimensions {
	t := u.active()
	var save, saveCopy, undo, redo, export, auto, before, fit func()
	if t != nil {
		s := t.editor.Snapshot()
		save = func() { u.chooseSave(t, false) }
		saveCopy = func() { u.chooseSave(t, true) }
		export = func() { u.chooseExport(t, "image") }
		fit = func() { t.fit = true }
		if s.Cursor > 0 {
			undo = func() { u.goHistory(t, t.editor.Snapshot().Cursor-1) }
		}
		if s.Cursor+1 < len(s.History) {
			redo = func() { u.goHistory(t, t.editor.Snapshot().Cursor+1) }
		}
		if s.State().Space != document.Data {
			auto = func() { u.levels(t) }
		}
		if l, ok := s.State().Layer(t.layer); ok && l.Kind == document.Adjustment {
			before = func() {
				if t.bypass == l.ID {
					t.bypass = 0
				} else {
					t.bypass = l.ID
				}
			}
		}
	}
	return layout.UniformInset(8).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return wrapRow(gtx, u.text("DigiPhoto   ", 17, false), u.workspaceButton("workspace", "Workspace", false, u.showWorkspaces),
			u.workspaceButton("new", "New", false, u.chooseNew),
			u.workspaceButton("open", "Open", false, u.chooseOpen), u.workspaceButton("save", "Save", false, save),
			u.workspaceButton("copy", "Save Copy", false, saveCopy), u.workspaceButton("export", "Export", false, export),
			u.workspaceButton("recover", "Recovery", false, func() { u.findRecovery(false) }),
			u.workspaceButton("undo", "Undo", false, undo), u.workspaceButton("redo", "Redo", false, redo),
			u.workspaceButton("auto", "Auto Levels", false, auto),
			u.workspaceButton("before", "Before / After", t != nil && t.bypass != 0, before),
			u.workspaceButton("fit", "Fit", false, fit))
	})
}

func (u *window) tabBar(gtx layout.Context) layout.Dimensions {
	if len(u.tabs) == 0 {
		return layout.Dimensions{}
	}
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	gtx.Constraints.Max.Y = gtx.Dp(32)
	tabs := slices.Clone(u.tabs)
	return layout.Inset{Left: 8, Right: 8}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return u.tabsList.Layout(gtx, len(tabs), func(gtx layout.Context, i int) layout.Dimensions {
			t := tabs[i]
			s := t.editor.Snapshot()
			name := t.name
			if s.Version != t.saved {
				name += " *"
			}
			return row(gtx, u.workspaceButton("tab-"+s.Project, name, u.index == i, func() {
				if j := slices.Index(u.tabs, t); j >= 0 {
					u.index = j
					u.stopPlayback()
					u.canvas.cancelGesture()
				}
			}), u.workspaceButton("close-"+s.Project, "Close", false, func() { u.confirmClose(t) }))
		})
	})
}

func (u *window) confirmClose(t *tab) {
	closeTab := func() {
		for i, candidate := range u.tabs {
			if t == candidate {
				u.tabs = append(u.tabs[:i], u.tabs[i+1:]...)
				u.index = min(u.index, max(0, len(u.tabs)-1))
				t.editor.Close()
				u.canvas.cancelGesture()
				u.stopPlayback()
				break
			}
		}
	}
	s := t.editor.Snapshot()
	if s.Version == t.saved {
		closeTab()
		return
	}
	status := u.journal.Status(s.Project)
	if status.Version != s.Version || status.Err != nil {
		u.setMessage("", errors.New("edits are pending recovery; save a copy or retry recovery before closing the tab"))
		return
	}
	u.show("Close project", "The current revision is available in Recovery.", nil, func(_ []string) bool {
		closeTab()
		return true
	})
}

func (u *window) toolRail(gtx layout.Context) layout.Dimensions {
	gtx.Constraints.Min.X = gtx.Dp(82)
	gtx.Constraints.Max.X = gtx.Constraints.Min.X
	gtx.Constraints.Min.Y = gtx.Constraints.Max.Y
	backdrop(gtx, panelBG)
	return layout.UniformInset(6).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		tools := []string{"Move", "Brush", "Eraser", "Select", "Crop", "Hand"}
		buttons := []layout.Widget{u.text("TOOLS", 11, true)}
		for _, name := range tools {
			buttons = append(buttons, u.workspaceButton("tool-"+name, name, u.tool == name, func() {
				u.tool = name
				u.canvas.cancelGesture()
			}))
		}
		return column(gtx, buttons...)
	})
}

func (u *window) statusBar(gtx layout.Context) layout.Dimensions {
	t := u.active()
	msg := u.message
	messageErr := u.messageErr
	if u.workspaceErr != nil && !messageErr {
		msg, messageErr = "Workspace settings: "+u.workspaceErr.Error(), true
	}
	if msg == "" {
		msg = "Offline workspace"
	}
	right := ""
	var retry func()
	if t != nil {
		s := t.editor.Snapshot()
		d := s.State().Bounds.Size()
		state := "Saved"
		if s.Version != t.saved {
			status := u.journal.Status(s.Project)
			switch {
			case status.Err != nil:
				state = "Recovery failed"
				msg, messageErr = status.Err.Error(), true
				retry = func() {
					u.schedule(t)
					u.setMessage("Retrying recovery", nil)
				}
			case status.Version == s.Version:
				state = "Recovered through current revision"
			default:
				state = "Recovery pending"
			}
		}
		right = fmt.Sprintf("%d × %d   ·   %s   ·   %.0f%%   ·   %s", d.X, d.Y, s.State().Space, t.zoom*100, state)
	}
	return layout.Inset{Top: 5, Bottom: 5, Left: 9, Right: 9}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				l := material.Label(u.theme, 12, msg)
				l.Color, l.MaxLines = textMuted, 1
				if messageErr {
					l.Color = errorText
				}
				return l.Layout(gtx)
			}),
			layout.Rigid(u.text(right, 12, true)),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				var actions []layout.Widget
				if retry != nil {
					actions = append(actions, u.workspaceButton("retry", "Retry recovery", false, retry))
				}
				if u.stopJob != nil {
					actions = append(actions, u.workspaceButton("canceljob", "Cancel "+u.job, false, u.stopJob))
				}
				if u.stopSave != nil {
					actions = append(actions, u.workspaceButton("cancelsave", "Cancel save", false, u.stopSave))
				}
				return row(gtx, actions...)
			}),
		)
	})
}

func (u *window) modal(gtx layout.Context) layout.Dimensions {
	f := u.form
	paint.FillShape(gtx.Ops, color.NRGBA{A: 160}, clip.Rect{Max: gtx.Constraints.Max}.Op())
	stack := clip.Rect{Max: gtx.Constraints.Max}.Push(gtx.Ops)
	event.Op(gtx.Ops, f)
	stack.Pop()
	return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = min(gtx.Constraints.Max.X, gtx.Dp(470))
		gtx.Constraints.Max.X = gtx.Constraints.Min.X
		var bounds image.Rectangle
		return layout.Stack{}.Layout(gtx,
			layout.Expanded(func(gtx layout.Context) layout.Dimensions {
				bounds.Max = gtx.Constraints.Min
				paint.FillShape(gtx.Ops, panelBG, clip.Rect(bounds).Op())
				return layout.Dimensions{Size: bounds.Max}
			}),
			layout.Stacked(func(gtx layout.Context) layout.Dimensions {
				return layout.UniformInset(18).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return u.modalContent(gtx, f)
				})
			}),
		)
	})
}

func (u *window) modalContent(gtx layout.Context, f *form) layout.Dimensions {
	widgets := []layout.Widget{u.text(f.title, 21, false), layout.Spacer{Height: 8}.Layout,
		u.text(f.hint, 13, true), layout.Spacer{Height: 12}.Layout}
	if f.comparison != nil {
		widgets = append(widgets, func(gtx layout.Context) layout.Dimensions {
			return u.formatComparison(gtx, f.comparison)
		}, layout.Spacer{Height: 10}.Layout)
	} else if f.previewing {
		widgets = append(widgets, u.text("Preparing comparison…", 12, true), layout.Spacer{Height: 8}.Layout)
	}
	for i, field := range f.fields {
		label := f.spec[i].label
		widgets = append(widgets, func(gtx layout.Context) layout.Dimensions { return u.entry(gtx, field, label) })
	}
	if f.browse != "" {
		widgets = append(widgets, u.button("modal-browse", "Browse…", false, func() {
			path, err := choosePath(u.native, f.browse)
			if err != nil {
				u.setMessage("", err)
			} else if path != "" {
				f.fields[0].SetText(path)
			}
		}))
	}
	for i, r := range f.entries {
		label := filepath.Base(r.Info.OriginalPath)
		if label == "." || label == "" {
			label = "Unsaved project"
		}
		label += fmt.Sprintf(" — revision %d (last save %d)", r.Info.Version, r.Info.Manual)
		widgets = append(widgets, u.button(fmt.Sprintf("recovery-%d", i), label, false, func() {
			u.form = nil
			u.open(r.Path, false, true)
		}))
	}
	if f.body != nil {
		widgets = append(widgets, f.body, layout.Spacer{Height: 8}.Layout)
	}
	var ok func()
	if f.submit != nil && !f.previewing {
		ok = func() {
			values := make([]string, len(f.fields))
			for i, field := range f.fields {
				values[i] = strings.TrimSpace(field.Text())
			}
			if f.submit(values) {
				u.form = nil
			}
		}
	}
	if u.messageErr {
		widgets = append(widgets, u.text(u.message, 12, false))
	}
	widgets = append(widgets, func(gtx layout.Context) layout.Dimensions {
		if ok == nil {
			return u.button("modal-cancel", "Close", false, func() { u.form = nil })(gtx)
		}
		return row(gtx, u.button("modal-submit", "Continue", true, ok),
			u.button("modal-cancel", "Cancel", false, func() { u.form = nil }))
	})
	return column(gtx, widgets...)
}

func (u *window) chooseNew() {
	u.show("New project", "Choose a canvas size and color space.", []fieldSpec{
		{"Width in pixels", "1024"}, {"Height in pixels", "768"}, {"Space  (srgb, display-p3, data)", "srgb"},
	}, func(v []string) bool {
		s := document.Space(v[2])
		if s != document.SRGB && s != document.P3 && s != document.Data {
			u.setMessage("", errors.New("choose srgb, display-p3, or data"))
			return false
		}
		return u.newProject(v[0], v[1], s)
	})
}

func (u *window) chooseOpen() {
	u.show("Open file", "Open an editable project or import an image.", []fieldSpec{
		{"File path", ""}, {"Interpret image as raw texture data  (yes / no)", "no"},
	}, func(v []string) bool {
		if v[0] == "" {
			u.setMessage("", errors.New("choose a file to open"))
			return false
		}
		u.open(v[0], strings.EqualFold(v[1], "yes"), false)
		return true
	})
	u.form.browse = "open"
}

func (u *window) chooseSave(t *tab, copy bool) {
	if t.path != "" && !copy {
		u.save(t, t.path, false)
		return
	}
	u.show("Save project", "Choose a new .dphoto file. The saved project includes all editable layers and frames.",
		[]fieldSpec{{"Project path", ""}}, func(v []string) bool {
			if v[0] == "" {
				u.setMessage("", errors.New("choose a project path"))
				return false
			}
			u.save(t, v[0], true)
			return true
		})
	u.form.browse = "save"
}

func (u *window) chooseExport(t *tab, mode string) {
	if u.stopJob != nil {
		u.setMessage("Finish or cancel the current export job first", nil)
		return
	}
	s := t.editor.Snapshot().State()
	r := s.Recipes[t.recipe]
	title, hint, field := "Export image", "Export from the current document revision using "+r.Name+".", "Output file path"
	if mode != "image" {
		title, field = "Export "+mode, "New output folder path"
		hint = "All files are prepared together and published as a new folder."
	}
	switch {
	case mode == "atlas":
		hint += " The folder contains atlas.png and atlas.json."
	case r.Format == "gif":
		hint += " GIF uses a 216-color palette, binary transparency, and 10 ms timing steps."
		if frame, ok := s.Frame(t.frame); ok && frame.Time.Duration() != media.GIFDelay(frame.Time) {
			hint += fmt.Sprintf(" The active frame changes from %s to %s.", frame.Time.Duration(), media.GIFDelay(frame.Time))
		}
	case r.Format == "jpg":
		hint += " JPEG uses 8-bit color and fills transparency with white."
	case r.Format == "webp":
		hint += " WebP uses 8-bit lossless color."
	}
	u.show(title, hint, []fieldSpec{{field, ""}}, func(v []string) bool {
		if v[0] == "" {
			u.setMessage("", errors.New("choose an output path"))
			return false
		}
		u.exportTo(t, v[0], mode)
		return true
	})
	if mode == "image" {
		u.form.browse = r.Format
		if r.Format == "gif" || r.Format == "jpg" {
			u.prepareComparison(u.form, s, t.frame, r.Format)
		}
	}
}

func (u *window) findRecovery(startup bool) {
	u.runJob("Finding recovery revisions", ioLane, func(ctx context.Context) completion {
		entries, err := project.Recoverable(ctx, u.recDir)
		return completion{err: err, message: "Recovery search complete", apply: func() {
			hint := "Select a revision to verify and open as a recovered project."
			if len(entries) == 0 {
				if startup {
					return
				}
				hint = "There are no unsaved recovery revisions."
			}
			u.form = &form{title: "Project recovery", hint: hint, entries: entries[:min(len(entries), 8)]}
		}}
	})
}

func (u *window) shortcuts(gtx layout.Context) {
	keys := []key.Filter{
		{Name: key.NameEscape}, {Name: "N", Required: key.ModCtrl}, {Name: "O", Required: key.ModCtrl},
		{Name: "S", Required: key.ModCtrl, Optional: key.ModShift}, {Name: "Z", Required: key.ModCtrl, Optional: key.ModShift},
		{Name: "Y", Required: key.ModCtrl}, {Name: "V"}, {Name: "B"}, {Name: "E"}, {Name: "M"}, {Name: "C"}, {Name: "H"},
	}
	filters := make([]event.Filter, len(keys))
	for i := range keys {
		filters[i] = keys[i]
	}
	for {
		e, ok := gtx.Event(filters...)
		if !ok {
			break
		}
		k := e.(key.Event)
		if k.State != key.Press {
			continue
		}
		if k.Name == key.NameEscape {
			if u.form != nil {
				u.form = nil
			} else if u.cancelDockDrag() {
				gtx.Execute(op.InvalidateCmd{})
			} else if u.stopEdit != nil {
				u.stopEdit()
			} else {
				u.canvas.cancelGesture()
				u.stopPlayback()
			}
			continue
		}
		if u.form != nil {
			continue
		}
		t := u.active()
		if !k.Modifiers.Contain(key.ModCtrl) {
			tools := map[key.Name]string{"V": "Move", "B": "Brush", "E": "Eraser", "M": "Select", "C": "Crop", "H": "Hand"}
			if tool := tools[k.Name]; tool != "" {
				u.tool = tool
				u.canvas.cancelGesture()
			}
			continue
		}
		switch {
		case k.Name == "N":
			u.chooseNew()
		case k.Name == "O":
			u.chooseOpen()
		case t == nil:
		case k.Name == "S":
			u.chooseSave(t, k.Modifiers.Contain(key.ModShift))
		case k.Name == "Y" || (k.Name == "Z" && k.Modifiers.Contain(key.ModShift)):
			u.goHistory(t, t.editor.Snapshot().Cursor+1)
		case k.Name == "Z":
			u.goHistory(t, t.editor.Snapshot().Cursor-1)
		}
	}
}

func number(value float64) string { return strconv.FormatFloat(value, 'f', -1, 64) }
