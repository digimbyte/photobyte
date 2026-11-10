package media

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"unsafe"

	"github.com/digimbyte/photobyte/internal/document"
	"github.com/digimbyte/photobyte/internal/tile"

	"github.com/HugoSmits86/nativewebp"
	"golang.org/x/sys/windows"
)

var native = sync.OnceValues(func() (*windows.DLL, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(filepath.Dir(exe), "photobyte_codecs.dll")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil, nil // portable build uses the Go drivers
	} else if err != nil {
		return nil, err
	}
	handle, err := windows.LoadLibraryEx(path, 0, windows.LOAD_LIBRARY_SEARCH_DLL_LOAD_DIR|windows.LOAD_LIBRARY_SEARCH_SYSTEM32)
	if err != nil {
		return nil, fmt.Errorf("load PhotoByte codec driver: %w", err)
	}
	return &windows.DLL{Name: path, Handle: handle}, nil
})

func nativeProc(name string) (*windows.Proc, error) {
	dll, err := native()
	if err != nil || dll == nil {
		return nil, err
	}
	return dll.FindProc(name)
}

func nativeDecodePNG(data []byte, w, h int) (image.Image, error) {
	proc, err := nativeProc("dp_png_decode")
	if err != nil {
		return nil, err
	}
	if proc == nil {
		return png.Decode(bytes.NewReader(data))
	}
	out := image.NewNRGBA64(image.Rect(0, 0, w, h))
	ok, _, _ := proc.Call(uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)),
		uintptr(unsafe.Pointer(&out.Pix[0])), uintptr(w), uintptr(h))
	runtime.KeepAlive(data)
	runtime.KeepAlive(out)
	if ok == 0 {
		return nil, errors.New("libpng could not decode the raster")
	}
	return out, nil
}

func nativeDecodeWebP(r io.Reader) (image.Image, error) {
	proc, err := nativeProc("dp_webp_decode")
	if err != nil {
		return nil, err
	}
	if proc == nil {
		return nativewebp.Decode(r)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	cfg, err := nativewebp.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	out := image.NewNRGBA(image.Rect(0, 0, cfg.Width, cfg.Height))
	ok, _, _ := proc.Call(uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)),
		uintptr(unsafe.Pointer(&out.Pix[0])), uintptr(cfg.Width), uintptr(cfg.Height))
	runtime.KeepAlive(data)
	runtime.KeepAlive(out)
	if ok == 0 {
		return nil, errors.New("libwebp could not decode the raster")
	}
	return out, nil
}

func nativeEncodeWebP(out io.Writer, img *image.NRGBA) error {
	proc, err := nativeProc("dp_webp_write")
	if err != nil {
		return err
	}
	f, ok := out.(*os.File)
	if proc == nil || !ok {
		return nativewebp.Encode(out, img, nil)
	}
	rc, _, _ := proc.Call(f.Fd(), uintptr(unsafe.Pointer(&img.Pix[0])), uintptr(img.Rect.Dx()), uintptr(img.Rect.Dy()))
	runtime.KeepAlive(f)
	runtime.KeepAlive(img)
	if rc == 0 {
		return errors.New("libwebp could not write the output")
	}
	return nil
}

func nativeEncodePNG(out io.Writer, in io.Reader, h Header, depth int) (bool, error) {
	f, ok := out.(*os.File)
	if !ok {
		return false, nil
	}
	begin, err := nativeProc("dp_png_begin")
	if begin == nil || err != nil {
		return false, err
	}
	row, err := nativeProc("dp_png_row")
	if err != nil {
		return true, err
	}
	end, err := nativeProc("dp_png_end")
	if err != nil {
		return true, err
	}
	space := 0
	if h.Space == document.SRGB {
		space = 1
	} else if h.Space == document.P3 {
		space = 2
	}
	writer, _, _ := begin.Call(f.Fd(), uintptr(h.Width), uintptr(h.Height), uintptr(depth), uintptr(space))
	defer runtime.KeepAlive(f)
	if writer == 0 {
		return true, errors.New("libpng could not write the PNG header")
	}
	defer func() {
		if writer != 0 {
			end.Call(writer, 0)
		}
	}()
	raw := make([]byte, h.Width*8)
	data := raw
	if depth == 8 {
		data = make([]byte, h.Width*4)
	}
	for range h.Height {
		if _, err := io.ReadFull(in, raw); err != nil {
			return true, err
		}
		if depth == 8 {
			for i := range data {
				v := uint32(raw[i*2])<<8 | uint32(raw[i*2+1])
				data[i] = byte((v + 128) / 257)
			}
		}
		ok, _, _ := row.Call(writer, uintptr(unsafe.Pointer(&data[0])))
		runtime.KeepAlive(data)
		if ok == 0 {
			return true, errors.New("libpng could not write a PNG row")
		}
	}
	okResult, _, _ := end.Call(writer, 1)
	writer = 0
	if okResult == 0 {
		return true, errors.New("libpng could not finish the PNG")
	}
	return true, nil
}

func profileDriver() (bool, error) {
	dll, err := native()
	return dll != nil, err
}

func convertProfile(img image.Image, profile []byte) (image.Image, error) {
	if len(profile) == 0 {
		return img, nil
	}
	proc, err := nativeProc("dp_icc_srgb")
	if proc == nil || err != nil {
		return img, err
	}
	b := img.Bounds()
	out, ok := img.(*image.NRGBA64)
	if !ok || out.Stride != b.Dx()*8 {
		out = image.NewNRGBA64(b)
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				out.SetNRGBA64(x, y, tile.Straight(img.At(x, y)))
			}
		}
	}
	result, _, _ := proc.Call(uintptr(unsafe.Pointer(&profile[0])), uintptr(len(profile)),
		uintptr(unsafe.Pointer(&out.Pix[0])), uintptr(b.Dx()*b.Dy()))
	runtime.KeepAlive(profile)
	runtime.KeepAlive(out)
	if result == 0 {
		return nil, errors.New("Little CMS could not convert this RGB or grayscale profile")
	}
	return out, nil
}
