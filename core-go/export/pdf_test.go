package export_test

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/naddrzz/nalarupa/core-go/export"
	"github.com/naddrzz/nalarupa/core-go/usvg"
)

// TestPDFTreesLeavesOutDamagedImages: of three image fills, the truncated PNG and the one with a bad
// CRC are left out of the trees (Assets.Verify), the intact one stays, and verification is off again
// afterwards.
func TestPDFTreesLeavesOutDamagedImages(t *testing.T) {
	// Absolute, so the image fills resolve to files the document folder holds.
	path, err := filepath.Abs(filepath.Join(testdata, "pdf", "damaged.pen"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := openCtx(t, path, dataDir(t), fonts(t))
	trees, err := ctx.PDFTrees([]string{"damaged"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := export.EncodePDF(trees)
	if err != nil {
		t.Fatal(err)
	}
	if n := bytes.Count(b, []byte("/Subtype /Image")); n != 1 {
		t.Errorf("%d images in the PDF, want 1", n)
	}
	// With verification off, the damaged files reach the tree again (they draw nothing when decoded).
	tree, err := ctx.NodeTreeAt("damaged", 1)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := countImages(tree.Root().Children()), 3; got != want {
		t.Errorf("%d image nodes after PDFTrees, want %d (verification left on?)", got, want)
	}
}

func TestPDFTreesErrors(t *testing.T) {
	path := filepath.Join(testdata, "pdf", "damaged.pen")
	ctx := openCtx(t, path, dataDir(t), fonts(t))
	if _, err := ctx.PDFTrees([]string{"damaged", "nope"}); err == nil || err.Error() != "node not found: nope" {
		t.Errorf("err %v, want node not found: nope", err)
	}
	// No trees is krilla's one empty A4 page.
	b, err := export.EncodePDF(nil)
	if err != nil {
		t.Fatalf("no pages: %v", err)
	}
	if !bytes.Contains(b, []byte("/MediaBox[0 0 595 842]")) || !bytes.Contains(b, []byte("/Count 1>>")) {
		t.Errorf("no trees: want one 595x842 page, got\n%s", b)
	}
}

// countImages counts image nodes, in patterns, masks and clip paths too.
func countImages(nodes []usvg.Node) int {
	n := 0
	for _, node := range nodes {
		if _, ok := node.(*usvg.Image); ok {
			n++
		}
		if g, ok := node.(*usvg.Group); ok {
			n += countImages(g.Children())
		}
		node.Subroots(func(g *usvg.Group) { n += countImages(g.Children()) })
	}
	return n
}
