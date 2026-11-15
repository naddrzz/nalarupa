package text

// Reading of the few OpenType tables the font database and metrics need, with ttf-parser 0.25's rules
// (what fontdb, usvg and text.rs read through), so faces load, match and measure as in Rust. Shaping is
// go-text's job; this file never touches glyph data.

import (
	"encoding/binary"
	"io"
	"math"
	"sort"
	"unicode/utf16"
)

func u16(b []byte, at int) (uint16, bool) {
	if at < 0 || at+2 > len(b) {
		return 0, false
	}
	return binary.BigEndian.Uint16(b[at:]), true
}

func u32(b []byte, at int) (uint32, bool) {
	if at < 0 || at+4 > len(b) {
		return 0, false
	}
	return binary.BigEndian.Uint32(b[at:]), true
}

func i16(b []byte, at int) (int16, bool) {
	v, ok := u16(b, at)
	return int16(v), ok
}

func tag(s string) uint32 { return binary.BigEndian.Uint32([]byte(s)) }

// readAt reads n bytes at off, or fails when they are not all there.
func readAt(r io.ReaderAt, size, off, n int64) ([]byte, bool) {
	if off < 0 || n < 0 || off+n > size {
		return nil, false
	}
	b := make([]byte, n)
	if _, err := r.ReadAt(b, off); err != nil && !(err == io.EOF && n == 0) {
		return nil, false
	}
	return b, true
}

const (
	magicTrueType   = 0x00010000
	magicTrue       = 0x74727565 // 'true'
	magicOpenType   = 0x4F54544F // 'OTTO'
	magicCollection = 0x74746366 // 'ttcf'
)

func isFaceMagic(m uint32) bool { return m == magicTrueType || m == magicTrue || m == magicOpenType }

// fontsInCollection is the face count of a collection; false for a single font (ttf_parser::fonts_in_collection).
func fontsInCollection(r io.ReaderAt, size int64) (uint32, bool) {
	h, ok := readAt(r, size, 0, 12)
	if !ok || binary.BigEndian.Uint32(h) != magicCollection {
		return 0, false
	}
	return binary.BigEndian.Uint32(h[8:]), true
}

type tableRecord struct{ tag, off, len uint32 }

// rawFace is the table directory of one face (ttf_parser::RawFace).
type rawFace struct {
	r       io.ReaderAt
	size    int64
	records []tableRecord
}

func parseRawFace(r io.ReaderAt, size int64, index uint32) (*rawFace, bool) {
	head, ok := readAt(r, size, 0, 4)
	if !ok {
		return nil, false
	}
	pos := int64(4)
	switch m := binary.BigEndian.Uint32(head); {
	case m == magicCollection:
		h, ok := readAt(r, size, 8, 4)
		if !ok {
			return nil, false
		}
		n := int64(binary.BigEndian.Uint32(h))
		if int64(index) >= n {
			return nil, false
		}
		offs, ok := readAt(r, size, 12, 4*n)
		if !ok {
			return nil, false
		}
		pos = 12 + 4*n
		off := int64(binary.BigEndian.Uint32(offs[4*index:]))
		if off < pos || off > size {
			return nil, false
		}
		mb, ok := readAt(r, size, off, 4)
		if !ok || !isFaceMagic(binary.BigEndian.Uint32(mb)) {
			return nil, false
		}
		pos = off + 4
	case isFaceMagic(m):
		if index != 0 {
			return nil, false
		}
	default:
		return nil, false
	}
	nb, ok := readAt(r, size, pos, 2)
	if !ok {
		return nil, false
	}
	n := int64(binary.BigEndian.Uint16(nb))
	recs, ok := readAt(r, size, pos+8, 16*n)
	if !ok {
		return nil, false
	}
	f := &rawFace{r: r, size: size, records: make([]tableRecord, n)}
	for i := range f.records {
		b := recs[16*i:]
		f.records[i] = tableRecord{binary.BigEndian.Uint32(b), binary.BigEndian.Uint32(b[8:]), binary.BigEndian.Uint32(b[12:])}
	}
	return f, true
}

// table is the data of a table, found by binary search over the (sorted) directory.
func (f *rawFace) table(t uint32) ([]byte, bool) {
	i := sort.Search(len(f.records), func(i int) bool { return f.records[i].tag >= t })
	if i == len(f.records) || f.records[i].tag != t {
		return nil, false
	}
	return readAt(f.r, f.size, int64(f.records[i].off), int64(f.records[i].len))
}

