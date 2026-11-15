package skia

import "math"

// Transform is an affine matrix (transform.rs). Like Skia, it is not validated: a zero or non-finite
// matrix is allowed.
type Transform struct{ SX, KX, KY, SY, TX, TY float32 }

// Identity is the identity transform.
func Identity() Transform { return Transform{SX: 1, SY: 1} }

// TransformFromRow creates a transform. The order is column-major (sx, ky, kx, sy, tx, ty), as in Rust.
func TransformFromRow(sx, ky, kx, sy, tx, ty float32) Transform {
	return Transform{SX: sx, KY: ky, KX: kx, SY: sy, TX: tx, TY: ty}
}

func TransformFromTranslate(tx, ty float32) Transform { return TransformFromRow(1, 0, 0, 1, tx, ty) }
func TransformFromScale(sx, sy float32) Transform     { return TransformFromRow(sx, 0, 0, sy, 0, 0) }
func TransformFromSkew(kx, ky float32) Transform      { return TransformFromRow(1, ky, kx, 1, 0, 0) }

// radsPerDeg is f32::to_radians' factor, PI / 180 rounded once in f32.
var radsPerDeg = func() float32 { pi, deg := float32(math.Pi), float32(180); return pi / deg }()

// TransformFromRotate creates a rotation by angle degrees.
func TransformFromRotate(angle float32) Transform {
	v := float32(angle * radsPerDeg)
	a, b := cos32(v), sin32(v)
	return TransformFromRow(a, b, -b, a, 0, 0)
}

// TransformFromRotateAt creates a rotation by angle degrees around (tx, ty).
func TransformFromRotateAt(angle, tx, ty float32) Transform {
	ts := Identity().PreTranslate(tx, ty)
	ts = ts.PreConcat(TransformFromRotate(angle))
	return ts.PreTranslate(-tx, -ty)
}

// TransformFromBBox maps the unit square onto bbox.
func TransformFromBBox(bbox NonZeroRect) Transform {
	return TransformFromRow(bbox.Width(), 0, 0, bbox.Height(), bbox.X(), bbox.Y())
}

func transformFromSinCos(sin, cos float32) Transform {
	return TransformFromRow(cos, sin, -sin, cos, 0, 0)
}

// IsFinite reports whether every entry is finite.
func (t Transform) IsFinite() bool {
	return isFinite(t.SX) && isFinite(t.KY) && isFinite(t.KX) && isFinite(t.SY) && isFinite(t.TX) && isFinite(t.TY)
}

// IsValid reports whether the transform is finite and has a non-zero scale.
func (t Transform) IsValid() bool {
	if !t.IsFinite() {
		return false
	}
	sx, sy := t.GetScale()
	return !(isNearlyZeroWithinTolerance(sx, f32Epsilon) || isNearlyZeroWithinTolerance(sy, f32Epsilon))
}

func (t Transform) IsIdentity() bool       { return t == Identity() }
func (t Transform) IsScale() bool          { return t.HasScale() && !t.HasSkew() && !t.HasTranslate() }
func (t Transform) IsSkew() bool           { return !t.HasScale() && t.HasSkew() && !t.HasTranslate() }
func (t Transform) IsTranslate() bool      { return !t.HasScale() && !t.HasSkew() && t.HasTranslate() }
func (t Transform) IsScaleTranslate() bool { return (t.HasScale() || t.HasTranslate()) && !t.HasSkew() }
func (t Transform) HasScale() bool         { return t.SX != 1 || t.SY != 1 }
func (t Transform) HasSkew() bool          { return t.KX != 0 || t.KY != 0 }
func (t Transform) HasTranslate() bool     { return t.TX != 0 || t.TY != 0 }

// GetScale returns the x and y scale factors.
func (t Transform) GetScale() (float32, float32) {
	return sqrt32(float32(t.SX*t.SX) + float32(t.KX*t.KX)), sqrt32(float32(t.KY*t.KY) + float32(t.SY*t.SY))
}

func (t Transform) PreScale(sx, sy float32) Transform { return t.PreConcat(TransformFromScale(sx, sy)) }
func (t Transform) PostScale(sx, sy float32) Transform {
	return t.PostConcat(TransformFromScale(sx, sy))
}
func (t Transform) PreTranslate(tx, ty float32) Transform {
	return t.PreConcat(TransformFromTranslate(tx, ty))
}
func (t Transform) PostTranslate(tx, ty float32) Transform {
	return t.PostConcat(TransformFromTranslate(tx, ty))
}
func (t Transform) PreRotate(angle float32) Transform { return t.PreConcat(TransformFromRotate(angle)) }
func (t Transform) PostRotate(angle float32) Transform {
	return t.PostConcat(TransformFromRotate(angle))
}

func (t Transform) PreRotateAt(angle, tx, ty float32) Transform {
	return t.PreConcat(TransformFromRotateAt(angle, tx, ty))
}

