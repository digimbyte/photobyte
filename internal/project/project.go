// Package project persists complete, versioned revisions and their tiles.
package project

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/digimbyte/photobyte/internal/document"
	"github.com/digimbyte/photobyte/internal/publish"
	"github.com/digimbyte/photobyte/internal/tile"

	_ "modernc.org/sqlite"
)

const (
	appID       = 0x44504854 // DPHT
	schema      = 1
	maxMetadata = 32 << 20
)

var ErrConflict = errors.New("the project on disk changed; use Save Copy to keep your edits")

type Info struct {
	Project      string
	Version      uint64
	Manual       uint64
	OriginalPath string
}

type options struct {
	info     Info
	previous uint64
	recovery bool
}

func Save(ctx context.Context, path string, store *tile.Store, snapshot document.Snapshot, previous uint64) error {
	opts := options{info: Info{Project: snapshot.Project, Version: snapshot.Version, Manual: snapshot.Version}, previous: previous}
	if previous == 0 {
		return create(ctx, path, store, snapshot, opts)
	}
	return write(ctx, path, store, snapshot, opts, false)
}

func RecoverTo(ctx context.Context, path string, store *tile.Store, snapshot document.Snapshot, manual uint64, originalPath string) error {
	opts := options{info: Info{Project: snapshot.Project, Version: snapshot.Version, Manual: manual, OriginalPath: originalPath}, recovery: true}
	_, err := os.Stat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return create(ctx, path, store, snapshot, opts)
	case err != nil:
		return err
	default:
		return write(ctx, path, store, snapshot, opts, false)
	}
}

func open(path, mode string) (*sql.DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	p := filepath.ToSlash(abs)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	q := url.Values{"mode": {mode}, "_pragma": {"busy_timeout(2500)", "trusted_schema(OFF)"}}
	u := url.URL{Scheme: "file", Path: p, RawQuery: q.Encode()}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

func validateSchema(ctx context.Context, db *sql.DB) error {
	var id, version int
	if err := db.QueryRowContext(ctx, "PRAGMA application_id").Scan(&id); err != nil {
		return err
	}
	if id != appID {
		return errors.New("file is not a PhotoByte project")
	}
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version != schema {
		return fmt.Errorf("unsupported PhotoByte project version %d", version)
	}
	return nil
}

func check(ctx context.Context, db *sql.DB) error {
	var result string
	if err := db.QueryRowContext(ctx, "PRAGMA quick_check(1)").Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return errors.New("project database failed its integrity check")
	}
	return nil
}

func create(ctx context.Context, path string, store *tile.Store, snapshot document.Snapshot, opts options) error {
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return os.ErrExist
		}
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".photobyte-*.dphoto")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer os.Remove(f.Name() + "-journal")
	if err := f.Close(); err != nil {
		return err
	}
	if err := write(ctx, f.Name(), store, snapshot, opts, true); err != nil {
		return err
	}
	f, err = os.OpenFile(f.Name(), os.O_RDWR, 0)
	if err != nil {
		return err
	}
	if err := errors.Join(f.Sync(), f.Close(), ctx.Err()); err != nil {
		return err
	}
	return publish.New(f.Name(), path)
}

const ddl = `
	CREATE TABLE project (
		slot INTEGER PRIMARY KEY CHECK (slot = 1),
		id TEXT NOT NULL,
		version INTEGER NOT NULL,
		manual INTEGER NOT NULL,
		original TEXT NOT NULL,
		snapshot BLOB NOT NULL
	);
	CREATE TABLE tiles (
		hash TEXT PRIMARY KEY,
		data BLOB NOT NULL
	) WITHOUT ROWID;
`

