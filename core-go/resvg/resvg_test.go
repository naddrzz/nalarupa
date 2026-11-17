package resvg

import (
	"fmt"
	"math"
	"testing"

	"github.com/naddrzz/nalarupa/core-go/skia"
	"github.com/naddrzz/nalarupa/core-go/usvg"
)

func parse(t *testing.T, src string) *usvg.Tree {
	t.Helper()
	opt := usvg.DefaultOptions()
	tree, err := usvg.FromStr(src, &opt)
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func pixel(p *skia.Pixmap, x, y uint32) [4]byte {
	i := (y*p.Width() + x) * 4
	return [4]byte(p.Data()[i : i+4])
}

func TestCreateBoxGauss(t *testing.T) {
	for _, c := range []struct {
		sigma float32
		want  [5]int32
	}{
		{0, [5]int32{1, 1, 1, 1, 1}},
		{-1, [5]int32{1, 1, 1, 1, 1}},
		// w_ideal = sqrt(12σ²/5)+1 floored to odd wl, wu = wl+2, m = round((12σ² - 5wl² - 20wl - 15) / (-4wl - 4)).
		{2, [5]int32{3, 3, 3, 3, 3}},       // wl 3, m 4.5 rounds away from zero to 5
		{6, [5]int32{9, 9, 9, 9, 11}},      // wl 9, m 4.2
		{20, [5]int32{31, 31, 31, 31, 31}}, // wl 31, m 5

	} {
		if got := createBoxGauss(c.sigma); got != c.want {
			t.Errorf("createBoxGauss(%v) = %v, want %v", c.sigma, got, c.want)
		}
	}
}

func TestBoxBlurKeepsMass(t *testing.T) {
	// A box blur of a single opaque pixel spreads it symmetrically and keeps the image transparent far away.
	p, _ := skia.NewPixmap(41, 41)
	copy(p.DataMut()[(20*41+20)*4:], []byte{255, 255, 255, 255})
	boxBlur(3, 3, asImageRef(p))
	c := pixel(p, 20, 20)
	if c[3] == 0 || c[3] == 255 {
		t.Fatalf("center alpha %d", c[3])
	}
	if pixel(p, 17, 20) != pixel(p, 23, 20) || pixel(p, 20, 17) != pixel(p, 20, 23) {
		t.Error("blur is not symmetric")
	}
	if pixel(p, 0, 0) != [4]byte{} {
		t.Error("corner not transparent")
	}
}

func TestIIRCoefficients(t *testing.T) {
	lambda, dnu := iirCoefficients(1.5, 4)
	if lambda != 1.5*1.5/8 {
		t.Errorf("lambda %v", lambda)
	}
	want := (1 + 2*lambda - math.Sqrt(1+4*lambda)) / (2 * lambda)
	if dnu != want {
		t.Errorf("dnu %v, want %v", dnu, want)
	}
	if powi(1.5, 8) != 1.5*1.5*1.5*1.5*1.5*1.5*1.5*1.5 || powi(2, -2) != 0.25 {
		t.Error("powi")
	}
}

func TestIIRBlurSpreads(t *testing.T) {
	p, _ := skia.NewPixmap(21, 1)
	copy(p.DataMut()[10*4:], []byte{200, 0, 0, 200})
	iirBlur(1.5, 0, asImageRef(p))
	if pixel(p, 9, 0) != pixel(p, 11, 0) || pixel(p, 9, 0)[3] == 0 || pixel(p, 10, 0)[3] >= 200 {
		t.Errorf("unexpected blur %v %v %v", pixel(p, 9, 0), pixel(p, 10, 0), pixel(p, 11, 0))
	}
}

// Nalarupa patch, lib.rs: a horizontal stroked line has no layer box but is still drawn.
func TestRenderNodeDrawsLineWithoutLayerBox(t *testing.T) {
	tree := parse(t, `<svg xmlns="http://www.w3.org/2000/svg" width="20" height="20"><path id="l" d="M2 10 H18" stroke="#f00" stroke-width="4"/></svg>`)
	node := tree.NodeByID("l")
	if _, ok := node.AbsLayerBoundingBox(); ok {
		t.Fatal("the line has a layer box")
	}
	p, _ := skia.NewPixmap(20, 20)
	if !RenderNode(node, skia.Identity(), p.AsMut()) {
		t.Fatal("RenderNode refused the line")
	}
	if pixel(p, 10, 10) != [4]byte{255, 0, 0, 255} {
		t.Errorf("line pixel %v", pixel(p, 10, 10))
	}
}

// boxlessNode is a non-path node without a layer box: usvg gives a text or an image none when its abs
// bounding box has a zero width or height.
type boxlessNode struct{ usvg.Node }

func (boxlessNode) AbsLayerBoundingBox() (skia.NonZeroRect, bool) { return skia.NonZeroRect{}, false }

// render_node returns None for a node without a layer box unless it is a path (the Nalarupa patch).
func TestRenderNodeRefusesNodeWithoutLayerBox(t *testing.T) {
	tree := parse(t, `<svg xmlns="http://www.w3.org/2000/svg" width="20" height="20"><rect id="r" width="20" height="20" fill="#f00"/></svg>`)
	p, _ := skia.NewPixmap(20, 20)
	if RenderNode(boxlessNode{tree.NodeByID("r")}, skia.Identity(), p.AsMut()) {
		t.Fatal("RenderNode accepted a non-path node without a layer box")
	}
	if pixel(p, 10, 10) != [4]byte{} {
		t.Errorf("it painted %v", pixel(p, 10, 10))
	}
}

// Nalarupa patch, render.rs: a layer without filters is clamped to the target pixmap. Unclamped, this
// group's layer (2,000,004 px square) cannot be allocated and the group is dropped.
func TestLayerClampedToTarget(t *testing.T) {
	tree := parse(t, `<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10">
	  <g opacity="0.5"><rect x="-1000000" y="-1000000" width="2000000" height="2000000" fill="#00f"/></g></svg>`)
	p, _ := skia.NewPixmap(10, 10)
	Render(tree, skia.Identity(), p.AsMut())
	if c := pixel(p, 5, 5); c[2] == 0 || c[3] < 127 || c[3] > 128 || c[2] != c[3] {
		t.Errorf("half-transparent blue expected, got %v", c)
	}
}

// Nalarupa patch, render.rs: a filtered layer reaches at most 512 px past its target, so content further
// out cannot be moved into view by the filter (upstream keeps 2x the root canvas around it).
func TestFilterLayerLimitedToTargetMargin(t *testing.T) {
	src := `<svg xmlns="http://www.w3.org/2000/svg" width="1000" height="10">
	  <filter id="f" x="-1000" y="-10" width="3000" height="30" filterUnits="userSpaceOnUse"><feOffset dx="%d"/></filter>
	  <g filter="url(#f)"><rect x="%d" y="0" width="20" height="10" fill="#00f"/></g></svg>`
	render := func(x, dx int) [4]byte {
		tree := parse(t, fmt.Sprintf(src, dx, x))
		p, _ := skia.NewPixmap(1000, 10)
		Render(tree, skia.Identity(), p.AsMut())
		return pixel(p, uint32(x+dx+10), 5)
	}
	if got := render(-500, 600); got != [4]byte{0, 0, 255, 255} {
		t.Errorf("content inside the margin: %v", got)
	}
	if got := render(-600, 700); got != [4]byte{} {
		t.Errorf("content past the margin was drawn: %v", got)
	}
}

// Nalarupa patch, filter/mod.rs: the filter region never reaches past the layer.
func TestFilterRegionFitsSource(t *testing.T) {
	tree := parse(t, `<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10">
	  <filter id="f" x="-100000" y="-100000" width="200000" height="200000" filterUnits="userSpaceOnUse"><feFlood flood-color="#0f0"/></filter>
	  <rect width="4" height="4" filter="url(#f)"/></svg>`)
	p, _ := skia.NewPixmap(10, 10)
	Render(tree, skia.Identity(), p.AsMut())
	if pixel(p, 0, 0) != [4]byte{0, 255, 0, 255} || pixel(p, 9, 9) != [4]byte{0, 255, 0, 255} {
		t.Errorf("flood %v %v", pixel(p, 0, 0), pixel(p, 9, 9))
	}
}

// Nalarupa patch, filter/mod.rs: an offset follows the node's rotation and flips.
func TestOffsetCoordinatesFollowTransform(t *testing.T) {
	ts := skia.TransformFromRotate(90)
	dx, dy := offsetCoordinates(10, 0, ts)
	if math.Abs(float64(dx)) > 1e-5 || math.Abs(float64(dy-10)) > 1e-5 {
		t.Errorf("rotated offset (%v, %v), want (0, 10)", dx, dy)
	}
	dx, dy = offsetCoordinates(3, 4, skia.TransformFromScale(-2, 1))
	if dx != -6 || dy != 4 {
		t.Errorf("flipped offset (%v, %v), want (-6, 4)", dx, dy)
	}
}

func TestFitToRect(t *testing.T) {
	r, _ := skia.IntRectFromXYWH(-10, 5, 100, 10)
	b, _ := skia.IntRectFromXYWH(0, 0, 50, 50)
	got, ok := fitToRect(r, b)
	want, _ := skia.IntRectFromXYWH(0, 5, 50, 10)
	if !ok || got != want {
		t.Errorf("fitToRect = %v %v", got, ok)
	}
	far, _ := skia.IntRectFromXYWH(60, 60, 5, 5)
	if _, ok := fitToRect(far, b); ok {
		t.Error("a rect outside the bounds fits")
	}
}

func TestPremultiply(t *testing.T) {
	if premultiplyU8(255, 128) != 128 || premultiplyU8(100, 0) != 0 || premultiplyU8(200, 255) != 200 {
		t.Error("premultiplyU8")
	}
	px := []byte{255, 100, 0, 128}
	premultiplyF64(px)
	if px[0] != 128 || px[1] != 50 || px[3] != 128 {
		t.Errorf("premultiplyF64 %v", px)
	}
	data := []byte{100, 50, 25, 128}
	demultiplyAlpha(data)
	multiplyAlpha(data)
	if data[0] != 100 || data[3] != 128 {
		t.Errorf("demultiply/multiply round trip %v", data)
	}
}

func TestTurbulenceRandom(t *testing.T) {
	// The Park-Miller generator: 16807^1 from seed 1, and seeds are folded into 1..m-1.
	if turbulenceRandom(1) != 16807 {
		t.Error("random(1)")
	}
	lattice, _ := turbulenceInit(0)
	seen := map[int]bool{}
	for _, v := range lattice[:bSize] {
		seen[v] = true
	}
	if len(seen) != bSize {
		t.Error("the lattice is not a permutation")
	}
}
