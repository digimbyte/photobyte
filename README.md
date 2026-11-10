# DigiPhoto

DigiPhoto is an offline Windows x64 editor for layered raster editing, frame animation, and web and game asset export. The editor and desktop interface are written in Go. The Windows codec helper uses libpng, libwebp, and Little CMS.

[CS6+ product goal](docs/goal.md)

[Editable SVG icon library](assets/icons/README.md)

## Run

Open `DigiPhoto.exe` from the extracted Windows release ZIP or the built `bin` folder. Keep `digiphoto_codecs.dll` alongside it.

1. Open an image or create a project. Use **Place Image** in the Layers panel to embed another raster. Save editable layers, frames, and undo checkpoints as a `.dphoto` file.
2. Use **Auto Levels** to add an editable adjustment. Select that layer to compare **Before / After** or edit the curve in **Properties**.
3. Use **Timeline** to duplicate and reorder frames, set exact frame durations, and preview playback or onion skinning.
4. Use **Assets** to name a selection, save an export recipe, or publish a sprite sheet and its JSON metadata. Batch output goes into a new folder.

Open and export PNG at 8 or 16 bits, APNG, GIF, JPEG, and still WebP. Display P3 PNG/APNG uses color-space tags. Use a data project for raw texture samples. Recovery is available from the toolbar and opens automatically when an unsaved checkpoint is found at startup.

Use `Ctrl+N`, `Ctrl+O`, `Ctrl+S`, `Ctrl+Shift+S`, `Ctrl+Z`, and `Ctrl+Shift+Z` for common commands. Scroll over the canvas to zoom and use **Hand** or the middle mouse button to pan.

## Arrange the workspace

- Drag a panel tab onto another group's tab strip to join it. Drop between groups to stack it separately, or at the left, right, or bottom edge of the canvas to dock it there.
- Drag the handle at the start of a tab strip to move the entire group. Drag a divider to resize a dock or its groups. Use the group menu to collapse, hide, move, or merge panels with the keyboard.
- Open **Workspace** to show hidden panels, save the current arrangement under a name, switch between saved layouts, or reset to the selected layout. DigiPhoto also restores the last working arrangement at startup. Safe Mode opens the default workspace and keeps layout changes temporary.

## Build and verify

The Windows build requires Go 1.25+ and the Visual Studio C++ build tools. Use CMake 3.21+ with Visual Studio 2022 or CMake 4.2+ with Visual Studio 2026. Run these commands from the repository root in PowerShell.

```powershell
.\scripts\build.ps1
.\bin\DigiPhoto.exe
```

The build verifies the pinned native source archives and includes their licenses under `bin\licenses`. The [Gio-licensed](scripts/LICENSE.gio) presentation adapter is built from a disposable copy of the pinned module. Use `-GoOnly` for a Go development build or `--safe-mode` on the executable to use WARP.

```powershell
$env:DIGIPHOTO_TEST_EXE = (Resolve-Path .\bin\DigiPhoto.exe).Path
$env:DIGIPHOTO_VISUAL_SMOKE = '1'
go test ./...
go test ./internal/media -run '^$' -fuzz FuzzPNGChunks -fuzztime 10s
```

Set `DIGIPHOTO_TEST_ICC` to an RGB ICC profile to run the native color conversion fixture. Clear `DIGIPHOTO_TEST_EXE` to test the portable Go codecs. The optional graphics test renders the workspace at 100%, 150%, and 200% DPI. For presentation diagnostics, `--presentation-report` writes the driver name after presentation succeeds.

### Cross-platform checks

The [build and test workflow](.github/workflows/checks.yml) checks the native Windows x64 build and the macOS development build. On macOS, use Go 1.25+ and Xcode Command Line Tools to compile the editor and run the tests with the portable codecs.

```sh
go test ./...
mkdir -p bin
go build -o bin/DigiPhoto ./cmd/digiphoto
```

Run the on-screen latency check separately on an interactive Windows desktop.

## Windows release checks

Run the display gate on the Windows 11 reference machine with eight logical cores, 16 GiB of RAM, integrated graphics, and the test window unobscured.

```powershell
.\scripts\test-race.ps1
.\scripts\test-live-release.ps1 -RequirePresentedSamples
.\scripts\test-full-volume.ps1
.\scripts\package-windows.ps1
```

- The race script uses a pinned portable MinGW-w64 toolchain by default. Use `-Compiler` to point it at an existing compatible GCC installation.
- The live probe drives brush input through a native editor window during a 4K, eight-layer APNG export. With `-RequirePresentedSamples`, it verifies that each of 32 tagged brush frames per driver reached an unobscured interactive desktop after a successful DXGI Present. It checks p95 input-to-visible-frame latency against 33 ms on hardware and 66 ms on WARP, plus durable autosave within two seconds. Two injected DXGI device-removal errors exercise hardware recreation followed by WARP fallback. Set `DIGIPHOTO_PROBE_PHASES=1` to record per-frame timings alongside the report. To build and run in separate shells, build with `-ReleaseProbe -Console`, then run the test script with `-SkipBuild`.
- The full-volume check requires an elevated PowerShell session. It fills a new 64 MiB NTFS VHD, verifies pending edits and Save Copy, then frees space and reopens the durable recovery revision. It detaches the image when finished.
- The packaged codec runs in a capability-free Windows AppContainer and a resource-limited Job Object. It gets a read-only input handle and bounded output pipes. The editor owns the staged files. Tests verify denied private-file and loopback access, and cancellation of a running helper.
