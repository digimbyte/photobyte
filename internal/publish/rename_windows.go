package publish

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// New performs a same-volume move without replacing the destination.
func New(source, dest string) error {
	return move(source, dest, windows.MOVEFILE_WRITE_THROUGH)
}

func replace(source, dest string) error {
	return move(source, dest, windows.MOVEFILE_WRITE_THROUGH|windows.MOVEFILE_REPLACE_EXISTING)
}

func move(source, dest string, flags uint32) error {
	from, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(dest)
	if err != nil {
		return err
	}
	if err := windows.MoveFileEx(from, to, flags); err != nil {
		return fmt.Errorf("publish %s: %w", dest, err)
	}
	return nil
}
