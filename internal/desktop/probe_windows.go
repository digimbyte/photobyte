//go:build windows && releaseprobe

package desktop

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"time"

	"github.com/digimbyte/photobyte/internal/document"
	"github.com/digimbyte/photobyte/internal/project"
	"github.com/digimbyte/photobyte/internal/tile"

	"gioui.org/app"
	"gioui.org/f32"
	"gioui.org/io/pointer"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	"golang.org/x/sys/windows"
)

type releaseReport struct {
	Failure            string  `json:"failure,omitempty"`
	TimingMethod       string  `json:"timing_method"`
	Width              int     `json:"width"`
	Height             int     `json:"height"`
	Layers             int     `json:"layers"`
	ViewportWidth      int     `json:"viewport_width"`
	ViewportHeight     int     `json:"viewport_height"`
	GoWorkers          int     `json:"go_workers"`
	HardwareSamples    int     `json:"hardware_samples"`
	WARPSamples        int     `json:"warp_samples"`
	HardwareShown      int     `json:"hardware_presented_samples"`
	WARPShown          int     `json:"warp_presented_samples"`
	HardwareComposited int     `json:"hardware_composited_samples"`
	WARPComposited     int     `json:"warp_composited_samples"`
	OccludedFrames     uint64  `json:"occluded_frames"`
	HardwareP95        float64 `json:"hardware_p95_ms"`
	WARPP95            float64 `json:"warp_p95_ms"`
	HardwareLayout     float64 `json:"hardware_layout_p95_ms"`
	HardwarePresent    float64 `json:"hardware_present_call_p95_ms"`
	WARPLayout         float64 `json:"warp_layout_p95_ms"`
	WARPPresent        float64 `json:"warp_present_call_p95_ms"`
	HardwareSubmitted  float64 `json:"hardware_submit_p95_ms"`
	WARPSubmitted      float64 `json:"warp_submit_p95_ms"`
	RecoveryMax        float64 `json:"recovery_max_ms"`
	Losses             uint32  `json:"injected_device_losses"`
	Strokes            int     `json:"committed_strokes"`
	Exported           bool    `json:"export_completed"`
	Reopened           bool    `json:"saved_and_reopened"`
	CloseProtected     bool    `json:"pending_edit_close_guard"`
}

type sampleTiming struct {
	Driver       string  `json:"driver"`
	Sequence     uint64  `json:"sequence"`
	Layout       float64 `json:"layout_ms"`
	DrawBegan    float64 `json:"draw_began_ms"`
	PresentBegan float64 `json:"present_began_ms"`
	Submitted    float64 `json:"submitted_ms"`
	Visible      float64 `json:"visible_ms"`
	PixelRead    float64 `json:"pixel_read_ms"`
	Polls        int     `json:"polls"`
	VBlank       float64 `json:"vblank_ms,omitempty"`
	Compose      float64 `json:"compose_ms,omitempty"`
	Period       float64 `json:"refresh_period_ms,omitempty"`
}

type liveProbe struct {
	u                        *window
	path                     string
	report                   releaseReport
	deadline                 time.Time
	warmDeadline             time.Time
	zoomed                   bool
	exposed                  bool
	exporting                bool
	exportPath               string
	savePath                 string
	saving                   bool
	strokes, points          int
	revision                 uint64
	committed                time.Time
	sampleStart              time.Time
	sampleLayout             time.Duration
	sampleLaidOut            bool
	sampleAfter              uint64
	sampleReceipt            app.PhotoByteReceipt
	sampleMarker             uint8
	capture                  *probeCapture
	awaitLoss                uint32
	lossDeadline             time.Time
	closeDeadline            time.Time
	pendingTab               *tab
	hardware, warp           []time.Duration
	hwLayout, swLayout       []time.Duration
	hwPresent, swPresent     []time.Duration
	hwSubmitted, swSubmitted []time.Duration
	timings                  []sampleTiming
}

func startReleaseProbe(u *window) bool {
	path := os.Getenv("PHOTOBYTE_RELEASE_PROBE")
	if path == "" {
		return false
	}
	p := &liveProbe{u: u, path: path, deadline: time.Now().Add(3 * time.Minute),
		exportPath: filepath.Join(filepath.Dir(path), "render.apng"),
		savePath:   filepath.Join(filepath.Dir(path), "edited.dphoto")}
	p.report = releaseReport{Width: 4096, Height: 4096, Layers: 8, GoWorkers: runtime.GOMAXPROCS(0), TimingMethod: "dwm_flush"}
	if os.Getenv("PHOTOBYTE_PROBE_DESKTOP_PIXEL") == "1" {
		p.capture = &probeCapture{trace: os.Getenv("PHOTOBYTE_PROBE_PHASES") == "1"}
		p.report.TimingMethod = "desktop_pixel"
	}
	u.frameProbe = p.frame
	u.frameProbeAfterLayout = func(gtx layout.Context) {
		if p.capture != nil && p.sampleMarker != 0 {
			drawProbeMarker(gtx, p.sampleMarker)
		}
		if !p.sampleStart.IsZero() && !p.sampleLaidOut {
			p.sampleLayout = time.Since(p.sampleStart)
			p.sampleLaidOut = true
		}
	}
	u.runJob("Preparing 4K release fixture", ioLane, func(ctx context.Context) completion {
		state, err := releaseFixture(ctx, u.store, p.report.Width, p.report.Height, p.report.Layers)
		return completion{err: err, apply: func() {
			u.add(document.NewSnapshot(state, "Release fixture"), "Release fixture", "", 0)
			u.tool, u.radius = "Brush", 24
		}}
	})
	return true
}