// faceParses reports whether ttf_parser::Face::parse accepts the face: head, hhea and maxp must parse.
func (f *rawFace) faceParses() bool {
	head, ok := f.table(tag("head"))
	if !ok || len(head) < 54 {
		return false
	}
	upem, _ := u16(head, 18)
	loca, _ := u16(head, 50)
	if upem < 16 || upem > 16384 || loca > 1 {
		return false
	}
	if hhea, ok := f.table(tag("hhea")); !ok || len(hhea) < 36 {
		return false
	}
	maxp, ok := f.table(tag("maxp"))
	if !ok {
		return false
	}
	v, ok1 := u32(maxp, 0)
	n, ok2 := u16(maxp, 4)
	return ok1 && ok2 && (v == 0x00005000 || v == 0x00010000) && n != 0
}

// ---- name ----

type nameRecord struct {
	platform, encoding, language, id uint16
	data                             []byte
}

func (n nameRecord) isUnicode() bool {
	return n.platform == 0 || n.platform == 3 && (n.encoding == 0 || n.encoding == 1)
}

func (n nameRecord) isMacRoman() bool { return n.platform == 1 && n.encoding == 0 }

func (n nameRecord) isEnglishUS() bool {
	return n.platform == 3 && n.language == 0x0409 || n.platform == 1 && n.encoding == 0 && n.language == 0
}

// text decodes a Unicode (UTF-16BE) or Mac Roman name; false for other encodings or broken UTF-16.
func (n nameRecord) text() (string, bool) {
	switch {
	case n.isUnicode():
		u := make([]uint16, len(n.data)/2)
		for i := range u {
			u[i] = binary.BigEndian.Uint16(n.data[2*i:])
		}
		for i := 0; i < len(u); i++ { // String::from_utf16 rejects unpaired surrogates
			if utf16.IsSurrogate(rune(u[i])) {
				if u[i] >= 0xDC00 || i+1 == len(u) || u[i+1] < 0xDC00 || u[i+1] > 0xDFFF {
					return "", false
				}
				i++
			}
		}
		return string(utf16.Decode(u)), true
	case n.isMacRoman():
		r := make([]rune, len(n.data))
		for i, b := range n.data {
			r[i] = rune(macRoman[b])
		}
		return string(r), true
	}
	return "", false
}

// parseNames reads the name records up to the first unreadable one, where ttf-parser's iterator stops.
func parseNames(data []byte) ([]nameRecord, bool) {
	version, ok1 := u16(data, 0)
	count, ok2 := u16(data, 2)
	storageOff, ok3 := u16(data, 4)
	if !ok1 || !ok2 || !ok3 {
		return nil, false
	}
	pos := 6
	switch version {
	case 0:
	case 1:
		// ttf-parser reads langTagCount right after the header (before the records).
		lt, ok := u16(data, 6)
		if !ok || lt > 0xFFFF/4 {
			return nil, false
		}
		pos = 8 + 4*int(lt)
	default:
		return nil, false
	}
	if pos+12*int(count) > len(data) {
		return nil, false
	}
	recs := data[pos : pos+12*int(count)]
	pos += 12 * int(count)
	if pos < int(storageOff) {
		pos = int(storageOff)
	}
	if pos > len(data) {
		return nil, false
	}
	storage := data[pos:]
	var out []nameRecord
	for i := 0; i < int(count); i++ {
		b := recs[12*i:]
		r := nameRecord{binary.BigEndian.Uint16(b), binary.BigEndian.Uint16(b[2:]), binary.BigEndian.Uint16(b[4:]), binary.BigEndian.Uint16(b[6:]), nil}
		length, off := int(binary.BigEndian.Uint16(b[8:])), int(binary.BigEndian.Uint16(b[10:]))
		if r.platform > 4 || off+length > len(storage) {
			break
		}
		r.data = storage[off : off+length]
		out = append(out, r)
	}
	return out, true
}

// family is one family name of a face; enUS marks the English (US) one.
type family struct {
	name string
	enUS bool
}

