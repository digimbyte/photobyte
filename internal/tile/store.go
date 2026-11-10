// Package tile stores immutable, straight-alpha 16-bit raster tiles.
package tile

import (
	"bytes"
	"compress/zlib"
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
	"maps"
	"os"
	"path/filepath"
	"sync"
)

const (
	Size         = 256
	RawBytes     = Size * Size * 8
	MaxBlob      = RawBytes + 4096
	pendingLimit = 64 << 20
)

var ErrPendingFull = errors.New("pending edit storage is full; free disk space or save a copy before editing")

type Pixel = color.NRGBA64
type Block [Size * Size]Pixel
type Hash [32]byte

func Straight(c color.Color) Pixel {
	if p, ok := c.(color.NRGBA); ok {
		return Pixel{R: uint16(p.R) * 257, G: uint16(p.G) * 257, B: uint16(p.B) * 257, A: uint16(p.A) * 257}
	}
	return color.NRGBA64Model.Convert(c).(Pixel)
}

func (h Hash) String() string { return hex.EncodeToString(h[:]) }

func (h Hash) MarshalText() ([]byte, error) { return []byte(h.String()), nil }

func (h *Hash) UnmarshalText(text []byte) error {
	if len(text) != hex.EncodedLen(len(h)) {
		return errors.New("invalid tile hash length")
	}
	_, err := hex.Decode(h[:], text)
	return err
}

type Coord struct{ X, Y int }

func (c Coord) MarshalText() ([]byte, error) {
	return fmt.Appendf(nil, "%d,%d", c.X, c.Y), nil
}

func (c *Coord) UnmarshalText(text []byte) error {
	_, err := fmt.Sscanf(string(text), "%d,%d", &c.X, &c.Y)
	if err == nil && string(text) != fmt.Sprintf("%d,%d", c.X, c.Y) {
		return errors.New("invalid tile coordinate")
	}
	return err
}

func At(x, y int) Coord { return Coord{x >> 8, y >> 8} }

func (c Coord) Rect() image.Rectangle {
	return image.Rect(c.X*Size, c.Y*Size, (c.X+1)*Size, (c.Y+1)*Size)
}

func Index(x, y int) int { return (y&(Size-1))*Size + (x & (Size - 1)) }

// Grid maps document-space tile coordinates to content. Unallocated pixels
// have the grid's Default value, including white in a newly created mask.
type Grid struct {
	Blocks  map[Coord]Hash
	Default Pixel
}

func (g Grid) Clone() Grid {
	g.Blocks = maps.Clone(g.Blocks)
	if g.Blocks == nil {
		g.Blocks = make(map[Coord]Hash)
	}
	return g
}

func (g Grid) Validate() error {
	for c, hash := range g.Blocks {
		if c.X < -1024 || c.X > 1024 || c.Y < -1024 || c.Y > 1024 || hash == (Hash{}) {
			return errors.New("invalid raster tile reference")
		}
	}
	return nil
}

type cacheEntry struct {
	hash  Hash
	block *Block
}

// Store holds compressed source tiles on disk. Only immutable decoded blocks
// in the LRU and those currently referenced by workers occupy decoded memory.
type Store struct {
	dir   string
	limit int
	mu    sync.Mutex
	cache map[Hash]*list.Element
	lru   list.List
	spool tileSpool
}

func NewStore(dir string, cacheBytes int) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Store{dir: dir, limit: max(0, cacheBytes/RawBytes), cache: make(map[Hash]*list.Element)}
	if err := s.initSpool(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) path(hash Hash) string { return filepath.Join(s.dir, hash.String()) }

func (s *Store) Get(hash Hash) (*Block, error) {
	s.mu.Lock()
	if entry := s.cache[hash]; entry != nil {
		s.lru.MoveToFront(entry)
		block := entry.Value.(cacheEntry).block
		s.mu.Unlock()
		return block, nil
	}
	s.mu.Unlock()
	data, err := s.Compressed(hash)
	if err != nil {
		return nil, err
	}
	block, err := decode(hash, data)
	if err != nil {
		return nil, err
	}
	return s.remember(hash, block), nil
}

