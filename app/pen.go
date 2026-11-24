package main

import "math"

// PenSide identifies the incoming ("in") or outgoing ("out") Bézier control handle of an Anchor.
type PenSide string

const (
	PenIn  PenSide = "in"
	PenOut PenSide = "out"

	PenAnchorPx  = 8.0
	PenHandlePx  = 9.0
	PenHitPx     = PenAnchorPx/2 + 5.0 // 9 px
	PenSegHitPx  = 10.0
	PenPointSnap = 8.0
	PenSegSnap   = 7.0
	PenAxisSnap  = 5.0
	PenDragPx    = 3.0
	PenGrid      = 0.5
)

// Anchor is a vector path anchor point in canvas coordinates with optional absolute In/Out handles.
type Anchor struct {
	X   float64 `json:"x"`
	Y   float64 `json:"y"`
	In  *Pt     `json:"in,omitempty"`
	Out *Pt     `json:"out,omitempty"`
}

// Pos returns the anchor's [x, y] Pt.
func (a *Anchor) Pos() Pt {
	return Pt{a.X, a.Y}
}

// Handle returns the handle pointer for side.
func (a *Anchor) Handle(side PenSide) *Pt {
	if side == PenIn {
		return a.In
	}
	return a.Out
}

// SetHandle sets the handle pointer for side.
func (a *Anchor) SetHandle(side PenSide, p *Pt) {
	if side == PenIn {
		a.In = p
	} else {
		a.Out = p
	}
}

// Clone returns a deep copy of Anchor.
func (a *Anchor) Clone() *Anchor {
	if a == nil {
		return nil
	}
	cp := &Anchor{X: a.X, Y: a.Y}
	if a.In != nil {
		v := *a.In
		cp.In = &v
	}
	if a.Out != nil {
		v := *a.Out
		cp.Out = &v
	}
	return cp
}

// Contour is an open or closed sequence of anchors.
type Contour struct {
	Anchors []*Anchor `json:"anchors"`
	Closed  bool      `json:"closed"`
}

// CloneContours deep-copies a slice of contours.
func CloneContours(cs []*Contour) []*Contour {
	out := make([]*Contour, len(cs))
	for i, c := range cs {
		anchors := make([]*Anchor, len(c.Anchors))
		for j, a := range c.Anchors {
			anchors[j] = a.Clone()
		}
		out[i] = &Contour{Anchors: anchors, Closed: c.Closed}
	}
	return out
}

// PenSeg represents segment i of contour C from From to To.
type PenSeg struct {
	C    *Contour
	I    int
	From *Anchor
	To   *Anchor
}

// PenSegHit extends PenSeg with parametric hit position T, point At, and query point Index.
type PenSegHit struct {
	PenSeg
	T     float64
	At    Pt
	Index int
}

// PenHandleRef identifies one handle on an anchor.
type PenHandleRef struct {
	Anchor *Anchor
	Side   PenSide
}

// OtherPenSide flips PenIn <-> PenOut.
func OtherPenSide(s PenSide) PenSide {
	if s == PenIn {
		return PenOut
	}
	return PenIn
}

// MirrorHandle reflects p across anchor a.
func MirrorHandle(a *Anchor, p Pt) Pt {
	return Pt{a.X*2 - p[0], a.Y*2 - p[1]}
}

// PenRound snaps v to step (default PenGrid = 0.5).
func PenRound(v, step float64) float64 {
	if step <= 0 {
		step = PenGrid
	}
	return math.Round(v/step) * step
}

// ContourDrawable reports whether contour c has at least 2 anchors or 1 closed curved anchor.
func ContourDrawable(c *Contour) bool {
	if c == nil {
		return false
	}
	if len(c.Anchors) >= 2 {
		return true
	}
	return c.Closed && len(c.Anchors) == 1 && (c.Anchors[0].In != nil || c.Anchors[0].Out != nil)
}

