package main

import (
	"testing"

	"github.com/egoist/mygo"
)

func TestWindowInitialSizeFitsScaledWorkArea(t *testing.T) {
	cases := []struct {
		name  string
		workW int
		workH int
	}{
		{name: "1080p_125pct", workW: 1536, workH: 816},
		{name: "720p_100pct", workW: 1280, workH: 680},
		{name: "compact_netbook", workW: 960, workH: 600},
		{name: "4k_150pct", workW: 2560, workH: 1392},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, h, minW, minH := ComputeInitialWindowSize(tc.workW, tc.workH)
			if w <= 0 || h <= 0 {
				t.Fatalf("expected positive dimensions, got %dx%d", w, h)
			}
			if w > tc.workW || h > tc.workH {
				t.Fatalf("initial window %dx%d exceeds work area %dx%d", w, h, tc.workW, tc.workH)
			}
			if minW > w || minH > h {
				t.Fatalf("minimum size %dx%d exceeds initial size %dx%d", minW, minH, w, h)
			}
		})
	}
}

func TestWindowBoundsClampedToWorkArea(t *testing.T) {
	work := mygo.Rectangle{X: 0, Y: 0, Width: 1536, Height: 816}

	// Exact corrupted state observed when UseContentSize + physical pixels pushed titlebar off-screen.
	corrupted := mygo.Rectangle{X: 41, Y: -61, Width: 1454, Height: 882}
	got := ClampWindowBoundsToWorkArea(corrupted, work, MinWinW, MinWinH)
	want := mygo.Rectangle{X: 41, Y: 0, Width: 1454, Height: 816}
	if got != want {
		t.Fatalf("clamped corrupted bounds = %+v, want %+v", got, want)
	}

	// Oversized off-screen window gets bounded to full work area.
	oversized := mygo.Rectangle{X: 200, Y: -50, Width: 1800, Height: 1000}
	gotOver := ClampWindowBoundsToWorkArea(oversized, work, MinWinW, MinWinH)
	if gotOver != work {
		t.Fatalf("clamped oversized bounds = %+v, want %+v", gotOver, work)
	}
}
