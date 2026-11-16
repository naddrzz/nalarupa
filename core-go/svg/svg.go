// Package svg turns a laid-out render tree into SVG (core/src/svg.rs), the input of every render and the
// SVG export. Paint order: drop shadows (cast by the node's own outline, knocked out under it) → fills →
// inner shadows → stroke → children; a clipping frame draws its stroke above its children. A layer blur
// wraps everything the node draws, shadows included. The output is byte-identical to Rust's.
package svg

import (
	"math"
	"strings"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/layout"
	"github.com/naddrzz/nalarupa/core-go/resolve"
)

// level is one open container: whether it is rotated/flipped, and the ids it painted so far (the backdrop
// for background blur).
type level struct {
	turned bool
	ids    []string
}

// Svg emits nodes into one SVG document. It is not safe for concurrent use.
type Svg struct {
	Fonts  layout.TextMeasurer
	Assets *Assets
	// Base is the document's folder, for relative image paths ("" when unsaved).
	Base string
	// Skip holds nodes (with their subtrees) left out: they are sliding in and painted on their own.
	Skip map[string]bool
	// Embed writes image files as tokens of Embedded (SVG export) instead of linking local files;
	// InlineEmbedded replaces them later.
	Embed    bool
	Embedded Embedded
	// Shader paints shader fills; without one they draw the error hatch.
	Shader ShaderPainter

	defs, out strings.Builder
	uid       int
	painted   []level
	// tag gives nodes ids, only needed when the tree has a background blur.
	tag bool
	// outline is the shape whose fills are being emitted, for shader fills' `@sdf`.
	outline *Outline
	// pad is how far the paints being built reach outside the node box (a stroke's outer half): image
	// patterns grow by it so they do not tile there.
	pad float64
}

// DefaultShader is the shader painter New gives emitters: package shader sets it to the GPU painter when it
// is linked in (svg cannot import it), else shader fills draw the error hatch.
var DefaultShader ShaderPainter

// New returns an emitter; base is the document's folder ("" when unsaved).
func New(fonts layout.TextMeasurer, assets *Assets, base string) *Svg {
	return &Svg{Fonts: fonts, Assets: assets, Base: base, Skip: map[string]bool{}, Shader: DefaultShader}
}

// StartIDs numbers ids from uid on, so several inline SVGs in one page do not share ids; read back with UID.
func (s *Svg) StartIDs(uid int) *Svg {
	s.uid = uid
	return s
}

// UID is the last id number used.
func (s *Svg) UID() int { return s.uid }

// Finish wraps the emitted nodes into a document. With view the output is cropped to that box (exports);
// without, coordinates stay absolute (renderer cache).
func (s *Svg) Finish(view *Rect) string {
	head := `width="1" height="1"`
	if v := view; v != nil {
		head = `width="` + f(fmax(v.W, 1)) + `" height="` + f(fmax(v.H, 1)) + `" viewBox="` + f(v.X) + " " + f(v.Y) + " " + f(v.W) + " " + f(v.H) + `"`
	}
	return `<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" ` + head + `><defs>` + s.defs.String() + `</defs>` + s.out.String() + `</svg>`
}

// Wrap wraps what was emitted so far in an ancestor's painted matrix (nil for none) and opacity.
func (s *Svg) Wrap(m *[6]float64, opacity float64) {
	g := ""
	if opacity < 1 {
		g += ` opacity="` + f(opacity) + `"`
	}
	if m != nil {
		g += ` transform="matrix(` + f(m[0]) + " " + f(m[1]) + " " + f(m[2]) + " " + f(m[3]) + " " + f(m[4]) + " " + f(m[5]) + `)"`
	}
	if g != "" {
		body := s.out.String()
		s.out.Reset()
		s.out.WriteString("<g" + g + ">" + body + "</g>")
	}
}

func (s *Svg) id(prefix string) string {
	s.uid++
	return prefix + itoa(s.uid)
}

