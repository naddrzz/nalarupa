package render

import "testing"

func TestFindEmptySpaceClippedFrameAlignedY(t *testing.T) {
	// Reproduces Gambar 4: starting from a clipped iPhone 16 frame at (0, 0, 393, 852) and calling
	// FindEmptySpace without an explicit anchor must align the next screen at y=0 (not y=64).
	js := `{"version":"2.19","children":[` +
		`{"type":"frame","id":"mob01","name":"iPhone 16","x":0,"y":0,"width":393,"height":852,"fill":"#FFFFFF","layout":"vertical","clip":true}` +
		`]}`
	d := openJSON(t, js)
	cx := offline(t, d)
	r := &Renderer{}
	r.Sync(cx)

	x, y := FindEmptySpace(r, 393, 852, "right", 80, nil)
	if x != 393+80 || y != 0 {
		t.Fatalf("FindEmptySpace next to clipped frame = (%v, %v), want (%v, 0)", x, y, 393+80)
	}
}