// collectFamilies follows fontdb: Unicode names with this id, plus the first Mac Roman one when there is
// no English (US) name.
func collectFamilies(id uint16, names []nameRecord) []family {
	var out []family
	hasUS := false
	for _, n := range names {
		if n.id == id && n.isUnicode() {
			if s, ok := n.text(); ok {
				out = append(out, family{s, n.isEnglishUS()})
				hasUS = hasUS || n.isEnglishUS()
			}
		}
	}
	if !hasUS {
		for _, n := range names {
			if n.id == id && n.isMacRoman() {
				if s, ok := n.text(); ok {
					out = append(out, family{s, n.isEnglishUS()})
					break
				}
			}
		}
	}
	return out
}

// ---- OS/2, hhea, MVAR ----

// os2 holds the OS/2 table when ttf-parser accepts it (a known version and long enough).
type os2 []byte

func parseOS2(data []byte, ok bool) os2 {
	if !ok {
		return nil
	}
	v, ok := u16(data, 0)
	need := map[uint16]int{0: 78, 1: 86, 2: 96, 3: 96, 4: 96, 5: 100}[v]
	if !ok || need == 0 || len(data) < need {
		return nil
	}
	return os2(data)
}

func (t os2) version() uint16      { v, _ := u16(t, 0); return v }
func (t os2) selection() uint16    { v, _ := u16(t, 62); return v }
func (t os2) typoMetrics() bool    { return t.version() >= 4 && t.selection()&(1<<7) != 0 }
func (t os2) typoAscender() int16  { v, _ := i16(t, 68); return v }
func (t os2) typoDescender() int16 { v, _ := i16(t, 70); return v }
func (t os2) typoLineGap() int16   { v, _ := i16(t, 72); return v }
func (t os2) winAscender() int16   { v, _ := i16(t, 74); return v }
func (t os2) winDescender() int16  { v, _ := i16(t, 76); return -v }

func (t os2) style() style {
	switch s := t.selection(); {
	case s&1 != 0:
		return styleItalic
	case t.version() >= 4 && s&(1<<9) != 0:
		return styleOblique
	}
	return styleNormal
}

func (t os2) weight() uint16 { v, _ := u16(t, 4); return v }

func (t os2) stretch() uint16 {
	if v, _ := u16(t, 6); v >= 1 && v <= 9 {
		return v
	}
	return 5
}

// vMetrics are ttf-parser's ascender, descender and line gap (font units) at the given coordinates:
// typographic metrics when USE_TYPO_METRICS is set, else hhea with its OS/2 fallbacks, moved by MVAR.
func vMetrics(f *rawFace, coords []int16) (asc, desc, gap int16) {
	hhea, _ := f.table(tag("hhea"))
	ha, _ := i16(hhea, 4)
	hd, _ := i16(hhea, 6)
	hg, _ := i16(hhea, 8)
	o := parseOS2(f.table(tag("OS/2")))
	var mvar []byte
	if len(coords) > 0 {
		mvar, _ = f.table(tag("MVAR"))
	}
	vary := func(t string, v int16) int16 {
		if len(coords) == 0 {
			return v
		}
		// i16::try_num_from truncates toward zero and rejects out-of-range values.
		x := float32(v) + mvarOffset(mvar, tag(t), coords)
		if x >= -32768 && x <= 32767 {
			return int16(x)
		}
		return v
	}
	if o != nil && o.typoMetrics() {
		return vary("hasc", o.typoAscender()), vary("hdsc", o.typoDescender()), vary("hlgp", o.typoLineGap())
	}
	asc, desc, gap = ha, hd, hg
	if asc == 0 && o != nil {
		if asc = o.typoAscender(); asc == 0 {
			asc = vary("hcla", o.winAscender())
		} else {
			asc = vary("hasc", asc)
		}
	}
	if desc == 0 && o != nil {
		if desc = o.typoDescender(); desc == 0 {
			desc = vary("hcld", o.winDescender())
		} else {
			desc = vary("hdsc", desc)
		}
	}
	if (ha == 0 || hd == 0) && o != nil {
		if o.typoAscender() != 0 || o.typoDescender() != 0 {
			gap = vary("hlgp", o.typoLineGap())
		} else {
			gap = 0
		}
	}
	return asc, desc, gap
}

