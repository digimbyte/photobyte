package media_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"digiphoto/internal/media"
)

type sandboxTargets struct {
	File, Address string
}

func init() {
	codecProbe = func(mode string) error {
		if err := media.CheckCodecIsolation(); err != nil {
			return err
		}
		if mode == "test-hold" {
			fmt.Fprintln(os.Stdout, "started")
			time.Sleep(time.Minute)
			return nil
		}
		var target sandboxTargets
		if err := json.NewDecoder(os.Stdin).Decode(&target); err != nil {
			return err
		}
		if _, err := os.ReadFile(target.File); !errors.Is(err, os.ErrPermission) {
			return fmt.Errorf("private file read should be denied, got %v", err)
		}
		if err := os.WriteFile(target.File, []byte("changed"), 0o600); !errors.Is(err, os.ErrPermission) {
			return fmt.Errorf("private file write should be denied, got %v", err)
		}
		c, err := net.DialTimeout("tcp4", target.Address, 500*time.Millisecond)
		if err == nil {
			c.Close()
			return errors.New("network access was allowed in the codec AppContainer")
		}
		fmt.Fprintln(os.Stdout, "private file and network access denied")
		return nil
	}
}

func TestCodecAppContainerIsolation(t *testing.T) {
	// Prove that the endpoint really is reachable for the ordinary editor.
	// The helper must be denied access to the same listener and private file.
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	c, err := net.DialTimeout("tcp4", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatalf("editor could not reach the control endpoint: %v", err)
	}
	c.Close()
	dir := t.TempDir()
	private := filepath.Join(dir, "private.txt")
	if err := os.WriteFile(private, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	b := media.NewBroker(exe, dir)
	input := filepath.Join(dir, "probe.json")
	target, err := json.Marshal(sandboxTargets{File: private, Address: listener.Addr().String()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(input, target, 0o600); err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(input)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.CreateTemp(dir, "probe-output-*")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	if err := media.RunCodecProbe(t.Context(), b, in, out, "test-restrictions"); err != nil {
		t.Fatal(err)
	}
	message, err := os.ReadFile(out.Name())
	if err != nil || string(message) != "private file and network access denied\n" {
		t.Fatalf("probe did not reach the allowed output pipe: %q, %v", message, err)
	}
	original, err := os.ReadFile(private)
	if err != nil || string(original) != "untouched" {
		t.Fatalf("the editor's private file changed: %q, %v", original, err)
	}
}

func TestCodecCancellationStopsIsolatedProcess(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	b := media.NewBroker(exe, dir)
	in, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.CreateTemp(dir, "cancel-output-*")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- media.RunCodecProbe(ctx, b, in, out, "test-hold") }()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	start := time.After(15 * time.Second)
	for {
		info, err := out.Stat()
		if err != nil {
			t.Fatal(err)
		}
		if info.Size() > 0 {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("helper stopped before the test could cancel it: %v", err)
		case <-start:
			t.Fatal("sandboxed helper did not start")
		case <-tick.C:
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel returned %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("sandboxed helper did not terminate promptly")
	}
}
