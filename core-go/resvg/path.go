package resvg

import (
	"github.com/naddrzz/nalarupa/core-go/skia"
	"github.com/naddrzz/nalarupa/core-go/usvg"
)

// Port of path.rs.

func renderPath(path *usvg.Path, blendMode skia.BlendMode, transform skia.Transform, pixmap skia.PixmapMut) {
	if !path.IsVisible() {
		return
	}
	if path.PaintOrder() == usvg.PaintOrderFillAndStroke {
		fillPath(path, blendMode, transform, pixmap)
		strokePath(path, blendMode, transform, pixmap)
	} else {
		strokePath(path, blendMode, transform, pixmap)
		fillPath(path, blendMode, transform, pixmap)
	}
}

func fillPath(path *usvg.Path, blendMode skia.BlendMode, transform skia.Transform, pixmap skia.PixmapMut) {
	fill := path.Fill()
	if fill == nil {
		return
	}
	// Horizontal and vertical lines cannot be filled. Skip.
	if b := path.Data().Bounds(); b.Width() == 0 || b.Height() == 0 {
		return
	}
	rule := skia.FillRuleWinding
	if fill.Rule() == usvg.FillRuleEvenOdd {
		rule = skia.FillRuleEvenOdd
	}
	paint, ok := convertPaint(fill.Paint(), fill.Opacity(), transform)
	if !ok {
		return
	}
	paint.AntiAlias = path.RenderingMode().UseShapeAntialiasing()
	paint.BlendMode = blendMode
	pixmap.FillPath(path.Data(), &paint, rule, transform, nil)
}

func strokePath(path *usvg.Path, blendMode skia.BlendMode, transform skia.Transform, pixmap skia.PixmapMut) {
	stroke := path.Stroke()
	if stroke == nil {
		return
	}
	paint, ok := convertPaint(stroke.Paint(), stroke.Opacity(), transform)
	if !ok {
		return
	}
	paint.AntiAlias = path.RenderingMode().UseShapeAntialiasing()
	paint.BlendMode = blendMode
	st := stroke.ToTinySkia()
	pixmap.StrokePath(path.Data(), &paint, &st, transform, nil)
}

// convertPaint is the paint match shared by fill_path and stroke_path.
func convertPaint(p usvg.Paint, opacity float32, transform skia.Transform) (skia.Paint, bool) {
	paint := skia.DefaultPaint()
	switch p := p.(type) {
	case usvg.Color:
		paint.SetColorRGBA8(p.Red, p.Green, p.Blue, opacityToU8(opacity))
	case *usvg.LinearGradient:
		shader, ok := convertLinearGradient(p, opacity)
		if !ok {
			return paint, false
		}
		paint.Shader = shader
	case *usvg.RadialGradient:
		shader, ok := convertRadialGradient(p, opacity)
		if !ok {
			return paint, false
		}
		paint.Shader = shader
	case *usvg.Pattern:
		pix, ts, ok := renderPatternPixmap(p, transform)
		if !ok {
			return paint, false
		}
		paint.Shader = skia.NewPattern(pix.AsRef(), skia.SpreadModeRepeat, skia.FilterQualityBicubic, opacity, ts)
	}
	return paint, true
}

func convertLinearGradient(g *usvg.LinearGradient, opacity float32) (skia.Shader, bool) {
	mode, points := convertBaseGradient(g, opacity)
	return skia.NewLinearGradient(skia.Pt(g.X1(), g.Y1()), skia.Pt(g.X2(), g.Y2()), points, mode, g.Transform())
}

func convertRadialGradient(g *usvg.RadialGradient, opacity float32) (skia.Shader, bool) {
	mode, points := convertBaseGradient(g, opacity)
	return skia.NewRadialGradient(skia.Pt(g.FX(), g.FY()), g.FR(), skia.Pt(g.CX(), g.CY()), g.R(), points, mode, g.Transform())
}

// baseGradient is what usvg's linear and radial gradients share.
type baseGradient interface {
	SpreadMethod() usvg.SpreadMethod
	Stops() []usvg.Stop
}

func convertBaseGradient(g baseGradient, opacity float32) (skia.SpreadMode, []skia.GradientStop) {
	mode := skia.SpreadModePad
	switch g.SpreadMethod() {
	case usvg.SpreadMethodReflect:
		mode = skia.SpreadModeReflect
	case usvg.SpreadMethodRepeat:
		mode = skia.SpreadModeRepeat
	}
	points := make([]skia.GradientStop, 0, len(g.Stops()))
	for _, stop := range g.Stops() {
		alpha := mulOpacity(stop.Opacity(), opacity)
		c := stop.Color()
		color := skia.ColorFromRGBA8(c.Red, c.Green, c.Blue, opacityToU8(alpha))
		points = append(points, skia.NewGradientStop(stop.Offset(), color))
	}
	return mode, points
}

func renderPatternPixmap(pattern *usvg.Pattern, transform skia.Transform) (*skia.Pixmap, skia.Transform, bool) {
	sx, sy := transform.PreConcat(pattern.Transform()).GetScale()

	rect := pattern.Rect()
	size, ok := skia.IntSizeFromWH(asU32(round32(float32(rect.Width()*sx))), asU32(round32(float32(rect.Height()*sy))))
	if !ok {
		return nil, skia.Transform{}, false
	}
	pixmap, ok := skia.NewPixmap(size.Width(), size.Height())
	if !ok {
		return nil, skia.Transform{}, false
	}

	renderNodes(pattern.Root(), skia.TransformFromScale(sx, sy), pixmap.AsMut())

	ts := skia.Identity()
	ts = ts.PreConcat(pattern.Transform())
	ts = ts.PreTranslate(rect.X(), rect.Y())
	ts = ts.PreScale(1/sx, 1/sy)
	return pixmap, ts, true
}
