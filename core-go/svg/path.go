package svg

// SVG path data, read as svgtypes 0.15's SimplifyingPathParser reads it (with kurbo 0.11's arc to cubic
// conversion), since `path_geometry` writes its segments back out and the output must match Rust's.

import (
	"math"
	"strconv"
)

type segKind uint8

const (
	segMove segKind = iota
	segLine
	segCurve
	segQuad
	segClose
)

// seg is one absolute segment: MoveTo/LineTo use x, y; Quadratic x1, y1, x, y; CurveTo all.
type seg struct {
	kind                 segKind
	x1, y1, x2, y2, x, y float64
}

// rawSeg is a PathParser segment: cmd is the lowercase command, abs whether it was uppercase.
type rawSeg struct {
	cmd             byte
	abs             bool
	n               [7]float64
	largeArc, sweep bool
}

type pathStream struct {
	s       string
	pos     int
	prevCmd byte // 0 before the first command
}

func (p *pathStream) atEnd() bool { return p.pos >= len(p.s) }

func (p *pathStream) curr() (byte, bool) {
	if p.atEnd() {
		return 0, false
	}
	return p.s[p.pos], true
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

func (p *pathStream) skipSpaces() {
	for !p.atEnd() && isSpace(p.s[p.pos]) {
		p.pos++
	}
}

func (p *pathStream) skipDigits() {
	for !p.atEnd() && p.s[p.pos] >= '0' && p.s[p.pos] <= '9' {
		p.pos++
	}
}

// number is svgtypes `parse_number`: the extent of a number, then Rust's f64 parser; inf and NaN are errors.
func (p *pathStream) number() (float64, bool) {
	p.skipSpaces()
	if p.atEnd() {
		return 0, false
	}
	start := p.pos
	c := p.s[p.pos]
	if c == '+' || c == '-' {
		p.pos++
		var ok bool
		if c, ok = p.curr(); !ok {
			return 0, false
		}
	}
	switch {
	case c >= '0' && c <= '9':
		p.skipDigits()
	case c == '.':
	default:
		return 0, false
	}
	if c, ok := p.curr(); ok && c == '.' {
		p.pos++
		p.skipDigits()
	}
	if c, ok := p.curr(); ok && (c == 'e' || c == 'E') {
		if p.pos+1 >= len(p.s) {
			return 0, false
		}
		if c2 := p.s[p.pos+1]; c2 != 'm' && c2 != 'x' {
			p.pos++
			switch c, _ := p.curr(); {
			case c == '+' || c == '-':
				p.pos++
				p.skipDigits()
			case c >= '0' && c <= '9':
				p.skipDigits()
			default:
				return 0, false
			}
		}
	}
	f, err := strconv.ParseFloat(p.s[start:p.pos], 64)
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
		return 0, false
	}
	return f, true
}

func (p *pathStream) listNumber() (float64, bool) {
	if p.atEnd() {
		return 0, false
	}
	n, ok := p.number()
	if !ok {
		return 0, false
	}
	p.skipSpaces()
	if c, ok := p.curr(); ok && c == ',' {
		p.pos++
	}
	return n, true
}

func (p *pathStream) flag() (bool, bool) {
	p.skipSpaces()
	c, ok := p.curr()
	if !ok || (c != '0' && c != '1') {
		return false, false
	}
	p.pos++
	if c, ok := p.curr(); ok && c == ',' {
		p.pos++
	}
	p.skipSpaces()
	return c == '1', true
}

func isCmd(c byte) bool {
	switch c {
	case 'M', 'm', 'Z', 'z', 'L', 'l', 'H', 'h', 'V', 'v', 'C', 'c', 'S', 's', 'Q', 'q', 'T', 't', 'A', 'a':
		return true
	}
	return false
}

func isNumberStart(c byte) bool { return c >= '0' && c <= '9' || c == '.' || c == '-' || c == '+' }

// next is PathParser's iterator step: done at the end, ok false on an error (after which the stream is at its end).
func (p *pathStream) next() (r rawSeg, done, ok bool) {
	p.skipSpaces()
	if p.atEnd() {
		return r, true, false
	}
	r, ok = p.nextImpl()
	if !ok {
		p.pos = len(p.s)
	}
	return r, false, ok
}

