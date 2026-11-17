package figma

import (
	"strings"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

// weights map a style name to a weight, first match wins (so "SemiBold" is not "Bold").
var weights = [][2]string{{"Thin", "100"}, {"ExtraLight", "200"}, {"Extra Light", "200"}, {"Light", "300"}, {"Regular", "normal"},
	{"SemiBold", "600"}, {"Semi Bold", "600"}, {"ExtraBold", "800"}, {"Extra Bold", "800"}, {"ExtraBlack", "950"}, {"Extra Black", "950"},
	{"Black", "900"}, {"Bold", "700"}, {"Medium", "500"}}

func text(p jsonv.Value, o *jsonv.Object, hs, vs size, w, h float64, flow bool) {
	sz := func(s size, v float64) jsonv.Value {
		switch {
		case s == fill && flow:
			return "fill_container"
		case s == fill:
			return "fill_container(" + rf(v) + ")"
		case s == fit:
			return "fit_content(" + rf(v) + ")"
		}
		return jsonv.Float(v)
	}
	resize, ok := jsonv.Str(get(p, "textAutoResize"))
	if !ok {
		resize = "NONE"
	}
	switch resize {
	case "WIDTH_AND_HEIGHT":
	case "HEIGHT":
		o.Set("textGrowth", "fixed-width")
		o.Set("width", sz(hs, w))
	default:
		o.Set("textGrowth", "fixed-width-height")
		o.Set("width", sz(hs, w))
		o.Set("height", sz(vs, h))
	}
	if c, ok := jsonv.Str(get(p, "textData", "characters")); ok && c != "" {
		o.Set("content", c)
	}
	fs, hasFs := num(get(p, "fontSize"))
	fsOr := func(d float64) float64 {
		if hasFs {
			return fs
		}
		return d
	}
	lh := get(p, "lineHeight")
	line := 0.0
	units, _ := jsonv.Str(get(lh, "units"))
	if v, ok := num(get(lh, "value")); ok {
		switch units {
		case "RAW":
			line = v
		case "PIXELS":
			line = v / fsOr(1)
		}
	}
	if line != 0 {
		o.Set("lineHeight", jsonv.Float(line))
	}
	switch jsonv.StrOr(get(p, "textAlignHorizontal")) {
	case "CENTER":
		o.Set("textAlign", "center")
	case "RIGHT":
		o.Set("textAlign", "right")
	case "JUSTIFIED":
		o.Set("textAlign", "justify")
	}
	switch jsonv.StrOr(get(p, "textAlignVertical")) {
	case "CENTER":
		o.Set("textAlignVertical", "middle")
	case "BOTTOM":
		o.Set("textAlignVertical", "bottom")
	}
	family, isFamily := jsonv.Str(get(p, "fontName", "family"))
	if !isFamily {
		family = "Inter"
	}
	o.Set("fontFamily", family)
	o.Set("fontSize", jsonv.Float(fsOr(14)))
	style := jsonv.StrOr(get(p, "fontName", "style"))
	weight := "normal"
	if isFamily {
		for _, w := range weights {
			if strings.Contains(style, w[0]) {
				weight = w[1]
				break
			}
		}
	}
	o.Set("fontWeight", weight)
	ls := get(p, "letterSpacing")
	spacing := 0.0
	lsUnits, _ := jsonv.Str(get(ls, "units"))
	if v, ok := num(get(ls, "value")); ok {
		switch lsUnits {
		case "RAW", "PIXELS":
			spacing = v
		case "PERCENT":
			spacing = v / 100 * fsOr(1)
		}
	}
	if spacing != 0 {
		o.Set("letterSpacing", jsonv.Float(spacing))
	}
	if strings.Contains(style, "Italic") {
		o.Set("fontStyle", "italic")
	}
}