func releaseFixture(ctx context.Context, store *tile.Store, width, height, layers int) (*document.State, error) {
	s := document.NewState(width, height, document.SRGB)
	s.Layers = nil
	for layer := range layers {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		block := new(tile.Block)
		for i := range block {
			x, y := uint16(i%tile.Size), uint16(i/tile.Size)
			block[i] = tile.Pixel{R: x * uint16(64+layer*9), G: y * 129, B: uint16(10000 + layer*2000), A: 65535}
		}
		hash, err := store.Put(block)
		if err != nil {
			return nil, err
		}
		l := document.NewLayer(fmt.Sprintf("Fixture layer %d", layer+1), document.Raster)
		l.ID = s.NextID
		s.NextID++
		if layer > 0 {
			l.Opacity = 0.35
		}
		l.Image = tile.Grid{Blocks: make(map[tile.Coord]tile.Hash)}
		for y := range height / tile.Size {
			for x := range width / tile.Size {
				l.Image.Blocks[tile.Coord{X: x, Y: y}] = hash
			}
		}
		s.Layers = append(s.Layers, l)
	}
	for range 2 {
		s.Frames = append(s.Frames, document.Frame{ID: s.NextID, Name: fmt.Sprintf("Frame %d", len(s.Frames)+1),
			Time: document.Delay{Num: 1, Den: 10}})
		s.NextID++
	}
	s.Recipes[0].Format = "apng"
	return s, s.Validate()
}

