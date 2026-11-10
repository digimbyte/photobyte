// Package brand exposes PhotoByte's embedded application mark.
package brand

import (
	"bytes"
	_ "embed"
	"image"
	"image/png"
	"sync"
)

//go:embed photobyte.png
var mark []byte

var Mark = sync.OnceValue(func() image.Image {
	img, err := png.Decode(bytes.NewReader(mark))
	if err != nil {
		panic(err)
	}
	return img
})
