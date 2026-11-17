package text

// CFF (version 1) glyph outlines as ttf-parser 0.25 produces them (tables/cff: cff1.rs, charstring.rs,
// index.rs, dict.rs, charset.rs, encoding.rs): the Type 2 charstring interpreter with f32 arithmetic,
// subroutines, CID fonts and seac accents. CFF2 outlines still come from go-text.

import (
	"encoding/binary"
	"strconv"
)

// ---- INDEX (index.rs) ----

type cffIndex struct {
	data       []byte
	offsets    []byte
	offsetSize int
}

func (x cffIndex) offsetsLen() uint32 {
	if x.offsetSize == 0 {
		return 0
	}
	return uint32(len(x.offsets) / x.offsetSize)
}

// offset is VarOffsets::get: the offset minus one, false past the end.
func (x cffIndex) offset(i uint32) (uint32, bool) {
	if i >= x.offsetsLen() {
		return 0, false
	}
	start := int(i) * x.offsetSize
	b := x.offsets[start : start+x.offsetSize]
	var n uint32
	for _, c := range b {
		n = n<<8 | uint32(c)
	}
	if n == 0 {
		return 0, false
	}
	return n - 1, true
}

// len is Index::len: the offsets count minus one.
func (x cffIndex) len() uint32 {
	if n := x.offsetsLen(); n > 0 {
		return n - 1
	}
	return 0
}

func (x cffIndex) get(i uint32) ([]byte, bool) {
	if i == 0xFFFFFFFF {
		return nil, false
	}
	start, ok1 := x.offset(i)
	end, ok2 := x.offset(i + 1)
	if !ok1 || !ok2 || start > end || int(end) > len(x.data) {
		return nil, false
	}
	return x.data[start:end], true
}

// parseCFFIndex is parse_index::<u16>.
func parseCFFIndex(s *stream) (cffIndex, bool) {
	count, ok := s.u16()
	if !ok {
		return cffIndex{}, false
	}
	if count == 0 {
		return cffIndex{offsetSize: 1}, true
	}
	size, ok := s.u8()
	if !ok || size < 1 || size > 4 {
		return cffIndex{}, false
	}
	offsetsLen := (int(count) + 1) * int(size)
	offsets, ok := s.readBytes(offsetsLen)
	if !ok {
		return cffIndex{}, false
	}
	idx := cffIndex{offsets: offsets, offsetSize: int(size)}
	last, ok := idx.offset(idx.offsetsLen() - 1)
	if !ok {
		return cffIndex{offsetSize: 1}, true
	}
	data, ok := s.readBytes(int(last))
	if !ok {
		return cffIndex{}, false
	}
	idx.data = data
	return idx, true
}

// skipCFFIndex is skip_index::<u16>.
func skipCFFIndex(s *stream) bool {
	count, ok := s.u16()
	if !ok {
		return false
	}
	if count == 0 {
		return true
	}
	size, ok := s.u8()
	if !ok || size < 1 || size > 4 {
		return false
	}
	offsets, ok := s.readBytes((int(count) + 1) * int(size))
	if !ok {
		return false
	}
	idx := cffIndex{offsets: offsets, offsetSize: int(size)}
	if last, ok := idx.offset(idx.offsetsLen() - 1); ok {
		s.offset += int(last)
	}
	return true
}

// ---- DICT (dict.rs) ----

type cffDict struct {
	data           []byte
	offset         int
	operandsOffset int
	operands       []float64
}

func cffIsOneByteOp(b byte) bool { return b <= 27 || b == 31 || b == 255 }

func (d *cffDict) next() (uint16, bool) {
	s, ok := newStreamAt(d.data, d.offset)
	if !ok {
		return 0, false
	}
	d.operandsOffset = d.offset
	for s.offset < len(s.data) {
		b, _ := s.u8()
		if cffIsOneByteOp(b) {
			op := uint16(b)
			if b == 12 {
				b2, ok := s.u8()
				if !ok {
					return 0, false
				}
				op = 1200 + uint16(b2)
			}
			d.offset = s.offset
			return op, true
		}
		if !cffSkipNumber(b, &s) {
			return 0, false
		}
	}
	return 0, false
}

func (d *cffDict) parseOperands() bool {
	s, ok := newStreamAt(d.data, d.operandsOffset)
	if !ok {
		return false
	}
	d.operands = d.operands[:0]
	for s.offset < len(s.data) {
		b, _ := s.u8()
		if cffIsOneByteOp(b) {
			break
		}
		v, ok := cffParseNumber(b, &s)
		if !ok {
			return false
		}
		d.operands = append(d.operands, v)
		if len(d.operands) >= 48 {
			break
		}
	}
	return true
}

