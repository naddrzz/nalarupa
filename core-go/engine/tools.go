package engine

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/export"
	"github.com/naddrzz/nalarupa/core-go/guide"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/render"
	"github.com/naddrzz/nalarupa/core-go/resolve"
	"github.com/naddrzz/nalarupa/core-go/script"
	"github.com/naddrzz/nalarupa/core-go/usvg"
)

// RustF64 is how Rust's `{}` writes an f64: the shortest digits that read back, never an exponent.
func RustF64(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// numText is how serde_json displays a number: an integer as written, a float as its shortest round trip.
func numText(n jsonv.Number) string {
	if !n.IsFloat() {
		if i, ok := n.Int64(); ok {
			return strconv.FormatInt(i, 10)
		}
		if u, ok := n.Uint64(); ok {
			return strconv.FormatUint(u, 10)
		}
	}
	f, _ := n.Float64()
	return jsonv.FormatFloat(f)
}

// roundU64 is Rust's `f.round() as u64`: saturating, NaN as 0.
func roundU64(f float64) uint64 {
	r := math.Round(f)
	switch {
	case !(r > 0):
		return 0
	case r >= math.MaxUint64:
		return math.MaxUint64
	}
	return uint64(r)
}

// EditorState is the Markdown summary the agent reads before editing (`get_editor_state`).
func (e *Engine) EditorState() string {
	e.Sync()
	var s strings.Builder
	s.WriteString("# Nalarupa editor state\n\n")
	path := "Untitled (not saved yet)"
	if e.Doc.Path != "" {
		path = e.Doc.Path
	}
	unsaved := ""
	if e.Dirty() {
		unsaved = " (unsaved changes)"
	}
	fmt.Fprintf(&s, "- Document: %s%s\n", path, unsaved)
	var sel []string
	for _, id := range e.Selection[:min(len(e.Selection), 40)] {
		if f := e.Renderer.Find(id); f != nil {
			sel = append(sel, fmt.Sprintf("`%s` \"%s\" %s %sx%s", id, f.Name, f.Type, RustF64(math.Round(f.W)), RustF64(math.Round(f.H))))
		} else {
			sel = append(sel, "`"+id+"`")
		}
	}
	if len(e.Selection) > 40 {
		sel = append(sel, fmt.Sprintf("... +%d others", len(e.Selection)-40))
	}
	selText := "nothing"
	if len(sel) > 0 {
		selText = strings.Join(sel, ", ")
	}
	fmt.Fprintf(&s, "- Selection: %s\n", selText)
	type top struct {
		visible bool
		line    string
	}
	var tops []top
	vp := e.Viewport
	for _, n := range e.Doc.Children() {
		id, ok := jsonv.Str(jsonv.Get(n, "id"))
		if !ok {
			continue
		}
		f := e.Renderer.Find(id)
		if f == nil {
			continue
		}
		visible := vp == nil || f.X < vp[0]+vp[2] && f.Y < vp[1]+vp[3] && f.X+f.W > vp[0] && f.Y+f.H > vp[1]
		comp, where := "", ""
		if jsonv.IsTrue(jsonv.Get(n, "reusable")) {
			comp = " [component]"
		}
		if vp != nil {
			where = " [outside viewport]"
			if visible {
				where = " [user visible]"
			}
		}
		r := func(v float64) string { return RustF64(math.Round(v)) }
		tops = append(tops, top{visible, fmt.Sprintf("  - `%s` \"%s\" %s at (%s, %s) size %sx%s%s%s", id, f.Name, f.Type, r(f.X), r(f.Y), r(f.W), r(f.H), comp, where)})
	}
	slices.SortStableFunc(tops, func(a, b top) int {
		switch {
		case a.visible == b.visible:
			return 0
		case a.visible:
			return -1
		}
		return 1
	})
	more := max(len(tops)-40, 0)
	var lines []string
	for _, t := range tops[:min(len(tops), 40)] {
		lines = append(lines, t.line)
	}
	if more > 0 {
		lines = append(lines, fmt.Sprintf("  - ... +%d others", more))
	}
	fmt.Fprintf(&s, "- Top-level nodes (%d):\n%s\n", len(e.Doc.Children()), strings.Join(lines, "\n"))
	var comps []string
	doc.Walk(e.Doc.Children(), func(n jsonv.Value) {
		if jsonv.IsTrue(jsonv.Get(n, "reusable")) && len(comps) < 80 {
			comps = append(comps, fmt.Sprintf("`%s` \"%s\"", jsonv.StrOr(jsonv.Get(n, "id")), jsonv.StrOr(jsonv.Get(n, "name"))))
		}
	})
	if len(comps) > 0 {
		fmt.Fprintf(&s, "- Components: %s\n", strings.Join(comps, ", "))
	}
	// The existing style, so the agent can post its direction from the prompt alone, before any read-only look:
	// the variables with their values (a themed one shows its first value) and the fonts in use with their sizes.
	vars := jsonv.Obj(jsonv.Get(e.Doc.Root, "variables"))
	themes := ""
	if t := jsonv.Obj(jsonv.Get(e.Doc.Root, "themes")); t != nil {
		themes = strings.Join(t.Keys(), ", ")
	}
	var values []string
	nvars := 0
	if vars != nil {
		nvars = vars.Len()
		vars.Each(func(name string, v jsonv.Value) {
			if len(values) >= 30 {
				return
			}
			value := jsonv.Get(v, "value")
			if a := jsonv.Arr(value); a != nil {
				// A themed variable shows its base (un-themed) entry, wherever it sits, as the resolver applies it.
				var pick jsonv.Value
				found := false
				for _, x := range a.Items {
					if t := jsonv.Obj(jsonv.Get(x, "theme")); t == nil || t.Len() == 0 {
						pick, found = x, true
						break
					}
				}
				if !found && len(a.Items) > 0 {
					pick, found = a.Items[0], true
				}
				value = nil
				if found {
					value = jsonv.Get(pick, "value")
				}
			}
			switch x := value.(type) {
			case string:
				values = append(values, name+" "+x)
			case jsonv.Number:
				values = append(values, name+" "+numText(x))
			case bool:
				values = append(values, name+" "+strconv.FormatBool(x))
			default:
				values = append(values, name)
			}
		})
	}
	if nvars > 30 {
		values = append(values, fmt.Sprintf("... +%d others", nvars-30))
	}
	axes, list := "", ""
	if themes != "" {
		axes = " (theme axes: " + themes + ")"
	}
	if len(values) > 0 {
		list = ": " + strings.Join(values, ", ")
	}
	fmt.Fprintf(&s, "- Variables: %d%s%s\n", nvars, axes, list)
	type family struct {
		name  string
		sizes []uint64
	}
	var fonts []family
	moreFonts := 0
	doc.Walk(e.Doc.Children(), func(n jsonv.Value) {
		name, ok := jsonv.Str(jsonv.Get(n, "fontFamily"))
		if !ok || name == "" {
			return
		}
		f, hasSize := jsonv.F64(jsonv.Get(n, "fontSize"))
		size := roundU64(f)
		if i := slices.IndexFunc(fonts, func(x family) bool { return x.name == name }); i >= 0 {
			if hasSize && !slices.Contains(fonts[i].sizes, size) {
				fonts[i].sizes = append(fonts[i].sizes, size)
			}
		} else if len(fonts) < 12 {
			fam := family{name: name}
			if hasSize {
				fam.sizes = []uint64{size}
			}
			fonts = append(fonts, fam)
		} else {
			moreFonts++
		}
	})
	if len(fonts) > 0 {
		var list []string
		for _, f := range fonts {
			slices.SortFunc(f.sizes, func(a, b uint64) int {
				switch {
				case a > b:
					return -1
				case a < b:
					return 1
				}
				return 0
			})
			f.sizes = f.sizes[:min(len(f.sizes), 8)]
			if len(f.sizes) == 0 {
				list = append(list, f.name)
				continue
			}
			sizes := make([]string, len(f.sizes))
			for i, sz := range f.sizes {
				sizes[i] = strconv.FormatUint(sz, 10)
			}
			list = append(list, f.name+" ("+strings.Join(sizes, ", ")+")")
		}
		if moreFonts > 0 {
			list = append(list, fmt.Sprintf("... +%d other families", moreFonts))
		}
		fmt.Fprintf(&s, "- Fonts in use: %s\n", strings.Join(list, "; "))
	}
	if b, ok := e.Renderer.Bounds(); ok {
		r := func(v float64) string { return RustF64(math.Round(v)) }
		fmt.Fprintf(&s, "- Canvas content bounds: x %s y %s w %s h %s\n", r(b.X), r(b.Y), r(b.W), r(b.H))
	}
	s.WriteString("\nCall `read_guide` (no arguments) before your first `execute`; it explains the .pen schema and the execute API.\n")
	return s.String()
}

// CapturedCanvasContext is the resolved result of pinned canvas node/viewport attachments for one agent turn:
// Text is the `[Attached canvas context]` Markdown block (empty when nothing resolved), Contexts are the
// validated/enriched metadata refs to show in the chat bubble and save in history, Missing are node IDs that
// were deleted before send, and Snapshots are optional auto-rendered PNG images (when includePNG is true).
type CapturedCanvasContext struct {
	Text      string
	Contexts  []CanvasContextRef
	Missing   []string
	Snapshots []AttachmentImage
}

const (
	maxContextNodeJSONBytes  = 4000
	maxContextTotalTextBytes = 14000
	maxContextNodeDepth      = 3
	maxContextNodeChildren   = 12
	maxContextViewportSide   = 1280
)

// CaptureCanvasContexts resolves pinned canvas node and viewport context references without holding Engine.Lock
// across JSON serialization, SVG rasterization, or PNG encoding. Call without holding Engine.Lock.
func (e *Engine) CaptureCanvasContexts(refs []CanvasContextRef, includePNG bool) CapturedCanvasContext {
	var res CapturedCanvasContext
	if len(refs) == 0 {
		return res
	}
	seen := map[string]bool{}
	type ctxSection struct {
		header string
		json   string
	}
	var built []ctxSection
	r := func(v float64) string { return RustF64(math.Round(v)) }

	for _, ref := range refs {
		kind := strings.ToLower(strings.TrimSpace(ref.Kind))
		if kind == "" && ref.ID != "" {
			kind = "node"
		}
		switch kind {
		case "viewport":
			if seen["viewport"] {
				continue
			}
			seen["viewport"] = true
			resolved, header, snapImg := e.captureViewportContext(ref, includePNG, len(res.Snapshots)+1)
			res.Contexts = append(res.Contexts, resolved)
			if snapImg != nil {
				res.Snapshots = append(res.Snapshots, *snapImg)
			}
			built = append(built, ctxSection{header: header})
		case "node":
			id := strings.TrimSpace(ref.ID)
			if id == "" || seen["node:"+id] {
				continue
			}
			seen["node:"+id] = true
			resolved, header, rawJSON, snapImg, found := e.captureNodeContext(ref, id, includePNG, len(res.Snapshots)+1, r)
			if !found {
				res.Missing = append(res.Missing, id)
				continue
			}
			res.Contexts = append(res.Contexts, resolved)
			if snapImg != nil {
				res.Snapshots = append(res.Snapshots, *snapImg)
			}
			built = append(built, ctxSection{header: header, json: rawJSON})
		}
	}

	if len(built) > 0 {
		// Always keep every resolved context's metadata header and Visual snapshot index line so
		// `image #N` stays 1:1 aligned with res.Snapshots; only trim/omit JSON payloads under budget.
		headerBytes := 0
		for _, b := range built {
			headerBytes += len(b.header) + 2
		}
		jsonBudget := max(0, maxContextTotalTextBytes-headerBytes)
		sections := make([]string, 0, len(built))
		for _, b := range built {
			sec := b.header
			if b.json != "" {
				const fenceOverhead = len("\n```json\n\n```")
				switch {
				case len(b.json)+fenceOverhead <= jsonBudget:
					sec += "\n```json\n" + b.json + "\n```"
					jsonBudget -= len(b.json) + fenceOverhead
				case jsonBudget >= 256+fenceOverhead:
					avail := jsonBudget - fenceOverhead
					sec += "\n```json\n" + truncateUTF8(b.json, avail) + "\n```"
					jsonBudget = 0
				default:
					sec += "\n- Node JSON omitted to stay within context budget."
				}
			}
			sections = append(sections, sec)
		}

		var sb strings.Builder
		sb.WriteString("[Attached canvas context]\n")
		sb.WriteString("The user explicitly pinned the following canvas target(s) for this turn. Treat them as primary targets even if the live canvas selection changes later.\n\n")
		sb.WriteString(strings.Join(sections, "\n\n"))
		sb.WriteString("\n[/Attached canvas context]")
		res.Text = sb.String()
	}
	return res
}

func (e *Engine) captureViewportContext(ref CanvasContextRef, includePNG bool, nextImageIdx int) (CanvasContextRef, string, *AttachmentImage) {
	var vp [4]float64
	var visibleLines []string
	var totalVisible int
	var snap render.SceneSnapshot
	r := func(v float64) string { return RustF64(math.Round(v)) }

	e.Locked(func() {
		e.ExpireSlides()
		e.Sync()
		switch {
		case ref.Viewport != nil && ref.Viewport[2] > 0 && ref.Viewport[3] > 0:
			vp = *ref.Viewport
		case e.Viewport != nil && e.Viewport[2] > 0 && e.Viewport[3] > 0:
			vp = *e.Viewport
		default:
			if b, ok := e.Renderer.Bounds(); ok && b.W > 0 && b.H > 0 {
				vp = [4]float64{b.X, b.Y, b.W, b.H}
			} else {
				vp = [4]float64{0, 0, 1280, 800}
			}
		}
		for _, n := range e.Doc.Children() {
			id, ok := jsonv.Str(jsonv.Get(n, "id"))
			if !ok {
				continue
			}
			f := e.Renderer.Find(id)
			if f == nil {
				continue
			}
			if f.X < vp[0]+vp[2] && f.Y < vp[1]+vp[3] && f.X+f.W > vp[0] && f.Y+f.H > vp[1] {
				totalVisible++
				if len(visibleLines) < 30 {
					comp := ""
					if jsonv.IsTrue(jsonv.Get(n, "reusable")) {
						comp = " [component]"
					}
					visibleLines = append(visibleLines, fmt.Sprintf("  - `%s` \"%s\" %s at (%s, %s) size %sx%s%s",
						id, f.Name, f.Type, r(f.X), r(f.Y), r(f.W), r(f.H), comp))
				}
			}
		}
		if includePNG {
			snap = e.Renderer.Snapshot(e.Version, e.now(), "", len(e.Streams) > 0)
		}
	})

	vpCopy := vp
	name := strings.TrimSpace(ref.Name)
	if name == "" {
		name = "Canvas viewport"
	}
	resolved := CanvasContextRef{
		Kind:     "viewport",
		Name:     name,
		NodeType: "viewport",
		W:        math.Round(vp[2]),
		H:        math.Round(vp[3]),
		Viewport: &vpCopy,
	}

	var snapImg *AttachmentImage
	if includePNG {
		longSide := max(vp[2], vp[3])
		scale := 1.0
		if longSide > maxContextViewportSide {
			scale = float64(maxContextViewportSide) / longSide
		} else if longSide > 0 && longSide < 320 {
			scale = min(2.0, 640.0/longSide)
		}
		pw := max(1, min(maxContextViewportSide, int(math.Round(vp[2]*scale))))
		ph := max(1, min(maxContextViewportSide, int(math.Round(vp[3]*scale))))
		zoom := min(float64(pw)/max(vp[2], 1), float64(ph)/max(vp[3], 1))
		det := render.NewDetachedRenderer()
		px := det.Render(snap, uint32(pw), uint32(ph), vp[0], vp[1], zoom, [3]uint8{0xff, 0xff, 0xff}, "", true)
		if len(px) >= pw*ph*4 {
			img := &image.RGBA{Pix: px, Stride: pw * 4, Rect: image.Rect(0, 0, pw, ph)}
			var buf bytes.Buffer
			if err := png.Encode(&buf, img); err == nil {
				snapImg = &AttachmentImage{
					Name:      "Canvas viewport.png",
					MediaType: "image/png",
					Data:      base64.StdEncoding.EncodeToString(buf.Bytes()),
					Width:     pw,
					Height:    ph,
				}
			}
		}
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "### Canvas viewport (x %s, y %s, %s×%s)\n", r(vp[0]), r(vp[1]), r(vp[2]), r(vp[3]))
	if totalVisible == 0 {
		sb.WriteString("- Visible top-level nodes: none\n")
	} else {
		fmt.Fprintf(&sb, "- Visible top-level nodes (%d):\n%s\n", totalVisible, strings.Join(visibleLines, "\n"))
		if totalVisible > len(visibleLines) {
			fmt.Fprintf(&sb, "  - ... +%d others\n", totalVisible-len(visibleLines))
		}
	}
	if snapImg != nil {
		fmt.Fprintf(&sb, "- Visual snapshot: attached as image #%d (%dx%d PNG)\n", nextImageIdx, snapImg.Width, snapImg.Height)
	}
	return resolved, strings.TrimRight(sb.String(), "\n"), snapImg
}

func rnodeToContextJSON(rn *resolve.RNode, depth int) *jsonv.Object {
	out := jsonv.NewObject()
	out.Set("id", rn.ID)
	if rn.Type != "" {
		out.Set("type", rn.Type)
	}
	if rn.P != nil {
		rn.P.Each(func(k string, v jsonv.Value) {
			if k == "id" || k == "type" || k == "children" {
				return
			}
			out.Set(k, jsonv.Clone(v))
		})
	}
	if len(rn.Kids) > 0 {
		if depth >= maxContextNodeDepth {
			out.Set("_childrenCount", jsonv.Int(int64(len(rn.Kids))))
		} else {
			kids := jsonv.NewArray()
			limit := min(len(rn.Kids), maxContextNodeChildren)
			for i := 0; i < limit; i++ {
				kids.Items = append(kids.Items, rnodeToContextJSON(rn.Kids[i], depth+1))
			}
			if len(rn.Kids) > limit {
				kids.Items = append(kids.Items, Object("_omittedChildren", jsonv.Int(int64(len(rn.Kids)-limit))))
			}
			out.Set("children", kids)
		}
	}
	return out
}

func (e *Engine) captureNodeContext(ref CanvasContextRef, id string, includePNG bool, nextImageIdx int, r func(float64) string) (CanvasContextRef, string, string, *AttachmentImage, bool) {
	var (
		found        bool
		rawClone     jsonv.Value
		parentID     string
		nodeName     string
		nodeType     string
		bx, by, w, h float64
		hasBox       bool
		scale        = 1.0
	)
	e.Locked(func() {
		e.Sync()
		raw := e.Doc.Find(id)
		f := e.Renderer.Find(id)
		var res *resolve.Resolver
		getRes := func() *resolve.Resolver {
			if res == nil {
				res = resolve.New(e.Doc.Root, e.Libs)
			}
			return res
		}
		if raw == nil && f == nil {
			if !getRes().HasPath(id) {
				return
			}
		}
		found = true
		if raw != nil {
			rawClone = jsonv.Clone(raw)
			nodeName = jsonv.StrOr(jsonv.Get(raw, "name"))
			nodeType = jsonv.StrOr(jsonv.Get(raw, "type"))
			if nodeType == "ref" {
				if inh := getRes().Inherited(raw); inh != nil {
					if obj := jsonv.Obj(rawClone); obj != nil {
						obj.Set("_inherited", jsonv.Clone(inh))
					}
				}
			}
		} else if strings.Contains(id, "/") {
			segs := strings.Split(id, "/")
			if rootRaw := getRes().Node(segs[0]); rootRaw != nil {
				if rn := getRes().Resolve(rootRaw); rn != nil {
					if desc := rn.Find(id); desc != nil {
						rawClone = rnodeToContextJSON(desc, 0)
						nodeName, _ = desc.S("name")
						nodeType = desc.Type
					}
				}
			}
		}
		if p, _, ok := e.Doc.ParentOf(id); ok {
			parentID = p
		} else if slash := strings.LastIndexByte(id, '/'); slash > 0 {
			parentID = id[:slash]
		}
		if f != nil {
			hasBox = true
			bx, by, w, h = f.X, f.Y, f.W, f.H
			if nodeName == "" {
				nodeName = f.Name
			}
			if nodeType == "" {
				nodeType = f.Type
			}
			scale = fclamp(fmin(1400/fmax(fmax(f.W, f.H), 1), 2), 0.05, 8)
		}
	})
	if !found {
		return CanvasContextRef{}, "", "", nil, false
	}

	if nodeName == "" {
		nodeName = strings.TrimSpace(ref.Name)
	}
	if nodeType == "" {
		nodeType = strings.TrimSpace(ref.NodeType)
	}
	if nodeName == "" {
		nodeName = id
	}
	if !hasBox {
		w, h = ref.W, ref.H
	}

	resolved := CanvasContextRef{
		Kind:     "node",
		ID:       id,
		Name:     nodeName,
		NodeType: nodeType,
		W:        math.Round(w),
		H:        math.Round(h),
	}

	var snapImg *AttachmentImage
	if includePNG {
		type built struct {
			tree *usvg.Tree
			err  error
		}
		b := script.OffLock(func(rec *script.Runs) (out built) {
			e.Locked(func() {
				cx := &export.Ctx{Doc: e.Doc, Libs: e.Libs, Fonts: e.Fonts, Assets: e.Assets, Runs: rec}
				out.tree, out.err = cx.NodeTreeAt(id, scale)
			})
			return out
		})
		if b.err == nil && b.tree != nil {
			if pngBytes, err := export.EncodeTree(b.tree, "png", scale, 0); err == nil && len(pngBytes) > 0 {
				sz := b.tree.Size()
				pw := max(1, int(math.Round(float64(sz.Width())*scale)))
				ph := max(1, int(math.Round(float64(sz.Height())*scale)))
				snapImg = &AttachmentImage{
					Name:      nodeName + ".png",
					MediaType: "image/png",
					Data:      base64.StdEncoding.EncodeToString(pngBytes),
					Width:     pw,
					Height:    ph,
				}
			}
		}
	}

	var sb strings.Builder
	header := fmt.Sprintf("### Node `%s` \"%s\"", id, nodeName)
	if nodeType != "" {
		header += " (" + nodeType
		if hasBox {
			header += fmt.Sprintf(", at (%s, %s), size %s×%s", r(bx), r(by), r(w), r(h))
		}
		if parentID != "" && parentID != doc.Root {
			header += fmt.Sprintf(", parent `%s`", parentID)
		}
		header += ")"
	}
	sb.WriteString(header + "\n")
	if snapImg != nil {
		fmt.Fprintf(&sb, "- Visual snapshot: attached as image #%d (%dx%d PNG)\n", nextImageIdx, snapImg.Width, snapImg.Height)
	}
	rawJSON := ""
	if rawClone != nil {
		compact := compactNodeForContext(rawClone, 0)
		rawJSON = truncateUTF8(string(jsonv.Marshal(compact)), maxContextNodeJSONBytes)
	}
	return resolved, strings.TrimRight(sb.String(), "\n"), rawJSON, snapImg, true
}

func compactNodeForContext(v jsonv.Value, depth int) jsonv.Value {
	switch x := v.(type) {
	case *jsonv.Object:
		out := jsonv.NewObject()
		x.Each(func(k string, val jsonv.Value) {
			if k == "children" {
				arr := jsonv.Arr(val)
				if arr == nil {
					return
				}
				if depth >= maxContextNodeDepth {
					out.Set("_childrenCount", jsonv.Int(int64(len(arr.Items))))
					return
				}
				kids := jsonv.NewArray()
				limit := min(len(arr.Items), maxContextNodeChildren)
				for i := 0; i < limit; i++ {
					kids.Items = append(kids.Items, compactNodeForContext(arr.Items[i], depth+1))
				}
				if len(arr.Items) > limit {
					kids.Items = append(kids.Items, Object("_omittedChildren", jsonv.Int(int64(len(arr.Items)-limit))))
				}
				out.Set(k, kids)
				return
			}
			if s, ok := val.(string); ok {
				if (k == "url" || k == "src") && strings.HasPrefix(s, "data:") && len(s) > 120 {
					out.Set(k, "[data-uri omitted]")
					return
				}
				if k == "geometry" && len(s) > 240 {
					out.Set(k, truncateUTF8(s, 240))
					return
				}
				if len(s) > 600 {
					out.Set(k, truncateUTF8(s, 600))
					return
				}
			}
			out.Set(k, compactNodeForContext(val, depth+1))
		})
		return out
	case *jsonv.Array:
		out := jsonv.NewArray()
		limit := min(len(x.Items), maxContextNodeChildren)
		for i := 0; i < limit; i++ {
			out.Items = append(out.Items, compactNodeForContext(x.Items[i], depth+1))
		}
		if len(x.Items) > limit {
			out.Items = append(out.Items, Object("_omittedItems", jsonv.Int(int64(len(x.Items)-limit))))
		}
		return out
	default:
		return jsonv.Clone(v)
	}
}

func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// Tool dispatches one agent tool call under the engine lock (shared by MCP and the built-in agent); `execute`
// holds it across the JavaScript (CallTool does not). Call Finish on the answer once the lock is released.
func (e *Engine) Tool(name string, args jsonv.Value) ToolOut {
	switch name {
	case "execute":
		if e.Executing {
			return bad("Another execute call is still running.")
		}
		c := e.beginExecute(args)
		out := e.executeWithSkipEdits(c, script.Call{})
		e.endExecute(c)
		return out
	case "get_editor_state":
		return ok(e.EditorState())
	case "read_guide":
		text, failed := guide.Read(jsonv.StrOr(jsonv.Get(args, "topic")))
		return ToolOut{Text: text, Error: failed}
	case "get_style":
		text, failed := guide.Directions(jsonv.StrOr(jsonv.Get(args, "name")))
		return ToolOut{Text: text, Error: failed}
	case "open_document":
		if e.SwitchBusyNow() {
			return bad(SwitchBusy)
		}
		if e.Dirty() {
			return bad(SwitchDirty)
		}
		path, has := jsonv.Str(jsonv.Get(args, "path"))
		if !has {
			return bad("path is required")
		}
		// A missing file is created on disk first, so a failed create leaves the open document alone.
		var err error
		if !exists(path) {
			err = doc.New().Save(path)
		}
		if err == nil {
			err = e.Open(path)
		}
		if err != nil {
			return bad(err.Error())
		}
		ev := jsonv.NewObject()
		ev.Set("type", "document_opened")
		e.Emit(ev)
		return ok("Opened " + path)
	}
	return bad("unknown tool: " + name)
}

// ToolSpecs is the tool schemas advertised over MCP and to the built-in agent.
func ToolSpecs() jsonv.Value { return jsonv.MustParse(toolSpecs) }

const toolSpecs = `[
{"name": "get_editor_state", "description": "Summary of the open Nalarupa document: path, selection, top-level frames, components, variables. Call this first.",
 "inputSchema": {"type": "object", "properties": {}}},
{"name": "read_guide", "description": "Read Nalarupa's design guide. No topic returns the overview and the list of topics (designing, schema, execute, components, slides, design, code).",
 "inputSchema": {"type": "object", "properties": {"topic": {"type": "string", "description": "Guide topic, e.g. 'schema', 'execute', 'slides'"}}}},
{"name": "get_style", "description": "Worked examples of design directions (framing, concept, palette and type with roles, layout, signature move), each grown from a sample brief. No name lists them.",
 "inputSchema": {"type": "object", "properties": {"name": {"type": "string"}}}},
{"name": "execute", "description": "Run a JavaScript snippet against the canvas (Insert/Update/Copy/Replace/Move/Delete/Get/SetVariables/TakeScreenshot/Export...). All operations in one call are a single undoable transaction; on error everything is rolled back. Read the 'execute' guide first. To fix a failed call, send ` + "`editId` with `edits`" + ` instead of resending the snippet.",
 "inputSchema": {"type": "object", "properties": {
    "input": {"type": "string", "description": "JavaScript source"},
    "editId": {"type": "string", "description": "editId from a failed call, used with edits"},
    "edits": {"type": "array", "items": {"type": "object", "properties": {"find": {"type": "string"}, "replace": {"type": "string"}, "all": {"type": "boolean"}}, "required": ["find", "replace"]}}
 }}},
{"name": "open_document", "description": "Open a .pen/.nala file in the editor, or create it when it does not exist.",
 "inputSchema": {"type": "object", "properties": {"path": {"type": "string"}}, "required": ["path"]}}
]`
