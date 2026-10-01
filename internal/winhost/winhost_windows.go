// Package winhost embeds one top-level window inside another as a Win32
// child. The app uses it to place the YouTube Music and settings webviews
// below the custom title bar, the way Electron's BrowserView did, so the
// site's own layout is never modified.
package winhost

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32            = windows.NewLazySystemDLL("user32.dll")
	procSetParent     = user32.NewProc("SetParent")
	procGetWindowLong = user32.NewProc("GetWindowLongPtrW")
	procSetWindowLong = user32.NewProc("SetWindowLongPtrW")
	procSetWindowPos  = user32.NewProc("SetWindowPos")
	procGetClientRect = user32.NewProc("GetClientRect")
	procGetDpiForWin  = user32.NewProc("GetDpiForWindow")
)

const (
	gwlStyle   = ^uintptr(15) // GWL_STYLE (-16)
	gwlExStyle = ^uintptr(19) // GWL_EXSTYLE (-20)

	wsChild        = 0x40000000
	wsPopup        = 0x80000000
	wsCaption      = 0x00C00000
	wsThickFrame   = 0x00040000
	wsSysMenu      = 0x00080000
	wsMinMax       = 0x00030000
	wsClipSiblings = 0x04000000
	wsExAppWindow  = 0x00040000
	wsExToolWindow = 0x00000080

	swpNoZOrder     = 0x0004
	swpNoActivate   = 0x0010
	swpFrameChanged = 0x0020
	swpShowWindow   = 0x0040
	swpNoMove       = 0x0002
	swpNoSize       = 0x0001
)

type rect struct{ Left, Top, Right, Bottom int32 }

// Attach turns child into a borderless WS_CHILD window of parent.
func Attach(child, parent uintptr) {
	style, _, _ := procGetWindowLong.Call(child, gwlStyle)
	style = (style &^ (wsPopup | wsCaption | wsThickFrame | wsSysMenu | wsMinMax)) | wsChild | wsClipSiblings
	procSetWindowLong.Call(child, gwlStyle, style)
	ex, _, _ := procGetWindowLong.Call(child, gwlExStyle)
	procSetWindowLong.Call(child, gwlExStyle, ex&^(wsExAppWindow|wsExToolWindow))
	procSetParent.Call(child, parent)
	procSetWindowPos.Call(child, 0, 0, 0, 0, 0, swpNoMove|swpNoSize|swpNoZOrder|swpNoActivate|swpFrameChanged)
}

// Scale converts a 96-DPI length to the parent's physical pixels.
func Scale(hwnd uintptr, length int) int {
	dpi, _, _ := procGetDpiForWin.Call(hwnd)
	if dpi == 0 {
		dpi = 96
	}
	return length * int(dpi) / 96
}

// FillBelow sizes child to the parent's client area minus a top band of
// topDIP device-independent pixels.
func FillBelow(child, parent uintptr, topDIP int) {
	var r rect
	procGetClientRect.Call(parent, uintptr(unsafe.Pointer(&r)))
	top := Scale(parent, topDIP)
	height := int(r.Bottom) - top
	if height < 0 {
		height = 0
	}
	procSetWindowPos.Call(child, 0, 0, uintptr(top), uintptr(r.Right), uintptr(height), swpNoZOrder|swpNoActivate)
}

// Raise brings child to the top of its siblings and shows it.
func Raise(child uintptr) {
	procSetWindowPos.Call(child, 0, 0, 0, 0, 0, swpNoMove|swpNoSize|swpNoActivate|swpShowWindow)
}
