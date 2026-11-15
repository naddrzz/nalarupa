package render

import (
	"math"
	"time"

	"github.com/naddrzz/nalarupa/core-go/skia"
)

// Render draws the visible part of the document into r.Pixels (RGBA, opaque) and returns it. solo ("" for
// none) paints one top-level node alone. Without sharp, a zoom change is served by a viewport-only paint
// when that is cheap enough, else by scaling the cached view; the UI asks for a sharp frame once the
// camera settles.
func (r *Renderer) Render(w, h uint32, camX, camY, zoom float64, bg [3]uint8, solo string, sharp bool) []byte {
	w, h = min(max(w, 1), 8192), min(max(h, 1), 8192)
	zoom = fmin(fmax(zoom, 0.01), 256)
	r.renderView(w, h, camX, camY, zoom, bg, solo, sharp)
	if len(r.Slides) > 0 && len(r.Pixels) == int(w*h*4) {
		r.compositeSlides(w, h, camX, camY, zoom, solo)
	}
	return r.Pixels
}

func resize(b []byte, n int) []byte {
	if cap(b) >= n {
		old := len(b)
		b = b[:n]
		if n > old {
			clear(b[old:])
		}
		return b
	}
	return append(b, make([]byte, n-len(b))...)
}

func (r *Renderer) painter() *painter {
	return &painter{layers: r.Layers, order: r.Order, threads: r.threads()}
}

func (r *Renderer) renderView(w, h uint32, camX, camY, zoom float64, bg [3]uint8, solo string, sharp bool) {
	vx, vy := i32(math.Round(camX*zoom)), i32(math.Round(camY*zoom))
	restack := r.restack
	r.restack = false
	if restack || r.view != nil && !(r.view.bg == bg && r.view.solo == solo) {
		r.view = nil
	}
	dirty := r.dirty
	r.dirty = nil
	p := r.painter()
	if v := r.view; v != nil {
		for _, d := range underBackdrops(merge(dirty), r.Layers, r.Order) {
			repaint(p, v, d)
		}
		ox, oy := int64(vx)-int64(v.x0), int64(vy)-int64(v.y0)
		exact := math.Abs(v.zoom-zoom) < 1e-9
		if exact && ox >= 0 && oy >= 0 && ox+int64(w) <= int64(v.pw) && oy+int64(h) <= int64(v.ph) {
			r.Pixels = cropInto(r.Pixels, v.pix, v.pw, uint32(ox), uint32(oy), w, h)
			return
		}
		live := !sharp && !exact
		// The cached view stays at its zoom for the sharp frame that follows.
		if live && r.MsPerMP*float64(w*h)/1e6 < liveBudgetMS {
			r.Pixels = resize(r.Pixels, int(w*h*4))
			if p.paint(solo, zoom, bg, vx, vy, w, h, r.Pixels) {
				return
			}
		}
		s := zoom / v.zoom
		cx0, cy0 := float64(v.x0)/v.zoom, float64(v.y0)/v.zoom
		cx1, cy1 := cx0+float64(v.pw)/v.zoom, cy0+float64(v.ph)/v.zoom
		covered := camX >= cx0 && camY >= cy0 && camX+float64(w)/zoom <= cx1 && camY+float64(h)/zoom <= cy1
		if live && covered && s >= 1/maxPreviewScale && s <= maxPreviewScale {
			r.Pixels = resize(r.Pixels, int(w*h*4))
			if pm, ok := skia.PixmapMutFromBytes(r.Pixels, w, h); ok {
				pm.Fill(skia.ColorFromRGBA8(bg[0], bg[1], bg[2], 255))
				tx := float64((float64(v.x0)/v.zoom - camX) * zoom)
				ty := float64((float64(v.y0)/v.zoom - camY) * zoom)
				paint := skia.DefaultPixmapPaint()
				paint.Quality = skia.FilterQualityBilinear
				src, _ := skia.PixmapRefFromBytes(v.pix, v.pw, v.ph)
				pm.DrawPixmap(0, 0, src, &paint, skia.TransformFromRow(float32(s), 0, 0, float32(s), float32(tx), float32(ty)), nil)
				return
			}
		}
	}
	x0, y0 := vx-margin, vy-margin
	cw, ch := w+2*margin, h+2*margin
	var pix []byte
	if v := r.view; v != nil && v.pw == cw && v.ph == ch {
		pix = v.pix
	}
	r.view = nil
	t := time.Now()
	if pix == nil {
		pm, ok := skia.NewPixmap(cw, ch)
		if !ok {
			r.Pixels = resize(r.Pixels, int(w*h*4))
			return
		}
		pix = pm.Data()
	}
	if !p.paint(solo, zoom, bg, x0, y0, cw, ch, pix) {
		// Callers read w×h×4 bytes whatever happens.
		r.Pixels = resize(r.Pixels, int(w*h*4))
		return
	}
	r.MsPerMP = float64(time.Since(t).Seconds()*1e9) / float64(cw*ch)
	r.Pixels = cropInto(r.Pixels, pix, cw, margin, margin, w, h)
	r.view = &view{pix: pix, pw: cw, ph: ch, x0: x0, y0: y0, zoom: zoom, bg: bg, solo: solo}
}

// cropInto copies a w×h window of pix (pw wide) into out, reusing its allocation.
func cropInto(out, pix []byte, pw, x, y, w, h uint32) []byte {
	stride := int(pw) * 4
	out = out[:0]
	for row := y; row < y+h; row++ {
		at := int(row)*stride + int(x)*4
		out = append(out, pix[at:at+int(w)*4]...)
	}
	return out
}

// repaint repaints the doc-space rectangle d inside the cached view.
func repaint(p *painter, v *view, d Rect) {
	z := v.zoom
	x0 := max(i32(math.Floor(d.X*z))-2, v.x0)
	y0 := max(i32(math.Floor(d.Y*z))-2, v.y0)
	x1 := min(i32(math.Ceil((d.X+d.W)*z))+2, v.x0+int32(v.pw))
	y1 := min(i32(math.Ceil((d.Y+d.H)*z))+2, v.y0+int32(v.ph))
	if x1 <= x0 || y1 <= y0 {
		return
	}
	w, h := uint32(x1-x0), uint32(y1-y0)
	part := make([]byte, int(w*h*4))
	if !p.paint(v.solo, z, v.bg, x0, y0, w, h, part) {
		return
	}
	stride, pw := int(v.pw)*4, int(w)*4
	ox, oy := int(x0-v.x0), int(y0-v.y0)
	for row := 0; row < int(h); row++ {
		at := (oy+row)*stride + ox*4
		copy(v.pix[at:at+pw], part[row*pw:(row+1)*pw])
	}
}
