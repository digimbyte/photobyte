package media

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"digiphoto/internal/document"
	"digiphoto/internal/tile"
)

type Codec interface {
	Import(context.Context, string, *tile.Store, bool) (*document.State, string, error)
	Encode(context.Context, *os.File, *os.File, string, int) error
}

type Broker struct {
	Scratch string
	sandbox codecSandbox
	permit  chan struct{}
}

func NewBroker(executable, scratch string) *Broker {
	return &Broker{Scratch: scratch, sandbox: newCodecSandbox(executable, scratch), permit: make(chan struct{}, 1)}
}

func (b *Broker) Import(ctx context.Context, path string, store *tile.Store, raw bool) (*document.State, string, error) {
	in, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return nil, "", err
	}
	if !info.Mode().IsRegular() || info.Size() > MaxInput {
		return nil, "", errors.New("image is not a regular file within the input limit")
	}
	out, err := os.CreateTemp(b.Scratch, "decoded-*")
	if err != nil {
		return nil, "", err
	}
	defer os.Remove(out.Name())
	defer out.Close()
	if err := b.run(ctx, in, out, "decode", strconv.FormatBool(raw)); err != nil {
		return nil, "", err
	}
	if _, err := out.Seek(0, io.SeekStart); err != nil {
		return nil, "", err
	}
	return ReadDocument(ctx, store, out, raw)
}

func (b *Broker) Encode(ctx context.Context, in, out *os.File, format string, depth int) error {
	return b.run(ctx, in, out, "encode", format, strconv.Itoa(depth))
}

func (b *Broker) run(ctx context.Context, in, out *os.File, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	select {
	case b.permit <- struct{}{}:
		defer func() { <-b.permit }()
	case <-ctx.Done():
		return ctx.Err()
	}
	readOut, writeOut, err := os.Pipe()
	if err != nil {
		return err
	}
	defer readOut.Close()
	defer writeOut.Close()
	readErr, writeErr, err := os.Pipe()
	if err != nil {
		return err
	}
	defer readErr.Close()
	defer writeErr.Close()
	// The editor owns the staged file. The helper can write only to the
	// inherited pipe, which is stopped if it exceeds its output budget.
	procCtx, stop := context.WithCancel(ctx)
	defer stop()
	copyResult := make(chan error, 1)
	go func() {
		err := copyCodecOutput(out, readOut, MaxWire+65536+8)
		if err != nil {
			stop()
		}
		copyResult <- err
	}()
	diagnostic := make(chan string, 1)
	go func() {
		msg, _ := io.ReadAll(io.LimitReader(readErr, 16<<10))
		io.Copy(io.Discard, readErr)
		diagnostic <- string(bytes.TrimSpace(msg))
	}()
	err = b.sandbox.run(procCtx, [3]*os.File{in, writeOut, writeErr}, args)
	writeOut.Close()
	writeErr.Close()
	copyErr, message := <-copyResult, <-diagnostic
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case copyErr != nil:
		return fmt.Errorf("stage codec output: %w", copyErr)
	case err != nil && message != "":
		return fmt.Errorf("image codec: %s", message)
	case err != nil:
		return fmt.Errorf("image codec stopped: %w", err)
	}
	return nil
}

func copyCodecOutput(out io.Writer, in io.Reader, limit int64) error {
	n, err := io.Copy(out, io.LimitReader(in, limit))
	if err != nil || n < limit {
		return err
	}
	var extra [1]byte
	if _, err := io.ReadFull(in, extra[:]); !errors.Is(err, io.EOF) {
		if err != nil {
			return err
		}
		return errors.New("codec output exceeded its byte limit")
	}
	return nil
}

// HelperMain reads and writes inherited handles. It accepts only a mode and
// format options, with filenames resolved by the editor process.
func HelperMain(args []string) error {
	args, err := enterRestriction(args)
	if err != nil {
		return err
	}
	switch {
	case len(args) == 2 && args[0] == "decode":
		raw, err := strconv.ParseBool(args[1])
		if err != nil {
			return err
		}
		return Decode(os.Stdin, os.Stdout, raw)
	case len(args) == 3 && args[0] == "encode":
		depth, err := strconv.Atoi(args[2])
		if err != nil {
			return err
		}
		return Encode(os.Stdin, os.Stdout, args[1], depth)
	default:
		return errors.New("invalid image helper request")
	}
}
