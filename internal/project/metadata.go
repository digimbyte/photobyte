package project

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"

	"github.com/digimbyte/photobyte/internal/document"
	"github.com/digimbyte/photobyte/internal/tile"
)

const (
	packedMetadata = "PBM1\n"
	metadataPrefix = "meta:"
	tileRefSize    = 4 + sha256.Size
	refsPerChunk   = 4096
	maxGridChunks  = (2049*2049 + refsPerChunk - 1) / refsPerChunk
	// A state may contain MaxLayers × MaxFrames cel references. The byte
	// limit applies to one state's properties, with pixel maps in separate blobs.
	maxStateMetadata = 64 << 20
)

type packedSnapshot struct {
	Project string
	Version uint64
	Cursor  int
	History []checkpointRef
}

type checkpointRef struct {
	Label string
	State string
}

// A checkpoint stores document properties and references to immutable tile
// maps. Maps, map chunks and checkpoint bodies share the blob table with
// raster tiles, under separate keys. The SQLite schema stays the same.
type gridRef struct {
	Blocks  string `json:",omitempty"`
	Default tile.Pixel
}

type packedLayer struct {
	document.Layer
	Image gridRef
	Mask  *gridRef
}

type packedFrame struct {
	document.Frame
	Cels map[document.ID]gridRef
}

type packedState struct {
	document.State
	Layers []packedLayer
	Frames []packedFrame
	Poster *gridRef
}

type metadataWriter struct {
	ctx    context.Context
	tx     *sql.Tx
	blobs  map[string]bool
	grids  map[uintptr]string
	states map[*document.State]string
	tiles  map[tile.Hash]struct{}
}

func writeMetadata(ctx context.Context, tx *sql.Tx, s document.Snapshot) ([]byte, map[tile.Hash]struct{}, error) {
	w := metadataWriter{ctx: ctx, tx: tx, blobs: make(map[string]bool), grids: make(map[uintptr]string),
		states: make(map[*document.State]string), tiles: make(map[tile.Hash]struct{})}
	root := packedSnapshot{Project: s.Project, Version: s.Version, Cursor: s.Cursor, History: make([]checkpointRef, len(s.History))}
	for i, entry := range s.History {
		key, err := w.state(entry.State)
		if err != nil {
			return nil, nil, err
		}
		root.History[i] = checkpointRef{Label: entry.Label, State: key}
	}
	data, err := json.Marshal(root)
	return append([]byte(packedMetadata), data...), w.tiles, err
}

func (w *metadataWriter) put(data []byte) (string, error) {
	if len(data) > maxStateMetadata {
		return "", errors.New("a project metadata record exceeds its size limit")
	}
	key := metadataKey(data)
	if w.blobs[key] {
		return key, nil
	}
	if _, err := w.tx.ExecContext(w.ctx, "INSERT OR IGNORE INTO tiles(hash, data) VALUES(?,?)", key, data); err != nil {
		return "", err
	}
	if _, err := w.tx.ExecContext(w.ctx, "INSERT INTO live_tiles(hash) VALUES(?)", key); err != nil {
		return "", err
	}
	w.blobs[key] = true
	return key, nil
}

func metadataKey(data []byte) string { return fmt.Sprintf("%s%x", metadataPrefix, sha256.Sum256(data)) }

