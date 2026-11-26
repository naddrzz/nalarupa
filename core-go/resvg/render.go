// Package resvg is a Go port of resvg 0.47 (core/vendor/resvg, without main.rs): it paints a usvg tree
// with core-go/skia, keeping resvg's layer, isolation, opacity, clip, mask and filter order and the four
// `Nalarupa:` patches (render_node without a layer box, the two layer clamps in render_group, the filter
// region limit and the offset that follows the node's axes).
//
// Floating-point products are wrapped in explicit float32/float64 conversions, which the Go spec says
// prevents fusing them into a multiply-add, so results do not depend on GOAMD64 or the architecture.
package resvg

import (
	"strings"

	"github.com/naddrzz/nalarupa/core-go/skia"
	"github.com/naddrzz/nalarupa/core-go/usvg"
)

// Upstream threads a render::Context holding `max_bbox` through every call. The Nalarupa patches in
// render_group replaced its only use, so the port leaves it out.

// Render paints tree onto pixmap; transform is the root transform (it can position the SVG inside the
// pixmap). The result is in the sRGB color space.
func Render(tree *usvg.Tree, transform skia.Transform, pixmap skia.PixmapMut) {
	renderNodes(tree.Root(), transform, pixmap)
}

// RenderNode paints node onto pixmap, shifted so its abs layer bounding box starts at the pixmap origin;
// the expected pixmap size is that box's. It returns false when the node has no layer box (a zero size),
// except for a path.
func RenderNode(node usvg.Node, transform skia.Transform, pixmap skia.PixmapMut) bool {
	// Nalarupa: a horizontal or vertical stroked path has no layer box but still paints; it is drawn
	// unshifted (the caller positions it) instead of being dropped.
	bbox, ok := node.AbsLayerBoundingBox()
	if _, isPath := node.(*usvg.Path); !ok && !isPath {
		return false
	}
	if ok {
		transform = transform.PreTranslate(-bbox.X(), -bbox.Y())
	}
	renderNode(node, transform, pixmap)
	return true
}

func renderNodes(parent *usvg.Group, transform skia.Transform, pixmap skia.PixmapMut) {
	for _, node := range parent.Children() {
		renderNode(node, transform, pixmap)
	}
}

func renderNode(node usvg.Node, transform skia.Transform, pixmap skia.PixmapMut) {
	switch n := node.(type) {
	case *usvg.Group:
		renderGroup(n, transform, pixmap)
	case *usvg.Path:
		renderPath(n, skia.BlendModeSourceOver, transform, pixmap)
	case *usvg.Image:
		renderImage(n, transform, pixmap)
	case *usvg.Text:
		renderGroup(n.Flattened(), transform, pixmap)
	}
}

