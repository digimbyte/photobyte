//go:build !windows

package publish

import (
	"errors"
	"os"
)

func replace(source, dest string) error { return os.Rename(source, dest) }

func New(source, dest string) error {
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return os.Link(source, dest)
	}
	if _, err := os.Lstat(dest); !errors.Is(err, os.ErrNotExist) {
		if err != nil {
			return err
		}
		return os.ErrExist
	}
	return os.Rename(source, dest)
}
