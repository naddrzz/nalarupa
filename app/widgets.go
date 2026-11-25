package main

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/egoist/mygo/ui"
)

var coalesceSeq atomic.Uint64

func nextCoalesceID(prefix string) string {
	n := coalesceSeq.Add(1)
	return fmt.Sprintf("%s:%d", prefix, n)
}

// IconBtn renders an accessible 28x28 (or custom size) Nalarupa icon button with tooltip and active/hover state.
func IconBtn(c *ui.Context, key, icon, label string, size float32, active bool) ui.Element {
	if size <= 0 {
		size = 28
	}
	b := ui.ButtonBase(c.Key(key)).
		Size(size, size).
		Radius(6).
		Center().
		Label(label).
		Tooltip(label).
		Cursor(ui.CursorPointer)
	iconCol := C.Muted
	switch {
	case active:
		b.Background(C.AccentSoft)
		iconCol = C.Accent
	case b.Pressed():
		b.Background(C.Border)
		iconCol = C.Text
	case b.Hovered():
		b.Background(C.Raised)
		iconCol = C.Text
	}
	iconPx := float32(15)
	if size < 26 {
		iconPx = 13
	}
	b.Children(func() {
		ui.Icon(c, IconSVG(icon)).FontSize(iconPx).TextColor(iconCol)
	})
	return b
}

// DangerIconBtn renders a destructive icon button (e.g. Stop agent).
func DangerIconBtn(c *ui.Context, key, icon, label string, size float32) ui.Element {
	if size <= 0 {
		size = 28
	}
	b := ui.ButtonBase(c.Key(key)).
		Size(size, size).
		Radius(6).
		Center().
		Label(label).
		Tooltip(label).
		Cursor(ui.CursorPointer).
		Border(1, C.DangerEdge)
	if b.Pressed() {
		b.Background(C.DangerEdge)
	} else if b.Hovered() {
		b.Background(C.DangerBg)
	} else {
		b.Background(C.DangerBg)
	}
	b.Children(func() {
		ui.Icon(c, IconSVG(icon)).FontSize(15).TextColor(C.Danger)
	})
	return b
}

// AccentIconBtn renders a primary orange icon button (e.g. Send message).
func AccentIconBtn(c *ui.Context, key, icon, label string, size float32, disabled bool) ui.Element {
	if size <= 0 {
		size = 28
	}
	b := ui.ButtonBase(c.Key(key)).
		Size(size, size).
		Radius(6).
		Center().
		Label(label).
		Tooltip(label).
		Disabled(disabled).
		Cursor(ui.CursorPointer)
	bg := C.Accent
	if disabled {
		bg = C.Raised
	} else if b.Pressed() {
		bg = C.AccentPress
	} else if b.Hovered() {
		bg = C.AccentHover
	}
	fg := C.AccentText
	if disabled {
		fg = C.Ghost
	}
	b.Background(bg).Children(func() {
		ui.Icon(c, IconSVG(icon)).FontSize(14).TextColor(fg)
	})
	return b
}

// ActionBtn renders a standard, "primary", or "danger" rectangular button with optional leading icon.
func ActionBtn(c *ui.Context, key, label, icon, variant string) ui.Element {
	b := ui.ButtonBase(c.Key(key)).
		Height(30).
		Padding(0, 12).
		Radius(7).
		Row().
		Gap(6).
		AlignItems(ui.Center).
		Justify(ui.Center).
		Label(label).
		Cursor(ui.CursorPointer)
	fg := C.Text
	weight := 500
	switch variant {
	case "primary":
		fg = C.AccentText
		weight = 600
		if b.Pressed() {
			b.Background(C.AccentPress)
		} else if b.Hovered() {
			b.Background(C.AccentHover)
		} else {
			b.Background(C.Accent)
		}
	case "danger":
		fg = C.Danger
		b.Border(1, C.Border)
		if b.Pressed() {
			b.Background(C.Border)
		} else if b.Hovered() {
			b.Background(C.RaisedHover)
		} else {
			b.Background(C.Raised)
		}
	default:
		b.Border(1, C.Border)
		if b.Pressed() {
			b.Background(C.Border)
		} else if b.Hovered() {
			b.Background(C.RaisedHover)
		} else {
			b.Background(C.Raised)
		}
	}
	b.Children(func() {
		if icon != "" {
			ui.Icon(c, IconSVG(icon)).FontSize(14).TextColor(fg)
		}
		ui.Text(c, label).FontSize(12).FontWeight(weight).TextColor(fg).MaxLines(1)
	})
	return b
}

