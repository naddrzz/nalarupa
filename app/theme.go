package main

import (
	"math"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
)

const (
	TopH        = 44
	LeftW       = 248
	RightW      = 300
	DefaultWinW = 1440
	DefaultWinH = 900
	MinWinW     = 1100
	MinWinH     = 680

	IndicatorTickMS = 50
)

var (
	CanvasBG  = [3]uint8{0x1c, 0x1c, 0x1f}
	PresentBG = [3]uint8{0x00, 0x00, 0x00}
)

// Palette holds Nalarupa's exact WCAG AA-verified dark UI colors.
type Palette struct {
	BG          ui.Color
	Panel       ui.Color
	Panel2      ui.Color
	Raised      ui.Color
	RaisedHover ui.Color
	Border      ui.Color
	Text        ui.Color
	Muted       ui.Color
	Ghost       ui.Color
	Accent      ui.Color
	AccentHover ui.Color
	AccentPress ui.Color
	AccentText  ui.Color
	AccentSoft  ui.Color
	Select      ui.Color
	SelectBg    ui.Color
	SelectHover ui.Color
	SelectDrop  ui.Color
	SelectSolid ui.Color
	Focus       ui.Color
	Field       ui.Color
	Edge        ui.Color
	Component   ui.Color
	Danger      ui.Color
	DangerBg    ui.Color
	DangerEdge  ui.Color
	OK          ui.Color
	Warn        ui.Color
	WarnBg      ui.Color
	WarnEdge    ui.Color
	CanvasFill  ui.Color
	Snap        ui.Color
	SnapLabel   ui.Color
	PenBlue     ui.Color
	Presence    ui.Color
}

var C = Palette{
	BG:          ui.Hex("#111113"),
	Panel:       ui.Hex("#17171A"),
	Panel2:      ui.Hex("#1E1E22"),
	Raised:      ui.Hex("#26262B"),
	RaisedHover: ui.Hex("#303036"),
	Border:      ui.Hex("#2B2B31"),
	Text:        ui.Hex("#ECECEF"),
	Muted:       ui.Hex("#A1A1AA"),
	Ghost:       ui.Hex("#8C8C96"),
	Accent:      ui.Hex("#FF6A3D"),
	AccentHover: ui.Hex("#FF8059"),
	AccentPress: ui.Hex("#E85A2F"),
	AccentText:  ui.Hex("#16110F"),
	AccentSoft:  ui.Hex("#FF6A3D22"),
	Select:      ui.Hex("#3B82F6"),
	SelectBg:    ui.Hex("#3B82F633"),
	SelectHover: ui.Hex("#3B82F644"),
	SelectDrop:  ui.Hex("#3B82F655"),
	SelectSolid: ui.Hex("#2563EB"),
	Focus:       ui.Hex("#60A5FA"),
	Field:       ui.Hex("#686870"),
	Edge:        ui.Hex("#45454E"),
	Component:   ui.Hex("#A78BFA"),
	Danger:      ui.Hex("#F87171"),
	DangerBg:    ui.Hex("#F871711A"),
	DangerEdge:  ui.Hex("#F8717155"),
	OK:          ui.Hex("#4ADE80"),
	Warn:        ui.Hex("#F59E0B"),
	WarnBg:      ui.Hex("#F59E0B1A"),
	WarnEdge:    ui.Hex("#F59E0B66"),
	CanvasFill:  ui.Hex("#1C1C1F"),
	Snap:        ui.Hex("#F24518"),
	SnapLabel:   ui.Hex("#D9380F"),
	PenBlue:     ui.Hex("#3D99FF"),
	Presence:    ui.Hex("#607DFF"),
}

var (
	themeOnce     sync.Once
	nalarupaTheme *ui.Theme
)

// NalarupaTheme returns the configured dark *ui.Theme for Nalarupa windows and tests.
func NalarupaTheme() *ui.Theme {
	themeOnce.Do(func() {
		t := ui.DarkTheme()
		t.Background = C.BG
		t.Surface = C.Panel
		t.SurfaceHover = C.Raised
		t.SurfacePressed = C.Border
		t.Text = C.Text
		t.TextMuted = C.Muted
		t.Border = C.Border
		t.Accent = C.Accent
		t.AccentText = C.AccentText
		t.Focus = C.Focus
		t.FocusRingWidth = 2
		t.FocusRingOffset = 2
		t.Inverse = C.Raised
		t.InverseText = C.Text
		t.Radius = 6
		t.FontSize = 12
		t.TooltipDelay = 350 * time.Millisecond
		nalarupaTheme = t
	})
	return nalarupaTheme
}

// MonoFont returns the platform monospace font family name.
func MonoFont() string {
	if runtime.GOOS == "darwin" {
		return "Menlo"
	}
	if runtime.GOOS == "windows" {
		return "Consolas"
	}
	return "DejaVu Sans Mono"
}

