package publish

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

// File stages an output on its destination volume and publishes it only
// after a successful write and flush. Existing files are protected.
func File(ctx context.Context, dest string, write func(*os.File) error) error {
	return stage(ctx, dest, write, New)
}

// ReplaceFile atomically updates a file whose previous contents may be replaced.
func ReplaceFile(ctx context.Context, dest string, write func(*os.File) error) error {
	return stage(ctx, dest, write, replace)
}

func stage(ctx context.Context, dest string, write func(*os.File) error, commit func(string, string) error) error {
	f, err := os.CreateTemp(filepath.Dir(dest), ".photobyte-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	writeErr := write(f)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	if err := errors.Join(writeErr, f.Close(), ctx.Err()); err != nil {
		return err
	}
	return commit(f.Name(), dest)
}
