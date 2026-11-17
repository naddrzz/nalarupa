// Package export is the raster export path of render.rs: a node's SVG at a scale, parsed by usvg, painted
// by resvg and encoded as PNG, JPEG or lossless WebP.
package export

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"math"
	"strings"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/layout"
	"github.com/naddrzz/nalarupa/core-go/resolve"
	"github.com/naddrzz/nalarupa/core-go/resvg"
	"github.com/naddrzz/nalarupa/core-go/script"
	// Shader fills run on the GPU (it sets svg.DefaultShader).
	_ "github.com/naddrzz/nalarupa/core-go/shader"
	"github.com/naddrzz/nalarupa/core-go/skia"
	"github.com/naddrzz/nalarupa/core-go/svg"
	"github.com/naddrzz/nalarupa/core-go/text"
	"github.com/naddrzz/nalarupa/core-go/usvg"
)

// Ctx is what exporting a node needs (render.rs `Ctx`): the opened document, the fonts (text measuring and
// the font database usvg resolves against) and the image and icon assets.
type Ctx struct {
	Doc *doc.Doc
	// Libs are the libraries the document's components may come from.
	Libs   []resolve.Lib
	Fonts  *text.Fonts
	Assets *svg.Assets
	// Scripts runs the script nodes of the laid-out node, noting the runs in rec instead of waiting for them
	// when rec is not nil (record mode, Runs); nil runs them with script.ExpandScripts.
	Scripts func(n *resolve.RNode, r *resolve.Resolver, rec *script.Runs)
	// Runs, when set, notes the script runs an export needs instead of waiting for them (record mode).
	Runs *script.Runs
}

func (c *Ctx) expand() svg.Expand {
	return func(n *resolve.RNode, r *resolve.Resolver) {
		if c.Scripts != nil {
			c.Scripts(n, r, c.Runs)
			return
		}
		script.ExpandScripts(n, r, c.Fonts, c.Doc.Dir(), c.Doc.Path, false, c.Runs)
	}
}

// LaidOut resolves and lays out the top-level node at index (render.rs `laid_out`), its script nodes run; nil
// when there is none or it is hidden.
func (c *Ctx) LaidOut(index int) *resolve.RNode {
	kids := c.Doc.Children()
	if index < 0 || index >= len(kids) {
		return nil
	}
	r := resolve.New(c.Doc.Root, c.Libs)
	n := r.Resolve(kids[index])
	if n == nil {
		return nil
	}
	layout.Layout(n, c.Fonts)
	// Script nodes need their laid-out size before they can run.
	c.expand()(n, r)
	return n
}

// TreeOptions is render.rs `tree_options`: the machine's fonts, the depth-limited image resolver and the
// document's folder for relative paths.
func (c *Ctx) TreeOptions() usvg.Options {
	opt := usvg.DefaultOptions()
	opt.FontDB = c.Fonts.Database()
	opt.FontResolver = usvg.FontsResolver(c.Fonts)
	opt.ImageHrefResolver = svg.ImageHrefResolver()
	opt.ResourcesDir = c.Doc.Dir()
	return opt
}

// Missing says why id cannot be exported: resolving drops hidden nodes, so a node that exists in the
// document but is absent from the laid-out tree is hidden itself or inside a hidden parent. An instance
// child `instanceId/childId` lives in the component, so each segment is looked up on its own.
func (c *Ctx) Missing(id string) string {
	for _, seg := range strings.Split(id, "/") {
		if c.Doc.Find(seg) == nil {
			return "node not found: " + id
		}
	}
	return id + " is hidden (or inside a hidden layer); show it before exporting"
}

// NodeTreeAt is the usvg tree of node id's SVG at scale (see svg.NodeSVGAt).
func (c *Ctx) NodeTreeAt(id string, scale float64) (*usvg.Tree, error) {
	markup, _, _, ok := svg.NodeSVGAt(c.Doc, c.Libs, id, scale, false, c.Fonts, c.Assets, c.expand())
	if !ok {
		return nil, errors.New(c.Missing(id))
	}
	opt := c.TreeOptions()
	return usvg.FromStr(markup, &opt)
}

// NodePNG is NodeImage as PNG.
func (c *Ctx) NodePNG(id string, scale float64) ([]byte, error) {
	return c.NodeImage(id, "png", scale, 0)
}

// NodeImage rasterises node id as "png", "jpeg" (flattened on white, quality 1-100) or "webp" (lossless);
// scale is clamped to 0.05..8.
func (c *Ctx) NodeImage(id, format string, scale float64, quality uint8) ([]byte, error) {
	scale = min(max(scale, 0.05), 8)
	tree, err := c.NodeTreeAt(id, scale)
	if err != nil {
		return nil, err
	}
	return EncodeTree(tree, format, scale, quality)
}

// EncodeTree rasterises a tree from NodeTreeAt(.., scale). It needs no document state, so callers can do it
// after releasing the engine lock. Any format but "jpeg" and "webp" is PNG.
func EncodeTree(tree *usvg.Tree, format string, scale float64, quality uint8) ([]byte, error) {
	size := tree.Size()
	// The view is already whole pixels at scale (see svg.NodeSVGAt); round away f32 noise.
	pw := clampSize(math.Round(float64(size.Width()) * scale))
	ph := clampSize(math.Round(float64(size.Height()) * scale))
	pm, ok := skia.NewPixmap(pw, ph)
	if !ok {
		return nil, errors.New("image too large")
	}
	if format == "jpeg" {
		pm.Fill(skia.ColorFromRGBA8(255, 255, 255, 255))
	}
	sx := float32(pw) / size.Width()
	sy := float32(ph) / size.Height()
	resvg.Render(tree, skia.TransformFromScale(sx, sy), pm.AsMut())

	rect := image.Rect(0, 0, int(pw), int(ph))
	var out bytes.Buffer
	switch format {
	case "jpeg":
		// Opaque after the white fill, so the premultiplied pixels are plain RGBA.
		img := &image.RGBA{Pix: pm.Data(), Stride: int(pw) * 4, Rect: rect}
		if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: int(min(max(quality, 1), 100))}); err != nil {
			return nil, err
		}
	case "webp":
		return encodeWebP(pm.TakeDemultiplied(), pw, ph)
	default:
		img := &image.NRGBA{Pix: pm.TakeDemultiplied(), Stride: int(pw) * 4, Rect: rect}
		if err := png.Encode(&out, img); err != nil {
			return nil, fmt.Errorf("png: %w", err)
		}
	}
	return out.Bytes(), nil
}

// clampSize is `(x as u32).clamp(1, 8192)`.
func clampSize(x float64) uint32 {
	switch {
	case !(x >= 1): // NaN too
		return 1
	case x >= 8192:
		return 8192
	}
	return uint32(x)
}
