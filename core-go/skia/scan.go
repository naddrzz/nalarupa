package skia

// Rectangle fills (scan/mod.rs).

// fillRect fills a rect without anti-aliasing.
func fillRect(rect Rect, clip screenIntRect, b blitter) {
	if r, ok := rect.Round(); ok {
		fillIntRect(r, clip, b)
	}
}

func fillIntRect(rect IntRect, clip screenIntRect, b blitter) {
	r, ok := rect.Intersect(clip.ToIntRect())
	if !ok {
		return // everything was clipped out
	}
	sr, ok := r.toScreenIntRect()
	if !ok {
		return
	}
	b.blitRect(sr)
}

// fillRectAA fills a rect with anti-aliasing.
func fillRectAA(rect Rect, clip screenIntRect, b blitter) {
	antiHairFillRect(rect, clip, b)
}
