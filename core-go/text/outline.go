package text

// Glyph outlines as ttf-parser 0.25 produces them for usvg's text flattening: `glyf` (simple and composite
// glyphs, with the implied on-curve midpoints and the closing line ttf-parser emits) and `gvar` variations,
// ported from tables/glyf.rs and tables/gvar.rs so the points match bit for bit. CFF outlines are ported too
// (cff.go); CFF2 outlines come from go-text, which computes charstrings in f64 where ttf-parser uses f32.
//
// Floating-point products are wrapped in float32 conversions, so the Go compiler never fuses them into a
// multiply-add.

import (
	"encoding/binary"
	"sync"

	"github.com/go-text/typesetting/font"
	ot "github.com/go-text/typesetting/font/opentype"
	"github.com/go-text/typesetting/font/opentype/tables"
)

// OutlineBuilder receives a glyph outline (ttf_parser::OutlineBuilder); *skia.PathBuilder implements it.
type OutlineBuilder interface {
	MoveTo(x, y float32)
	LineTo(x, y float32)
	QuadTo(x1, y1, x, y float32)
	CubicTo(x1, y1, x2, y2, x, y float32)
	Close()
}

// ---- byte stream (ttf_parser::Stream) ----

type stream struct {
	data   []byte
	offset int
}

func newStreamAt(data []byte, offset int) (stream, bool) {
	if offset <= len(data) {
		return stream{data, offset}, true
	}
	return stream{}, false
}

func (s *stream) readBytes(n int) ([]byte, bool) {
	if n < 0 || s.offset+n > len(s.data) {
		return nil, false
	}
	v := s.data[s.offset : s.offset+n]
	s.offset += n
	return v, true
}

func (s *stream) u8() (uint8, bool) {
	b, ok := s.readBytes(1)
	if !ok {
		return 0, false
	}
	return b[0], true
}

func (s *stream) i8() (int8, bool) {
	v, ok := s.u8()
	return int8(v), ok
}

func (s *stream) u16() (uint16, bool) {
	b, ok := s.readBytes(2)
	if !ok {
		return 0, false
	}
	return binary.BigEndian.Uint16(b), true
}

func (s *stream) i16() (int16, bool) {
	v, ok := s.u16()
	return int16(v), ok
}

func (s *stream) u32() (uint32, bool) {
	b, ok := s.readBytes(4)
	if !ok {
		return 0, false
	}
	return binary.BigEndian.Uint32(b), true
}

func (s *stream) tail() ([]byte, bool) {
	if s.offset > len(s.data) {
		return nil, false
	}
	return s.data[s.offset:], true
}

// f2dot14 is F2DOT14::to_f32.
func f2dot14(v int16) float32 { return float32(v) / 16384 }

// tryI16 is ttf-parser's i16::try_num_from(f32): truncation toward zero, inside the i16 range.
func tryI16(v float32) (int16, bool) {
	if !(v > -2147483904 && v < 2147483648) { // i32::try_num_from, NaN included
		return 0, false
	}
	i := int32(v)
	if i < -32768 || i > 32767 {
		return 0, false
	}
	return int16(i), true
}

// ---- transform and bbox (ttf_parser::Transform, RectF) ----

type glyphTransform struct{ a, b, c, d, e, f float32 }

var identityGlyphTransform = glyphTransform{a: 1, d: 1}

func (t glyphTransform) isDefault() bool { return t == identityGlyphTransform }

func combineTransforms(t1, t2 glyphTransform) glyphTransform {
	return glyphTransform{
		a: float32(t1.a*t2.a) + float32(t1.c*t2.b),
		b: float32(t1.b*t2.a) + float32(t1.d*t2.b),
		c: float32(t1.a*t2.c) + float32(t1.c*t2.d),
		d: float32(t1.b*t2.c) + float32(t1.d*t2.d),
		e: float32(t1.a*t2.e) + float32(t1.c*t2.f) + t1.e,
		f: float32(t1.b*t2.e) + float32(t1.d*t2.f) + t1.f,
	}
}

func (t glyphTransform) apply(x, y float32) (float32, float32) {
	return float32(t.a*x) + float32(t.c*y) + t.e, float32(t.b*x) + float32(t.d*y) + t.f
}

const (
	f32Max = 3.4028234663852886e+38
)

type rectF struct{ xMin, yMin, xMax, yMax float32 }

func newRectF() rectF { return rectF{f32Max, f32Max, -f32Max, -f32Max} }

func (r rectF) isDefault() bool {
	return r.xMin == f32Max && r.yMin == f32Max && r.xMax == -f32Max && r.yMax == -f32Max
}

// f32Min and f32Max2 are f32::min and f32::max: a NaN operand gives the other one.
func f32Min(a, b float32) float32 {
	if a != a || b < a {
		return b
	}
	return a
}

func f32Max2(a, b float32) float32 {
	if a != a || b > a {
		return b
	}
	return a
}

func (r *rectF) extend(x, y float32) {
	r.xMin = f32Min(r.xMin, x)
	r.yMin = f32Min(r.yMin, y)
	r.xMax = f32Max2(r.xMax, x)
	r.yMax = f32Max2(r.yMax, y)
}

// toRect is RectF::to_rect: false when a side does not fit an i16.
func (r rectF) toRect() bool {
	_, ok1 := tryI16(r.xMin)
	_, ok2 := tryI16(r.yMin)
	_, ok3 := tryI16(r.xMax)
	_, ok4 := tryI16(r.yMax)
	return ok1 && ok2 && ok3 && ok4
}

// ---- glyf outline builder (glyf::Builder) ----

type glyfPoint struct{ x, y float32 }

func (p glyfPoint) lerp(o glyfPoint, t float32) glyfPoint {
	return glyfPoint{p.x + float32(t*(o.x-p.x)), p.y + float32(t*(o.y-p.y))}
}

type glyfBuilder struct {
	b                OutlineBuilder
	transform        glyphTransform
	isDefaultTS      bool
	bbox             rectF
	firstOnCurve     glyfPoint
	hasFirstOnCurve  bool
	firstOffCurve    glyfPoint
	hasFirstOffCurve bool
	lastOffCurve     glyfPoint
	hasLastOffCurve  bool
}

func newGlyfBuilder(ts glyphTransform, bbox rectF, b OutlineBuilder) *glyfBuilder {
	return &glyfBuilder{b: b, transform: ts, isDefaultTS: ts.isDefault(), bbox: bbox}
}