func (d *cffDict) number() (float64, bool) {
	if !d.parseOperands() || len(d.operands) == 0 {
		return 0, false
	}
	return d.operands[0], true
}

// asUsize is usize::try_from(n as i32).
func asUsize(n float64) (int, bool) {
	var i int32
	switch {
	case n != n:
		i = 0
	case n >= 2147483647:
		i = 2147483647
	case n <= -2147483648:
		i = -2147483648
	default:
		i = int32(n)
	}
	return int(i), i >= 0
}

func (d *cffDict) offsetOperand() (int, bool) {
	if !d.parseOperands() || len(d.operands) != 1 {
		return 0, false
	}
	return asUsize(d.operands[0])
}

func (d *cffDict) rangeOperand() (int, int, bool) {
	if !d.parseOperands() || len(d.operands) != 2 {
		return 0, 0, false
	}
	n, ok1 := asUsize(d.operands[0])
	start, ok2 := asUsize(d.operands[1])
	if !ok1 || !ok2 {
		return 0, 0, false
	}
	return start, start + n, true
}

func cffParseNumber(b0 byte, s *stream) (float64, bool) {
	switch {
	case b0 == 28:
		v, ok := s.i16()
		return float64(v), ok
	case b0 == 29:
		v, ok := s.u32()
		return float64(int32(v)), ok
	case b0 == 30:
		return cffParseFloat(s)
	case b0 >= 32 && b0 <= 246:
		return float64(int32(b0) - 139), true
	case b0 >= 247 && b0 <= 250:
		b1, ok := s.u8()
		return float64((int32(b0)-247)*256 + int32(b1) + 108), ok
	case b0 >= 251 && b0 <= 254:
		b1, ok := s.u8()
		return float64(-(int32(b0)-251)*256 - int32(b1) - 108), ok
	}
	return 0, false
}

func cffParseFloat(s *stream) (float64, bool) {
	var buf []byte
	nibble := func(n byte) bool {
		if len(buf) == 64 {
			return false
		}
		switch {
		case n <= 9:
			buf = append(buf, '0'+n)
		case n == 10:
			buf = append(buf, '.')
		case n == 11:
			buf = append(buf, 'E')
		case n == 12:
			if len(buf)+1 == 64 {
				return false
			}
			buf = append(buf, 'E', '-')
		case n == 14:
			buf = append(buf, '-')
		default:
			return false
		}
		return true
	}
	for {
		b, ok := s.u8()
		if !ok {
			return 0, false
		}
		n1, n2 := b>>4, b&15
		if n1 == 15 {
			break
		}
		if !nibble(n1) {
			return 0, false
		}
		if n2 == 15 {
			break
		}
		if !nibble(n2) {
			return 0, false
		}
	}
	v, err := strconv.ParseFloat(string(buf), 64)
	if err != nil {
		if ne, ok := err.(*strconv.NumError); !ok || ne.Err != strconv.ErrRange {
			return 0, false
		}
	}
	return v, true
}

func cffSkipNumber(b0 byte, s *stream) bool {
	switch {
	case b0 == 28:
		s.offset += 2
	case b0 == 29:
		s.offset += 4
	case b0 == 30:
		for s.offset < len(s.data) {
			b, _ := s.u8()
			if b>>4 == 15 || b&15 == 15 {
				break
			}
		}
	case b0 >= 32 && b0 <= 246:
	case b0 >= 247 && b0 <= 254:
		s.offset++
	default:
		return false
	}
	return true
}

// ---- charset (charset.rs) and the standard encoding (encoding.rs) ----

type cffCharset struct {
	kind uint8 // 0 ISOAdobe, 1 Expert, 2 ExpertSubset, 3 format 0, 4 format 1, 5 format 2
	data []byte
}

