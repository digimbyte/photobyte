//go:build windows && photobyte_win

package desktop

import "gioui.org/app"

func installCloseHandler(handler func()) { app.PhotoByteSetCloseHandler(handler) }