// EffectiveDPR returns the device pixel ratio, honoring NALARUPA_DPR when set.
func EffectiveDPR(scale float32) float64 {
	if raw := strings.TrimSpace(os.Getenv("NALARUPA_DPR")); raw != "" {
		if v, err := strconv.ParseFloat(raw, 64); err == nil && v > 0 && !math.IsNaN(v) && !math.IsInf(v, 0) {
			return v
		}
	}
	if scale >= 1 {
		return float64(scale)
	}
	return 1
}

// InitialReducedMotion checks NALARUPA_REDUCED_MOTION before the UI loop starts;
// OS preferences are synced once AttachWindow or View(c) runs.
func InitialReducedMotion() bool {
	if raw := os.Getenv("NALARUPA_REDUCED_MOTION"); raw != "" {
		return raw == "1"
	}
	return false
}

// EffectiveReducedMotion resolves reduced-motion inside a UI frame or Tester.
func EffectiveReducedMotion(c *ui.Context) bool {
	if raw := os.Getenv("NALARUPA_REDUCED_MOTION"); raw != "" {
		return raw == "1"
	}
	if c != nil {
		return c.Preferences().ReduceMotion
	}
	return false
}

// EllipsisAt cycles 1..3 dots every 400ms (at 50ms indicator ticks).
func EllipsisAt(nowMS int64) string {
	step := int((nowMS/400)%3 + 3) % 3
	return strings.Repeat(".", step+1)
}

// EllipsisPadAt returns trailing spaces so EllipsisAt + EllipsisPadAt is always 3 chars wide.
func EllipsisPadAt(nowMS int64) string {
	return strings.Repeat(" ", 3-len(EllipsisAt(nowMS)))
}

// PulseAt returns a smooth [0, 1] sinusoidal pulse over time in milliseconds.
func PulseAt(nowMS int64) float64 {
	return 0.5 + 0.5*math.Sin(float64(nowMS)/200.0)
}

// ParseHexRGBA parses #RGB, #RGBA, #RRGGBB, or #RRGGBBAA into (r, g, b, a, ok).
func ParseHexRGBA(s string) (uint8, uint8, uint8, uint8, bool) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "#") {
		return 0, 0, 0, 0, false
	}
	h := s[1:]
	switch len(h) {
	case 3:
		r, ok1 := hexNibble(h[0])
		g, ok2 := hexNibble(h[1])
		b, ok3 := hexNibble(h[2])
		if !ok1 || !ok2 || !ok3 {
			return 0, 0, 0, 0, false
		}
		return r * 17, g * 17, b * 17, 255, true
	case 4:
		r, ok1 := hexNibble(h[0])
		g, ok2 := hexNibble(h[1])
		b, ok3 := hexNibble(h[2])
		a, ok4 := hexNibble(h[3])
		if !ok1 || !ok2 || !ok3 || !ok4 {
			return 0, 0, 0, 0, false
		}
		return r * 17, g * 17, b * 17, a * 17, true
	case 6:
		r, ok1 := hexByte(h[0:2])
		g, ok2 := hexByte(h[2:4])
		b, ok3 := hexByte(h[4:6])
		if !ok1 || !ok2 || !ok3 {
			return 0, 0, 0, 0, false
		}
		return r, g, b, 255, true
	case 8:
		r, ok1 := hexByte(h[0:2])
		g, ok2 := hexByte(h[2:4])
		b, ok3 := hexByte(h[4:6])
		a, ok4 := hexByte(h[6:8])
		if !ok1 || !ok2 || !ok3 || !ok4 {
			return 0, 0, 0, 0, false
		}
		return r, g, b, a, true
	default:
		return 0, 0, 0, 0, false
	}
}

// ParseInspectorHexColor validates #RGB, #RRGGBB, or #RRGGBBAA (rejecting 4-digit #RGBA per Nalarupa spec).
func ParseInspectorHexColor(s string) (ui.Color, bool) {
	s = strings.TrimSpace(s)
	if len(s) != 4 && len(s) != 7 && len(s) != 9 {
		return ui.Color{}, false
	}
	r, g, b, a, ok := ParseHexRGBA(s)
	if !ok {
		return ui.Color{}, false
	}
	return ui.RGBA(r, g, b, float32(a)/255.0), true
}

func hexNibble(c byte) (uint8, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	default:
		return 0, false
	}
}

func hexByte(s string) (uint8, bool) {
	hi, ok1 := hexNibble(s[0])
	lo, ok2 := hexNibble(s[1])
	if !ok1 || !ok2 {
		return 0, false
	}
	return hi<<4 | lo, true
}

// ContrastText returns dark (#111113) or light (#FFFFFF) text with WCAG contrast against hexBg.
func ContrastText(hexBg string) ui.Color {
	r, g, b, _, ok := ParseHexRGBA(hexBg)
	if !ok {
		return ui.Hex("#FFFFFF")
	}
	lin := func(v uint8) float64 {
		x := float64(v) / 255.0
		if x <= 0.03928 {
			return x / 12.92
		}
		return math.Pow((x+0.055)/1.055, 2.4)
	}
	lum := 0.2126*lin(r) + 0.7152*lin(g) + 0.0722*lin(b)
	if lum > 0.35 {
		return ui.Hex("#111113")
	}
	return ui.Hex("#FFFFFF")
}