func (p *pathStream) nextImpl() (rawSeg, bool) {
	var r rawSeg
	first := p.s[p.pos]
	has := p.prevCmd != 0
	if !has && first != 'M' && first != 'm' {
		return r, false
	}
	implicitMove := false
	var cmd byte
	switch {
	case isCmd(first):
		cmd = first
		p.pos++
	case isNumberStart(first) && has:
		switch p.prevCmd {
		case 'Z', 'z':
			return r, false
		case 'M':
			implicitMove, cmd = true, 'L'
		case 'm':
			implicitMove, cmd = true, 'l'
		default:
			cmd = p.prevCmd
		}
	default:
		return r, false
	}
	r.abs = cmd >= 'A' && cmd <= 'Z'
	r.cmd = cmd | 0x20
	count := map[byte]int{'m': 2, 'l': 2, 'h': 1, 'v': 1, 'c': 6, 's': 4, 'q': 4, 't': 2, 'z': 0}[r.cmd]
	if r.cmd == 'a' {
		for i := range 3 {
			v, ok := p.listNumber()
			if !ok {
				return r, false
			}
			r.n[i] = v
		}
		var ok bool
		if r.largeArc, ok = p.flag(); !ok {
			return r, false
		}
		if r.sweep, ok = p.flag(); !ok {
			return r, false
		}
		for i := 3; i < 5; i++ {
			v, ok := p.listNumber()
			if !ok {
				return r, false
			}
			r.n[i] = v
		}
	} else {
		for i := range count {
			v, ok := p.listNumber()
			if !ok {
				return r, false
			}
			r.n[i] = v
		}
	}
	switch {
	case implicitMove && r.abs:
		p.prevCmd = 'M'
	case implicitMove:
		p.prevCmd = 'm'
	default:
		p.prevCmd = cmd
	}
	return r, true
}

// parsePath returns the simplified segments of d up to the first error, and whether there was one.
func parsePath(d string) (out []seg, failed bool) {
	p := &pathStream{s: d}
	var prevMX, prevMY, prevTX, prevTY, prevX, prevY float64
	prev := rawSeg{cmd: 'm', abs: true} // prev_seg, kept absolute for curves
	var last *seg                       // prev_simple_seg
	for {
		r, done, ok := p.next()
		if done {
			return out, false
		}
		if !ok {
			return out, true
		}
		var buf []seg
		if last != nil && last.kind == segClose && r.cmd != 'm' && r.cmd != 'z' {
			s := seg{kind: segMove, x: prevMX, y: prevMY}
			buf = append(buf, s)
			last = &s
		}
		n := r.n
		switch r.cmd {
		case 'm':
			x, y := n[0], n[1]
			if !r.abs {
				if last != nil && last.kind == segClose {
					x, y = x+prevMX, y+prevMY
				} else {
					x, y = x+prevX, y+prevY
				}
			}
			buf = append(buf, seg{kind: segMove, x: x, y: y})
			prev = r
		case 'l':
			x, y := n[0], n[1]
			if !r.abs {
				x, y = x+prevX, y+prevY
			}
			buf = append(buf, seg{kind: segLine, x: x, y: y})
			prev = r
		case 'h':
			x := n[0]
			if !r.abs {
				x += prevX
			}
			buf = append(buf, seg{kind: segLine, x: x, y: prevY})
			prev = r
		case 'v':
			y := n[0]
			if !r.abs {
				y += prevY
			}
			buf = append(buf, seg{kind: segLine, x: prevX, y: y})
			prev = r
		case 'c':
			if !r.abs {
				for i := 0; i < 6; i += 2 {
					n[i] += prevX
					n[i+1] += prevY
				}
			}
			buf = append(buf, seg{segCurve, n[0], n[1], n[2], n[3], n[4], n[5]})
			prev = rawSeg{cmd: 'c', abs: true, n: n}
		case 's':
			x1, y1 := prevX, prevY
			switch prev.cmd {
			case 'c':
				x1, y1 = prev.n[4]*2-prev.n[2], prev.n[5]*2-prev.n[3]
			case 's':
				x1, y1 = prev.n[2]*2-prev.n[0], prev.n[3]*2-prev.n[1]
			}
			if !r.abs {
				for i := 0; i < 4; i += 2 {
					n[i] += prevX
					n[i+1] += prevY
				}
			}
			buf = append(buf, seg{segCurve, x1, y1, n[0], n[1], n[2], n[3]})
			prev = rawSeg{cmd: 's', abs: true, n: n}
		case 'q':
			if !r.abs {
				for i := 0; i < 4; i += 2 {
					n[i] += prevX
					n[i+1] += prevY
				}
			}
			buf = append(buf, seg{kind: segQuad, x1: n[0], y1: n[1], x: n[2], y: n[3]})
			prev = rawSeg{cmd: 'q', abs: true, n: n}
		case 't':
			x1, y1 := prevX, prevY
			switch prev.cmd {
			case 'q':
				x1, y1 = prev.n[2]*2-prev.n[0], prev.n[3]*2-prev.n[1]
			case 't':
				x1, y1 = prev.n[0]*2-prevTX, prev.n[1]*2-prevTY
			}
			prevTX, prevTY = x1, y1
			if !r.abs {
				n[0] += prevX
				n[1] += prevY
			}
			buf = append(buf, seg{kind: segQuad, x1: x1, y1: y1, x: n[0], y: n[1]})
			prev = rawSeg{cmd: 't', abs: true, n: n}
		case 'a':
			x, y := n[3], n[4]
			if !r.abs {
				x, y = x+prevX, y+prevY
			}
			if !arcToCubics(prevX, prevY, x, y, n[0], n[1], toRad(n[2]), r.largeArc, r.sweep, func(s seg) { buf = append(buf, s) }) {
				buf = append(buf, seg{kind: segLine, x: x, y: y})
			}
			prev = r
		case 'z':
			if last == nil || last.kind != segClose {
				buf = append(buf, seg{kind: segClose})
			}
			prev = r
		}
		if len(buf) > 0 {
			s := buf[len(buf)-1]
			last = &s
			switch s.kind {
			case segMove:
				prevX, prevY = s.x, s.y
				prevMX, prevMY = prevX, prevY
			case segClose:
				prevX, prevY = prevMX, prevMY
			default:
				prevX, prevY = s.x, s.y
			}
		}
		out = append(out, buf...)
	}
}

