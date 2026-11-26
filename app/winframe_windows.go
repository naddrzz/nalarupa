//go:build windows

package main

import (
	"math"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

var (
	user32                    = syscall.NewLazyDLL("user32.dll")
	gdi32                     = syscall.NewLazyDLL("gdi32.dll")
	comctl32                  = syscall.NewLazyDLL("comctl32.dll")
	dwmapi                    = syscall.NewLazyDLL("dwmapi.dll")
	procSPIW                  = user32.NewProc("SystemParametersInfoW")
	procLoadImageW            = user32.NewProc("LoadImageW")
	procSendMessageW          = user32.NewProc("SendMessageW")
	procPostMessageW          = user32.NewProc("PostMessageW")
	procGetSystemMetrics      = user32.NewProc("GetSystemMetrics")
	procGetDpiForSystem       = user32.NewProc("GetDpiForSystem")
	procGetDpiForWindow       = user32.NewProc("GetDpiForWindow")
	procGetWindowLongPtrW     = user32.NewProc("GetWindowLongPtrW")
	procSetWindowLongPtrW     = user32.NewProc("SetWindowLongPtrW")
	procSetWindowPos          = user32.NewProc("SetWindowPos")
	procFindWindowExW         = user32.NewProc("FindWindowExW")
	procSetWindowDisplayAff   = user32.NewProc("SetWindowDisplayAffinity")
	procSetCursor             = user32.NewProc("SetCursor")
	procGetCursorPos          = user32.NewProc("GetCursorPos")
	procScreenToClient        = user32.NewProc("ScreenToClient")
	procCreateIconIndirect    = user32.NewProc("CreateIconIndirect")
	procGetCursorInfo         = user32.NewProc("GetCursorInfo")
	procCreateDIBSection      = gdi32.NewProc("CreateDIBSection")
	procCreateBitmap          = gdi32.NewProc("CreateBitmap")
	procDeleteObject          = gdi32.NewProc("DeleteObject")
	procSetWindowSubclass     = comctl32.NewProc("SetWindowSubclass")
	procDefSubclassProc       = comctl32.NewProc("DefSubclassProc")
	procRemoveWindowSubclass  = comctl32.NewProc("RemoveWindowSubclass")
	procDwmSetWindowAttribute = dwmapi.NewProc("DwmSetWindowAttribute")
)

type winRect struct {
	Left, Top, Right, Bottom int32
}

// InitialWindowMetrics computes work-area-clamped window dimensions in DIPs on Windows.
func InitialWindowMetrics() (w, h, minW, minH int) {
	w, h, minW, minH = DefaultWinW, DefaultWinH, MinWinW, MinWinH
	var r winRect
	const spiGetWorkArea = 0x0030
	ret, _, _ := procSPIW.Call(spiGetWorkArea, 0, uintptr(unsafe.Pointer(&r)), 0)
	if ret != 0 {
		aw := int(r.Right - r.Left)
		ah := int(r.Bottom - r.Top)
		dpi := 96
		if procGetDpiForSystem.Find() == nil {
			if d, _, _ := procGetDpiForSystem.Call(); d >= 96 {
				dpi = int(d)
			}
		}
		if dpi > 96 {
			aw = aw * 96 / dpi
			ah = ah * 96 / dpi
		}
		if aw > 0 && ah > 0 {
			return ComputeInitialWindowSize(aw, ah)
		}
	}
	return w, h, minW, minH
}

// DecorateNativeWindow ensures standard Windows frame controls, dark DWM caption/border styling,
// and loads nalarupa.ico onto the HWND.
func DecorateNativeWindow(win *mygo.Window) {
	if win == nil {
		return
	}
	hwnd := win.NativeHandle()
	if hwnd == 0 {
		return
	}
	// Guarantee WS_OVERLAPPEDWINDOW frame bits and ensure WS_EX_NOREDIRECTIONBITMAP is cleared on the frame HWND
	// so DWM + OBS Windows Graphics Capture (WGC) / Window Capture always see a valid composition surface.
	if procGetWindowLongPtrW.Find() == nil && procSetWindowLongPtrW.Find() == nil {
		const (
			wsOverlappedWindow    = 0x00CF0000
			wsExNoRedirectionBmp  = 0x00200000
			swpFrameSync          = 0x0037 // SWP_NOSIZE | SWP_NOMOVE | SWP_NOZORDER | SWP_NOACTIVATE | SWP_FRAMECHANGED
		)
		gwlStyle := ^uintptr(15)   // GWL_STYLE (-16)
		gwlExStyle := ^uintptr(19) // GWL_EXSTYLE (-20)
		styleChanged := false
		style, _, _ := procGetWindowLongPtrW.Call(hwnd, gwlStyle)
		if style != 0 && style&wsOverlappedWindow != wsOverlappedWindow {
			_, _, _ = procSetWindowLongPtrW.Call(hwnd, gwlStyle, style|wsOverlappedWindow)
			styleChanged = true
		}
		exStyle, _, _ := procGetWindowLongPtrW.Call(hwnd, gwlExStyle)
		if exStyle&wsExNoRedirectionBmp != 0 {
			_, _, _ = procSetWindowLongPtrW.Call(hwnd, gwlExStyle, exStyle&^wsExNoRedirectionBmp)
			styleChanged = true
		}
		if styleChanged && procSetWindowPos.Find() == nil {
			_, _, _ = procSetWindowPos.Call(hwnd, 0, 0, 0, 0, 0, swpFrameSync)
		}
	}

	// Explicitly ensure WDA_NONE (0) display affinity so screen/OBS capture is never blocked,
	// and subclass MyGoSurface so CursorGrab / CursorGrabbing render true open/closed hand cursors on Windows.
	if procSetWindowDisplayAff.Find() == nil {
		const wdaNone = 0x00000000
		_, _, _ = procSetWindowDisplayAff.Call(hwnd, wdaNone)
	}
	if child := findMyGoSurfaceHWND(hwnd); child != 0 {
		if procSetWindowDisplayAff.Find() == nil {
			_, _, _ = procSetWindowDisplayAff.Call(child, 0)
		}
		installGrabCursorSubclass(child)
	}

	// DWMWA_USE_IMMERSIVE_DARK_MODE = 20
	// DWMWA_WINDOW_CORNER_PREFERENCE = 33 (2 = DWMWCP_ROUND)
	// DWMWA_BORDER_COLOR = 34 (#45454E -> COLORREF 0x004E4545)
	// DWMWA_CAPTION_COLOR = 35 (#17171A -> COLORREF 0x001A1717)
	// DWMWA_TEXT_COLOR = 36 (#F4F4F6 -> COLORREF 0x00F6F4F4)
	darkMode := uint32(1)
	roundCorner := uint32(2)
	borderCol := uint32(0x004E4545)
	captionCol := uint32(0x001A1717)
	textCol := uint32(0x00F6F4F4)
	_, _, _ = procDwmSetWindowAttribute.Call(hwnd, 20, uintptr(unsafe.Pointer(&darkMode)), 4)
	_, _, _ = procDwmSetWindowAttribute.Call(hwnd, 33, uintptr(unsafe.Pointer(&roundCorner)), 4)
	_, _, _ = procDwmSetWindowAttribute.Call(hwnd, 34, uintptr(unsafe.Pointer(&borderCol)), 4)
	_, _, _ = procDwmSetWindowAttribute.Call(hwnd, 35, uintptr(unsafe.Pointer(&captionCol)), 4)
	_, _, _ = procDwmSetWindowAttribute.Call(hwnd, 36, uintptr(unsafe.Pointer(&textCol)), 4)

	icoPath := findWindowsICO()
	if icoPath == "" {
		return
	}
	pathPtr, err := syscall.UTF16PtrFromString(icoPath)
	if err != nil {
		return
	}
	const (
		imageIcon      = 1
		lrLoadFromFile = 0x0010
		wmSetIcon      = 0x0080
		smCxIcon       = 11
		smCxSmIcon     = 49
	)
	smW, _, _ := procGetSystemMetrics.Call(smCxSmIcon)
	lgW, _, _ := procGetSystemMetrics.Call(smCxIcon)
	if smW == 0 {
		smW = 16
	}
	if lgW == 0 {
		lgW = 32
	}
	hSmall, _, _ := procLoadImageW.Call(0, uintptr(unsafe.Pointer(pathPtr)), imageIcon, smW, smW, lrLoadFromFile)
	if hSmall != 0 {
		_, _, _ = procSendMessageW.Call(hwnd, wmSetIcon, 0, hSmall)
	}
	hBig, _, _ := procLoadImageW.Call(0, uintptr(unsafe.Pointer(pathPtr)), imageIcon, lgW, lgW, lrLoadFromFile)
	if hBig != 0 {
		_, _, _ = procSendMessageW.Call(hwnd, wmSetIcon, 1, hBig)
	}
}

func findWindowsICO() string {
	if exe, err := os.Executable(); err == nil {
		cand := filepath.Join(filepath.Dir(exe), "nalarupa.ico")
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
	}
	cand := filepath.Join("assets", "brand", "nalarupa.ico")
	if _, err := os.Stat(cand); err == nil {
		return cand
	}
	return ""
}

type winPoint struct {
	X, Y int32
}

type winBitmapInfoHeader struct {
	BiSize          uint32
	BiWidth         int32
	BiHeight        int32
	BiPlanes        uint16
	BiBitCount      uint16
	BiCompression   uint32
	BiSizeImage     uint32
	BiXPelsPerMeter int32
	BiYPelsPerMeter int32
	BiClrUsed       uint32
	BiClrImportant  uint32
}

type winIconInfo struct {
	FIcon    int32
	XHotspot uint32
	YHotspot uint32
	HbmMask  uintptr
	HbmColor uintptr
}

type winCursorInfo struct {
	CbSize      uint32
	Flags       uint32
	HCursor     uintptr
	PtScreenPos winPoint
}

var (
	grabCursorsOnce      sync.Once
	hWinCursorGrab       uintptr
	hWinCursorGrabbing   uintptr
	grabSubclassCallback uintptr

	winGrabMu          sync.Mutex
	winGrabSurfHWND    uintptr
	winGrabSubclassed  = map[uintptr]bool{}
	winCanvasRectDIP   ui.Rect
	activeWinGrabMode  atomic.Int32
)

func findMyGoSurfaceHWND(frameHWND uintptr) uintptr {
	if frameHWND == 0 || procFindWindowExW.Find() != nil {
		return 0
	}
	surfCls, err := syscall.UTF16PtrFromString("MyGoSurface")
	if err != nil {
		return 0
	}
	child, _, _ := procFindWindowExW.Call(frameHWND, 0, uintptr(unsafe.Pointer(surfCls)), 0)
	return child
}

func initNativeGrabCursors() {
	grabCursorsOnce.Do(func() {
		hWinCursorGrab = buildNativeHandCursor(false)
		hWinCursorGrabbing = buildNativeHandCursor(true)
		grabSubclassCallback = syscall.NewCallback(grabCursorSubclassProc)
	})
}

func nativeGrabCursorHandle(mode int) uintptr {
	initNativeGrabCursors()
	if mode == 2 {
		return hWinCursorGrabbing
	}
	if mode == 1 {
		return hWinCursorGrab
	}
	return 0
}

func installGrabCursorSubclass(surfHWND uintptr) {
	if surfHWND == 0 || procSetWindowSubclass.Find() != nil || procDefSubclassProc.Find() != nil {
		return
	}
	initNativeGrabCursors()
	winGrabMu.Lock()
	already := winGrabSubclassed[surfHWND]
	if !already {
		winGrabSubclassed[surfHWND] = true
		winGrabSurfHWND = surfHWND
	}
	winGrabMu.Unlock()
	if !already && grabSubclassCallback != 0 {
		_, _, _ = procSetWindowSubclass.Call(surfHWND, grabSubclassCallback, 1, 0)
	}
}

func isCursorInsideWinCanvas(surfHWND uintptr) bool {
	if surfHWND == 0 {
		return false
	}
	var pt winPoint
	r1, _, _ := procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	if r1 == 0 {
		return false
	}
	r2, _, _ := procScreenToClient.Call(surfHWND, uintptr(unsafe.Pointer(&pt)))
	if r2 == 0 {
		return false
	}
	dpi := float32(96)
	if procGetDpiForWindow.Find() == nil {
		if d, _, _ := procGetDpiForWindow.Call(surfHWND); d >= 96 {
			dpi = float32(d)
		}
	}
	scale := dpi / 96.0
	winGrabMu.Lock()
	rc := winCanvasRectDIP
	winGrabMu.Unlock()
	if rc.W <= 0 || rc.H <= 0 {
		return false
	}
	fx, fy := float32(pt.X)/scale, float32(pt.Y)/scale
	return fx >= rc.X && fx < rc.X+rc.W && fy >= rc.Y && fy < rc.Y+rc.H
}

func grabCursorSubclassProc(hwnd, msg, wp, lp, uIdSubclass, _ uintptr) uintptr {
	const (
		wmSetCursor = 0x0020
		wmNCDestroy = 0x0082
		htClient    = 1
	)
	if msg == wmNCDestroy {
		if procRemoveWindowSubclass.Find() == nil {
			_, _, _ = procRemoveWindowSubclass.Call(hwnd, grabSubclassCallback, uIdSubclass)
		}
		winGrabMu.Lock()
		delete(winGrabSubclassed, hwnd)
		winGrabMu.Unlock()
	} else if msg == wmSetCursor && (lp&0xFFFF) == htClient {
		if mode := int(activeWinGrabMode.Load()); mode != 0 {
			if mode == 2 || isCursorInsideWinCanvas(hwnd) {
				if hc := nativeGrabCursorHandle(mode); hc != 0 {
					_, _, _ = procSetCursor.Call(hc)
					return 1
				}
			}
		}
	}
	ret, _, _ := procDefSubclassProc.Call(hwnd, msg, wp, lp)
	return ret
}

// SyncNativeCanvasCursor updates the active Windows grab/grabbing HCURSOR state for canvasRect
// and immediately refreshes the live OS cursor when Space or Pan state transitions without moving the mouse.
func SyncNativeCanvasCursor(win *mygo.Window, mode int, canvasRect ui.Rect) {
	if win == nil || win.IsDestroyed() {
		return
	}
	winGrabMu.Lock()
	winCanvasRectDIP = canvasRect
	surf := winGrabSurfHWND
	winGrabMu.Unlock()
	if surf == 0 {
		if child := findMyGoSurfaceHWND(win.NativeHandle()); child != 0 {
			installGrabCursorSubclass(child)
			surf = child
		}
	}
	prev := activeWinGrabMode.Swap(int32(mode))
	if surf != 0 {
		if prev != int32(mode) {
			if mode != 0 && (mode == 2 || isCursorInsideWinCanvas(surf)) {
				if hc := nativeGrabCursorHandle(mode); hc != 0 {
					_, _, _ = procSetCursor.Call(hc)
				}
			}
			// Post WM_SETCURSOR (HTCLIENT=1) after MyGo's post-View surface.SetCursor pass so stationary Space press/release updates immediately!
			if procPostMessageW.Find() == nil {
				_, _, _ = procPostMessageW.Call(surf, 0x0020, surf, 1)
			} else {
				_, _, _ = procSendMessageW.Call(surf, 0x0020, surf, 1)
			}
		} else if mode != 0 && (mode == 2 || isCursorInsideWinCanvas(surf)) {
			if hc := nativeGrabCursorHandle(mode); hc != 0 && ActiveNativeCursorHandle() != hc {
				_, _, _ = procSetCursor.Call(hc)
			}
		}
	}
}

// ActiveNativeCursorHandle returns the current global OS HCURSOR on Windows (used by E2E tests).
func ActiveNativeCursorHandle() uintptr {
	if procGetCursorInfo.Find() != nil {
		return 0
	}
	var ci winCursorInfo
	ci.CbSize = uint32(unsafe.Sizeof(ci))
	if r, _, _ := procGetCursorInfo.Call(uintptr(unsafe.Pointer(&ci))); r != 0 {
		return ci.HCursor
	}
	return 0
}

// ExpectedNativeGrabCursors returns Nalarupa's custom open-hand (grab) and closed-hand (grabbing) HCURSORs.
func ExpectedNativeGrabCursors() (grab, grabbing uintptr) {
	initNativeGrabCursors()
	return hWinCursorGrab, hWinCursorGrabbing
}

func sdSegment(px, py, ax, ay, bx, by float64) float64 {
	pax, pay := px-ax, py-ay
	bax, bay := bx-ax, by-ay
	denom := bax*bax + bay*bay
	h := 0.0
	if denom > 1e-9 {
		h = max(0.0, min(1.0, (pax*bax+pay*bay)/denom))
	}
	return math.Hypot(pax-bax*h, pay-bay*h)
}

func sdRoundBox(px, py, cx, cy, hx, hy, rad float64) float64 {
	qx := math.Abs(px-cx) - (hx - rad)
	qy := math.Abs(py-cy) - (hy - rad)
	out := math.Hypot(max(qx, 0.0), max(qy, 0.0))
	in := min(max(qx, qy), 0.0)
	return out + in - rad
}

func handCursorSDF(x, y float64, grabbing bool) float64 {
	d := 1e9
	if !grabbing {
		// Open 5-finger palm ("grab"): clearly splayed fingers + angled thumb so it never resembles IDC_HAND!
		fingers := [][5]float64{
			{6.6, 15.6, 11.8, 20.8, 1.62},  // open thumb
			{10.6, 7.4, 12.2, 16.6, 1.36},  // index
			{14.4, 5.8, 14.8, 16.6, 1.36},  // middle
			{18.2, 6.6, 17.6, 16.6, 1.36},  // ring
			{21.5, 9.2, 20.1, 17.6, 1.30},  // pinky
		}
		for _, f := range fingers {
			d = min(d, sdSegment(x, y, f[0], f[1], f[2], f[3])-f[4])
		}
		d = min(d, sdRoundBox(x, y, 15.8, 19.8, 4.9, 4.5, 3.8))
	} else {
		// Closed fist ("grabbing"): tucked knuckles & thumb during active canvas pan drag.
		knuckles := [][5]float64{
			{9.0, 16.0, 13.2, 19.4, 1.58},  // tucked thumb
			{11.5, 11.4, 12.2, 16.5, 1.40}, // curled index
			{14.6, 10.6, 14.8, 16.5, 1.40}, // curled middle
			{17.8, 11.0, 17.5, 16.5, 1.40}, // curled ring
			{20.6, 12.3, 19.9, 17.2, 1.34}, // curled pinky
		}
		for _, f := range knuckles {
			d = min(d, sdSegment(x, y, f[0], f[1], f[2], f[3])-f[4])
		}
		d = min(d, sdRoundBox(x, y, 15.6, 19.2, 4.8, 4.2, 3.6))
	}
	return d
}

func smoothStep(edge0, edge1, x float64) float64 {
	t := max(0.0, min(1.0, (x-edge0)/(edge1-edge0)))
	return t * t * (3.0 - 2.0*t)
}

func buildNativeHandCursor(grabbing bool) uintptr {
	if procCreateDIBSection.Find() != nil || procCreateBitmap.Find() != nil || procCreateIconIndirect.Find() != nil {
		return 0
	}
	const smCxCursor = 13
	sysSize, _, _ := procGetSystemMetrics.Call(smCxCursor)
	size := int(max(32, min(sysSize, 64)))
	scale := float64(size) / 32.0

	bmi := winBitmapInfoHeader{
		BiSize:        uint32(unsafe.Sizeof(winBitmapInfoHeader{})),
		BiWidth:       int32(size),
		BiHeight:      -int32(size), // top-down 32-bit BGRA DIB
		BiPlanes:      1,
		BiBitCount:    32,
		BiCompression: 0,
	}
	var bitsPtr *byte
	hColor, _, _ := procCreateDIBSection.Call(0, uintptr(unsafe.Pointer(&bmi)), 0, uintptr(unsafe.Pointer(&bitsPtr)), 0, 0)
	if hColor == 0 || bitsPtr == nil {
		return 0
	}
	defer procDeleteObject.Call(hColor)

	pix := unsafe.Slice(bitsPtr, size*size*4)
	aa := 0.75 / scale
	for py := 0; py < size; py++ {
		ly := (float64(py) + 0.5) / scale
		for px := 0; px < size; px++ {
			lx := (float64(px) + 0.5) / scale
			d := handCursorSDF(lx, ly, grabbing)
			dShadow := handCursorSDF(lx-0.75, ly-1.25, grabbing)

			// Drop shadow + crisp #111827 border (1.35u) + pure #FFFFFF fill
			shA := 0.24 * (1.0 - smoothStep(-0.4, 2.0, dShadow))
			borderCov := 1.0 - smoothStep(1.30-aa, 1.30+aa, d)
			fillCov := 1.0 - smoothStep(-aa, aa, d)

			// Composite shadow -> border (#111827) -> white interior (#FFFFFF) in premultiplied BGRA
			r, g, b, a := 0.0, 0.0, 0.0, shA
			if borderCov > 0 {
				br, bg, bb := 17.0/255.0, 24.0/255.0, 39.0/255.0
				outA := borderCov + a*(1.0-borderCov)
				r = br*borderCov + r*(1.0-borderCov)
				g = bg*borderCov + g*(1.0-borderCov)
				b = bb*borderCov + b*(1.0-borderCov)
				a = outA
			}
			if fillCov > 0 {
				outA := fillCov + a*(1.0-fillCov)
				r = 1.0*fillCov + r*(1.0-fillCov)
				g = 1.0*fillCov + g*(1.0-fillCov)
				b = 1.0*fillCov + b*(1.0-fillCov)
				a = outA
			}
			off := (py*size + px) * 4
			pix[off+0] = uint8(math.Round(max(0.0, min(1.0, b)) * 255.0))
			pix[off+1] = uint8(math.Round(max(0.0, min(1.0, g)) * 255.0))
			pix[off+2] = uint8(math.Round(max(0.0, min(1.0, r)) * 255.0))
			pix[off+3] = uint8(math.Round(max(0.0, min(1.0, a)) * 255.0))
		}
	}

	maskStride := ((size + 15) / 16) * 2
	maskBits := make([]byte, maskStride*size)
	hMask, _, _ := procCreateBitmap.Call(uintptr(size), uintptr(size), 1, 1, uintptr(unsafe.Pointer(&maskBits[0])))
	if hMask == 0 {
		return 0
	}
	defer procDeleteObject.Call(hMask)

	hx := uint32(math.Round(15.0 * scale))
	hy := uint32(math.Round(15.0 * scale))
	ii := winIconInfo{
		FIcon:    0,
		XHotspot: hx,
		YHotspot: hy,
		HbmMask:  hMask,
		HbmColor: hColor,
	}
	hCur, _, _ := procCreateIconIndirect.Call(uintptr(unsafe.Pointer(&ii)))
	return hCur
}
