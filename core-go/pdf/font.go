package pdf

// Text as krilla writes it: each font instance (a face at a set of variation coordinates) is a Type 0
// font with Identity-H encoding over a CID font whose CIDs are the glyph ids of an embedded subset
// (subset.go), a /W array of advances and a ToUnicode CMap. A glyph whose CID already maps to other
// text is wrapped in an /ActualText span, as krilla's text spanner does. A face without outlines (a
// bitmap-only font) is a Type 3 font instead, as in krilla, whose glyphs draw nothing (krilla draws the
// bitmaps) but keep their text.

import (
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"math"
	"strings"

	"github.com/naddrzz/nalarupa/core-go/skia"
	"github.com/naddrzz/nalarupa/core-go/text"
)

type pdfFont struct {
	ref    ref
	db     *text.Database
	face   text.FaceID
	vars   []text.Variation
	upem   float32
	bbox   [4]float32 // head's box in font units (krilla's font.bbox())
	type3  bool       // no glyf, CFF or CFF2: a Type 3 font of at most 256 glyphs
	gids   []uint16   // by CID (the character code of a Type 3 font); CID 0 is .notdef
	cids   map[uint16]uint16
	texts  []string // by CID
	hasTxt []bool
	widths []float32 // by CID, in font units
}

// font is krilla-svg's Fonts::retrieve: the PDF font of a face at the span's variations, or false when
// the face cannot be embedded.
func (d *document) font(db *text.Database, face text.FaceID, vars []text.Variation) (*pdfFont, bool) {
	var key strings.Builder
	fmt.Fprintf(&key, "%p/%d", db, face)
	for _, v := range vars {
		fmt.Fprintf(&key, "/%s=%08x", v.Tag[:], math.Float32bits(v.Value))
	}
	k := key.String()
	if f, ok := d.fonts[k]; ok {
		return f, f != nil
	}
	f := newPDFFont(db, face, vars)
	d.fonts[k] = f
	if f == nil {
		return nil, false
	}
	f.ref = d.w.alloc()
	d.fontOrder = append(d.fontOrder, f)
	return f, true
}

func newPDFFont(db *text.Database, face text.FaceID, vars []text.Variation) *pdfFont {
	data, index, ok := db.FaceData(face)
	if !ok {
		return nil
	}
	sf, ok := parseSfnt(data, index)
	if !ok {
		return nil
	}
	head, ok := sf.table("head")
	if !ok || len(head) < 54 {
		return nil
	}
	_, glyf := sf.table("glyf")
	_, cff := sf.table("CFF ")
	_, cff2 := sf.table("CFF2")
	f := &pdfFont{db: db, face: face, vars: vars, cids: map[uint16]uint16{}, type3: !glyf && !cff && !cff2}
	f.upem = float32(binary.BigEndian.Uint16(head[18:]))
	if f.upem == 0 {
		return nil
	}
	for i := range f.bbox {
		f.bbox[i] = float32(int16(binary.BigEndian.Uint16(head[36+2*i:])))
	}
	if !f.type3 {
		f.addCID(0)
	}
	return f
}

func (f *pdfFont) addCID(gid uint16) uint16 {
	cid := uint16(len(f.gids))
	f.gids = append(f.gids, gid)
	f.cids[gid] = cid
	f.texts = append(f.texts, "")
	f.hasTxt = append(f.hasTxt, false)
	w, _ := f.db.Advance(f.face, gid, f.vars)
	f.widths = append(f.widths, w)
	return cid
}

// addGlyph is CIDFont::add_glyph (Type3Font::add_glyph): the glyph's CID or code, assigned on first
// use; false when a Type 3 font already holds 256 glyphs (krilla starts another one; such glyphs are
// left out here).
func (f *pdfFont) addGlyph(gid uint16) (uint16, bool) {
	if cid, ok := f.cids[gid]; ok {
		return cid, true
	}
	if f.type3 && len(f.gids) == 256 {
		return 0, false
	}
	return f.addCID(gid), true
}

// outline is the glyph's outline in font units at the font's variations (krilla's glyph_path).
func (f *pdfFont) outline(gid uint16) *skia.Path {
	b := skia.NewPathBuilder()
	if !f.db.OutlineVaried(f.face, gid, f.vars, b) {
		return nil
	}
	return b.Finish()
}