func (s *Store) remember(hash Hash, block *Block) *Block {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry := s.cache[hash]; entry != nil {
		s.lru.MoveToFront(entry)
		return entry.Value.(cacheEntry).block
	}
	if s.limit > 0 {
		s.cache[hash] = s.lru.PushFront(cacheEntry{hash: hash, block: block})
		if s.lru.Len() > s.limit {
			old := s.lru.Back()
			delete(s.cache, old.Value.(cacheEntry).hash)
			s.lru.Remove(old)
		}
	}
	return block
}

func (s *Store) Compressed(hash Hash) ([]byte, error) {
	s.spool.mu.Lock()
	pending := s.spool.pending[hash].data
	s.spool.mu.Unlock()
	if pending != nil {
		return pending, nil
	}
	f, err := os.Open(s.path(hash))
	if err != nil {
		return nil, fmt.Errorf("read tile %s: %w", hash, err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxBlob+1))
	if len(data) > MaxBlob {
		return nil, errors.New("tile data exceeds its size limit")
	}
	return data, err
}

func (s *Store) Put(block *Block) (Hash, error) {
	raw := make([]byte, RawBytes)
	for i, pixel := range block {
		out := raw[i*8 : i*8+8]
		binary.BigEndian.PutUint16(out, pixel.R)
		binary.BigEndian.PutUint16(out[2:], pixel.G)
		binary.BigEndian.PutUint16(out[4:], pixel.B)
		binary.BigEndian.PutUint16(out[6:], pixel.A)
	}
	hash := sha256.Sum256(raw)
	if s.touch(hash) {
		return hash, nil
	}
	var buf bytes.Buffer
	w, err := zlib.NewWriterLevel(&buf, zlib.BestSpeed)
	if err != nil {
		return Hash{}, err
	}
	if _, err := w.Write(raw); err != nil {
		return Hash{}, err
	}
	if err := w.Close(); err != nil {
		return Hash{}, err
	}
	if err := s.write(hash, buf.Bytes()); err != nil {
		return Hash{}, err
	}
	return hash, nil
}

func (s *Store) Import(hash Hash, data []byte) error {
	if _, err := decode(hash, data); err != nil {
		return err
	}
	return s.write(hash, data)
}

func (s *Store) writeDisk(hash Hash, data []byte) error {
	f, err := os.CreateTemp(s.dir, "tile-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, writeErr := f.Write(data)
	if err := errors.Join(writeErr, f.Close()); err != nil {
		return err
	}
	// The spool is disposable. A project's SQLite transaction makes the
	// compressed tile durable when it is journaled or saved.
	if err := os.Rename(f.Name(), s.path(hash)); err != nil {
		if _, statErr := os.Stat(s.path(hash)); statErr != nil {
			return err
		}
	}
	return nil
}

func decode(hash Hash, data []byte) (*Block, error) {
	if len(data) > MaxBlob {
		return nil, errors.New("tile data exceeds its size limit")
	}
	r, err := zlib.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	raw, err := io.ReadAll(io.LimitReader(r, RawBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) != RawBytes || sha256.Sum256(raw) != hash {
		return nil, fmt.Errorf("invalid data for tile %s", hash)
	}
	block := new(Block)
	for i := range block {
		in := raw[i*8 : i*8+8]
		block[i] = Pixel{
			R: binary.BigEndian.Uint16(in), G: binary.BigEndian.Uint16(in[2:]),
			B: binary.BigEndian.Uint16(in[4:]), A: binary.BigEndian.Uint16(in[6:]),
		}
	}
	return block, nil
}

func (s *Store) FromImage(ctx context.Context, src image.Image) (Grid, error) {
	grid := Grid{Blocks: make(map[Coord]Hash)}
	size := src.Bounds().Size()
	for y := 0; y < size.Y; y += Size {
		for x := 0; x < size.X; x += Size {
			if err := ctx.Err(); err != nil {
				return Grid{}, err
			}
			block := new(Block)
			used := false
			for dy := range min(Size, size.Y-y) {
				for dx := range min(Size, size.X-x) {
					p := Straight(src.At(x+dx+src.Bounds().Min.X, y+dy+src.Bounds().Min.Y))
					block[dy*Size+dx] = p
					used = used || p != (Pixel{})
				}
			}
			if used {
				hash, err := s.Put(block)
				if err != nil {
					return Grid{}, err
				}
				grid.Blocks[At(x, y)] = hash
			}
		}
	}
	return grid, nil
}
