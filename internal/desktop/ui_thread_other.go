//go:build !windows

package desktop

func prioritizeLayoutThread() func() { return func() {} }