func (t Transform) PostRotateAt(angle, tx, ty float32) Transform {
	return t.PostConcat(TransformFromRotateAt(angle, tx, ty))
}

// PreConcat returns t * other: other applies first.
func (t Transform) PreConcat(other Transform) Transform { return concat(t, other) }

// PostConcat returns other * t: t applies first.
func (t Transform) PostConcat(other Transform) Transform { return concat(other, t) }

// MapPoint maps a point.
func (t Transform) MapPoint(p *Point) {
	switch {
	case t.IsIdentity():
	case t.IsTranslate():
		p.X += t.TX
		p.Y += t.TY
	case t.IsScaleTranslate():
		p.X = float32(p.X*t.SX) + t.TX
		p.Y = float32(p.Y*t.SY) + t.TY
	default:
		x := float32(p.X*t.SX) + float32(p.Y*t.KX) + t.TX
		y := float32(p.X*t.KY) + float32(p.Y*t.SY) + t.TY
		p.X, p.Y = x, y
	}
}

// MapPoints maps points in place.
func (t Transform) MapPoints(points []Point) {
	if len(points) == 0 {
		return
	}
	switch {
	case t.IsIdentity():
	case t.IsTranslate():
		for i := range points {
			points[i].X += t.TX
			points[i].Y += t.TY
		}
	case t.IsScaleTranslate():
		for i := range points {
			p := &points[i]
			p.X = float32(p.X*t.SX) + t.TX
			p.Y = float32(p.Y*t.SY) + t.TY
		}
	default:
		for i := range points {
			p := &points[i]
			x := float32(p.X*t.SX) + float32(p.Y*t.KX) + t.TX
			y := float32(p.X*t.KY) + float32(p.Y*t.SY) + t.TY
			p.X, p.Y = x, y
		}
	}
}

// Invert returns the inverse; ok is false for a non-invertible matrix.
func (t Transform) Invert() (Transform, bool) {
	if t.IsIdentity() {
		return t, true
	}
	if t.IsScaleTranslate() {
		if t.HasScale() {
			invX, invY := invert(t.SX), invert(t.SY)
			return TransformFromRow(invX, 0, 0, invY, float32(-t.TX*invX), float32(-t.TY*invY)), true
		}
		return TransformFromTranslate(-t.TX, -t.TY), true
	}
	invDet, ok := invDeterminant(t)
	if !ok {
		return Transform{}, false
	}
	inv := computeInv(t, invDet)
	if !inv.IsFinite() {
		return Transform{}, false
	}
	return inv, true
}

func invDeterminant(t Transform) (float64, bool) {
	det := dcross(float64(t.SX), float64(t.SY), float64(t.KX), float64(t.KY))
	// The determinant is on the order of the cube of the matrix members, so compare to the cube of the
	// nearly-zero constant.
	const tolerance = ScalarNearlyZero * ScalarNearlyZero * ScalarNearlyZero // exact, 2^-36
	if isNearlyZeroWithinTolerance(float32(det), tolerance) {
		return 0, false
	}
	return 1.0 / det, true
}

func computeInv(t Transform, invDet float64) Transform {
	return TransformFromRow(
		float32(float64(t.SY)*invDet),
		float32(float64(-t.KY)*invDet),
		float32(float64(-t.KX)*invDet),
		float32(float64(t.SX)*invDet),
		dcrossDscale(t.KX, t.TY, t.SY, t.TX, invDet),
		dcrossDscale(t.KY, t.TX, t.SX, t.TY, invDet),
	)
}

func dcross(a, b, c, d float64) float64 { return float64(a*b) - float64(c*d) }

func dcrossDscale(a, b, c, d float32, scale float64) float32 {
	return float32(dcross(float64(a), float64(b), float64(c), float64(d)) * scale)
}

func concat(a, b Transform) Transform {
	switch {
	case a.IsIdentity():
		return b
	case b.IsIdentity():
		return a
	case !a.HasSkew() && !b.HasSkew():
		return TransformFromRow(float32(a.SX*b.SX), 0, 0, float32(a.SY*b.SY), float32(a.SX*b.TX)+a.TX, float32(a.SY*b.TY)+a.TY)
	}
	return TransformFromRow(
		mulAddMul(a.SX, b.SX, a.KX, b.KY),
		mulAddMul(a.KY, b.SX, a.SY, b.KY),
		mulAddMul(a.SX, b.KX, a.KX, b.SY),
		mulAddMul(a.KY, b.KX, a.SY, b.SY),
		mulAddMul(a.SX, b.TX, a.KX, b.TY)+a.TX,
		mulAddMul(a.KY, b.TX, a.SY, b.TY)+a.TY,
	)
}

func mulAddMul(a, b, c, d float32) float32 {
	return float32(float64(float64(a)*float64(b)) + float64(float64(c)*float64(d)))
}
