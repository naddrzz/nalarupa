//go:build !windows

package main

import (
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// InitialWindowMetrics returns default window dimensions on non-Windows platforms.
func InitialWindowMetrics() (w, h, minW, minH int) {
	return DefaultWinW, DefaultWinH, MinWinW, MinWinH
}

// DecorateNativeWindow is a no-op outside Windows (win.SetIcon handles PNG window icon).
func DecorateNativeWindow(_ *mygo.Window) {}

// SyncNativeCanvasCursor is a no-op outside Windows (macOS and Linux already have native open/closed hand cursors).
func SyncNativeCanvasCursor(_ *mygo.Window, _ int, _ ui.Rect) {}

// ExpectedNativeGrabCursors returns zero handles on non-Windows platforms.
func ExpectedNativeGrabCursors() (grab, grabbing uintptr) { return 0, 0 }