func write(ctx context.Context, path string, store *tile.Store, snapshot document.Snapshot, opts options, fresh bool) error {
	if err := snapshot.Validate(); err != nil {
		return err
	}
	db, err := open(path, "rw")
	if err != nil {
		return err
	}
	defer db.Close()
	if !fresh {
		if err := validateSchema(ctx, db); err != nil {
			return err
		}
	}
	if _, err := db.ExecContext(ctx, "PRAGMA journal_mode=DELETE; PRAGMA synchronous=EXTRA"); err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if fresh {
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA application_id=%d; PRAGMA user_version=%d; ", appID, schema)+ddl); err != nil {
			return err
		}
	} else {
		var current Info
		if err := tx.QueryRowContext(ctx, "SELECT id, version, manual FROM project WHERE slot=1").
			Scan(&current.Project, &current.Version, &current.Manual); err != nil {
			return err
		}
		if current.Project != snapshot.Project || (!opts.recovery && current.Version != opts.previous) {
			return ErrConflict
		}
		if opts.recovery {
			if current.Version > snapshot.Version {
				return nil
			}
			opts.info.Manual = max(opts.info.Manual, current.Manual)
		}
	}
	if _, err := tx.ExecContext(ctx, "CREATE TEMP TABLE live_tiles(hash TEXT PRIMARY KEY) WITHOUT ROWID"); err != nil {
		return err
	}
	meta, hashes, err := writeMetadata(ctx, tx, snapshot)
	if err != nil {
		return err
	}
	for hash := range hashes {
		if err := ctx.Err(); err != nil {
			return err
		}
		key := hash.String()
		if _, err := tx.ExecContext(ctx, "INSERT INTO live_tiles(hash) VALUES(?)", key); err != nil {
			return err
		}
		var present bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM tiles WHERE hash=?)", key).Scan(&present); err != nil {
			return err
		}
		if present {
			continue
		}
		data, err := store.Compressed(hash)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO tiles(hash, data) VALUES(?,?)", key, data); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM tiles WHERE hash NOT IN (SELECT hash FROM live_tiles)"); err != nil {
		return err
	}
	info := opts.info
	if _, err := tx.ExecContext(ctx, `INSERT INTO project(slot,id,version,manual,original,snapshot)
		VALUES(1,?,?,?,?,?)
		ON CONFLICT(slot) DO UPDATE SET version=excluded.version, manual=excluded.manual,
		original=excluded.original, snapshot=excluded.snapshot`,
		info.Project, info.Version, info.Manual, info.OriginalPath, meta); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if fresh {
		return check(ctx, db)
	}
	return nil
}

func references(snapshot document.Snapshot) (map[tile.Hash]struct{}, error) {
	hashes := make(map[tile.Hash]struct{})
	for _, entry := range snapshot.History {
		err := entry.State.VisitGrids(func(g tile.Grid) error {
			for _, hash := range g.Blocks {
				hashes[hash] = struct{}{}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return hashes, nil
}

func Inspect(ctx context.Context, path string) (Info, error) {
	db, err := open(path, "ro")
	if err != nil {
		return Info{}, err
	}
	defer db.Close()
	if err := validateSchema(ctx, db); err != nil {
		return Info{}, err
	}
	var info Info
	err = db.QueryRowContext(ctx, "SELECT id,version,manual,original FROM project WHERE slot=1").
		Scan(&info.Project, &info.Version, &info.Manual, &info.OriginalPath)
	return info, err
}

func Load(ctx context.Context, path string, store *tile.Store) (document.Snapshot, Info, error) {
	db, err := open(path, "ro")
	if err != nil {
		return document.Snapshot{}, Info{}, err
	}
	defer db.Close()
	if err := validateSchema(ctx, db); err != nil {
		return document.Snapshot{}, Info{}, err
	}
	if err := check(ctx, db); err != nil {
		return document.Snapshot{}, Info{}, err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return document.Snapshot{}, Info{}, err
	}
	defer tx.Rollback()
	var info Info
	var meta []byte
	if err := tx.QueryRowContext(ctx, `SELECT id,version,manual,original,snapshot FROM project
		WHERE slot=1 AND length(snapshot)<=?`, maxMetadata).
		Scan(&info.Project, &info.Version, &info.Manual, &info.OriginalPath, &meta); err != nil {
		return document.Snapshot{}, Info{}, err
	}
	snapshot, err := readMetadata(ctx, tx, meta)
	if err != nil {
		return document.Snapshot{}, Info{}, err
	}
	if err := snapshot.Validate(); err != nil {
		return document.Snapshot{}, Info{}, err
	}
	if info.Project != snapshot.Project || info.Version != snapshot.Version || info.Manual > info.Version {
		return document.Snapshot{}, Info{}, errors.New("project revision metadata is inconsistent")
	}
	hashes, err := references(snapshot)
	if err != nil {
		return document.Snapshot{}, Info{}, err
	}
	for hash := range hashes {
		var data []byte
		if err := tx.QueryRowContext(ctx, "SELECT data FROM tiles WHERE hash=? AND length(data)<=?", hash.String(), tile.MaxBlob).
			Scan(&data); err != nil {
			return document.Snapshot{}, Info{}, fmt.Errorf("tile %s is missing or too large: %w", hash, err)
		}
		if err := store.Import(hash, data); err != nil {
			return document.Snapshot{}, Info{}, err
		}
	}
	return snapshot, info, tx.Commit()
}
