//go:build !windows

package desktop

import "errors"

func windowHandle(any) uintptr { return 0 }

func choosePath(uintptr, string) (string, error) {
	return "", errors.New("enter the file path in the dialog")
}
