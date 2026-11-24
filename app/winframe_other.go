//go:build !windows

package main

import "github.com/egoist/mygo"

// InitialWindowMetrics returns default window dimensions on non-Windows platforms.
func InitialWindowMetrics() (w, h, minW, minH int) {
	return DefaultWinW, DefaultWinH, MinWinW, MinWinH
}

// DecorateNativeWindow is a no-op outside Windows (win.SetIcon handles PNG window icon).
func DecorateNativeWindow(_ *mygo.Window) {}