// Node emits n and its subtree.
func (s *Svg) Node(n *resolve.RNode) {
	if len(s.painted) == 0 {
		s.tag = hasBackdropBlur(n)
	}
	opacity := fOr(n, "opacity", 1)
	if opacity <= 0 || s.Skip[n.ID] {
		return
	}
	tf := transform(n)
	fx := EffectsOf(n)
	// The backdrop is blurred inside the node's opacity layer, which starts out empty: below full
	// opacity nothing shows.
	backdrop := fx.Backdrop > 0 && opacity >= 1
	// Id of the node's black outline (its mask path): what casts its shadows and shapes its background blur.
	outline := ""
	if len(fx.shadows) > 0 || backdrop {
		outline = s.id("m")
	}
	if backdrop {
		s.backdropBlur(n, tf, fx.Backdrop, outline)
	}
	gid := s.id("n")
	g := ""
	if s.tag {
		g += ` id="` + gid + `"`
	}
	if opacity < 1 {
		g += ` opacity="` + f(opacity) + `"`
	}
	if tf != "" {
		g += ` transform="` + tf + `"`
	}
	s.out.WriteString("<g" + g + ">")
	blur := s.openBlur(n, fx)
	switch n.Type {
	case "frame", "rectangle", "ellipse", "polygon", "path", "line":
		s.shape(n, fx, outline)
	case "group":
		s.group(n, fx, outline)
	case "text", "icon":
		s.glyphs(n, fx, outline)
	case "note", "prompt", "context":
		s.note(n)
	case "script":
		if n.P.Has("__status") {
			s.scriptStatus(n)
		} else {
			// Generated children in the node's box, clipped when `clip` is set.
			var clip *Geo
			if n.B("clip") {
				g := Geometry(n)
				clip = &g
			}
			s.children(n, "", clip)
		}
	case "browser":
		s.placeholder(n)
	}
	if blur {
		s.out.WriteString("</g>")
	}
	s.out.WriteString("</g>")
	if s.tag && len(s.painted) > 0 {
		l := &s.painted[len(s.painted)-1]
		l.ids = append(l.ids, gid)
	}
}

// openBlur opens the group of n's layer blur, if it has one; the caller closes it when this is true.
func (s *Svg) openBlur(n *resolve.RNode, fx Effects) bool {
	if fx.Blur <= 0 {
		return false
	}
	id := s.id("f")
	r := inflate(extent(n), 3*fx.Blur+fx.reach()+maxStroke(n)+2)
	s.defs.WriteString(`<filter id="` + id + `" filterUnits="userSpaceOnUse"` + region(r) + ` color-interpolation-filters="sRGB"><feGaussianBlur stdDeviation="` + f(fx.Blur) + `"/></filter>`)
	s.out.WriteString(`<g filter="url(#` + id + `)">`)
	return true
}

// region is ` x="…" y="…" width="…" height="…"`.
func region(r Rect) string {
	return ` x="` + f(r.X) + `" y="` + f(r.Y) + `" width="` + f(r.W) + `" height="` + f(r.H) + `"`
}

// Above emits only what stacks above node id inside n: for each ancestor of id, the siblings after the
// branch, within its rotation, flips, opacity, layer blur and clip. Repainted over a sliding id.
func (s *Svg) Above(n *resolve.RNode, id string) {
	i := -1
	for j, k := range n.Kids {
		if k.ID == id || findRNode(k, id) != nil {
			i = j
			break
		}
	}
	if i < 0 {
		return
	}
	opacity := fOr(n, "opacity", 1)
	if opacity <= 0 || s.Skip[n.ID] {
		return
	}
	tf := transform(n)
	var clip *Geo
	if n.Type == "frame" && n.B("clip") {
		g := Geometry(n)
		clip = &g
	}
	g := ""
	if opacity < 1 {
		g += ` opacity="` + f(opacity) + `"`
	}
	if tf != "" {
		g += ` transform="` + tf + `"`
	}
	s.out.WriteString("<g" + g + ">")
	blur := s.openBlur(n, EffectsOf(n))
	if len(s.painted) == 0 {
		// Siblings with a background blur reference what was painted before them.
		s.tag = hasBackdropBlur(n)
	}
	s.painted = append(s.painted, level{turned: tf != ""})
	if clip != nil {
		s.openClip(*clip)
	}
	if n.Kids[i].ID != id {
		s.Above(n.Kids[i], id)
	}
	for _, k := range n.Kids[i+1:] {
		s.Node(k)
	}
	if clip != nil {
		s.out.WriteString("</g>")
	}
	s.painted = s.painted[:len(s.painted)-1]
	if blur {
		s.out.WriteString("</g>")
	}
	s.out.WriteString("</g>")
}