func parseCFFCharset(numGlyphs uint16, s *stream) (cffCharset, bool) {
	format, ok := s.u8()
	if !ok {
		return cffCharset{}, false
	}
	switch format {
	case 0:
		b, ok := s.readBytes(int(numGlyphs-1) * 2)
		return cffCharset{kind: 3, data: b}, ok
	case 1, 2:
		left := int(numGlyphs) - 1
		count := 0
		probe := *s
		for left > 0 {
			probe.offset += 2
			var n int
			if format == 1 {
				v, ok := probe.u8()
				if !ok {
					return cffCharset{}, false
				}
				n = int(v) + 1
			} else {
				v, ok := probe.u16()
				if !ok || v == 0xFFFF {
					return cffCharset{}, false
				}
				n = int(v) + 1
			}
			if n > left {
				return cffCharset{}, false
			}
			left -= n
			count++
		}
		if count > 0xFFFF {
			return cffCharset{}, false
		}
		size := 3
		if format == 2 {
			size = 4
		}
		b, ok := s.readBytes(count * size)
		return cffCharset{kind: 3 + format, data: b}, ok
	}
	return cffCharset{}, false
}

func (c cffCharset) sidToGID(sid uint16) (uint16, bool) {
	if sid == 0 {
		return 0, true
	}
	switch c.kind {
	case 3:
		for i := 0; i+2 <= len(c.data); i += 2 {
			if binary.BigEndian.Uint16(c.data[i:]) == sid {
				return uint16(i/2 + 1), true
			}
		}
	case 4, 5:
		size := 3
		if c.kind == 5 {
			size = 4
		}
		gid := uint16(1)
		for i := 0; i+size <= len(c.data); i += size {
			first := binary.BigEndian.Uint16(c.data[i:])
			var left uint16
			if size == 3 {
				left = uint16(c.data[i+2])
			} else {
				left = binary.BigEndian.Uint16(c.data[i+2:])
			}
			if first <= sid && uint32(sid) <= uint32(first)+uint32(left) {
				return gid + (sid - first), true
			}
			gid += left + 1
		}
	}
	return 0, false
}

var cffStandardEncoding = [256]uint8{
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16,
	17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32,
	33, 34, 35, 36, 37, 38, 39, 40, 41, 42, 43, 44, 45, 46, 47, 48,
	49, 50, 51, 52, 53, 54, 55, 56, 57, 58, 59, 60, 61, 62, 63, 64,
	65, 66, 67, 68, 69, 70, 71, 72, 73, 74, 75, 76, 77, 78, 79, 80,
	81, 82, 83, 84, 85, 86, 87, 88, 89, 90, 91, 92, 93, 94, 95, 0,
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	0, 96, 97, 98, 99, 100, 101, 102, 103, 104, 105, 106, 107, 108, 109, 110,
	0, 111, 112, 113, 114, 0, 115, 116, 117, 118, 119, 120, 121, 122, 0, 123,
	0, 124, 125, 126, 127, 128, 129, 130, 131, 0, 132, 133, 0, 134, 135, 136,
	137, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	0, 138, 0, 139, 0, 0, 0, 0, 140, 141, 142, 143, 0, 0, 0, 0,
	0, 144, 0, 0, 0, 145, 0, 0, 146, 147, 148, 149, 0, 0, 0, 0,
}

// parseCFFEncoding is parse_encoding, kept only for its failures (an unparsable encoding fails the
// whole table).
func parseCFFEncoding(s *stream) bool {
	format, ok := s.u8()
	if !ok {
		return false
	}
	supplemental := format&0x80 != 0
	format &= 0x7f
	count, ok := s.u8()
	if !ok {
		return false
	}
	switch format {
	case 0:
		if _, ok := s.readBytes(int(count)); !ok {
			return false
		}
	case 1:
		if _, ok := s.readBytes(int(count) * 2); !ok {
			return false
		}
	default:
		return false
	}
	if supplemental {
		n, ok := s.u8()
		if !ok {
			return false
		}
		if _, ok := s.readBytes(int(n) * 3); !ok {
			return false
		}
	}
	return true
}

// ---- the table (cff1.rs) ----

type cffTable struct {
	data        []byte
	globalSubrs cffIndex
	charset     cffCharset
	charStrings cffIndex
	cid         bool
	localSubrs  cffIndex // SID fonts
	fdArray     cffIndex // CID fonts
	fdSelect    []byte   // CID fonts: the format byte, then the data
	numGlyphs   uint16
}

