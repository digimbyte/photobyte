# DigiPhoto product goal

Build a native, offline **CS6+** editor for photo editing, web graphics, and game assets. Use Photoshop CS6 Standard as the baseline for familiar editing behavior. Improve on it with responsive previews, recoverable projects, current formats, and repeatable asset exports. [Photoshop CS6 reference](https://help.adobe.com/archive/en/photoshop/cs6/photoshop_reference.pdf)

## CS6 editing baseline

- Give users a tabbed canvas, familiar shortcuts, a tool rail and options bar, and grouped panels they can arrange, save, and reset.
- Support the complete editing loop from selections, brush and eraser, crop and transform through layers, groups, masks, blend modes, and editable tonal adjustments. Each committed gesture gets one undo step.
- Close parity gaps through realistic workflows such as retouching, Curves, editable text and paths, reusable layer content, and batch actions. Use those workflows to drive regression tests for stability and usability.

## What the plus adds

- Keep previews responsive during edits, saves, and exports with tiled rendering and bounded background work. Preserve editable revisions through crashes, device loss, and disk exhaustion.
- Give animation a frame timeline with timing, tags, loop playback, and onion skinning. Preserve frame controls and color intent when importing and exporting.
- Make 8-bit and 16-bit PNG, APNG, GIF, JPEG, and still WebP practical for production. Provide repeatable exports for named slices and deterministic sprite sheets with JSON metadata. Preserve packed channel values for game textures.

## Design influences

- **DaVinci Resolve** informs task-focused workspaces and a color workflow with adjustable corrections and clear before-and-after comparisons. [Resolve Color](https://www.blackmagicdesign.com/products/davinciresolve/color)
- **GIMP** informs tiled image processing and filters users can preview, revise, or remove after applying them. [GEGL architecture](https://developer.gimp.org/resource/writing-a-filter/architecture-install/) · [GIMP filters](https://developer.gimp.org/resource/writing-a-filter/)
- **Krita** informs responsive painting and a dockable animation timeline with direct frame editing and onion skin controls. [Krita Animation Timeline](https://docs.krita.org/en/reference_manual/dockers/animation_timeline.html)

## Release order

Qualify Windows 11 x64 first, then carry the same Go engine to macOS and Linux. During export of a 4K, eight-layer project, target p95 input-to-visible-preview latency of 33 ms on hardware and 66 ms in WARP. Make ordinary edits durable within two seconds. Verify that the last committed project opens, pending edits can be recovered, and canceled exports publish no partial output.
