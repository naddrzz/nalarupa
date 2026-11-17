package html

// Icons and the shapes CSS cannot draw, inlined as SVG (the icon and SVG arms of html.rs `element`).

import (
	"encoding/xml"
	"io"
	"strings"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/resolve"
	"github.com/naddrzz/nalarupa/core-go/svg"
)

// icon writes an icon with a plain colour fill as its inline SVG tinted by `color`. No fill = invisible icon.
func (h *writer) icon(n *resolve.RNode, attrs, css string) {
	markup := ""
	if all := fills(n, "fill"); len(all) > 0 {
		if s, ok := solid(all[0]); ok {
			if fill, ok := cssColor(s); ok {
				weight, ok := n.F("weight")
				if !ok {
					weight = 200
				}
				src, _ := h.assets.IconWeight(sOr(n, "library", "lucide"), sOr(n, "icon", ""), weight)
				// A bare-path icon (Material Symbols) sets no fill and would paint black; the canvas fills it with the colour.
				if i := strings.Index(src, "<svg"); i >= 0 {
					src = activeFree(src[i:])
				}
				if strings.HasPrefix(src, "<svg") {
					tag, _, _ := strings.Cut(src, ">")
					paint := ` fill="currentColor"`
					if strings.Contains(tag, ` fill="`) {
						paint = ""
					}
					markup = strings.Replace(src, "<svg", `<svg style="width:100%;height:100%;color:`+fill+`"`+paint, 1)
				}
			}
		}
	}
	h.out.WriteString("<span" + attrs + ` style="display:inline-flex;` + css + `">` + markup + "</span>\n")
}

// vector writes n drawn by the SVG exporter, with ids numbered after the inline SVGs before it. The div's CSS
// already rotates, flips and fades it.
func (h *writer) vector(n *resolve.RNode, attrs, css string) {
	p := jsonv.Clone(n.P).(*jsonv.Object)
	for _, k := range []string{"rotation", "flipX", "flipY", "opacity"} {
		p.Remove(k)
	}
	shape := resolve.Leaf(n.ID, n.Type, p, "")
	shape.X, shape.Y, shape.W, shape.H = n.X, n.Y, n.W, n.H
	s := svg.New(h.fonts, h.assets, h.base).StartIDs(h.svgIDs)
	s.Node(shape)
	h.svgIDs = s.UID()
	// An inline svg sits on the text baseline (display:block keeps a height-0 line in place) and browsers clip it to
	// its viewport; caps, outer strokes and shadows paint past the box.
	markup := strings.Replace(s.Finish(&svg.Rect{X: n.X, Y: n.Y, W: fmax(n.W, 1), H: fmax(n.H, 1)}), "<svg ", `<svg style="display:block;overflow:visible" `, 1)
	h.out.WriteString("<div" + attrs + ` style="` + css + `">` + markup + "</div>\n")
}

// activeFree is an icon's SVG without what would run in the exported page: `script` and `foreignObject`
// elements, `on*` event attributes and `javascript:` links. A clean icon is returned as it is; one with any of
// them is written again without them, and one that is not well-formed XML is dropped ("").
func activeFree(src string) string {
	active := func(t xml.Token) bool {
		el, ok := t.(xml.StartElement)
		if !ok {
			return false
		}
		if n := strings.ToLower(el.Name.Local); n == "script" || n == "foreignobject" {
			return true
		}
		for _, a := range el.Attr {
			if activeAttr(a) {
				return true
			}
		}
		return false
	}
	var toks []xml.Token
	var open []xml.Name
	dirty := false
	d := xml.NewDecoder(strings.NewReader(src))
	for {
		t, err := d.RawToken()
		if err == io.EOF {
			break
		}
		if err != nil {
			return ""
		}
		// RawToken does not match end tags to start tags.
		switch t := t.(type) {
		case xml.StartElement:
			open = append(open, t.Name)
		case xml.EndElement:
			if len(open) == 0 || open[len(open)-1] != t.Name {
				return ""
			}
			open = open[:len(open)-1]
		}
		dirty = dirty || active(t)
		toks = append(toks, xml.CopyToken(t))
	}
	if len(open) > 0 {
		return ""
	}
	if !dirty {
		return src
	}
	name := func(n xml.Name) string {
		if n.Space != "" {
			return n.Space + ":" + n.Local
		}
		return n.Local
	}
	var out strings.Builder
	skip := 0 // depth inside a dropped element
	for _, t := range toks {
		switch t := t.(type) {
		case xml.StartElement:
			if n := strings.ToLower(t.Name.Local); skip > 0 || n == "script" || n == "foreignobject" {
				skip++
				continue
			}
			out.WriteString("<" + name(t.Name))
			for _, a := range t.Attr {
				if !activeAttr(a) {
					out.WriteString(" " + name(a.Name) + `="`)
					xml.EscapeText(&out, []byte(a.Value))
					out.WriteString(`"`)
				}
			}
			out.WriteString(">")
		case xml.EndElement:
			if skip > 0 {
				skip--
				continue
			}
			out.WriteString("</" + name(t.Name) + ">")
		case xml.CharData:
			if skip == 0 {
				xml.EscapeText(&out, t)
			}
		}
	}
	return out.String()
}

// activeAttr is an event handler or a `javascript:` link.
func activeAttr(a xml.Attr) bool {
	if strings.HasPrefix(strings.ToLower(a.Name.Local), "on") {
		return true
	}
	return strings.ToLower(a.Name.Local) == "href" && strings.HasPrefix(strings.ToLower(strings.TrimSpace(a.Value)), "javascript:")
}