func renderGroup(group *usvg.Group, transform skia.Transform, pixmap skia.PixmapMut) {
	transform = transform.PreConcat(group.Transform())

	if rest, ok := strings.CutPrefix(group.ID(), "nrbb:"); ok && isNativeBackdropGroup(group) {
		renderBackdropBlur(group, rest, transform, pixmap)
		return
	}

	if !group.ShouldIsolate() {
		renderNodes(group, transform, pixmap)
		return
	}

	bbox, ok := group.LayerBoundingBox().Transform(transform)
	if !ok {
		return
	}

	var ibbox skia.IntRect
	if len(group.Filters()) == 0 {
		// Convert group bbox into an integer one, expanding each side outwards by 2px to make sure that
		// anti-aliased pixels would not be clipped.
		x, okX := checkedSubI32(asI32(floor32(bbox.X())), 2)
		y, okY := checkedSubI32(asI32(floor32(bbox.Y())), 2)
		w, okW := checkedAddU32(asU32(ceil32(bbox.Width())), 4)
		h, okH := checkedAddU32(asU32(ceil32(bbox.Height())), 4)
		if !okX || !okY || !okW || !okH {
			return
		}
		if ibbox, ok = skia.IntRectFromXYWH(x, y, w, h); !ok {
			return
		}
	} else {
		// The bounding box for groups with filters is special and should not be expanded by 2px, because
		// it's already acting as a clipping region.
		// Nalarupa: limit the filter region to the target plus a fixed margin, in the target's own
		// coordinates (upstream uses `ctx.max_bbox`, which is in root-canvas coordinates and wrong for
		// nested layers). 512 px covers the reach of shadows and blurs in practice.
		target, ok := skia.IntRectFromXYWH(-512, -512, pixmap.Width()+1024, pixmap.Height()+1024)
		if !ok {
			return
		}
		if ibbox, ok = fitToRect(bbox.ToIntRect(), target); !ok {
			return
		}
	}

	// Nalarupa: without filters nothing outside the target can become visible, so clamp the layer to the
	// target pixmap (upstream clamps to 5x the root canvas in root coordinates, which drops content in
	// nested layers when painting bands/tiles, and wastes memory).
	if len(group.Filters()) == 0 {
		target, ok := skia.IntRectFromXYWH(0, 0, pixmap.Width(), pixmap.Height())
		if !ok {
			return
		}
		if ibbox, ok = fitToRect(ibbox, target); !ok {
			return
		}
	}

	// The original shift, corrected for subpixel positioned layers.
	dx := bbox.X()
	dy := bbox.Y()
	dx -= bbox.X() - float32(ibbox.X())
	dy -= bbox.Y() - float32(ibbox.Y())
	transform = skia.TransformFromTranslate(-dx, -dy).PreConcat(transform)

	sub, ok := skia.NewPixmap(ibbox.Width(), ibbox.Height())
	if !ok {
		return // Rust logs "Failed to allocate a group layer"
	}

	renderNodes(group, transform, sub.AsMut())

	for _, filter := range group.Filters() {
		applyFilter(filter, transform, sub)
	}
	if clip := group.ClipPath(); clip != nil {
		applyClip(clip, transform, sub)
	}
	if mask := group.Mask(); mask != nil {
		applyMask(mask, transform, sub)
	}

	paint := skia.PixmapPaint{
		Opacity:   group.Opacity(),
		BlendMode: convertBlendMode(group.BlendMode()),
		Quality:   skia.FilterQualityNearest,
	}
	pixmap.DrawPixmap(ibbox.X(), ibbox.Y(), sub.AsRef(), &paint, skia.Identity(), nil)
}

func convertBlendMode(mode usvg.BlendMode) skia.BlendMode {
	switch mode {
	case usvg.BlendModeMultiply:
		return skia.BlendModeMultiply
	case usvg.BlendModeScreen:
		return skia.BlendModeScreen
	case usvg.BlendModeOverlay:
		return skia.BlendModeOverlay
	case usvg.BlendModeDarken:
		return skia.BlendModeDarken
	case usvg.BlendModeLighten:
		return skia.BlendModeLighten
	case usvg.BlendModeColorDodge:
		return skia.BlendModeColorDodge
	case usvg.BlendModeColorBurn:
		return skia.BlendModeColorBurn
	case usvg.BlendModeHardLight:
		return skia.BlendModeHardLight
	case usvg.BlendModeSoftLight:
		return skia.BlendModeSoftLight
	case usvg.BlendModeDifference:
		return skia.BlendModeDifference
	case usvg.BlendModeExclusion:
		return skia.BlendModeExclusion
	case usvg.BlendModeHue:
		return skia.BlendModeHue
	case usvg.BlendModeSaturation:
		return skia.BlendModeSaturation
	case usvg.BlendModeColor:
		return skia.BlendModeColor
	case usvg.BlendModeLuminosity:
		return skia.BlendModeLuminosity
	}
	return skia.BlendModeSourceOver
}

// fitToRect is geom.rs fit_to_rect: r clamped to bounds.
func fitToRect(r, bounds skia.IntRect) (skia.IntRect, bool) {
	left := r.Left()
	if left < bounds.Left() {
		left = bounds.Left()
	}
	top := r.Top()
	if top < bounds.Top() {
		top = bounds.Top()
	}
	right := r.Right()
	if right > bounds.Right() {
		right = bounds.Right()
	}
	bottom := r.Bottom()
	if bottom > bounds.Bottom() {
		bottom = bounds.Bottom()
	}
	return skia.IntRectFromLTRB(left, top, right, bottom)
}