func (g *glyfBuilder) moveTo(x, y float32) {
	if !g.isDefaultTS {
		x, y = g.transform.apply(x, y)
	}
	g.bbox.extend(x, y)
	g.b.MoveTo(x, y)
}

func (g *glyfBuilder) lineTo(x, y float32) {
	if !g.isDefaultTS {
		x, y = g.transform.apply(x, y)
	}
	g.bbox.extend(x, y)
	g.b.LineTo(x, y)
}

func (g *glyfBuilder) quadTo(x1, y1, x, y float32) {
	if !g.isDefaultTS {
		x1, y1 = g.transform.apply(x1, y1)
		x, y = g.transform.apply(x, y)
	}
	g.bbox.extend(x1, y1)
	g.bbox.extend(x, y)
	g.b.QuadTo(x1, y1, x, y)
}

func (g *glyfBuilder) pushPoint(x, y float32, onCurve, last bool) {
	p := glyfPoint{x, y}
	if !g.hasFirstOnCurve {
		if onCurve {
			g.firstOnCurve, g.hasFirstOnCurve = p, true
			g.moveTo(p.x, p.y)
		} else if g.hasFirstOffCurve {
			mid := g.firstOffCurve.lerp(p, 0.5)
			g.firstOnCurve, g.hasFirstOnCurve = mid, true
			g.lastOffCurve, g.hasLastOffCurve = p, true
			g.moveTo(mid.x, mid.y)
		} else {
			g.firstOffCurve, g.hasFirstOffCurve = p, true
		}
	} else {
		switch {
		case g.hasLastOffCurve && onCurve:
			g.hasLastOffCurve = false
			g.quadTo(g.lastOffCurve.x, g.lastOffCurve.y, p.x, p.y)
		case g.hasLastOffCurve:
			off := g.lastOffCurve
			g.lastOffCurve = p
			mid := off.lerp(p, 0.5)
			g.quadTo(off.x, off.y, mid.x, mid.y)
		case onCurve:
			g.lineTo(p.x, p.y)
		default:
			g.lastOffCurve, g.hasLastOffCurve = p, true
		}
	}
	if last {
		g.finishContour()
	}
}

func (g *glyfBuilder) finishContour() {
	if g.hasFirstOffCurve && g.hasLastOffCurve {
		g.hasLastOffCurve = false
		mid := g.lastOffCurve.lerp(g.firstOffCurve, 0.5)
		g.quadTo(g.lastOffCurve.x, g.lastOffCurve.y, mid.x, mid.y)
	}
	switch {
	case g.hasFirstOnCurve && g.hasFirstOffCurve:
		g.quadTo(g.firstOffCurve.x, g.firstOffCurve.y, g.firstOnCurve.x, g.firstOnCurve.y)
	case g.hasFirstOnCurve && g.hasLastOffCurve:
		g.quadTo(g.lastOffCurve.x, g.lastOffCurve.y, g.firstOnCurve.x, g.firstOnCurve.y)
	case g.hasFirstOnCurve:
		g.lineTo(g.firstOnCurve.x, g.firstOnCurve.y)
	}
	g.hasFirstOnCurve, g.hasFirstOffCurve, g.hasLastOffCurve = false, false, false
	g.b.Close()
}

// ---- composite glyphs (glyf::CompositeGlyphIter) ----

type compositeInfo struct {
	glyphID   uint16
	transform glyphTransform
	argsAreXY bool
}

// compositeGlyphs parses the component records, stopping where the stream runs out or after the
// component without MORE_COMPONENTS.
func compositeGlyphs(data []byte) []compositeInfo {
	var out []compositeInfo
	s := stream{data: data}
	for {
		flags, ok := s.u16()
		if !ok {
			return out
		}
		gid, ok := s.u16()
		if !ok {
			return out
		}
		ts := identityGlyphTransform
		if flags&0x0002 != 0 {
			if flags&0x0001 != 0 {
				e, ok1 := s.i16()
				f, ok2 := s.i16()
				if !ok1 || !ok2 {
					return out
				}
				ts.e, ts.f = float32(e), float32(f)
			} else {
				e, ok1 := s.i8()
				f, ok2 := s.i8()
				if !ok1 || !ok2 {
					return out
				}
				ts.e, ts.f = float32(e), float32(f)
			}
		}
		read := func() (float32, bool) {
			v, ok := s.i16()
			return f2dot14(v), ok
		}
		switch {
		case flags&0x0080 != 0:
			a, ok1 := read()
			b, ok2 := read()
			c, ok3 := read()
			d, ok4 := read()
			if !ok1 || !ok2 || !ok3 || !ok4 {
				return out
			}
			ts.a, ts.b, ts.c, ts.d = a, b, c, d
		case flags&0x0040 != 0:
			a, ok1 := read()
			d, ok2 := read()
			if !ok1 || !ok2 {
				return out
			}
			ts.a, ts.d = a, d
		case flags&0x0008 != 0:
			a, ok := read()
			if !ok {
				return out
			}
			ts.a, ts.d = a, a
		}
		out = append(out, compositeInfo{glyphID: gid, transform: ts, argsAreXY: flags&0x0002 != 0})
		if flags&0x0020 == 0 {
			return out
		}
	}
}

// ---- simple glyph points (glyf::GlyphPointsIter) ----

type glyphPoint struct {
	x, y          int16
	onCurve, last bool
}

type endpointsIter struct {
	endpoints []byte // u16 array
	index     uint16
	left      uint16
}

func (e *endpointsIter) get(i uint16) (uint16, bool) {
	if int(i)*2+2 > len(e.endpoints) {
		return 0, false
	}
	return binary.BigEndian.Uint16(e.endpoints[int(i)*2:]), true
}

func (e *endpointsIter) next() bool {
	if e.left == 0 {
		if end, ok := e.get(e.index); ok {
			prev, ok := e.get(e.index - 1)
			if !ok {
				prev = 0
			}
			if end > prev {
				e.left = end - prev
			} else {
				e.left = 0
			}
			if e.left > 0 {
				e.left--
			}
		}
		if e.index != 0xFFFF {
			e.index++
		}
		return true
	}
	e.left--
	return false
}

type flagsIter struct {
	s       stream
	repeats uint8
	flags   uint8
}