func (s *Svg) shape(n *resolve.RNode, fx Effects, outline string) {
	geo := Geometry(n)
	b := box(n)
	sweep, hasSweep := n.F("sweepAngle")
	fullEllipse := fOr(n, "innerRadius", 0) <= 0 && (!hasSweep || math.Abs(sweep) >= 360)
	switch {
	case n.Type == "frame" || n.Type == "rectangle":
		s.outline = &Outline{Kind: OutlineRRect, Radii: Radii(n)}
	case n.Type == "ellipse" && fullEllipse:
		s.outline = &Outline{Kind: OutlineEllipse}
	default:
		s.outline = &Outline{Kind: OutlinePath, D: geo.D, X: n.X, Y: n.Y, EvenOdd: geo.EvenOdd}
	}
	var fills []paint
	if n.Type != "line" {
		v, _ := n.P.Get("fill")
		fills = s.paints(v, b)
	}
	s.outline = nil
	st := s.strokeOf(n, geo)
	// Shapes without a visible fill have no outline (no mask for them); frames always do.
	visible := false
	for _, p := range fills {
		if p.alpha > 0 {
			visible = true
		}
	}
	mask := ""
	if outline != "" && (visible || n.Type == "frame") {
		body := `<path d="` + geo.D + `"` + geo.rule("fill-rule") + ` fill="#000"/>`
		if st != nil && st.align != "inner" {
			body += s.strokeBody(n, geo, st, black, "")
		}
		s.defs.WriteString(`<g id="` + outline + `">` + body + `</g>`)
		mask = outline
	}
	reach := 0.0
	if st != nil {
		reach = st.reach()
	}
	mb := inflate(b, reach+1)
	round := roundOutline(n, st)
	if mask != "" {
		s.dropShadows(fx, mask, mb, round)
	}
	// Inner shadows are measured from the fill edge whatever the stroke: an outer/center stroke grows
	// only the drop shadow's silhouette, so it gets a fill-only mask of its own.
	grown := st != nil && st.align != "inner"
	innerMask := ""
	if mask != "" {
		innerMask = mask
		hasInner := false
		for _, sh := range fx.shadows {
			hasInner = hasInner || sh.inner
		}
		if grown && hasInner {
			innerMask = mask + "i"
			s.defs.WriteString(`<path id="` + innerMask + `" d="` + geo.D + `"` + geo.rule("fill-rule") + ` fill="#000"/>`)
		}
	}
	innerRound := round
	if grown {
		innerRound = roundOutline(n, nil)
	}
	// A clip group is isolated, so a blended child would blend against transparent instead of the frame's
	// fill: such a frame paints its fill and inner shadows inside the clip group too.
	clip := n.Type == "frame" && n.B("clip")
	inside := false
	if clip {
		kidBlends := false
		for _, k := range n.Kids {
			if Blends(k) {
				kidBlends = true
				break
			}
		}
		inside = kidBlends
		for _, p := range fills {
			inside = inside && p.blend == ""
		}
		for _, sh := range fx.shadows {
			inside = inside && (!sh.inner || sh.blend == "")
		}
	}
	if inside {
		s.openClip(geo)
	}
	fillsID := ""
	if s.tag && len(fills) > 0 {
		fillsID = s.id("f")
		s.out.WriteString(`<g id="` + fillsID + `">`)
	}
	for _, p := range fills {
		s.out.WriteString(`<path d="` + geo.D + `"` + geo.rule("fill-rule") + p.attrs("fill") + `/>`)
	}
	if fillsID != "" {
		s.out.WriteString("</g>")
	}
	fv, _ := n.P.Get("fill")
	unavailable := false
	for _, fl := range Fills(fv) {
		u, _ := jsonv.Str(jsonv.Get(fl, "url"))
		if jsonv.StrIs(jsonv.Get(fl, "type"), "image") && !IsPendingURL(u) {
			if _, ok := s.Assets.Image(u, s.Base); !ok && !s.Assets.Loading(u) {
				unavailable = true
				break
			}
		}
	}
	if unavailable {
		// An unavailable image is outlined with a 2 px grey stroke clipped to the shape.
		c := s.id("c")
		s.defs.WriteString(`<clipPath id="` + c + `"><path d="` + geo.D + `"` + geo.rule("clip-rule") + `/></clipPath>`)
		s.out.WriteString(`<path d="` + geo.D + `" fill="none" stroke="#808080" stroke-width="2" clip-path="url(#` + c + `)"/>`)
	}
	if innerMask != "" {
		s.innerShadows(fx, innerMask, mb, innerRound)
	}
	if !clip {
		s.strokePaint(n, geo, st)
	}
	if n.Type == "frame" {
		var cg *Geo
		if clip && !inside {
			cg = &geo
		}
		s.children(n, fillsID, cg)
	}
	if inside {
		s.out.WriteString("</g>")
	}
	if clip {
		s.strokePaint(n, geo, st)
	}
	if v, ok := n.P.Get("slot"); n.Type == "frame" && len(n.Kids) == 0 && ok && v != nil {
		s.slot(n)
	}
}

