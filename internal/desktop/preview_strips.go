package desktop

import (
	"cmp"
	"image"
	"image/draw"
	"slices"

	"github.com/digimbyte/photobyte/internal/render"
)

const (
	stripTiles     = 4
	stripByteLimit = 8 << 20
)

type stripKey struct {
	area  image.Rectangle
	tiles [stripTiles]*image.RGBA
}

type previewStrips map[stripKey]*image.RGBA

// batch joins adjacent full-resolution tiles for presentation only. Unchanged
// strips retain their image identity so Gio can reuse the uploaded texture.
func (cache *previewStrips) batch(tiles []render.PreviewTile) []render.PreviewTile {
	total := 0
	for _, part := range tiles {
		total += len(part.Image.Pix)
	}
	if len(tiles) < 2 || total > stripByteLimit {
		*cache = nil
		return tiles
	}
	slices.SortFunc(tiles, func(a, b render.PreviewTile) int {
		if y := cmp.Compare(a.Area.Min.Y, b.Area.Min.Y); y != 0 {
			return y
		}
		return cmp.Compare(a.Area.Min.X, b.Area.Min.X)
	})
	next := make(previewStrips)
	out := make([]render.PreviewTile, 0, len(tiles))
	for len(tiles) > 0 {
		key := stripKey{area: tiles[0].Area, tiles: [stripTiles]*image.RGBA{tiles[0].Image}}
		count := 1
		for count < min(stripTiles, len(tiles)) {
			part := tiles[count]
			if part.Area.Min.Y != key.area.Min.Y || part.Area.Max.Y != key.area.Max.Y || part.Area.Min.X != key.area.Max.X {
				break
			}
			key.area.Max.X = part.Area.Max.X
			key.tiles[count] = part.Image
			count++
		}
		if count == 1 {
			out = append(out, tiles[0])
		} else {
			img := (*cache)[key]
			if img == nil {
				img = image.NewRGBA(image.Rectangle{Max: key.area.Size()})
				for _, part := range tiles[:count] {
					draw.Draw(img, part.Area.Sub(key.area.Min), part.Image, part.Image.Bounds().Min, draw.Src)
				}
			}
			next[key] = img
			out = append(out, render.PreviewTile{Area: key.area, Image: img})
		}
		tiles = tiles[count:]
	}
	*cache = next
	return out
}