func (f *flagsIter) next() uint8 {
	if f.repeats == 0 {
		v, ok := f.s.u8()
		if !ok {
			v = 0
		}
		f.flags = v
		if f.flags&0x08 != 0 {
			r, ok := f.s.u8()
			if !ok {
				r = 0
			}
			f.repeats = r
		}
	} else {
		f.repeats--
	}
	return f.flags
}

type coordsIter struct {
	s    stream
	prev int16
}

func (c *coordsIter) next(short, sameOrShort bool) int16 {
	var n int16
	if short {
		v, ok := c.s.u8()
		if !ok {
			v = 0
		}
		n = int16(v)
		if !sameOrShort {
			n = -n
		}
	} else if !sameOrShort {
		v, ok := c.s.i16()
		if !ok {
			v = 0
		}
		n = v
	}
	c.prev += n // wrapping_add
	return c.prev
}

type glyphPointsIter struct {
	endpoints  endpointsIter
	flags      flagsIter
	xCoords    coordsIter
	yCoords    coordsIter
	pointsLeft uint16
}

func (g *glyphPointsIter) currentContour() uint16 { return g.endpoints.index - 1 }

func (g *glyphPointsIter) next() (glyphPoint, bool) {
	if g.pointsLeft == 0 {
		return glyphPoint{}, false
	}
	g.pointsLeft--
	last := g.endpoints.next()
	flags := g.flags.next()
	return glyphPoint{
		x:       g.xCoords.next(flags&0x02 != 0, flags&0x10 != 0),
		y:       g.yCoords.next(flags&0x04 != 0, flags&0x20 != 0),
		onCurve: flags&0x01 != 0,
		last:    last,
	}, true
}

// parseSimpleOutline is glyf::parse_simple_outline.
func parseSimpleOutline(data []byte, contours uint16) (glyphPointsIter, bool) {
	s := stream{data: data}
	endpoints, ok := s.readBytes(int(contours) * 2)
	if !ok || contours == 0 {
		return glyphPointsIter{}, false
	}
	lastEnd := binary.BigEndian.Uint16(endpoints[len(endpoints)-2:])
	if lastEnd == 0xFFFF {
		return glyphPointsIter{}, false
	}
	total := lastEnd + 1
	if total == 1 {
		return glyphPointsIter{}, true // a single point is ignored, but not an error
	}
	insLen, ok := s.u16()
	if !ok {
		return glyphPointsIter{}, false
	}
	s.offset += int(insLen)
	flagsOff := s.offset
	xLen, yLen, ok := resolveCoordsLen(&s, total)
	if !ok {
		return glyphPointsIter{}, false
	}
	xOff := s.offset
	yOff := xOff + int(xLen)
	yEnd := yOff + int(yLen)
	if flagsOff > xOff || xOff > len(data) || yOff > len(data) || yEnd > len(data) {
		return glyphPointsIter{}, false
	}
	first := binary.BigEndian.Uint16(endpoints)
	return glyphPointsIter{
		endpoints:  endpointsIter{endpoints: endpoints, index: 1, left: first},
		flags:      flagsIter{s: stream{data: data[flagsOff:xOff]}},
		xCoords:    coordsIter{s: stream{data: data[xOff:yOff]}},
		yCoords:    coordsIter{s: stream{data: data[yOff:yEnd]}},
		pointsLeft: total,
	}, true
}

func resolveCoordsLen(s *stream, total uint16) (uint32, uint32, bool) {
	left := uint32(total)
	var xLen, yLen uint32
	for left > 0 {
		flags, ok := s.u8()
		if !ok {
			return 0, 0, false
		}
		repeats := uint32(1)
		if flags&0x08 != 0 {
			r, ok := s.u8()
			if !ok {
				return 0, 0, false
			}
			repeats = uint32(r) + 1
		}
		if repeats > left {
			return 0, 0, false
		}
		if flags&0x02 != 0 {
			xLen += repeats
		}
		if flags&(0x02|0x10) == 0 {
			xLen += repeats * 2
		}
		if flags&0x04 != 0 {
			yLen += repeats
		}
		if flags&(0x04|0x20) == 0 {
			yLen += repeats * 2
		}
		left -= repeats
	}
	return xLen, yLen, true
}

// ---- loca and glyf ----

type locaTable struct {
	data []byte
	long bool
	n    uint16 // offsets
}

func parseLoca(numGlyphs uint16, long bool, data []byte) (locaTable, bool) {
	total := numGlyphs
	if numGlyphs != 0xFFFF {
		total++
	}
	actual := len(data) / 2
	if long {
		actual = len(data) / 4
	}
	if actual > 0xFFFF {
		return locaTable{}, false
	}
	if uint16(actual) < total {
		total = uint16(actual)
	}
	return locaTable{data: data, long: long, n: total}, true
}

func (l locaTable) get(i uint16) uint32 {
	if l.long {
		return binary.BigEndian.Uint32(l.data[int(i)*4:])
	}
	return uint32(binary.BigEndian.Uint16(l.data[int(i)*2:])) * 2
}

// glyphRange is loca::Table::glyph_range.
func (l locaTable) glyphRange(gid uint16) (int, int, bool) {
	if gid == 0xFFFF || gid+1 >= l.n {
		return 0, 0, false
	}
	start, end := l.get(gid), l.get(gid+1)
	if start >= end {
		return 0, 0, false
	}
	return int(start), int(end), true
}

type glyfTable struct {
	loca locaTable
	data []byte
}

func (g glyfTable) get(gid uint16) ([]byte, bool) {
	start, end, ok := g.loca.glyphRange(gid)
	if !ok || end > len(g.data) {
		return nil, false
	}
	return g.data[start:end], true
}

const maxComponents = 32

