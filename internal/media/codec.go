package media

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"io"
	"math"
	"time"

	"digiphoto/internal/document"
	"digiphoto/internal/render"
	"digiphoto/internal/tile"

	"github.com/HugoSmits86/nativewebp"
)

func Decode(in io.Reader, out io.Writer, raw bool) error {
	data, err := io.ReadAll(io.LimitReader(in, MaxInput+1))
	if err != nil {
		return err
	}
	if len(data) > MaxInput {
		return errors.New("compressed image exceeds the 128 MiB input limit")
	}
	switch {
	case bytes.HasPrefix(data, []byte(pngMagic)):
		return decodePNG(data, out, raw)
	case bytes.HasPrefix(data, []byte("GIF87a")) || bytes.HasPrefix(data, []byte("GIF89a")):
		return decodeGIF(data, out, raw)
	case bytes.HasPrefix(data, []byte{255, 216}):
		var profile []byte
		if !raw {
			profile, err = jpegProfile(data)
			if err != nil {
				return err
			}
		}
		return decodeStill(data, out, raw, jpeg.DecodeConfig, jpeg.Decode, profile)
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		if len(data) > 21 && string(data[12:16]) == "VP8X" && data[20]&2 != 0 {
			return errors.New("animated WebP is unsupported; use APNG or GIF")
		}
		var profile []byte
		if !raw {
			profile, err = webpProfile(data)
			if err != nil {
				return err
			}
		}
		return decodeStill(data, out, raw, nativewebp.DecodeConfig, nativeDecodeWebP, profile)
	default:
		return errors.New("choose a PNG, APNG, GIF, JPEG, or still WebP file")
	}
}

func decodeStill(data []byte, out io.Writer, raw bool, config func(io.Reader) (image.Config, error),
	decode func(io.Reader) (image.Image, error), profile []byte) error {
	cfg, err := config(bytes.NewReader(data))
	if err != nil {
		return err
	}
	h := Header{Width: cfg.Width, Height: cfg.Height, Space: document.SRGB,
		Times: []document.Delay{{Num: 1, Den: 10}}, Plays: 1}
	if raw {
		h.Space = document.Data
	} else if err := prepareProfile(&h, &profile); err != nil {
		return err
	}
	if err := h.Validate(); err != nil {
		return err
	}
	img, err := decode(bytes.NewReader(data))
	if err != nil {
		return err
	}
	img, err = convertProfile(img, profile)
	if err != nil {
		return err
	}
	if err := WriteHeader(out, h); err != nil {
		return err
	}
	return writeImage(out, img)
}

func gifBudget(data []byte, width, height int) (int, error) {
	if len(data) < 13 {
		return 0, errors.New("truncated GIF header")
	}
	flags := data[10]
	data = data[13:]
	if flags&128 != 0 {
		n := 3 * (2 << (flags & 7))
		if n > len(data) {
			return 0, errors.New("truncated GIF palette")
		}
		data = data[n:]
	}
	frames := 0
	for len(data) > 0 {
		switch data[0] {
		case 0x3b:
			if frames > 0 {
				return frames, nil
			}
			return 0, errors.New("GIF contains no frames")
		case 0x21:
			if len(data) < 2 {
				return 0, errors.New("truncated GIF extension")
			}
			data = data[2:]
		case 0x2c:
			if len(data) < 10 {
				return 0, errors.New("truncated GIF frame")
			}
			frames++
			if frames > document.MaxFrames || int64(frames)*int64(width)*int64(height)*8 > MaxWire {
				return 0, errors.New("GIF exceeds its decoded size limit")
			}
			x := int(binary.LittleEndian.Uint16(data[1:]))
			y := int(binary.LittleEndian.Uint16(data[3:]))
			w := int(binary.LittleEndian.Uint16(data[5:]))
			h := int(binary.LittleEndian.Uint16(data[7:]))
			if w == 0 || h == 0 || x+w > width || y+h > height {
				return 0, errors.New("invalid GIF frame bounds")
			}
			flag := data[9]
			data = data[10:]
			n := 1
			if flag&128 != 0 {
				n += 3 * (2 << (flag & 7))
			}
			if n > len(data) {
				return 0, errors.New("truncated GIF frame palette")
			}
			data = data[n:]
		default:
			return 0, errors.New("invalid GIF block")
		}
		for {
			if len(data) == 0 || int(data[0]) >= len(data) {
				return 0, errors.New("truncated GIF sub-block")
			}
			n := int(data[0])
			data = data[1+n:]
			if n == 0 {
				break
			}
		}
	}
	return 0, errors.New("missing GIF trailer")
}

func decodeGIF(data []byte, out io.Writer, raw bool) error {
	cfg, err := gif.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return err
	}
	h := Header{Width: cfg.Width, Height: cfg.Height, Space: document.SRGB, Times: []document.Delay{{Num: 1, Den: 10}}}
	if err := h.Validate(); err != nil {
		return err
	}
	if _, err := gifBudget(data, cfg.Width, cfg.Height); err != nil {
		return err
	}
	g, err := gif.DecodeAll(bytes.NewReader(data))
	if err != nil {
		return err
	}
	h.Times = make([]document.Delay, len(g.Delay))
	for i, d := range g.Delay {
		h.Times[i] = document.Delay{Num: uint16(d), Den: 100}
	}
	if g.LoopCount != 0 {
		h.Plays = uint32(max(0, g.LoopCount) + 1)
	}
	if raw {
		h.Space = document.Data
	}
	if err := WriteHeader(out, h); err != nil {
		return err
	}
	var background image.Image = image.Transparent
	if p, ok := g.Config.ColorModel.(color.Palette); ok && int(g.BackgroundIndex) < len(p) {
		// A transparent first frame uses a transparent animation canvas.
		for _, c := range g.Image[0].Palette {
			_, _, _, a := c.RGBA()
			if a == 0 {
				p = nil
				break
			}
		}
		if p != nil {
			background = image.NewUniform(p[g.BackgroundIndex])
		}
	}
	canvas := image.NewNRGBA(image.Rect(0, 0, h.Width, h.Height))
	draw.Draw(canvas, canvas.Bounds(), background, image.Point{}, draw.Src)
	for i, f := range g.Image {
		b := f.Bounds()
		var previous *image.NRGBA
		if g.Disposal[i] == gif.DisposalPrevious {
			previous = image.NewNRGBA(b)
			draw.Draw(previous, b, canvas, b.Min, draw.Src)
		}
		draw.Draw(canvas, b, f, b.Min, draw.Over)
		if err := writeImage(out, canvas); err != nil {
			return err
		}
		if previous != nil {
			draw.Draw(canvas, b, previous, b.Min, draw.Src)
		} else if g.Disposal[i] == gif.DisposalBackground {
			draw.Draw(canvas, b, background, image.Point{}, draw.Src)
		}
	}
	return nil
}

