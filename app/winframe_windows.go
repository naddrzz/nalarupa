//go:build windows

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"unsafe"

	"github.com/egoist/mygo"
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	dwmapi   = syscall.NewLazyDLL("dwmapi.dll")
	procSPIW = user32.NewProc("SystemParametersInfoW")
	procLoadImageW = user32.NewProc("LoadImageW")
	procSendMessageW = user32.NewProc("SendMessageW")
	procGetSystemMetrics = user32.NewProc("GetSystemMetrics")
	procDwmSetWindowAttribute = dwmapi.NewProc("DwmSetWindowAttribute")
)

type winRect struct {
	Left, Top, Right, Bottom int32
}

// InitialWindowMetrics computes work-area-clamped window dimensions on Windows.
func InitialWindowMetrics() (w, h, minW, minH int) {
	w, h, minW, minH = DefaultWinW, DefaultWinH, MinWinW, MinWinH
	var r winRect
	const spiGetWorkArea = 0x0030
	ret, _, _ := procSPIW.Call(spiGetWorkArea, 0, uintptr(unsafe.Pointer(&r)), 0)
	if ret != 0 {
		aw := int(r.Right - r.Left)
		ah := int(r.Bottom - r.Top)
		if aw > 0 && ah > 0 {
			cw := int(float64(aw)*0.94 - 16)
			ch := int(float64(ah)*0.94 - 40)
			if cw < w {
				w = cw
			}
			if ch < h {
				h = ch
			}
			if w < minW {
				minW = w
			}
			if h < minH {
				minH = h
			}
		}
	}
	return w, h, minW, minH
}

// DecorateNativeWindow applies Windows 11 DWM border/caption colors and loads nalarupa.ico onto the HWND.
func DecorateNativeWindow(win *mygo.Window) {
	if win == nil {
		return
	}
	hwnd := win.NativeHandle()
	if hwnd == 0 {
		return
	}
	// DWMWA_BORDER_COLOR = 34 (#45454E -> COLORREF 0x004E4545)
	// DWMWA_CAPTION_COLOR = 35 (#17171A -> COLORREF 0x001A1717)
	borderCol := uint32(0x004E4545)
	captionCol := uint32(0x001A1717)
	_, _, _ = procDwmSetWindowAttribute.Call(hwnd, 34, uintptr(unsafe.Pointer(&borderCol)), 4)
	_, _, _ = procDwmSetWindowAttribute.Call(hwnd, 35, uintptr(unsafe.Pointer(&captionCol)), 4)

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
