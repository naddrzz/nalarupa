package resolve

import (
	"strconv"
	"strings"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

// Migrate runs the .pen file migrations (2.6 → 2.19) in place and stamps the document as 2.19. Files
// without a version are treated as current; icon_font / stroke objects are upgraded regardless.
func Migrate(root jsonv.Value) {
	// ponytail: migrations older than 2.6 (layout objects, text size, image nodes) are skipped; no kit uses them.
	major, minor := uint32(2), uint32(19)
	if v, ok := jsonv.Str(jsonv.Get(root, "version")); ok {
		if a, b, ok := strings.Cut(v, "."); ok {
			x, okA := parseU32(a)
			y, okB := parseU32(b)
			if okA && okB {
				major, minor = x, y
			}
		}
	}
	before := func(m uint32) bool { return major < 2 || (major == 2 && minor < m) }
	for _, k := range jsonv.Items(jsonv.Get(root, "children")) {
		migrateWalk(k, func(n *jsonv.Object) { fixNode(n, before) })
	}
	if o := jsonv.Obj(root); o != nil {
		o.Set("version", "2.19")
	}
}

// parseU32 is Rust's `str::parse::<u32>`: decimal digits with an optional leading `+`.
func parseU32(s string) (uint32, bool) {
	s = strings.TrimPrefix(s, "+")
	if s == "" || strings.ContainsAny(s, "+-") {
		return 0, false
	}
	v, err := strconv.ParseUint(s, 10, 32)
	return uint32(v), err == nil
}

// migrateWalk visits n, then its instance overrides and its children.
func migrateWalk(n jsonv.Value, f func(*jsonv.Object)) {
	o := jsonv.Obj(n)
	if o == nil {
		return
	}
	f(o)
	for _, key := range []string{"descendants", "children"} {
		switch x := jsonv.Get(o, key).(type) {
		case *jsonv.Object:
			x.Each(func(_ string, v jsonv.Value) { migrateWalk(v, f) })
		case *jsonv.Array:
			for _, v := range x.Items {
				migrateWalk(v, f)
			}
		}
	}
}

func fixNode(o *jsonv.Object, before func(uint32) bool) {
	ty := jsonv.StrOr(jsonv.Get(o, "type"))
	if ty == "icon_font" {
		o.Set("type", "icon")
	}
	if v, ok := o.Remove("iconFontName"); ok {
		o.Set("icon", v)
	}
	if v, ok := o.Remove("iconFontFamily"); ok {
		o.Set("library", v)
	}
	// 2.11 → 2.12: stroke objects become flat props; a missing align means inner.
	if s := jsonv.Obj(jsonv.Get(o, "stroke")); s != nil && (before(12) || s.Has("thickness") || s.Has("align")) {
		s = jsonv.Obj(jsonv.Clone(s))
		or := func(k string, def jsonv.Value) jsonv.Value {
			if v, ok := s.Get(k); ok {
				return v
			}
			return def
		}
		o.Set("stroke", or("fill", jsonv.NewArray()))
		o.Set("strokeWidth", or("thickness", jsonv.Int(0)))
		o.Set("strokeLinejoin", or("join", "miter"))
		cap := "butt"
		if c := jsonv.StrOr(jsonv.Get(s, "cap")); c == "round" || c == "square" {
			cap = c
		}
		o.Set("strokeLinecap", cap)
		align := "inner"
		switch a, _ := jsonv.Str(jsonv.Get(s, "align")); a {
		case "outside", "outer":
			align = "outer"
		case "center":
			align = "center"
		}
		o.Set("strokeAlignment", align)
	}
	// 2.6 → 2.7: sweep angles flip direction.
	if before(7) && (ty == "ellipse" || ty == "ref") {
		if v, ok := jsonv.F64(jsonv.Get(o, "sweepAngle")); ok {
			o.Set("sweepAngle", jsonv.Float(-v))
		}
	}
	// 2.14 → 2.15: uneven per-side strokes get round joins.
	if before(15) && (ty == "rectangle" || ty == "frame") {
		if w := jsonv.Obj(jsonv.Get(o, "strokeWidth")); w != nil {
			g := func(k string) float64 {
				f, _ := Num(jsonv.Get(w, k))
				return f
			}
			if g("top") != g("right") || g("top") != g("bottom") || g("top") != g("left") {
				o.Set("strokeLinejoin", "round")
			}
		}
	}
	// 2.15 → 2.16: groups with a layout become non-clipping frames; others lose layout props.
	if before(16) && ty == "group" {
		if l := jsonv.StrOr(jsonv.Get(o, "layout")); l == "horizontal" || l == "vertical" {
			o.Set("type", "frame")
			o.Set("clip", false)
		} else {
			for _, k := range []string{"layout", "gap", "padding", "justifyContent", "alignItems", "layoutIncludeStroke"} {
				o.Remove(k)
			}
		}
	}
	// 2.18 → 2.19: inner shadows become outer, spread is gone.
	if before(19) {
		var shadows []jsonv.Value
		switch e := jsonv.Get(o, "effect").(type) {
		case *jsonv.Array:
			shadows = e.Items
		case *jsonv.Object:
			shadows = []jsonv.Value{e}
		}
		for _, e := range shadows {
			eo := jsonv.Obj(e)
			if eo == nil || !jsonv.StrIs(jsonv.Get(eo, "type"), "shadow") {
				continue
			}
			if jsonv.StrIs(jsonv.Get(eo, "shadowType"), "inner") {
				eo.Set("shadowType", "outer")
			}
			eo.Remove("spread")
		}
	}
}
