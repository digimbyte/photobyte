package media

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/png"
	"testing"

	"digiphoto/internal/document"
	"digiphoto/internal/tile"
)

func TestAPNGPosterOffsetsAndDisposal(t *testing.T) {
	var src bytes.Buffer
	src.WriteString(pngMagic)
	red := tile.Pixel{R: 65535, A: 65535}
	green := tile.Pixel{G: 65535, A: 65535}
	blue := tile.Pixel{B: 65535, A: 65535}
	yellow := tile.Pixel{R: 65535, G: 65535, A: 65535}
	var ctl [8]byte
	binary.BigEndian.PutUint32(ctl[:], 3)
	binary.BigEndian.PutUint32(ctl[4:], 2)
	makeStill := func(w int, p tile.Pixel) []chunk {
		img := image.NewNRGBA64(image.Rect(0, 0, w, 1))
		for x := range w {
			img.SetNRGBA64(x, 0, p)
		}
		var b bytes.Buffer
		if err := png.Encode(&b, img); err != nil {
			t.Fatal(err)
		}
		chunks, err := scanPNG(b.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		return chunks
	}
	poster := makeStill(2, red)
	writeChunk(&src, "IHDR", poster[0].data)
	writeChunk(&src, "acTL", ctl[:])
	for _, c := range poster {
		if c.kind == "IDAT" {
			writeChunk(&src, c.kind, c.data)
		}
	}
	var seq uint32
	for i, p := range []tile.Pixel{green, blue, yellow} {
		var f [26]byte
		binary.BigEndian.PutUint32(f[:], seq)
		seq++
		binary.BigEndian.PutUint32(f[4:], 1)
		binary.BigEndian.PutUint32(f[8:], 1)
		binary.BigEndian.PutUint32(f[12:], uint32(i%2))
		binary.BigEndian.PutUint16(f[20:], uint16(i+1))
		binary.BigEndian.PutUint16(f[22:], 25)
		if i == 1 {
			f[24] = 2
		}
		writeChunk(&src, "fcTL", f[:])
		for _, c := range makeStill(1, p) {
			if c.kind != "IDAT" {
				continue
			}
			part := make([]byte, len(c.data)+4)
			binary.BigEndian.PutUint32(part, seq)
			seq++
			copy(part[4:], c.data)
			writeChunk(&src, "fdAT", part)
		}
	}
	writeChunk(&src, "IEND", nil)
	var decoded bytes.Buffer
	if err := Decode(&src, &decoded, false); err != nil {
		t.Fatal(err)
	}
	h, err := ReadHeader(&decoded)
	if err != nil || !h.Poster || h.Plays != 2 || h.Times[2] != (document.Delay{Num: 3, Den: 25}) {
		t.Fatalf("APNG metadata %+v: %v", h, err)
	}
	want := []tile.Pixel{red, red, green, {}, green, blue, yellow, {}}
	if decoded.Len() != len(want)*8 {
		t.Fatalf("decoded %d bytes, want %d", decoded.Len(), len(want)*8)
	}
	for i, p := range want {
		if got := getPixel(decoded.Bytes()[i*8:]); got != p {
			t.Errorf("sample %d = %+v, want %+v", i, got, p)
		}
	}
}

func FuzzPNGChunks(f *testing.F) {
	var b bytes.Buffer
	png.Encode(&b, image.NewNRGBA(image.Rect(0, 0, 2, 2)))
	f.Add(b.Bytes())
	f.Add([]byte(pngMagic))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 64*1024 {
			t.Skip()
		}
		chunks, err := scanPNG(data)
		if err == nil && (len(chunks) == 0 || chunks[len(chunks)-1].kind != "IEND") {
			t.Fatal("scanner accepted a PNG without IEND")
		}
	})
}
