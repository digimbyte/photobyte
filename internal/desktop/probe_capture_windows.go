//go:build windows && releaseprobe

package desktop

import (
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"runtime"
	"sync/atomic"
	"time"
	"unsafe"

	"gioui.org/app"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"golang.org/x/sys/windows"
)

var (
	probeUser32         = windows.NewLazySystemDLL("user32.dll")
	probeClientToScreen = probeUser32.NewProc("ClientToScreen")
	probeWindowAt       = probeUser32.NewProc("WindowFromPoint")
	probeScreenDC       = probeUser32.NewProc("GetDC")
	probeReleaseDC      = probeUser32.NewProc("ReleaseDC")
	probeGetPixel       = windows.NewLazySystemDLL("gdi32.dll").NewProc("GetPixel")
	probeMarkerBounds   = image.Rect(4, 4, 12, 12)
	probeDWMClock       = windows.NewLazySystemDLL("dwmapi.dll").NewProc("DwmGetCompositionTimingInfo")
	probeCounter        = windows.NewLazySystemDLL("kernel32.dll").NewProc("QueryPerformanceCounter")
	probeFrequency      = windows.NewLazySystemDLL("kernel32.dll").NewProc("QueryPerformanceFrequency")
)

const probeMarkerRed, probeMarkerGreen = 0x42, 0xcd

type pixelRequest struct {
	marker uint8
	after  uint64
}

type pixelObservation struct {
	marker uint8
	at     time.Time
	err    error
	read   time.Duration
	polls  int
	clock  compositorClock
}

// Probe one generated pixel on the interactive desktop. The observer checks
// the window under the marker before reading and keeps only the timestamp.
type probeCapture struct {
	trace    bool
	requests chan pixelRequest
	stop     chan struct{}
	done     chan struct{}
	latest   atomic.Pointer[pixelObservation]
}

func (c *probeCapture) open(hwnd uintptr) error {
	c.requests, c.stop, c.done = make(chan pixelRequest, 1), make(chan struct{}), make(chan struct{})
	ready := make(chan error, 1)
	go c.observe(hwnd, ready)
	return <-ready
}

func (c *probeCapture) observe(hwnd uintptr, ready chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(c.done)
	dc, _, _ := probeScreenDC.Call(0)
	if dc == 0 {
		ready <- errors.New("cannot read a marker on this desktop")
		return
	}
	defer probeReleaseDC.Call(0, dc)
	ready <- nil
	for {
		select {
		case <-c.stop:
			return
		case req := <-c.requests:
			c.latest.Store(c.waitPixel(hwnd, dc, req))
		}
	}
}

func (c *probeCapture) waitPixel(hwnd, dc uintptr, req pixelRequest) *pixelObservation {
	result := &pixelObservation{marker: req.marker}
	center := probeMarkerBounds.Min.Add(probeMarkerBounds.Size().Div(2))
	point := struct{ X, Y int32 }{int32(center.X), int32(center.Y)}
	ok, _, _ := probeClientToScreen.Call(hwnd, uintptr(unsafe.Pointer(&point)))
	if ok == 0 {
		result.err = errors.New("cannot locate the probe marker")
		return result
	}
	xy := uintptr(uint64(uint32(point.Y))<<32 | uint64(uint32(point.X)))
	want := uintptr(req.marker)<<16 | probeMarkerGreen<<8 | probeMarkerRed
	until := time.Now().Add(time.Second)
	for time.Now().Before(until) {
		select {
		case <-c.stop:
			return result
		default:
		}
		if receipt := app.PhotoBytePresentState(); receipt.Sequence > req.after && receipt.Displayed {
			at, _, _ := probeWindowAt.Call(xy)
			if at != hwnd {
				result.err = errors.New("the editor marker is obscured on the desktop")
				return result
			}
			readStarted := time.Now()
			value, _, _ := probeGetPixel.Call(dc, uintptr(point.X), uintptr(point.Y))
			result.polls++
			if value == want {
				result.at = time.Now()
				result.read = result.at.Sub(readStarted)
				if c.trace {
					result.clock = readCompositorClock()
				}
				return result
			}
		}
		time.Sleep(time.Millisecond)
	}
	result.err = errors.New("the submitted brush marker did not become visible on the desktop")
	return result
}

type compositorClock struct {
	vblank, compose time.Time
	period          time.Duration
}

// DWM_TIMING_INFO is byte-packed on Windows. Read only the clock fields
// needed to diagnose display cadence, after recording the observed pixel.
func readCompositorClock() compositorClock {
	var info [292]byte
	binary.LittleEndian.PutUint32(info[:4], uint32(len(info)))
	status, _, _ := probeDWMClock.Call(0, uintptr(unsafe.Pointer(&info[0])))
	if int32(status) != 0 {
		return compositorClock{}
	}
	var counter, frequency int64
	probeFrequency.Call(uintptr(unsafe.Pointer(&frequency)))
	now := time.Now()
	probeCounter.Call(uintptr(unsafe.Pointer(&counter)))
	if frequency == 0 || counter == 0 {
		return compositorClock{}
	}
	duration := func(ticks int64) time.Duration {
		return time.Duration(float64(ticks) * float64(time.Second) / float64(frequency))
	}
	readQPC := func(offset int) time.Time {
		ticks := int64(binary.LittleEndian.Uint64(info[offset : offset+8]))
		if ticks == 0 {
			return time.Time{}
		}
		return now.Add(duration(ticks - counter))
	}
	return compositorClock{
		vblank: readQPC(28), compose: readQPC(48),
		period: duration(int64(binary.LittleEndian.Uint64(info[12:20]))),
	}
}

func (c *probeCapture) close() {
	if c.stop != nil {
		close(c.stop)
		<-c.done
	}
}

func drawProbeMarker(gtx layout.Context, marker uint8) {
	paint.FillShape(gtx.Ops, color.NRGBA{R: probeMarkerRed, G: probeMarkerGreen, B: marker, A: 255}, clip.Rect(probeMarkerBounds).Op())
}
