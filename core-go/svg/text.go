package svg

// Text, icons, notes and placeholders (svg.rs `text_markup`, `lines_with`, `icon_markup`, `note`,
// `placeholder`, `script_status`).

import (
	"math"
	"strings"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/layout"
	"github.com/naddrzz/nalarupa/core-go/resolve"
)

// textStrokeMarkup is the glyph outlines stroked with paints, unclipped: width k·w, so inner/outer strokes
// are double width and textStroke clips them. more is added to every `<text>`.
func (s *Svg) textStrokeMarkup(n *resolve.RNode, st *stroke, paints []paint, more string) string {
	extra := ` fill="none" stroke-width="` + f(st.widths[0]*st.k()) + `"` + more + lineAttrs(n)
	return s.linesWith(n, layout.StyleOf(n), n.Lines, box(n), paints, "stroke", extra)
}

// textStroke is the text stroke painted with paints; inner/outer keep the half of the double-width
// stroke inside/outside the glyphs.
func (s *Svg) textStroke(n *resolve.RNode, st *stroke, paints []paint) string {
	if st.align == "center" {
		return s.textStrokeMarkup(n, st, paints, "")
	}
	id := s.id("k")
	r := inflate(ownBox(n), layout.StyleOf(n).Size/2+st.reach()+2)
	inner := st.align == "inner"
	fill := "#000"
	if inner {
		fill = "#fff"
	}
	glyphs := s.textMarkup(n, []paint{{value: fill, alpha: 1}})
	rect := ""
	if !inner {
		rect = `<rect` + region(r) + ` fill="#fff"/>`
	}
	s.defs.WriteString(`<mask id="` + id + `" maskUnits="userSpaceOnUse"` + region(r) + `>` + rect + glyphs + `</mask>`)
	// A mask group is isolated, so a blended stroke would blend against transparent: mask each text instead.
	for _, p := range paints {
		if p.blend != "" {
			return s.textStrokeMarkup(n, st, paints, ` mask="url(#`+id+`)"`)
		}
	}
	return `<g mask="url(#` + id + `)">` + s.textStrokeMarkup(n, st, paints, "") + `</g>`
}

func (s *Svg) textMarkup(n *resolve.RNode, paints []paint) string {
	return s.linesMarkup(n, layout.StyleOf(n), n.Lines, box(n), paints)
}

// linesMarkup is one `<text>` per paint; lines are positioned from our own measurement (text-anchor
// start), so alignment, justify and wrapping match the layout exactly.
func (s *Svg) linesMarkup(n *resolve.RNode, st layout.TextStyle, lines []resolve.Line, b Rect, paints []paint) string {
	return s.linesWith(n, st, lines, b, paints, "fill", "")
}

