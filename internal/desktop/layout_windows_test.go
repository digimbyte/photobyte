package desktop

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"digiphoto/internal/document"
	"digiphoto/internal/render"
	"digiphoto/internal/tile"

	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget"
)

func TestDesktopLayout(t *testing.T) {
	if os.Getenv("DIGIPHOTO_VISUAL_SMOKE") == "" {
		t.Skip("set DIGIPHOTO_VISUAL_SMOKE for native graphics testing")
	}
	store, err := tile.NewStore(filepath.Join(t.TempDir(), "tiles"), 4*tile.RawBytes)
	if err != nil {
		t.Fatal(err)
	}
	pool := render.NewPool(4 << 20)
	defer pool.Close()
	doc := document.NewState(640, 400, document.SRGB)
	img := image.NewNRGBA64(doc.Bounds)
	for y := range img.Rect.Dy() {
		for x := range img.Rect.Dx() {
			img.SetNRGBA64(x, y, tile.Pixel{R: uint16(x * 80), G: uint16(14000 + y*96), B: 32000, A: 65535})
		}
	}
	doc.Layers[0].Image, err = store.FromImage(t.Context(), img)
	if err != nil {
		t.Fatal(err)
	}
	ed := document.Open(document.NewSnapshot(doc, "Open example"))
	defer ed.Close()
	active := &tab{editor: ed, name: "Example.dphoto", saved: 1, zoom: 1, fit: true}
	active.normalize()
	u := &window{theme: newTheme(), store: store, pool: pool, ctx: t.Context(),
		buttons: make(map[string]*widget.Clickable), tabs: []*tab{active}, workspace: defaultWorkspaceState(), tool: "Move"}
	u.canvas.u = u
	u.canvas.requests = make(chan previewRequest, 1)
	u.canvas.ready = make(chan previewFrame, 1)
	u.tabsList.Axis, u.frameList.Axis = layout.Horizontal, layout.Horizontal
	for _, scale := range []float32{1, 1.5, 2} {
		size := image.Pt(int(1040*scale), int(690*scale))
		gl, err := headless.NewWindow(size.X, size.Y)
		if err != nil {
			t.Fatal(err)
		}
		var ops op.Ops
		var router input.Router
		gtx := layout.Context{Constraints: layout.Exact(size), Metric: unit.Metric{PxPerDp: scale, PxPerSp: scale},
			Now: time.Now(), Ops: &ops, Source: router.Source()}
		active.fit = true
		u.canvas.requested = previewKey{}
		u.layout(gtx)
		select {
		case req := <-u.canvas.requests:
			u.canvas.ready <- u.canvas.render(req)
		default:
			t.Fatal("desktop did not schedule a preview")
		}
		ops.Reset()
		u.layout(gtx)
		if err := gl.Frame(&ops); err != nil {
			gl.Release()
			t.Fatal(err)
		}
		screenshot := image.NewRGBA(image.Rectangle{Max: size})
		err = gl.Screenshot(screenshot)
		gl.Release()
		if err != nil {
			t.Fatal(err)
		}
		blue, labels := 0, 0
		for y := range size.Y {
			for x := range size.X {
				c := screenshot.RGBAAt(x, y)
				if c.B > 110 && c.R < 180 && c.G > 60 {
					blue++
				}
				if y < int(40*scale) && c.R > 185 && c.G > 185 && c.B > 185 {
					labels++
				}
			}
		}
		if len(u.canvas.front.tiles) == 0 || blue < size.X*size.Y/20 {
			t.Fatalf("canvas content missing at %.0f%% scale", scale*100)
		}
		if labels < int(200*scale*scale) {
			t.Fatalf("toolbar labels missing at %.0f%% scale", scale*100)
		}
		if path := os.Getenv("DIGIPHOTO_SCREENSHOT"); path != "" && scale == 1 {
			f, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			err = png.Encode(f, screenshot)
			f.Close()
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if u.canvas.cancelJob != nil {
		u.canvas.cancelJob()
	}
}
