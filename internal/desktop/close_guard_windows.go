//go:build windows && digiphoto_win

package desktop

import "gioui.org/app"

func installCloseHandler(handler func()) { app.DigiPhotoSetCloseHandler(handler) }
