// Port of usvg 0.47 src/parser/units.rs.

package usvg

import "math"

// convertLength is units::convert_length: length in user units. Percentages are a fraction for
// objectBoundingBox units, otherwise relative to the view box side aid measures (or its diagonal).
func convertLength(length stLength, node svgNode, a aid, objectUnits units, state *converterState) float32 {
	dpi := state.opt.DPI
	n := float32(length.Number)
	switch length.Unit {
	case stLengthUnitNone, stLengthUnitPx:
		return n
	case stLengthUnitEm:
		return float32(n * resolveFontSize(node, state))
	case stLengthUnitEx:
		return float32(n*resolveFontSize(node, state)) / 2
	case stLengthUnitIn:
		return float32(n * dpi)
	case stLengthUnitCm:
		return float32(n*dpi) / 2.54
	case stLengthUnitMm:
		return float32(n*dpi) / 25.4
	case stLengthUnitPt:
		return float32(n*dpi) / 72
	case stLengthUnitPc:
		return float32(n*dpi) / 6
	}
	// stLengthUnitPercent
	if objectUnits == unitsObjectBoundingBox {
		return n / 100
	}
	vb := state.viewBox
	switch a {
	case aidCx, aidDx, aidFx, aidMarkerWidth, aidRefX, aidRx, aidWidth, aidX, aidX1, aidX2:
		return convertPercent(length, vb.Width())
	case aidCy, aidDy, aidFy, aidHeight, aidMarkerHeight, aidRefY, aidRy, aidY, aidY1, aidY2:
		return convertPercent(length, vb.Height())
	}
	w, h := vb.Width(), vb.Height()
	vbLen := float32(w*w) + float32(h*h) // powi(2) is a single multiply
	vbLen = float32(math.Sqrt(float64(vbLen / 2)))
	return convertPercent(length, vbLen)
}

// convertUserLength is units::convert_user_length.
func convertUserLength(length stLength, node svgNode, a aid, state *converterState) float32 {
	return convertLength(length, node, a, unitsUserSpaceOnUse, state)
}

// convertList is units::convert_list: the attribute as a list of user lengths, skipping invalid
// items (the parser ends at the first); ok is false when the attribute is missing or none.
func convertList(node svgNode, a aid, state *converterState) ([]float32, bool) {
	text, ok := node.attrStr(a)
	if !ok {
		return nil, false
	}
	list := []float32{}
	p := stNewLengthListParser(text)
	for {
		l, ok, err := p.Next()
		if !ok {
			break
		}
		if err != nil {
			continue
		}
		list = append(list, convertUserLength(l, node, a, state))
	}
	return list, true
}

func convertPercent(length stLength, base float32) float32 {
	return float32(base*float32(length.Number)) / 100
}

// resolveFontSize is units::resolve_font_size: the font size inherited down to node, starting from
// Options.FontSize at the root.
func resolveFontSize(node svgNode, state *converterState) float32 {
	var nodes []svgNode
	for n := range node.Ancestors() {
		nodes = append(nodes, n)
	}
	fontSize := state.opt.FontSize
	// Reversed, skipping the root.
	for i := len(nodes) - 2; i >= 0; i-- {
		n := nodes[i]
		if length, ok := svgTryAttr(n, aidFontSize, svgParseLength); ok {
			dpi := state.opt.DPI
			v := float32(length.Number)
			switch length.Unit {
			case stLengthUnitNone, stLengthUnitPx:
				fontSize = v
			case stLengthUnitEm:
				fontSize = float32(v * fontSize)
			case stLengthUnitEx:
				fontSize = float32(v*fontSize) / 2
			case stLengthUnitIn:
				fontSize = float32(v * dpi)
			case stLengthUnitCm:
				fontSize = float32(v*dpi) / 2.54
			case stLengthUnitMm:
				fontSize = float32(v*dpi) / 25.4
			case stLengthUnitPt:
				fontSize = float32(v*dpi) / 72
			case stLengthUnitPc:
				fontSize = float32(v*dpi) / 6
			case stLengthUnitPercent:
				// A percentage is relative to the parent's font size.
				fontSize = float32(float32(v*fontSize) * 0.01)
			}
		} else if name, ok := n.attrStr(aidFontSize); ok {
			fontSize = convertNamedFontSize(name, fontSize)
		}
	}
	return fontSize
}

// namedFontSizeScale is 1.2f32.powi(factor) for factors -3..=3, as compiler-rt's __powisf2 computes it
// (repeated f32 multiplication, then a reciprocal for negative factors).
func namedFontSizeScale(factor int) float32 {
	recip := factor < 0
	b := factor
	if recip {
		b = -b
	}
	a, r := float32(1.2), float32(1)
	for {
		if b&1 != 0 {
			r = float32(r * a)
		}
		b /= 2
		if b == 0 {
			break
		}
		a = float32(a * a)
	}
	if recip {
		return 1 / r
	}
	return r
}

// convertNamedFontSize is convert_named_font_size: a 1.2 scale between adjacent keywords, as CSS
// suggests for screens. An unknown name keeps the parent size.
func convertNamedFontSize(name string, parentFontSize float32) float32 {
	factor := 0
	switch name {
	case "xx-small":
		factor = -3
	case "x-small":
		factor = -2
	case "small", "smaller":
		factor = -1
	case "large", "larger":
		factor = 1
	case "x-large":
		factor = 2
	case "xx-large":
		factor = 3
	}
	return float32(parentFontSize * namedFontSizeScale(factor))
}