// slot is the empty-slot marker: the frame inset 10 px (radii − 10), hatched with 1 px stripes every 5 px
// at 135° plus a 1 px outline; magenta in a component, purple in an instance.
func (s *Svg) slot(n *resolve.RNode) {
	w, h := n.W-20, n.H-20
	if w <= 0 || h <= 0 {
		return
	}
	color := "#D480FF"
	if strings.Contains(n.ID, "/") || n.Inst != "" {
		color = "#9580FF"
	}
	rr := Radii(n)
	for i := range rr {
		rr[i] = fmax(rr[i]-10, 0)
	}
	r := scaleRadii(rr, w, h)
	ix, iy := n.X+10, n.Y+10
	d := rrectD(ix, iy, w, h, [4][2]float64{{r[0], r[0]}, {r[1], r[1]}, {r[2], r[2]}, {r[3], r[3]}})
	// Stripe centres lie where the frame-local point projected on (−1, 1)/√2 is 5·(k + ½).
	const sq = math.Sqrt2 / 2
	proj := func(x, y float64) float64 { return (y - x) * sq }
	lo, hi := math.MaxFloat64, -math.MaxFloat64
	for _, p := range [][2]float64{{10, 10}, {n.W - 10, 10}, {10, n.H - 10}, {n.W - 10, n.H - 10}} {
		lo, hi = fmin(lo, proj(p[0], p[1])), fmax(hi, proj(p[0], p[1]))
	}
	reach := n.W + n.H
	var stripes strings.Builder
	for k := int64(math.Floor(lo/5 - 0.5)); k <= int64(math.Ceil(hi/5)); k++ {
		c := 5 * (float64(k) + 0.5)
		cx, cy := n.X-c*sq, n.Y+c*sq
		stripes.WriteString("M" + f2(cx-reach*sq) + " " + f2(cy-reach*sq) + "L" + f2(cx+reach*sq) + " " + f2(cy+reach*sq))
	}
	clip := s.id("c")
	s.defs.WriteString(`<clipPath id="` + clip + `"><path d="` + d + `"/></clipPath>`)
	s.out.WriteString(`<path d="` + stripes.String() + `" fill="none" stroke="` + color + `" stroke-width="1" clip-path="url(#` + clip + `)"/><path d="` + d + `" fill="none" stroke="` + color + `" stroke-width="1"/>`)
}

func (s *Svg) group(n *resolve.RNode, fx Effects, outline string) {
	kids := ""
	if outline != "" {
		kids = outline + "k"
	}
	// The children's strokes, shadows and blur paint outside their boxes and shape the group's outline.
	b := extent(n)
	for _, k := range n.Kids {
		b = union(b, VisualBounds(k))
	}
	if outline != "" {
		// The children's outline in solid black (the union of their masks).
		fid := s.id("f")
		s.defs.WriteString(`<filter id="` + fid + `" filterUnits="userSpaceOnUse"` + region(inflate(b, 2)) +
			`><feFlood flood-color="#000"/><feComposite in2="SourceAlpha" operator="in"/></filter><g id="` + outline + `" filter="url(#` + fid + `)"><use xlink:href="#` + kids + `"/></g>`)
		s.dropShadows(fx, outline, b, nil)
		s.out.WriteString(`<g id="` + kids + `">`)
	}
	s.children(n, "", nil)
	if outline != "" {
		s.out.WriteString("</g>")
		s.innerShadows(fx, outline, b, nil)
	}
}

