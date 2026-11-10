package project

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/digimbyte/photobyte/internal/document"
	"github.com/digimbyte/photobyte/internal/tile"
)

type recoveryWrite struct {
	snapshot document.Snapshot
	manual   uint64
	original string
}

type Status struct {
	Version   uint64
	WrittenAt time.Time
	Err       error
}

type Entry struct {
	Path     string
	Info     Info
	Modified time.Time
}

type Journal struct {
	dir      string
	store    *tile.Store
	notify   func()
	mu       sync.Mutex
	queue    map[string]recoveryWrite
	inFlight map[string]recoveryWrite
	status   map[string]Status
	stop     chan struct{}
	done     chan struct{}
	ctx      context.Context
	cancel   context.CancelFunc
}

func NewJournal(dir string, store *tile.Store, notify func()) (*Journal, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	j := &Journal{dir: dir, store: store, notify: notify, queue: make(map[string]recoveryWrite),
		status: make(map[string]Status), stop: make(chan struct{}), done: make(chan struct{}), ctx: ctx, cancel: cancel}
	go j.run()
	return j, nil
}

func (j *Journal) Submit(s document.Snapshot, manual uint64, original string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if pending, ok := j.queue[s.Project]; !ok || pending.snapshot.Version <= s.Version {
		j.queue[s.Project] = recoveryWrite{snapshot: s, manual: manual, original: original}
	}
}

func (j *Journal) Status(id string) Status {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.status[id]
}

// PendingSnapshots includes batches already handed off to the journal worker.
func (j *Journal) PendingSnapshots() []document.Snapshot {
	j.mu.Lock()
	defer j.mu.Unlock()
	pins := make([]document.Snapshot, 0, len(j.queue)+len(j.inFlight))
	for _, batch := range []map[string]recoveryWrite{j.queue, j.inFlight} {
		for _, write := range batch {
			pins = append(pins, write.snapshot)
		}
	}
	return pins
}

func (j *Journal) Close() {
	close(j.stop)
	timer := time.AfterFunc(4*time.Second, j.cancel)
	<-j.done
	timer.Stop()
	j.cancel()
}

func (j *Journal) run() {
	defer close(j.done)
	ticker := time.NewTicker(350 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			j.writePending()
		case <-j.stop:
			j.writePending()
			return
		}
	}
}

func (j *Journal) writePending() {
	j.mu.Lock()
	queue := j.queue
	j.queue = make(map[string]recoveryWrite)
	j.inFlight = queue
	j.mu.Unlock()
	for id, write := range queue {
		err := RecoverTo(j.ctx, filepath.Join(j.dir, id+".dphoto"), j.store, write.snapshot, write.manual, write.original)
		j.mu.Lock()
		status := j.status[id]
		if err == nil {
			status.Version = write.snapshot.Version
			status.WrittenAt = time.Now()
		}
		status.Err = err
		j.status[id] = status
		j.mu.Unlock()
		if j.notify != nil {
			j.notify()
		}
		if err == nil {
			j.store.FlushPending(j.ctx)
		}
	}
	j.mu.Lock()
	j.inFlight = nil
	j.mu.Unlock()
}

func Recoverable(ctx context.Context, dir string) ([]Entry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") || filepath.Ext(entry.Name()) != ".dphoto" {
			continue
		}
		p := filepath.Join(dir, entry.Name())
		info, err := Inspect(ctx, p)
		if err != nil || info.Version <= info.Manual {
			continue
		}
		file, err := entry.Info()
		if err != nil {
			continue
		}
		out = append(out, Entry{Path: p, Info: info, Modified: file.ModTime()})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Modified.After(out[j].Modified) })
	return out, nil
}