func (p *liveProbe) frame(gtx layout.Context) {
	u := p.u
	if !p.exposed && u.native != 0 {
		// Keep the test window visible to DWM without taking input focus.
		setWindowPos := windows.NewLazySystemDLL("user32.dll").NewProc("SetWindowPos")
		const showWithoutActivation = 0x0001 | 0x0002 | 0x0010 | 0x0040 | 0x4000
		setWindowPos.Call(u.native, ^uintptr(0), 0, 0, 0, 0, showWithoutActivation)
		p.exposed = true
		if p.capture != nil {
			if err := p.capture.open(u.native); err != nil {
				p.finish(err.Error())
				return
			}
		}
	}
	if time.Now().After(p.deadline) {
		p.finish(fmt.Sprintf("probe timed out after %d strokes: %s", p.strokes, u.message))
		return
	}
	tick := func() { gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(8 * time.Millisecond)}) }
	t := u.active()
	if t == nil {
		if u.messageErr {
			p.finish(u.message)
		} else {
			tick()
		}
		return
	}
	v := &u.canvas
	s := t.editor.Snapshot()
	receipt := app.PhotoBytePresentState()
	if !p.zoomed {
		if p.capture != nil && !p.exposed {
			tick()
			return
		}
		if v.size.X < 400 || v.size.Y < 200 {
			tick()
			return
		}
		t.fit, t.zoom = false, 1
		t.origin = f32.Pt(float32(v.size.X-p.report.Width)/2, float32(v.size.Y-p.report.Height)/2)
		p.report.ViewportWidth, p.report.ViewportHeight = v.size.X, v.size.Y
		p.zoomed = true
		p.warmDeadline = time.Now().Add(20 * time.Second)
		tick()
		return
	}
	if !p.exporting {
		if receipt.Sequence == 0 || v.front.key.version != s.Version || v.front.key.step != 1 ||
			u.journal.Status(s.Project).Version != s.Version {
			if time.Now().After(p.warmDeadline) {
				p.finish(fmt.Sprintf("warm-up stalled: presents %d, occluded %d, visible revision %d, scale step %d, recovered revision %d",
					receipt.Sequence, app.PhotoByteOccludedFrames(), v.front.key.version, v.front.key.step, u.journal.Status(s.Project).Version))
				return
			}
			tick()
			return
		}
		if receipt.Driver != "hardware" {
			p.finish("the live probe needs a Direct3D 11 hardware presenter")
			return
		}
		p.exporting = true
		u.exportTo(t, p.exportPath, "image")
	}
	if !p.sampleStart.IsZero() {
		if p.sampleReceipt.Sequence == 0 && receipt.Sequence > p.sampleAfter {
			p.sampleReceipt = receipt
		}
		sample := p.sampleReceipt
		if sample.Sequence == 0 {
			tick()
			return
		}
		delay := sample.At.Sub(p.sampleStart)
		if p.capture != nil {
			observed := p.capture.latest.Load()
			if observed == nil || observed.marker != p.sampleMarker {
				tick()
				return
			}
			if observed.err != nil {
				p.finish(observed.err.Error())
				return
			}
			delay = observed.at.Sub(p.sampleStart)
			sample.Composited = true
			if p.capture.trace {
				p.recordTiming(sample, observed)
			}
		}
		if sample.Driver == "warp" {
			p.warp = append(p.warp, delay)
			p.swLayout = append(p.swLayout, p.sampleLayout)
			p.swPresent = append(p.swPresent, sample.PresentCall)
			p.swSubmitted = append(p.swSubmitted, sample.SubmittedAt.Sub(p.sampleStart))
			if sample.Displayed {
				p.report.WARPShown++
			}
			if sample.Composited {
				p.report.WARPComposited++
			}
		} else {
			p.hardware = append(p.hardware, delay)
			p.hwLayout = append(p.hwLayout, p.sampleLayout)
			p.hwPresent = append(p.hwPresent, sample.PresentCall)
			p.hwSubmitted = append(p.hwSubmitted, sample.SubmittedAt.Sub(p.sampleStart))
			if sample.Displayed {
				p.report.HardwareShown++
			}
			if sample.Composited {
				p.report.HardwareComposited++
			}
		}
		p.sampleStart = time.Time{}
		p.sampleReceipt = app.PhotoByteReceipt{}
	}
	if p.awaitLoss != 0 {
		if receipt.Losses < p.awaitLoss {
			if time.Now().After(p.lossDeadline) {
				p.finish("presentation did not resume after the injected device removal")
			}
			return
		}
		p.report.Losses = receipt.Losses
		p.awaitLoss = 0
	}
	if p.revision != 0 {
		if s.Version < p.revision {
			if u.stopEdit == nil {
				p.finish("brush did not commit: " + u.message)
				return
			}
			tick()
			return
		}
		if p.committed.IsZero() {
			p.committed = s.ChangedAt
		}
		status := u.journal.Status(s.Project)
		if status.Err != nil {
			p.finish("autosave during live editing: " + status.Err.Error())
			return
		}
		if status.Version < p.revision || v.front.key.version < p.revision {
			if time.Since(p.committed) > 3*time.Second {
				p.finish("preview or autosave stalled after a committed stroke")
				return
			}
			tick()
			return
		}
		p.report.RecoveryMax = max(p.report.RecoveryMax, milliseconds(status.WrittenAt.Sub(p.committed)))
		p.revision, p.committed = 0, time.Time{}
		p.strokes++
	}
	if p.strokes == 4 && receipt.Losses < 2 {
		p.awaitLoss = receipt.Losses + 1
		p.lossDeadline = time.Now().Add(4 * time.Second)
		// The fallback must schedule its own repaint. The delayed callback
		// only wakes the probe to report a failure if no repaint arrives.
		time.AfterFunc(4*time.Second, u.win.Invalidate)
		app.PhotoByteInjectDeviceLoss()
		return
	}
	if p.strokes >= 8 {
		if !p.saving {
			fmt.Fprintf(os.Stderr, "Live p95 so far %.2f ms hardware (%.2f ms layout, %.2f ms Present), %.2f ms WARP\n",
				percentile95(p.hardware), percentile95(p.hwLayout), percentile95(p.hwPresent), percentile95(p.warp))
			p.saving = true
			p.writeTimings()
			u.save(t, p.savePath, true)
		}
		if u.stopSave != nil || u.stopJob != nil {
			tick()
			return
		}
		if _, err := os.Stat(p.exportPath); err != nil {
			p.finish("export did not complete: " + u.message)
			return
		}
		p.report.Exported = true
		if !p.report.Reopened {
			reopened, _, err := project.Load(u.ctx, p.savePath, u.store)
			if err != nil || reopened.Version != s.Version || len(reopened.History) != p.strokes+1 {
				p.finish(fmt.Sprintf("saved revision failed validation: %v", err))
				return
			}
			p.report.Reopened = true
		}
		if p.pendingTab == nil {
			state := document.NewState(32, 32, document.SRGB)
			p.pendingTab = &tab{editor: document.Open(document.NewSnapshot(state, "Pending")), name: "Pending"}
			u.tabs = append(u.tabs, p.pendingTab)
			p.closeDeadline = time.Now().Add(3 * time.Second)
			u.win.Perform(system.ActionClose)
			tick()
			return
		}
		if u.form == nil || u.form.title != "Edits are still in memory" {
			if time.Now().After(p.closeDeadline) {
				p.finish("the native close guard did not protect the pending revision")
			} else {
				tick()
			}
			return
		}
		p.pendingTab.editor.Close()
		u.tabs = u.tabs[:len(u.tabs)-1]
		u.form = nil
		p.report.CloseProtected = true
		p.finish("")
		return
	}
	if u.stopJob == nil {
		p.finish("the export ended before the live-load sample completed: " + u.message)
		return
	}
	if p.strokes >= 4 && receipt.Driver != "warp" {
		p.finish("two device losses did not switch the presenter to WARP")
		return
	}
	e := pointer.Event{Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, PointerID: 1,
		Position: f32.Pt(float32(v.size.X)/2-60+float32(p.points*12), float32(v.size.Y)/2-42+float32(p.strokes*12))}
	switch p.points {
	case 0:
		e.Kind = pointer.Press
	case 8:
		e.Kind, e.Buttons = pointer.Release, 0
	default:
		e.Kind = pointer.Drag
	}
	if e.Kind != pointer.Release {
		p.sampleAfter = receipt.Sequence
		if p.capture != nil {
			p.sampleMarker++
			p.capture.requests <- pixelRequest{marker: p.sampleMarker, after: p.sampleAfter}
		} else {
			app.PhotoByteMeasureNextPresent()
		}
		p.sampleLayout = 0
		p.sampleLaidOut = false
		p.sampleStart = time.Now()
	}
	v.handlePointer(gtx, t, e)
	p.points++
	if e.Kind == pointer.Release {
		p.points, p.revision = 0, s.Version+1
	}
	tick()
}