// glyphBBox is get_glyphs_bbox for one glyph at the origin without advance: the font box at size,
// flipped, joined with the origin.
func (f *pdfFont) glyphBBox(size float32) skia.Rect {
	unit, _ := skia.RectFromXYWH(0, 0, 1, 1)
	r, ok := skia.RectFromLTRB(f.bbox[0], f.bbox[1], f.bbox[2], f.bbox[3])
	if !ok {
		r, _ = skia.RectFromXYWH(0, 0, f.upem, f.upem)
	}
	s := size / f.upem
	t, ok := r.Transform(skia.TransformFromScale(s, -s))
	if !ok {
		return unit
	}
	out, ok := skia.RectFromLTRB(min(t.Left(), 0), min(t.Top(), 0), max(t.Right(), 0), max(t.Bottom(), 0))
	if !ok {
		return unit
	}
	return out
}

// drawGlyph is Surface::draw_glyphs for one glyph at the origin with no advance (how krilla-svg draws
// every glyph): as text, or as a path when outlined.
func (s *surface) drawGlyph(f *pdfFont, gid uint16, txt string, size float32, outlined bool) {
	if outlined {
		s.outlineGlyph(f, gid, size)
		return
	}
	switch {
	case s.fill != nil && s.stroke != nil:
		if s.complexFillOrStroke() {
			// Fill as text and stroke as a path, so the text is not embedded twice.
			s.bd().drawGlyph(f, gid, txt, size, s.fill, nil)
			s.outlineGlyph(f, gid, size)
		} else {
			s.bd().drawGlyph(f, gid, txt, size, s.fill, s.stroke)
		}
	case s.stroke != nil:
		s.bd().drawGlyph(f, gid, txt, size, nil, s.stroke)
	case s.fill != nil:
		s.bd().drawGlyph(f, gid, txt, size, s.fill, nil)
	default:
		s.bd().drawGlyph(f, gid, txt, size, defaultFill(), nil)
	}
}

// outlineGlyph is outline_glyphs: the glyph outline drawn as a path with the current fill and stroke.
func (s *surface) outlineGlyph(f *pdfFont, gid uint16, size float32) {
	p := f.outline(gid)
	if p == nil {
		return
	}
	sc := size / f.upem
	if p = p.Transform(skia.TransformFromScale(sc, -sc)); p != nil {
		s.drawPath(p)
	}
}

// drawGlyph is ContentBuilder::draw_glyphs for one glyph.
func (b *builder) drawGlyph(f *pdfFont, gid uint16, txt string, size float32, fl *fill, st *stroke) {
	if fl == nil && st == nil {
		return
	}
	cid, ok := f.addGlyph(gid)
	if !ok {
		return
	}
	b.saveState()
	bbox := func(solid bool) skia.Rect {
		if b.bboxImportant || !solid {
			r := f.glyphBBox(size)
			b.expandBBox(r)
			return r
		}
		r, _ := skia.RectFromXYWH(0, 0, 1, 1)
		return r
	}
	if fl != nil && fl.paint.pattern == nil {
		b.setFillOpacity(fl.opacity)
	}
	if st != nil && st.paint.pattern == nil {
		b.setStrokeOpacity(st.opacity)
		b.setFillOpacity(st.opacity)
	}
	mode := "0 Tr"
	switch {
	case f.type3: // Type 3 glyphs are always filled
	case fl != nil && st != nil:
		mode = "2 Tr"
	case st != nil:
		mode = "1 Tr"
	}
	b.applyIsolatedOp(nil, func() {
		if fl != nil {
			b.setFillProps(bbox(fl.paint.solid()), fl)
		}
		if st != nil {
			b.setStrokeProps(bbox(st.paint.solid()), st)
		}
		b.op("BT")
		// use_text_spanner and GlyphSpanner for a single glyph: a CID that already maps to other text
		// gets an ActualText span; one without a mapping takes this glyph's text.
		spanned := false
		if f.hasTxt[cid] {
			spanned = f.texts[cid] != txt
		} else {
			f.texts[cid], f.hasTxt[cid] = txt, true
		}
		if spanned {
			b.op("/Span <</ActualText " + textString(txt) + ">> BDC")
		}
		b.op(mode)
		b.op(b.res.name(resFont, f.ref) + " " + num(size) + " Tf")
		b.op("1 0 0 -1 0 0 Tm")
		if f.type3 {
			b.op(fmt.Sprintf("<%02x> Tj", cid))
		} else {
			b.op(fmt.Sprintf("<%04x> Tj", cid))
		}
		if spanned {
			b.op("EMC")
		}
		b.op("ET")
	})
	b.restoreState()
}