func parseCFF(data []byte) (*cffTable, bool) {
	s := stream{data: data}
	major, ok1 := s.u8()
	s.offset++
	headerSize, ok2 := s.u8()
	s.offset++
	if !ok1 || !ok2 || major != 1 {
		return nil, false
	}
	if headerSize > 4 {
		s.offset += int(headerSize) - 4
	}
	if !skipCFFIndex(&s) {
		return nil, false
	}
	// Top DICT.
	topIndex, ok := parseCFFIndex(&s)
	if !ok {
		return nil, false
	}
	topData, ok := topIndex.get(0)
	if !ok {
		return nil, false
	}
	var charsetOff, encodingOff, charStringsOff, fdArrayOff, fdSelectOff int
	hasCharset, hasEncoding, hasFDArray, hasFDSelect, hasROS := false, false, false, false, false
	privStart, privEnd, hasPrivate := 0, 0, false
	d := cffDict{data: topData}
	for {
		op, ok := d.next()
		if !ok {
			break
		}
		switch op {
		case 15:
			charsetOff, hasCharset = d.offsetOperand()
		case 16:
			encodingOff, hasEncoding = d.offsetOperand()
		case 17:
			if charStringsOff, ok = d.offsetOperand(); !ok {
				return nil, false
			}
		case 18:
			privStart, privEnd, hasPrivate = d.rangeOperand()
		case 1207:
			if !d.parseOperands() {
				return nil, false
			}
		case 1230:
			hasROS = true
		case 1236:
			fdArrayOff, hasFDArray = d.offsetOperand()
		case 1237:
			fdSelectOff, hasFDSelect = d.offsetOperand()
		}
	}
	if charStringsOff == 0 {
		return nil, false
	}
	if _, ok := parseCFFIndex(&s); !ok { // String INDEX
		return nil, false
	}
	t := &cffTable{data: data}
	if t.globalSubrs, ok = parseCFFIndex(&s); !ok {
		return nil, false
	}
	cs, ok := newStreamAt(data, charStringsOff)
	if !ok {
		return nil, false
	}
	if t.charStrings, ok = parseCFFIndex(&cs); !ok {
		return nil, false
	}
	n := t.charStrings.len()
	if n == 0 || n > 0xFFFF {
		return nil, false
	}
	t.numGlyphs = uint16(n)
	switch {
	case !hasCharset || charsetOff == 0:
		t.charset = cffCharset{kind: 0}
	case charsetOff == 1:
		t.charset = cffCharset{kind: 1}
	case charsetOff == 2:
		t.charset = cffCharset{kind: 2}
	default:
		cs, ok := newStreamAt(data, charsetOff)
		if !ok {
			return nil, false
		}
		if t.charset, ok = parseCFFCharset(t.numGlyphs, &cs); !ok {
			return nil, false
		}
	}
	if hasROS {
		if !hasCharset || !hasFDArray || !hasFDSelect || charsetOff <= 2 {
			return nil, false
		}
		t.cid = true
		fa, ok := newStreamAt(data, fdArrayOff)
		if !ok {
			return nil, false
		}
		if t.fdArray, ok = parseCFFIndex(&fa); !ok {
			return nil, false
		}
		fs, ok := newStreamAt(data, fdSelectOff)
		if !ok {
			return nil, false
		}
		format, ok := fs.u8()
		if !ok {
			return nil, false
		}
		switch format {
		case 0:
			b, ok := fs.readBytes(int(t.numGlyphs))
			if !ok {
				return nil, false
			}
			t.fdSelect = append([]byte{0}, b...)
		case 3:
			tail, _ := fs.tail()
			t.fdSelect = append([]byte{3}, tail...)
		default:
			return nil, false
		}
		return t, true
	}
	if hasEncoding && encodingOff > 1 {
		es, ok := newStreamAt(data, encodingOff)
		if !ok || !parseCFFEncoding(&es) {
			return nil, false
		}
	}
	if hasPrivate {
		if privStart > privEnd || privEnd > len(data) {
			return nil, false
		}
		subrsOff, ok := cffPrivateSubrs(data[privStart:privEnd])
		if ok && privStart+subrsOff >= privStart {
			start := privStart + subrsOff
			if start > len(data) {
				return nil, false
			}
			ls := stream{data: data[start:]}
			if t.localSubrs, ok = parseCFFIndex(&ls); !ok {
				return nil, false
			}
		}
	}
	return t, true
}

// cffPrivateSubrs is the local subroutines offset of a Private DICT.
func cffPrivateSubrs(data []byte) (int, bool) {
	d := cffDict{data: data}
	off, has := 0, false
	for {
		op, ok := d.next()
		if !ok {
			return off, has
		}
		switch op {
		case 19:
			off, has = d.offsetOperand()
		case 20, 21:
			d.number()
		}
	}
}