// glyphs paints text and icons: every fill paints the glyphs; shadows use the glyph outline.
func (s *Svg) glyphs(n *resolve.RNode, fx Effects, outline string) {
	text := n.Type == "text"
	// Text takes a stroke like any shape; icons draw their own strokes.
	var st *stroke
	if text {
		st = s.strokeOf(n, Geo{Closed: true})
	}
	reach := 0.0
	if st != nil {
		reach = st.reach()
	}
	b := box(n)
	var fills []paint
	if text {
		b = inflate(ownBox(n), layout.StyleOf(n).Size/2+reach)
		v, _ := n.P.Get("fill")
		fills = s.paints(v, box(n))
	}
	visible := false
	for _, p := range fills {
		visible = visible || p.alpha > 0
	}
	if outline != "" {
		// Like shapes, glyphs without a visible fill are no outline: stroke-only text casts its stroke.
		body := ""
		switch {
		case !text:
			body = s.iconMarkup(n, black)
		case visible:
			body = s.textMarkup(n, black)
		}
		switch {
		case st != nil && !visible:
			body += s.textStroke(n, st, black)
		case st != nil && st.align != "inner":
			// Over the fill an unclipped stroke adds the same outline; an inner one adds nothing.
			body += s.textStrokeMarkup(n, st, black, "")
		}
		s.defs.WriteString(`<g id="` + outline + `">` + body + `</g>`)
		s.dropShadows(fx, outline, b, nil)
	}
	if text {
		s.out.WriteString(s.textMarkup(n, fills))
	} else {
		s.out.WriteString(s.iconMarkup(n, nil))
	}
	if outline != "" {
		s.innerShadows(fx, outline, b, nil)
	}
	if st != nil {
		s.out.WriteString(s.textStroke(n, st, st.paints))
	}
}

// openClip opens a group clipped to g; the caller closes it.
func (s *Svg) openClip(g Geo) {
	id := s.id("c")
	s.defs.WriteString(`<clipPath id="` + id + `"><path d="` + g.D + `"` + g.rule("clip-rule") + `/></clipPath>`)
	s.out.WriteString(`<g clip-path="url(#` + id + `)">`)
}

func (s *Svg) children(n *resolve.RNode, fillsID string, clip *Geo) {
	if len(n.Kids) == 0 {
		return
	}
	l := level{turned: transform(n) != ""}
	if fillsID != "" {
		l.ids = []string{fillsID}
	}
	s.painted = append(s.painted, l)
	if clip != nil {
		s.openClip(*clip)
	}
	for _, k := range n.Kids {
		s.Node(k)
	}
	if clip != nil {
		s.out.WriteString("</g>")
	}
	s.painted = s.painted[:len(s.painted)-1]
}

// roundRect is a shadow source as (box, radius).
type roundRect struct {
	b Rect
	r float64
}

func blendStyle(blend string) string {
	if blend == "" {
		return ""
	}
	return ` style="mix-blend-mode:` + blend + `"`
}

// dropShadows emits the drop shadows of the black outline m, knocked out where the outline itself is.
// With a rounded-rect outline the group is tagged for analytic shadows (raster.rs).
func (s *Svg) dropShadows(fx Effects, m string, b Rect, outline *roundRect) {
	var drops []shadow
	for _, sh := range fx.shadows {
		if !sh.inner {
			drops = append(drops, sh)
		}
	}
	if len(drops) == 0 {
		return
	}
	reg := b
	for _, sh := range drops {
		reg = union(reg, inflate(Rect{b.X + sh.dx, b.Y + sh.dy, b.W, b.H}, 3*sh.sigma+1))
	}
	ko := s.id("k")
	s.defs.WriteString(`<mask id="` + ko + `" maskUnits="userSpaceOnUse"` + region(reg) + `><rect` + region(reg) + ` fill="#fff"/><use xlink:href="#` + m + `"/></mask>`)
	tag := ""
	if outline != nil {
		plain := true
		for _, sh := range drops {
			plain = plain && sh.blend == ""
		}
		if plain {
			tag = ` id="` + shadowTag('d', ko, *outline, drops) + `"`
		}
	}
	s.out.WriteString(`<g` + tag + ` mask="url(#` + ko + `)">`)
	for _, sh := range drops {
		fid := s.id("s")
		src := `<feOffset in="SourceAlpha"`
		if sh.sigma > 0 {
			src = `<feGaussianBlur in="SourceAlpha" stdDeviation="` + f(sh.sigma) + `"/><feOffset`
		}
		s.defs.WriteString(`<filter id="` + fid + `" filterUnits="userSpaceOnUse"` + region(reg) + ` color-interpolation-filters="sRGB">` + src + ` dx="` + f(sh.dx) + `" dy="` + f(sh.dy) +
			`" result="o"/><feFlood flood-color="` + sh.color + `" flood-opacity="` + f(sh.alpha) + `"/><feComposite in2="o" operator="in"/></filter>`)
		s.out.WriteString(`<use xlink:href="#` + m + `" filter="url(#` + fid + `)"` + blendStyle(sh.blend) + `/>`)
	}
	s.out.WriteString("</g>")
}

