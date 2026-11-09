package media

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/draw"
	"io"

	"digiphoto/internal/document"
	"digiphoto/internal/render"
	"digiphoto/internal/tile"
)

const pngMagic = "\x89PNG\r\n\x1a\n"

type chunk struct {
	kind string
	data []byte
}

func scanPNG(data []byte) ([]chunk, error) {
	if !bytes.HasPrefix(data, []byte(pngMagic)) {
		return nil, errors.New("invalid PNG signature")
	}
	data = data[len(pngMagic):]
	out := make([]chunk, 0, 16)
	for len(data) >= 12 && len(out) < 65536 {
		n := int(binary.BigEndian.Uint32(data))
		if n > len(data)-12 {
			return nil, errors.New("truncated PNG chunk")
		}
		if crc32.ChecksumIEEE(data[4:8+n]) != binary.BigEndian.Uint32(data[8+n:]) {
			return nil, errors.New("PNG chunk failed CRC verification")
		}
		part := chunk{kind: string(data[4:8]), data: data[8 : 8+n]}
		out = append(out, part)
		data = data[12+n:]
		if part.kind == "IEND" && n == 0 && len(data) == 0 {
			return out, nil
		}
	}
	return nil, errors.New("missing PNG end or too many chunks")
}

func writeChunk(w io.Writer, kind string, data []byte) error {
	var head [8]byte
	binary.BigEndian.PutUint32(head[:], uint32(len(data)))
	copy(head[4:], kind)
	crc := crc32.Update(crc32.ChecksumIEEE(head[4:]), crc32.IEEETable, data)
	var tail [4]byte
	binary.BigEndian.PutUint32(tail[:], crc)
	for _, bytes := range [][]byte{head[:], data, tail[:]} {
		if _, err := w.Write(bytes); err != nil {
			return err
		}
	}
	return nil
}

type frameControl struct {
	area     image.Rectangle
	delay    document.Delay
	disposal byte
	blend    byte
	idat     bool
	parts    [][]byte
}