// EndOfContour returns the first (PenIn) or last (PenOut) anchor of c.
func EndOfContour(c *Contour, end PenSide) *Anchor {
	if c == nil || len(c.Anchors) == 0 {
		return nil
	}
	if end == PenIn {
		return c.Anchors[0]
	}
	return c.Anchors[len(c.Anchors)-1]
}

// FindOpenEndpoint returns the open contour ending at a and which end it is.
func FindOpenEndpoint(cs []*Contour, a *Anchor) (*Contour, PenSide, bool) {
	for _, c := range cs {
		if c.Closed || len(c.Anchors) == 0 {
			continue
		}
		if c.Anchors[len(c.Anchors)-1] == a {
			return c, PenOut, true
		}
		if c.Anchors[0] == a {
			return c, PenIn, true
		}
	}
	return nil, "", false
}

// HandlesMirrored reports whether a.In and a.Out oppose each other within 2% of the longer handle.
func HandlesMirrored(a *Anchor) bool {
	if a == nil || a.In == nil || a.Out == nil {
		return false
	}
	ix, iy := a.In[0]-a.X, a.In[1]-a.Y
	ox, oy := a.Out[0]-a.X, a.Out[1]-a.Y
	li := math.Hypot(ix, iy)
	lo := math.Hypot(ox, oy)
	if li < 1e-6 || lo < 1e-6 {
		return false
	}
	tol := 0.02 * max(li, lo)
	return math.Abs(ix+ox) < tol && math.Abs(iy+oy) < tol
}

// ContourSegments returns all drawable segments across cs.
func ContourSegments(cs []*Contour) []PenSeg {
	var out []PenSeg
	for _, c := range cs {
		n := len(c.Anchors)
		if !c.Closed {
			n--
		}
		for i := 0; i < n; i++ {
			out = append(out, PenSeg{
				C:    c,
				I:    i,
				From: c.Anchors[i],
				To:   c.Anchors[(i+1)%len(c.Anchors)],
			})
		}
	}
	return out
}

func segCurved(a, b *Anchor) bool {
	return a.Out != nil || b.In != nil
}

func segBezier(a, b *Anchor) [4]Pt {
	p0 := a.Pos()
	p1 := p0
	if a.Out != nil {
		p1 = *a.Out
	}
	p3 := b.Pos()
	p2 := p3
	if b.In != nil {
		p2 = *b.In
	}
	return [4]Pt{p0, p1, p2, p3}
}

func evalCubic(p [4]Pt, t float64) Pt {
	s := 1 - t
	k0 := s * s * s
	k1 := 3 * s * s * t
	k2 := 3 * s * t * t
	k3 := t * t * t
	return Pt{
		k0*p[0][0] + k1*p[1][0] + k2*p[2][0] + k3*p[3][0],
		k0*p[0][1] + k1*p[1][1] + k2*p[2][1] + k3*p[3][1],
	}
}

// PenPointAt evaluates the segment from a to b at parameter t in [0, 1].
func PenPointAt(a, b *Anchor, t float64) Pt {
	return evalCubic(segBezier(a, b), t)
}

func dist2Pt(a, b Pt) float64 {
	dx, dy := a[0]-b[0], a[1]-b[1]
	return dx*dx + dy*dy
}

// PenNearest finds the closest point on segment (a, b) to p.
func PenNearest(a, b *Anchor, p Pt) (t float64, at Pt, dist2 float64) {
	if !segCurved(a, b) {
		dx, dy := b.X-a.X, b.Y-a.Y
		l2 := dx*dx + dy*dy
		if l2 > 0 {
			t = min(1.0, max(0.0, ((p[0]-a.X)*dx+(p[1]-a.Y)*dy)/l2))
		}
		at = Pt{a.X + dx*t, a.Y + dy*t}
		return t, at, dist2Pt(at, p)
	}
	bez := segBezier(a, b)
	best := 0.0
	bd := math.Inf(1)
	for i := 0; i <= 64; i++ {
		u := float64(i) / 64.0
		v := dist2Pt(evalCubic(bez, u), p)
		if v < bd {
			bd, best = v, u
		}
	}
	for step := 1.0 / 128.0; step > 1e-6; step /= 2 {
		for _, cand := range [2]float64{best - step, best + step} {
			if cand < 0 || cand > 1 {
				continue
			}
			v := dist2Pt(evalCubic(bez, cand), p)
			if v < bd {
				bd, best = v, cand
			}
		}
	}
	at = evalCubic(bez, best)
	return best, at, bd
}