// innerShadows emits the offset, blurred inverse of the outline, kept inside the outline.
func (s *Svg) innerShadows(fx Effects, m string, b Rect, outline *roundRect) {
	for _, sh := range fx.shadows {
		if !sh.inner {
			continue
		}
		r := inflate(b, math.Abs(sh.dx)+math.Abs(sh.dy)+3*sh.sigma+2)
		fid := s.id("s")
		blur := ""
		if sh.sigma > 0 {
			blur = `<feGaussianBlur stdDeviation="` + f(sh.sigma) + `"/>`
		}
		s.defs.WriteString(`<filter id="` + fid + `" filterUnits="userSpaceOnUse"` + region(r) + ` color-interpolation-filters="sRGB"><feFlood flood-color="` + sh.color + `" flood-opacity="` + f(sh.alpha) +
			`"/><feComposite in2="SourceAlpha" operator="out"/><feOffset dx="` + f(sh.dx) + `" dy="` + f(sh.dy) + `"/>` + blur + `<feComposite in2="SourceAlpha" operator="in"/></filter>`)
		tag := ""
		if outline != nil && sh.blend == "" {
			tag = ` id="` + shadowTag('i', fid, *outline, []shadow{sh}) + `"`
		}
		s.out.WriteString(`<use` + tag + ` xlink:href="#` + m + `" filter="url(#` + fid + `)"` + blendStyle(sh.blend) + `/>`)
	}
}

// backdropBlur is the background blur through the node's outline m. The canvas renderer draws it from the
// `nrbb:{uid}:{sigma}` tag; in an export the group's content approximates the backdrop with what this
// top-level node already painted.
func (s *Svg) backdropBlur(n *resolve.RNode, tf string, sigma float64, m string) {
	var items []string
	for i := len(s.painted) - 1; i >= 0; i-- {
		items = append(append([]string(nil), s.painted[i].ids...), items...)
		if s.painted[i].turned {
			break
		}
	}
	b, fid := s.id("b"), s.id("f")
	l := localAABB(n)
	px, py := n.X-n.BX, n.Y-n.BY
	aabb := Rect{px + l[0], py + l[1], l[2] - l[0], l[3] - l[1]}
	area := aabb
	switch n.Type {
	case "group":
		area = union(aabb, extent(n))
	case "text":
		area = union(aabb, turn(n, ownBox(n)))
	}
	// The outline's reach past the box: outer strokes, glyph overhang.
	reach := maxStroke(n)
	if n.Type == "text" {
		reach += layout.StyleOf(n).Size
	}
	mr := inflate(area, reach+1)
	fr := inflate(mr, 3*sigma)
	t := ""
	if tf != "" {
		t = ` transform="` + tf + `"`
	}
	s.defs.WriteString(`<mask id="` + b + `" mask-type="alpha" maskUnits="userSpaceOnUse"` + region(mr) + `><use xlink:href="#` + m + `"` + t + `/></mask><filter id="` + fid +
		`" filterUnits="userSpaceOnUse"` + region(fr) + ` color-interpolation-filters="sRGB"><feGaussianBlur stdDeviation="` + f(sigma) + `"/></filter>`)
	var uses strings.Builder
	for _, i := range items {
		uses.WriteString(`<use xlink:href="#` + i + `"/>`)
	}
	s.out.WriteString(`<g id="nrbb:` + b + `:` + f(sigma) + `" mask="url(#` + b + `)"><g filter="url(#` + fid + `)">` + uses.String() + `</g></g>`)
}