// cidLocalSubrs is parse_cid_local_subrs.
func (t *cffTable) cidLocalSubrs(gid uint16) (cffIndex, bool) {
	fd, ok := t.fontDictIndex(gid)
	if !ok {
		return cffIndex{}, false
	}
	fdData, ok := t.fdArray.get(uint32(fd))
	if !ok {
		return cffIndex{}, false
	}
	d := cffDict{data: fdData}
	start, end, found := 0, 0, false
	for {
		op, ok := d.next()
		if !ok {
			break
		}
		if op == 18 {
			start, end, found = d.rangeOperand()
			break
		}
	}
	if !found || start > end || end > len(t.data) {
		return cffIndex{}, false
	}
	off, ok := cffPrivateSubrs(t.data[start:end])
	if !ok {
		return cffIndex{}, false
	}
	at := start + off
	if at > len(t.data) {
		return cffIndex{}, false
	}
	s := stream{data: t.data[at:]}
	return parseCFFIndex(&s)
}

func (t *cffTable) fontDictIndex(gid uint16) (uint8, bool) {
	if len(t.fdSelect) == 0 {
		return 0, false
	}
	if t.fdSelect[0] == 0 {
		if int(gid) < len(t.fdSelect)-1 {
			return t.fdSelect[1+int(gid)], true
		}
		return 0, false
	}
	s := stream{data: t.fdSelect[1:]}
	ranges, ok := s.u16()
	if !ok || ranges == 0 || ranges == 0xFFFF {
		return 0, false
	}
	prevFirst, ok1 := s.u16()
	prevIndex, ok2 := s.u8()
	if !ok1 || !ok2 {
		return 0, false
	}
	for i := uint16(1); i < ranges+1; i++ {
		first, ok := s.u16()
		if !ok {
			return 0, false
		}
		if gid >= prevFirst && gid < first {
			return prevIndex, true
		}
		if prevIndex, ok = s.u8(); !ok {
			return 0, false
		}
		prevFirst = first
	}
	return 0, false
}

func cffSubrBias(n uint32) int32 {
	switch {
	case n < 1240:
		return 107
	case n < 33900:
		return 1131
	}
	return 32768
}

// cffSubrIndex is conv_subroutine_index.
func cffSubrIndex(v float32, bias int32) (uint32, bool) {
	if !(v > -2147483904 && v < 2147483648) {
		return 0, false
	}
	i := int64(int32(v)) + int64(bias)
	if i < 0 || i > 0x7FFFFFFF {
		return 0, false
	}
	return uint32(i), true
}

// cffParser is CharStringParser with its context and Builder.
type cffParser struct {
	t             *cffTable
	b             OutlineBuilder
	bbox          rectF
	stack         []float32
	x, y          float32
	hasMoveTo     bool
	firstMoveTo   bool
	width         bool // a width was seen
	stemsLen      uint32
	hasEndchar    bool
	hasSeac       bool
	gid           uint16
	localSubrs    cffIndex
	hasLocalSubrs bool
}

func (p *cffParser) moveTo(x, y float32) { p.bbox.extend(x, y); p.b.MoveTo(x, y) }
func (p *cffParser) lineTo(x, y float32) { p.bbox.extend(x, y); p.b.LineTo(x, y) }
func (p *cffParser) curveTo(x1, y1, x2, y2, x, y float32) {
	p.bbox.extend(x1, y1)
	p.bbox.extend(x2, y2)
	p.bbox.extend(x, y)
	p.b.CubicTo(x1, y1, x2, y2, x, y)
}

func (p *cffParser) push(v float32) bool {
	if len(p.stack) == 48 {
		return false
	}
	p.stack = append(p.stack, v)
	return true
}

func (p *cffParser) pop() float32 {
	v := p.stack[len(p.stack)-1]
	p.stack = p.stack[:len(p.stack)-1]
	return v
}

// outline is Table::outline: false for any CFFError.
func (t *cffTable) outline(gid uint16, b OutlineBuilder) bool {
	data, ok := t.charStrings.get(uint32(gid))
	if !ok {
		return false
	}
	p := &cffParser{t: t, b: b, bbox: newRectF(), firstMoveTo: true, gid: gid, stack: make([]float32, 0, 48)}
	if !t.cid {
		p.localSubrs, p.hasLocalSubrs = t.localSubrs, true
	}
	if !p.parse(data, 0) || !p.hasEndchar || p.bbox.isDefault() {
		return false
	}
	return p.bbox.toRect()
}

func (p *cffParser) beginMove(n int) bool {
	if len(p.stack) != n {
		return false
	}
	if p.firstMoveTo {
		p.firstMoveTo = false
	} else {
		p.b.Close()
	}
	p.hasMoveTo = true
	return true
}

