package pdf

// Font subsets for embedding (what krilla gets from the subsetter crate): glyph ids are renumbered to
// the CID order, so CID = glyph id in the subset.
//   - TrueType at its default variation: the glyf entries are copied as they are (with their hinting
//     instructions, cvt, fpgm and prep), composite glyphs with their components.
//   - TrueType at other coordinates: each glyph is the varied outline as a simple glyph, rounded to
//     integers, with its varied advance (the subsetter instances through skrifa the same way).
//   - CFF and CFF2: a CID-keyed CFF whose charstrings draw the (varied) outlines, unhinted.

import (
	"encoding/binary"
	"errors"
	"math"
	"sort"
	"unicode/utf16"

	"github.com/naddrzz/nalarupa/core-go/text"
)

// sfnt is an OpenType font's table directory.
type sfnt struct {
	tables map[string][]byte
}

func parseSfnt(data []byte, index uint32) (*sfnt, bool) {
	u32 := func(at int) (uint32, bool) {
		if at < 0 || at+4 > len(data) {
			return 0, false
		}
		return binary.BigEndian.Uint32(data[at:]), true
	}
	off := 0
	magic, ok := u32(0)
	if !ok {
		return nil, false
	}
	if magic == 0x74746366 { // ttcf
		o, ok := u32(12 + 4*int(index))
		if !ok {
			return nil, false
		}
		off = int(o)
	}
	if off+12 > len(data) {
		return nil, false
	}
	n := int(binary.BigEndian.Uint16(data[off+4:]))
	s := &sfnt{tables: map[string][]byte{}}
	for i := 0; i < n; i++ {
		rec := off + 12 + 16*i
		if rec+16 > len(data) {
			return nil, false
		}
		tag := string(data[rec : rec+4])
		start, length := binary.BigEndian.Uint32(data[rec+8:]), binary.BigEndian.Uint32(data[rec+12:])
		end := uint64(start) + uint64(length)
		if end > uint64(len(data)) {
			continue
		}
		s.tables[tag] = data[start:end]
	}
	return s, true
}

func (s *sfnt) table(tag string) ([]byte, bool) {
	t, ok := s.tables[tag]
	return t, ok
}

// subsetResult is the embedded program and what the font descriptor says about the face.
type subsetResult struct {
	data           []byte // nil when no program could be built
	cff            bool   // data is a bare CFF (FontFile3), else TrueType (FontFile2)
	postscriptName string
	fixedPitch     bool
	italicAngle    float32
	ascent         float32
	descent        float32
	capHeight      float32
	hasCapHeight   bool
	weight         float32
	bbox           [4]float32 // font units
}

func subsetFont(f *pdfFont) *subsetResult {
	res := &subsetResult{weight: 400}
	data, index, ok := f.db.FaceData(f.face)
	if !ok {
		return res
	}
	sf, ok := parseSfnt(data, index)
	if !ok {
		return res
	}
	describe(sf, res)
	coords := f.db.Coords(f.face, f.vars)
	varied := false
	for _, c := range coords {
		varied = varied || c != 0
	}
	var err error
	if _, ok := sf.table("glyf"); ok {
		if varied {
			res.data, err = instancedGlyf(sf, f)
		} else {
			res.data, err = copiedGlyf(sf, f)
		}
	} else {
		res.cff = true
		res.data, err = outlineCFF(f)
	}
	if err != nil {
		res.data = nil
	}
	return res
}

// describe reads the descriptor values krilla's FontInfo reads.
func describe(sf *sfnt, res *subsetResult) {
	i16 := func(b []byte, at int) (float32, bool) {
		if at+2 > len(b) {
			return 0, false
		}
		return float32(int16(binary.BigEndian.Uint16(b[at:]))), true
	}
	if head, ok := sf.table("head"); ok && len(head) >= 54 {
		for i := range res.bbox {
			res.bbox[i], _ = i16(head, 36+2*i)
		}
	}
	hhea, _ := sf.table("hhea")
	res.ascent, _ = i16(hhea, 4)
	res.descent, _ = i16(hhea, 6)
	if os2, ok := sf.table("OS/2"); ok && len(os2) >= 78 {
		res.ascent, _ = i16(os2, 68)
		res.descent, _ = i16(os2, 70)
		res.weight = float32(binary.BigEndian.Uint16(os2[4:]))
		if binary.BigEndian.Uint16(os2) >= 2 {
			res.capHeight, res.hasCapHeight = i16(os2, 88)
		}
	}
	if post, ok := sf.table("post"); ok && len(post) >= 16 {
		res.italicAngle = float32(int32(binary.BigEndian.Uint32(post[4:]))) / 65536
		res.fixedPitch = binary.BigEndian.Uint32(post[12:]) != 0
	}
	res.postscriptName = postscriptName(sf)
}