func (s *Svg) strokeOf(n *resolve.RNode, geo Geo) *stroke {
	v, ok := n.P.Get("strokeWidth")
	if !ok {
		return nil
	}
	var widths [4]float64
	if o, isObj := v.(*jsonv.Object); isObj {
		for i, k := range []string{"top", "right", "bottom", "left"} {
			x, _ := o.Get(k)
			widths[i] = fmax(numOr(x, 0), 0)
		}
	} else {
		w := fmax(numOr(v, 0), 0)
		widths = [4]float64{w, w, w, w}
	}
	// Non-rect shapes only use the top width.
	if n.Type != "frame" && n.Type != "rectangle" {
		widths = [4]float64{widths[0], widths[0], widths[0], widths[0]}
	}
	if widths[0] <= 0 && widths[1] <= 0 && widths[2] <= 0 && widths[3] <= 0 {
		return nil
	}
	align := "center"
	if geo.Closed {
		switch a, _ := n.S("strokeAlignment"); a {
		case "inner", "inside":
			align = "inner"
		case "outer", "outside":
			align = "outer"
		}
	}
	st := &stroke{widths: widths, align: align}
	// Inner strokes stay inside the box; a miter tip past the pad repeats the pattern edge (accepted).
	s.pad = 0
	if align != "inner" {
		s.pad = st.reach() + 2
	}
	sv, _ := n.P.Get("stroke")
	st.paints = s.paints(sv, box(n))
	s.pad = 0
	if len(st.paints) == 0 {
		return nil
	}
	return st
}

// lineAttrs are the stroke-linecap and stroke-linejoin attributes the node sets.
func lineAttrs(n *resolve.RNode) string {
	out := ""
	for _, pa := range [][2]string{{"strokeLinecap", "stroke-linecap"}, {"strokeLinejoin", "stroke-linejoin"}} {
		if v, ok := n.S(pa[0]); ok {
			out += " " + pa[1] + `="` + Esc(v) + `"`
		}
	}
	return out
}

// strokeBody is the stroke outline painted with paints, unclipped. Rects/frames build a per-side band
// (filled); other shapes use an SVG stroke of k·width. extra is added to every path.
func (s *Svg) strokeBody(n *resolve.RNode, geo Geo, st *stroke, paints []paint, extra string) string {
	k := st.k()
	var out strings.Builder
	if n.Type == "frame" || n.Type == "rectangle" {
		w := st.widths
		for i := range w {
			w[i] *= k
		}
		d := rectStrokeD(n, w, st.align != "outer", sIs(n, "strokeLinejoin", "round"))
		for _, p := range paints {
			out.WriteString(`<path d="` + d + `" fill-rule="evenodd"` + p.attrs("fill") + extra + `/>`)
		}
		return out.String()
	}
	attrs := ` fill="none" stroke-width="` + f(st.widths[0]*k) + `"` + lineAttrs(n)
	for _, p := range paints {
		out.WriteString(`<path d="` + geo.D + `"` + attrs + p.attrs("stroke") + extra + `/>`)
	}
	return out.String()
}

// strokePaint emits the stroke; inner/outer strokes are double-width strokes clipped to inside/outside the fill.
func (s *Svg) strokePaint(n *resolve.RNode, geo Geo, st *stroke) {
	if st == nil {
		return
	}
	blended := false
	for _, p := range st.paints {
		blended = blended || p.blend != ""
	}
	clip := ""
	if st.align == "inner" {
		id := s.id("c")
		s.defs.WriteString(`<clipPath id="` + id + `"><path d="` + geo.D + `"` + geo.rule("clip-rule") + `/></clipPath>`)
		clip = ` clip-path="url(#` + id + `)"`
	}
	// A clip group is isolated, so a blended stroke would blend against transparent: clip each path instead.
	extra := ""
	if blended {
		extra = clip
	}
	body := s.strokeBody(n, geo, st, st.paints, extra)
	switch {
	case st.align == "inner" && blended:
		s.out.WriteString(body)
	case st.align == "inner":
		s.out.WriteString(`<g` + clip + `>` + body + `</g>`)
	case st.align == "outer":
		id := s.id("k")
		r := inflate(box(n), st.reach()+2)
		s.defs.WriteString(`<mask id="` + id + `" maskUnits="userSpaceOnUse"` + region(r) + `><rect` + region(r) + ` fill="#fff"/><path d="` + geo.D + `"` + geo.rule("fill-rule") + ` fill="#000"/></mask>`)
		s.out.WriteString(`<g mask="url(#` + id + `)">` + body + `</g>`)
	default:
		s.out.WriteString(body)
	}
}