// mvarOffset is the MVAR delta of a metric (0 when absent), evaluated in float32 like ttf-parser.
func mvarOffset(data []byte, t uint32, coords []int16) float32 {
	if v, _ := u32(data, 0); v != 0x00010000 {
		return 0
	}
	recSize, _ := u16(data, 6)
	count, _ := u16(data, 8)
	storeOff, _ := u16(data, 10)
	if recSize != 8 || count == 0 || storeOff == 0 || 12+8*int(count) > len(data) || int(storeOff) > len(data) {
		return 0
	}
	recs := data[12 : 12+8*int(count)]
	i := sort.Search(int(count), func(i int) bool { return binary.BigEndian.Uint32(recs[8*i:]) >= t })
	if i == int(count) || binary.BigEndian.Uint32(recs[8*i:]) != t {
		return 0
	}
	outer, inner := binary.BigEndian.Uint16(recs[8*i+4:]), binary.BigEndian.Uint16(recs[8*i+6:])
	d, _ := itemDelta(data[storeOff:], outer, inner, coords)
	return d
}

// itemDelta reads one delta of an item variation store (ttf-parser ItemVariationStore::parse_delta).
func itemDelta(store []byte, outer, inner uint16, coords []int16) (float32, bool) {
	if f, ok := u16(store, 0); !ok || f != 1 {
		return 0, false
	}
	regionOff, ok1 := u32(store, 2)
	count, ok2 := u16(store, 6)
	if !ok1 || !ok2 || 8+4*int(count) > len(store) {
		return 0, false
	}
	axisCount, ok1 := u16(store, int(regionOff))
	regionCount, ok2 := u16(store, int(regionOff)+2)
	total := int(regionCount) * int(axisCount)
	if !ok1 || !ok2 || total > 0xFFFF || int(regionOff)+4+6*total > len(store) {
		return 0, false
	}
	regions := store[int(regionOff)+4 : int(regionOff)+4+6*total]
	if outer >= count {
		return 0, false
	}
	off := int(binary.BigEndian.Uint32(store[8+4*int(outer):]))
	itemCount, ok1 := u16(store, off)
	wordCount, ok2 := u16(store, off+2)
	indexCount, ok3 := u16(store, off+4)
	if !ok1 || !ok2 || !ok3 || off+6+2*int(indexCount) > len(store) {
		return 0, false
	}
	indices := store[off+6 : off+6+2*int(indexCount)]
	if inner >= itemCount {
		return 0, false
	}
	long := wordCount&0x8000 != 0
	wordCount &= 0x7FFF
	rowLen := int(uint16(wordCount + indexCount))
	if long {
		rowLen = int(uint16(rowLen * 2))
	}
	pos := off + 6 + 2*int(indexCount) + int(inner)*rowLen
	region := func(idx uint16) float32 {
		v := float32(1)
		for i, c := range coords {
			at := (int(idx)*int(axisCount) + i) * 6
			if at+6 > len(regions) {
				return 0
			}
			start, peak, end := int16(binary.BigEndian.Uint16(regions[at:])), int16(binary.BigEndian.Uint16(regions[at+2:])), int16(binary.BigEndian.Uint16(regions[at+4:]))
			f := axisFactor(start, peak, end, c)
			if f == 0 {
				return 0
			}
			v *= f
		}
		return v
	}
	var delta float32
	for i := 0; i < int(indexCount); i++ {
		idx := binary.BigEndian.Uint16(indices[2*i:])
		var num float32
		switch {
		case i < int(wordCount) && long:
			v, ok := u32(store, pos)
			if !ok {
				return 0, false
			}
			num, pos = float32(int32(v)), pos+4
		case i < int(wordCount) || long:
			v, ok := i16(store, pos)
			if !ok {
				return 0, false
			}
			num, pos = float32(v), pos+2
		default:
			if pos >= len(store) {
				return 0, false
			}
			num, pos = float32(int8(store[pos])), pos+1
		}
		delta += num * region(idx)
	}
	if int(wordCount) > int(indexCount) {
		return 0, false // ttf-parser fails reading a region index past the list
	}
	return delta, true
}

func axisFactor(start, peak, end, c int16) float32 {
	switch {
	case start > peak || peak > end:
		return 1
	case start < 0 && end > 0 && peak != 0:
		return 1
	case peak == 0 || c == peak:
		return 1
	case c <= start || end <= c:
		return 0
	case c < peak:
		return float32(c-start) / float32(peak-start)
	}
	return float32(end-c) / float32(end-peak)
}