func (p *cffParser) parse(cs []byte, depth uint8) bool {
	s := stream{data: cs}
	for s.offset < len(s.data) {
		op, _ := s.u8()
		st := p.stack
		switch {
		case op == 0 || op == 2 || op == 9 || op == 13 || op == 15 || op == 16 || op == 17:
			return false
		case op == 1 || op == 3 || op == 18 || op == 23: // stems
			n := len(st)
			if n&1 == 1 && !p.width {
				p.width = true
				n--
			}
			p.stemsLen += uint32(n) >> 1
			p.stack = st[:0]
		case op == 4: // vmoveto
			i := 0
			if len(st) == 2 {
				i = 1
				p.width = true
			}
			if !p.beginMove(i + 1) {
				return false
			}
			p.y += st[i]
			p.moveTo(p.x, p.y)
			p.stack = st[:0]
		case op == 5: // rlineto
			if !p.hasMoveTo || len(st)&1 == 1 {
				return false
			}
			for i := 0; i < len(st); i += 2 {
				p.x += st[i]
				p.y += st[i+1]
				p.lineTo(p.x, p.y)
			}
			p.stack = st[:0]
		case op == 6 || op == 7: // hlineto, vlineto
			if !p.hasMoveTo || len(st) == 0 {
				return false
			}
			horizontal := op == 6
			for i := 0; i < len(st); {
				if horizontal {
					p.x += st[i]
				} else {
					p.y += st[i]
				}
				i++
				p.lineTo(p.x, p.y)
				if i == len(st) {
					break
				}
				if horizontal {
					p.y += st[i]
				} else {
					p.x += st[i]
				}
				i++
				p.lineTo(p.x, p.y)
			}
			p.stack = st[:0]
		case op == 8: // rrcurveto
			if !p.hasMoveTo || len(st)%6 != 0 {
				return false
			}
			for i := 0; i < len(st); i += 6 {
				p.curve6(st[i:])
			}
			p.stack = st[:0]
		case op == 10: // callsubr
			if len(st) == 0 || depth == 10 {
				return false
			}
			if !p.hasLocalSubrs && p.t.cid {
				p.localSubrs, p.hasLocalSubrs = p.t.cidLocalSubrs(p.gid)
			}
			if !p.hasLocalSubrs {
				return false
			}
			idx, ok := cffSubrIndex(p.pop(), cffSubrBias(p.localSubrs.len()))
			if !ok {
				return false
			}
			sub, ok := p.localSubrs.get(idx)
			if !ok || !p.parse(sub, depth+1) {
				return false
			}
			if p.hasEndchar && !p.hasSeac {
				return s.offset >= len(s.data)
			}
		case op == 11: // return
			return true
		case op == 12:
			op2, ok := s.u8()
			if !ok {
				return false
			}
			if !p.hasMoveTo {
				switch op2 {
				case 34, 35, 36, 37:
					return false
				}
			}
			switch op2 {
			case 34: // hflex
				if len(st) != 7 {
					return false
				}
				dx1 := p.x + st[0]
				dy1 := p.y
				dx2 := dx1 + st[1]
				dy2 := dy1 + st[2]
				dx3 := dx2 + st[3]
				dy3 := dy2
				dx4 := dx3 + st[4]
				dy4 := dy2
				dx5 := dx4 + st[5]
				dy5 := p.y
				p.x = dx5 + st[6]
				p.curveTo(dx1, dy1, dx2, dy2, dx3, dy3)
				p.curveTo(dx4, dy4, dx5, dy5, p.x, p.y)
			case 35: // flex
				if len(st) != 13 {
					return false
				}
				dx1 := p.x + st[0]
				dy1 := p.y + st[1]
				dx2 := dx1 + st[2]
				dy2 := dy1 + st[3]
				dx3 := dx2 + st[4]
				dy3 := dy2 + st[5]
				dx4 := dx3 + st[6]
				dy4 := dy3 + st[7]
				dx5 := dx4 + st[8]
				dy5 := dy4 + st[9]
				p.x = dx5 + st[10]
				p.y = dy5 + st[11]
				p.curveTo(dx1, dy1, dx2, dy2, dx3, dy3)
				p.curveTo(dx4, dy4, dx5, dy5, p.x, p.y)
			case 36: // hflex1
				if len(st) != 9 {
					return false
				}
				dx1 := p.x + st[0]
				dy1 := p.y + st[1]
				dx2 := dx1 + st[2]
				dy2 := dy1 + st[3]
				dx3 := dx2 + st[4]
				dy3 := dy2
				dx4 := dx3 + st[5]
				dy4 := dy2
				dx5 := dx4 + st[6]
				dy5 := dy4 + st[7]
				p.x = dx5 + st[8]
				p.curveTo(dx1, dy1, dx2, dy2, dx3, dy3)
				p.curveTo(dx4, dy4, dx5, dy5, p.x, p.y)
			case 37: // flex1
				if len(st) != 11 {
					return false
				}
				dx1 := p.x + st[0]
				dy1 := p.y + st[1]
				dx2 := dx1 + st[2]
				dy2 := dy1 + st[3]
				dx3 := dx2 + st[4]
				dy3 := dy2 + st[5]
				dx4 := dx3 + st[6]
				dy4 := dy3 + st[7]
				dx5 := dx4 + st[8]
				dy5 := dy4 + st[9]
				if abs32(dx5-p.x) > abs32(dy5-p.y) {
					p.x = dx5 + st[10]
				} else {
					p.y = dy5 + st[10]
				}
				p.curveTo(dx1, dy1, dx2, dy2, dx3, dy3)
				p.curveTo(dx4, dy4, dx5, dy5, p.x, p.y)
			default:
				return false
			}
			p.stack = st[:0]
		case op == 14: // endchar
			if len(st) == 4 || (!p.width && len(st) == 5) {
				accent, ok1 := p.seacGID(p.pop())
				if !ok1 {
					return false
				}
				base, ok2 := p.seacGID(p.pop())
				if !ok2 {
					return false
				}
				dy := p.pop()
				dx := p.pop()
				if !p.width && len(p.stack) > 0 {
					p.width = true
					p.pop()
				}
				p.hasSeac = true
				if depth == 10 {
					return false
				}
				baseCS, ok := p.t.charStrings.get(uint32(base))
				if !ok || !p.parse(baseCS, depth+1) {
					return false
				}
				p.x, p.y = dx, dy
				accentCS, ok := p.t.charStrings.get(uint32(accent))
				if !ok || !p.parse(accentCS, depth+1) {
					return false
				}
			} else if len(st) == 1 && !p.width {
				p.width = true
				p.pop()
			}
			if !p.firstMoveTo {
				p.firstMoveTo = true
				p.b.Close()
			}
			if s.offset < len(s.data) {
				return false
			}
			p.hasEndchar = true
			return true
		case op == 19 || op == 20: // hintmask, cntrmask
			n := len(st)
			p.stack = st[:0]
			if n&1 == 1 {
				n--
				p.width = true
			}
			p.stemsLen += uint32(n) >> 1
			s.offset += int((p.stemsLen + 7) >> 3)
		case op == 21: // rmoveto
			i := 0
			if len(st) == 3 {
				i = 1
				p.width = true
			}
			if !p.beginMove(i + 2) {
				return false
			}
			p.x += st[i]
			p.y += st[i+1]
			p.moveTo(p.x, p.y)
			p.stack = st[:0]
		case op == 22: // hmoveto
			i := 0
			if len(st) == 2 {
				i = 1
				p.width = true
			}
			if !p.beginMove(i + 1) {
				return false
			}
			p.x += st[i]
			p.moveTo(p.x, p.y)
			p.stack = st[:0]
		case op == 24: // rcurveline
			if !p.hasMoveTo || len(st) < 8 || (len(st)-2)%6 != 0 {
				return false
			}
			i := 0
			for ; i < len(st)-2; i += 6 {
				p.curve6(st[i:])
			}
			p.x += st[i]
			p.y += st[i+1]
			p.lineTo(p.x, p.y)
			p.stack = st[:0]
		case op == 25: // rlinecurve
			if !p.hasMoveTo || len(st) < 8 || (len(st)-6)&1 == 1 {
				return false
			}
			i := 0
			for ; i < len(st)-6; i += 2 {
				p.x += st[i]
				p.y += st[i+1]
				p.lineTo(p.x, p.y)
			}
			p.curve6(st[i:])
			p.stack = st[:0]
		case op == 26: // vvcurveto
			if !p.hasMoveTo {
				return false
			}
			i := 0
			if len(st)&1 == 1 {
				p.x += st[0]
				i = 1
			}
			if (len(st)-i)%4 != 0 {
				return false
			}
			for ; i < len(st); i += 4 {
				x1 := p.x
				y1 := p.y + st[i]
				x2 := x1 + st[i+1]
				y2 := y1 + st[i+2]
				p.x = x2
				p.y = y2 + st[i+3]
				p.curveTo(x1, y1, x2, y2, p.x, p.y)
			}
			p.stack = st[:0]
		case op == 27: // hhcurveto
			if !p.hasMoveTo {
				return false
			}
			i := 0
			if len(st)&1 == 1 {
				p.y += st[0]
				i = 1
			}
			if (len(st)-i)%4 != 0 {
				return false
			}
			for ; i < len(st); i += 4 {
				x1 := p.x + st[i]
				y1 := p.y
				x2 := x1 + st[i+1]
				y2 := y1 + st[i+2]
				p.x = x2 + st[i+3]
				p.y = y2
				p.curveTo(x1, y1, x2, y2, p.x, p.y)
			}
			p.stack = st[:0]
		case op == 28: // shortint
			v, ok := s.i16()
			if !ok || !p.push(float32(v)) {
				return false
			}
		case op == 29: // callgsubr
			if len(st) == 0 || depth == 10 {
				return false
			}
			idx, ok := cffSubrIndex(p.pop(), cffSubrBias(p.t.globalSubrs.len()))
			if !ok {
				return false
			}
			sub, ok := p.t.globalSubrs.get(idx)
			if !ok || !p.parse(sub, depth+1) {
				return false
			}
			if p.hasEndchar && !p.hasSeac {
				return s.offset >= len(s.data)
			}
		case op == 30 || op == 31: // vhcurveto, hvcurveto
			if !p.hasMoveTo || len(st) < 4 {
				return false
			}
			if !p.hvCurves(op == 31) {
				return false
			}
		case op >= 32 && op <= 246:
			if !p.push(float32(int16(op) - 139)) {
				return false
			}
		case op >= 247 && op <= 250:
			b1, ok := s.u8()
			if !ok || !p.push(float32((int16(op)-247)*256+int16(b1)+108)) {
				return false
			}
		case op >= 251 && op <= 254:
			b1, ok := s.u8()
			if !ok || !p.push(float32(-(int16(op)-251)*256-int16(b1)-108)) {
				return false
			}
		case op == 255:
			v, ok := s.u32()
			if !ok || !p.push(float32(int32(v))/65536) {
				return false
			}
		}
	}
	return true
}

