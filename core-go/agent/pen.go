package agent

// The "AI Pen" (pen.rs; streaming SVG generation, `Generate("svg", frameId, prompt)`): the model writes an SVG and
// the frame fills while it streams. Every element becomes a path node as soon as its tag is complete; the path
// still being written shows as a thin outline that grows with each finished number, under a pen cursor, and gets
// its real paint when its tag closes. The streamed prefix is closed into a well-formed document and parsed by
// usvg, so styles, groups, transforms, `use` and gradients resolve exactly as in a full parse.

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/naddrzz/nalarupa/core-go/engine"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/libm"
	"github.com/naddrzz/nalarupa/core-go/model"
	"github.com/naddrzz/nalarupa/core-go/skia"
	"github.com/naddrzz/nalarupa/core-go/usvg"
)

const penSystem = "You are a vector illustrator. Reply with one standalone SVG document and nothing else: no prose, no markdown fences. " +
	"Start with <svg and give it a viewBox. Use only path, rect, circle, ellipse, line, polyline, polygon, g, and linearGradient / radialGradient inside defs; " +
	"no text, images, filters, masks, patterns, scripts or animation. Paint back to front: large background shapes first, details last. " +
	"Give every shape an explicit fill (and stroke where needed) as hex colors. Keep it lean: at most about 80 shapes, path data with few decimals."

// penID is given to the element whose geometry is still streaming, to find it in the parsed tree.
const penID = "nalarupa-pen"

// penPreview is the outline of the path being drawn (Nalarupa's agent presence color).
const penPreview = "#607DFF"

// applyEvery: the document is updated at most this often while the model writes.
const applyEvery = 50 * time.Millisecond

// startPen draws prompt into frame (w×h) in the background (pen.rs `start`). It stops early when cancel (the
// pen's entry in Agent.Pens) is set by Stop, or the frame disappears; then it removes that entry.
func startPen(e *engine.Engine, frame, prompt string, w, h float64, cancel *engine.Flag) {
	go func() {
		defer func() {
			// A panic ends this pen only (Rust's thread would die and leave its entry behind), and its frame
			// finishes as after a normal end, without drawing again.
			r := recover()
			e.Locked(func() {
				if r != nil {
					e.Emit(engine.Object("type", "toast", "text", fmt.Sprintf("AI Pen: internal error (panic): %v", r)))
					e.Emit(engine.Object("type", "pen_end", "id", frame))
					e.ApplyGenerated(frame, engine.Object("placeholder", false), true)
				}
				// Its own entry: another pen drawing into the same frame keeps its own.
				for i, p := range e.Agent.Pens {
					if p.Cancel == cancel {
						e.Agent.Pens = append(e.Agent.Pens[:i:i], e.Agent.Pens[i+1:]...)
						break
					}
				}
			})
		}()
		pen := &pen{e: e, frame: frame, w: w, h: h}
		ask := fmt.Sprintf("%s\n\nThe artwork fills a %.0f × %.0f frame: use a viewBox with that aspect ratio.", prompt, w, h)
		if err := penGenerate(e, ask, cancel, func(t string) bool { return pen.feed(t, false) }); err != nil {
			e.Locked(func() { e.Emit(engine.Object("type", "toast", "text", "AI Pen: "+err.Error())) })
		}
		pen.feed("", true)
	}()
}

type pen struct {
	e     *engine.Engine
	frame string
	w, h  float64
	text  strings.Builder
	// ids is the node id per drawn element, stable while the drawing grows.
	ids     []string
	applied time.Time
	// seen is the text length at the last update.
	seen     int
	point    [2]float64
	hasPoint bool
}

// feed takes more model output and redraws when due. False once the frame is gone (stop generating).
func (p *pen) feed(t string, done bool) bool {
	p.text.WriteString(t)
	if !done && (p.text.Len() == p.seen || (!p.applied.IsZero() && time.Since(p.applied) < applyEvery)) {
		return true
	}
	p.applied = time.Now()
	p.seen = p.text.Len()
	d := penDraw(p.text.String(), p.w, p.h, done)
	e := p.e
	e.Lock()
	defer e.Unlock()
	props := jsonv.NewObject()
	if d != nil {
		kids := jsonv.NewArray()
		for i, k := range d.nodes {
			if len(p.ids) <= i {
				p.ids = append(p.ids, e.Doc.GenID())
			}
			k.Set("id", p.ids[i])
			kids.Items = append(kids.Items, k)
		}
		props.Set("children", kids)
		// The frame keeps its size, lays nothing out and clips the artwork.
		props.Set("layout", "none")
		props.Set("clip", true)
		if d.hasPoint && (!p.hasPoint || d.point != p.point) {
			p.point, p.hasPoint = d.point, true
			if f := e.Renderer.Find(p.frame); f != nil {
				e.Emit(engine.Object("type", "pen", "id", p.frame, "x", jsonv.Float(f.X+d.point[0]), "y", jsonv.Float(f.Y+d.point[1])))
			}
		}
	}
	if done {
		props.Set("placeholder", false)
		e.Emit(engine.Object("type", "pen_end", "id", p.frame))
	}
	if props.Len() == 0 {
		return e.Doc.Find(p.frame) != nil
	}
	return e.ApplyGenerated(p.frame, props, done)
}

