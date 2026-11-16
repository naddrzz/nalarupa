package figma

// Conversion of one Figma node and its children to .pen.

import (
	"math"
	"strings"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/libm"
)

// deg is Rust's to_degrees factor.
const deg = 180 / math.Pi

// size is Figma's sizing as .pen sizing.
type size uint8

const (
	fixed size = iota
	fit
	fill
)

// convert turns one node and its children into .pen; parentFlow = placed by its parent's auto layout.
// It returns nil for nodes it skips.
func (d *doc) convert(e *node, up []jsonv.Value, parentFlow bool, depth int) *jsonv.Object {
	if depth > maxDepth || d.budget == 0 {
		return nil
	}
	d.budget--
	if jsonv.StrIs(get(e.p, "type"), "INSTANCE") {
		swapped := real(get(e.p, "overriddenSymbolID"))
		d.bind(e.p, up, get(e.p, "parameterConsumptionMap", "entries"))
		for _, a := range arr(get(e.p, "componentPropRefs")) {
			if truthy(get(a, "defID")) && truthy(get(a, "componentPropNodeField")) {
				entry := jsonv.NewArray(obj("variableField", jsonv.Clone(get(a, "componentPropNodeField")),
					"variableData", obj("value", obj("propRefValue", obj("defId", jsonv.Clone(get(a, "defID")))))))
				d.bind(e.p, up, entry)
			}
		}
		symbol := get(e.p, "symbolData", "symbolID")
		if real(get(e.p, "overriddenSymbolID")) {
			symbol = get(e.p, "overriddenSymbolID")
		}
		if k, ok := key(symbol); ok {
			if l := d.subtree(k, depth+1); l != nil {
				ovr := arr(get(e.p, "symbolData", "symbolOverrides"))
				derived := arr(get(e.p, "derivedSymbolData"))
				chain := with(up, e.p)
				for _, k := range l.kids {
					d.overrides(k, chain, ovr, derived, depth+1)
				}
				e.kids = l.kids
				if !swapped && real(get(e.p, "overriddenSymbolID")) {
					keep := []string{"name", "fillPaints", "strokePaints"}
					vals := make([]jsonv.Value, len(keep))
					for i, k := range keep {
						vals[i] = jsonv.Clone(get(e.p, k))
					}
					e.p = d.swap(e.p, jsonv.Clone(get(e.p, "overriddenSymbolID")))
					for i, k := range keep {
						e.p.Set(k, vals[i])
					}
				}
				e.p.Set("type", "FRAME")
			}
		}
	}
	d.bind(e.p, up, get(e.p, "parameterConsumptionMap", "entries"))
	d.applyStyles(e.p, with(up, scope(e.p)))
	p := e.p
	var ty string
	switch jsonv.StrOr(get(p, "type")) {
	case "SYMBOL", "SECTION", "FRAME":
		ty = "frame"
		if truthy(get(p, "resizeToFit")) {
			ty = "group"
		}
	case "BOOLEAN_OPERATION":
		ty = "group"
	case "VECTOR":
		ty = "path"
	case "TEXT":
		ty = "text"
	case "LINE":
		ty = "line"
	case "ELLIPSE":
		ty = "ellipse"
	case "REGULAR_POLYGON":
		ty = "polygon"
	case "RECTANGLE", "ROUNDED_RECTANGLE":
		ty = "rectangle"
	default:
		return nil
	}
	layout := ""
	switch jsonv.StrOr(get(p, "stackMode")) {
	case "HORIZONTAL":
		layout = "horizontal"
	case "VERTICAL":
		layout = "vertical"
	}
	absolute := jsonv.StrIs(get(p, "stackPositioning"), "ABSOLUTE")
	flow := parentFlow && !absolute
	parentDir, hasParentDir := "", false
	if len(up) > 0 {
		parentDir, hasParentDir = jsonv.Str(get(up[len(up)-1], "stackMode"))
	}
	hs, vs := sizing(p, parentDir, hasParentDir, true), sizing(p, parentDir, hasParentDir, false)
	w, h := numOr(get(p, "size", "x"), 0), numOr(get(p, "size", "y"), 0)

	o := jsonv.NewObject()
	o.Set("type", ty)
	if n, ok := jsonv.Str(get(p, "name")); ok && n != "" {
		o.Set("name", n)
	}
	if absolute {
		o.Set("layoutPosition", "absolute")
	}
	if jsonv.IsFalse(get(p, "visible")) {
		o.Set("enabled", false)
	}
	if op, ok := num(get(p, "opacity")); ok && op != 1 {
		o.Set("opacity", jsonv.Float(op))
	}
	if t := get(p, "transform"); isObj(t) {
		m := func(k string) float64 { return numOr(get(t, k), 0) }
		m00, m01, m02, m10, m11, m12 := m("m00"), m("m01"), m("m02"), m("m10"), m("m11"), m("m12")
		// Rotation from the first column, flips where the matrix disagrees.
		mirrored := float64(m00*m11)-float64(m10*m01) < 0
		sx, sy := math.Sqrt(float64(m00*m00)+float64(m10*m10)), math.Sqrt(float64(m01*m01)+float64(m11*m11))
		rot := libm.Atan2(m10, m00)
		if mirrored {
			rot = libm.Atan2(-m10, -m00)
		}
		c, s := libm.Cos(rot), libm.Sin(rot)
		if !flow {
			o.Set("x", jsonv.Float(m02))
			o.Set("y", jsonv.Float(m12))
		}
		if rot != 0 {
			o.Set("rotation", jsonv.Float(-(rot * deg)))
		}
		if math.Abs(m00-float64(sx*c)) > 1e-6 || math.Abs(m10-float64(sx*s)) > 1e-6 {
			o.Set("flipX", true)
		}
		if math.Abs(m01+float64(sy*s)) > 1e-6 || math.Abs(m11-float64(sy*c)) > 1e-6 {
			o.Set("flipY", true)
		}
	}

	// Children first: a fit_content frame needs to know whether it hugs anything.
	var kids []jsonv.Value
	chain := with(up, p)
	childFlow := ty == "frame" && layout != ""
	for _, k := range e.kids {
		if v := d.convert(k, chain, childFlow, depth+1); v != nil {
			kids = append(kids, v)
		}
	}
	hugs := func(axis string) bool {
		if layout == "" {
			return false
		}
		for _, k := range kids {
			s, isStr := jsonv.Str(get(k, axis))
			if !jsonv.StrIs(get(k, "layoutPosition"), "absolute") && !(isStr && strings.HasPrefix(s, "fill_container")) {
				return true
			}
		}
		return false
	}
	dim := func(s size, v float64, axis string, defaultFit bool) (jsonv.Value, bool) {
		switch {
		case s == fixed:
			return jsonv.Float(v), true
		case s == fit && hugs(axis):
			return "fit_content", !defaultFit
		case s == fit:
			return "fit_content(" + rf(v) + ")", true
		case flow:
			return "fill_container", true
		}
		return "fill_container(" + rf(v) + ")", true
	}
	mask := truthy(get(p, "mask"))
	var fills, strokes []jsonv.Value
	if !mask {
		fills = paints(get(p, "fillPaints"))
	}
	// Text keeps no stroke (only frames, vectors and shapes are stroked).
	if !mask && ty != "text" {
		strokes = paints(get(p, "strokePaints"))
	}
	one := func(v []jsonv.Value) jsonv.Value {
		if len(v) == 1 {
			return v[0]
		}
		return jsonv.NewArray(v...)
	}

	switch ty {
	case "frame":
		if !jsonv.IsTrue(get(p, "frameMaskDisabled")) {
			o.Set("clip", true)
		}
		if v, ok := dim(hs, w, "width", true); ok {
			o.Set("width", v)
		}
		if v, ok := dim(vs, h, "height", true); ok {
			o.Set("height", v)
		}
	case "group":
		if hs == fill && flow {
			o.Set("width", "fill_container")
		}
		if vs == fill && flow {
			o.Set("height", "fill_container")
		}
	case "text":
	default:
		if v, ok := dim(hs, w, "width", false); ok {
			o.Set("width", v)
		}
		if v, ok := dim(vs, h, "height", false); ok {
			o.Set("height", v)
		}
	}
	switch ty {
	case "rectangle", "frame":
		if r := radius(p); r != nil {
			o.Set("cornerRadius", r)
		}
	case "ellipse":
		a := get(p, "arcData")
		if r, ok := num(get(a, "innerRadius")); ok && r != 0 {
			o.Set("innerRadius", jsonv.Float(r))
		}
		start := numOr(get(a, "startingAngle"), 0) * deg
		if start != 0 {
			o.Set("startAngle", jsonv.Float(-start))
		}
		if end, ok := num(get(a, "endingAngle")); ok {
			sweep := start - float64(end*deg)
			if math.Abs(sweep) != 360 {
				o.Set("sweepAngle", jsonv.Float(sweep))
			}
		}
	case "polygon":
		if c, ok := num(get(p, "count")); ok {
			o.Set("polygonCount", jsonv.Float(c))
		}
		if r, ok := num(get(p, "cornerRadius")); ok && r != 0 {
			o.Set("cornerRadius", jsonv.Float(r))
		}
	case "path":
		if dd, rule, ok := d.vector(p); ok {
			o.Set("geometry", dd)
			if rule == "evenodd" {
				o.Set("fillRule", rule)
			}
		}
	case "text":
		text(p, o, hs, vs, w, h, flow)
	}
	if ty != "group" && len(fills) > 0 {
		o.Set("fill", one(fills))
	}
	if ty != "group" && len(strokes) > 0 {
		o.Set("stroke", one(strokes))
		wt := func(k string) float64 { return numOr(get(p, k), 0) }
		var ws [4]float64
		has := false
		if truthy(get(p, "borderStrokeWeightsIndependent")) {
			ws, has = [4]float64{wt("borderTopWeight"), wt("borderRightWeight"), wt("borderBottomWeight"), wt("borderLeftWeight")}, true
		} else if v, ok := num(get(p, "strokeWeight")); ok && v != 0 {
			ws, has = [4]float64{v, v, v, v}, true
		}
		if has {
			if ws[0] == ws[1] && ws[0] == ws[2] && ws[0] == ws[3] {
				o.Set("strokeWidth", jsonv.Float(ws[0]))
			} else {
				m := jsonv.NewObject()
				for i, k := range []string{"top", "right", "bottom", "left"} {
					if ws[i] != 0 {
						m.Set(k, jsonv.Float(ws[i]))
					}
				}
				o.Set("strokeWidth", m)
			}
		}
		switch jsonv.StrOr(get(p, "strokeAlign")) {
		case "INSIDE":
			o.Set("strokeAlignment", "inner")
		case "OUTSIDE":
			o.Set("strokeAlignment", "outer")
		}
		switch jsonv.StrOr(get(p, "strokeJoin")) {
		case "BEVEL":
			o.Set("strokeLinejoin", "bevel")
		case "ROUND":
			o.Set("strokeLinejoin", "round")
		}
		switch jsonv.StrOr(get(p, "strokeCap")) {
		case "ROUND":
			o.Set("strokeLinecap", "round")
		case "SQUARE":
			o.Set("strokeLinecap", "square")
		}
	}
	if fx := effects(get(p, "effects")); len(fx) > 0 {
		o.Set("effect", one(fx))
	}
	if ty == "frame" {
		if layout == "" {
			o.Set("layout", "none")
		} else {
			if layout == "vertical" {
				o.Set("layout", layout)
			}
			if g, ok := num(get(p, "stackSpacing")); ok && g != 0 {
				o.Set("gap", jsonv.Float(g))
			}
			var pad [4]float64
			for i, k := range []string{"stackVerticalPadding", "stackPaddingRight", "stackPaddingBottom", "stackHorizontalPadding"} {
				pad[i] = numOr(get(p, k), 0)
			}
			if pad != [4]float64{} {
				switch {
				case pad[0] == pad[1] && pad[0] == pad[2] && pad[0] == pad[3]:
					o.Set("padding", jsonv.Float(pad[0]))
				case pad[0] == pad[2] && pad[1] == pad[3]:
					o.Set("padding", jsonv.NewArray(jsonv.Float(pad[0]), jsonv.Float(pad[1])))
				default:
					o.Set("padding", jsonv.NewArray(jsonv.Float(pad[0]), jsonv.Float(pad[1]), jsonv.Float(pad[2]), jsonv.Float(pad[3])))
				}
			}
			switch jsonv.StrOr(get(p, "stackPrimaryAlignItems")) {
			case "CENTER":
				o.Set("justifyContent", "center")
			case "MAX":
				o.Set("justifyContent", "end")
			case "SPACE_BETWEEN", "SPACE_EVENLY":
				o.Set("justifyContent", "space_between")
			}
			switch jsonv.StrOr(get(p, "stackCounterAlignItems")) {
			case "CENTER":
				o.Set("alignItems", "center")
			case "MAX":
				o.Set("alignItems", "end")
			}
			if truthy(get(p, "bordersTakeSpace")) {
				o.Set("layoutIncludeStroke", true)
			}
		}
	}
	if (ty == "frame" || ty == "group") && len(kids) > 0 {
		o.Set("children", jsonv.NewArray(kids...))
	}
	return o
}