// postscriptName is the first name record with id 6 that decodes.
func postscriptName(sf *sfnt) string {
	name, ok := sf.table("name")
	if !ok || len(name) < 6 {
		return ""
	}
	count := int(binary.BigEndian.Uint16(name[2:]))
	strings := int(binary.BigEndian.Uint16(name[4:]))
	for i := 0; i < count; i++ {
		rec := 6 + 12*i
		if rec+12 > len(name) {
			break
		}
		platform, encoding := binary.BigEndian.Uint16(name[rec:]), binary.BigEndian.Uint16(name[rec+2:])
		id := binary.BigEndian.Uint16(name[rec+6:])
		length, off := int(binary.BigEndian.Uint16(name[rec+8:])), int(binary.BigEndian.Uint16(name[rec+10:]))
		if id != 6 || strings+off+length > len(name) {
			continue
		}
		raw := name[strings+off : strings+off+length]
		switch {
		case platform == 0 || (platform == 3 && (encoding == 1 || encoding == 10)):
			units := make([]uint16, len(raw)/2)
			for j := range units {
				units[j] = binary.BigEndian.Uint16(raw[2*j:])
			}
			return string(utf16.Decode(units))
		case platform == 1 && encoding == 0:
			ascii := true
			for _, c := range raw {
				ascii = ascii && c < 0x80
			}
			if ascii {
				return string(raw)
			}
		}
	}
	return ""
}

// ---- TrueType ----

type locaTable struct {
	offsets []uint32
}

func readLoca(sf *sfnt) (locaTable, []byte, uint16, error) {
	head, _ := sf.table("head")
	maxp, _ := sf.table("maxp")
	loca, _ := sf.table("loca")
	glyf, _ := sf.table("glyf")
	if len(head) < 54 || len(maxp) < 6 {
		return locaTable{}, nil, 0, errors.New("malformed font")
	}
	n := binary.BigEndian.Uint16(maxp[4:])
	long := binary.BigEndian.Uint16(head[50:]) == 1
	t := locaTable{offsets: make([]uint32, int(n)+1)}
	for i := range t.offsets {
		if long {
			if 4*i+4 > len(loca) {
				return locaTable{}, nil, 0, errors.New("malformed loca")
			}
			t.offsets[i] = binary.BigEndian.Uint32(loca[4*i:])
		} else {
			if 2*i+2 > len(loca) {
				return locaTable{}, nil, 0, errors.New("malformed loca")
			}
			t.offsets[i] = 2 * uint32(binary.BigEndian.Uint16(loca[2*i:]))
		}
	}
	return t, glyf, n, nil
}

func (l locaTable) glyph(glyf []byte, gid uint16) []byte {
	if int(gid)+1 >= len(l.offsets) {
		return nil
	}
	a, b := l.offsets[gid], l.offsets[gid+1]
	if a >= b || uint64(b) > uint64(len(glyf)) {
		return nil
	}
	return glyf[a:b]
}

// components calls fn with the byte offset of each component glyph id of a composite glyph.
func components(g []byte, fn func(at int)) {
	if len(g) < 10 || int16(binary.BigEndian.Uint16(g)) >= 0 {
		return
	}
	at := 10
	for at+4 <= len(g) {
		flags := binary.BigEndian.Uint16(g[at:])
		fn(at + 2)
		at += 4
		if flags&0x0001 != 0 { // ARG_1_AND_2_ARE_WORDS
			at += 4
		} else {
			at += 2
		}
		switch {
		case flags&0x0008 != 0: // WE_HAVE_A_SCALE
			at += 2
		case flags&0x0040 != 0: // WE_HAVE_AN_X_AND_Y_SCALE
			at += 4
		case flags&0x0080 != 0: // WE_HAVE_A_TWO_BY_TWO
			at += 8
		}
		if flags&0x0020 == 0 { // MORE_COMPONENTS
			break
		}
	}
}

