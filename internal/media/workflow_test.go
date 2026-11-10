package media_test

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/digimbyte/photobyte/internal/document"
	"github.com/digimbyte/photobyte/internal/exporter"
	"github.com/digimbyte/photobyte/internal/media"
	"github.com/digimbyte/photobyte/internal/project"
	"github.com/digimbyte/photobyte/internal/render"
	"github.com/digimbyte/photobyte/internal/tile"
)

var codecProbe func(string) error

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "--codec-helper" {
		var err error
		if codecProbe != nil && len(os.Args) == 3 && strings.HasPrefix(os.Args[2], "test-") {
			err = codecProbe(os.Args[2])
		} else {
			err = media.HelperMain(os.Args[2:])
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestWindowsICCConversion(t *testing.T) {
	exe, icc := os.Getenv("PHOTOBYTE_TEST_EXE"), os.Getenv("PHOTOBYTE_TEST_ICC")
	if exe == "" || icc == "" {
		t.Skip("set PHOTOBYTE_TEST_EXE and PHOTOBYTE_TEST_ICC for the native color check")
	}
	profile, err := os.ReadFile(icc)
	if err != nil {
		t.Fatal(err)
	}
	var source, chunk bytes.Buffer
	px := tile.Pixel{R: 30000, G: 18000, B: 54000, A: 20000}
	img := image.NewNRGBA64(image.Rect(0, 0, 1, 1))
	img.SetNRGBA64(0, 0, px)
	if err := png.Encode(&source, img); err != nil {
		t.Fatal(err)
	}
	chunk.WriteString("iCCPtest profile\x00\x00")
	compressed := zlib.NewWriter(&chunk)
	if _, err := compressed.Write(profile); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	const ihdrEnd = 33
	output.Write(source.Bytes()[:ihdrEnd])
	binary.Write(&output, binary.BigEndian, uint32(chunk.Len()-4))
	output.Write(chunk.Bytes())
	binary.Write(&output, binary.BigEndian, crc32.ChecksumIEEE(chunk.Bytes()))
	output.Write(source.Bytes()[ihdrEnd:])
	dir := t.TempDir()
	path := filepath.Join(dir, "profile.png")
	if err := os.WriteFile(path, output.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := tile.NewStore(filepath.Join(dir, "tiles"), tile.RawBytes)
	if err != nil {
		t.Fatal(err)
	}
	codec := media.NewBroker(exe, dir)
	state, message, err := codec.Import(t.Context(), path, store, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(message, "converted to sRGB") {
		t.Fatalf("color conversion did not run: %s", message)
	}
	got := render.NewScene(store, state, state.Frames[0].ID, document.SRGB, 0).Sampler().Pixel(0, 0)
	if got.A != px.A {
		t.Fatalf("ICC conversion changed alpha: %d -> %d", px.A, got.A)
	}
	raw, _, err := codec.Import(t.Context(), path, store, true)
	if err != nil {
		t.Fatal(err)
	}
	got = render.NewScene(store, raw, raw.Frames[0].ID, document.Data, 0).Sampler().Pixel(0, 0)
	if got != px {
		t.Fatalf("data import changed samples: %+v -> %+v", px, got)
	}
}

func TestPNGEditSaveRecoverAndExport(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	store, err := tile.NewStore(filepath.Join(dir, "tiles"), 2*tile.RawBytes)
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if path := os.Getenv("PHOTOBYTE_TEST_EXE"); path != "" {
		exe = path
	}
	codec := media.NewBroker(exe, dir)
	source := filepath.Join(dir, "source.png")
	img := image.NewNRGBA64(image.Rect(0, 0, 20, 12))
	hidden := tile.Pixel{R: 12345, G: 56789, B: 1001}
	img.SetNRGBA64(0, 0, hidden)
	for y := 1; y < 12; y++ {
		for x := range 20 {
			v := uint16(8000 + x*1200)
			img.SetNRGBA64(x, y, tile.Pixel{R: v, G: v, B: v, A: 65535})
		}
	}
	var original bytes.Buffer
	if err := png.Encode(&original, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, original.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	state, _, err := codec.Import(ctx, source, store, false)
	if err != nil {
		t.Fatal(err)
	}
	sampler := render.NewScene(store, state, state.Frames[0].ID, state.Space, 0).Sampler()
	if got := sampler.Pixel(0, 0); got != hidden {
		t.Fatalf("16-bit transparent RGB changed: %+v != %+v", got, hidden)
	}
	levels, err := render.AutoLevels(ctx, render.NewScene(store, state, state.Frames[0].ID, state.Space, 0))
	if err != nil || levels == document.NeutralLevels() {
		t.Fatalf("Auto Levels = %+v, %v", levels, err)
	}
	initial := document.NewSnapshot(state, "Import")
	ed := document.Open(initial)
	defer ed.Close()
	cmd := document.AddLayer{Kind: document.Adjustment, Name: "Auto Levels", Levels: levels}
	if err := ed.Apply(ctx, initial.Version, cmd); err != nil {
		t.Fatal(err)
	}
	if err := ed.Apply(ctx, initial.Version, cmd); !errors.Is(err, document.ErrStale) {
		t.Fatalf("stale edit accepted: %v", err)
	}
	saved := ed.Snapshot()
	file := filepath.Join(dir, "art.dphoto")
	if err := project.Save(ctx, file, store, saved, 0); err != nil {
		t.Fatal(err)
	}
	if err := ed.Apply(ctx, saved.Version, document.AddFrame{After: state.Frames[0].ID, Duplicate: true}); err != nil {
		t.Fatal(err)
	}
	dirty := ed.Snapshot()
	recovery := filepath.Join(dir, dirty.Project+".dphoto")
	if err := project.RecoverTo(ctx, recovery, store, dirty, saved.Version, file); err != nil {
		t.Fatal(err)
	}
	restoredStore, err := tile.NewStore(filepath.Join(dir, "reopened-tiles"), tile.RawBytes)
	if err != nil {
		t.Fatal(err)
	}
	restored, info, err := project.Load(ctx, recovery, restoredStore)
	if err != nil || restored.Version != dirty.Version || info.Manual != saved.Version || len(restored.State().Frames) != 2 {
		t.Fatalf("recover revision %d with metadata %+v: %v", restored.Version, info, err)
	}
	reopened, _, err := project.Load(ctx, file, restoredStore)
	if err != nil || reopened.Version != saved.Version || len(reopened.State().Frames) != 1 {
		t.Fatalf("manual save changed: %+v %v", reopened, err)
	}
	if err := project.Save(ctx, file, store, dirty, saved.Version); err != nil {
		t.Fatal(err)
	}
	if err := project.Save(ctx, file, store, dirty, saved.Version); !errors.Is(err, project.ErrConflict) {
		t.Fatalf("stale manual save = %v", err)
	}
	exp := exporter.Exporter{Store: restoredStore, Codec: codec, Scratch: dir}
	r := document.DefaultRecipe()
	r.Format, r.Depth = "apng", 16
	dst := filepath.Join(dir, "animation.png")
	if err := exp.Image(ctx, dst, restored.State(), restored.State().Frames[0].ID, restored.State().Bounds, r); err != nil {
		t.Fatal(err)
	}
	imported, _, err := codec.Import(ctx, dst, restoredStore, false)
	if err != nil || len(imported.Frames) != 2 || imported.Frames[0].Time != state.Frames[0].Time {
		t.Fatalf("APNG reimport failed: %v", err)
	}
	for _, format := range []string{"png", "gif", "jpg", "webp"} {
		t.Run(format, func(t *testing.T) {
			recipe := document.DefaultRecipe()
			recipe.Format = format
			if format == "png" {
				recipe.Space, recipe.Depth = document.P3, 16
			}
			s := restored.State()
			dest := filepath.Join(dir, "still."+format)
			if err := exp.Image(ctx, dest, s, s.Frames[0].ID, s.Bounds, recipe); err != nil {
				t.Fatal(err)
			}
			opened, _, err := codec.Import(ctx, dest, restoredStore, false)
			if err != nil {
				t.Fatal(err)
			}
			expectedFrames := 1
			if format == "gif" {
				expectedFrames = len(s.Frames)
			}
			if opened.Bounds.Size() != s.Bounds.Size() || opened.Space != recipe.Space || len(opened.Frames) != expectedFrames {
				t.Fatalf("unexpected %s round trip: bounds %v, space %s, %d frame(s)", format, opened.Bounds, opened.Space, len(opened.Frames))
			}
		})
	}
	if err := exp.Atlas(ctx, filepath.Join(dir, "sprites"), restored.State(), r); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"atlas.png", "atlas.json"} {
		if _, err := os.Stat(filepath.Join(dir, "sprites", name)); err != nil {
			t.Fatal(err)
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	dest := filepath.Join(dir, "canceled")
	if err := exp.Atlas(canceled, dest, restored.State(), r); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled export = %v", err)
	}
	if _, err := os.Stat(dest); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled export was published: %v", err)
	}
	out, err := os.ReadFile(source)
	if err != nil || !bytes.Equal(out, original.Bytes()) {
		t.Fatalf("source was changed: %v", err)
	}
	broken := filepath.Join(dir, "broken.png")
	if err := os.WriteFile(broken, original.Bytes()[:24], 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := codec.Import(ctx, broken, store, false); err == nil {
		t.Fatal("truncated image imported")
	}
	if _, _, err := codec.Import(ctx, source, store, false); err != nil {
		t.Fatalf("codec failed after a malformed image: %v", err)
	}
}
