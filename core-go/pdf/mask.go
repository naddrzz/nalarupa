package pdf

// Form XObjects, transparency groups, soft masks and tiling patterns (krilla's graphics/xobject.rs,
// mask.rs and tiling_pattern.rs).

import (
	"github.com/naddrzz/nalarupa/core-go/skia"
)

// xobject is a form XObject from a finished stream: a transparency group when isolated or when it needs
// a group color space (masks always do, as does any stream that invokes a soft mask).
type xobject struct {
	stream     *stream
	isolated   bool
	groupCS    bool
	customBBox *skia.Rect
}

func newXObject(s *stream, isolated, groupCS bool, customBBox *skia.Rect) *xobject {
	return &xobject{stream: s, isolated: isolated, groupCS: groupCS || s.usesMask, customBBox: customBBox}
}

func (x *xobject) bbox() skia.Rect {
	if x.customBBox != nil {
		return *x.customBBox
	}
	return x.stream.bbox
}

func rectArray(r skia.Rect) string {
	return "[" + nums(r.Left(), r.Top(), r.Right(), r.Bottom()) + "]"
}

// xobject writes the form.
func (d *document) xobject(x *xobject) ref {
	r := d.w.alloc()
	dict := "/Type /XObject /Subtype /Form /Resources " + x.stream.res.dict() + " /BBox " + rectArray(x.bbox())
	if x.isolated || x.groupCS {
		dict += " /Group <</Type /Group /S /Transparency"
		if x.isolated {
			dict += " /I true"
		}
		dict += " /CS /DeviceRGB>>"
	}
	d.w.stream(r, dict, x.stream.content, true)
	return r
}

// mask is a soft mask: the luminosity or alpha of a stream drawn as a transparency group.
type mask struct {
	stream     *stream
	luminosity bool
	bbox       *skia.Rect // the shading masks of gradients use the shape's box
}

func (d *document) mask(m *mask) ref {
	x := d.xobject(newXObject(m.stream, false, true, m.bbox))
	s := "/Alpha"
	if m.luminosity {
		s = "/Luminosity"
	}
	r := d.w.alloc()
	d.w.obj(r, "<</Type /Mask /S "+s+" /G "+x.String()+">>")
	return r
}

// extGState writes a graphics state parameter dictionary once per distinct state.
func (d *document) extGState(e extGState) ref {
	key := e.dict()
	if r, ok := d.extGStates[key]; ok {
		return r
	}
	r := d.w.alloc()
	d.w.obj(r, key)
	d.extGStates[key] = r
	return r
}

// tilingPattern is TilingPattern: a colored pattern cell of w×h with the given matrix. Viewers apply
// fill and stroke opacity to patterns inconsistently, so the cell carries the opacity itself.
func (d *document) tilingPattern(s *stream, ts skia.Transform, opacity float32, w, h float32) ref {
	if opacity != 1 {
		sub := newSurface(d, newBuilder(d, skia.Identity(), true))
		sub.drawOpacifiedStream(opacity, s)
		s = sub.finish()
	}
	r := d.w.alloc()
	bbox := "[0 0 " + nums(w, h) + "]"
	d.w.stream(r, "/Type /Pattern /PatternType 1 /PaintType 1 /TilingType 1 /Resources "+s.res.dict()+
		" /BBox "+bbox+" /Matrix ["+nums(ts.SX, ts.KY, ts.KX, ts.SY, ts.TX, ts.TY)+"] /XStep "+num(w)+" /YStep "+num(h), s.content, true)
	return r
}