// ---- variations ----

// wghtCoords are the normalized coordinates ttf-parser (and so rustybuzz) uses after
// `set_variation("wght", weight)`: other axes stay at their default, nil for a static face.
func wghtCoords(f *rawFace, weight uint16) []int16 {
	fvar, ok := f.table(tag("fvar"))
	if !ok {
		return nil
	}
	v, _ := u32(fvar, 0)
	axesOff, _ := u16(fvar, 4)
	n, _ := u16(fvar, 8)
	if v != 0x00010000 || n == 0 || int(axesOff)+20*int(n) > len(fvar) {
		return nil
	}
	coords := make([]int16, min(int(n), 64))
	if int(n) >= 64 {
		return coords
	}
	fixed := func(b []byte) float32 { return float32(int32(binary.BigEndian.Uint32(b))) / 65536 }
	avar, hasAvar := f.table(tag("avar"))
	for i := 0; i < int(n); i++ {
		a := fvar[int(axesOff)+20*i:]
		if binary.BigEndian.Uint32(a) != tag("wght") {
			continue
		}
		lo, def, hi := fixed(a[4:]), fixed(a[8:]), fixed(a[12:])
		lo, hi = float32(math.Min(float64(def), float64(lo))), float32(math.Max(float64(def), float64(hi)))
		w := float32(weight)
		w = max(lo, min(w, hi))
		switch {
		case w == def:
			w = 0
		case w < def:
			w = (w - def) / (def - lo)
		default:
			w = (w - def) / (hi - def)
		}
		coords[i] = int16(max(-1, min(w, 1)) * 16384)
		if hasAvar {
			avarMap(avar, coords, i)
		}
	}
	return coords
}

// avarMap applies the avar segment map of axis i (ttf-parser avar::Table::map_coordinate).
func avarMap(avar []byte, coords []int16, axis int) {
	if v, _ := u32(avar, 0); v != 0x00010000 {
		return
	}
	if n, ok := u16(avar, 6); !ok || int(n) != len(coords) {
		return
	}
	pos := 8
	for i := 0; ; i++ {
		count, ok := u16(avar, pos)
		if !ok || pos+2+4*int(count) > len(avar) {
			return
		}
		m := avar[pos+2 : pos+2+4*int(count)]
		pos += 2 + 4*int(count)
		if i < axis {
			continue
		}
		from := func(j int) int16 { return int16(binary.BigEndian.Uint16(m[4*j:])) }
		to := func(j int) int16 { return int16(binary.BigEndian.Uint16(m[4*j+2:])) }
		value, l := coords[axis], int(count)
		var out int16
		switch {
		case l == 0:
			out = value
		case l == 1 || value <= from(0):
			out = value - from(0) + to(0)
		default:
			j := 1
			for j < l && value > from(j) {
				j++
			}
			if j == l {
				j--
			}
			if value >= from(j) {
				out = value - from(j) + to(j)
			} else if from(j-1) == from(j) {
				out = to(j - 1)
			} else {
				denom := int32(from(j)) - int32(from(j-1))
				k := (int32(to(j))-int32(to(j-1)))*(int32(value)-int32(from(j-1))) + denom/2
				r := int32(to(j-1)) + k/denom
				if r < -32768 || r > 32767 {
					return
				}
				out = int16(r)
			}
		}
		coords[axis] = max(-16384, min(out, 16384))
		return
	}
}

func isVariable(f *rawFace) bool {
	fvar, ok := f.table(tag("fvar"))
	if !ok {
		return false
	}
	v, _ := u32(fvar, 0)
	axesOff, _ := u16(fvar, 4)
	n, _ := u16(fvar, 8)
	return v == 0x00010000 && n != 0 && int(axesOff)+20*int(n) <= len(fvar)
}

// ---- cmap ----

