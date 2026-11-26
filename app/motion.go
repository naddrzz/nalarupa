package main

import (
	"math"
	"time"

	"github.com/egoist/mygo/ui"
)

// Custom easing curves calibrated per Emil Kowalski's interface animation system:
// fast initial velocity for immediate feedback, smooth deceleration to rest, <300ms UI ceiling,
// and exits ~25-30% faster than enters.
var (
	// EaseOutQuint (cubic-bezier(0.23, 1, 0.32, 1)) — primary strong ease-out for enters, badges, and tactile feedback.
	EaseOutQuint = ui.CubicBezier(0.23, 1, 0.32, 1)
	// EaseOutQuart (cubic-bezier(0.165, 0.84, 0.44, 1)) — crisp ease-out for origin-aware popovers and overlays.
	EaseOutQuart = ui.CubicBezier(0.165, 0.84, 0.44, 1)
	// EaseDrawer (cubic-bezier(0.32, 0.72, 0, 1)) — iOS-grade sheet/accordion curve for collapsible sections.
	EaseDrawer = ui.CubicBezier(0.32, 0.72, 0, 1)
	// EaseInOutQuart (cubic-bezier(0.77, 0, 0.175, 1)) — on-screen morph/relocation curve.
	EaseInOutQuart = ui.CubicBezier(0.77, 0, 0.175, 1)
	// EaseExitSubtle (cubic-bezier(0.32, 0, 0.67, 0)) — quiet, rapid exit pairing.
	EaseExitSubtle = ui.CubicBezier(0.32, 0, 0.67, 0)

	// IconSwapSpring — bounce-free 260ms spring for contextual icon swaps (copy->check, running->done).
	IconSwapSpring = ui.Animation{Spring: ui.SpringOptions{Duration: 260 * time.Millisecond, Bounce: 0}}
	// NumericRollAnim — fast 180ms bounce-free spring for rolling numeric counters (lines, zoom %, progress).
	NumericRollAnim = ui.Animation{Spring: ui.SpringOptions{Duration: 180 * time.Millisecond, Bounce: 0}}
)

const (
	DurPress   = 135 * time.Millisecond
	DurHover   = 140 * time.Millisecond
	DurPopover = 165 * time.Millisecond
	DurChatIn  = 180 * time.Millisecond
	DurDrawer  = 190 * time.Millisecond
	DurToast   = 210 * time.Millisecond
)

// EaseOutQuint64 evaluates 1 - (1 - t)^5 in float64 for 120Hz GPU Painter animations.
func EaseOutQuint64(t float64) float64 {
	t = max(0, min(1, t))
	u := 1 - t
	u2 := u * u
	return 1 - u2*u2*u
}

// EaseOutQuart64 evaluates 1 - (1 - t)^4 in float64 for 120Hz GPU Painter animations.
func EaseOutQuart64(t float64) float64 {
	t = max(0, min(1, t))
	u := 1 - t
	u2 := u * u
	return 1 - u2*u2
}

// InstantTransition disables all motion and opacity fades immediately when reduced motion is active.
var InstantTransition = ui.ElementTransition{
	Duration:     -1,
	ExitDuration: -1,
}

// ColorHoverTransition smoothly crossfades surface & border colors in Oklab space over 140ms.
func ColorHoverTransition(reduced bool) ui.ElementTransition {
	if reduced {
		return InstantTransition
	}
	return ui.ElementTransition{
		Colors:  true,
		Shadows: true,
		Animation: &ui.Animation{
			Duration: DurHover,
			Ease:     EaseOutQuint,
			Oklab:    true,
		},
	}
}

// PopoverTransition provides an origin-aware (Flip: true) 165ms scale+nudge enter and faster 115ms exit.
func PopoverTransition(reduced bool) ui.ElementTransition {
	if reduced {
		return InstantTransition
	}
	return ui.ElementTransition{
		Duration:     DurPopover,
		Ease:         EaseOutQuart,
		ExitDuration: 115 * time.Millisecond,
		ExitEase:     EaseExitSubtle,
		Enter:        &ui.Motion{Scale: 0.96, Y: -4, Flip: true},
		Exit:         &ui.Motion{Scale: 0.97, Y: -3, Flip: true},
	}
}