func abs32(v float32) float32 {
	if v < 0 || (v == 0 && 1/v < 0) {
		return -v
	}
	return v
}

func (p *cffParser) curve6(a []float32) {
	x1 := p.x + a[0]
	y1 := p.y + a[1]
	x2 := x1 + a[2]
	y2 := y1 + a[3]
	p.x = x2 + a[4]
	p.y = y2 + a[5]
	p.curveTo(x1, y1, x2, y2, p.x, p.y)
}

// hvCurves is parse_hv_curve_to (horizontal first) and parse_vh_curve_to.
func (p *cffParser) hvCurves(horizontalFirst bool) bool {
	st := p.stack
	for a, b := 0, len(st)-1; a < b; a, b = a+1, b-1 {
		st[a], st[b] = st[b], st[a]
	}
	horizontal := horizontalFirst
	for len(p.stack) > 0 {
		if len(p.stack) < 4 {
			return false
		}
		if horizontal {
			x1 := p.x + p.pop()
			y1 := p.y
			x2 := x1 + p.pop()
			y2 := y1 + p.pop()
			p.y = y2 + p.pop()
			p.x = x2
			if len(p.stack) == 1 {
				p.x += p.pop()
			}
			p.curveTo(x1, y1, x2, y2, p.x, p.y)
		} else {
			x1 := p.x
			y1 := p.y + p.pop()
			x2 := x1 + p.pop()
			y2 := y1 + p.pop()
			p.x = x2 + p.pop()
			p.y = y2
			if len(p.stack) == 1 {
				p.y += p.pop()
			}
			p.curveTo(x1, y1, x2, y2, p.x, p.y)
		}
		horizontal = !horizontal
	}
	return true
}

// seacGID is seac_code_to_glyph_id.
func (p *cffParser) seacGID(n float32) (uint16, bool) {
	if !(n > -2147483904 && n < 2147483648) {
		return 0, false
	}
	code := int32(n)
	if code < 0 || code > 255 {
		return 0, false
	}
	sid := uint16(cffStandardEncoding[code])
	switch p.t.charset.kind {
	case 0:
		if code <= 228 {
			return sid, true
		}
		return 0, false
	case 1, 2:
		return 0, false
	}
	return p.t.charset.sidToGID(sid)
}