// penGenerate streams the SVG text from the model (the agent's backend) into on (pen.rs `generate`).
func penGenerate(e *engine.Engine, ask string, cancel *engine.Flag, on func(string) bool) error {
	if Backend(e) == "claude-code" {
		return penClaudeCode(e, ask, cancel, on)
	}
	var client model.Provider
	var name string
	var err error
	e.Locked(func() {
		var ok bool
		if name, ok, err = e.RunModel(e.Backend()); err != nil {
			return
		}
		if !ok {
			name = DefaultModel
		}
		client, err = e.CurrentProvider()
	})
	if err != nil {
		return err
	}
	body := engine.Object("model", name, "max_tokens", jsonv.Number("16000"), "system", penSystem,
		"messages", jsonv.NewArray(engine.Object("role", "user", "content", ask)))
	var gone atomic.Bool
	_, err = client.Stream(body, "", func() bool { return gone.Load() || cancel.IsSet() }, func(ev model.Event) {
		if ev.Kind == model.EventText && !on(ev.Text) {
			gone.Store(true)
		}
	})
	if err != nil && err.Error() != "stopped" {
		return err
	}
	return nil
}

// penArgs are the `claude` CLI's arguments for the pen (pen.rs `cli`): no tools or session, on the composer's
// model (unset is the CLI's default, as for the agent's own process).
func penArgs(ask, model string, hasModel bool) []string {
	args := []string{"-p", ask, "--output-format", "stream-json", "--verbose", "--include-partial-messages", "--no-session-persistence",
		"--strict-mcp-config", "--tools", "", "--system-prompt", penSystem}
	if hasModel {
		args = append(args, "--model", model)
	}
	return args
}

// penClaudeCode streams the `claude` CLI's text deltas into on (pen.rs `claude_code`).
func penClaudeCode(e *engine.Engine, ask string, cancel *engine.Flag, on func(string) bool) error {
	var name string
	var has bool
	e.Locked(func() { name, has = e.Setting("agent_model") })
	cmd := command(claudeProgram, penArgs(ask, name, has)...)
	stdout, err := cmd.StdoutPipe()
	if err == nil {
		err = cmd.Start()
	}
	if err != nil {
		text, _ := startError(err)
		return fmt.Errorf("could not start the `claude` CLI (%s)", text)
	}
	var failed error
	// ponytail: cancellation is noticed at the next streamed line; a watcher goroutine could kill a silent process sooner.
	r := bufio.NewReader(stdout)
read:
	for {
		line, err := r.ReadString('\n')
		if err != nil && (err != io.EOF || line == "") {
			break
		}
		// Rust's `lines`: the "\n" or "\r\n" goes; a line that is not UTF-8 ends the read.
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if !utf8.ValidString(line) || cancel.IsSet() {
			break
		}
		ev, perr := jsonv.Parse([]byte(line))
		if perr != nil {
			continue
		}
		switch jsonv.StrOr(jsonv.Get(ev, "type")) {
		case "stream_event":
			delta := jsonv.Get(jsonv.Get(ev, "event"), "delta")
			if jsonv.StrIs(jsonv.Get(delta, "type"), "text_delta") && !on(jsonv.StrOr(jsonv.Get(delta, "text"))) {
				break read
			}
		case "result":
			if jsonv.IsTrue(jsonv.Get(ev, "is_error")) {
				msg, ok := jsonv.Str(jsonv.Get(ev, "result"))
				if !ok {
					msg = "Claude Code reported an error"
				}
				failed = errors.New(msg)
			}
		}
	}
	cmd.Process.Kill()
	cmd.Wait()
	return failed
}

// drawing is the nodes for the SVG streamed so far, fitted into the frame, plus the pen position (frame
// coordinates).
type drawing struct {
	nodes    []*jsonv.Object
	point    [2]float64
	hasPoint bool
}