// glyphIndex is ttf_parser::Face::glyph_index: the first Unicode subtable that maps c, up to the first
// subtable ttf-parser cannot read.
func glyphIndex(cmap []byte, c rune) bool {
	count, ok := u16(cmap, 2)
	if !ok || 4+8*int(count) > len(cmap) {
		return false
	}
	for i := 0; i < int(count); i++ {
		rec := cmap[4+8*i:]
		platform, enc, off := binary.BigEndian.Uint16(rec), binary.BigEndian.Uint16(rec[2:]), int(binary.BigEndian.Uint32(rec[4:]))
		if platform > 4 || off > len(cmap) {
			return false
		}
		sub := cmap[off:]
		format, ok := u16(sub, 0)
		if !ok {
			return false
		}
		lookup, ok := cmapSubtable(format, sub)
		if !ok {
			return false
		}
		unicode := platform == 0 || platform == 3 && (enc == 1 || enc == 10 && (format == 12 || format == 13))
		if unicode && lookup != nil && lookup(uint32(c)) {
			return true
		}
	}
	return false
}

// cmapSubtable parses a subtable like ttf-parser; the lookup is nil for formats that map nothing here.
func cmapSubtable(format uint16, d []byte) (func(uint32) bool, bool) {
	switch format {
	case 0:
		if len(d) < 6+256 {
			return nil, false
		}
		return func(c uint32) bool { return c < 256 && d[6+c] != 0 }, true
	case 2:
		if len(d) < 6+512 {
			return nil, false
		}
		return nil, true // CJK legacy encodings, never in a Unicode subtable
	case 4:
		segX2, ok := u16(d, 6)
		if !ok || segX2 < 2 {
			return nil, false
		}
		seg := int(segX2 / 2)
		ends, starts, deltas, ranges := 14, 16+2*seg, 16+4*seg, 16+6*seg
		if ranges+2*seg > len(d) {
			return nil, false
		}
		return func(c uint32) bool {
			if c > 0xFFFF {
				return false
			}
			cp := uint16(c)
			lo, hi := 0, seg
			for hi > lo {
				i := (lo + hi) / 2
				if end, _ := u16(d, ends+2*i); end >= cp {
					start, _ := u16(d, starts+2*i)
					if start > cp {
						hi = i
						continue
					}
					ro, _ := u16(d, ranges+2*i)
					delta, _ := u16(d, deltas+2*i)
					if ro == 0 {
						return true
					} else if ro == 0xFFFF {
						return false
					}
					dd := (uint32(cp) - uint32(start)) * 2
					if dd > 0xFFFF {
						return false
					}
					p := uint16(ranges+2*i) + uint16(dd) + ro
					g, ok := u16(d, int(p))
					if !ok || g == 0 {
						return false
					}
					return int16(g)+int16(delta) >= 0
				} else {
					lo = i + 1
				}
			}
			return false
		}, true
	case 6:
		first, ok1 := u16(d, 6)
		n, ok2 := u16(d, 8)
		if !ok1 || !ok2 || 10+2*int(n) > len(d) {
			return nil, false
		}
		return func(c uint32) bool {
			if c > 0xFFFF || c < uint32(first) || c-uint32(first) >= uint32(n) {
				return false
			}
			return true
		}, true
	case 8:
		return nil, true
	case 10:
		first, ok1 := u32(d, 12)
		n, ok2 := u32(d, 16)
		if !ok1 || !ok2 || 20+2*int64(n) > int64(len(d)) {
			return nil, false
		}
		return func(c uint32) bool {
			if c < first || c-first >= n {
				return false
			}
			return true
		}, true
	case 12, 13:
		n, ok := u32(d, 12)
		if !ok || 16+12*int64(n) > int64(len(d)) {
			return nil, false
		}
		group := func(i int) (uint32, uint32, uint32) {
			b := d[16+12*i:]
			return binary.BigEndian.Uint32(b), binary.BigEndian.Uint32(b[4:]), binary.BigEndian.Uint32(b[8:])
		}
		if format == 13 {
			return func(c uint32) bool {
				for i := 0; i < int(n); i++ {
					if s, e, g := group(i); c >= s && c <= e {
						return g <= 0xFFFF
					}
				}
				return false
			}, true
		}
		return func(c uint32) bool {
			i := sort.Search(int(n), func(i int) bool { _, e, _ := group(i); return e >= c })
			if i == int(n) {
				return false
			}
			s, _, g := group(i)
			if c < s {
				return false
			}
			sum := uint64(g) + uint64(c)
			return sum <= math.MaxUint32 && sum-uint64(s) <= 0xFFFF
		}, true
	case 14:
		n, ok := u32(d, 6)
		if !ok || 10+11*int64(n) > int64(len(d)) {
			return nil, false
		}
		return nil, true
	}
	return nil, false
}

