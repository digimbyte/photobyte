// Package desktop binds the Go editor engine to a native Gio workspace.
package desktop

import (
	"context"
	"errors"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"digiphoto/internal/document"
	"digiphoto/internal/exporter"
	"digiphoto/internal/media"
	"digiphoto/internal/project"
	"digiphoto/internal/render"
	"digiphoto/internal/tile"

	"gioui.org/app"
	"gioui.org/f32"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

type Options struct {
	DataDir   string
	Open      string
	QuitAfter time.Duration
	SafeMode  bool
}

type tab struct {
	editor    *document.Editor
	path      string
	name      string
	saved     uint64
	layer     document.ID
	frame     document.ID
	selection image.Rectangle
	zoom      float32
	origin    f32.Point
	fit       bool
	recipe    int
	onion     bool
	bypass    document.ID
}

type completion struct {
	message string
	apply   func()
	err     error
	lane    jobLane
}

type jobLane uint8

const (
	ioLane jobLane = iota
	editLane
	saveLane
)

type window struct {
	win     *app.Window
	native  uintptr
	theme   *material.Theme
	store   *tile.Store
	codec   *media.Broker
	pool    *render.Pool
	export  exporter.Exporter
	journal *project.Journal
	recDir  string
	ctx     context.Context
	results chan completion
	workers sync.WaitGroup

	tabs       []*tab
	index      int
	job        string
	stopJob    context.CancelFunc
	stopEdit   context.CancelFunc
	stopSave   context.CancelFunc
	message    string
	messageErr bool

	buttons         map[string]*widget.Clickable
	workspace       workspaceState
	workspaceWriter *workspaceWriter
	workspaceErr    error
	docking         dockWidgets

	tool      string
	mask      bool
	ink       tile.Pixel
	radius    float64
	playAt    time.Time
	playIndex int

	tabsList, frameList   widget.List
	form                  *form
	canvas                viewport
	frameProbe            func(layout.Context)
	frameProbeAfterLayout func(layout.Context)
	closeRequested        atomic.Bool
}

func Run(opts Options) error {
	if err := os.MkdirAll(opts.DataDir, 0o700); err != nil {
		return err
	}
	scratch, err := os.MkdirTemp(opts.DataDir, "session-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)
	store, err := tile.NewStore(filepath.Join(scratch, "tiles"), 512<<20)
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	win := new(app.Window)
	win.Option(app.Title("DigiPhoto"), app.Size(unit.Dp(1380), unit.Dp(870)), app.MinSize(unit.Dp(920), unit.Dp(610)))
	recDir := filepath.Join(opts.DataDir, "recovery")
	journal, err := project.NewJournal(recDir, store, win.Invalidate)
	if err != nil {
		return err
	}
	defer journal.Close()
	// Reserve presentation memory for the working, queued, and visible strips.
	pool := render.NewPool((128 << 20) - 3*stripByteLimit)
	defer pool.Close()
	codec := media.NewBroker(exe, scratch)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	u := &window{
		win: win, theme: newTheme(), store: store, codec: codec, pool: pool,
		export:  exporter.Exporter{Store: store, Codec: codec, Scratch: scratch},
		journal: journal, recDir: recDir, ctx: ctx, results: make(chan completion, 4),
		buttons: make(map[string]*widget.Clickable),
		tool:    "Move", radius: 12, ink: tile.Pixel{R: 65535, G: 65535, B: 65535, A: 65535},
		workspace: defaultWorkspaceState(),
	}
	if !opts.SafeMode {
		path := filepath.Join(opts.DataDir, "workspace.json")
		u.workspace, u.workspaceErr = readWorkspace(path)
		u.workspaceWriter = startWorkspaceWriter(path, win.Invalidate)
		defer u.workspaceWriter.close()
	}
	u.tabsList.Axis, u.frameList.Axis = layout.Horizontal, layout.Horizontal
	releaseThread := prioritizeLayoutThread()
	defer releaseThread()
	u.canvas.start(u)
	defer u.canvas.close()
	installCloseHandler(func() {
		u.closeRequested.Store(true)
		win.Invalidate()
	})
	defer installCloseHandler(nil)
	probing := startReleaseProbe(u)
	if !probing && opts.Open != "" {
		u.open(opts.Open, false, false)
	} else if !probing {
		u.findRecovery(true)
	}
	var closeAt time.Time
	if opts.QuitAfter > 0 {
		closeAt = time.Now().Add(opts.QuitAfter)
	}
	var ops op.Ops
	for {
		switch e := win.Event().(type) {
		case app.DestroyEvent:
			cancel()
			u.workers.Wait()
			u.receive()
			for _, t := range u.tabs {
				if t.editor.Snapshot().Version != t.saved {
					u.schedule(t)
				}
				t.editor.Close()
			}
			return e.Err
		case app.FrameEvent:
			u.receive()
			if w := u.workspaceWriter; w != nil {
				select {
				case u.workspaceErr = <-w.results:
				default:
				}
			}
			gtx := app.NewContext(&ops, e)
			if u.closeRequested.Swap(false) {
				u.requestClose()
			}
			if u.frameProbe != nil {
				u.frameProbe(gtx)
			}
			if !closeAt.IsZero() {
				if !e.Now.Before(closeAt) {
					u.closeNow()
				} else {
					gtx.Execute(op.InvalidateCmd{At: closeAt})
				}
			}
			u.layout(gtx)
			if u.frameProbeAfterLayout != nil {
				u.frameProbeAfterLayout(gtx)
			}
			e.Frame(gtx.Ops)
		default:
			if handle := windowHandle(e); handle != 0 {
				u.native = handle
			}
		}
	}
}

func (u *window) requestClose() {
	for _, t := range u.tabs {
		s := t.editor.Snapshot()
		status := u.journal.Status(s.Project)
		if s.Version != t.saved && (status.Version < s.Version || status.Err != nil) {
			u.show("Edits are still in memory", "Save a copy or retry recovery before closing DigiPhoto.", nil, nil)
			return
		}
	}
	u.closeNow()
}

func (u *window) closeNow() {
	installCloseHandler(nil)
	u.win.Perform(system.ActionClose)
}

func (u *window) active() *tab {
	if u.index < 0 || u.index >= len(u.tabs) {
		return nil
	}
	return u.tabs[u.index]
}

func (u *window) add(s document.Snapshot, name, path string, saved uint64) {
	for i, t := range u.tabs {
		if t.editor.Snapshot().Project == s.Project {
			u.index = i
			u.setMessage("Project is already open", nil)
			return
		}
	}
	t := &tab{editor: document.Open(s), name: name, path: path, saved: saved, zoom: 1, fit: true}
	t.normalize()
	u.tabs = append(u.tabs, t)
	u.index = len(u.tabs) - 1
	u.stopPlayback()
	if saved == 0 {
		u.schedule(t)
	}
}

func (t *tab) normalize() {
	s := t.editor.Snapshot().State()
	if _, ok := s.Layer(t.layer); !ok {
		t.layer = 0
		if len(s.Layers) > 0 {
			t.layer = s.Layers[len(s.Layers)-1].ID
		}
	}
	if _, ok := s.Frame(t.frame); !ok {
		t.frame = s.Frames[0].ID
	}
	t.recipe = min(max(t.recipe, 0), max(0, len(s.Recipes)-1))
	t.selection = t.selection.Intersect(s.Bounds)
}

func (u *window) setMessage(message string, err error) {
	u.messageErr = err != nil
	if err != nil {
		u.message = err.Error()
	} else if message != "" {
		u.message = message
	}
}

func (u *window) runJob(name string, lane jobLane, work func(context.Context) completion) {
	stop := &u.stopJob
	switch lane {
	case editLane:
		stop = &u.stopEdit
	case saveLane:
		stop = &u.stopSave
	}
	if *stop != nil {
		u.setMessage("A job is already running", nil)
		return
	}
	ctx, cancel := context.WithCancel(u.ctx)
	*stop = cancel
	if lane == ioLane {
		u.job = name
	}
	u.setMessage(name+"…", nil)
	u.workers.Go(func() {
		defer cancel()
		r := work(ctx)
		r.lane = lane
		select {
		case u.results <- r:
			if u.ctx.Err() == nil {
				u.win.Invalidate()
			}
		case <-u.ctx.Done():
		}
	})
}

func (u *window) receive() {
	for {
		select {
		case r := <-u.results:
			switch r.lane {
			case editLane:
				u.stopEdit = nil
				u.canvas.pending = nil
			case saveLane:
				u.stopSave = nil
			default:
				u.stopJob, u.job = nil, ""
				if u.form != nil {
					u.form.previewing = false
				}
			}
			if r.apply != nil && r.err == nil {
				r.apply()
			}
			if errors.Is(r.err, context.Canceled) {
				u.setMessage("Job canceled", nil)
			} else {
				u.setMessage(r.message, r.err)
			}
		default:
			return
		}
	}
}

func (u *window) open(path string, raw, recovery bool) {
	abs, err := filepath.Abs(path)
	if err != nil {
		u.setMessage("", err)
		return
	}
	path = abs
	u.runJob("Opening", ioLane, func(ctx context.Context) completion {
		var s document.Snapshot
		var saved uint64
		var warning string
		var err error
		dest := path
		name := filepath.Base(path)
		if strings.EqualFold(filepath.Ext(path), ".dphoto") || recovery {
			var info project.Info
			s, info, err = project.Load(ctx, path, u.store)
			saved = info.Version
			if recovery {
				name, dest, saved = "Recovered project", "", 0
				if info.OriginalPath != "" {
					name = "Recovered " + filepath.Base(info.OriginalPath)
				}
			}
		} else {
			var state *document.State
			state, warning, err = u.codec.Import(ctx, path, u.store, raw)
			if err == nil {
				state.Layers[0].Name = "Source"
				s = document.NewSnapshot(state, "Import image")
			}
			dest = ""
		}
		if err != nil {
			return completion{err: err}
		}
		if warning == "" {
			warning = "Opened " + name
		}
		return completion{message: warning, apply: func() { u.add(s, name, dest, saved) }}
	})
}

func (u *window) schedule(t *tab) {
	u.journal.Submit(t.editor.Snapshot(), t.saved, t.path)
}

func (u *window) place(t *tab, path string) {
	s := t.editor.Snapshot()
	u.runJob("Placing image", ioLane, func(ctx context.Context) completion {
		src, warning, err := u.codec.Import(ctx, path, u.store, s.State().Space == document.Data)
		if err != nil {
			return completion{err: err}
		}
		scene := render.PosterScene(u.store, src, s.State().Space)
		if scene == nil {
			scene = render.NewScene(u.store, src, src.Frames[0].ID, s.State().Space, 0)
		}
		sample := scene.Sampler()
		grid, err := u.store.FromImage(ctx, sample)
		if err != nil || sample.Err() != nil {
			return completion{err: errors.Join(err, sample.Err())}
		}
		name := filepath.Base(path)
		if len(name) > 128 {
			name = "Placed image"
		}
		err = t.editor.Apply(ctx, s.Version, document.PlaceRaster{Name: name, Grid: grid})
		if warning == "" {
			warning = "Placed " + name
		}
		return completion{message: warning, err: err, apply: func() {
			t.layer = s.State().NextID
			u.schedule(t)
		}}
	})
}

func (u *window) save(t *tab, dest string, copy bool) {
	path, err := filepath.Abs(dest)
	if err != nil {
		u.setMessage("", err)
		return
	}
	dest = path
	if !strings.EqualFold(filepath.Ext(dest), ".dphoto") {
		dest += ".dphoto"
	}
	s := t.editor.Snapshot()
	var previous uint64
	if !copy {
		previous = t.saved
	}
	u.runJob("Saving project", saveLane, func(ctx context.Context) completion {
		if err := project.Save(ctx, dest, u.store, s, previous); err != nil {
			return completion{err: err}
		}
		return completion{message: fmt.Sprintf("Saved revision %d", s.Version), apply: func() {
			t.path, t.name, t.saved = dest, filepath.Base(dest), s.Version
			u.schedule(t)
		}}
	})
}

func (u *window) apply(t *tab, cmd document.Command) bool {
	s := t.editor.Snapshot()
	if err := t.editor.Apply(u.ctx, s.Version, cmd); err != nil {
		u.setMessage("", err)
		return false
	}
	t.normalize()
	u.schedule(t)
	return true
}

func (u *window) goHistory(t *tab, i int) {
	s := t.editor.Snapshot()
	if err := t.editor.History(u.ctx, s.Version, i); err != nil {
		u.setMessage("", err)
		return
	}
	t.bypass = 0
	t.normalize()
	u.schedule(t)
}

func (u *window) levels(t *tab) {
	s, frame := t.editor.Snapshot(), t.frame
	u.runJob("Analyzing Auto Levels", editLane, func(ctx context.Context) completion {
		state := s.State()
		l, err := render.AutoLevels(ctx, render.NewScene(u.store, state, frame, state.Space, 0))
		if err != nil {
			return completion{err: err}
		}
		if l == document.NeutralLevels() {
			return completion{message: "Auto Levels found no tonal range to adjust"}
		}
		err = t.editor.Apply(ctx, s.Version, document.AddLayer{Kind: document.Adjustment, Name: "Auto Levels", Levels: l})
		return completion{err: err, message: "Auto Levels added. Use Before/After to compare.", apply: func() {
			t.layer = s.State().NextID
			u.schedule(t)
			if t == u.active() {
				u.activatePanel(panelProperties)
			}
		}}
	})
}

func (u *window) paint(t *tab, stroke document.Stroke) {
	s := t.editor.Snapshot()
	u.runJob("Applying stroke", editLane, func(ctx context.Context) completion {
		cmd, err := document.PrepareStroke(ctx, u.store, s.State(), stroke)
		if err == nil {
			err = t.editor.Apply(ctx, s.Version, cmd)
		}
		return completion{err: err, message: "Stroke applied", apply: func() { u.schedule(t) }}
	})
}

func (u *window) exportTo(t *tab, path string, mode string) {
	s, frame := t.editor.Snapshot().State(), t.frame
	r := document.DefaultRecipe()
	if len(s.Recipes) > 0 {
		r = s.Recipes[t.recipe]
	}
	u.runJob("Rendering export", ioLane, func(ctx context.Context) completion {
		var err error
		switch mode {
		case "atlas":
			err = u.export.Atlas(ctx, path, s, r)
		case "slices":
			err = u.export.Slices(ctx, path, s, frame, r)
		default:
			if filepath.Ext(path) == "" {
				path += "." + r.Format
			}
			err = u.export.Image(ctx, path, s, frame, s.Bounds, r)
		}
		return completion{message: "Exported " + filepath.Base(path), err: err}
	})
}

func (u *window) stopPlayback() { u.playAt = time.Time{} }

func (u *window) newProject(width, height string, space document.Space) bool {
	w, ew := strconv.Atoi(width)
	h, eh := strconv.Atoi(height)
	if ew != nil || eh != nil || w < 1 || h < 1 || w > document.MaxSide || h > document.MaxSide {
		u.setMessage("", errors.New("enter canvas dimensions from 1 to 16384 pixels"))
		return false
	}
	u.add(document.NewSnapshot(document.NewState(w, h, space), "New project"), "Untitled", "", 0)
	u.setMessage("New project", nil)
	return true
}
