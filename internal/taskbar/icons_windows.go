package taskbar

import (
	"math"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	gdi32                  = windows.NewLazySystemDLL("gdi32.dll")
	procCreateDIBSection   = gdi32.NewProc("CreateDIBSection")
	procCreateBitmap       = gdi32.NewProc("CreateBitmap")
	procDeleteObject       = gdi32.NewProc("DeleteObject")
	procCreateIconIndirect = user32.NewProc("CreateIconIndirect")
	procDestroyIcon        = user32.NewProc("DestroyIcon")
)

// Shape is a white glyph on a transparent background, described in unit
// coordinates (0–1).
type Shape [][][2]float64 // polygons

var (
	ShapePlay  = Shape{{{0.32, 0.2}, {0.32, 0.8}, {0.8, 0.5}}}
	ShapePause = Shape{
		{{0.26, 0.22}, {0.43, 0.22}, {0.43, 0.78}, {0.26, 0.78}},
		{{0.57, 0.22}, {0.74, 0.22}, {0.74, 0.78}, {0.57, 0.78}},
	}
	ShapeNext = Shape{
		{{0.2, 0.22}, {0.2, 0.78}, {0.62, 0.5}},
		{{0.66, 0.22}, {0.78, 0.22}, {0.78, 0.78}, {0.66, 0.78}},
	}
	ShapePrevious = Shape{
		{{0.8, 0.22}, {0.8, 0.78}, {0.38, 0.5}},
		{{0.22, 0.22}, {0.34, 0.22}, {0.34, 0.78}, {0.22, 0.78}},
	}
)

func inside(poly [][2]float64, x, y float64) bool {
	in := false
	for i, j := 0, len(poly)-1; i < len(poly); j, i = i, i+1 {
		xi, yi, xj, yj := poly[i][0], poly[i][1], poly[j][0], poly[j][1]
		if (yi > y) != (yj > y) && x < (xj-xi)*(y-yi)/(yj-yi)+xi {
			in = !in
		}
	}
	return in
}

// Rasterize draws the shape at size×size with 4×4 supersampling and returns
// straight-alpha BGRA pixels, top row first.
func (s Shape) Rasterize(size int) []byte {
	const ss = 4
	px := make([]byte, size*size*4)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			hits := 0
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					ux := (float64(x) + (float64(sx)+0.5)/ss) / float64(size)
					uy := (float64(y) + (float64(sy)+0.5)/ss) / float64(size)
					for _, poly := range s {
						if inside(poly, ux, uy) {
							hits++
							break
						}
					}
				}
			}
			i := (y*size + x) * 4
			px[i], px[i+1], px[i+2] = 255, 255, 255
			px[i+3] = byte(math.Round(float64(hits) * 255 / ss / ss))
		}
	}
	return px
}

type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

type iconInfo struct {
	FIcon    int32
	XHotspot uint32
	YHotspot uint32
	HbmMask  uintptr
	HbmColor uintptr
}

// Icon creates an HICON for the shape. The caller destroys it.
func (s Shape) Icon(size int) (uintptr, error) {
	header := bitmapInfoHeader{Size: uint32(unsafe.Sizeof(bitmapInfoHeader{})), Width: int32(size), Height: -int32(size), Planes: 1, BitCount: 32}
	var bits unsafe.Pointer
	color, _, err := procCreateDIBSection.Call(0, uintptr(unsafe.Pointer(&header)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if color == 0 {
		return 0, err
	}
	defer procDeleteObject.Call(color)
	copy(unsafe.Slice((*byte)(bits), size*size*4), s.Rasterize(size))
	mask, _, err := procCreateBitmap.Call(uintptr(size), uintptr(size), 1, 1, 0)
	if mask == 0 {
		return 0, err
	}
	defer procDeleteObject.Call(mask)
	info := iconInfo{FIcon: 1, HbmMask: mask, HbmColor: color}
	icon, _, err := procCreateIconIndirect.Call(uintptr(unsafe.Pointer(&info)))
	if icon == 0 {
		return 0, err
	}
	return icon, nil
}

func destroyIcon(icon uintptr) {
	if icon != 0 {
		procDestroyIcon.Call(icon)
	}
}
