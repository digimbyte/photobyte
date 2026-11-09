package desktop

import (
	"image"
	"image/color"
	"testing"

	"digiphoto/internal/document"
)

func TestRasterBrushPreviewBlendsOverlapsOnce(t *testing.T) {
	ink := color.NRGBA{R: 255, G: 128, A: 150}
	key := brushPreviewKey{size: image.Pt(40, 32), zoom: 1, width: 8, ink: ink}
	img, at, ok := rasterBrushPreview([]document.Point{{X: 10, Y: 10}, {X: 24, Y: 10}, {X: 10, Y: 10}}, key)
	if !ok || img == nil {
		t.Fatal("small brush preview was not rasterized")
	}
	center := img.RGBAAt(14-at.X, 10-at.Y)
	if center != (color.RGBA{R: 150, G: 75, A: 150}) {
		t.Fatalf("overlapping brush preview = %v", center)
	}
	if img.RGBAAt(6-at.X, 10-at.Y).A == 0 || img.RGBAAt(6-at.X, 6-at.Y).A != 0 {
		t.Fatal("brush preview did not preserve round caps")
	}
}