// subsetTag is a six-letter tag from the glyph set, as subset font names carry.
func subsetTag(gids []uint16) string {
	h := fnv.New64a()
	for _, g := range gids {
		h.Write([]byte{byte(g >> 8), byte(g)})
	}
	v := h.Sum64()
	tag := make([]byte, 6)
	for i := range tag {
		tag[i] = 'A' + byte(v%26)
		v /= 26
	}
	return string(tag)
}

// writeFont writes the Type 0 font, its CID font, descriptor, embedded subset and ToUnicode CMap.
func (d *document) writeFont(f *pdfFont) {
	if f.type3 {
		d.writeType3(f)
		return
	}
	// A font that cannot be subset is written without a glyph program (viewers substitute one; the text
	// stays extractable). Not expected for faces that parse.
	sub := subsetFont(f)
	cidRef, descRef, cmapRef := d.w.alloc(), d.w.alloc(), d.w.alloc()
	psName := sub.postscriptName
	if psName == "" {
		psName = "unknown"
	}
	if len(psName) > 127-6-1-1-len("Identity-H") {
		psName = psName[:127-6-1-1-len("Identity-H")]
	}
	base := subsetTag(f.gids) + "+" + psName
	type0Base := base
	if sub.cff {
		type0Base = base + "-Identity-H"
	}
	d.w.obj(f.ref, "<</Type /Font /Subtype /Type0 /BaseFont /"+pdfName(type0Base)+" /Encoding /Identity-H /DescendantFonts ["+cidRef.String()+"] /ToUnicode "+cmapRef.String()+">>")

	toPDF := func(v float32) float32 { return float32(v/f.upem) * 1000 }
	var cid strings.Builder
	cid.WriteString("<</Type /Font")
	if sub.cff {
		cid.WriteString(" /Subtype /CIDFontType0")
	} else {
		cid.WriteString(" /Subtype /CIDFontType2")
	}
	cid.WriteString(" /BaseFont /" + pdfName(base) + " /CIDSystemInfo <</Registry (Adobe) /Ordering (Identity) /Supplement 0>> /FontDescriptor " + descRef.String() + " /DW 0")
	if !sub.cff {
		cid.WriteString(" /CIDToGIDMap /Identity")
	}
	// Runs of equal widths, zero widths left to /DW (krilla's grouping).
	cid.WriteString(" /W [")
	for first := 0; first < len(f.widths); {
		end := first
		for end < len(f.widths) && f.widths[end] == f.widths[first] {
			end++
		}
		if f.widths[first] != 0 {
			fmt.Fprintf(&cid, " %d %d %s", first, end-1, num(toPDF(f.widths[first])))
		}
		first = end
	}
	cid.WriteString(" ]>>")
	d.w.obj(cidRef, cid.String())

	flags := 4 | 1<<17 // symbolic, small cap (as krilla)
	if strings.Contains(sub.postscriptName, "Serif") {
		flags |= 2
	}
	if sub.fixedPitch {
		flags |= 1
	}
	if sub.italicAngle != 0 {
		flags |= 64
	}
	bbox := sub.bbox
	asc, desc := toPDF(sub.ascent), toPDF(sub.descent)
	capHeight := asc
	if sub.hasCapHeight {
		capHeight = toPDF(sub.capHeight)
	}
	stemV := 10 + float32(0.244*(sub.weight-50))
	desc_ := "<</Type /FontDescriptor /FontName /" + pdfName(base) + " /Flags " + itoa(flags) +
		" /FontBBox [" + nums(toPDF(bbox[0]), toPDF(bbox[1]), toPDF(bbox[2]), toPDF(bbox[3])) + "]" +
		" /ItalicAngle " + num(sub.italicAngle) + " /Ascent " + num(asc) + " /Descent " + num(desc) +
		" /CapHeight " + num(capHeight) + " /StemV " + num(stemV)
	if len(sub.data) > 0 {
		dataRef := d.w.alloc()
		if sub.cff {
			desc_ += " /FontFile3 " + dataRef.String()
			d.w.stream(dataRef, "/Subtype /CIDFontType0C", sub.data, true)
		} else {
			desc_ += " /FontFile2 " + dataRef.String()
			d.w.stream(dataRef, "/Length1 "+itoa(len(sub.data)), sub.data, true)
		}
	}
	d.w.obj(descRef, desc_+">>")
	d.w.stream(cmapRef, "/Type /CMap /CMapName /Custom /CIDSystemInfo <</Registry (Adobe) /Ordering (Identity) /Supplement 0>> /WMode 0", toUnicode(f.texts, 1, 2), true)
}