// roundOutline is the shadow source of a frame/rectangle as (box, radius) when it is one uniformly
// rounded rect: the fill outline grown by an outer/center stroke the way rectStrokeD grows it.
func roundOutline(n *resolve.RNode, st *stroke) *roundRect {
	r := Radii(n)
	if n.Type != "frame" && n.Type != "rectangle" {
		return nil
	}
	for _, v := range r {
		if math.Abs(v-r[0]) > 1e-9 {
			return nil
		}
	}
	if st == nil || st.align == "inner" {
		return &roundRect{box(n), r[0]}
	}
	for _, w := range st.widths {
		if math.Abs(w-st.widths[0]) > 1e-9 {
			return nil
		}
	}
	e := st.reach()
	rad := r[0]
	if st.align != "outer" && r[0] < e {
		rad = 0
	}
	outer := 0.0
	switch {
	case rad > 0:
		outer = rad + e
	case sIs(n, "strokeLinejoin", "round"):
		outer = e
	}
	return &roundRect{inflate(box(n), e), outer}
}

// shadowTag is the group id that lets raster.rs draw these shadows analytically:
// `nrsh:{d|i}:{uid}:{x},{y},{w},{h},{radius}:{dx},{dy},{sigma},{color},{alpha};…`.
func shadowTag(kind byte, uid string, o roundRect, list []shadow) string {
	parts := make([]string, len(list))
	for i, sh := range list {
		parts[i] = f(sh.dx) + "," + f(sh.dy) + "," + f(sh.sigma) + "," + sh.color + "," + f(sh.alpha)
	}
	return "nrsh:" + string(kind) + ":" + uid + ":" + f(o.b.X) + "," + f(o.b.Y) + "," + f(o.b.W) + "," + f(o.b.H) + "," + f(o.r) + ":" + strings.Join(parts, ";")
}

// blursBackdrop reports whether an `effect` value holds an enabled background blur.
func blursBackdrop(effect jsonv.Value, present bool) bool {
	is := func(e jsonv.Value) bool {
		return jsonv.StrIs(jsonv.Get(e, "type"), "background_blur") && !jsonv.IsFalse(jsonv.Get(e, "enabled"))
	}
	if a, ok := effect.(*jsonv.Array); ok {
		for _, e := range a.Items {
			if is(e) {
				return true
			}
		}
		return false
	}
	return present && is(effect)
}

// HasBackdropBlur reports whether n or a descendant shows a background blur.
func HasBackdropBlur(n *resolve.RNode) bool { return hasBackdropBlur(n) }

func hasBackdropBlur(n *resolve.RNode) bool {
	v, ok := n.P.Get("effect")
	if blursBackdrop(v, ok) {
		return true
	}
	for _, k := range n.Kids {
		if hasBackdropBlur(k) {
			return true
		}
	}
	return false
}

// DocHasBackdropBlur is hasBackdropBlur on a document node.
func DocHasBackdropBlur(n jsonv.Value) bool {
	v, ok := get(n, "effect")
	if blursBackdrop(v, ok) {
		return true
	}
	for _, k := range jsonv.Items(jsonv.Get(n, "children")) {
		if DocHasBackdropBlur(k) {
			return true
		}
	}
	return false
}

// ClipSVG is the clip shape of a node that clips its children, as a one-shape SVG (opaque white) under
// the matrix m: the mask of a sliding descendant.
func ClipSVG(n *resolve.RNode, m *[6]float64) (string, bool) {
	if !n.B("clip") || !(n.Type == "frame" || n.Type == "script" && !n.P.Has("__status")) {
		return "", false
	}
	g := Geometry(n)
	tf := ""
	if m != nil {
		tf = ` transform="matrix(` + f(m[0]) + " " + f(m[1]) + " " + f(m[2]) + " " + f(m[3]) + " " + f(m[4]) + " " + f(m[5]) + `)"`
	}
	return `<svg xmlns="http://www.w3.org/2000/svg" width="1" height="1"><path d="` + g.D + `"` + g.rule("fill-rule") + ` fill="#fff"` + tf + `/></svg>`, true
}

// IsPendingURL reports an image that is still being generated.
func IsPendingURL(u string) bool {
	return strings.HasPrefix(u, "nalarupa:pending") || strings.HasPrefix(u, "pencil:pending")
}

func findRNode(n *resolve.RNode, id string) *resolve.RNode {
	if n.ID == id {
		return n
	}
	for _, k := range n.Kids {
		if r := findRNode(k, id); r != nil {
			return r
		}
	}
	return nil
}
