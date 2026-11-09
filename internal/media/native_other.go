//go:build !windows

package media

import (
	"bytes"
	"image"
	"image/png"
	"io"

	"github.com/HugoSmits86/nativewebp"
)

func nativeDecodePNG(data []byte, _, _ int) (image.Image, error) {
	return png.Decode(bytes.NewReader(data))
}

func nativeDecodeWebP(r io.Reader) (image.Image, error) { return nativewebp.Decode(r) }

func nativeEncodeWebP(out io.Writer, img *image.NRGBA) error { return nativewebp.Encode(out, img, nil) }

func nativeEncodePNG(io.Writer, io.Reader, Header, int) (bool, error) { return false, nil }

func profileDriver() (bool, error) { return false, nil }

func convertProfile(img image.Image, _ []byte) (image.Image, error) { return img, nil }
