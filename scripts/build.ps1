[CmdletBinding()]
param([switch]$Console, [switch]$GoOnly, [switch]$ReleaseProbe)

$ErrorActionPreference = 'Stop'
$root = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot '..')).Path
Push-Location -LiteralPath $root
try {
    $cache = Join-Path $root '.cache'
    if (-not $env:GOMODCACHE) { $env:GOMODCACHE = Join-Path $cache 'gomod' }
    if (-not $env:GOCACHE) { $env:GOCACHE = Join-Path $cache 'gobuild' }
    if (-not $env:GOTMPDIR) { $env:GOTMPDIR = Join-Path $cache 'gotmp' }
    $overlayDir = Join-Path $cache $(if ($ReleaseProbe) { 'gio-overlay-probe' } else { 'gio-overlay' })
    $out = if ($ReleaseProbe) { Join-Path $cache 'release-probe' } else { Join-Path $root 'bin' }
    New-Item -ItemType Directory -Force -Path $env:GOTMPDIR, $overlayDir, $out | Out-Null

    $gio = go list -mod=readonly -m -f '{{.Dir}}' gioui.org
    if ($LASTEXITCODE -ne 0 -or -not $gio) {
        go mod download gioui.org
        if ($LASTEXITCODE -ne 0) { throw 'Could not download the pinned Gio module.' }
        $gio = go list -mod=readonly -m -f '{{.Dir}}' gioui.org
    }
    if ($LASTEXITCODE -ne 0 -or -not $gio) { throw 'Could not locate the pinned Gio module.' }

    $driverPath = Join-Path $gio 'app/d3d11_windows.go'
    $windowPath = Join-Path $gio 'app/window.go'
    $eventsPath = Join-Path $gio 'app/os_windows.go'
    $dxgiPath = Join-Path $gio 'internal/d3d11/d3d11_windows.go'
    $driver = (Get-Content -LiteralPath $driverPath -Raw).Replace("`r`n", "`n")
    $window = (Get-Content -LiteralPath $windowPath -Raw).Replace("`r`n", "`n")
    $events = (Get-Content -LiteralPath $eventsPath -Raw).Replace("`r`n", "`n")
    $dxgi = (Get-Content -LiteralPath $dxgiPath -Raw).Replace("`r`n", "`n") + "`n" +
        (Get-Content -LiteralPath (Join-Path $PSScriptRoot 'gio-dxgi-flip.inc') -Raw)
    $init = [regex]::new('(?s)func init\(\) \{.*?\n\}\n(?=\s*func \(c \*d3d11Context\) API)')
    $present = 'return wrapErr(c.swchain.Present(1, 0))'
    $handleLoss = "if errors.Is(err, gpu.ErrDeviceLost) {`n"
    if ($init.Matches($driver).Count -ne 1 -or -not $driver.Contains($present) -or
        -not $window.Contains($handleLoss) -or -not $window.Contains('err = w.ctx.Present()') -or
        -not $events.Contains('case windows.WM_DESTROY:') -or
        -not $events.Contains("runtime.LockOSThread()`n")) {
        throw 'The pinned Gio Windows backend changed. Review the presentation overlay before building.'
    }

    $driver = $driver.Replace("import (`n", "import (`n`t`"os`"`n`t`"sync`"`n`t`"sync/atomic`"`n")
    $driver = $driver.Replace("type d3d11Context struct {`n", "type d3d11Context struct {`n`tdriverName string`n")
    $adapter = @'
var digiPhotoDeviceLoss atomic.Uint32
var digiPhotoReportOnce sync.Once

func init() {
	digiPhotoPresentationLost = func() { digiPhotoDeviceLoss.Add(1) }
	drivers = append(drivers, gpuAPI{
		priority: 1,
		initializer: func(w *window) (context, error) {
			losses := digiPhotoDeviceLoss.Load()
			if losses >= 4 {
				return nil, fmt.Errorf("presentation device failed repeatedly")
			}
			if losses < 2 && os.Getenv("DIGIPHOTO_SOFTWARE") != "1" && os.Getenv("DIGIPHOTO_TEST_HARDWARE_FAILURE") != "1" {
				if ctx, err := digiPhotoD3D11(w, d3d11.DRIVER_TYPE_HARDWARE, "hardware"); err == nil {
					return ctx, nil
				}
			}
			// D3D_DRIVER_TYPE_WARP is 5 in the Windows D3D headers.
			return digiPhotoD3D11(w, 5, "warp")
		},
	})
}

func digiPhotoD3D11(w *window, driver uint32, name string) (context, error) {
	hwnd, _, _ := w.HWND()
	var flags uint32
	if debugDirectX {
		flags |= d3d11.CREATE_DEVICE_DEBUG
	}
	dev, ctx, _, err := d3d11.CreateDevice(driver, flags)
	if err != nil {
		return nil, fmt.Errorf("create %s D3D11 context: %w", name, err)
	}
	if name == "hardware" {
		// Give interactive updates a single queued frame on this device.
		if err := dev.DigiPhotoLimitFrameQueue(); err != nil {
			d3d11.IUnknownRelease(unsafe.Pointer(ctx), ctx.Vtbl.Release)
			d3d11.IUnknownRelease(unsafe.Pointer(dev), dev.Vtbl.Release)
			return nil, err
		}
	}
	chain, err := d3d11.CreateSwapChain(dev, hwnd)
	if err != nil {
		d3d11.IUnknownRelease(unsafe.Pointer(ctx), ctx.Vtbl.Release)
		d3d11.IUnknownRelease(unsafe.Pointer(dev), dev.Vtbl.Release)
		return nil, err
	}
	return &d3d11Context{win: w, dev: dev, ctx: ctx, swchain: chain, driverName: name}, nil
}

'@
    $driver = $init.Replace($driver, $adapter + "`n")
    $driver = $driver.Replace('d3d11.CreateSwapChain(dev, hwnd)', 'd3d11.DigiPhotoCreateSwapChain(dev, hwnd)')
    $driver = $driver.Replace('c.dev.CreateRenderTargetView(texture)', 'c.dev.DigiPhotoCreateSRGBRenderTargetView(texture)')
    $driver = $driver.Replace('if c.renderTarget != nil {', "if c.renderTarget != nil {`n`t`tc.ctx.OMSetRenderTargets(nil, nil)")
    $driver = $driver.Replace("if c.ctx != nil {`n", "if c.ctx != nil {`n`t`tc.ctx.DigiPhotoFlushReleasedBindings()`n")
    $driver = $driver.Replace($present, @'
err := wrapErr(c.swchain.Present(0, 0))
	if err == nil {
		digiPhotoReportOnce.Do(func() {
			if report := os.Getenv("DIGIPHOTO_PRESENT_REPORT"); report != "" {
				os.WriteFile(report, []byte(c.driverName), 0600)
			}
		})
	}
	return err
'@)

    if ($ReleaseProbe) {
        $driver = $driver.Replace("import (`n", "import (`n`t`"time`"`n`n`t`"golang.org/x/sys/windows`"`n")
        $driver = $driver.Replace("`tdriverName string`n", "`tdriverName string`n`tdrawBegan time.Time`n")
        $window = $window.Replace("if err := w.frame(frame, size); err != nil {", @'
if ctx, ok := w.ctx.(*d3d11Context); ok {
				ctx.drawBegan = time.Now()
			}
			if err := w.frame(frame, size); err != nil {
'@)
        $dxgi = $dxgi.Replace("import (`n", "import (`n`t`"time`"`n")
        $dxgi += @'

// Wait for the actual GPU commands when the desktop compositor cannot
// display this window. This path is used only by the release probe.
func (c *DeviceContext) DigiPhotoAwaitFrame(d *Device) error {
	var desc [2]uint32 // D3D11_QUERY_EVENT, no miscellaneous flags
	var query *IUnknown
	code, _, _ := syscall.SyscallN(d.Vtbl.CreateQuery, uintptr(unsafe.Pointer(d)),
		uintptr(unsafe.Pointer(&desc[0])), uintptr(unsafe.Pointer(&query)))
	if code != 0 {
		return ErrorCode{Name: "CreateQuery", Code: uint32(code)}
	}
	defer IUnknownRelease(unsafe.Pointer(query), query.Vtbl.Release)
	syscall.SyscallN(c.Vtbl.End, uintptr(unsafe.Pointer(c)), uintptr(unsafe.Pointer(query)))
	syscall.SyscallN(c.Vtbl.Flush, uintptr(unsafe.Pointer(c)))
	until := time.Now().Add(150 * time.Millisecond)
	for {
		code, _, _ = syscall.SyscallN(c.Vtbl.GetData, uintptr(unsafe.Pointer(c)),
			uintptr(unsafe.Pointer(query)), 0, 0, 1)
		switch uint32(code) {
		case 0: // S_OK
			return nil
		case 1: // S_FALSE, the GPU is still busy
			if time.Now().After(until) {
				return fmt.Errorf("timed out waiting for the submitted GPU frame")
			}
			time.Sleep(time.Millisecond)
		default:
			return ErrorCode{Name: "GetData", Code: uint32(code)}
		}
	}
}
'@
        $driver = $driver.Replace('err := wrapErr(c.swchain.Present(0, 0))', @'
if digiPhotoInjectedLoss.Swap(false) {
		return wrapErr(d3d11.ErrorCode{Name: "Present", Code: d3d11.DXGI_ERROR_DEVICE_REMOVED})
	}
	started := time.Now()
	present := c.swchain.Present(0, 0)
	submitted := time.Now()
	err := wrapErr(present)
	displayed := present == nil
	composited := false
	if digiPhotoMeasureNext.Swap(false) && displayed {
		// Confirm the submitted DirectX updates reached the compositor.
		// Only probe samples wait; the shipping presenter never flushes.
		code, _, _ := digiPhotoDwmFlush.Call()
		composited = uint32(code) == 0
	}
	if code, ok := present.(d3d11.ErrorCode); ok && code.Code == d3d11.DXGI_STATUS_OCCLUDED {
		digiPhotoOccludedFrames.Add(1)
		if ready := c.ctx.DigiPhotoAwaitFrame(c.dev); ready != nil {
			return wrapErr(ready)
		}
	}
	if err == nil {
		digiPhotoPresentReceipt.Store(&DigiPhotoReceipt{
			Sequence: digiPhotoPresentSequence.Add(1),
			Driver: c.driverName,
			At: time.Now(),
			DrawBegan: c.drawBegan,
			SubmittedAt: submitted,
			PresentCall: submitted.Sub(started),
			Displayed: displayed,
			Composited: composited,
			Losses: digiPhotoDeviceLoss.Load(),
		})
	}
'@)
        $driver += @'

type DigiPhotoReceipt struct {
	Sequence uint64
	Driver string
	At time.Time
	DrawBegan time.Time
	SubmittedAt time.Time
	PresentCall time.Duration
	Displayed bool
	Composited bool
	Losses uint32
}

var digiPhotoInjectedLoss atomic.Bool
var digiPhotoMeasureNext atomic.Bool
var digiPhotoDwmFlush = windows.NewLazySystemDLL("dwmapi.dll").NewProc("DwmFlush")
var digiPhotoPresentSequence atomic.Uint64
var digiPhotoOccludedFrames atomic.Uint64
var digiPhotoPresentReceipt atomic.Pointer[DigiPhotoReceipt]

func DigiPhotoPresentState() DigiPhotoReceipt {
	if current := digiPhotoPresentReceipt.Load(); current != nil {
		return *current
	}
	return DigiPhotoReceipt{}
}

func DigiPhotoInjectDeviceLoss() { digiPhotoInjectedLoss.Store(true) }
func DigiPhotoMeasureNextPresent() { digiPhotoMeasureNext.Store(true) }
func DigiPhotoOccludedFrames() uint64 { return digiPhotoOccludedFrames.Load() }
'@
    }
    $first = 'func (w *Window) validateAndProcess'
    $window = $window.Replace($first, "var digiPhotoPresentationLost func()`n`n$first")
    $window = $window.Replace($handleLoss, $handleLoss + @'
				if digiPhotoPresentationLost != nil {
					digiPhotoPresentationLost()
				}

'@)
    $window = $window.Replace("err = w.ctx.Present()`n`t`t`tw.ctx.Unlock()", @'
err = w.ctx.Present()
			w.ctx.Unlock()
			if errors.Is(err, gpu.ErrDeviceLost) {
				w.destroyGPU()
				if digiPhotoPresentationLost != nil {
					digiPhotoPresentationLost()
				}
				w.setNextFrame(time.Now())
				return nil
			}
'@)
    $driverOverlay = Join-Path $overlayDir 'd3d11_windows.go'
    $windowOverlay = Join-Path $overlayDir 'window.go'
    $eventsOverlay = Join-Path $overlayDir 'os_windows.go'
    $dxgiOverlay = Join-Path $overlayDir 'dxgi_d3d11_windows.go'
    $events = $events.Replace("import (`n", "import (`n`t`"sync/atomic`"`n")
    $events = $events.Replace("runtime.LockOSThread()`n", @'
runtime.LockOSThread()
		// This dedicated Win32 thread draws and presents the editor window.
		// Give it priority over the background export and codec work.
		const threadPriorityAboveNormal = 1
		syscall.NewLazySystemDLL("kernel32.dll").NewProc("SetThreadPriority").
			Call(uintptr(syscall.CurrentThread()), threadPriorityAboveNormal)

'@)
    $events = $events.Replace('case windows.WM_DESTROY:', @'
case windows.WM_CLOSE:
		if handler := digiPhotoCloseHandler.Load(); handler != nil {
			(*handler)()
			return 0
		}
		return windows.DefWindowProc(hwnd, msg, wParam, lParam)
	case windows.WM_DESTROY:
'@)
    $events += @'

var digiPhotoCloseHandler atomic.Pointer[func()]

func DigiPhotoSetCloseHandler(handler func()) {
	if handler == nil {
		digiPhotoCloseHandler.Store(nil)
		return
	}
	digiPhotoCloseHandler.Store(&handler)
}
'@
    $json = Join-Path $overlayDir 'overlay.json'
    # Go disallows overlays inside its checksum-verified module cache.
    # Work on a disposable copy and use a separate build-only module file.
    $gioCopy = Join-Path $overlayDir 'gioui.org'
    New-Item -ItemType Directory -Force -Path $gioCopy | Out-Null
    Copy-Item -Path (Join-Path $gio '*') -Destination $gioCopy -Recurse -Force
    $buildMod = Join-Path $overlayDir 'build.mod'
    Copy-Item -LiteralPath (Join-Path $root 'go.mod') -Destination $buildMod -Force
    Copy-Item -LiteralPath (Join-Path $root 'go.sum') -Destination (Join-Path $overlayDir 'build.sum') -Force
    go mod edit "-modfile=$buildMod" "-replace=gioui.org=$gioCopy"
    if ($LASTEXITCODE -ne 0) { throw 'Could not configure the local Gio backend.' }
    $utf8 = [System.Text.UTF8Encoding]::new($false)
    [System.IO.File]::WriteAllText($driverOverlay, $driver, $utf8)
    [System.IO.File]::WriteAllText($windowOverlay, $window, $utf8)
    [System.IO.File]::WriteAllText($eventsOverlay, $events, $utf8)
    [System.IO.File]::WriteAllText($dxgiOverlay, $dxgi, $utf8)
    $replace = @{}
    $replace[(Join-Path $gioCopy 'app/d3d11_windows.go')] = $driverOverlay
    $replace[(Join-Path $gioCopy 'app/window.go')] = $windowOverlay
    $replace[(Join-Path $gioCopy 'app/os_windows.go')] = $eventsOverlay
    $replace[(Join-Path $gioCopy 'internal/d3d11/d3d11_windows.go')] = $dxgiOverlay
    [System.IO.File]::WriteAllText($json, (@{ Replace = $replace } | ConvertTo-Json), $utf8)

    $flags = '-X main.warpBuild=enabled'
    if (-not $Console) { $flags += ' -H=windowsgui' }
    $tags = if ($ReleaseProbe) { @('-tags', 'digiphoto_win,releaseprobe') } else { @('-tags', 'digiphoto_win') }
    go build -mod=readonly "-modfile=$buildMod" -trimpath "-overlay=$json" "-ldflags=$flags" @tags -o (Join-Path $out 'DigiPhoto.exe') ./cmd/digiphoto
    if ($LASTEXITCODE -ne 0) { throw 'DigiPhoto build failed.' }
    if (-not $GoOnly) { & (Join-Path $PSScriptRoot 'build-codecs.ps1') }
    if ($ReleaseProbe) {
        Copy-Item -LiteralPath (Join-Path $root 'bin/digiphoto_codecs.dll') -Destination $out -Force
    }
}
finally {
    Pop-Location
}