// hMetric is a glyph's advance and left side bearing from hmtx.
func hMetric(sf *sfnt, gid uint16) (uint16, int16) {
	hhea, _ := sf.table("hhea")
	hmtx, _ := sf.table("hmtx")
	if len(hhea) < 36 {
		return 0, 0
	}
	n := int(binary.BigEndian.Uint16(hhea[34:]))
	if n == 0 {
		return 0, 0
	}
	g := int(gid)
	if g < n {
		if 4*g+4 > len(hmtx) {
			return 0, 0
		}
		return binary.BigEndian.Uint16(hmtx[4*g:]), int16(binary.BigEndian.Uint16(hmtx[4*g+2:]))
	}
	var adv uint16
	if 4*n <= len(hmtx) {
		adv = binary.BigEndian.Uint16(hmtx[4*n-4:])
	}
	at := 4*n + 2*(g-n)
	if at+2 > len(hmtx) {
		return adv, 0
	}
	return adv, int16(binary.BigEndian.Uint16(hmtx[at:]))
}

// copiedGlyf subsets a TrueType font at its default variation: the glyphs (and their components) copied.
func copiedGlyf(sf *sfnt, f *pdfFont) ([]byte, error) {
	loca, glyf, numGlyphs, err := readLoca(sf)
	if err != nil {
		return nil, err
	}
	// The glyph closure: components join after the CIDs, in the order first met.
	gids := append([]uint16(nil), f.gids...)
	newID := map[uint16]uint16{}
	for i, g := range gids {
		newID[g] = uint16(i)
	}
	for i := 0; i < len(gids); i++ {
		components(loca.glyph(glyf, gids[i]), func(at int) {
			c := binary.BigEndian.Uint16(loca.glyph(glyf, gids[i])[at:])
			if _, ok := newID[c]; !ok && c < numGlyphs && len(gids) < 0xffff {
				newID[c] = uint16(len(gids))
				gids = append(gids, c)
			}
		})
	}
	var newGlyf []byte
	offsets := make([]uint32, 0, len(gids)+1)
	hmtx := make([]byte, 0, 4*len(gids))
	for _, g := range gids {
		offsets = append(offsets, uint32(len(newGlyf)))
		data := append([]byte(nil), loca.glyph(glyf, g)...)
		components(data, func(at int) {
			c := binary.BigEndian.Uint16(data[at:])
			binary.BigEndian.PutUint16(data[at:], newID[c])
		})
		newGlyf = append(newGlyf, data...)
		for len(newGlyf)%4 != 0 {
			newGlyf = append(newGlyf, 0)
		}
		adv, lsb := hMetric(sf, g)
		hmtx = binary.BigEndian.AppendUint16(hmtx, adv)
		hmtx = binary.BigEndian.AppendUint16(hmtx, uint16(lsb))
	}
	offsets = append(offsets, uint32(len(newGlyf)))
	tables := map[string][]byte{"glyf": newGlyf, "hmtx": hmtx}
	for _, t := range []string{"cvt ", "fpgm", "prep"} {
		if d, ok := sf.table(t); ok {
			tables[t] = d
		}
	}
	return buildTrueType(sf, tables, offsets, len(gids), nil)
}

// instancedGlyf subsets a TrueType font at non-default coordinates: varied outlines as simple glyphs.
func instancedGlyf(sf *sfnt, f *pdfFont) ([]byte, error) {
	var newGlyf, hmtx []byte
	offsets := make([]uint32, 0, len(f.gids)+1)
	maxPoints, maxContours := 0, 0
	for _, g := range f.gids {
		offsets = append(offsets, uint32(len(newGlyf)))
		contours := glyphContours(f, g)
		data, xMin, points := simpleGlyph(contours)
		maxPoints, maxContours = max(maxPoints, points), max(maxContours, len(contours))
		newGlyf = append(newGlyf, data...)
		for len(newGlyf)%4 != 0 {
			newGlyf = append(newGlyf, 0)
		}
		adv, _ := f.db.Advance(f.face, g, f.vars)
		hmtx = binary.BigEndian.AppendUint16(hmtx, uint16(otRound(adv)))
		hmtx = binary.BigEndian.AppendUint16(hmtx, uint16(xMin))
	}
	offsets = append(offsets, uint32(len(newGlyf)))
	return buildTrueType(sf, map[string][]byte{"glyf": newGlyf, "hmtx": hmtx}, offsets, len(f.gids), &[2]int{maxPoints, maxContours})
}