func decodePNG(data []byte, out io.Writer, raw bool) error {
	chunks, err := scanPNG(data)
	if err != nil {
		return err
	}
	if chunks[0].kind != "IHDR" || len(chunks[0].data) != 13 {
		return errors.New("missing PNG header")
	}
	ihdr := chunks[0].data
	h := Header{Width: int(binary.BigEndian.Uint32(ihdr)), Height: int(binary.BigEndian.Uint32(ihdr[4:])),
		Space: document.SRGB, Times: []document.Delay{{Num: 1, Den: 10}}, Plays: 1}
	if err := h.Validate(); err != nil {
		return err
	}
	var (
		count        uint32
		seq          uint32
		sawIDAT      bool
		profileKnown bool
		frames       []frameControl
		inherited    []chunk
		profile      []byte
	)
	for _, c := range chunks[1:] {
		switch c.kind {
		case "IHDR":
			return errors.New("duplicate PNG header")
		case "PLTE", "tRNS":
			if sawIDAT {
				return errors.New("PNG palette follows image data")
			}
			inherited = append(inherited, c)
		case "sRGB":
			if !profileKnown && !raw {
				h.Space, h.Warning = document.SRGB, ""
			}
		case "cICP":
			if raw {
				continue
			}
			switch {
			case bytes.Equal(c.data, []byte{12, 13, 0, 1}):
				h.Space, h.Warning = document.P3, ""
				profileKnown = true
			case bytes.Equal(c.data, []byte{1, 13, 0, 1}):
				h.Space, h.Warning = document.SRGB, ""
				profileKnown = true
			default:
				h.Warning = "Unsupported PNG color profile. Preview assumes sRGB."
			}
		case "iCCP":
			if !raw {
				profile, err = pngProfile(c.data)
				if err != nil {
					return err
				}
			}
		case "gAMA", "cHRM":
			if !profileKnown && !raw && (c.kind != "gAMA" || !bytes.Equal(c.data, []byte{0, 0, 177, 143})) {
				h.Warning = "Embedded PNG profile was not converted. Preview assumes sRGB."
			}
		case "acTL":
			if sawIDAT || count != 0 || len(c.data) != 8 {
				return errors.New("invalid APNG animation control")
			}
			count, h.Plays = binary.BigEndian.Uint32(c.data), binary.BigEndian.Uint32(c.data[4:])
			if count == 0 || count > document.MaxFrames {
				return errors.New("APNG frame count exceeds the limit")
			}
		case "fcTL":
			if count == 0 || len(c.data) != 26 || binary.BigEndian.Uint32(c.data) != seq || len(frames) >= int(count) {
				return errors.New("invalid APNG frame control or sequence")
			}
			seq++
			if len(frames) > 0 && len(frames[len(frames)-1].parts) == 0 {
				return errors.New("APNG frame has no image data")
			}
			w, height := int(binary.BigEndian.Uint32(c.data[4:])), int(binary.BigEndian.Uint32(c.data[8:]))
			x, y := int(binary.BigEndian.Uint32(c.data[12:])), int(binary.BigEndian.Uint32(c.data[16:]))
			f := frameControl{area: image.Rect(x, y, x+w, y+height),
				delay:    document.Delay{Num: binary.BigEndian.Uint16(c.data[20:]), Den: binary.BigEndian.Uint16(c.data[22:])},
				disposal: c.data[24], blend: c.data[25], idat: !sawIDAT}
			if w == 0 || height == 0 || !f.area.In(image.Rect(0, 0, h.Width, h.Height)) ||
				f.disposal > 2 || f.blend > 1 || (f.idat && (len(frames) > 0 || w != h.Width || height != h.Height || x != 0 || y != 0)) {
				return errors.New("invalid APNG frame bounds, blend or disposal")
			}
			frames = append(frames, f)
		case "IDAT":
			sawIDAT = true
			if len(frames) == 1 && frames[0].idat {
				frames[0].parts = append(frames[0].parts, c.data)
			} else if len(frames) > 0 {
				return errors.New("APNG IDAT outside the default image")
			}
		case "fdAT":
			if !sawIDAT || len(frames) == 0 || len(c.data) < 4 || binary.BigEndian.Uint32(c.data) != seq {
				return errors.New("invalid APNG frame data sequence")
			}
			seq++
			f := &frames[len(frames)-1]
			if f.idat {
				return errors.New("APNG default frame must use IDAT")
			}
			f.parts = append(f.parts, c.data[4:])
		case "IEND":
		default:
			if len(c.kind) != 4 || c.kind[0]&32 == 0 {
				return fmt.Errorf("unknown critical PNG chunk %q", c.kind)
			}
		}
	}
	if raw {
		h.Space = document.Data
	}
	if profileKnown {
		profile = nil // understood cICP takes precedence
	}
	if len(profile) > 0 {
		if err := prepareProfile(&h, &profile); err != nil {
			return err
		}
	}
	if !sawIDAT {
		return errors.New("PNG is missing image data")
	}
	if count == 0 {
		img, err := nativeDecodePNG(data, h.Width, h.Height)
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
	if len(frames) != int(count) || len(frames[len(frames)-1].parts) == 0 {
		return errors.New("incomplete APNG animation")
	}
	h.Poster = !frames[0].idat
	h.Times = make([]document.Delay, len(frames))
	for i, f := range frames {
		h.Times[i] = f.delay
	}
	if err := WriteHeader(out, h); err != nil {
		return err
	}
	if h.Poster {
		poster, err := nativeDecodePNG(data, h.Width, h.Height)
		if err != nil {
			return err
		}
		poster, err = convertProfile(poster, profile)
		if err != nil {
			return err
		}
		if err := writeImage(out, poster); err != nil {
			return err
		}
	}
	canvas := image.NewNRGBA64(image.Rect(0, 0, h.Width, h.Height))
	for i, f := range frames {
		if err := decodeAPNGFrame(out, canvas, ihdr, inherited, f, i, h.Space, profile); err != nil {
			return err
		}
	}
	return nil
}

func decodeAPNGFrame(out io.Writer, canvas *image.NRGBA64, ihdr []byte, inherited []chunk, f frameControl, index int, space document.Space, profile []byte) error {
	var buf bytes.Buffer
	buf.WriteString(pngMagic)
	head := bytes.Clone(ihdr)
	binary.BigEndian.PutUint32(head, uint32(f.area.Dx()))
	binary.BigEndian.PutUint32(head[4:], uint32(f.area.Dy()))
	writeChunk(&buf, "IHDR", head)
	for _, c := range inherited {
		writeChunk(&buf, c.kind, c.data)
	}
	for _, part := range f.parts {
		writeChunk(&buf, "IDAT", part)
	}
	writeChunk(&buf, "IEND", nil)
	img, err := nativeDecodePNG(buf.Bytes(), f.area.Dx(), f.area.Dy())
	if err != nil {
		return err
	}
	img, err = convertProfile(img, profile)
	if err != nil {
		return err
	}
	var previous *image.NRGBA64
	if f.disposal == 2 && index > 0 {
		previous = image.NewNRGBA64(f.area)
		draw.Draw(previous, f.area, canvas, f.area.Min, draw.Src)
	}
	for y := f.area.Min.Y; y < f.area.Max.Y; y++ {
		for x := f.area.Min.X; x < f.area.Max.X; x++ {
			src := tile.Straight(img.At(x-f.area.Min.X, y-f.area.Min.Y))
			if f.blend == 1 {
				src = render.Over(canvas.NRGBA64At(x, y), src, space)
			}
			canvas.SetNRGBA64(x, y, src)
		}
	}
	if err := writeImage(out, canvas); err != nil {
		return err
	}
	if previous != nil {
		draw.Draw(canvas, f.area, previous, f.area.Min, draw.Src)
	} else if f.disposal != 0 {
		draw.Draw(canvas, f.area, image.Transparent, image.Point{}, draw.Src)
	}
	return nil
}

type pngData struct {
	out io.Writer
	seq *uint32
	buf []byte
}

func (p *pngData) Write(b []byte) (int, error) {
	n := len(b)
	for len(b) > 0 {
		step := min(len(b), 64*1024-len(p.buf))
		p.buf = append(p.buf, b[:step]...)
		b = b[step:]
		if len(p.buf) == 64*1024 {
			if err := p.flush(); err != nil {
				return n - len(b), err
			}
		}
	}
	return n, nil
}

func (p *pngData) flush() error {
	if len(p.buf) == 0 {
		return nil
	}
	var err error
	if p.seq == nil {
		err = writeChunk(p.out, "IDAT", p.buf)
	} else {
		data := make([]byte, len(p.buf)+4)
		binary.BigEndian.PutUint32(data, *p.seq)
		*p.seq++
		copy(data[4:], p.buf)
		err = writeChunk(p.out, "fdAT", data)
	}
	p.buf = p.buf[:0]
	return err
}

func encodePNG(out io.Writer, in io.Reader, h Header, depth int, animated bool) error {
	w := bufio.NewWriterSize(out, 128*1024)
	if _, err := io.WriteString(w, pngMagic); err != nil {
		return err
	}
	var ihdr [13]byte
	binary.BigEndian.PutUint32(ihdr[:], uint32(h.Width))
	binary.BigEndian.PutUint32(ihdr[4:], uint32(h.Height))
	ihdr[8], ihdr[9] = byte(depth), 6
	if err := writeChunk(w, "IHDR", ihdr[:]); err != nil {
		return err
	}
	switch h.Space {
	case document.P3:
		if err := writeChunk(w, "cICP", []byte{12, 13, 0, 1}); err != nil {
			return err
		}
	case document.SRGB:
		if err := writeChunk(w, "sRGB", []byte{0}); err != nil {
			return err
		}
	}
	if animated {
		var ctl [8]byte
		binary.BigEndian.PutUint32(ctl[:], uint32(len(h.Times)))
		binary.BigEndian.PutUint32(ctl[4:], h.Plays)
		if err := writeChunk(w, "acTL", ctl[:]); err != nil {
			return err
		}
	}
	frames := 1
	if animated {
		frames = h.frames()
	}
	var seq uint32
	raw := make([]byte, h.Width*8)
	pitch := depth / 2
	filtered := make([]byte, 1+h.Width*pitch)
	filtered[0] = 1 // PNG Sub filter
	for i := range frames {
		if animated && !(h.Poster && i == 0) {
			idx := i
			if h.Poster {
				idx--
			}
			var fc [26]byte
			binary.BigEndian.PutUint32(fc[:], seq)
			seq++
			copy(fc[4:12], ihdr[:8])
			binary.BigEndian.PutUint16(fc[20:], h.Times[idx].Num)
			binary.BigEndian.PutUint16(fc[22:], h.Times[idx].Den)
			if err := writeChunk(w, "fcTL", fc[:]); err != nil {
				return err
			}
		}
		data := &pngData{out: w, buf: make([]byte, 0, 64*1024)}
		if i > 0 {
			data.seq = &seq
		}
		enc, err := zlib.NewWriterLevel(data, zlib.BestSpeed)
		if err != nil {
			return err
		}
		for range h.Height {
			if _, err := io.ReadFull(in, raw); err != nil {
				return err
			}
			if depth == 16 {
				copy(filtered[1:], raw)
			} else {
				for j := range h.Width * 4 {
					filtered[j+1] = byte((uint32(binary.BigEndian.Uint16(raw[j*2:])) + 128) / 257)
				}
			}
			for j := len(filtered) - 1; j > pitch; j-- {
				filtered[j] -= filtered[j-pitch]
			}
			if _, err := enc.Write(filtered); err != nil {
				return err
			}
		}
		if err := enc.Close(); err != nil {
			return err
		}
		if err := data.flush(); err != nil {
			return err
		}
	}
	if err := writeChunk(w, "IEND", nil); err != nil {
		return err
	}
	return w.Flush()
}
