package export

import (
	"github.com/naddrzz/nalarupa/core-go/pdf"
	"github.com/naddrzz/nalarupa/core-go/usvg"
)

// PDFTrees is render.rs `pdf_trees`: the trees of one PDF page per node, in the given order. Raster
// images whose data does not fully decode are left out of them (Assets.Verify), as Rust does because
// its PDF library would fail the whole document on one damaged image.
func (c *Ctx) PDFTrees(ids []string) ([]*usvg.Tree, error) {
	c.Assets.Verify(true)
	defer c.Assets.Verify(false)
	trees := make([]*usvg.Tree, 0, len(ids))
	for _, id := range ids {
		t, err := c.NodeTreeAt(id, 1)
		if err != nil {
			return nil, err
		}
		trees = append(trees, t)
	}
	return trees, nil
}

// EncodePDF is render.rs `encode_pdf`: one page per tree (1 px = 1 pt) with selectable text. It needs no
// document state, like EncodeTree.
func EncodePDF(trees []*usvg.Tree) ([]byte, error) {
	return pdf.Encode(trees)
}