func (p *liveProbe) recordTiming(receipt app.PhotoByteReceipt, observed *pixelObservation) {
	sinceInput := func(t time.Time) float64 {
		if t.IsZero() {
			return 0
		}
		return milliseconds(t.Sub(p.sampleStart))
	}
	p.timings = append(p.timings, sampleTiming{
		Driver: receipt.Driver, Sequence: receipt.Sequence, Layout: milliseconds(p.sampleLayout),
		DrawBegan: sinceInput(receipt.DrawBegan), PresentBegan: sinceInput(receipt.SubmittedAt.Add(-receipt.PresentCall)),
		Submitted: sinceInput(receipt.SubmittedAt), Visible: sinceInput(observed.at),
		PixelRead: milliseconds(observed.read), Polls: observed.polls,
		VBlank: sinceInput(observed.clock.vblank), Compose: sinceInput(observed.clock.compose),
		Period: milliseconds(observed.clock.period),
	})
}

func (p *liveProbe) writeTimings() {
	if len(p.timings) > 0 {
		data, _ := json.MarshalIndent(p.timings, "", "  ")
		os.WriteFile(filepath.Join(filepath.Dir(p.path), "timings.json"), data, 0o600)
	}
}

func (p *liveProbe) finish(failure string) {
	p.report.HardwareSamples, p.report.WARPSamples = len(p.hardware), len(p.warp)
	p.report.OccludedFrames = app.PhotoByteOccludedFrames()
	p.report.HardwareP95, p.report.WARPP95 = percentile95(p.hardware), percentile95(p.warp)
	p.report.HardwareLayout, p.report.WARPLayout = percentile95(p.hwLayout), percentile95(p.swLayout)
	p.report.HardwarePresent, p.report.WARPPresent = percentile95(p.hwPresent), percentile95(p.swPresent)
	p.report.HardwareSubmitted, p.report.WARPSubmitted = percentile95(p.hwSubmitted), percentile95(p.swSubmitted)
	p.report.Strokes = p.strokes
	if failure == "" {
		switch {
		case p.report.HardwareSamples != 32 || p.report.WARPSamples != 32 || p.report.Losses != 2 || !p.report.CloseProtected:
			failure = "incomplete hardware, WARP or device-loss samples"
		case p.report.HardwareP95 > 33 || p.report.WARPP95 > 66:
			failure = "input-to-present latency exceeded the release budget"
		case p.report.RecoveryMax > 2000:
			failure = "durable autosave exceeded the release budget"
		}
	}
	p.report.Failure = failure
	data, _ := json.MarshalIndent(p.report, "", "  ")
	os.WriteFile(p.path, data, 0o600)
	p.writeTimings()
	if p.capture != nil {
		p.capture.close()
	}
	p.u.frameProbe = nil
	p.u.frameProbeAfterLayout = nil
	p.u.closeNow()
}

func milliseconds(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

func percentile95(samples []time.Duration) float64 {
	if len(samples) == 0 {
		return 0
	}
	slices.Sort(samples)
	return milliseconds(samples[(len(samples)*95+99)/100-1])
}