// outlineImpl is glyf::outline_impl. ok false is None; the inner Option<Rect> is not needed here.
func (g glyfTable) outlineImpl(data []byte, depth uint8, b *glyfBuilder) bool {
	if depth >= maxComponents {
		return false
	}
	s := stream{data: data}
	contours, ok := s.i16()
	if !ok {
		return false
	}
	s.offset += 8
	if contours > 0 {
		tail, ok := s.tail()
		if !ok {
			return false
		}
		points, ok := parseSimpleOutline(tail, uint16(contours))
		if !ok {
			return false
		}
		for {
			p, ok := points.next()
			if !ok {
				break
			}
			b.pushPoint(float32(p.x), float32(p.y), p.onCurve, p.last)
		}
	} else if contours < 0 {
		tail, ok := s.tail()
		if !ok {
			return false
		}
		for _, comp := range compositeGlyphs(tail) {
			start, end, ok := g.loca.glyphRange(comp.glyphID)
			if !ok || end > len(g.data) {
				continue
			}
			nb := newGlyfBuilder(combineTransforms(b.transform, comp.transform), b.bbox, b.b)
			if !g.outlineImpl(g.data[start:end], depth+1, nb) {
				return false
			}
			b.bbox = nb.bbox
		}
	}
	return true
}

// outline is glyf::Table::outline: false when the glyph has no outline.
func (g glyfTable) outline(gid uint16, out OutlineBuilder) bool {
	b := newGlyfBuilder(identityGlyphTransform, newRectF(), out)
	data, ok := g.get(gid)
	if !ok {
		return false
	}
	if !g.outlineImpl(data, 0, b) || b.bbox.isDefault() {
		return false
	}
	return b.bbox.toRect()
}

// ---- gvar (tables/gvar.rs) ----

type packedPointsIter struct {
	data       []byte
	offset     uint16
	wordState  uint8 // 0 control, 1 short point, 2 long point
	pointsLeft uint8
}

// newPackedPoints is PackedPointsIter::new: ok false is a parse error, has false is "no points".
func newPackedPoints(s *stream) (it packedPointsIter, has bool, ok bool) {
	b1, ok := s.u8()
	if !ok {
		return it, false, false
	}
	count := uint16(b1)
	if b1&0x80 != 0 {
		b2, ok := s.u8()
		if !ok {
			return it, false, false
		}
		count = uint16(b1&0x7F)<<8 | uint16(b2)
	}
	if count == 0 {
		return it, false, true
	}
	start := s.offset
	tail, ok := s.tail()
	if !ok {
		return it, false, false
	}
	var i uint16
	for i < count {
		control, ok := s.u8()
		if !ok {
			return it, false, false
		}
		run := uint16(control&0x7F) + 1
		size := 1
		if control&0x80 != 0 {
			size = 2
		}
		if s.offset+size*int(run) > len(s.data) {
			return it, false, false
		}
		s.offset += size * int(run)
		i += run
	}
	if i == 0 {
		return it, false, true
	}
	if i > count {
		return it, false, false
	}
	dataLen := s.offset - start
	if dataLen > 0xFFFF {
		return it, false, false
	}
	return packedPointsIter{data: tail[:dataLen]}, true, true
}

func (p *packedPointsIter) next() (uint16, bool) {
	for {
		if int(p.offset) >= len(p.data) {
			return 0, false
		}
		if p.wordState == 0 {
			control := p.data[p.offset]
			p.offset++
			p.pointsLeft = control&0x7F + 1
			if control&0x80 != 0 {
				p.wordState = 2
			} else {
				p.wordState = 1
			}
			continue
		}
		s, ok := newStreamAt(p.data, int(p.offset))
		if !ok {
			return 0, false
		}
		var point uint16
		if p.wordState == 2 {
			p.offset += 2
			v, ok := s.u16()
			if !ok {
				return 0, false
			}
			point = v
		} else {
			p.offset++
			v, ok := s.u8()
			if !ok {
				return 0, false
			}
			point = uint16(v)
		}
		p.pointsLeft--
		if p.pointsLeft == 0 {
			p.wordState = 0
		}
		return point, true
	}
}

func (p packedPointsIter) count() int {
	n := 0
	for {
		if _, ok := p.next(); !ok {
			return n
		}
		n++
	}
}

type setPointsIter struct {
	iter       packedPointsIter
	unrefCount uint16
}

func newSetPoints(iter packedPointsIter) setPointsIter {
	u, ok := iter.next()
	if !ok {
		u = 0
	}
	return setPointsIter{iter: iter, unrefCount: u}
}

func (s setPointsIter) restart() setPointsIter {
	it := s.iter
	it.offset, it.wordState, it.pointsLeft = 0, 0, 0
	return newSetPoints(it)
}

func (s *setPointsIter) next() bool {
	if s.unrefCount != 0 {
		s.unrefCount--
		return false
	}
	if u, ok := s.iter.next(); ok {
		s.unrefCount = u
		if s.unrefCount != 0 {
			s.unrefCount--
		}
	}
	return true
}

type runState struct {
	dataOffset uint16
	state      uint8 // 0 control, 1 zero, 2 short, 3 long
	left       uint8
}

func (r *runState) next(data []byte, scalar float32) (float32, bool) {
	for {
		if r.state == 0 {
			if int(r.dataOffset) == len(data) {
				return 0, false
			}
			if int(r.dataOffset) >= len(data) {
				return 0, false
			}
			control := data[r.dataOffset]
			r.dataOffset++
			r.left = control&0x3F + 1
			switch {
			case control&0x80 != 0:
				r.state = 1
			case control&0x40 != 0:
				r.state = 3
			default:
				r.state = 2
			}
			continue
		}
		s, ok := newStreamAt(data, int(r.dataOffset))
		if !ok {
			return 0, false
		}
		var delta float32
		switch r.state {
		case 3:
			r.dataOffset += 2
			v, ok := s.i16()
			if !ok {
				return 0, false
			}
			delta = float32(float32(v) * scalar)
		case 1:
			delta = 0
		default:
			r.dataOffset++
			v, ok := s.i8()
			if !ok {
				return 0, false
			}
			delta = float32(float32(v) * scalar)
		}
		r.left--
		if r.left == 0 {
			r.state = 0
		}
		return delta, true
	}
}

type packedDeltasIter struct {
	data   []byte
	xRun   runState
	yRun   runState
	total  uint16
	scalar float32
}

func newPackedDeltas(scalar float32, count uint16, data []byte) packedDeltasIter {
	it := packedDeltasIter{data: data, total: count, scalar: scalar}
	for i := uint16(0); i < count; i++ {
		it.yRun.next(data, scalar)
	}
	return it
}

func (p packedDeltasIter) restart() packedDeltasIter {
	return newPackedDeltas(p.scalar, p.total, p.data)
}