func (w *metadataWriter) grid(g tile.Grid) (gridRef, error) {
	ref := gridRef{Default: g.Default}
	if g.Blocks == nil {
		return ref, nil
	}
	// The snapshot owns the immutable maps for the duration of the write.
	// Map identity avoids walking the same map for every undo checkpoint.
	identity := reflect.ValueOf(g.Blocks).Pointer()
	if key, ok := w.grids[identity]; ok {
		ref.Blocks = key
		return ref, nil
	}
	coords := make([]tile.Coord, 0, len(g.Blocks))
	for c := range g.Blocks {
		coords = append(coords, c)
	}
	slices.SortFunc(coords, func(a, b tile.Coord) int {
		if a.Y == b.Y {
			return cmp.Compare(a.X, b.X)
		}
		return cmp.Compare(a.Y, b.Y)
	})
	chunks := make([]string, 0, (len(coords)+refsPerChunk-1)/refsPerChunk)
	buffer := make([]byte, 0, min(len(coords), refsPerChunk)*tileRefSize)
	for part := range slices.Chunk(coords, refsPerChunk) {
		if err := w.ctx.Err(); err != nil {
			return gridRef{}, err
		}
		buffer = buffer[:0]
		for _, c := range part {
			hash := g.Blocks[c]
			buffer = binary.LittleEndian.AppendUint16(buffer, uint16(int16(c.X)))
			buffer = binary.LittleEndian.AppendUint16(buffer, uint16(int16(c.Y)))
			buffer = append(buffer, hash[:]...)
			w.tiles[hash] = struct{}{}
		}
		key, err := w.put(buffer)
		if err != nil {
			return gridRef{}, err
		}
		chunks = append(chunks, key)
	}
	data, err := json.Marshal(chunks)
	if err != nil {
		return gridRef{}, err
	}
	ref.Blocks, err = w.put(data)
	if err == nil {
		w.grids[identity] = ref.Blocks
	}
	return ref, err
}

func (w *metadataWriter) state(s *document.State) (string, error) {
	if key, ok := w.states[s]; ok {
		return key, nil
	}
	state := packedState{State: *s}
	if s.Layers != nil {
		state.Layers = make([]packedLayer, len(s.Layers))
	}
	state.Frames = make([]packedFrame, len(s.Frames))
	for i, layer := range s.Layers {
		image, err := w.grid(layer.Image)
		if err != nil {
			return "", err
		}
		stored := packedLayer{Layer: layer, Image: image}
		if layer.Mask != nil {
			mask, err := w.grid(*layer.Mask)
			if err != nil {
				return "", err
			}
			stored.Mask = &mask
		}
		state.Layers[i] = stored
	}
	for i, frame := range s.Frames {
		stored := packedFrame{Frame: frame}
		if frame.Cels != nil {
			stored.Cels = make(map[document.ID]gridRef, len(frame.Cels))
		}
		for id, grid := range frame.Cels {
			ref, err := w.grid(grid)
			if err != nil {
				return "", err
			}
			stored.Cels[id] = ref
		}
		state.Frames[i] = stored
	}
	if s.Poster != nil {
		ref, err := w.grid(*s.Poster)
		if err != nil {
			return "", err
		}
		state.Poster = &ref
	}
	data, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	key, err := w.put(data)
	if err == nil {
		w.states[s] = key
	}
	return key, err
}

type metadataReader struct {
	ctx    context.Context
	tx     *sql.Tx
	grids  map[string]map[tile.Coord]tile.Hash
	states map[string]*document.State
}

func readMetadata(ctx context.Context, tx *sql.Tx, data []byte) (document.Snapshot, error) {
	body, packed := bytes.CutPrefix(data, []byte(packedMetadata))
	if !packed {
		var snapshot document.Snapshot
		err := json.Unmarshal(data, &snapshot)
		return snapshot, err
	}
	var root packedSnapshot
	if err := json.Unmarshal(body, &root); err != nil {
		return document.Snapshot{}, err
	}
	if len(root.History) == 0 || len(root.History) > document.UndoLimit+1 || root.Cursor < 0 || root.Cursor >= len(root.History) {
		return document.Snapshot{}, errors.New("invalid packed undo history")
	}
	r := metadataReader{ctx: ctx, tx: tx, grids: make(map[string]map[tile.Coord]tile.Hash), states: make(map[string]*document.State)}
	s := document.Snapshot{Project: root.Project, Version: root.Version, Cursor: root.Cursor, History: make([]document.Checkpoint, len(root.History))}
	for i, entry := range root.History {
		state, err := r.state(entry.State)
		if err != nil {
			return document.Snapshot{}, err
		}
		s.History[i] = document.Checkpoint{Label: entry.Label, State: state}
	}
	return s, nil
}

