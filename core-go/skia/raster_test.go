package skia

// The Rust unit tests of color.rs, geom.rs and painter.rs. The other raster files' tests are in
// raster_<area>_test.go.

import (
	"math"
	"testing"
)

func TestPremultiplyU8(t *testing.T) {
	if got, want := ColorU8FromRGBA(10, 20, 30, 40).Premultiply(), (PremultipliedColorU8{2, 3, 5, 40}); got != want {
		t.Errorf("premultiply = %v, want %v", got, want)
	}
	if got, want := ColorU8FromRGBA(10, 20, 30, 255).Premultiply(), (PremultipliedColorU8{10, 20, 30, 255}); got != want {
		t.Errorf("premultiply opaque = %v, want %v", got, want)
	}
}

func TestDemultiplyU8(t *testing.T) {
	for _, c := range []struct {
		in   PremultipliedColorU8
		want ColorU8
	}{
		{PremultipliedColorU8{2, 3, 5, 40}, ColorU8{13, 19, 32, 40}},
		{PremultipliedColorU8{10, 20, 30, 255}, ColorU8{10, 20, 30, 255}},
		{PremultipliedColorU8{153, 99, 54, 180}, ColorU8{217, 140, 77, 180}},
	} {
		if got := c.in.Demultiply(); got != c.want {
			t.Errorf("%v.Demultiply() = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestPixmapByteOrderRGBA(t *testing.T) {
	p, _ := NewPixmap(2, 1)
	c0, c1 := PremultipliedColorU8{0, 1, 2, 3}, PremultipliedColorU8{10, 11, 12, 13}
	copy(p.data[0:4], c0[:])
	copy(p.data[4:8], c1[:])
	want := []byte{0, 1, 2, 3, 10, 11, 12, 13}
	for i, b := range want {
		if p.Data()[i] != b {
			t.Fatalf("data = %v, want %v", p.Data(), want)
		}
	}
	if c, ok := p.Pixel(1, 0); !ok || c != (PremultipliedColorU8{10, 11, 12, 13}) {
		t.Errorf("Pixel(1, 0) = %v, %v", c, ok)
	}
	if _, ok := p.Pixel(2, 0); ok {
		t.Error("Pixel(2, 0) is outside the data")
	}
}

func TestScreenIntRect(t *testing.T) {
	const maxU32 = math.MaxUint32
	for _, c := range [][4]uint32{
		{0, 0, 0, 0}, {0, 0, 1, 0}, {0, 0, 0, 1},
		{0, 0, maxU32, maxU32}, {0, 0, 1, maxU32}, {0, 0, maxU32, 1},
		{maxU32, 0, 1, 1}, {0, maxU32, 1, 1}, {maxU32, maxU32, maxU32, maxU32},
	} {
		if _, ok := screenIntRectFromXYWH(c[0], c[1], c[2], c[3]); ok {
			t.Errorf("screenIntRectFromXYWH%v is valid", c)
		}
	}

	r, ok := screenIntRectFromXYWH(1, 2, 3, 4)
	if !ok || r.X() != 1 || r.Y() != 2 || r.Width() != 3 || r.Height() != 4 || r.Right() != 4 || r.Bottom() != 6 {
		t.Errorf("screenIntRectFromXYWH(1, 2, 3, 4) = %+v, %v", r, ok)
	}
}

func TestDrawTiler(t *testing.T) {
	const m = drawTilerMaxDimensions
	rect := func(x, y, w, h uint32) screenIntRect {
		r, _ := screenIntRectFromXYWH(x, y, w, h)
		return r
	}
	for _, c := range []struct {
		w, h uint32
		want []screenIntRect
	}{
		{10000, 500, []screenIntRect{rect(0, 0, m, 500), rect(m, 0, 10000-m, 500)}},
		{500, 10000, []screenIntRect{rect(0, 0, 500, m), rect(0, m, 500, 10000-m)}},
		{10000, 10000, []screenIntRect{
			rect(0, 0, m, m), rect(m, 0, 10000-m, m),
			rect(0, m, m, 10000-m), rect(m, m, 10000-m, 10000-m),
		}},
	} {
		tiler, ok := newDrawTiler(c.w, c.h)
		if !ok {
			t.Fatalf("newDrawTiler(%d, %d) gives no tiler", c.w, c.h)
		}
		for i, want := range c.want {
			if got, ok := tiler.next(); !ok || got != want {
				t.Errorf("%dx%d tile %d = %+v, %v; want %+v", c.w, c.h, i, got, ok, want)
			}
		}
		if _, ok := tiler.next(); ok {
			t.Errorf("%dx%d: more tiles than expected", c.w, c.h)
		}
	}
	if _, ok := newDrawTiler(100, 500); ok {
		t.Error("newDrawTiler(100, 500) needs no tiler")
	}
}
