package agent

import (
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/engine"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/render"
	"github.com/naddrzz/nalarupa/core-go/script"
	"github.com/naddrzz/nalarupa/core-go/svg"
)

// AppendDesignAudit runs deterministic post-execute layout overflow and multi-screen surface/token
// consistency checks and appends any findings under "## Issues detected".
func AppendDesignAudit(e *engine.Engine, toolName, reportText string, isError bool) string {
	return AppendDesignAuditWithSnapshot(e, nil, toolName, reportText, isError)
}

// AppendDesignAuditWithSnapshot audits snap completely off Engine.Lock (capturing a fresh lightweight snapshot
// if snap is nil) so canvas renders/polls never block and concurrent MCP execute calls audit their own commit.
func AppendDesignAuditWithSnapshot(e *engine.Engine, snap *engine.AuditSnapshot, toolName, reportText string, isError bool) string {
	if isError || toolName != "execute" {
		return reportText
	}
	if snap == nil && e != nil {
		e.Locked(func() {
			snap = e.CaptureAuditSnapshot()
		})
	}
	if snap == nil || snap.Root == nil {
		return reportText
	}
	findings := collectDesignAuditSnapshot(snap, reportText)
	if len(findings) == 0 {
		return reportText
	}
	return appendIssueBullets(reportText, findings)
}

func appendIssueBullets(text string, warnings []string) string {
	const head = "## Issues detected (fix them in the next call)\n"
	var bullets strings.Builder
	for _, w := range warnings {
		if w == "" || strings.Contains(text, w) {
			continue
		}
		bullets.WriteString(script.IssueLine(w))
	}
	if bullets.Len() == 0 {
		return text
	}
	at := strings.Index(text, head)
	if at < 0 {
		sep := "\n"
		if strings.HasSuffix(text, "\n\n") {
			sep = ""
		}
		return text + sep + head + bullets.String()
	}
	end := at + len(head)
	for strings.HasPrefix(text[end:], "- ") {
		if i := strings.IndexByte(text[end:], '\n'); i >= 0 {
			end += i + 1
		} else {
			end = len(text)
		}
	}
	return text[:end] + bullets.String() + text[end:]
}

type screenSummary struct {
	id             string
	name           string
	raw            jsonv.Value
	w, h           float64
	rawFill        string
	resolvedFill   string
	usedRefs       map[string]string // componentID -> componentName
	surfacePattern string            // name of full-bleed / textured background child if any
}

