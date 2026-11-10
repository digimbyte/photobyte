# PhotoByte icon library

[Browse the offline gallery](gallery.html) to search by tool name, asset ID, or reference app. The gallery previews 16, 24, 32 and 48 px and each card downloads the individual SVG.

## Source artwork

The individual SVGs are the editable source. Open them directly in a vector editor or edit the paths as text. Each icon has a stable `family/name` ID and lives at `assets/icons/<family>/<name>.svg`. Use the canonical ID across tools, menus and panels. Use the catalog's reference mappings to look up alternate tool names.

The artwork uses an original 24 × 24 grid with transparent backgrounds, rounded strokes and a palette for dark surfaces. The light ink carries the silhouette. Blue marks an active control or handle, violet marks depth or compositing, and coral marks a cut, removal or warning. Keep the silhouette and state differences readable without color.

| Area | Example IDs |
| --- | --- |
| Painting | `paint/brush`, `paint/pencil`, `paint/brush-mixer` |
| Selection | `selection/object-select`, `selection/sub-object-select` |
| Paths | `vector/spline`, `vector/spline-add`, `vector/spline-edit` |
| Workspaces | `widgets/panel-group`, `widgets/dock-left`, `widgets/layout-save` |
| Animation | `timeline/onion`, `timeline/frame-tag`, `timeline/keyframe-bezier` |
| 3D | `three-d/gizmo-rotate`, `three-d/vertex-select`, `three-d/text-3d` |
| Asset production | `assets/sprite-sheet`, `assets/slice-nine`, `assets/channel-pack` |

## Coverage

The [catalog](catalog.json) lists the assets and maps reference tool names to equivalent PhotoByte glyphs. It covers the current PhotoByte tool rail and panels, the CS6-era Photoshop toolbar and documented additions, Krita 5.3's toolbox and brush engines, GIMP 3.2's toolbox, and selected DaVinci Resolve 21 workflows and Fusion node families. Equivalent actions can share an SVG. The files include artwork for planned capabilities as well as controls available in PhotoByte today. [Photoshop tools](https://helpx.adobe.com/photoshop/desktop/get-started/set-up-toolbars-panels/customize-the-toolbar.html) · [Krita tools](https://docs.krita.org/en/reference_manual/tools.html) · [GIMP tools](https://docs.gimp.org/3.2/en/gimp-tools.html) · [DaVinci Resolve Fusion](https://www.blackmagicdesign.com/products/davinciresolve/fusion)

## Add or update an icon

1. Save the editable 24 × 24 SVG under its family. Use static SVG shapes with inline geometry and the catalog palette so it works as a standalone file.
2. Keep existing IDs stable. Add new IDs, titles and any reference aliases to `catalog.json`. Keep the SVG `title` equal to the catalog title.
3. After a catalog change, run `pwsh -File scripts/sync-icon-gallery.ps1` from the repository root to refresh the gallery's embedded data. Artwork-only edits appear immediately when reloading the gallery.
4. Run `go test ./assets/icons` and check the glyph at 16 and 24 px in the gallery. For PNG use, rasterize the SVG at the required output size.