// otRound is OpenType's rounding: floor(x + 0.5).
func otRound(f float32) int32 { return int32(math.Floor(float64(f) + 0.5)) }

type contourPoint struct {
	x, y float32
	on   bool
}

// contourPen collects outline segments as TrueType contours (quads only; cubics are not drawn by glyf
// faces).
type contourPen struct {
	contours [][]contourPoint
	cur      []contourPoint
}

func (p *contourPen) MoveTo(x, y float32) {
	p.end()
	p.cur = []contourPoint{{x, y, true}}
}
func (p *contourPen) LineTo(x, y float32) { p.cur = append(p.cur, contourPoint{x, y, true}) }
func (p *contourPen) QuadTo(x1, y1, x, y float32) {
	p.cur = append(p.cur, contourPoint{x1, y1, false}, contourPoint{x, y, true})
}
func (p *contourPen) CubicTo(x1, y1, x2, y2, x, y float32) {
	// Not reached for glyf outlines; a cubic's midpoint quad keeps the contour closed.
	last := p.cur[len(p.cur)-1]
	cx := (float32(3*(x1+x2)) - last.x - x) / 4
	cy := (float32(3*(y1+y2)) - last.y - y) / 4
	p.QuadTo(cx, cy, x, y)
}
func (p *contourPen) Close() { p.end() }

func (p *contourPen) end() {
	if len(p.cur) == 0 {
		return
	}
	c := p.cur
	p.cur = nil
	// The closing segment returns to the start, which the contour implies.
	if len(c) > 1 && c[len(c)-1].on && c[len(c)-1].x == c[0].x && c[len(c)-1].y == c[0].y {
		c = c[:len(c)-1]
	}
	// On-curve points midway between two off-curve points are implied, as glyf stores them.
	n := len(c)
	if n > 2 {
		keep := make([]contourPoint, 0, n)
		for i, pt := range c {
			prev, next := c[(i+n-1)%n], c[(i+1)%n]
			if pt.on && !prev.on && !next.on &&
				pt.x == prev.x+float32(0.5*(next.x-prev.x)) && pt.y == prev.y+float32(0.5*(next.y-prev.y)) {
				continue
			}
			keep = append(keep, pt)
		}
		c = keep
	}
	if len(c) > 0 {
		p.contours = append(p.contours, c)
	}
}

func glyphContours(f *pdfFont, gid uint16) [][]contourPoint {
	var pen contourPen
	f.db.OutlineVaried(f.face, gid, f.vars, &pen)
	pen.end()
	return pen.contours
}

// simpleGlyph encodes contours as a glyf simple glyph with no instructions; it returns the data, xMin
// and the point count. No contours is an empty glyph.
func simpleGlyph(contours [][]contourPoint) ([]byte, int16, int) {
	if len(contours) == 0 {
		return nil, 0, 0
	}
	type ipt struct {
		x, y int32
		on   bool
	}
	var pts []ipt
	var ends []uint16
	xMin, yMin, xMax, yMax := int32(math.MaxInt32), int32(math.MaxInt32), int32(math.MinInt32), int32(math.MinInt32)
	for _, c := range contours {
		for _, p := range c {
			q := ipt{otRound(p.x), otRound(p.y), p.on}
			pts = append(pts, q)
			xMin, yMin, xMax, yMax = min(xMin, q.x), min(yMin, q.y), max(xMax, q.x), max(yMax, q.y)
		}
		ends = append(ends, uint16(len(pts)-1))
	}
	clamp := func(v int32) int16 { return int16(min(max(v, math.MinInt16), math.MaxInt16)) }
	var b []byte
	b = binary.BigEndian.AppendUint16(b, uint16(len(contours)))
	for _, v := range []int32{xMin, yMin, xMax, yMax} {
		b = binary.BigEndian.AppendUint16(b, uint16(clamp(v)))
	}
	for _, e := range ends {
		b = binary.BigEndian.AppendUint16(b, e)
	}
	b = binary.BigEndian.AppendUint16(b, 0) // instructionLength
	var flags, xs, ys []byte
	var px, py int32
	for _, p := range pts {
		var fl byte
		if p.on {
			fl |= 0x01
		}
		dx, dy := p.x-px, p.y-py
		px, py = p.x, p.y
		switch {
		case dx == 0:
			fl |= 0x10
		case dx > -256 && dx < 256:
			fl |= 0x02
			if dx > 0 {
				fl |= 0x10
			} else {
				dx = -dx
			}
			xs = append(xs, byte(dx))
		default:
			xs = binary.BigEndian.AppendUint16(xs, uint16(clamp(dx)))
		}
		switch {
		case dy == 0:
			fl |= 0x20
		case dy > -256 && dy < 256:
			fl |= 0x04
			if dy > 0 {
				fl |= 0x20
			} else {
				dy = -dy
			}
			ys = append(ys, byte(dy))
		default:
			ys = binary.BigEndian.AppendUint16(ys, uint16(clamp(dy)))
		}
		flags = append(flags, fl)
	}
	b = append(b, flags...)
	b = append(b, xs...)
	b = append(b, ys...)
	return b, clamp(xMin), len(pts)
}