// ChatItemTransition animates newly streamed chat items upward 6px from scale 0.985 in 180ms,
// with a quiet 125ms collapsing exit when queued/permission items resolve (never collapsing on Enter so buttons stay clickable from frame 0!).
func ChatItemTransition(reduced bool) ui.ElementTransition {
	if reduced {
		return InstantTransition
	}
	return ui.ElementTransition{
		Duration:     DurChatIn,
		Ease:         EaseOutQuint,
		ExitDuration: 125 * time.Millisecond,
		ExitEase:     EaseExitSubtle,
		Enter:        &ui.Motion{Y: 6, Scale: 0.985},
		Exit:         &ui.Motion{Y: -4, Scale: 0.985, Collapse: true},
	}
}

// DrawerCollapseTransition smoothly reveals accordion details and drawers via GPU transform+opacity in 185ms (130ms collapsing exit),
// keeping interactive controls immediately clickable from frame 0 without animating layout height on enter.
func DrawerCollapseTransition(reduced bool) ui.ElementTransition {
	if reduced {
		return InstantTransition
	}
	return ui.ElementTransition{
		Duration:     185 * time.Millisecond,
		Ease:         EaseDrawer,
		ExitDuration: 130 * time.Millisecond,
		ExitEase:     EaseExitSubtle,
		Enter:        &ui.Motion{Y: -4, Scale: 0.99},
		Exit:         &ui.Motion{Collapse: true, Y: -4},
	}
}

// ChipTransition animates contextual chips (e.g. active /skills, jump-to-bottom pill) in 150ms / 105ms out.
func ChipTransition(reduced bool) ui.ElementTransition {
	if reduced {
		return InstantTransition
	}
	return ui.ElementTransition{
		Duration:     150 * time.Millisecond,
		Ease:         EaseOutQuint,
		ExitDuration: 105 * time.Millisecond,
		ExitEase:     EaseExitSubtle,
		Enter:        &ui.Motion{Scale: 0.95, Y: 4},
		Exit:         &ui.Motion{Scale: 0.96, Y: 3},
	}
}

// ToastTransition animates bottom toasts in from Y:+14, Scale:0.97 (210ms) and out in 145ms.
func ToastTransition(reduced bool) ui.ElementTransition {
	if reduced {
		return InstantTransition
	}
	return ui.ElementTransition{
		Duration:     DurToast,
		Ease:         EaseOutQuint,
		ExitDuration: 145 * time.Millisecond,
		ExitEase:     EaseExitSubtle,
		Enter:        &ui.Motion{Y: 14, Scale: 0.97},
		Exit:         &ui.Motion{Y: 8, Scale: 0.98},
	}
}

// DialogBannerTransition animates inline confirmation banners (e.g. Unsaved changes) via transform+opacity (Scale:0.97, Y:-6)
// so Cancel/Save buttons remain 100% hit-testable on the very first frame!
func DialogBannerTransition(reduced bool) ui.ElementTransition {
	if reduced {
		return InstantTransition
	}
	return ui.ElementTransition{
		Duration:     200 * time.Millisecond,
		Ease:         EaseDrawer,
		ExitDuration: 140 * time.Millisecond,
		ExitEase:     EaseExitSubtle,
		Enter:        &ui.Motion{Scale: 0.97, Y: -6},
		Exit:         &ui.Motion{Scale: 0.98, Y: -4, Collapse: true},
	}
}

// ApplyPressScale applies tactile active:scale(0.96) press feedback with a 135ms EaseOutQuint return,
// disabled cleanly when disabled or reduced motion is active.
func ApplyPressScale(el ui.Element, pressed, disabled, reduced bool) {
	if reduced {
		return
	}
	target := float32(1.0)
	if pressed && !disabled {
		target = 0.96
	}
	s := el.AnimateWith("press", target, DurPress, EaseOutQuint)
	if math.Abs(float64(s-1.0)) > 1e-4 {
		el.Scale(s, s)
	}
}

// AnimatedPopover renders an origin-aware popover panel with PopoverTransition (165ms enter / 115ms exit).
func AnimatedPopover(c *ui.Context, anchor ui.Element, open *bool, originX, originY float32, fn func()) ui.Element {
	reduced := EffectiveReducedMotion(c)
	return ui.PopoverBase(c, anchor, open, func(panel ui.Element) {
		panel.MinWidth(anchor.Bounds().W).
			Margin(4, 0, 0, 0).
			Radius(8).
			Background(C.Panel2).
			Border(1, C.Border).
			Shadow(0, 8, 24, 0, ui.RGBA(0, 0, 0, 0.36)).
			TransformOrigin(originX, originY).
			Transition(PopoverTransition(reduced))
		fn()
	})
}
