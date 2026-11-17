package resvg

import (
	"github.com/naddrzz/nalarupa/core-go/skia"
	"github.com/naddrzz/nalarupa/core-go/usvg"
)

// Port of clip.rs and mask.rs.

func applyClip(clip *usvg.ClipPath, transform skia.Transform, pixmap *skia.Pixmap) {
	clipPixmap := mustPixmap(pixmap.Width(), pixmap.Height())
	clipPixmap.Fill(skia.ColorFromRGBA8(0, 0, 0, 255))

	clipDrawChildren(clip.Root(), skia.BlendModeClear, transform.PreConcat(clip.Transform()), clipPixmap.AsMut())

	if sub := clip.ClipPath(); sub != nil {
		applyClip(sub, transform, pixmap)
	}

	mask := skia.MaskFromPixmap(clipPixmap.AsRef(), skia.MaskTypeAlpha)
	mask.Invert()
	pixmap.ApplyMask(mask)
}

func clipDrawChildren(parent *usvg.Group, mode skia.BlendMode, transform skia.Transform, pixmap skia.PixmapMut) {
	for _, child := range parent.Children() {
		switch c := child.(type) {
		case *usvg.Path:
			if !c.IsVisible() {
				continue
			}
			fillPath(c, mode, transform, pixmap)
		case *usvg.Text:
			clipDrawChildren(c.Flattened(), mode, transform, pixmap)
		case *usvg.Group:
			transform := transform.PreConcat(c.Transform())
			if clip := c.ClipPath(); clip != nil {
				// If a `clipPath` child also has a `clip-path` then we should render this child on a new
				// canvas, clip it, and only then draw it to the `clipPath`.
				clipGroup(c, clip, transform, pixmap)
			} else {
				clipDrawChildren(c, mode, transform, pixmap)
			}
		}
	}
}

func clipGroup(children *usvg.Group, clip *usvg.ClipPath, transform skia.Transform, pixmap skia.PixmapMut) {
	clipPixmap := mustPixmap(pixmap.Width(), pixmap.Height())

	clipDrawChildren(children, skia.BlendModeSourceOver, transform, clipPixmap.AsMut())
	applyClip(clip, transform, clipPixmap)

	paint := skia.DefaultPixmapPaint()
	paint.BlendMode = skia.BlendModeXor
	pixmap.DrawPixmap(0, 0, clipPixmap.AsRef(), &paint, skia.Identity(), nil)
}

func applyMask(mask *usvg.Mask, transform skia.Transform, pixmap *skia.Pixmap) {
	if !mask.Root().HasChildren() {
		pixmap.Fill(skia.Color{})
		return
	}

	maskPixmap := mustPixmap(pixmap.Width(), pixmap.Height())
	{
		// Mask has to be clipped by mask.region.
		alphaMask, ok := skia.NewMask(pixmap.Width(), pixmap.Height())
		if !ok {
			panic("resvg: mask allocation") // Rust unwraps
		}
		alphaMask.FillPath(skia.PathFromRect(mask.Rect().ToRect()), skia.FillRuleWinding, true, transform)

		renderNodes(mask.Root(), transform, maskPixmap.AsMut())

		maskPixmap.ApplyMask(alphaMask)
	}

	if sub := mask.Mask(); sub != nil {
		applyMask(sub, transform, pixmap)
	}

	maskType := skia.MaskTypeLuminance
	if mask.Kind() == usvg.MaskTypeAlpha {
		maskType = skia.MaskTypeAlpha
	}
	pixmap.ApplyMask(skia.MaskFromPixmap(maskPixmap.AsRef(), maskType))
}

// mustPixmap is `Pixmap::new(w, h).unwrap()`.
func mustPixmap(w, h uint32) *skia.Pixmap {
	p, ok := skia.NewPixmap(w, h)
	if !ok {
		panic("resvg: pixmap allocation") // Rust unwraps
	}
	return p
}
