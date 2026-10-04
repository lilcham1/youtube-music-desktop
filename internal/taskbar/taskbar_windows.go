// Package taskbar adds Previous / Play-Pause / Next buttons to the window's
// taskbar thumbnail preview (ITaskbarList3 thumbnail toolbar).
package taskbar

import (
	"errors"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32                      = windows.NewLazySystemDLL("user32.dll")
	ole32                       = windows.NewLazySystemDLL("ole32.dll")
	procRegisterWindowMessage   = user32.NewProc("RegisterWindowMessageW")
	procGetDpiForWindow         = user32.NewProc("GetDpiForWindow")
	procGetSystemMetricsForDpi  = user32.NewProc("GetSystemMetricsForDpi")
	procCoCreateInstance        = ole32.NewProc("CoCreateInstance")
	clsidTaskbarList            = windows.GUID{Data1: 0x56FDF344, Data2: 0xFD6D, Data3: 0x11d0, Data4: [8]byte{0x95, 0x8A, 0x00, 0x60, 0x97, 0xC9, 0xA0, 0x90}}
	iidTaskbarList3             = windows.GUID{Data1: 0xEA1AFB91, Data2: 0x9E28, Data3: 0x4B86, Data4: [8]byte{0x90, 0xE9, 0x9E, 0x9F, 0x8A, 0x5E, 0xEF, 0xAF}}
	taskbarButtonCreatedMessage uint32
)

const (
	clsctxInprocServer = 0x1
	wmCommand          = 0x0111
	thbnClicked        = 0x1800
	thbIcon            = 0x2
	thbTooltip         = 0x4
	thbFlags           = 0x8
	smCxSmIcon         = 49

	// ITaskbarList3 vtable slots (IUnknown 0–2, ITaskbarList 3–7,
	// ITaskbarList2 8, ITaskbarList3 9–).
	vtRelease            = 2
	vtHrInit             = 3
	vtThumbBarAddButtons = 15
	vtThumbBarUpdateBtns = 16
	baseButtonID         = 0x4D00
	ButtonPrevious       = baseButtonID + 0
	ButtonPlayPause      = baseButtonID + 1
	ButtonNext           = baseButtonID + 2
)

// ButtonCreatedMessage is the registered message Windows sends once the
// window's taskbar button exists; buttons can only be added after it.
func ButtonCreatedMessage() uint32 {
	if taskbarButtonCreatedMessage == 0 {
		name, _ := windows.UTF16PtrFromString("TaskbarButtonCreated")
		r, _, _ := procRegisterWindowMessage.Call(uintptr(unsafe.Pointer(name)))
		taskbarButtonCreatedMessage = uint32(r)
	}
	return taskbarButtonCreatedMessage
}

type thumbButton struct {
	Mask   uint32
	ID     uint32
	Bitmap uint32
	Icon   uintptr
	Tip    [260]uint16
	Flags  uint32
}

// ThumbBar owns the buttons of one window. Use it from the UI thread.
type ThumbBar struct {
	hwnd    uintptr
	list    unsafe.Pointer // ITaskbarList3*
	icons   map[uint32]uintptr
	playing bool
	added   bool
}

func call(obj unsafe.Pointer, slot int, args ...uintptr) error {
	vtbl := *(*unsafe.Pointer)(obj)
	fn := *(*uintptr)(unsafe.Add(vtbl, uintptr(slot)*unsafe.Sizeof(uintptr(0))))
	hr, _, _ := syscall.SyscallN(fn, append([]uintptr{uintptr(obj)}, args...)...)
	if int32(hr) < 0 {
		return windows.Errno(hr)
	}
	return nil
}

// Add creates the buttons for hwnd. Call it when ButtonCreatedMessage
// arrives; calling it again (Explorer restarted) re-adds them.
func (t *ThumbBar) Add(hwnd uintptr) error {
	t.Close()
	var list unsafe.Pointer
	hr, _, _ := procCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidTaskbarList)), 0, clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidTaskbarList3)), uintptr(unsafe.Pointer(&list)))
	if int32(hr) < 0 || list == nil {
		return errors.New("taskbar: ITaskbarList3 unavailable")
	}
	if err := call(list, vtHrInit); err != nil {
		call(list, vtRelease)
		return err
	}
	t.hwnd, t.list = hwnd, list
	size := iconSize(hwnd)
	t.icons = map[uint32]uintptr{}
	for id, shape := range map[uint32]Shape{ButtonPrevious: ShapePrevious, ButtonNext: ShapeNext, ButtonPlayPause: ShapePlay, ButtonPlayPause | 0x80: ShapePause} {
		icon, err := shape.Icon(size)
		if err != nil {
			t.Close()
			return err
		}
		t.icons[id] = icon
	}
	buttons := []thumbButton{t.button(ButtonPrevious, "Previous"), t.playPauseButton(), t.button(ButtonNext, "Next")}
	if err := call(list, vtThumbBarAddButtons, hwnd, uintptr(len(buttons)), uintptr(unsafe.Pointer(&buttons[0]))); err != nil {
		t.Close()
		return err
	}
	t.added = true
	return nil
}

func (t *ThumbBar) button(id uint32, tip string) thumbButton {
	b := thumbButton{Mask: thbIcon | thbTooltip | thbFlags, ID: id, Icon: t.icons[id]}
	copy(b.Tip[:len(b.Tip)-1], windows.StringToUTF16(tip))
	return b
}

func (t *ThumbBar) playPauseButton() thumbButton {
	if t.playing {
		b := t.button(ButtonPlayPause, "Pause")
		b.Icon = t.icons[ButtonPlayPause|0x80]
		return b
	}
	return t.button(ButtonPlayPause, "Play")
}

// SetPlaying switches the middle button between Play and Pause.
func (t *ThumbBar) SetPlaying(playing bool) {
	if t.playing == playing && t.added {
		return
	}
	t.playing = playing
	if !t.added {
		return
	}
	b := t.playPauseButton()
	_ = call(t.list, vtThumbBarUpdateBtns, t.hwnd, 1, uintptr(unsafe.Pointer(&b)))
}

// Clicked decodes a WM_COMMAND from a thumbnail button.
func Clicked(msg uint32, wParam uintptr) (button uint32, ok bool) {
	if msg != wmCommand || uint32(wParam>>16)&0xFFFF != thbnClicked {
		return 0, false
	}
	id := uint32(wParam & 0xFFFF)
	return id, id >= ButtonPrevious && id <= ButtonNext
}

// Close releases the COM object and icons.
func (t *ThumbBar) Close() {
	if t.list != nil {
		call(t.list, vtRelease)
		t.list = nil
	}
	for _, icon := range t.icons {
		destroyIcon(icon)
	}
	t.icons, t.added = nil, false
}

func iconSize(hwnd uintptr) int {
	dpi, _, _ := procGetDpiForWindow.Call(hwnd)
	if dpi == 0 {
		dpi = 96
	}
	size, _, _ := procGetSystemMetricsForDpi.Call(smCxSmIcon, dpi)
	if size == 0 {
		return 16
	}
	return int(size)
}