// buildTrueType assembles the subset font: the given tables plus loca (long offsets), head, hhea, maxp,
// name and post (version 3, no glyph names) derived from the original. maxPC overrides maxp's point
// and contour maxima for instanced glyphs.
func buildTrueType(sf *sfnt, tables map[string][]byte, offsets []uint32, numGlyphs int, maxPC *[2]int) ([]byte, error) {
	head, _ := sf.table("head")
	hhea, _ := sf.table("hhea")
	maxp, _ := sf.table("maxp")
	if len(head) < 54 || len(hhea) < 36 || len(maxp) < 6 {
		return nil, errors.New("malformed font")
	}
	loca := make([]byte, 0, 4*len(offsets))
	for _, o := range offsets {
		loca = binary.BigEndian.AppendUint32(loca, o)
	}
	tables["loca"] = loca
	h := append([]byte(nil), head...)
	binary.BigEndian.PutUint16(h[50:], 1) // long loca
	tables["head"] = h
	hh := append([]byte(nil), hhea...)
	binary.BigEndian.PutUint16(hh[34:], uint16(numGlyphs))
	tables["hhea"] = hh
	m := append([]byte(nil), maxp...)
	binary.BigEndian.PutUint16(m[4:], uint16(numGlyphs))
	if maxPC != nil && len(m) >= 32 {
		binary.BigEndian.PutUint16(m[6:], uint16(min(maxPC[0], 0xffff)))
		binary.BigEndian.PutUint16(m[8:], uint16(min(maxPC[1], 0xffff)))
		binary.BigEndian.PutUint16(m[10:], 0) // maxCompositePoints
		binary.BigEndian.PutUint16(m[12:], 0) // maxCompositeContours
		binary.BigEndian.PutUint16(m[28:], 0) // maxComponentElements
		binary.BigEndian.PutUint16(m[30:], 0) // maxComponentDepth
	}
	tables["maxp"] = m
	if name, ok := sf.table("name"); ok {
		tables["name"] = name
	}
	if post, ok := sf.table("post"); ok && len(post) >= 32 {
		p := append([]byte(nil), post[:32]...)
		binary.BigEndian.PutUint32(p, 0x00030000)
		tables["post"] = p
	}
	return assembleSfnt(0x00010000, tables), nil
}

// assembleSfnt writes an OpenType file: the table directory sorted by tag, 4-byte aligned tables and
// head's checksum adjustment.
func assembleSfnt(flavor uint32, tables map[string][]byte) []byte {
	tags := make([]string, 0, len(tables))
	for t := range tables {
		tags = append(tags, t)
	}
	sort.Strings(tags)
	n := len(tags)
	sel := 0
	for 1<<(sel+1) <= n {
		sel++
	}
	var out []byte
	out = binary.BigEndian.AppendUint32(out, flavor)
	out = binary.BigEndian.AppendUint16(out, uint16(n))
	out = binary.BigEndian.AppendUint16(out, uint16(16<<sel))
	out = binary.BigEndian.AppendUint16(out, uint16(sel))
	out = binary.BigEndian.AppendUint16(out, uint16(n*16-16<<sel))
	offset := 12 + 16*n
	headAt := -1
	for _, t := range tags {
		d := tables[t]
		if t == "head" {
			d[8], d[9], d[10], d[11] = 0, 0, 0, 0
			headAt = offset
		}
		out = append(out, t...)
		out = binary.BigEndian.AppendUint32(out, checksum(d))
		out = binary.BigEndian.AppendUint32(out, uint32(offset))
		out = binary.BigEndian.AppendUint32(out, uint32(len(d)))
		offset += (len(d) + 3) &^ 3
	}
	for _, t := range tags {
		out = append(out, tables[t]...)
		for len(out)%4 != 0 {
			out = append(out, 0)
		}
	}
	if headAt >= 0 {
		binary.BigEndian.PutUint32(out[headAt+8:], 0xB1B0AFBA-checksum(out))
	}
	return out
}