// linesWith is linesMarkup painting each `<text>` with prop (`fill` or `stroke`) plus the raw attributes extra.
func (s *Svg) linesWith(n *resolve.RNode, st layout.TextStyle, lines []resolve.Line, b Rect, paints []paint, prop, extra string) string {
	if len(paints) == 0 {
		return ""
	}
	m := s.Fonts.Metrics(st)
	x, y, w, h := b.X, b.Y, b.W, b.H
	if sIs(n, "textGrowth", "fixed-width", "fixed-width-height") {
		w = math.Ceil(w)
	}
	total := float64(len(lines)) * m.LineH
	off := valignOff(n, h, total)
	align, _ := n.S("textAlign")
	var spans strings.Builder
	for i, l := range lines {
		free := w - l.Width
		// Centred/right lines sit as if they carried one more letter spacing (SkParagraph's alignment),
		// while left-aligned lines start at the box edge.
		dx, ws := 0.0, 0.0
		switch {
		case align == "center":
			dx = (free - st.LetterSpacing) / 2
		case align == "right":
			dx = free - st.LetterSpacing
		case align == "justify" && l.Soft:
			gaps := strings.Count(l.Text, " ") + strings.Count(l.Text, " ")
			if gaps > 0 {
				ws = free / float64(gaps)
			}
		}
		wsAttr := ""
		if ws != 0 {
			wsAttr = ` word-spacing="` + f(ws) + `"`
		}
		// Baselines land on whole pixels (exact at zoom/scale 1).
		base := math.Round(y + off + float64(i)*m.LineH + m.Baseline)
		spans.WriteString(`<tspan x="` + f(x+dx) + `" y="` + f(base) + `"` + wsAttr + `>` + Esc(l.Text) + `</tspan>`)
	}
	// Underline/strikethrough are a Nalarupa extension.
	var deco []string
	if n.B("underline") {
		deco = append(deco, "underline")
	}
	if n.B("strikethrough") {
		deco = append(deco, "line-through")
	}
	decoAttr := ""
	if len(deco) > 0 {
		decoAttr = ` text-decoration="` + strings.Join(deco, " ") + `"`
	}
	ls := ""
	if st.LetterSpacing != 0 {
		ls = ` letter-spacing="` + f(st.LetterSpacing) + `"`
	}
	italic := ""
	if st.Italic {
		italic = ` font-style="italic"`
	}
	weight := itoa(int(st.Weight))
	var out strings.Builder
	for _, p := range paints {
		// `wght` = weight on every variable font and nothing else: optical size stays at the font's default.
		out.WriteString(`<text font-family="'` + Esc(st.Family) + `', 'Noto Sans', sans-serif" font-size="` + f(st.Size) + `" font-weight="` + weight +
			`" font-variation-settings="'wght' ` + weight + `" font-optical-sizing="none"` + italic + ls + decoAttr + ` xml:space="preserve"` + extra + p.attrs(prop) + `>` + spans.String() + `</text>`)
	}
	return out.String()
}

// iconMarkup draws the icon into the centered min(w, h) square, once per paint (nil: the node's fills).
func (s *Svg) iconMarkup(n *resolve.RNode, paints []paint) string {
	lib, ok := n.S("library")
	if !ok {
		lib = "lucide"
	}
	name, _ := n.S("icon")
	weight := fOr(n, "weight", 200)
	src, have := s.Assets.IconWeight(lib, name, weight)
	inner, attrs, vb, parsed := parseIcon(src, have)
	if !parsed {
		// Still downloading: draw nothing, like a remote image that has not arrived yet.
		if !have && s.Assets.IconLoading(lib, name, weight) {
			return ""
		}
		// Missing icon: dashed box in the first solid fill color.
		fv, _ := n.P.Get("fill")
		for _, p := range s.paints(fv, box(n)) {
			if strings.HasPrefix(p.value, "#") {
				return `<rect x="` + f(n.X) + `" y="` + f(n.Y) + `" width="` + f(n.W) + `" height="` + f(n.H) + `" rx="3" fill="none" stroke="` + p.value + `" stroke-opacity="0.4" stroke-dasharray="2 2"/>`
			}
		}
		return ""
	}
	vx, vy, vw, vh := vb[0], vb[1], vb[2], vb[3]
	sc := fmin(n.W/vw, n.H/vh)
	if sc <= 0 {
		return ""
	}
	tx, ty := n.X+(n.W-vw*sc)/2-vx*sc, n.Y+(n.H-vh*sc)/2-vy*sc
	if paints == nil {
		// Paint servers are in the icon's own units, so map the node box into them.
		fv, _ := n.P.Get("fill")
		paints = s.paints(fv, Rect{(n.X - tx) / sc, (n.Y - ty) / sc, n.W / sc, n.H / sc})
	}
	var out strings.Builder
	for _, p := range paints {
		out.WriteString(`<g transform="matrix(` + f(sc) + ` 0 0 ` + f(sc) + ` ` + f(tx) + ` ` + f(ty) + `)" opacity="` + f(p.alpha) + `"` + blendStyle(p.blend) +
			strings.ReplaceAll(attrs, "currentColor", p.value) + `>` + strings.ReplaceAll(inner, "currentColor", p.value) + `</g>`)
	}
	return out.String()
}