// ToggleChip renders a compact 24px-tall checkbox chip ("Hug W", "Fast", etc.).
func ToggleChip(c *ui.Context, key, label string, on bool) (ui.Element, bool) {
	b := ui.ButtonBase(c.Key(key)).
		Height(24).
		Padding(0, 8).
		Radius(5).
		Row().
		AlignItems(ui.Center).
		Justify(ui.Center).
		Role(ui.RoleCheckBox).
		Checked(on).
		Label(label).
		Cursor(ui.CursorPointer)
	fg := C.Muted
	if on {
		fg = C.Text
		b.Border(1, C.Select)
		if b.Hovered() {
			b.Background(C.SelectHover)
		} else {
			b.Background(C.SelectBg)
		}
	} else {
		b.Border(1, C.Border)
		if b.Hovered() {
			b.Background(C.Raised)
		} else {
			b.Background(C.Panel2)
		}
	}
	b.Children(func() {
		ui.Text(c, label).FontSize(11).FontWeight(500).TextColor(fg).MaxLines(1)
	})
	return b, b.Clicked()
}

// SegOption describes a tab option inside SegmentedTabs.
type SegOption[V comparable] struct {
	Key   string
	Value V
	Label string
}

// SegmentedTabs renders a full-width WAI-ARIA tablist with individually keyed tab buttons.
func SegmentedTabs[V comparable](c *ui.Context, key, ariaLabel string, current V, opts []SegOption[V]) (V, bool) {
	next := current
	changed := false
	ui.Row(c.Key(key)).
		FillWidth().
		Padding(2).
		Gap(2).
		Radius(7).
		Background(C.Panel2).
		Border(1, C.Border).
		Role(ui.RoleTabList).
		Label(ariaLabel).
		Children(func() {
			for i, opt := range opts {
				sel := opt.Value == current
				tKey := opt.Key
				if tKey == "" {
					tKey = fmt.Sprintf("%s-%d", key, i)
				}
				tab := ui.ButtonBase(c.Key(tKey)).
					Grow(1).
					Basis(0).
					Height(24).
					Padding(0, 6).
					Radius(5).
					Center().
					Role(ui.RoleTab).
					Selected(sel).
					Label(opt.Label).
					Cursor(ui.CursorPointer)
				fg := C.Muted
				if sel {
					fg = C.Text
					tab.Background(C.Raised)
				} else if tab.Hovered() {
					fg = C.Text
				}
				tab.Children(func() {
					ui.Text(c, opt.Label).FontSize(11).FontWeight(500).TextColor(fg).MaxLines(1)
				})
				if tab.Clicked() && !sel {
					next = opt.Value
					changed = true
				}
			}
		})
	return next, changed
}

// FieldOpts configures CommittedField.
type FieldOpts struct {
	Key         string
	AriaLabel   string
	Prefix      string
	Value       string
	Placeholder string
	Mono        bool
	Password    bool
	Width       float32
	Grow        bool
	FocusBack   *ui.Handle
	Handle      *ui.Handle
	OnCommit    func(val string) bool
}

type fieldLocalState struct {
	draft      string
	lastSynced string
	editing    bool
	escaped    bool
}

// CommittedField renders a 26px-tall single-line input that syncs external Value while idle
// and commits on Enter or blur (reverting if OnCommit returns false or Escape is pressed).
func CommittedField(c *ui.Context, o FieldOpts) ui.Element {
	wrap := ui.Row(c.Key(o.Key)).Gap(4).AlignItems(ui.Center)
	if o.Width > 0 {
		wrap.Width(o.Width)
	} else if o.Grow {
		wrap.Grow(1).Basis(0)
	}
	st := ui.Local(wrap, "field", func() fieldLocalState {
		return fieldLocalState{draft: o.Value, lastSynced: o.Value}
	})
	if !st.editing && o.Value != st.lastSynced {
		st.draft = o.Value
		st.lastSynced = o.Value
	}

	var inp ui.Element
	wrap.Children(func() {
		if o.Prefix != "" {
			ui.Box(c).Width(14).Height(26).Center().Children(func() {
				ui.Text(c, o.Prefix).FontSize(11).TextColor(C.Ghost).Unselectable()
			})
		}
		inp = ui.TextInput(c.Key("in"), &st.draft).
			Grow(1).
			Basis(0).
			Height(26).
			Radius(6).
			Background(C.Panel2).
			Border(1, C.Field).
			TextColor(C.Text).
			CaretColor(C.Accent).
			FontSize(12).
			Label(o.AriaLabel)
		if o.Handle != nil {
			inp.Bind(o.Handle)
		}
		if o.Placeholder != "" {
			inp.Placeholder(o.Placeholder).PlaceholderColor(C.Ghost)
		}
		if o.Mono {
			inp.Font(MonoFont())
		}
		if o.Password {
			inp.Password()
		}
		inp.OnTextKey(func(k ui.TextKey) bool {
			if k.Key == ui.KeyEscape {
				st.draft = o.Value
				st.lastSynced = o.Value
				st.escaped = true
				k.SetText(o.Value)
				if o.FocusBack != nil {
					o.FocusBack.Focus()
				}
				return true
			}
			return false
		})

		focused := inp.Focused()
		submitted := inp.Submitted()
		focusLeft := inp.FocusLeft()

		if focused && !st.editing {
			st.editing = true
			st.escaped = false
			st.draft = o.Value
			st.lastSynced = o.Value
		}

		if submitted {
			val := st.draft
			ok := true
			if o.OnCommit != nil {
				ok = o.OnCommit(val)
			}
			if !ok {
				st.draft = o.Value
			} else {
				st.lastSynced = val
			}
			st.editing = false
			if o.FocusBack != nil {
				o.FocusBack.Focus()
			}
		} else if focusLeft {
			if st.escaped {
				st.escaped = false
				st.editing = false
				st.draft = o.Value
			} else if st.editing {
				val := st.draft
				if val != o.Value && o.OnCommit != nil {
					if !o.OnCommit(val) {
						st.draft = o.Value
					} else {
						st.lastSynced = val
					}
				}
				st.editing = false
			}
		}
	})
	return inp
}

