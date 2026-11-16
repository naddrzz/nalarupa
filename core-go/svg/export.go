package svg

// The SVG file export (render.rs `export_svg`, svg.rs `Embedded` and `inline_embedded`): image files are
// written as tokens while the tree is emitted and inlined as `data:` URIs afterwards.

import (
	"encoding/base64"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/layout"
	"github.com/naddrzz/nalarupa/core-go/resolve"
)

// Embedded holds the image files an SVG export inlines afterwards: each `<image>` href is written as Tag
// followed by its index in Hrefs. The tag carries a random key, so text in the document never matches it.
type Embedded struct {
	Tag   string
	Hrefs []string
}

func (e *Embedded) token(href string) string {
	if e.Tag == "" {
		e.Tag = fmt.Sprintf("nalarupa-embed-%016x:", doc.RandomU64())
	}
	e.Hrefs = append(e.Hrefs, href)
	return e.Tag + strconv.Itoa(len(e.Hrefs)-1)
}

// LeftOut is the href of an image an SVG export leaves out: a transparent pixel. An empty href would
// resolve to the SVG file itself.
const LeftOut = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAAC0lEQVR4nGNgAAIAAAUAAXpeqz8AAAAASUVORK5CYII="

// InlineEmbedded is svg with each token of embedded replaced by its file as a `data:` URI, in one pass. A
// file that cannot be inlined (unreadable, a gzip bomb past the cap, or an SVG nested past MaxSVGDepth)
// is left out.
func InlineEmbedded(svg string, embedded Embedded) string {
	if len(embedded.Hrefs) == 0 {
		return svg
	}
	var out strings.Builder
	out.Grow(len(svg))
	rest := svg
	for {
		at := strings.Index(rest, embedded.Tag)
		if at < 0 {
			break
		}
		out.WriteString(rest[:at])
		after := rest[at+len(embedded.Tag):]
		digits := 0
		for digits < len(after) && after[digits] >= '0' && after[digits] <= '9' {
			digits++
		}
		i, err := strconv.ParseUint(after[:digits], 10, 64)
		if err != nil || i >= uint64(len(embedded.Hrefs)) {
			out.WriteString(embedded.Tag)
			rest = after
			continue
		}
		href := embedded.Hrefs[i]
		if _, _, ok := deepKey(href); ok && svgTooDeep(href) {
			// An SVG nested too deep stays unavailable, as when rendering.
			out.WriteString(LeftOut)
		} else if uri, ok := dataURI(href); ok {
			out.WriteString(Esc(uri))
		} else {
			// Not inlinable: left out, never linked by its path on this machine.
			out.WriteString(LeftOut)
		}
		rest = after[digits:]
	}
	out.WriteString(rest)
	return out.String()
}

// dataURI is the file at href (a `data:` URI stays as it is) as a `data:` URI.
func dataURI(href string) (string, bool) {
	if strings.HasPrefix(href, "data:") {
		return href, true
	}
	b, err := os.ReadFile(href)
	if err != nil {
		return "", false
	}
	if len(b) >= 2 && b[0] == 0x1f && b[1] == 0x8b {
		// A browser reads no gzip inside a data: URI.
		var ok bool
		if b, ok = gunzipCapped(b); !ok {
			return "", false
		}
	}
	mime := imageExt(b)
	switch mime {
	case "jpg":
		mime = "jpeg"
	case "svg":
		mime = "svg+xml"
	}
	return "data:image/" + mime + ";base64," + base64.StdEncoding.EncodeToString(b), true
}

// Expand runs the script nodes of a laid-out top-level node (script.ExpandScripts, waiting for each run);
// nil leaves them unrun.
type Expand func(n *resolve.RNode, r *resolve.Resolver)

// Export is the SVG file export of node id (possibly nested): its visual bounds, `ceil`ed, as the view.
// The markup still holds the image tokens of the returned Embedded; pass both to InlineEmbedded.
func Export(d *doc.Doc, libs []resolve.Lib, id string, fonts layout.TextMeasurer, assets *Assets, expand Expand) (string, Embedded, bool) {
	svg, _, embedded, ok := NodeSVGAt(d, libs, id, 1, true, fonts, assets, expand)
	return svg, embedded, ok
}

// NodeSVGAt is render.rs `node_svg_at`: node id (possibly nested) as an SVG whose view is its visual bounds
// (shadows, blur and outer stroke included) from their unrounded corner, ceil((max - min) * scale) pixels
// in each axis at scale. With embed, image files are written as tokens of the returned Embedded. libs are the
// libraries the document's components may come from.
func NodeSVGAt(d *doc.Doc, libs []resolve.Lib, id string, scale float64, embed bool, fonts layout.TextMeasurer, assets *Assets, expand Expand) (string, Rect, Embedded, bool) {
	top := topIndex(d, id)
	if top < 0 {
		return "", Rect{}, Embedded{}, false
	}
	r := resolve.New(d.Root, libs)
	root := r.Resolve(d.Children()[top])
	if root == nil {
		return "", Rect{}, Embedded{}, false
	}
	layout.Layout(root, fonts)
	if expand != nil {
		// Script nodes need their laid-out size before they can run.
		expand(root, r)
	}
	node := findRNode(root, id)
	if node == nil {
		return "", Rect{}, Embedded{}, false
	}
	b := VisualBounds(node)
	pw := fmax(math.Ceil(((b.X+b.W)-b.X)*scale), 1)
	ph := fmax(math.Ceil(((b.Y+b.H)-b.Y)*scale), 1)
	view := Rect{b.X, b.Y, pw / scale, ph / scale}
	s := New(fonts, assets, d.Dir())
	s.Embed = embed
	s.Node(node)
	return s.Finish(&view), view, s.Embedded, true
}

// topIndex is the index of the top-level node that holds id (an instance path is looked up by its head).
func topIndex(d *doc.Doc, id string) int {
	head, _, _ := strings.Cut(id, "/")
	for i, n := range d.Children() {
		if jsonv.StrIs(jsonv.Get(n, "id"), head) || doc.FindIn([]jsonv.Value{n}, head) != nil {
			return i
		}
	}
	return -1
}
