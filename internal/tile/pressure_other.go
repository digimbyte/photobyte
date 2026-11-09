//go:build !windows

package tile

import (
	"errors"
	"os"
	"syscall"
)

func isStoragePressure(err error) bool {
	return errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT)
}
