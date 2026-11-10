package desktop

import (
	"context"
	"image"
	"image/color"
	"math"

	"github.com/digimbyte/photobyte/internal/document"
	"github.com/digimbyte/photobyte/internal/media"
	"github.com/digimbyte/photobyte/internal/render"
	"github.com/digimbyte/photobyte/internal/tile"

	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"
)

type exportPreview struct {
	images [2]paint.ImageOp
}

func (u *window) prepareComparison(form *form, s *document.State, frame document.ID, format string) {
	form.previewing = true
	u.runJob("Preparing export preview", ioLane, func(ctx context.Context) completion {
		sample := render.NewScene(u.store, s, frame, document.SRGB, 0).Sampler()
		bounds := s.Bounds
		ratio := min(176.0/float64(bounds.Dx()), 100.0/float64(bounds.Dy()))
		w, h := max(1, int(math.Round(float64(bounds.Dx())*ratio))), max(1, int(math.Round(float64(bounds.Dy())*ratio)))
		imageBounds := image.Rect(0, 0, w, h)
		before, after := image.NewNRGBA(imageBounds), image.NewNRGBA(imageBounds)
		pixel := func(p tile.Pixel) color.NRGBA {
			return color.NRGBA{R: byte(p.R >> 8), G: byte(p.G >> 8), B: byte(p.B >> 8), A: byte(p.A >> 8)}
		}
		for y := range h {
			if err := ctx.Err(); err != nil {
				return completion{err: err}
			}
			for x := range w {
				p := sample.Pixel(bounds.Min.X+x*bounds.Dx()/w, bounds.Min.Y+y*bounds.Dy()/h)
				before.SetNRGBA(x, y, pixel(p))
				after.SetNRGBA(x, y, pixel(media.PreviewPixel(p, format)))
			}
		}
		if err := sample.Err(); err != nil {
			return completion{err: err}
		}
		return completion{message: "Export comparison ready", apply: func() {
			form.comparison = &exportPreview{images: [2]paint.ImageOp{paint.NewImageOp(before), paint.NewImageOp(after)}}
		}}
	})
}

func (u *window) formatComparison(gtx layout.Context, preview *exportPreview) layout.Dimensions {
	item := func(name string, img paint.ImageOp) layout.Widget {
		return func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Right: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return column(gtx, u.text(name, 12, true), func(gtx layout.Context) layout.Dimensions {
					r := image.Rectangle{Max: img.Size()}
					paint.FillShape(gtx.Ops, buttonBG, clip.Rect(r).Op())
					return (widget.Image{Src: img, Fit: widget.Unscaled, Scale: 1}).Layout(gtx)
				})
			})
		}
	}
	return row(gtx, item("Original", preview.images[0]), item("Export color and alpha", preview.images[1]))
}