// writeType3 is Type3Font::serialize for glyphs without outlines: each glyph procedure only sets the
// advance (krilla draws a color or bitmap glyph there), the font matrix maps font units, and the
// ToUnicode CMap keeps the text, code 0 included.
func (d *document) writeType3(f *pdfFont) {
	res := &subsetResult{}
	if data, index, ok := f.db.FaceData(f.face); ok {
		if sf, ok := parseSfnt(data, index); ok {
			describe(sf, res)
		}
	}
	name := res.postscriptName
	if name == "" {
		name = "unknown"
	}
	if len(name) > 127-2-len("Identity-H") {
		name = name[:127-2-len("Identity-H")]
	}
	descRef, cmapRef := d.w.alloc(), d.w.alloc()
	var procs, diffs []string
	for i, w := range f.widths {
		r := d.w.alloc()
		d.w.stream(r, "", []byte(num(w)+" 0 d0"), true)
		procs = append(procs, fmt.Sprintf("/g%d %s", i, r))
		diffs = append(diffs, fmt.Sprintf("/g%d", i))
	}
	flags := 4 | 1<<17
	if strings.Contains(name, "Serif") {
		flags |= 2
	}
	if res.fixedPitch {
		flags |= 1
	}
	if res.italicAngle != 0 {
		flags |= 64
	}
	// krilla's box is that of the drawn bitmaps; nothing is drawn here, so the head box stands in for
	// them (text extraction groups glyphs into lines by it).
	bbox := "[" + nums(f.bbox[:]...) + "]"
	d.w.obj(descRef, "<</Type /FontDescriptor /FontName /"+pdfName(name)+" /Flags "+itoa(flags)+
		" /FontBBox "+bbox+" /ItalicAngle "+num(res.italicAngle)+" /Ascent "+num(f.bbox[3])+" /Descent "+num(f.bbox[1])+">>")
	m := 1 / f.upem
	d.w.obj(f.ref, "<</Type /Font /Subtype /Type3 /Name /"+pdfName(name)+" /FontBBox "+bbox+" /FontMatrix ["+nums(m, 0, 0, m, 0, 0)+
		"] /Resources <<>> /CharProcs <<"+strings.Join(procs, " ")+">> /Encoding <</Type /Encoding /Differences [0 "+strings.Join(diffs, " ")+
		"]>> /FirstChar 0 /LastChar "+itoa(len(f.widths)-1)+" /Widths ["+nums(f.widths...)+"] /FontDescriptor "+descRef.String()+
		" /ToUnicode "+cmapRef.String()+">>")
	d.w.stream(cmapRef, "/Type /CMap /CMapName /Custom /CIDSystemInfo <</Registry (Adobe) /Ordering (Identity) /Supplement 0>> /WMode 0", toUnicode(f.texts, 0, 1), true)
}

// pdfName escapes a name's delimiters and non-regular characters as #xx.
func pdfName(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x21 || c > 0x7e || strings.IndexByte("()<>[]{}/%#", c) >= 0 {
			fmt.Fprintf(&b, "#%02X", c)
		} else {
			b.WriteByte(c)
		}
	}
	return b.String()
}