func extractPrimaryColorFill(v jsonv.Value) string {
	if v == nil {
		return ""
	}
	if s, ok := jsonv.Str(v); ok {
		return strings.TrimSpace(s)
	}
	if arr := jsonv.Arr(v); arr != nil {
		for i := len(arr.Items) - 1; i >= 0; i-- {
			if c := extractPrimaryColorFill(arr.Items[i]); c != "" {
				return c
			}
		}
		return ""
	}
	if obj := jsonv.Obj(v); obj != nil {
		if jsonv.IsFalse(jsonv.Get(obj, "enabled")) {
			return ""
		}
		ty := strings.ToLower(strings.TrimSpace(jsonv.StrOr(jsonv.Get(obj, "type"))))
		if ty == "" || ty == "color" || ty == "solid" {
			if s, ok := jsonv.Str(jsonv.Get(obj, "color")); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
			if s, ok := jsonv.Str(jsonv.Get(obj, "value")); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

func collectDesignAuditSnapshot(snap *engine.AuditSnapshot, existingReport string) []string {
	if snap == nil || snap.Root == nil {
		return nil
	}
	root := snap.Root
	topKids := jsonv.Items(jsonv.Get(root, "children"))

	// Collect defined color variables: hex (lowercased) -> $varName
	colorVarByHex := map[string]string{}
	if vars := jsonv.Obj(jsonv.Get(root, "variables")); vars != nil {
		vars.Each(func(name string, v jsonv.Value) {
			if strings.HasPrefix(name, "preserved_") || !jsonv.StrIs(jsonv.Get(v, "type"), "color") {
				return
			}
			val := jsonv.Get(v, "value")
			if arr := jsonv.Arr(val); arr != nil && len(arr.Items) > 0 {
				val = jsonv.Get(arr.Items[0], "value")
			}
			if hex, ok := jsonv.Str(val); ok && strings.HasPrefix(hex, "#") {
				colorVarByHex[strings.ToLower(strings.TrimSpace(hex))] = "$" + name
			}
		})
	}

	// Map reusable top-level / nested components: id -> name
	compNames := map[string]string{}
	doc.Walk(topKids, func(n jsonv.Value) {
		if jsonv.IsTrue(jsonv.Get(n, "reusable")) {
			cid := jsonv.StrOr(jsonv.Get(n, "id"))
			cname := jsonv.StrOr(jsonv.Get(n, "name"))
			if cid != "" {
				if cname == "" {
					cname = cid
				}
				compNames[cid] = cname
			}
		}
	})

	var warnings []string
	var screens []screenSummary

	for _, topRaw := range topKids {
		if !jsonv.StrIs(jsonv.Get(topRaw, "type"), "frame") || jsonv.IsTrue(jsonv.Get(topRaw, "reusable")) {
			continue
		}
		topID := jsonv.StrOr(jsonv.Get(topRaw, "id"))
		flats := snap.FlatsByTopID[topID]
		if len(flats) == 0 {
			continue
		}
		rootFlat := flats[0]
		if rootFlat.W < 280 || rootFlat.H < 200 {
			continue
		}

		// 1. Deep Clipped-Screen Overflow Gate
		if rootFlat.Clip {
			warnings = append(warnings, checkScreenOverflows(topRaw, flats, existingReport)...)
		}

		// Gather screen metadata for multi-screen consistency checks
		rawFill := extractPrimaryColorFill(jsonv.Get(topRaw, "fill"))
		resFill := resolveColorToken(rawFill, colorVarByHex, root)
		usedRefs := map[string]string{}
		doc.Walk([]jsonv.Value{topRaw}, func(n jsonv.Value) {
			if jsonv.StrIs(jsonv.Get(n, "type"), "ref") {
				refID := jsonv.StrOr(jsonv.Get(n, "ref"))
				if cname, exists := compNames[refID]; exists {
					usedRefs[refID] = cname
				}
			}
		})

		surfacePat := detectSurfaceMotif(topRaw, rootFlat)
		screens = append(screens, screenSummary{
			id:             topID,
			name:           rootFlat.Name,
			raw:            topRaw,
			w:              rootFlat.W,
			h:              rootFlat.H,
			rawFill:        rawFill,
			resolvedFill:   resFill,
			usedRefs:       usedRefs,
			surfacePattern: surfacePat,
		})
	}

	// 2a. Semantic color binding check on EVERY screen (including screens[0]) when color variables exist
	if !snap.StyleVariationMode && len(colorVarByHex) > 0 {
		for _, sc := range screens {
			if strings.HasPrefix(sc.rawFill, "#") {
				if tok, ok := colorVarByHex[strings.ToLower(sc.rawFill)]; ok {
					warnings = append(warnings, fmt.Sprintf(
						"screen %q (%s) hardcodes fill %s instead of document variable %s; bind shared colors to semantic variables so all screens stay synchronized",
						sc.name, sc.id, sc.rawFill, tok))
				}
			}
		}
	}

	// 2b & 2c. Multi-Screen Consistency Gate (when >= 2 screens exist in the same flow)
	if !snap.StyleVariationMode && len(screens) >= 2 {
		base := screens[0]
		for _, sc := range screens[1:] {
			// 2b. Shared chrome components (e.g., TopNav / Footer) used on Screen 1 but omitted on Screen 2+
			if math.Abs(sc.w-base.w) < 2 {
				var missingChrome []string
				for cid, cname := range base.usedRefs {
					if _, has := sc.usedRefs[cid]; !has && looksLikeSharedChrome(cname) {
						missingChrome = append(missingChrome, fmt.Sprintf("%q (%s)", cname, cid))
					}
				}
				slices.Sort(missingChrome)
				if len(missingChrome) > 0 {
					warnings = append(warnings, fmt.Sprintf(
						"screen %q (%s) omits reusable page chrome %s used on %q; instantiate shared header/footer components across sibling screens",
						sc.name, sc.id, strings.Join(missingChrome, ", "), base.name))
				}
			}
			// 2c. Surface treatment / background motif drift across screens sharing the same ground color
			if base.surfacePattern != "" && sc.surfacePattern == "" &&
				base.resolvedFill != "" && strings.EqualFold(base.resolvedFill, sc.resolvedFill) {
				warnings = append(warnings, fmt.Sprintf(
					"screen %q (%s) has a flat %s background while sibling %q layers surface treatment %q; keep consistent background texture/surface motifs across screens",
					sc.name, sc.id, sc.resolvedFill, base.name, base.surfacePattern))
			}
		}
	}

	return warnings
}

func checkScreenOverflows(topRaw jsonv.Value, flats []render.Flat, existingReport string) []string {
	if len(flats) <= 1 {
		return nil
	}
	root := flats[0]
	rx0, ry0 := root.X, root.Y
	rx1, ry1 := root.X+root.W, root.Y+root.H

	rawByID := map[string]jsonv.Value{}
	doc.Walk([]jsonv.Value{topRaw}, func(n jsonv.Value) {
		if id, ok := jsonv.Str(jsonv.Get(n, "id")); ok && id != "" {
			rawByID[id] = n
		}
	})

	var out []string
	const maxPerScreen = 3
	reportedSubtreeDepth := -1

	for i := 1; i < len(flats); i++ {
		f := flats[i]
		if reportedSubtreeDepth >= 0 {
			if f.Depth > reportedSubtreeDepth {
				continue
			}
			reportedSubtreeDepth = -1
		}
		if f.Faded || f.W < 1 || f.H < 1 || f.Type == "group" {
			continue
		}
		raw := rawByID[f.ID]
		if isIntentionalBleedDecor(raw, f, root) {
			continue
		}

		overRight := (f.X + f.W) - rx1
		overLeft := rx0 - f.X
		overTop := ry0 - f.Y
		overBottom := (f.Y + f.H) - ry1

		if overRight <= 1.0 && overLeft <= 1.0 && overTop <= 1.0 && overBottom <= 1.0 {
			continue
		}
		alreadyNamed := strings.Contains(existingReport, `"`+f.Name+`" (`+f.ID+`) sticks out`)

		if f.Type == "frame" && i+1 < len(flats) && flats[i+1].Depth > f.Depth {
			childAlsoOverflows := false
			for j := i + 1; j < len(flats) && flats[j].Depth > f.Depth; j++ {
				cf := flats[j]
				if (cf.X+cf.W)-rx1 > 1.0 || rx0-cf.X > 1.0 || ry0-cf.Y > 1.0 || (cf.Y+cf.H)-ry1 > 1.0 {
					childAlsoOverflows = true
					break
				}
			}
			if childAlsoOverflows {
				continue
			}
		}

		if overRight > 1.0 {
			if !alreadyNamed || !strings.Contains(existingReport, "fill_container") {
				out = append(out, fmt.Sprintf(
					"%q (%s) overflows right edge of screen %q (%s) by %.0fpx (reaches %.0fpx > %.0fpx); set row and multi-column card widths to \"fill_container\" instead of fixed pixel widths",
					f.Name, f.ID, root.Name, root.ID, math.Round(overRight), math.Round(f.X+f.W-rx0), math.Round(root.W)))
				reportedSubtreeDepth = f.Depth
			}
		} else if overLeft > 1.0 && !alreadyNamed {
			out = append(out, fmt.Sprintf(
				"%q (%s) overflows left edge of screen %q (%s) by %.0fpx; check x offset, alignment, or horizontal padding",
				f.Name, f.ID, root.Name, root.ID, math.Round(overLeft)))
			reportedSubtreeDepth = f.Depth
		} else if overTop > 1.0 && !alreadyNamed {
			out = append(out, fmt.Sprintf(
				"%q (%s) overflows top edge of screen %q (%s) by %.0fpx; check y offset or vertical padding",
				f.Name, f.ID, root.Name, root.ID, math.Round(overTop)))
			reportedSubtreeDepth = f.Depth
		} else if overBottom > 2.0 && !alreadyNamed {
			out = append(out, fmt.Sprintf(
				"%q (%s) overflows bottom of screen %q (%s) by %.0fpx (height %.0fpx is too short for its content); grow the screen height or set height: \"fit_content\"",
				f.Name, f.ID, root.Name, root.ID, math.Round(overBottom), math.Round(root.H)))
			reportedSubtreeDepth = f.Depth
		}

		if len(out) >= maxPerScreen {
			break
		}
	}
	return out
}

func isIntentionalBleedDecor(raw jsonv.Value, f render.Flat, root render.Flat) bool {
	if raw == nil {
		return false
	}
	if !jsonv.StrIs(jsonv.Get(raw, "layoutPosition"), "absolute") {
		return false
	}
	if f.Type == "ellipse" || f.Type == "path" || f.Type == "line" || f.Type == "polygon" {
		return true
	}
	if f.Type == "rectangle" {
		if op, ok := jsonv.F64(jsonv.Get(raw, "opacity")); ok && op < 0.5 {
			return true
		}
		if eff := jsonv.Get(raw, "effect"); eff != nil {
			return true
		}
	}
	_ = root
	return false
}

func resolveColorToken(raw string, colorVarByHex map[string]string, docRoot *jsonv.Object) string {
	_ = colorVarByHex
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "#") {
		return strings.ToLower(raw)
	}
	if name, ok := strings.CutPrefix(raw, "$"); ok && docRoot != nil {
		if vars := jsonv.Obj(jsonv.Get(docRoot, "variables")); vars != nil {
			for range 16 {
				v, exists := vars.Get(name)
				if !exists {
					break
				}
				val := jsonv.Get(v, "value")
				if arr := jsonv.Arr(val); arr != nil && len(arr.Items) > 0 {
					val = jsonv.Get(arr.Items[0], "value")
				}
				str, ok := jsonv.Str(val)
				if !ok {
					break
				}
				str = strings.TrimSpace(str)
				if nextName, isRef := strings.CutPrefix(str, "$"); isRef {
					name = nextName
					continue
				}
				return strings.ToLower(str)
			}
		}
	}
	return strings.ToLower(raw)
}

func detectSurfaceMotif(topRaw jsonv.Value, rootFlat render.Flat) string {
	if fillV := jsonv.Get(topRaw, "fill"); fillV != nil {
		for _, f := range svg.Fills(fillV) {
			if _, isFlat := svg.Flat(f); !isFlat {
				return "textured/gradient ground fill"
			}
		}
	}
	kids := jsonv.Items(jsonv.Get(topRaw, "children"))
	for _, k := range kids {
		ty := jsonv.StrOr(jsonv.Get(k, "type"))
		if ty == "text" || ty == "ref" {
			continue
		}
		name := jsonv.StrOr(jsonv.Get(k, "name"))
		lname := strings.ToLower(name)
		isAbs := jsonv.StrIs(jsonv.Get(k, "layoutPosition"), "absolute")
		w, _ := jsonv.F64(jsonv.Get(k, "width"))
		h, _ := jsonv.F64(jsonv.Get(k, "height"))
		coversHalf := w >= rootFlat.W*0.65 && h >= rootFlat.H*0.45
		if isAbs && coversHalf {
			return name
		}
		if isAbs && (strings.Contains(lname, "grid") || strings.Contains(lname, "dot") ||
			strings.Contains(lname, "pattern") || strings.Contains(lname, "texture") ||
			strings.Contains(lname, "surface") || strings.Contains(lname, "grain") || strings.Contains(lname, "paper")) {
			return name
		}
	}
	return ""
}

func looksLikeSharedChrome(name string) bool {
	l := strings.ToLower(name)
	for _, kw := range []string{"nav", "header", "topbar", "top bar", "footer", "sidebar", "shell", "tabbar", "tab bar", "chrome", "masthead"} {
		if strings.Contains(l, kw) {
			return true
		}
	}
	return false
}
