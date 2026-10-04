package taskbar

import (
	"testing"
	"unsafe"
)

// THUMBBUTTON is 552 bytes on 64-bit Windows; a wrong layout would make
// Windows read garbage.
func TestThumbButtonLayout(t *testing.T) {
	if unsafe.Sizeof(uintptr(0)) == 8 && unsafe.Sizeof(thumbButton{}) != 552 {
		t.Fatalf("THUMBBUTTON size = %d, want 552", unsafe.Sizeof(thumbButton{}))
	}
	if off := unsafe.Offsetof(thumbButton{}.Icon); unsafe.Sizeof(uintptr(0)) == 8 && off != 16 {
		t.Fatalf("hIcon offset = %d, want 16", off)
	}
}

func TestShapesRasterize(t *testing.T) {
	for name, s := range map[string]Shape{"play": ShapePlay, "pause": ShapePause, "next": ShapeNext, "previous": ShapePrevious} {
		px := s.Rasterize(16)
		opaque, clear := 0, 0
		for i := 3; i < len(px); i += 4 {
			switch px[i] {
			case 255:
				opaque++
			case 0:
				clear++
			}
		}
		if opaque < 20 || clear < 60 {
			t.Errorf("%s: %d opaque, %d clear pixels", name, opaque, clear)
		}
		if px[3] != 0 {
			t.Errorf("%s: corner must be transparent", name)
		}
	}
	// Pause has a gap between its bars.
	if px := ShapePause.Rasterize(16); px[(8*16+8)*4+3] != 0 {
		t.Error("pause bars must be separate")
	}
}

func TestIconsCreateAndClickDecoding(t *testing.T) {
	icon, err := ShapePlay.Icon(20)
	if err != nil || icon == 0 {
		t.Fatalf("Icon: %v", err)
	}
	destroyIcon(icon)
	if id, ok := Clicked(wmCommand, uintptr(thbnClicked)<<16|ButtonNext); !ok || id != ButtonNext {
		t.Fatalf("click = %v, %v", id, ok)
	}
	if _, ok := Clicked(wmCommand, 0x1234); ok {
		t.Fatal("ordinary menu commands are not thumbnail clicks")
	}
	if ButtonCreatedMessage() == 0 {
		t.Fatal("TaskbarButtonCreated must register")
	}
}