// penDraw is pen.rs `draw`: nil until the root `<svg>` tag is complete.
func penDraw(text string, fw, fh float64, done bool) *drawing {
	at := strings.Index(text, "<svg")
	if at < 0 {
		return nil
	}
	src := text[at:]
	if i := strings.Index(src, "</svg>"); i >= 0 && done {
		src = src[:i+6]
	}
	doc, ok := closeSVG(src)
	if !ok {
		return nil
	}
	opt := usvg.DefaultOptions()
	// Model output never reads local or remote files.
	opt.ImageHrefResolver = usvg.ImageHrefResolver{
		ResolveData:   func(string, []byte, *usvg.Options) (usvg.ImageKind, bool) { return nil, false },
		ResolveString: func(string, *usvg.Options) (usvg.ImageKind, bool) { return nil, false },
	}
	tree, err := usvg.FromStr(doc, &opt)
	if err != nil {
		return nil
	}
	w0, h0 := float64(tree.Size().Width()), float64(tree.Size().Height())
	// The drawing fits into the frame (contain, centered).
	s := math.Min(fw/w0, fh/h0)
	fit := skia.TransformFromRow(float32(s), 0, 0, float32(s), float32((fw-w0*s)/2), float32((fh-h0*s)/2))
	out := &drawing{}
	penWalk(tree.Root(), fit, 1, math.Max(w0*s/1000, 0.5), out)
	return out
}

func penWalk(g *usvg.Group, fit skia.Transform, opacity, outline float64, out *drawing) {
	for _, n := range g.Children() {
		switch n := n.(type) {
		// ponytail: group opacity is applied per path (overlapping children blend), and clips/masks are dropped.
		case *usvg.Group:
			penWalk(n, fit, opacity*float64(n.Opacity()), outline, out)
		case *usvg.Path:
			if n.IsVisible() {
				if node := pathNode(n, fit, opacity, outline, out); node != nil {
					out.nodes = append(out.nodes, node)
				}
			}
		}
	}
}

func r2(v float64) float64 { return math.Round(v*100) / 100 }
func r3(v float64) float64 { return math.Round(v*1000) / 1000 }

func pathNode(p *usvg.Path, fit skia.Transform, opacity, outline float64, out *drawing) *jsonv.Object {
	ts := fit.PreConcat(p.AbsTransform())
	data := p.Data().Transform(ts)
	if data == nil {
		return nil
	}
	num := func(v float32) string { return engine.RustF64(r2(float64(v))) }
	var d strings.Builder
	it := data.Segments()
	for {
		seg, ok := it.Next()
		if !ok {
			break
		}
		a, b, c := seg.Pts[0], seg.Pts[1], seg.Pts[2]
		switch seg.Verb {
		case skia.VerbMove:
			fmt.Fprintf(&d, "M%s %s", num(a.X), num(a.Y))
		case skia.VerbLine:
			fmt.Fprintf(&d, "L%s %s", num(a.X), num(a.Y))
		case skia.VerbQuad:
			fmt.Fprintf(&d, "Q%s %s %s %s", num(a.X), num(a.Y), num(b.X), num(b.Y))
		case skia.VerbCubic:
			fmt.Fprintf(&d, "C%s %s %s %s %s %s", num(a.X), num(a.Y), num(b.X), num(b.Y), num(c.X), num(c.Y))
		default:
			d.WriteString("Z")
		}
	}
	if pts := data.Points(); len(pts) > 0 {
		last := pts[len(pts)-1]
		out.point, out.hasPoint = [2]float64{float64(last.X), float64(last.Y)}, true
	}
	bb := data.Bounds()
	box := [4]float64{float64(bb.X()), float64(bb.Y()), float64(bb.Width()), float64(bb.Height())}
	x, y, w, h := jsonv.Float(r2(box[0])), jsonv.Float(r2(box[1])), jsonv.Float(r2(box[2])), jsonv.Float(r2(box[3]))
	n := engine.Object("type", "path", "x", x, "y", y, "width", w, "height", h, "geometry", d.String(), "viewBox", jsonv.NewArray(x, y, w, h))
	if p.ID() == penID {
		n.Set("stroke", penPreview)
		n.Set("strokeWidth", jsonv.Float(r2(outline)))
		return n
	}
	if opacity < 1 {
		n.Set("opacity", jsonv.Float(r2(opacity)))
	}
	if f := p.Fill(); f != nil {
		if v := penPaint(f.Paint(), float64(f.Opacity()), ts, box); v != nil {
			n.Set("fill", v)
		}
		if f.Rule() == usvg.FillRuleEvenOdd {
			n.Set("fillRule", "evenodd")
		}
	}
	if s := p.Stroke(); s != nil {
		if v := penPaint(s.Paint(), float64(s.Opacity()), ts, box); v != nil {
			// In f32, as Rust computes it.
			scale := math.Sqrt(float64(float32(math.Abs(float64(float32(ts.SX*ts.SY) - float32(ts.KX*ts.KY))))))
			n.Set("stroke", v)
			n.Set("strokeWidth", jsonv.Float(r2(float64(s.Width())*scale)))
			linecap := "butt"
			switch s.Linecap() {
			case usvg.LineCapRound:
				linecap = "round"
			case usvg.LineCapSquare:
				linecap = "square"
			}
			n.Set("strokeLinecap", linecap)
			linejoin := "miter"
			switch s.Linejoin() {
			case usvg.LineJoinRound:
				linejoin = "round"
			case usvg.LineJoinBevel:
				linejoin = "bevel"
			}
			n.Set("strokeLinejoin", linejoin)
		}
	}
	return n
}

