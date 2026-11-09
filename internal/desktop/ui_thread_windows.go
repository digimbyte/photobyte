package desktop

import (
	"runtime"

	"golang.org/x/sys/windows"
)

func prioritizeLayoutThread() func() {
	runtime.LockOSThread()
	kernel := windows.NewLazySystemDLL("kernel32.dll")
	thread := uintptr(windows.CurrentThread())
	getPriority := kernel.NewProc("GetThreadPriority")
	setPriority := kernel.NewProc("SetThreadPriority")
	previous, _, _ := getPriority.Call(thread)
	const priorityError = 0x7fffffff
	const aboveNormal = 1
	if int32(previous) == priorityError {
		return runtime.UnlockOSThread
	}
	setPriority.Call(thread, aboveNormal)
	return func() {
		setPriority.Call(thread, previous)
		runtime.UnlockOSThread()
	}
}
