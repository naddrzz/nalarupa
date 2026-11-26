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
// consistency checks on the synced document and appends any findings under "## Issues detected".
func AppendDesignAudit(e *engine.Engine, toolName, reportText string, isError bool) string {
	if isError || toolName != "execute" || e == nil {
		return reportText
	}
	var findings []string
	e.Locked(func() {
		findings = collectDesignAuditLocked(e, reportText)
	})
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

func collectDesignAuditLocked(e *engine.Engine, existingReport string) []string {
	if e.Doc == nil {
		return nil
	}
	e.Sync()

	// Skip cross-screen style comparison when intentionally exploring divergent style variations.
	styleVariationMode := e.Agent.Mode == "side" || (e.Agent.Cook > 1 && e.Agent.Iterate == "style")

	// Collect defined color variables: hex (lowercased) -> $varName
	colorVarByHex := map[string]string{}
	if vars := jsonv.Obj(jsonv.Get(e.Doc.Root, "variables")); vars != nil {
		vars.Each(func(name string, v jsonv.Value) {
			if !jsonv.StrIs(jsonv.Get(v, "type"), "color") {
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
	doc.Walk(e.Doc.Children(), func(n jsonv.Value) {
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

	for _, topRaw := range e.Doc.Children() {
		if !jsonv.StrIs(jsonv.Get(topRaw, "type"), "frame") || jsonv.IsTrue(jsonv.Get(topRaw, "reusable")) {
			continue
		}
		topID := jsonv.StrOr(jsonv.Get(topRaw, "id"))
		layer, ok := e.Renderer.Layers[topID]
		if !ok || len(layer.Flat) == 0 {
			continue
		}
		rootFlat := layer.Flat[0]
		if rootFlat.W < 280 || rootFlat.H < 200 {
			continue
		}

		// 1. Deep Clipped-Screen Overflow Gate:
		// Catch descendants (such as fixed-width cards inside a fit_content horizontal row) that overflow
		// the clipped screen frame even when only the inner children were touched in this execute call!
		if rootFlat.Clip {
			warnings = append(warnings, checkScreenOverflows(topRaw, layer.Flat, existingReport)...)
		}

		// Gather screen metadata for multi-screen consistency checks
		rawFill := strings.TrimSpace(jsonv.StrOr(jsonv.Get(topRaw, "fill")))
		resFill := resolveColorToken(rawFill, colorVarByHex, e.Doc.Root)
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

	// 2. Multi-Screen Consistency Gate (when >= 2 screens exist in the same flow)
	if !styleVariationMode && len(screens) >= 2 {
		base := screens[0]
		for _, sc := range screens[1:] {
			// 2a. Raw hex on screen ground when matching semantic variable exists
			if strings.HasPrefix(sc.rawFill, "#") {
				if tok, ok := colorVarByHex[strings.ToLower(sc.rawFill)]; ok {
					warnings = append(warnings, fmt.Sprintf(
						"screen %q (%s) hardcodes fill %s instead of document variable %s; bind shared colors to semantic variables so all screens stay synchronized",
						sc.name, sc.id, sc.rawFill, tok))
				}
			}
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
	rx0 := root.X
	rx1, ry1 := root.X+root.W, root.Y+root.H

	// Index raw nodes by ID inside this screen so we can distinguish intentional decorative bleed shapes
	// (e.g. blurred background glow ellipses) from content/layout containers, cards, controls, and text.
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

		// Skip intermediate fit_content row/column wrappers if they have children we can point at more specifically,
		// OR report the leaf/card that actually gets clipped at the screen boundary!
		overRight := (f.X + f.W) - rx1
		overLeft := rx0 - f.X
		overBottom := (f.Y + f.H) - ry1

		if overRight <= 1.0 && overLeft <= 1.0 && overBottom <= 1.0 {
			continue
		}
		// If existing script validate already named this exact node sticking out, avoid duplicate noise unless it lacked fill_container guidance.
		alreadyNamed := strings.Contains(existingReport, `"`+f.Name+`" (`+f.ID+`) sticks out`)

		// Prefer reporting concrete clipped cards/controls/text rather than an outer auto-layout wrapper when children also overflow.
		if f.Type == "frame" && i+1 < len(flats) && flats[i+1].Depth > f.Depth {
			childAlsoOverflows := false
			for j := i + 1; j < len(flats) && flats[j].Depth > f.Depth; j++ {
				cf := flats[j]
				if (cf.X+cf.W)-rx1 > 1.0 || rx0-cf.X > 1.0 || (cf.Y+cf.H)-ry1 > 1.0 {
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
	// Decorative background glows, ambient circles, rules, and paths placed with layoutPosition:"absolute"
	if f.Type == "ellipse" || f.Type == "path" || f.Type == "line" || f.Type == "polygon" {
		return true
	}
	if f.Type == "rectangle" {
		// Blurred or low-opacity ambient shapes are intentional bleed backgrounds
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
			if v, exists := vars.Get(name); exists {
				val := jsonv.Get(v, "value")
				if arr := jsonv.Arr(val); arr != nil && len(arr.Items) > 0 {
					val = jsonv.Get(arr.Items[0], "value")
				}
				if hex, ok := jsonv.Str(val); ok {
					return strings.ToLower(strings.TrimSpace(hex))
				}
			}
		}
	}
	return strings.ToLower(raw)
}

func detectSurfaceMotif(topRaw jsonv.Value, rootFlat render.Flat) string {
	// Check if the screen frame itself uses a non-flat fill (gradient / image / pattern)
	if fillV := jsonv.Get(topRaw, "fill"); fillV != nil {
		for _, f := range svg.Fills(fillV) {
			if _, isFlat := svg.Flat(f); !isFlat {
				return "textured/gradient ground fill"
			}
		}
	}
	// Or if a direct child acts as a large background pattern / grid / surface motif layer
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