func (r *metadataReader) get(key string, limit int) ([]byte, error) {
	if len(key) != len(metadataPrefix)+hex.EncodedLen(sha256.Size) || key[:len(metadataPrefix)] != metadataPrefix {
		return nil, errors.New("invalid project metadata reference")
	}
	var data []byte
	if err := r.tx.QueryRowContext(r.ctx, "SELECT data FROM tiles WHERE hash=? AND length(data)<=?", key, limit).Scan(&data); err != nil {
		return nil, fmt.Errorf("project metadata record is missing or too large: %w", err)
	}
	if key != metadataKey(data) {
		return nil, errors.New("project metadata record failed its checksum")
	}
	return data, nil
}

func (r *metadataReader) grid(ref gridRef) (tile.Grid, error) {
	g := tile.Grid{Default: ref.Default}
	if ref.Blocks == "" {
		return g, nil
	}
	if blocks, ok := r.grids[ref.Blocks]; ok {
		g.Blocks = blocks
		return g, nil
	}
	data, err := r.get(ref.Blocks, 128<<10)
	if err != nil {
		return tile.Grid{}, err
	}
	var chunks []string
	if err := json.Unmarshal(data, &chunks); err != nil {
		return tile.Grid{}, err
	}
	if len(chunks) > maxGridChunks {
		return tile.Grid{}, errors.New("too many tile map chunks")
	}
	g.Blocks = make(map[tile.Coord]tile.Hash)
	for _, key := range chunks {
		part, err := r.get(key, refsPerChunk*tileRefSize)
		if err != nil {
			return tile.Grid{}, err
		}
		if len(part) == 0 || len(part)%tileRefSize != 0 {
			return tile.Grid{}, errors.New("invalid tile map chunk")
		}
		for len(part) > 0 {
			c := tile.Coord{X: int(int16(binary.LittleEndian.Uint16(part))), Y: int(int16(binary.LittleEndian.Uint16(part[2:])))}
			if _, exists := g.Blocks[c]; exists || c.X < -1024 || c.X > 1024 || c.Y < -1024 || c.Y > 1024 {
				return tile.Grid{}, errors.New("invalid or duplicate tile coordinate")
			}
			g.Blocks[c] = tile.Hash(part[4:tileRefSize])
			part = part[tileRefSize:]
		}
	}
	r.grids[ref.Blocks] = g.Blocks
	return g, nil
}

func (r *metadataReader) state(key string) (*document.State, error) {
	if s := r.states[key]; s != nil {
		return s, nil
	}
	data, err := r.get(key, maxStateMetadata)
	if err != nil {
		return nil, err
	}
	var stored packedState
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, err
	}
	if len(stored.Layers) > document.MaxLayers || len(stored.Frames) > document.MaxFrames {
		return nil, errors.New("too many layers or frames in project metadata")
	}
	s := stored.State
	if stored.Layers != nil {
		s.Layers = make([]document.Layer, len(stored.Layers))
	}
	s.Frames = make([]document.Frame, len(stored.Frames))
	for i, layer := range stored.Layers {
		l := layer.Layer
		l.Image, err = r.grid(layer.Image)
		if err != nil {
			return nil, err
		}
		if layer.Mask != nil {
			mask, err := r.grid(*layer.Mask)
			if err != nil {
				return nil, err
			}
			l.Mask = &mask
		}
		s.Layers[i] = l
	}
	for i, frame := range stored.Frames {
		f := frame.Frame
		if len(frame.Cels) > len(s.Layers) {
			return nil, errors.New("too many cels in a frame")
		}
		if frame.Cels != nil {
			f.Cels = make(map[document.ID]tile.Grid, len(frame.Cels))
		}
		for id, ref := range frame.Cels {
			g, err := r.grid(ref)
			if err != nil {
				return nil, err
			}
			f.Cels[id] = g
		}
		s.Frames[i] = f
	}
	if stored.Poster != nil {
		g, err := r.grid(*stored.Poster)
		if err != nil {
			return nil, err
		}
		s.Poster = &g
	}
	r.states[key] = &s
	return &s, nil
}