// PenSplit splits segment s at parameter t (de Casteljau), inserting and returning the new *Anchor.
func PenSplit(s PenSeg, t float64) *Anchor {
	t = min(1-1e-4, max(1e-4, t))
	a, b := s.From, s.To
	var n *Anchor
	if !segCurved(a, b) {
		n = &Anchor{X: a.X + (b.X-a.X)*t, Y: a.Y + (b.Y-a.Y)*t}
	} else {
		bez := segBezier(a, b)
		lerp := func(p, q Pt) Pt {
			return Pt{p[0] + (q[0]-p[0])*t, p[1] + (q[1]-p[1])*t}
		}
		q0 := lerp(bez[0], bez[1])
		q1 := lerp(bez[1], bez[2])
		q2 := lerp(bez[2], bez[3])
		r0 := lerp(q0, q1)
		r1 := lerp(q1, q2)
		m := lerp(r0, r1)
		if a.Out != nil {
			q0c := q0
			a.Out = &q0c
		}
		if b.In != nil {
			q2c := q2
			b.In = &q2c
		}
		r0c, r1c := r0, r1
		n = &Anchor{X: m[0], Y: m[1], In: &r0c, Out: &r1c}
	}
	idx := s.I + 1
	s.C.Anchors = append(s.C.Anchors, nil)
	copy(s.C.Anchors[idx+1:], s.C.Anchors[idx:])
	s.C.Anchors[idx] = n
	return n
}

// HitAnchor finds the closest anchor within PenHitPx screen pixels of p.
func HitAnchor(cs []*Contour, p Pt, zoom float64) *Anchor {
	var best *Anchor
	bd := math.Pow(PenHitPx/zoom, 2)
	for _, c := range cs {
		for _, a := range c.Anchors {
			v := dist2Pt(a.Pos(), p)
			if v <= bd {
				bd, best = v, a
			}
		}
	}
	return best
}

// ShownHandles returns anchors whose handles should be displayed given selected anchors sel.
func ShownHandles(cs []*Contour, sel map[*Anchor]bool) map[*Anchor]bool {
	out := make(map[*Anchor]bool)
	for _, c := range cs {
		n := len(c.Anchors)
		for i, a := range c.Anchors {
			if !sel[a] {
				continue
			}
			out[a] = true
			if i > 0 || (c.Closed && n > 1) {
				out[c.Anchors[(i-1+n)%n]] = true
			}
			if i < n-1 || (c.Closed && n > 1) {
				out[c.Anchors[(i+1)%n]] = true
			}
		}
	}
	return out
}

// HitPenHandle finds the closest visible handle knob within PenHitPx screen pixels of p.
func HitPenHandle(shown map[*Anchor]bool, cs []*Contour, p Pt, zoom float64) *PenHandleRef {
	var best *PenHandleRef
	bd := math.Pow(PenHitPx/zoom, 2)
	minAnchorDist2 := math.Pow(PenAnchorPx/2/zoom, 2)
	for _, c := range cs {
		for _, a := range c.Anchors {
			if !shown[a] {
				continue
			}
			for _, side := range [2]PenSide{PenIn, PenOut} {
				h := a.Handle(side)
				if h == nil || dist2Pt(*h, a.Pos()) <= minAnchorDist2 {
					continue
				}
				v := dist2Pt(*h, p)
				if v <= bd {
					bd = v
					ref := PenHandleRef{Anchor: a, Side: side}
					best = &ref
				}
			}
		}
	}
	return best
}

