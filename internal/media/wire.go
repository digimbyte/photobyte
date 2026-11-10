// Package media brokers still and animated images through a bounded helper.
package media

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"
	"os"

	"github.com/digimbyte/photobyte/internal/document"
	"github.com/digimbyte/photobyte/internal/tile"
)

const (
	MaxPixels = 32 * 1024 * 1024
	MaxInput  = 128 << 20
	MaxWire   = int64(4) << 30
)

type Header struct {
	Width, Height int
	Space         document.Space
	Times         []document.Delay
	Plays         uint32
	Poster        bool
	Warning       string
}

func (h Header) frames() int {
	n := len(h.Times)
	if h.Poster {
		n++
	}
	return n
}

func (h Header) Validate() error {
	if h.Width < 1 || h.Height < 1 || h.Width > document.MaxSide || h.Height > document.MaxSide ||
		h.Width*h.Height > MaxPixels || len(h.Times) < 1 || len(h.Times) > document.MaxFrames ||
		int64(h.Width)*int64(h.Height)*int64(h.frames())*8 > MaxWire {
		return errors.New("image exceeds supported dimensions, frame count or decoded size")
	}
	if h.Space != document.SRGB && h.Space != document.P3 && h.Space != document.Data {
		return errors.New("unsupported raster color space")
	}
	return nil
}

func WriteHeader(w io.Writer, h Header) error {
	if err := h.Validate(); err != nil {
		return err
	}
	b, err := json.Marshal(h)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(w, "DPIM"); err != nil {
		return err
	}
	if err := binary.Write(w, binary.BigEndian, uint32(len(b))); err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

func ReadHeader(r io.Reader) (Header, error) {
	var head [8]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return Header{}, err
	}
	n := binary.BigEndian.Uint32(head[4:])
	if string(head[:4]) != "DPIM" || n == 0 || n > 65536 {
		return Header{}, errors.New("invalid raster stream")
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return Header{}, err
	}
	var h Header
	if err := json.Unmarshal(b, &h); err != nil {
		return Header{}, err
	}
	return h, h.Validate()
}

func putPixel(dst []byte, p tile.Pixel) {
	binary.BigEndian.PutUint16(dst, p.R)
	binary.BigEndian.PutUint16(dst[2:], p.G)
	binary.BigEndian.PutUint16(dst[4:], p.B)
	binary.BigEndian.PutUint16(dst[6:], p.A)
}

func getPixel(src []byte) tile.Pixel {
	return tile.Pixel{R: binary.BigEndian.Uint16(src), G: binary.BigEndian.Uint16(src[2:]),
		B: binary.BigEndian.Uint16(src[4:]), A: binary.BigEndian.Uint16(src[6:])}
}

func writeImage(w io.Writer, img image.Image) error {
	b := img.Bounds()
	if rgba, ok := img.(*image.NRGBA64); ok {
		for y := range b.Dy() {
			if _, err := w.Write(rgba.Pix[y*rgba.Stride : y*rgba.Stride+b.Dx()*8]); err != nil {
				return err
			}
		}
		return nil
	}
	row := make([]byte, b.Dx()*8)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			putPixel(row[(x-b.Min.X)*8:], tile.Straight(img.At(x, y)))
		}
		if _, err := w.Write(row); err != nil {
			return err
		}
	}
	return nil
}

func WriteRaster(ctx context.Context, w io.Writer, h Header, row func(frame, y int, dst []tile.Pixel) error) error {
	out := bufio.NewWriterSize(w, 128*1024)
	if err := WriteHeader(out, h); err != nil {
		return err
	}
	pixels := make([]tile.Pixel, h.Width)
	raw := make([]byte, h.Width*8)
	for frame := range h.frames() {
		for y := range h.Height {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := row(frame, y, pixels); err != nil {
				return err
			}
			for x, p := range pixels {
				putPixel(raw[x*8:], p)
			}
			if _, err := out.Write(raw); err != nil {
				return err
			}
		}
	}
	return out.Flush()
}

func readGrid(ctx context.Context, store *tile.Store, f io.ReaderAt, offset int64, width, height int) (tile.Grid, error) {
	g := tile.Grid{Blocks: make(map[tile.Coord]tile.Hash)}
	row := make([]byte, tile.Size*8)
	for y := 0; y < height; y += tile.Size {
		for x := 0; x < width; x += tile.Size {
			if err := ctx.Err(); err != nil {
				return tile.Grid{}, err
			}
			block := new(tile.Block)
			used := false
			for dy := range min(height-y, tile.Size) {
				part := row[:min(width-x, tile.Size)*8]
				if _, err := f.ReadAt(part, offset+(int64(y+dy)*int64(width)+int64(x))*8); err != nil {
					return tile.Grid{}, err
				}
				for dx := 0; dx < len(part)/8; dx++ {
					p := getPixel(part[dx*8:])
					block[dy*tile.Size+dx] = p
					used = used || p != (tile.Pixel{})
				}
			}
			if used {
				hash, err := store.Put(block)
				if err != nil {
					return tile.Grid{}, err
				}
				g.Blocks[tile.At(x, y)] = hash
			}
		}
	}
	return g, nil
}

func ReadDocument(ctx context.Context, store *tile.Store, f *os.File, raw bool) (*document.State, string, error) {
	h, err := ReadHeader(f)
	if err != nil {
		return nil, "", err
	}
	start, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return nil, "", err
	}
	size, err := f.Stat()
	frameBytes := int64(h.Width) * int64(h.Height) * 8
	if err != nil || size.Size() != start+frameBytes*int64(h.frames()) {
		return nil, "", errors.Join(err, errors.New("incomplete or oversized raster stream"))
	}
	if raw {
		h.Space, h.Warning = document.Data, ""
	}
	s := document.NewState(h.Width, h.Height, h.Space)
	s.Plays = h.Plays
	s.Frames = nil
	for i := range h.frames() {
		g, err := readGrid(ctx, store, f, start+int64(i)*frameBytes, h.Width, h.Height)
		if err != nil {
			return nil, "", err
		}
		if h.Poster && i == 0 {
			s.Poster = &g
			continue
		}
		j := len(s.Frames)
		frame := document.Frame{ID: s.NextID, Name: fmt.Sprintf("Frame %d", j+1), Time: h.Times[j]}
		s.NextID++
		if len(h.Times) == 1 {
			s.Layers[0].Image = g
		} else {
			frame.Cels = map[document.ID]tile.Grid{s.Layers[0].ID: g}
		}
		s.Frames = append(s.Frames, frame)
	}
	return s, h.Warning, s.Validate()
}
