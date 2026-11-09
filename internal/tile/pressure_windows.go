package tile

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func isStoragePressure(err error) bool {
	return errors.Is(err, os.ErrPermission) || errors.Is(err, windows.ERROR_DISK_FULL) ||
		errors.Is(err, windows.ERROR_HANDLE_DISK_FULL) || errors.Is(err, windows.ERROR_DISK_QUOTA_EXCEEDED)
}
