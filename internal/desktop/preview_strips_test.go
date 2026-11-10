package desktop

import (
	"image"
	"image/color"
	"image/draw"
	"slices"
	"testing"

	"github.com/digimbyte/photobyte/internal/render"
)

func TestPreviewStripsPreservePixelsAndReuseUnchangedRows(t *testing.T) {
	part := func(x, y int, pixel color.RGBA) render.PreviewTile {
		img := image.NewRGBA(image.Rect(4, 4, 6, 6))
		draw.Draw(img, img.Bounds(), image.NewUniform(pixel), image.Point{}, draw.Src)
		return render.PreviewTile{Area: image.Rect(x, y, x+2, y+2), Image: img}
	}
	red := color.RGBA{R: 140, A: 255}
	blue := color.RGBA{B: 75, A: 120}
	tiles := []render.PreviewTile{part(12, 22, red), part(12, 20, blue), part(10, 22, blue), part(10, 20, red)}
	var cache previewStrips
	first := cache.batch(slices.Clone(tiles))
	if len(first) != 2 || first[0].Area != image.Rect(10, 20, 14, 22) ||
		first[0].Image.RGBAAt(1, 1) != red || first[0].Image.RGBAAt(2, 1) != blue {
		t.Fatalf("packed preview = %+v", first)
	}
	tiles[0] = part(12, 22, blue)
	second := cache.batch(tiles)
	if second[0].Image != first[0].Image || second[1].Image == first[1].Image ||
		second[1].Image.RGBAAt(2, 1) != blue {
		t.Fatal("only the row with a changed tile should be repacked")
	}
}