// parseIcon splits an icon SVG into its content, its root's paint attributes and its viewBox.
func parseIcon(src string, have bool) (inner, attrs string, vb [4]float64, ok bool) {
	if !have {
		return
	}
	start := strings.Index(src, "<svg")
	if start < 0 {
		return
	}
	end := strings.IndexByte(src[start:], '>')
	if end < 0 {
		return
	}
	tagEnd := start + end
	innerEnd := strings.LastIndex(src, "</svg>")
	if innerEnd < 0 {
		innerEnd = len(src)
	}
	tag := src[start:tagEnd]
	attr := func(name string) (string, bool) {
		k := " " + name + `="`
		i := strings.Index(tag, k)
		if i < 0 {
			return "", false
		}
		i += len(k)
		j := strings.IndexByte(tag[i:], '"')
		if j < 0 {
			return "", false
		}
		return tag[i : i+j], true
	}
	vbs, have := attr("viewBox")
	if !have {
		vbs = "0 0 24 24"
	}
	var nums []float64
	for _, p := range strings.FieldsFunc(vbs, func(r rune) bool { return r == ' ' || r == ',' }) {
		if v, ok := resolve.ParseFloat(p); ok {
			nums = append(nums, v)
		}
	}
	// FieldsFunc drops empty fields, which Rust's parse rejects anyway.
	vb = [4]float64{0, 0, 24, 24}
	if len(nums) == 4 && nums[2] > 0 && nums[3] > 0 {
		vb = [4]float64{nums[0], nums[1], nums[2], nums[3]}
	}
	for _, k := range []string{"fill", "stroke", "stroke-width", "stroke-linecap", "stroke-linejoin"} {
		if v, ok := attr(k); ok {
			attrs += " " + k + `="` + Esc(v) + `"`
		}
	}
	if _, ok := attr("fill"); !ok {
		attrs += ` fill="currentColor"`
	}
	if tagEnd+1 <= innerEnd {
		inner = src[tagEnd+1 : innerEnd]
	}
	return inner, attrs, vb, true
}

func (s *Svg) note(n *resolve.RNode) {
	bg, fg, label := "#fff1a8", "#3d3418", "Note"
	switch n.Type {
	case "prompt":
		bg, fg, label = "#e8eeff", "#1f2a5c", "Prompt"
	case "context":
		bg, fg, label = "#efeaff", "#33245c", "Context"
	}
	s.out.WriteString(`<rect x="` + f(n.X) + `" y="` + f(n.Y) + `" width="` + f(n.W) + `" height="` + f(n.H) + `" rx="8" fill="` + bg + `"/>`)
	lines := n.Lines
	if len(lines) == 0 {
		lines = []resolve.Line{{Text: label}}
	}
	s.out.WriteString(s.linesMarkup(n, layout.NoteStyle(n), lines, Rect{n.X + 12, n.Y + 12, fmax(n.W-24, 0), fmax(n.H-24, 0)}, []paint{{value: fg, alpha: 1}}))
}

func (s *Svg) placeholder(n *resolve.RNode) {
	var label string
	if n.Type == "browser" {
		u, _ := n.S("url")
		label = "Browser · " + u
	} else {
		u, _ := n.S("scriptUri")
		label = "Script · " + u
	}
	s.out.WriteString(`<rect x="` + f(n.X) + `" y="` + f(n.Y) + `" width="` + f(n.W) + `" height="` + f(n.H) + `" rx="6" fill="#f4f4f5" stroke="#a1a1aa" stroke-dasharray="6 4"/>`)
	st := layout.TextStyle{Family: "Inter", Size: 13, Weight: 500}
	s.out.WriteString(s.linesMarkup(n, st, []resolve.Line{{Text: label}}, Rect{n.X + 12, n.Y + 10, n.W, 20}, []paint{{value: "#52525b", alpha: 1}}))
}

