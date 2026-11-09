package document

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

var (
	ErrStale  = errors.New("the document changed while the edit was being prepared")
	ErrClosed = errors.New("document is closed")
)

type Command interface {
	change(*State) error
	label() string
}

type request struct {
	ctx     context.Context
	base    uint64
	command Command
	jump    int
	history bool
	reply   chan error
}

// Editor has one writer. Every published Snapshot and all its state are
// read-only to callers and can be pinned by background jobs without locking.
type Editor struct {
	current atomic.Pointer[Snapshot]
	queue   chan request
	stop    chan struct{}
	done    chan struct{}
	once    sync.Once
}

func Open(s Snapshot) *Editor {
	e := &Editor{queue: make(chan request, 16), stop: make(chan struct{}), done: make(chan struct{})}
	e.current.Store(&s)
	go e.run()
	return e
}

func (e *Editor) Snapshot() Snapshot { return *e.current.Load() }

func (e *Editor) Apply(ctx context.Context, base uint64, command Command) error {
	return e.send(request{ctx: ctx, base: base, command: command})
}

func (e *Editor) History(ctx context.Context, base uint64, cursor int) error {
	return e.send(request{ctx: ctx, base: base, jump: cursor, history: true})
}

func (e *Editor) send(r request) error {
	r.reply = make(chan error, 1)
	select {
	case <-e.done:
		return ErrClosed
	case <-r.ctx.Done():
		return r.ctx.Err()
	case e.queue <- r:
	}
	select {
	case err := <-r.reply:
		return err
	case <-e.done:
		select {
		case err := <-r.reply:
			return err
		default:
			return ErrClosed
		}
	}
}

func (e *Editor) run() {
	defer close(e.done)
	for {
		select {
		case <-e.stop:
			return
		case r := <-e.queue:
			r.reply <- e.apply(r)
		}
	}
}

func (e *Editor) apply(r request) error {
	if err := r.ctx.Err(); err != nil {
		return err
	}
	current := e.Snapshot()
	if r.base != current.Version {
		return ErrStale
	}
	if r.history {
		if r.jump < 0 || r.jump >= len(current.History) || r.jump == current.Cursor {
			return nil
		}
		current.Cursor = r.jump
	} else {
		if r.command == nil {
			return errors.New("missing edit command")
		}
		state := current.State().clone()
		if err := r.command.change(state); err != nil {
			return err
		}
		if err := state.Validate(); err != nil {
			return err
		}
		start := max(0, current.Cursor-UndoLimit+1)
		current.History = append(slices.Clone(current.History[start:current.Cursor+1]), Checkpoint{
			Label: r.command.label(), State: state,
		})
		current.Cursor = len(current.History) - 1
	}
	current.Version++
	current.ChangedAt = time.Now()
	e.current.Store(&current)
	return nil
}

func (e *Editor) Close() {
	e.once.Do(func() { close(e.stop) })
	<-e.done
}
