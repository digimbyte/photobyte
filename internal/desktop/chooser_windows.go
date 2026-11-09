package desktop

import (
	"fmt"
	"runtime"
	"unsafe"

	"gioui.org/app"
	"golang.org/x/sys/windows"
)

func windowHandle(e any) uintptr {
	if e, ok := e.(app.Win32ViewEvent); ok {
		return uintptr(e.HWND)
	}
	return 0
}

type fileDialog struct {
	size         uint32
	owner        uintptr
	instance     uintptr
	filter       *uint16
	custom       *uint16
	customLength uint32
	filterIndex  uint32
	file         *uint16
	fileLength   uint32
	fileTitle    *uint16
	titleLength  uint32
	initialDir   *uint16
	title        *uint16
	flags        uint32
	fileOffset   uint16
	extOffset    uint16
	defaultExt   *uint16
	customData   uintptr
	hook         uintptr
	template     *uint16
	reserved     uintptr
	reservedSize uint32
	flagsEx      uint32
}

func choosePath(owner uintptr, mode string) (string, error) {
	const (
		noChangeDir   = 0x00000008
		pathMustExist = 0x00000800
		fileMustExist = 0x00001000
		explorer      = 0x00080000
	)
	dll := windows.NewLazySystemDLL("comdlg32.dll")
	funcName := "GetOpenFileNameW"
	filters := "Supported files\x00*.dphoto;*.png;*.apng;*.gif;*.jpg;*.jpeg;*.webp\x00All files\x00*.*\x00\x00"
	flags := uint32(noChangeDir | pathMustExist | fileMustExist | explorer)
	var ext *uint16
	if mode == "image" {
		filters = "Supported images\x00*.png;*.apng;*.gif;*.jpg;*.jpeg;*.webp\x00All files\x00*.*\x00\x00"
	} else if mode != "open" {
		funcName = "GetSaveFileNameW"
		flags = noChangeDir | pathMustExist | explorer
		if mode == "save" {
			mode = "dphoto"
		}
		filters = fmt.Sprintf("%s files\x00*.%s\x00\x00", mode, mode)
		ext, _ = windows.UTF16PtrFromString(mode)
	}
	filter := make([]uint16, len(filters))
	for i, c := range []byte(filters) {
		filter[i] = uint16(c)
	}
	file := make([]uint16, 32768)
	offer := fileDialog{owner: owner, file: &file[0], fileLength: uint32(len(file)),
		filter: &filter[0], filterIndex: 1, flags: flags, defaultExt: ext}
	offer.size = uint32(unsafe.Sizeof(offer))
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	r, _, _ := dll.NewProc(funcName).Call(uintptr(unsafe.Pointer(&offer)))
	runtime.KeepAlive(&offer)
	if r == 0 {
		detail, _, _ := dll.NewProc("CommDlgExtendedError").Call()
		if detail != 0 {
			return "", fmt.Errorf("file picker failed (0x%x)", detail)
		}
		return "", nil
	}
	return windows.UTF16ToString(file), nil
}