// scriptStatus is the script status view: a tinted box with a 1 px border and, centred and scaled down to
// fit inside 8 px margins, an icon, the title, the description and the code.
func (s *Svg) scriptStatus(n *resolve.RNode) {
	sv, _ := n.P.Get("__status")
	isErr := jsonv.StrIs(jsonv.Get(sv, "kind"), "error")
	bg, border, fg := "#36b25f", "#36b25f", "#1f9947"
	if isErr {
		bg, border, fg = "#ff6666", "#ff6666", "#e52e2e"
	}
	s.out.WriteString(`<rect x="` + f(n.X) + `" y="` + f(n.Y) + `" width="` + f(n.W) + `" height="` + f(n.H) + `" fill="` + bg + `" fill-opacity="0.102"/>`)
	s.out.WriteString(`<rect x="` + f(n.X+0.5) + `" y="` + f(n.Y+0.5) + `" width="` + f(fmax(n.W-1, 0)) + `" height="` + f(fmax(n.H-1, 0)) + `" fill="none" stroke="` + border + `"/>`)
	style := func(family string, size, lh float64) layout.TextStyle {
		return layout.TextStyle{Family: family, Size: size, Weight: 500, LineHeight: lh / size}
	}
	paintOf := func(c string) []paint { return []paint{{value: c, alpha: 1}} }
	p := jsonv.NewObject()
	p.Set("textGrowth", "fixed-width")
	left := &resolve.RNode{Type: "text", P: p}
	pc := jsonv.Clone(p).(*jsonv.Object)
	pc.Set("textAlign", "center")
	centred := &resolve.RNode{Type: "text", P: pc}
	icon := func(d string, x, y float64) string {
		return `<g transform="translate(` + f(x) + " " + f(y) + `) scale(0.6667)"><path d="` + d + `" fill="none" stroke="` + fg + `" stroke-width="2.25" stroke-linecap="round" stroke-linejoin="round"/></g>`
	}
	width := 200.0
	if isErr {
		width = 240
	}
	var body strings.Builder
	var y float64
	title, _ := jsonv.Str(jsonv.Get(sv, "title"))
	if isErr {
		body.WriteString(icon("M2.586 16.726A2 2 0 0 1 2 15.312V8.688a2 2 0 0 1 .586-1.414l4.688-4.688A2 2 0 0 1 8.688 2h6.624a2 2 0 0 1 1.414.586l4.688 4.688A2 2 0 0 1 22 8.688v6.624a2 2 0 0 1-.586 1.414l-4.688 4.688a2 2 0 0 1-1.414.586H8.688a2 2 0 0 1-1.414-.586zM15 9l-6 6M9 9l6 6", width/2-8, 0))
		y = 20
		st := style("Inter", 12, 16)
		lines := s.Fonts.Wrap(title, width, st)
		body.WriteString(s.linesMarkup(centred, st, lines, Rect{0, y, width, 0}, paintOf(fg)))
		y += float64(len(lines)) * 16
	} else {
		st := style("Inter", 10, 10)
		lines := s.Fonts.Wrap(title, math.Inf(1), st)
		tw := 16.0
		for _, l := range lines {
			tw = fmax(tw, l.Width)
		}
		body.WriteString(icon("m16 18 6-6-6-6M8 6l-6 6 6 6", width/2-8, 4))
		body.WriteString(s.linesMarkup(centred, st, lines, Rect{width/2 - tw/2, 24, tw, 0}, paintOf(fg)))
		y = 38
	}
	if d, ok := jsonv.Str(jsonv.Get(sv, "description")); ok && d != "" {
		st := style("Inter", 10, 14)
		lines := s.Fonts.Wrap(d, width, st)
		body.WriteString(s.linesMarkup(centred, st, lines, Rect{0, y + 4, width, 0}, paintOf(fg)))
		y += 4 + float64(len(lines))*14
	}
	if c, ok := jsonv.Str(jsonv.Get(sv, "code")); ok && isErr && c != "" {
		st := style("JetBrains Mono", 10, 12)
		lines := s.Fonts.Wrap(c, 224, st)
		h := 16 + float64(len(lines))*12
		body.WriteString(`<rect x="0" y="` + f(y+4) + `" width="` + f(width) + `" height="` + f(h) + `" rx="2" fill="#141414"/>`)
		body.WriteString(s.linesMarkup(left, st, lines, Rect{8, y + 12, 224, 0}, paintOf("#adadb2")))
		y += 4 + h
	}
	k := fmin(fmin(fmax(n.W-16, 0)/width, fmax(n.H-16, 0)/fmax(y, 1)), 1)
	if k > 0 {
		s.out.WriteString(`<g transform="translate(` + f(n.X+(n.W-width*k)/2) + " " + f(n.Y+(n.H-y*k)/2) + `) scale(` + f(k) + `)">` + body.String() + `</g>`)
	}
}