func (p *packedDeltasIter) next() (float32, float32, bool) {
	x, ok := p.xRun.next(p.data, p.scalar)
	if !ok {
		return 0, 0, false
	}
	y, ok := p.yRun.next(p.data, p.scalar)
	if !ok {
		return 0, 0, false
	}
	return x, y, true
}

type pointAndDelta struct {
	x, y           int16
	xDelta, yDelta float32
}

type variationTuple struct {
	setPoints    setPointsIter
	hasSetPoints bool
	deltas       packedDeltasIter
	prev         pointAndDelta
	hasPrev      bool
}

type gvarTable struct {
	axisCount    uint16
	sharedTuples []byte // F2DOT14 array
	offsets      []byte
	long         bool
	offsetsCount uint16
	variations   []byte
}

func parseGvar(data []byte) (*gvarTable, bool) {
	s := stream{data: data}
	version, ok := s.u32()
	if !ok || version != 0x00010000 {
		return nil, false
	}
	axisCount, ok1 := s.u16()
	sharedCount, ok2 := s.u16()
	sharedOff, ok3 := s.u32()
	glyphCount, ok4 := s.u16()
	flags, ok5 := s.u16()
	arrayOff, ok6 := s.u32()
	if !ok1 || !ok2 || !ok3 || !ok4 || !ok5 || !ok6 || axisCount == 0 {
		return nil, false
	}
	ss, ok := newStreamAt(data, int(sharedOff))
	if !ok {
		return nil, false
	}
	n := uint32(sharedCount) * uint32(axisCount)
	if n > 0xFFFF {
		return nil, false
	}
	shared, ok := ss.readBytes(int(n) * 2)
	if !ok {
		return nil, false
	}
	if int(arrayOff) > len(data) {
		return nil, false
	}
	variations := data[arrayOff:]
	if glyphCount == 0xFFFF {
		return nil, false
	}
	count := glyphCount + 1
	long := flags&1 == 1
	size := 2
	if long {
		size = 4
	}
	offsets, ok := s.readBytes(int(count) * size)
	if !ok {
		return nil, false
	}
	return &gvarTable{axisCount: axisCount, sharedTuples: shared, offsets: offsets, long: long, offsetsCount: count, variations: variations}, true
}

func (g *gvarTable) offset(i uint16) (int, bool) {
	if i >= g.offsetsCount {
		return 0, false
	}
	if g.long {
		return int(binary.BigEndian.Uint32(g.offsets[int(i)*4:])), true
	}
	return int(binary.BigEndian.Uint16(g.offsets[int(i)*2:])) * 2, true
}

// parseVariationData is Table::parse_variation_data: false is None, an empty slice no variation data.
func (g *gvarTable) parseVariationData(gid uint16, coords []int16, pointsLen uint16) ([]variationTuple, bool) {
	if len(coords) != int(g.axisCount) {
		return nil, false
	}
	if gid == 0xFFFF {
		return nil, false
	}
	start, ok1 := g.offset(gid)
	end, ok2 := g.offset(gid + 1)
	if !ok1 || !ok2 {
		return nil, false
	}
	if start == end {
		return []variationTuple{}, true
	}
	if start > end || end > len(g.variations) {
		return nil, false
	}
	return parseGlyphVariationData(coords, g.sharedTuples, pointsLen, g.variations[start:end])
}

func parseGlyphVariationData(coords []int16, shared []byte, pointsLen uint16, data []byte) ([]variationTuple, bool) {
	main := stream{data: data}
	count, ok1 := main.u16()
	dataOff, ok2 := main.u16()
	if !ok1 || !ok2 {
		return nil, false
	}
	hasShared := count&0x8000 != 0
	count &= 0x0FFF
	if count == 0 {
		return nil, false
	}
	ser, ok := newStreamAt(data, int(dataOff))
	if !ok {
		return nil, false
	}
	var sharedPoints packedPointsIter
	hasSharedPoints := false
	if hasShared {
		it, has, ok := newPackedPoints(&ser)
		if !ok {
			return nil, false
		}
		sharedPoints, hasSharedPoints = it, has
	}
	if pointsLen > 0xFFFF-4 {
		return nil, false
	}
	pointsLen += 4
	tuples := []variationTuple{}
	for i := uint16(0); i < count; i++ {
		scalar, private, serLen, ok := parseTupleHeader(coords, shared, &main)
		if !ok {
			return nil, false
		}
		if !(scalar > 0) {
			ser.offset += int(serLen)
			continue
		}
		serStart := ser.offset
		points, hasPoints := sharedPoints, hasSharedPoints
		if private {
			it, has, ok := newPackedPoints(&ser)
			if !ok {
				return nil, false
			}
			points, hasPoints = it, has
		}
		deltasCount := pointsLen
		if hasPoints {
			n := points.count()
			if n > 0xFFFF {
				return nil, false
			}
			deltasCount = uint16(n)
		}
		used := ser.offset - serStart
		if used > int(serLen) {
			return nil, false
		}
		deltasData, ok := ser.readBytes(int(serLen) - used)
		if !ok {
			return nil, false
		}
		t := variationTuple{deltas: newPackedDeltas(scalar, deltasCount, deltasData)}
		if hasPoints {
			t.setPoints, t.hasSetPoints = newSetPoints(points), true
		}
		tuples = append(tuples, t)
	}
	return tuples, true
}

func f2dot14At(arr []byte, i uint16) (int16, bool) {
	if int(i)*2+2 > len(arr) {
		return 0, false
	}
	return int16(binary.BigEndian.Uint16(arr[int(i)*2:])), true
}