// macRoman maps Mac OS Roman bytes to UTF-16.
var macRoman = [256]uint16{
	0x0000, 0x0001, 0x0002, 0x0003, 0x0004, 0x0005, 0x0006, 0x0007, 0x0008, 0x0009, 0x000A, 0x000B, 0x000C, 0x000D, 0x000E, 0x000F,
	0x0010, 0x2318, 0x21E7, 0x2325, 0x2303, 0x0015, 0x0016, 0x0017, 0x0018, 0x0019, 0x001A, 0x001B, 0x001C, 0x001D, 0x001E, 0x001F,
	0x0020, 0x0021, 0x0022, 0x0023, 0x0024, 0x0025, 0x0026, 0x0027, 0x0028, 0x0029, 0x002A, 0x002B, 0x002C, 0x002D, 0x002E, 0x002F,
	0x0030, 0x0031, 0x0032, 0x0033, 0x0034, 0x0035, 0x0036, 0x0037, 0x0038, 0x0039, 0x003A, 0x003B, 0x003C, 0x003D, 0x003E, 0x003F,
	0x0040, 0x0041, 0x0042, 0x0043, 0x0044, 0x0045, 0x0046, 0x0047, 0x0048, 0x0049, 0x004A, 0x004B, 0x004C, 0x004D, 0x004E, 0x004F,
	0x0050, 0x0051, 0x0052, 0x0053, 0x0054, 0x0055, 0x0056, 0x0057, 0x0058, 0x0059, 0x005A, 0x005B, 0x005C, 0x005D, 0x005E, 0x005F,
	0x0060, 0x0061, 0x0062, 0x0063, 0x0064, 0x0065, 0x0066, 0x0067, 0x0068, 0x0069, 0x006A, 0x006B, 0x006C, 0x006D, 0x006E, 0x006F,
	0x0070, 0x0071, 0x0072, 0x0073, 0x0074, 0x0075, 0x0076, 0x0077, 0x0078, 0x0079, 0x007A, 0x007B, 0x007C, 0x007D, 0x007E, 0x007F,
	0x00C4, 0x00C5, 0x00C7, 0x00C9, 0x00D1, 0x00D6, 0x00DC, 0x00E1, 0x00E0, 0x00E2, 0x00E4, 0x00E3, 0x00E5, 0x00E7, 0x00E9, 0x00E8,
	0x00EA, 0x00EB, 0x00ED, 0x00EC, 0x00EE, 0x00EF, 0x00F1, 0x00F3, 0x00F2, 0x00F4, 0x00F6, 0x00F5, 0x00FA, 0x00F9, 0x00FB, 0x00FC,
	0x2020, 0x00B0, 0x00A2, 0x00A3, 0x00A7, 0x2022, 0x00B6, 0x00DF, 0x00AE, 0x00A9, 0x2122, 0x00B4, 0x00A8, 0x2260, 0x00C6, 0x00D8,
	0x221E, 0x00B1, 0x2264, 0x2265, 0x00A5, 0x00B5, 0x2202, 0x2211, 0x220F, 0x03C0, 0x222B, 0x00AA, 0x00BA, 0x03A9, 0x00E6, 0x00F8,
	0x00BF, 0x00A1, 0x00AC, 0x221A, 0x0192, 0x2248, 0x2206, 0x00AB, 0x00BB, 0x2026, 0x00A0, 0x00C0, 0x00C3, 0x00D5, 0x0152, 0x0153,
	0x2013, 0x2014, 0x201C, 0x201D, 0x2018, 0x2019, 0x00F7, 0x25CA, 0x00FF, 0x0178, 0x2044, 0x20AC, 0x2039, 0x203A, 0xFB01, 0xFB02,
	0x2021, 0x00B7, 0x201A, 0x201E, 0x2030, 0x00C2, 0x00CA, 0x00C1, 0x00CB, 0x00C8, 0x00CD, 0x00CE, 0x00CF, 0x00CC, 0x00D3, 0x00D4,
	0xF8FF, 0x00D2, 0x00DA, 0x00DB, 0x00D9, 0x0131, 0x02C6, 0x02DC, 0x00AF, 0x02D8, 0x02D9, 0x02DA, 0x00B8, 0x02DD, 0x02DB, 0x02C7,
}
