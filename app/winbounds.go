package main

import "github.com/egoist/mygo"

// ComputeInitialWindowSize computes a comfortable initial window outer size
// and minimum size in DIPs that always fits inside the given work area.
func ComputeInitialWindowSize(workW, workH int) (w, h, minW, minH int) {
	w, h, minW, minH = DefaultWinW, DefaultWinH, MinWinW, MinWinH
	if workW <= 0 || workH <= 0 {
		return w, h, minW, minH
	}

	if workW-40 < minW {
		minW = max(320, workW-40)
	}
	if workH-40 < minH {
		minH = max(240, workH-40)
	}
	if minW > workW {
		minW = workW
	}
	if minH > workH {
		minH = workH
	}

	if w > workW-32 {
		w = int(float64(workW) * 0.92)
	}
	if h > workH-24 {
		h = int(float64(workH) * 0.92)
	}
	if w < minW {
		w = minW
	}
	if h < minH {
		h = minH
	}
	if w > workW {
		w = workW
	}
	if h > workH {
		h = workH
	}
	return w, h, minW, minH
}

// ClampWindowBoundsToWorkArea ensures saved or restored outer window bounds
// remain visible inside the work area so the native titlebar and resize borders
// are never clipped off-screen.
func ClampWindowBoundsToWorkArea(bounds, work mygo.Rectangle, minW, minH int) mygo.Rectangle {
	if work.Width <= 0 || work.Height <= 0 {
		return bounds
	}
	w, h := bounds.Width, bounds.Height
	if w <= 0 {
		w = DefaultWinW
	}
	if h <= 0 {
		h = DefaultWinH
	}
	if minW > work.Width {
		minW = work.Width
	}
	if minH > work.Height {
		minH = work.Height
	}
	if minW > 0 && w < minW {
		w = minW
	}
	if minH > 0 && h < minH {
		h = minH
	}
	if w > work.Width {
		w = work.Width
	}
	if h > work.Height {
		h = work.Height
	}

	x, y := bounds.X, bounds.Y
	if x+w > work.X+work.Width {
		x = work.X + work.Width - w
	}
	if x < work.X {
		x = work.X
	}
	if y+h > work.Y+work.Height {
		y = work.Y + work.Height - h
	}
	if y < work.Y {
		y = work.Y
	}
	return mygo.Rectangle{X: x, Y: y, Width: w, Height: h}
}

// EnsureWindowInWorkArea clamps a non-maximized, non-fullscreen window back into
// its matching display work area and updates its minimum size in DIPs.
func EnsureWindowInWorkArea(win *mygo.Window) {
	if win == nil || win.IsDestroyed() || win.IsFullScreen() || win.IsMaximized() {
		return
	}
	bounds := win.Bounds()
	display := mygo.Screen.DisplayMatching(bounds)
	work := display.WorkArea
	if work.Width <= 0 || work.Height <= 0 {
		work = mygo.Screen.PrimaryDisplay().WorkArea
	}
	if work.Width <= 0 || work.Height <= 0 {
		return
	}
	_, _, minW, minH := ComputeInitialWindowSize(work.Width, work.Height)
	win.SetMinimumSize(minW, minH)
	clamped := ClampWindowBoundsToWorkArea(bounds, work, minW, minH)
	if clamped != bounds {
		win.SetBounds(clamped)
	}
}