// parseTupleHeader is parse_tuple_variation_header: the scalar, the private-points flag and the
// serialized data length.
func parseTupleHeader(coords []int16, shared []byte, s *stream) (float32, bool, uint16, bool) {
	serLen, ok1 := s.u16()
	index, ok2 := s.u16()
	if !ok1 || !ok2 {
		return 0, false, 0, false
	}
	embedded := index&0x8000 != 0
	intermediate := index&0x4000 != 0
	private := index&0x2000 != 0
	index &= 0x0FFF
	axisCount := uint16(len(coords))
	var peak []byte
	if embedded {
		b, ok := s.readBytes(int(axisCount) * 2)
		if !ok {
			return 0, false, 0, false
		}
		peak = b
	} else {
		start := uint32(index) * uint32(axisCount)
		end := start + uint32(axisCount)
		if start > 0xFFFF || end > 0xFFFF || int(end)*2 > len(shared) {
			return 0, false, 0, false
		}
		peak = shared[start*2 : end*2]
	}
	var startT, endT []byte
	if intermediate {
		b1, ok1 := s.readBytes(int(axisCount) * 2)
		b2, ok2 := s.readBytes(int(axisCount) * 2)
		if !ok1 || !ok2 {
			return 0, false, 0, false
		}
		startT, endT = b1, b2
	}
	scalar := float32(1)
	for i := uint16(0); i < axisCount; i++ {
		v := coords[i]
		pk, ok := f2dot14At(peak, i)
		if !ok {
			return 0, false, 0, false
		}
		if pk == 0 || v == pk {
			continue
		}
		if intermediate {
			st, ok1 := f2dot14At(startT, i)
			en, ok2 := f2dot14At(endT, i)
			if !ok1 || !ok2 {
				return 0, false, 0, false
			}
			if st > pk || pk > en || (st < 0 && en > 0 && pk != 0) {
				continue
			}
			if v < st || v > en {
				return 0, private, serLen, true
			}
			if v < pk {
				if pk != st {
					scalar = float32(scalar * float32(float32(v-st)/float32(pk-st)))
				}
			} else if pk != en {
				scalar = float32(scalar * float32(float32(en-v)/float32(en-pk)))
			}
		} else if v == 0 || v < min(0, pk) || v > max(0, pk) {
			return 0, private, serLen, true
		} else {
			scalar = float32(scalar * float32(float32(v)/float32(pk)))
		}
	}
	return scalar, private, serLen, true
}

// applyTuples is VariationTuples::apply.
func applyTuples(tuples []variationTuple, all, points glyphPointsIter, p glyphPoint) (float32, float32) {
	x, y := float32(p.x), float32(p.y)
	for i := range tuples {
		t := &tuples[i]
		if t.hasSetPoints {
			if t.setPoints.next() {
				if dx, dy, ok := t.deltas.next(); ok {
					t.prev, t.hasPrev = pointAndDelta{p.x, p.y, dx, dy}, true
					x += dx
					y += dy
				} else {
					dx, dy := inferDeltas(t, t.setPoints, points, all, p)
					x += dx
					y += dy
				}
			} else {
				dx, dy := inferDeltas(t, t.setPoints, points, all, p)
				x += dx
				y += dy
			}
			if p.last {
				t.hasPrev = false
			}
		} else if dx, dy, ok := t.deltas.next(); ok {
			x += dx
			y += dy
		}
	}
	return x, y
}

// applyNull is VariationTuples::apply_null.
func applyNull(tuples []variationTuple) (float32, float32) {
	var x, y float32
	for i := range tuples {
		t := &tuples[i]
		if t.hasSetPoints {
			if t.setPoints.next() {
				if dx, dy, ok := t.deltas.next(); ok {
					x += dx
					y += dy
				}
			}
		} else if dx, dy, ok := t.deltas.next(); ok {
			x += dx
			y += dy
		}
	}
	return x, y
}

func inferDeltas(t *variationTuple, set setPointsIter, points, all glyphPointsIter, cur glyphPoint) (float32, float32) {
	contour := points.currentContour()
	if cur.last && contour != 0 {
		contour--
	}
	var prev pointAndDelta
	if t.hasPrev {
		prev = t.prev
	} else {
		found := false
		deltas := t.deltas
		pts, ps := points, set
		for {
			pt, ok := pts.next()
			if !ok {
				break
			}
			isSet := ps.next()
			if isSet {
				if dx, dy, ok := deltas.next(); ok {
					prev, found = pointAndDelta{pt.x, pt.y, dx, dy}, true
				}
			}
			if pt.last {
				break
			}
		}
		if !found {
			return 0, 0
		}
	}
	var next pointAndDelta
	hasNext := false
	if !cur.last {
		deltas := t.deltas
		pts, ps := points, set
		for {
			pt, ok := pts.next()
			if !ok {
				break
			}
			isSet := ps.next()
			if isSet {
				if dx, dy, ok := deltas.next(); ok {
					next, hasNext = pointAndDelta{pt.x, pt.y, dx, dy}, true
				}
				break
			}
			if pt.last {
				break
			}
		}
	}
	if !hasNext {
		allPts := all
		deltas := t.deltas.restart()
		ps := set.restart()
		var c uint16
		for {
			pt, ok := allPts.next()
			isSet := ps.next()
			if !ok {
				break
			}
			if c != contour {
				if isSet {
					deltas.next()
				}
				c = allPts.currentContour()
				continue
			}
			if isSet {
				dx, dy, ok := deltas.next()
				if !ok {
					dx, dy = 0, 0
				}
				next, hasNext = pointAndDelta{pt.x, pt.y, dx, dy}, true
				break
			}
			if pt.last {
				break
			}
		}
	}
	if !hasNext {
		return 0, 0
	}
	return inferDelta(prev.x, cur.x, next.x, prev.xDelta, next.xDelta),
		inferDelta(prev.y, cur.y, next.y, prev.yDelta, next.yDelta)
}

func inferDelta(prev, target, next int16, prevDelta, nextDelta float32) float32 {
	switch {
	case prev == next:
		if prevDelta == nextDelta {
			return prevDelta
		}
		return 0
	case target <= min(prev, next):
		if prev < next {
			return prevDelta
		}
		return nextDelta
	case target >= max(prev, next):
		if prev > next {
			return prevDelta
		}
		return nextDelta
	}
	ts := int32(target) - int32(prev)
	ns := int32(next) - int32(prev)
	if ts < -32768 || ts > 32767 || ns < -32768 || ns > 32767 {
		return 0
	}
	d := float32(float32(ts) / float32(ns))
	return float32(float32((1-d)*prevDelta) + float32(d*nextDelta))
}