// arcToCubics is kurbo's `Arc::from_svg_arc` and `to_cubic_beziers(0.1)`; false when the arc is a straight line.
func arcToCubics(fx, fy, tx, ty, rx, ry, xRot float64, large, sweep bool, emit func(seg)) bool {
	if math.Abs(rx) <= 1e-5 || math.Abs(ry) <= 1e-5 || (fx == tx && fy == ty) {
		return false
	}
	rx, ry = math.Abs(rx), math.Abs(ry)
	xr := math.Mod(xRot, 2*pi)
	sinPhi, cosPhi := math.Sin(xr), math.Cos(xr)
	hdX, hdY := (fx-tx)*0.5, (fy-ty)*0.5
	hsX, hsY := (fx+tx)*0.5, (fy+ty)*0.5
	px, py := cosPhi*hdX+sinPhi*hdY, -sinPhi*hdX+cosPhi*hdY
	if rf := px*px/(rx*rx) + py*py/(ry*ry); rf > 1 {
		scale := math.Sqrt(rf)
		rx *= scale
		ry *= scale
	}
	rxry, rxpy, rypx := rx*ry, rx*py, ry*px
	sumOfSq := rxpy*rxpy + rypx*rypx
	signCoe := 1.0
	if large == sweep {
		signCoe = -1
	}
	coe := signCoe * math.Sqrt(math.Abs((rxry*rxry-sumOfSq)/sumOfSq))
	tcx, tcy := coe*rxpy/ry, -coe*rypx/rx
	cx, cy := cosPhi*tcx-sinPhi*tcy+hsX, sinPhi*tcx+cosPhi*tcy+hsY
	start := math.Atan2((py-tcy)/ry, (px-tcx)/rx)
	sweepAngle := math.Mod(math.Atan2((-py-tcy)/ry, (-px-tcx)/rx)-start, 2*pi)
	if sweep && sweepAngle < 0 {
		sweepAngle += 2 * pi
	} else if !sweep && sweepAngle > 0 {
		sweepAngle -= 2 * pi
	}
	// append_iter
	sign := 1.0
	switch {
	case sweepAngle < 0 || (sweepAngle == 0 && math.Signbit(sweepAngle)):
		sign = -1
	case math.IsNaN(sweepAngle):
		sign = sweepAngle
	}
	scaledErr := fmax(rx, ry) / 0.1
	nErr := fmax(math.Pow(1.1163*scaledErr, 1.0/6.0), 3.999_999)
	n := math.Ceil(nErr * math.Abs(sweepAngle) * (1 / (2 * pi)))
	step := sweepAngle / n
	armLen := (4.0 / 3.0) * math.Tan(math.Abs(0.25*step)) * sign
	sample := func(angle float64) (float64, float64) {
		u, v := rx*math.Cos(angle), ry*math.Sin(angle)
		s, c := math.Sin(xRot), math.Cos(xRot)
		return u*c - v*s, u*s + v*c
	}
	a0 := start
	p0x, p0y := sample(a0)
	for range int(saturatingUint(n)) {
		a1 := a0 + step
		hx, hy := sample(a0 + pi/2)
		p1x, p1y := p0x+armLen*hx, p0y+armLen*hy
		p3x, p3y := sample(a1)
		kx, ky := sample(a1 + pi/2)
		p2x, p2y := p3x-armLen*kx, p3y-armLen*ky
		a0, p0x, p0y = a1, p3x, p3y
		emit(seg{segCurve, cx + p1x, cy + p1y, cx + p2x, cy + p2y, cx + p3x, cy + p3y})
	}
	return true
}

// saturatingUint is Rust's `f as usize` for a count: NaN and negatives are 0.
func saturatingUint(f float64) float64 {
	if !(f > 0) {
		return 0
	}
	return math.Min(f, 1<<31)
}