// NumFieldOpts configures ScrubNumField.
type NumFieldOpts struct {
	Key       string
	AriaLabel string
	Prefix    string
	Value     float64
	Mixed     bool
	Step      float64
	HasMin    bool
	Min       float64
	HasMax    bool
	Max       float64
	Width     float32
	Grow      bool
	FocusBack *ui.Handle
	Handle    *ui.Handle
	OnSet     func(val float64, coalesce string) bool
	OnStep    func(delta float64, coalesce string)
}

type numFieldLocalState struct {
	draft       string
	lastFormatted string
	editing     bool
	escaped     bool
	scrubbing   bool
	scrubAccum  float64
	sessionKey  string
}

func formatNum(v float64) string {
	rounded := math.Round(v*100) / 100
	if rounded == 0 {
		rounded = 0
	}
	return strconv.FormatFloat(rounded, 'f', -1, 64)
}

func clampNum(v float64, o NumFieldOpts) float64 {
	if o.HasMin && v < o.Min {
		v = o.Min
	}
	if o.HasMax && v > o.Max {
		v = o.Max
	}
	return v
}

// ScrubNumField renders a numeric input with a 14x26 horizontal-drag scrubbable prefix label,
// Up/Down (+ Shift x10) arrow stepping, Mixed multi-selection relative stepping, and undo coalescing.
func ScrubNumField(c *ui.Context, o NumFieldOpts) ui.Element {
	step := o.Step
	if step <= 0 {
		step = 1
	}
	extStr := ""
	if !o.Mixed {
		extStr = formatNum(o.Value)
	}

	wrap := ui.Row(c.Key(o.Key)).Gap(4).AlignItems(ui.Center)
	if o.Width > 0 {
		wrap.Width(o.Width)
	} else if o.Grow {
		wrap.Grow(1).Basis(0)
	}

	st := ui.Local(wrap, "num", func() numFieldLocalState {
		return numFieldLocalState{draft: extStr, lastFormatted: extStr}
	})
	if !st.editing && !st.scrubbing && extStr != st.lastFormatted {
		st.draft = extStr
		st.lastFormatted = extStr
	}

	applyDelta := func(steps float64) {
		if steps == 0 {
			return
		}
		if st.sessionKey == "" {
			st.sessionKey = nextCoalesceID("scrub:" + o.Key)
		}
		delta := steps * step
		if o.Mixed && o.OnStep != nil {
			o.OnStep(delta, st.sessionKey)
			return
		}
		base := o.Value
		if v, err := strconv.ParseFloat(strings.TrimSpace(st.draft), 64); err == nil && !math.IsNaN(v) && !math.IsInf(v, 0) {
			base = v
		}
		next := clampNum(base+delta, o)
		if o.OnSet != nil {
			o.OnSet(next, st.sessionKey)
		}
		st.draft = formatNum(next)
		st.lastFormatted = st.draft
	}

	var inp ui.Element
	wrap.Children(func() {
		if o.Prefix != "" {
			scrubKey := o.Key + "-scrub"
			lbl := ui.Box(c.Key(scrubKey)).
				Size(14, 26).
				Center().
				Role(ui.RoleSlider).
				Label(o.AriaLabel + " scrub").
				Cursor(ui.CursorResizeEW)
			lbl.Children(func() {
				ui.Text(c, o.Prefix).FontSize(11).TextColor(C.Ghost).Unselectable()
			})
			dx, _, held := lbl.Dragged()
			if held {
				if !st.scrubbing {
					st.scrubbing = true
					st.scrubAccum = 0
					st.sessionKey = nextCoalesceID("scrub:" + o.Key)
				}
				st.scrubAccum += float64(dx)
				if c.Modifiers()&ui.Shift != 0 {
					ticks := math.Trunc(st.scrubAccum / 20.0)
					if ticks != 0 {
						st.scrubAccum -= ticks * 20.0
						applyDelta(ticks * 10.0)
					}
				} else {
					ticks := math.Trunc(st.scrubAccum / 2.0)
					if ticks != 0 {
						st.scrubAccum -= ticks * 2.0
						applyDelta(ticks)
					}
				}
			} else if st.scrubbing {
				st.scrubbing = false
				st.scrubAccum = 0
				st.sessionKey = ""
			}
		}

		inp = ui.TextInput(c.Key("in"), &st.draft).
			Grow(1).
			Basis(0).
			Height(26).
			Radius(6).
			Background(C.Panel2).
			Border(1, C.Field).
			TextColor(C.Text).
			CaretColor(C.Accent).
			FontSize(12).
			FontFeatures("tnum").
			Label(o.AriaLabel)
		if o.Handle != nil {
			inp.Bind(o.Handle)
		}
		if o.Mixed {
			inp.Placeholder("Mixed").PlaceholderColor(C.Ghost)
		}
		inp.OnTextKey(func(k ui.TextKey) bool {
			switch k.Key {
			case ui.KeyUp:
				mult := 1.0
				if k.Mods&ui.Shift != 0 {
					mult = 10.0
				}
				applyDelta(mult)
				k.SetText(st.draft)
				return true
			case ui.KeyDown:
				mult := -1.0
				if k.Mods&ui.Shift != 0 {
					mult = -10.0
				}
				applyDelta(mult)
				k.SetText(st.draft)
				return true
			case ui.KeyEscape:
				st.draft = extStr
				st.lastFormatted = extStr
				st.escaped = true
				st.sessionKey = ""
				k.SetText(extStr)
				if o.FocusBack != nil {
					o.FocusBack.Focus()
				}
				return true
			}
			return false
		})

		focused := inp.Focused()
		submitted := inp.Submitted()
		focusLeft := inp.FocusLeft()

		if focused && !st.editing {
			st.editing = true
			st.escaped = false
			st.sessionKey = nextCoalesceID("step:" + o.Key)
			st.draft = extStr
			st.lastFormatted = extStr
		}

		commitTyped := func() {
			raw := strings.TrimSpace(st.draft)
			if raw == "" {
				st.draft = extStr
				return
			}
			v, err := strconv.ParseFloat(raw, 64)
			if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
				st.draft = extStr
				return
			}
			clamped := clampNum(v, o)
			if o.OnSet != nil {
				if !o.OnSet(clamped, "") {
					st.draft = extStr
					return
				}
			}
			st.draft = formatNum(clamped)
			st.lastFormatted = st.draft
		}

		if submitted {
			commitTyped()
			st.editing = false
			st.sessionKey = ""
			if o.FocusBack != nil {
				o.FocusBack.Focus()
			}
		} else if focusLeft {
			if st.escaped {
				st.escaped = false
				st.editing = false
				st.sessionKey = ""
				st.draft = extStr
			} else if st.editing {
				if st.draft != extStr {
					commitTyped()
				}
				st.editing = false
				st.sessionKey = ""
			}
		}
	})
	return inp
}

// InspectorSection renders a titled Inspector section with bottom border and optional header right slot.
func InspectorSection(c *ui.Context, key, title string, right func(), body func()) {
	ui.Column(c.Key(key)).
		FillWidth().
		Padding(12, 14).
		Gap(8).
		BorderWidth(0, 0, 1, 0).
		BorderColor(C.Border).
		Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
				ui.Text(c, title).FontSize(11).FontWeight(600).TextColor(C.Muted).Grow(1)
				if right != nil {
					right()
				}
			})
			body()
		})
}

// ColorSwatch renders an 18x18 rounded color swatch box.
func ColorSwatch(c *ui.Context, key string, col ui.Color) {
	ui.Box(c.Key(key)).
		Size(18, 18).
		Radius(4).
		Background(col).
		Border(1, ui.Hex("#FFFFFF22"))
}
