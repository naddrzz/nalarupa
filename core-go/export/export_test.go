package export

import (
	"bytes"
	"image"
	"image/png"
	"math/rand"
	"testing"

	"golang.org/x/image/webp"

	"github.com/naddrzz/nalarupa/core-go/usvg"
)

func decodeWebPNRGBA(t *testing.T, b []byte) *image.NRGBA {
	t.Helper()
	img, err := webp.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	m, ok := img.(*image.NRGBA)
	if !ok {
		t.Fatalf("lossless WebP decodes to %T", img)
	}
	return m
}

func TestWebPRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for _, c := range []struct {
		w, h  uint32
		noise bool
	}{{1, 1, false}, {3, 2, true}, {17, 9, true}, {64, 64, false}, {300, 7, true}} {
		data := make([]byte, c.w*c.h*4)
		for i := range data {
			if c.noise {
				data[i] = byte(rng.Intn(256))
			} else {
				data[i] = byte((i / 4 / 5) % 3 * 90) // long runs
			}
		}
		out, err := encodeWebP(data, c.w, c.h)
		if err != nil {
			t.Fatal(err)
		}
		m := decodeWebPNRGBA(t, out)
		if m.Rect.Dx() != int(c.w) || m.Rect.Dy() != int(c.h) || !bytes.Equal(m.Pix, data) {
			t.Errorf("%dx%d: the decoded pixels differ", c.w, c.h)
		}
	}
	if _, err := encodeWebP(nil, 0, 0); err == nil || err.Error() != "Invalid dimensions" {
		t.Errorf("zero size: %v", err)
	}
}

func TestHuffmanLengthLimit(t *testing.T) {
	// Fibonacci frequencies make the optimal code 29 levels deep; the limit squeezes it to 15 while keeping
	// a complete prefix code.
	freqs := make([]uint32, 30)
	a, b := uint32(1), uint32(1)
	for i := range freqs {
		freqs[i] = a
		a, b = b, a+b
	}
	lengths := make([]uint8, len(freqs))
	codes := make([]uint16, len(freqs))
	if !buildHuffmanTree(freqs, lengths, codes, 15) {
		t.Fatal("no tree")
	}
	kraft := 0.0
	for _, l := range lengths {
		if l == 0 || l > 15 {
			t.Fatalf("length %d", l)
		}
		kraft += 1 / float64(uint32(1)<<l)
	}
	if kraft != 1 {
		t.Errorf("Kraft sum %v", kraft)
	}
	if lengths[len(lengths)-1] > lengths[0] {
		t.Error("the most frequent symbol has the longest code")
	}
	if buildHuffmanTree([]uint32{0, 5, 0}, lengths[:3], codes[:3], 15) {
		t.Error("a single symbol needs no tree")
	}
}

func TestHeapOrder(t *testing.T) {
	h := heapFrom([]heapItem{{5, 0}, {1, 1}, {3, 2}, {1, 3}, {9, 4}})
	var got []uint32
	for len(h) > 0 {
		got = append(got, h.pop().freq)
	}
	for i := 1; i < len(got); i++ {
		if got[i] < got[i-1] {
			t.Fatalf("pops %v are not rarest first", got)
		}
	}
}

func treeOf(t *testing.T, w, h string) *usvg.Tree {
	t.Helper()
	opt := usvg.DefaultOptions()
	tree, err := usvg.FromStr(`<svg xmlns="http://www.w3.org/2000/svg" width="`+w+`" height="`+h+`"><rect width="100%" height="100%" fill="#08f"/></svg>`, &opt)
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func TestEncodeTreeSizes(t *testing.T) {
	for _, c := range []struct {
		w, h   string
		scale  float64
		pw, ph int
	}{
		{"10", "20", 1, 10, 20},
		{"10", "20", 2.5, 25, 50},
		{"0.3", "9000", 1, 1, 8192}, // round(0.3) = 0 clamps to 1, 9000 to 8192
		{"7", "7", 0.05, 1, 1},      // round(0.35) = 0
		{"3", "3", 0.5, 2, 2},       // round(1.5) = 2, half away from zero
	} {
		b, err := EncodeTree(treeOf(t, c.w, c.h), "png", c.scale, 0)
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := png.DecodeConfig(bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Width != c.pw || cfg.Height != c.ph {
			t.Errorf("%sx%s at %v: %dx%d, want %dx%d", c.w, c.h, c.scale, cfg.Width, cfg.Height, c.pw, c.ph)
		}
	}
}

func TestEncodeTreeFormats(t *testing.T) {
	tree := treeOf(t, "8", "4")
	png1, _ := EncodeTree(tree, "png", 1, 0)
	other, _ := EncodeTree(tree, "tiff", 1, 0)
	if !bytes.Equal(png1, other) {
		t.Error("an unknown format is not PNG")
	}
	if b, _ := EncodeTree(tree, "jpeg", 1, 0); len(b) < 2 || b[0] != 0xff || b[1] != 0xd8 {
		t.Error("jpeg (quality 0 clamps to 1) is not a JPEG")
	}
	b, err := EncodeTree(tree, "webp", 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if m := decodeWebPNRGBA(t, b); m.Pix[0] != 0 || m.Pix[1] != 0x88 || m.Pix[2] != 0xff || m.Pix[3] != 255 {
		t.Errorf("webp pixel %v", m.Pix[:4])
	}
}