// outlineVar is gvar's outline_var_impl.
func (g *gvarTable) outlineVar(glyf glyfTable, gid uint16, data []byte, coords []int16, depth uint8, b *glyfBuilder) bool {
	if depth >= maxComponents {
		return false
	}
	s := stream{data: data}
	contours, ok := s.i16()
	if !ok {
		return false
	}
	s.offset += 8
	switch {
	case contours > 0:
		tail, ok := s.tail()
		if !ok {
			return false
		}
		points, ok := parseSimpleOutline(tail, uint16(contours))
		if !ok {
			return false
		}
		all := points
		tuples, ok := g.parseVariationData(gid, coords, points.pointsLeft)
		if !ok {
			return false
		}
		for {
			p, ok := points.next()
			if !ok {
				break
			}
			x, y := applyTuples(tuples, all, points, p)
			b.pushPoint(x, y, p.onCurve, p.last)
		}
		return true
	case contours < 0:
		tail, ok := s.tail()
		if !ok {
			return false
		}
		comps := compositeGlyphs(tail)
		tuples, ok := g.parseVariationData(gid, coords, uint16(len(comps)))
		if !ok {
			return false
		}
		for _, comp := range comps {
			tx, ty := applyNull(tuples)
			ts := b.transform
			if comp.argsAreXY {
				ts = combineTransforms(ts, glyphTransform{a: 1, d: 1, e: tx, f: ty})
			}
			ts = combineTransforms(ts, comp.transform)
			nb := newGlyfBuilder(ts, b.bbox, b.b)
			if data, ok := glyf.get(comp.glyphID); ok {
				if !g.outlineVar(glyf, comp.glyphID, data, coords, depth+1, nb) {
					return false
				}
				b.bbox = nb.bbox
			}
		}
		return true
	}
	return false
}

func (g *gvarTable) outline(glyf glyfTable, coords []int16, gid uint16, out OutlineBuilder) bool {
	b := newGlyfBuilder(identityGlyphTransform, newRectF(), out)
	data, ok := glyf.get(gid)
	if !ok {
		return false
	}
	g.outlineVar(glyf, gid, data, coords, 0, b)
	return b.bbox.toRect()
}

// ---- per-face outline tables ----

// outlineFace holds what Face::outline_glyph reads, parsed once per face.
type outlineFace struct {
	glyf    glyfTable
	hasGlyf bool
	gvar    *gvarTable
	fvar    []byte // the axis records, 20 bytes each
	avar    []byte
	hasAvar bool
	axes    int // coordinates kept (at most 64)
	cff1    *cffTable
	cff     *font.Face // CFF2, through go-text

	// memo holds each outline drawn so far, by glyph and coordinates, as the builder calls it made: text is
	// converted again on every rebuild (a sliding node's snapshot, a landed font), and a variable font's
	// glyph costs its gvar deltas each time. memoMu also serialises the outlining (cff's coordinates are state).
	memoMu sync.Mutex
	memo   map[outlineKey]*outlineRecord
	// built holds what callers made of an outline (usvg's finished path), by the same key (OutlineMemo).
	built map[outlineKey]any
}

type outlineKey struct {
	gid    uint16
	coords string // the normalised coordinates, two bytes each
	varied bool   // coords is not nil
}

// outlineRecord is one outline: the builder calls in order, and what outlineGlyph returned.
type outlineRecord struct {
	ops []outlineOp
	ok  bool
}

type outlineOp struct {
	kind uint8 // 'M', 'L', 'Q', 'C', 'Z'
	p    [6]float32
}

func (r *outlineRecord) MoveTo(x, y float32) { r.ops = append(r.ops, outlineOp{'M', [6]float32{x, y}}) }
func (r *outlineRecord) LineTo(x, y float32) { r.ops = append(r.ops, outlineOp{'L', [6]float32{x, y}}) }
func (r *outlineRecord) QuadTo(x1, y1, x, y float32) {
	r.ops = append(r.ops, outlineOp{'Q', [6]float32{x1, y1, x, y}})
}
func (r *outlineRecord) CubicTo(x1, y1, x2, y2, x, y float32) {
	r.ops = append(r.ops, outlineOp{'C', [6]float32{x1, y1, x2, y2, x, y}})
}
func (r *outlineRecord) Close() { r.ops = append(r.ops, outlineOp{kind: 'Z'}) }

// replay makes the recorded calls on b.
func (r *outlineRecord) replay(b OutlineBuilder) bool {
	for _, op := range r.ops {
		p := &op.p
		switch op.kind {
		case 'M':
			b.MoveTo(p[0], p[1])
		case 'L':
			b.LineTo(p[0], p[1])
		case 'Q':
			b.QuadTo(p[0], p[1], p[2], p[3])
		case 'C':
			b.CubicTo(p[0], p[1], p[2], p[3], p[4], p[5])
		case 'Z':
			b.Close()
		}
	}
	return r.ok
}

// maxOutlineMemo bounds a face's memo; past it the memo starts over.
const maxOutlineMemo = 1 << 15

func (fi *faceInfo) outlineFace() *outlineFace {
	if fi.outl != nil || fi.noOutl {
		return fi.outl
	}
	fi.noOutl = true
	data, ok := fi.src.bytes()
	if !ok {
		return nil
	}
	r := &byteReaderAt{data}
	raw, ok := parseRawFace(r, int64(len(data)), fi.index)
	if !ok || !raw.faceParses() {
		return nil
	}
	o := &outlineFace{}
	table := func(t string) ([]byte, bool) { return raw.tableSlice(data, tag(t)) }
	head, _ := table("head")
	maxp, _ := table("maxp")
	numGlyphs, _ := u16(maxp, 4)
	locFormat, _ := u16(head, 50)
	if glyf, ok := table("glyf"); ok {
		if locaData, ok := table("loca"); ok {
			if loca, ok := parseLoca(numGlyphs, locFormat == 1, locaData); ok {
				o.glyf, o.hasGlyf = glyfTable{loca: loca, data: glyf}, true
			}
		}
	}
	if isVariable(raw) {
		fvar, _ := table("fvar")
		axesOff, _ := u16(fvar, 4)
		n, _ := u16(fvar, 8)
		o.fvar = fvar[int(axesOff) : int(axesOff)+20*int(n)]
		o.axes = min(int(n), 64)
		o.avar, o.hasAvar = table("avar")
		if gvar, ok := table("gvar"); ok {
			o.gvar, _ = parseGvar(gvar)
		}
	}
	if !o.hasGlyf {
		if cff, ok := table("CFF "); ok {
			o.cff1, _ = parseCFF(cff)
		}
		if _, ok := table("CFF2"); ok && o.cff1 == nil {
			if ft := fi.font(); ft != nil {
				o.cff = font.NewFace(ft)
			}
		}
	}
	fi.outl = o
	return o
}