func penHex(c usvg.Color, alpha float64) string {
	a := uint8(math.Round(math.Max(0, math.Min(alpha, 1)) * 255))
	if a == 255 {
		return fmt.Sprintf("#%02x%02x%02x", c.Red, c.Green, c.Blue)
	}
	return fmt.Sprintf("#%02x%02x%02x%02x", c.Red, c.Green, c.Blue, a)
}

// penPaint is an SVG paint as a Nalarupa fill (pen.rs `paint`). Gradients become box-relative ones (see svg.rs
// `gradient`): the SVG gradient geometry in frame space, expressed in the node box's 0..1 units. nil for a pattern.
func penPaint(p usvg.Paint, opacity float64, ts skia.Transform, box [4]float64) jsonv.Value {
	var stops []usvg.Stop
	var m skia.Transform
	switch g := p.(type) {
	case usvg.Color:
		return penHex(g, opacity)
	case *usvg.LinearGradient:
		stops, m = g.Stops(), ts.PreConcat(g.Transform())
	case *usvg.RadialGradient:
		stops, m = g.Stops(), ts.PreConcat(g.Transform())
	default:
		return nil
	}
	colors := jsonv.NewArray()
	for _, s := range stops {
		colors.Items = append(colors.Items, engine.Object("color", penHex(s.Color(), float64(s.Opacity())*opacity), "position", jsonv.Float(r2(float64(s.Offset())))))
	}
	bx, by, bw, bh := box[0], box[1], box[2], box[3]
	if bw < 1e-6 || bh < 1e-6 {
		if len(colors.Items) == 0 {
			return nil
		}
		return jsonv.Get(colors.Items[0], "color")
	}
	unit := func(x, y float32) [2]float64 {
		pt := skia.Pt(x, y)
		m.MapPoint(&pt)
		return [2]float64{(float64(pt.X) - bx) / bw, (float64(pt.Y) - by) / bh}
	}
	deg := func(rad float64) float64 { return rad * (180 / math.Pi) }
	switch g := p.(type) {
	case *usvg.LinearGradient:
		a, z := unit(g.X1(), g.Y1()), unit(g.X2(), g.Y2())
		up := [2]float64{(z[0] - a[0]) / 2, (z[1] - a[1]) / 2}
		phi := libm.Atan2(up[0], -up[1])
		return engine.Object("type", "gradient", "gradientType", "linear", "rotation", jsonv.Float(r2(-deg(phi))),
			"center", engine.Object("x", jsonv.Float(r3((a[0]+z[0])/2)), "y", jsonv.Float(r3((a[1]+z[1])/2))),
			"size", engine.Object("width", jsonv.Number("1"), "height", jsonv.Float(r3(2*math.Hypot(up[0], up[1])))), "colors", colors)
	case *usvg.RadialGradient:
		c, ex, ey := unit(g.CX(), g.CY()), unit(g.CX()+g.R(), g.CY()), unit(g.CX(), g.CY()+g.R())
		right, down := [2]float64{ex[0] - c[0], ex[1] - c[1]}, [2]float64{ey[0] - c[0], ey[1] - c[1]}
		phi := libm.Atan2(right[1], right[0])
		return engine.Object("type", "gradient", "gradientType", "radial", "rotation", jsonv.Float(r2(-deg(phi))),
			"center", engine.Object("x", jsonv.Float(r3(c[0])), "y", jsonv.Float(r3(c[1]))),
			"size", engine.Object("width", jsonv.Float(r3(2*math.Hypot(right[0], right[1]))), "height", jsonv.Float(r3(2*math.Hypot(down[0], down[1])))),
			"colors", colors)
	}
	return nil
}