// HitPenSegment finds the nearest segment within px screen pixels of any point in pts.
func HitPenSegment(cs []*Contour, pts []Pt, zoom float64, skip map[*Anchor]bool, px float64) *PenSegHit {
	if px <= 0 {
		px = PenSegHitPx
	}
	var best *PenSegHit
	bd := math.Pow(px/zoom, 2)
	for _, s := range ContourSegments(cs) {
		if skip != nil && (skip[s.From] || skip[s.To]) {
			continue
		}
		for idx, p := range pts {
			t, at, d2 := PenNearest(s.From, s.To, p)
			if d2 < bd {
				bd = d2
				hit := PenSegHit{PenSeg: s, T: t, At: at, Index: idx}
				best = &hit
			}
		}
	}
	return best
}

// Constrain45 snaps the direction from -> to to 45-degree multiples while preserving length.
func Constrain45(from, to Pt) Pt {
	dx, dy := to[0]-from[0], to[1]-from[1]
	length := math.Hypot(dx, dy)
	if length < 1e-9 {
		return to
	}
	step := math.Pi / 4
	a := math.Round(math.Atan2(dy, dx)/step) * step
	return Pt{from[0] + math.Cos(a)*length, from[1] + math.Sin(a)*length}
}

// SnapRay slides along ray from -> to to hit the nearest candidate X or Y within maxDist canvas units.
func SnapRay(from, to Pt, candidates []Pt, maxDist float64) *Pt {
	dx, dy := to[0]-from[0], to[1]-from[1]
	length := math.Hypot(dx, dy)
	hasBest := false
	bestK := 0.0
	bd := maxDist
	for _, c := range candidates {
		for a := 0; a < 2; a++ {
			d := dx
			if a == 1 {
				d = dy
			}
			if math.Abs(d) <= 1e-6 {
				continue
			}
			k := (c[a] - from[a]) / d
			dist := math.Abs(k-1) * length
			if k > 0 && dist <= bd {
				bd = dist
				bestK = k
				hasBest = true
			}
		}
	}
	if !hasBest {
		return nil
	}
	res := Pt{from[0] + dx*bestK, from[1] + dy*bestK}
	return &res
}

// RoundRay snaps to the 0.5px grid line crossing ray from -> to nearest to.
func RoundRay(from, to Pt) Pt {
	dx, dy := to[0]-from[0], to[1]-from[1]
	hasK := false
	bestK := 0.0
	axis := 0
	if math.Abs(dx) > 1e-6 {
		rx := PenRound(to[0], PenGrid)
		t := (rx - from[0]) / dx
		if t >= 0 {
			hasK, bestK, axis = true, t, 0
		}
	}
	if math.Abs(dy) > 1e-6 {
		ry := PenRound(to[1], PenGrid)
		t := (ry - from[1]) / dy
		if t >= 0 && (!hasK || math.Abs(t-1) < math.Abs(bestK-1)) {
			hasK, bestK, axis = true, t, 1
		}
	}
	if !hasK {
		return to
	}
	if axis == 0 {
		return Pt{PenRound(to[0], PenGrid), from[1] + dy*bestK}
	}
	return Pt{from[0] + dx*bestK, PenRound(to[1], PenGrid)}
}