// sizing is fill when stretched/grown in the parent's auto layout, hug when the node's own auto layout
// sizes that axis, else fixed. parent is the parent's stack direction.
func sizing(p jsonv.Value, parent string, hasParent, horizontal bool) size {
	along, across := "VERTICAL", "HORIZONTAL"
	if horizontal {
		along, across = across, along
	}
	own, hasOwn := jsonv.Str(get(p, "stackMode"))
	grow := truthy(get(p, "stackChildPrimaryGrow"))
	switch {
	case (hasParent && parent == across && jsonv.StrIs(get(p, "stackChildAlignSelf"), "STRETCH")) || (hasParent && parent == along && grow):
		return fill
	case hasOwn && own == across && jsonv.StrIs(get(p, "stackCounterSizing"), "RESIZE_TO_FIT_WITH_IMPLICIT_SIZE"):
		return fit
	case hasOwn && own == along && jsonv.StrIs(get(p, "stackPrimarySizing"), "FIXED"):
		return fixed
	case hasOwn && own == along:
		return fit
	}
	return fixed
}

// radius is [tl, tr, br, bl]: per-corner radii when any is set, else the uniform one; one number when
// equal; nil when there is none.
func radius(p jsonv.Value) jsonv.Value {
	var c [4]float64
	for i, k := range []string{"rectangleTopLeftCornerRadius", "rectangleTopRightCornerRadius", "rectangleBottomRightCornerRadius", "rectangleBottomLeftCornerRadius"} {
		c[i] = numOr(get(p, k), 0)
	}
	r := c
	if c == [4]float64{} {
		u := numOr(get(p, "cornerRadius"), 0)
		r = [4]float64{u, u, u, u}
	}
	switch {
	case r[0] == 0 && r[1] == 0 && r[2] == 0 && r[3] == 0:
		return nil
	case r[0] == r[1] && r[0] == r[2] && r[0] == r[3]:
		return jsonv.Float(r[0])
	}
	return jsonv.NewArray(jsonv.Float(r[0]), jsonv.Float(r[1]), jsonv.Float(r[2]), jsonv.Float(r[3]))
}