// closeSVG closes a streamed SVG prefix into a well-formed document (pen.rs `close`): complete markup only, then
// the path or polygon whose geometry is still being written (cut after its last finished number, tagged penID),
// then end tags for every element still open. False until the `<svg>` tag is complete.
func closeSVG(src string) (string, bool) {
	var open []string
	cut, i := 0, 0
	growing := ""
	specials := [][2]string{{"<!--", "-->"}, {"<![CDATA[", "]]>"}, {"<?", "?>"}, {"<!", ">"}}
	for {
		k := strings.IndexByte(src[i:], '<')
		if k < 0 {
			break
		}
		lt := i + k
		rest := src[lt:]
		special := -1
		for j, s := range specials {
			if strings.HasPrefix(rest, s[0]) {
				special = j
				break
			}
		}
		var end int
		if special >= 0 {
			c := specials[special][1]
			e := strings.Index(rest, c)
			if e < 0 {
				break
			}
			end = lt + e + len(c)
		} else {
			e, ok := tagEnd(rest)
			if !ok {
				growing = growingShape(rest, open)
				break
			}
			end = lt + e + 1
		}
		if special < 0 {
			tag := src[lt+1 : end-1]
			if name, ok := strings.CutPrefix(tag, "/"); ok {
				name = strings.TrimSpace(name)
				for at := len(open) - 1; at >= 0; at-- {
					if open[at] == name {
						open = open[:at]
						break
					}
				}
			} else if !strings.HasSuffix(tag, "/") {
				open = append(open, tagName(tag))
			}
		}
		cut, i = end, end
	}
	if cut == 0 {
		return "", false
	}
	var doc strings.Builder
	doc.WriteString(src[:cut])
	doc.WriteString(growing)
	for j := len(open) - 1; j >= 0; j-- {
		fmt.Fprintf(&doc, "</%s>", open[j])
	}
	return doc.String(), true
}

// tagEnd is the offset of the `>` ending the tag that starts tag, outside attribute quotes.
func tagEnd(tag string) (int, bool) {
	var quote rune
	for i, c := range tag {
		if i == 0 {
			continue
		}
		switch {
		case quote != 0 && c == quote:
			quote = 0
		case quote != 0:
		case c == '"' || c == '\'':
			quote = c
		case c == '>':
			return i, true
		}
	}
	return 0, false
}

func tagName(tag string) string {
	if i := strings.IndexFunc(tag, func(c rune) bool { return unicode.IsSpace(c) || c == '/' }); i >= 0 {
		return tag[:i]
	}
	return tag
}

// growingShape is the unfinished tag rest as a complete preview element when it is a path/polygon/polyline whose
// geometry attribute is being written (and it would be drawn, so not inside defs); "" otherwise.
func growingShape(rest string, open []string) string {
	for _, n := range open {
		switch n {
		case "defs", "clipPath", "mask", "symbol", "pattern", "marker", "linearGradient", "radialGradient":
			return ""
		}
	}
	name := tagName(rest[1:])
	var attr string
	switch name {
	case "path":
		attr = "d="
	case "polygon", "polyline":
		attr = "points="
	default:
		return ""
	}
	at := -1
	for from := 0; ; {
		j := strings.Index(rest[from:], attr)
		if j < 0 {
			break
		}
		j += from
		if r, _ := utf8.DecodeLastRuneInString(rest[:j]); j > 0 && unicode.IsSpace(r) {
			at = j
		}
		from = j + len(attr)
	}
	if at < 0 {
		return ""
	}
	at += len(attr)
	if at >= len(rest) || (rest[at] != '"' && rest[at] != '\'') {
		return ""
	}
	q := rest[at : at+1]
	value := rest[at+1:]
	if end := strings.Index(value, q); end >= 0 {
		// Geometry complete, later attributes still coming: keep showing the outline.
		value = value[:end]
	} else {
		value = strings.TrimRightFunc(value, func(c rune) bool { return c >= '0' && c <= '9' || strings.ContainsRune(".-+eE", c) })
	}
	attrs := strings.ReplaceAll(rest[1+len(name):at+1], " id=", " data-id=")
	return fmt.Sprintf(`<%s id="%s"%s%s%s/>`, name, penID, attrs, value, q)
}
