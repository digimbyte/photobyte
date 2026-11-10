package desktop

import "golang.org/x/sys/windows"

func setWindowIcon(hwnd uintptr) {
	kernel := windows.NewLazySystemDLL("kernel32.dll")
	user := windows.NewLazySystemDLL("user32.dll")
	module, _, _ := kernel.NewProc("GetModuleHandleW").Call(0)
	dpi, _, _ := user.NewProc("GetDpiForWindow").Call(hwnd)
	if dpi == 0 {
		dpi = 96
	}
	load := user.NewProc("LoadImageW")
	post := user.NewProc("PostMessageW")
	const iconResource, imageIcon, shared, setIcon = 1, 1, 0x8000, 0x0080
	for kind, logical := range []uintptr{16, 32} {
		size := (logical*dpi + 48) / 96
		handle, _, _ := load.Call(module, iconResource, imageIcon, size, size, shared)
		if handle != 0 {
			post.Call(hwnd, setIcon, uintptr(kind), handle)
		}
	}
}