// SnapPenPoints snaps dragged points pts against non-moved anchors, segments, 0.5px grid, and axis guides.
func SnapPenPoints(pts []Pt, cs []*Contour, moved map[*Anchor]bool, zoom float64, on bool, skip map[*Anchor]bool) (Pt, []SnapGuide) {
	if skip == nil {
		skip = moved
	}
	pull := func() Pt {
		if len(pts) == 0 {
			return Pt{0, 0}
		}
		bx, by := math.Inf(1), math.Inf(1)
		for _, p := range pts {
			ox := PenRound(p[0], PenGrid) - p[0]
			oy := PenRound(p[1], PenGrid) - p[1]
			if math.Abs(ox) < math.Abs(bx) {
				bx = ox
			}
			if math.Abs(oy) < math.Abs(by) {
				by = oy
			}
		}
		return Pt{bx, by}
	}
	if !on {
		return pull(), nil
	}
	var candidates []Pt
	for _, c := range cs {
		for _, a := range c.Anchors {
			if moved == nil || !moved[a] {
				candidates = append(candidates, a.Pos())
			}
		}
	}
	var near *Pt
	bd := math.Pow(PenPointSnap/zoom, 2)
	for _, p := range pts {
		for _, c := range candidates {
			v := dist2Pt(p, c)
			if v <= bd {
				bd = v
				off := Pt{c[0] - p[0], c[1] - p[1]}
				near = &off
			}
		}
	}
	if near != nil {
		return *near, nil
	}
	if s := HitPenSegment(cs, pts, zoom, skip, PenSegSnap); s != nil {
		at := s.At
		if !segCurved(s.From, s.To) {
			mid := PenPointAt(s.From, s.To, 0.5)
			if dist2Pt(mid, pts[s.Index]) <= math.Pow(PenPointSnap/zoom, 2) {
				at = mid
			}
		}
		return Pt{at[0] - pts[s.Index][0], at[1] - pts[s.Index][1]}, nil
	}
	g := pull()
	pulled := make([]Pt, len(pts))
	for i, p := range pts {
		pulled[i] = Pt{p[0] + g[0], p[1] + g[1]}
	}
	off, guides := Snap(pulled, candidates, PenAxisSnap/zoom, [2]bool{true, true})
	return Pt{g[0] + off[0], g[1] + off[1]}, guides
}

// ToggleSmooth converts double-clicked anchor a between corner and mirrored smooth handles.
func ToggleSmooth(c *Contour, a *Anchor) {
	if c == nil || a == nil {
		return
	}
	if a.In != nil && a.Out != nil {
		a.In, a.Out = nil, nil
		return
	}
	if a.In != nil {
		m := MirrorHandle(a, *a.In)
		a.Out = &m
		return
	}
	if a.Out != nil {
		m := MirrorHandle(a, *a.Out)
		a.In = &m
		return
	}
	idx := -1
	for i, x := range c.Anchors {
		if x == a {
			idx = i
			break
		}
	}
	if idx < 0 {
		return
	}
	n := len(c.Anchors)
	var prev, next *Anchor
	if idx > 0 || (c.Closed && n > 1) {
		prev = c.Anchors[(idx-1+n)%n]
	}
	if idx < n-1 || (c.Closed && n > 1) {
		next = c.Anchors[(idx+1)%n]
	}
	var p, q *Pt
	if prev != nil {
		if prev.Out != nil {
			v := *prev.Out
			p = &v
		} else {
			v := prev.Pos()
			p = &v
		}
	}
	if next != nil {
		if next.In != nil {
			v := *next.In
			q = &v
		} else {
			v := next.Pos()
			q = &v
		}
	}
	var hx, hy float64
	switch {
	case p != nil && q != nil:
		hx, hy = (q[0]-p[0])/6, (q[1]-p[1])/6
	case q != nil:
		hx, hy = (q[0]-a.X)/3, (q[1]-a.Y)/3
	case p != nil:
		hx, hy = (a.X-p[0])/3, (a.Y-p[1])/3
	}
	if hx == 0 && hy == 0 {
		return
	}
	if prev != nil {
		inPt := Pt{a.X - hx, a.Y - hy}
		a.In = &inPt
	}
	if next != nil {
		outPt := Pt{a.X + hx, a.Y + hy}
		a.Out = &outPt
	}
}

// DeletePenPoints deletes selected anchors from cs, dropping degenerate contours and returning next anchors to select.
func DeletePenPoints(cs []*Contour, sel map[*Anchor]bool) (contours []*Contour, next []*Anchor) {
	for _, c := range cs {
		var kept []*Anchor
		var near []*Anchor
		pending := false
		for _, a := range c.Anchors {
			if sel[a] {
				if len(kept) > 0 {
					near = append(near, kept[len(kept)-1])
				} else {
					pending = true
				}
				continue
			}
			kept = append(kept, a)
			if pending {
				near = append(near, a)
				pending = false
			}
		}
		cand := &Contour{Anchors: kept, Closed: c.Closed}
		if ContourDrawable(cand) {
			contours = append(contours, cand)
			next = append(next, near...)
		}
	}
	return contours, next
}