// byteReaderAt is an io.ReaderAt over loaded font data.
type byteReaderAt struct{ b []byte }

func (r *byteReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off > int64(len(r.b)) {
		return 0, errOutOfRange
	}
	n := copy(p, r.b[off:])
	if n < len(p) {
		return n, errOutOfRange
	}
	return n, nil
}

type rangeError struct{}

func (rangeError) Error() string { return "font data out of range" }

var errOutOfRange error = rangeError{}

// tableSlice is table without copying, for data loaded whole.
func (f *rawFace) tableSlice(data []byte, t uint32) ([]byte, bool) {
	for _, r := range f.records {
		if r.tag == t {
			end := uint64(r.off) + uint64(r.len)
			if end > uint64(len(data)) {
				return nil, false
			}
			return data[r.off:end], true
		}
	}
	return nil, false
}

// coords are the normalized coordinates after Face::set_variation for each variation in turn.
func (o *outlineFace) coords(vars []Variation) []int16 {
	if o.fvar == nil {
		return nil
	}
	coords := make([]int16, o.axes)
	if o.axes >= 64 {
		return coords
	}
	for _, v := range vars {
		setVariation(o.fvar, o.avar, o.hasAvar, coords, binary.BigEndian.Uint32(v.Tag[:]), v.Value)
	}
	return coords
}

// setVariation is ttf_parser::Face::set_variation on coords (every axis with this tag).
func setVariation(fvar, avar []byte, hasAvar bool, coords []int16, t uint32, value float32) {
	fixed := func(b []byte) float32 { return float32(int32(binary.BigEndian.Uint32(b))) / 65536 }
	for i := 0; i < len(coords); i++ {
		a := fvar[20*i:]
		if binary.BigEndian.Uint32(a) != t {
			continue
		}
		lo, def, hi := fixed(a[4:]), fixed(a[8:]), fixed(a[12:])
		lo, hi = f32Min(def, lo), f32Max2(def, hi) // VariationAxis::parse
		v := value
		if v > hi {
			v = hi
		} else if v < lo {
			v = lo
		}
		switch {
		case v == def:
			v = 0
		case v < def:
			v = (v - def) / (def - lo)
		default:
			v = (v - def) / (hi - def)
		}
		if v > 1 {
			v = 1
		} else if v < -1 {
			v = -1
		}
		coords[i] = int16(float32(v * 16384))
		if hasAvar {
			avarMap(avar, coords, i)
		}
	}
}

// outlineGlyph is ttf_parser::Face::outline_glyph at coords.
func memoKey(gid uint16, coords []int16) outlineKey {
	k := outlineKey{gid: gid, varied: coords != nil}
	if len(coords) > 0 {
		c := make([]byte, 2*len(coords))
		for i, v := range coords {
			binary.BigEndian.PutUint16(c[2*i:], uint16(v))
		}
		k.coords = string(c)
	}
	return k
}

func (o *outlineFace) outlineGlyph(gid uint16, coords []int16, b OutlineBuilder) bool {
	k := memoKey(gid, coords)
	o.memoMu.Lock()
	r, ok := o.memo[k]
	if !ok {
		r = &outlineRecord{}
		r.ok = o.drawGlyph(gid, coords, r)
		if o.memo == nil || len(o.memo) >= maxOutlineMemo {
			o.memo = map[outlineKey]*outlineRecord{}
		}
		o.memo[k] = r
	}
	o.memoMu.Unlock()
	return r.replay(b)
}

// drawGlyph outlines a glyph at coords (ttf_parser::Face::outline_glyph).
func (o *outlineFace) drawGlyph(gid uint16, coords []int16, b OutlineBuilder) bool {
	if o.gvar != nil {
		if !o.hasGlyf {
			return false
		}
		return o.gvar.outline(o.glyf, coords, gid, b)
	}
	if o.hasGlyf {
		return o.glyf.outline(gid, b)
	}
	if o.cff1 != nil {
		return o.cff1.outline(gid, b)
	}
	if o.cff != nil {
		if coords != nil {
			c := make([]tables.Coord, len(coords))
			for i, v := range coords {
				c[i] = tables.Coord(v)
			}
			o.cff.SetCoords(c)
		} else {
			o.cff.SetCoords(nil)
		}
		out, ok := o.cff.GlyphDataOutline(font.GID(gid))
		if !ok || len(out.Segments) == 0 {
			return false
		}
		emitSegments(out.Segments, b)
		return true
	}
	return false
}

// emitSegments replays go-text segments, closing each contour as ttf-parser's CFF builder does. go-text
// ends a contour that does not return to its start with a line back to it, where ttf-parser only closes:
// such a final line is dropped. A charstring that draws that line itself looks the same, so there the
// port leaves out a line that ttf-parser keeps; font tools rarely write it, since closing draws it.
func emitSegments(segs []font.Segment, b OutlineBuilder) {
	open := false
	var first font.SegmentPoint
	for i, s := range segs {
		switch s.Op {
		case ot.SegmentOpMoveTo:
			first = s.Args[0]
		case ot.SegmentOpLineTo:
			last := i+1 == len(segs) || segs[i+1].Op == ot.SegmentOpMoveTo
			if last && i > 0 && s.Args[0] == first && segEnd(segs[i-1]) != first && segs[i-1].Op != ot.SegmentOpMoveTo {
				continue
			}
		}
		switch s.Op {
		case ot.SegmentOpMoveTo:
			if open {
				b.Close()
			}
			open = true
			b.MoveTo(s.Args[0].X, s.Args[0].Y)
		case ot.SegmentOpLineTo:
			b.LineTo(s.Args[0].X, s.Args[0].Y)
		case ot.SegmentOpQuadTo:
			b.QuadTo(s.Args[0].X, s.Args[0].Y, s.Args[1].X, s.Args[1].Y)
		case ot.SegmentOpCubeTo:
			b.CubicTo(s.Args[0].X, s.Args[0].Y, s.Args[1].X, s.Args[1].Y, s.Args[2].X, s.Args[2].Y)
		}
	}
	if open {
		b.Close()
	}
}

func segEnd(s font.Segment) font.SegmentPoint {
	switch s.Op {
	case ot.SegmentOpQuadTo:
		return s.Args[1]
	case ot.SegmentOpCubeTo:
		return s.Args[2]
	}
	return s.Args[0]
}