func Encode(in io.Reader, out io.Writer, format string, depth int) error {
	r := bufio.NewReaderSize(in, 128*1024)
	h, err := ReadHeader(r)
	if err != nil {
		return err
	}
	if depth != 8 && depth != 16 {
		return errors.New("choose 8-bit or 16-bit output")
	}
	if format == "png" || format == "apng" {
		if format == "png" {
			handled, err := nativeEncodePNG(out, r, h, depth)
			if handled || err != nil {
				return err
			}
		}
		return encodePNG(out, r, h, depth, format == "apng")
	}
	if h.Space == document.P3 || h.Space == document.Data {
		return errors.New("choose PNG/APNG for Display P3 or texture samples")
	}
	switch format {
	case "jpg", "webp":
		img, err := readStill(r, h, format == "jpg")
		if err != nil {
			return err
		}
		if format == "jpg" {
			return jpeg.Encode(out, img, &jpeg.Options{Quality: 92})
		}
		return nativeEncodeWebP(out, img)
	case "gif":
		return encodeGIF(out, r, h)
	default:
		return errors.New("unsupported export format")
	}
}

func readStill(r io.Reader, h Header, flatten bool) (*image.NRGBA, error) {
	raw := make([]byte, h.Width*8)
	img := image.NewNRGBA(image.Rect(0, 0, h.Width, h.Height))
	for y := range h.Height {
		if _, err := io.ReadFull(r, raw); err != nil {
			return nil, err
		}
		for x := range h.Width {
			p := getPixel(raw[x*8:])
			if flatten {
				p = PreviewPixel(p, "jpg")
			}
			img.SetNRGBA(x, y, color.NRGBA{
				R: byte((uint32(p.R) + 128) / 257), G: byte((uint32(p.G) + 128) / 257),
				B: byte((uint32(p.B) + 128) / 257), A: byte((uint32(p.A) + 128) / 257),
			})
		}
	}
	return img, nil
}

func encodeGIF(out io.Writer, in io.Reader, h Header) error {
	if h.Plays > math.MaxUint16+1 {
		return errors.New("GIF loop count exceeds its format limit")
	}
	g := &gif.GIF{LoopCount: int(h.Plays) - 1}
	if h.Plays <= 1 {
		g.LoopCount = -int(h.Plays)
	}
	palette := make(color.Palette, 1, 217)
	palette[0] = color.Transparent
	for r := range 6 {
		for g := range 6 {
			for b := range 6 {
				palette = append(palette, color.NRGBA{R: byte(r * 51), G: byte(g * 51), B: byte(b * 51), A: 255})
			}
		}
	}
	raw := make([]byte, h.Width*8)
	if h.Poster {
		if _, err := io.CopyN(io.Discard, in, int64(h.Width)*int64(h.Height)*8); err != nil {
			return err
		}
	}
	for _, t := range h.Times {
		p := image.NewPaletted(image.Rect(0, 0, h.Width, h.Height), palette)
		for y := range h.Height {
			if _, err := io.ReadFull(in, raw); err != nil {
				return err
			}
			for x := range h.Width {
				p.Pix[y*p.Stride+x] = gifIndex(getPixel(raw[x*8:]))
			}
		}
		g.Image = append(g.Image, p)
		delay := int(GIFDelay(t) / (10 * time.Millisecond))
		if delay > math.MaxUint16 {
			return errors.New("a frame delay exceeds the GIF limit")
		}
		g.Delay = append(g.Delay, delay)
		g.Disposal = append(g.Disposal, gif.DisposalBackground)
	}
	g.Config = image.Config{ColorModel: palette, Width: h.Width, Height: h.Height}
	return gif.EncodeAll(out, g)
}

func gifIndex(p tile.Pixel) byte {
	if p.A < 32768 {
		return 0
	}
	r, g, b := (int(p.R)*5+32767)/65535, (int(p.G)*5+32767)/65535, (int(p.B)*5+32767)/65535
	return byte(1 + r*36 + g*6 + b)
}

func GIFDelay(d document.Delay) time.Duration {
	return d.Duration().Round(10 * time.Millisecond)
}

func PreviewPixel(p tile.Pixel, format string) tile.Pixel {
	switch format {
	case "gif":
		i := int(gifIndex(p))
		if i == 0 {
			return tile.Pixel{}
		}
		i--
		return tile.Pixel{R: uint16(i/36) * 13107, G: uint16(i/6%6) * 13107, B: uint16(i%6) * 13107, A: 65535}
	case "jpg":
		white := tile.Pixel{R: 65535, G: 65535, B: 65535, A: 65535}
		return render.Over(white, p, document.SRGB)
	default:
		return p
	}
}