func checksum(d []byte) uint32 {
	var sum uint32
	for i := 0; i < len(d); i += 4 {
		var w [4]byte
		copy(w[:], d[i:])
		sum += binary.BigEndian.Uint32(w[:])
	}
	return sum
}

// ---- CFF ----

// outlineCFF writes a CID-keyed CFF (one font dict, identity charset) whose charstrings draw each glyph's
// outline at the font's variations, scaled to a 1000-unit em (the default font matrix).
func outlineCFF(f *pdfFont) ([]byte, error) {
	return buildCFF(f.upem, len(f.gids), func(i int, pen text.OutlineBuilder) {
		f.db.OutlineVaried(f.face, f.gids[i], f.vars, pen)
	})
}

// buildCFF writes the CFF of n glyphs drawn in font units of an upem-unit em.
func buildCFF(upem float32, n int, draw func(i int, pen text.OutlineBuilder)) ([]byte, error) {
	scale := float32(1000) / upem
	charstrings := make([][]byte, n)
	for i := range charstrings {
		pen := &charstringPen{scale: scale}
		draw(i, pen)
		cs, ok := pen.finish()
		if !ok {
			return nil, errors.New("glyph out of CFF range")
		}
		charstrings[i] = cs
	}
	// Top DICT with fixed-size offsets, so its length is known before the layout.
	topDict := func(charset, charStrings, fdArray, fdSelect int) []byte {
		var d []byte
		d = append(d, dictInt(391)...)
		d = append(d, dictInt(392)...)
		d = append(d, dictInt(0)...)
		d = append(d, 12, 30) // ROS
		d = append(d, dictInt(n)...)
		d = append(d, 12, 34) // CIDCount
		d = append(d, dictOffset(charset)...)
		d = append(d, 15)
		d = append(d, dictOffset(charStrings)...)
		d = append(d, 17)
		d = append(d, dictOffset(fdArray)...)
		d = append(d, 12, 36)
		d = append(d, dictOffset(fdSelect)...)
		d = append(d, 12, 37)
		return d
	}
	header := []byte{1, 0, 4, 4}
	nameIndex := cffIndex([][]byte{[]byte("Subset")})
	stringIndex := cffIndex([][]byte{[]byte("Adobe"), []byte("Identity")})
	gsubrs := cffIndex(nil)
	var charset []byte
	if n > 1 {
		charset = []byte{2, 0, 1}
		charset = binary.BigEndian.AppendUint16(charset, uint16(n-2))
	} else {
		charset = []byte{0}
	}
	fdSelect := []byte{3, 0, 1, 0, 0, 0}
	fdSelect = binary.BigEndian.AppendUint16(fdSelect, uint16(n))
	csIndex := cffIndex(charstrings)
	private := []byte{139, 21} // nominalWidthX 0

	topLen := len(cffIndex([][]byte{topDict(0, 0, 0, 0)}))
	at := len(header) + len(nameIndex) + topLen + len(stringIndex) + len(gsubrs)
	charsetAt := at
	at += len(charset)
	fdSelectAt := at
	at += len(fdSelect)
	csAt := at
	at += len(csIndex)
	fdArrayAt := at
	fontDict := func(privAt int) []byte {
		d := append(dictInt(len(private)), dictOffset(privAt)...)
		return append(d, 18)
	}
	fdArray := cffIndex([][]byte{fontDict(0)})
	privateAt := fdArrayAt + len(fdArray)
	fdArray = cffIndex([][]byte{fontDict(privateAt)})

	var out []byte
	out = append(out, header...)
	out = append(out, nameIndex...)
	out = append(out, cffIndex([][]byte{topDict(charsetAt, csAt, fdArrayAt, fdSelectAt)})...)
	out = append(out, stringIndex...)
	out = append(out, gsubrs...)
	out = append(out, charset...)
	out = append(out, fdSelect...)
	out = append(out, csIndex...)
	out = append(out, fdArray...)
	out = append(out, private...)
	return out, nil
}

// cffIndex writes a CFF INDEX with 4-byte offsets (an empty one is just its count).
func cffIndex(items [][]byte) []byte {
	out := binary.BigEndian.AppendUint16(nil, uint16(len(items)))
	if len(items) == 0 {
		return out
	}
	out = append(out, 4)
	off := uint32(1)
	out = binary.BigEndian.AppendUint32(out, off)
	for _, it := range items {
		off += uint32(len(it))
		out = binary.BigEndian.AppendUint32(out, off)
	}
	for _, it := range items {
		out = append(out, it...)
	}
	return out
}

// dictInt encodes a DICT integer operand.
func dictInt(v int) []byte {
	switch {
	case v >= -107 && v <= 107:
		return []byte{byte(v + 139)}
	case v >= 108 && v <= 1131:
		v -= 108
		return []byte{byte(v>>8) + 247, byte(v)}
	case v >= -1131 && v <= -108:
		v = -v - 108
		return []byte{byte(v>>8) + 251, byte(v)}
	case v >= -32768 && v <= 32767:
		return []byte{28, byte(v >> 8), byte(v)}
	}
	return dictOffset(v)
}

// dictOffset encodes an integer operand in the fixed 5-byte form.
func dictOffset(v int) []byte {
	return []byte{29, byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
}

// charstringPen writes a Type 2 charstring: rmoveto, rlineto and rrcurveto on coordinates quantised to
// 1/65536 (so relative moves add up exactly), then endchar.
type charstringPen struct {
	scale float32
	out   []byte
	x, y  int64 // current point in 16.16
	bad   bool
}

func (p *charstringPen) fixed(v float32) int64 {
	return int64(math.Round(float64(v) * float64(p.scale) * 65536))
}

func (p *charstringPen) arg(d int64) {
	if d%65536 == 0 && d/65536 >= -32768 && d/65536 <= 32767 {
		v := int(d / 65536)
		switch {
		case v >= -107 && v <= 107:
			p.out = append(p.out, byte(v+139))
		case v >= 108 && v <= 1131:
			v -= 108
			p.out = append(p.out, byte(v>>8)+247, byte(v))
		case v >= -1131 && v <= -108:
			v = -v - 108
			p.out = append(p.out, byte(v>>8)+251, byte(v))
		default:
			p.out = append(p.out, 28, byte(v>>8), byte(v))
		}
		return
	}
	if d < math.MinInt32 || d > math.MaxInt32 {
		p.bad = true
		return
	}
	p.out = append(p.out, 255, byte(d>>24), byte(d>>16), byte(d>>8), byte(d))
}

func (p *charstringPen) to(x, y float32) {
	fx, fy := p.fixed(x), p.fixed(y)
	p.arg(fx - p.x)
	p.arg(fy - p.y)
	p.x, p.y = fx, fy
}

func (p *charstringPen) MoveTo(x, y float32) {
	p.to(x, y)
	p.out = append(p.out, 21) // rmoveto
}

func (p *charstringPen) LineTo(x, y float32) {
	p.to(x, y)
	p.out = append(p.out, 5) // rlineto
}

func (p *charstringPen) QuadTo(x1, y1, x, y float32) {
	// The exact cubic of the quad.
	x0, y0 := float32(float64(p.x)/65536)/p.scale, float32(float64(p.y)/65536)/p.scale
	c1x, c1y := x0+float32(2*(x1-x0))/3, y0+float32(2*(y1-y0))/3
	c2x, c2y := x+float32(2*(x1-x))/3, y+float32(2*(y1-y))/3
	p.CubicTo(c1x, c1y, c2x, c2y, x, y)
}

func (p *charstringPen) CubicTo(x1, y1, x2, y2, x, y float32) {
	p.to(x1, y1)
	p.to(x2, y2)
	p.to(x, y)
	p.out = append(p.out, 8) // rrcurveto
}

func (p *charstringPen) Close() {}

func (p *charstringPen) finish() ([]byte, bool) {
	p.out = append(p.out, 14) // endchar
	return p.out, !p.bad
}
