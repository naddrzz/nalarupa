package main

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
)

const BadInspectorValueMsg = "Unable to apply that value. Check its format, for example #FF6A3D or 24, and try again."

var (
	reSignedFloat   = regexp.MustCompile(`^-?\d+(\.\d+)?$`)
	reUnsignedFloat = regexp.MustCompile(`^\d+(\.\d+)?$`)
	reDigitsOnly    = regexp.MustCompile(`^\d+$`)
	reHexPaint      = regexp.MustCompile(`(?i)^#([0-9a-f]{3}|[0-9a-f]{6}|[0-9a-f]{8})$`)

	knownPaintTypes  = []string{"color", "gradient", "image", "mesh_gradient", "shader"}
	knownEffectTypes = []string{"shadow", "blur", "background_blur"}
	noFillTypes      = []string{"line", "note", "prompt", "context"}
	strokeTypes      = []string{"frame", "rectangle", "ellipse", "polygon", "path", "line"}
	radiusTypes      = []string{"frame", "rectangle", "polygon"}
)

type alignBtnDef struct {
	Dir   string
	Icon  string
	Label string
}

var inspectorAlignBtns = []alignBtnDef{
	{Dir: "left", Icon: "alignLeft", Label: "Align left (Alt+A)"},
	{Dir: "hcenter", Icon: "alignHCenter", Label: "Align horizontal centers (Alt+H)"},
	{Dir: "right", Icon: "alignRight", Label: "Align right (Alt+D)"},
	{Dir: "top", Icon: "alignTop", Label: "Align top (Alt+W)"},
	{Dir: "vcenter", Icon: "alignVCenter", Label: "Align vertical centers (Alt+V)"},
	{Dir: "bottom", Icon: "alignBottom", Label: "Align bottom (Alt+S)"},
}

// InspectorState holds persistent scroll and mixed-stepping state for the Design Inspector.
type InspectorState struct {
	Scroll       ui.ScrollState
	TextDraft    string
	TextDraftID  string
	TextSynced   string
	MixedSession string
	MixedBase    map[string]float64
	MixedAccum   float64
}

// ParseInspectorJSONOrScalar parses numbers, JSON arrays/objects, or strings; empty returns nil (JSON null).
func ParseInspectorJSONOrScalar(v string) any {
	t := strings.TrimSpace(v)
	if t == "" {
		return nil
	}
	if reSignedFloat.MatchString(t) {
		if f, err := strconv.ParseFloat(t, 64); err == nil {
			return f
		}
	}
	if strings.HasPrefix(t, "[") || strings.HasPrefix(t, "{") {
		var parsed any
		if err := json.Unmarshal([]byte(t), &parsed); err == nil {
			return parsed
		}
	}
	return t
}

func isValidVar(v any, vars map[string]any, allowedTypes ...string) bool {
	s, ok := v.(string)
	if !ok || !strings.HasPrefix(s, "$") || vars == nil {
		return false
	}
	raw, exists := vars[s[1:]]
	if !exists {
		return false
	}
	if len(allowedTypes) == 0 {
		allowedTypes = []string{"number"}
	}
	def, ok := raw.(map[string]any)
	if !ok {
		return false
	}
	vt, _ := def["type"].(string)
	return slices.Contains(allowedTypes, vt)
}

func isNonNegLength(v any) bool {
	switch x := v.(type) {
	case float64:
		return !math.IsNaN(x) && !math.IsInf(x, 0) && x >= 0
	case int:
		return x >= 0
	default:
		return false
	}
}

func isLengthOrVar(v any, vars map[string]any) bool {
	return isNonNegLength(v) || isValidVar(v, vars, "number")
}

// ValidPaint validates a fill/stroke paint input against Nalarupa's renderer contract.
func ValidPaint(v string, vars map[string]any) bool {
	t := strings.TrimSpace(v)
	if t == "" || reHexPaint.MatchString(t) {
		return true
	}
	if strings.HasPrefix(t, "$") {
		if vars == nil {
			return false
		}
		_, ok := vars[t[1:]]
		return ok
	}
	parsed := ParseInspectorJSONOrScalar(t)
	checkOne := func(f any) bool {
		if s, ok := f.(string); ok {
			return strings.HasPrefix(s, "#") || strings.HasPrefix(s, "$")
		}
		m, ok := f.(map[string]any)
		if !ok || m == nil {
			return false
		}
		pt, _ := m["type"].(string)
		if pt == "" {
			pt = "color"
		}
		if pt == "color" {
			_, ok := m["color"].(string)
			return ok
		}
		return slices.Contains(knownPaintTypes, pt)
	}
	switch x := parsed.(type) {
	case []any:
		if len(x) == 0 {
			return false
		}
		for _, item := range x {
			if !checkOne(item) {
				return false
			}
		}
		return true
	case map[string]any:
		return checkOne(x)
	default:
		return false
	}
}

// SolidSwatchHex extracts a #RRGGBB/#RGB color from fill (including $variables) for Swatch preview.
func SolidSwatchHex(fill any, vars map[string]any) (ui.Color, bool) {
	cur := fill
	if arr, ok := cur.([]any); ok && len(arr) > 0 {
		cur = arr[0]
	}
	var cStr string
	switch x := cur.(type) {
	case string:
		cStr = x
	case map[string]any:
		if pt, _ := x["type"].(string); pt == "" || pt == "color" {
			cStr, _ = x["color"].(string)
		}
	}
	if strings.HasPrefix(cStr, "$") && vars != nil {
		if def, ok := vars[cStr[1:]].(map[string]any); ok {
			val := def["value"]
			if arr, ok := val.([]any); ok && len(arr) > 0 {
				if first, ok := arr[0].(map[string]any); ok {
					cStr, _ = first["value"].(string)
				}
			} else if sv, ok := val.(string); ok {
				cStr = sv
			}
		}
	}
	return ParseInspectorHexColor(cStr)
}

// SidesOf parses 1, 2, or 4 non-negative lengths (CSS space/comma or JSON array) or $variable;
// returns (value, true) on valid input (nil clears), or (nil, false) when invalid.
func SidesOf(v string, vars map[string]any, allowedCounts []int) (any, bool) {
	t := strings.TrimSpace(v)
	if t == "" {
		return nil, true
	}
	if isValidVar(t, vars, "number") {
		return t, true
	}
	var items []any
	if strings.HasPrefix(t, "[") {
		p := ParseInspectorJSONOrScalar(t)
		arr, ok := p.([]any)
		if !ok {
			return nil, false
		}
		items = arr
	} else {
		parts := strings.FieldsFunc(t, func(r rune) bool {
			return r == ' ' || r == '\t' || r == ','
		})
		for _, part := range parts {
			if !reUnsignedFloat.MatchString(part) {
				return nil, false
			}
			f, err := strconv.ParseFloat(part, 64)
			if err != nil {
				return nil, false
			}
			items = append(items, f)
		}
	}
	if !slices.Contains(allowedCounts, len(items)) {
		return nil, false
	}
	for _, it := range items {
		if !isLengthOrVar(it, vars) {
			return nil, false
		}
	}
	if len(items) == 1 {
		return items[0], true
	}
	return items, true
}

// LengthOf validates a single non-negative number, $variable, or empty (nil).
func LengthOf(v string, vars map[string]any) (any, bool) {
	t := strings.TrimSpace(v)
	if t == "" {
		return nil, true
	}
	if isValidVar(t, vars, "number") {
		return t, true
	}
	if reUnsignedFloat.MatchString(t) {
		f, err := strconv.ParseFloat(t, 64)
		if err == nil {
			return f, true
		}
	}
	return nil, false
}

// StrokeWidthOf validates scalar stroke width or per-side JSON {top,right,bottom,left}.
func StrokeWidthOf(v string, vars map[string]any) (any, bool) {
	if p, ok := LengthOf(v, vars); ok {
		return p, true
	}
	o := ParseInspectorJSONOrScalar(v)
	m, ok := o.(map[string]any)
	if !ok || len(m) == 0 {
		return nil, false
	}
	for k, val := range m {
		if k != "top" && k != "right" && k != "bottom" && k != "left" {
			return nil, false
		}
		if !isLengthOrVar(val, vars) {
			return nil, false
		}
	}
	return m, true
}

// FontWeightOf validates "normal", "bold", 1..1000, $variable, or empty (nil).
func FontWeightOf(v string, vars map[string]any) (any, bool) {
	t := strings.TrimSpace(v)
	if t == "" {
		return nil, true
	}
	if isValidVar(t, vars, "number", "string") || t == "normal" || t == "bold" {
		return t, true
	}
	if reDigitsOnly.MatchString(t) {
		n, err := strconv.Atoi(t)
		if err == nil && n >= 1 && n <= 1000 {
			return t, true
		}
	}
	return nil, false
}

// EffectOf validates single or array of {type: "shadow"|"blur"|"background_blur", ...} or empty (nil).
func EffectOf(v string, vars map[string]any) (any, bool) {
	t := strings.TrimSpace(v)
	if t == "" {
		return nil, true
	}
	p := ParseInspectorJSONOrScalar(t)
	isCoord := func(x any) bool {
		if f, ok := x.(float64); ok && !math.IsNaN(f) && !math.IsInf(f, 0) {
			return true
		}
		return isValidVar(x, vars, "number")
	}
	isHue := func(x any) bool {
		s, ok := x.(string)
		if !ok {
			return false
		}
		st := strings.TrimSpace(s)
		return reHexPaint.MatchString(st) || isValidVar(st, vars, "color")
	}
	checkOne := func(raw any) bool {
		e, ok := raw.(map[string]any)
		if !ok || e == nil {
			return false
		}
		et, _ := e["type"].(string)
		if !slices.Contains(knownEffectTypes, et) {
			return false
		}
		if et != "shadow" {
			if r, exists := e["radius"]; exists && r != nil && !isLengthOrVar(r, vars) {
				return false
			}
			return true
		}
		if bl, exists := e["blur"]; exists && bl != nil && !isLengthOrVar(bl, vars) {
			return false
		}
		if col, exists := e["color"]; exists && col != nil && !isHue(col) {
			return false
		}
		if off, exists := e["offset"]; exists && off != nil {
			om, ok := off.(map[string]any)
			if !ok {
				return false
			}
			if ox, hasX := om["x"]; hasX && ox != nil && !isCoord(ox) {
				return false
			}
			if oy, hasY := om["y"]; hasY && oy != nil && !isCoord(oy) {
				return false
			}
		}
		return true
	}
	switch arr := p.(type) {
	case []any:
		if len(arr) == 0 {
			return nil, false
		}
		for _, item := range arr {
			if !checkOne(item) {
				return nil, false
			}
		}
		return arr, true
	case map[string]any:
		if checkOne(arr) {
			return arr, true
		}
	}
	return nil, false
}

func formatValueString(v any) string {
	if v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return formatNum(x)
	case bool:
		if x {
			return "true"
		}
		return "false"
	default:
		b, err := json.Marshal(x)
		if err != nil {
			return fmt.Sprint(x)
		}
		return string(b)
	}
}

type selectedNodeEntry struct {
	id string
	nd NodeDetail
}

func (a *NalarupaApp) viewInspector(c *ui.Context) {
	takesText := c.FocusTakesText()
	scroll := ui.Scroll(c.Key("inspector-scroll")).
		Fill().
		TrackScroll(&a.Inspector.Scroll)

	scroll.HandleInput(func(ev ui.InputEvent) bool {
		if ev.Kind == ui.InputKeyDown && !takesText {
			return a.HandleEditorShortcut(ev.Mods, ev.Key)
		}
		return false
	})

	scroll.Children(func() {
		if len(a.State.Selection) == 0 {
			ui.Column(c.Key("inspector-empty")).FillWidth().Padding(16).Gap(10).Children(func() {
				ui.Text(c, "Nothing selected").FontSize(13).FontWeight(600).TextColor(C.Text)
				ui.Text(c, "Click a layer on the canvas or in Layers to inspect and edit its properties.").
					FontSize(12).TextColor(C.Muted).Wrap()
				ui.Text(c, "Tools: V move · F frame · R rectangle · O ellipse · T text · P pen · H hand").
					FontSize(11).TextColor(C.Ghost).Wrap()
				ui.Text(c, "Layout & align: Shift+A auto layout · Ctrl+G group · Alt+W/A/S/D/H/V align · Alt+hover measure").
					FontSize(11).TextColor(C.Ghost).Wrap()
			})
			return
		}

		var infos []selectedNodeEntry
		for _, id := range a.State.Selection {
			var nd NodeDetail
			if a.Bridge.TryCall("node", map[string]any{"id": id}, &nd) && nd.Exists && nd.Node != nil {
				infos = append(infos, selectedNodeEntry{id: id, nd: nd})
			}
		}

		// Pure instance child selection without direct node record
		if len(infos) == 0 {
			firstID := a.State.Selection[0]
			ui.Column(c.Key("inspector-instance-child")).FillWidth().Padding(16).Gap(10).Children(func() {
				ui.Text(c, "Instance child "+firstID).FontSize(13).FontWeight(600).TextColor(C.Component)
				ui.Text(c, "Layers inside a component instance cannot be deleted or duplicated on their own. Hide it to leave it out of this instance.").
					FontSize(12).TextColor(C.Muted).Wrap()
				if ActionBtn(c, "hide-instance-child", "Hide", "hidden", "").Clicked() {
					var st CoreState
					if err := a.Bridge.Call("update", map[string]any{
						"ids":   a.State.Selection,
						"props": map[string]any{"enabled": false},
					}, &st); err == nil {
						a.State = st
						a.Select([]string{})
					}
				}
			})
			return
		}

		multi := len(infos) > 1
		first := infos[0]
		firstType, _ := first.nd.Node["type"].(string)
		firstReusable, _ := first.nd.Node["reusable"].(bool)
		vars := first.nd.Variables
		if vars == nil {
			vars = map[string]any{}
		}

		hasInstanceChild := false
		for _, id := range a.State.Selection {
			if strings.Contains(id, "/") {
				hasInstanceChild = true
				break
			}
		}

		applyUpdate := func(props map[string]any, coalesce string) bool {
			args := map[string]any{
				"ids":   a.State.Selection,
				"props": props,
			}
			if coalesce != "" {
				args["coalesce"] = coalesce
			}
			var st CoreState
			if err := a.Bridge.Call("update", args, &st); err != nil {
				a.ShowToast(BadInspectorValueMsg, true)
				return false
			}
			a.State = st
			a.syncWindowTitle()
			return true
		}

		applyStepMixed := func(prop string, delta float64, minVal *float64, coalesce string) {
			sessionKey := prop + ":" + coalesce
			if a.Inspector.MixedSession != sessionKey {
				a.Inspector.MixedSession = sessionKey
				a.Inspector.MixedBase = map[string]float64{}
				a.Inspector.MixedAccum = 0
				for _, inf := range infos {
					v := asFloat(inf.nd.Node[prop])
					if (prop == "width" || prop == "height") && inf.nd.Box != nil {
						if prop == "width" {
							v = inf.nd.Box.W
						} else {
							v = inf.nd.Box.H
						}
					}
					a.Inspector.MixedBase[inf.id] = v
				}
			}
			a.Inspector.MixedAccum += delta
			for _, inf := range infos {
				base := a.Inspector.MixedBase[inf.id]
				next := math.Round((base+a.Inspector.MixedAccum)*100) / 100
				if minVal != nil && next < *minVal {
					next = *minVal
				}
				var st CoreState
				if err := a.Bridge.Call("update", map[string]any{
					"ids":      []string{inf.id},
					"props":    map[string]any{prop: next},
					"coalesce": coalesce,
				}, &st); err == nil {
					a.State = st
				}
			}
			a.syncWindowTitle()
		}

		// Header: Icon + Layer name (or "N layers selected")
		ui.Row(c.Key("inspector-header")).
			FillWidth().
			Padding(12, 14).
			Gap(8).
			AlignItems(ui.Center).
			BorderWidth(0, 0, 1, 0).
			BorderColor(C.Border).
			Children(func() {
				hdrIcon := "layers"
				if !multi {
					hdrIcon = NodeIconName(firstType, firstReusable)
				}
				iconCol := C.Muted
				if !multi && (firstReusable || firstType == "ref") {
					iconCol = C.Component
				}
				ui.Icon(c, IconSVG(hdrIcon)).FontSize(15).TextColor(iconCol).Shrink(0)
				if multi {
					ui.Text(c, fmt.Sprintf("%d layers selected", len(infos))).
						FontSize(12).FontWeight(600).TextColor(C.Text).Grow(1)
				} else {
					curName, _ := first.nd.Node["name"].(string)
					if curName == "" && first.nd.Box != nil {
						curName = first.nd.Box.Name
					}
					CommittedField(c, FieldOpts{
						Key:       "inspector-name",
						AriaLabel: "Layer name",
						Value:     curName,
						Grow:      true,
						FocusBack: &a.CanvasHandle,
						OnCommit: func(val string) bool {
							trimmed := strings.TrimSpace(val)
							if trimmed == "" {
								return false
							}
							return applyUpdate(map[string]any{"name": trimmed}, "")
						},
					})
				}
			})

		// Check flow state
		anyInFlow := false
		allInFlow := true
		allGroup := true
		allAutoFrame := true
		for _, inf := range infos {
			nt, _ := inf.nd.Node["type"].(string)
			if nt != "group" {
				allGroup = false
			}
			lay, _ := inf.nd.Node["layout"].(string)
			if nt != "frame" || (lay != "horizontal" && lay != "vertical") {
				allAutoFrame = false
			}
			lp, _ := inf.nd.Node["layoutPosition"].(string)
			inF := (inf.nd.ParentLayout == "horizontal" || inf.nd.ParentLayout == "vertical") && lp != "absolute"
			if inF {
				anyInFlow = true
			} else {
				allInFlow = false
			}
		}

		// 1. Section: Position
		InspectorSection(c, "sec-position", "Position", nil, func() {
			ui.Row(c.Key("align-row")).FillWidth().Gap(4).Children(func() {
				for _, ab := range inspectorAlignBtns {
					btn := IconBtn(c, "align-"+ab.Dir, ab.Icon, ab.Label, 26, false)
					if btn.Clicked() {
						var st CoreState
						if err := a.Bridge.Call("align", map[string]any{
							"ids": a.State.Selection,
							"dir": ab.Dir,
						}, &st); err == nil {
							a.State = st
							a.syncWindowTitle()
						}
					}
				}
			})

			if !anyInFlow {
				xVal, xMixed := numericPropAcross(infos, func(e selectedNodeEntry) float64 {
					return asFloat(e.nd.Node["x"])
				})
				yVal, yMixed := numericPropAcross(infos, func(e selectedNodeEntry) float64 {
					return asFloat(e.nd.Node["y"])
				})
				ui.Row(c.Key("pos-xy")).FillWidth().Gap(8).Children(func() {
					ScrubNumField(c, NumFieldOpts{
						Key:       "inspector-x",
						AriaLabel: "X position",
						Prefix:    "X",
						Value:     xVal,
						Mixed:     xMixed,
						Step:      1,
						Grow:      true,
						FocusBack: &a.CanvasHandle,
						OnSet: func(v float64, coal string) bool {
							return applyUpdate(map[string]any{"x": v}, coal)
						},
						OnStep: func(delta float64, coal string) {
							applyStepMixed("x", delta, nil, coal)
						},
					})
					ScrubNumField(c, NumFieldOpts{
						Key:       "inspector-y",
						AriaLabel: "Y position",
						Prefix:    "Y",
						Value:     yVal,
						Mixed:     yMixed,
						Step:      1,
						Grow:      true,
						FocusBack: &a.CanvasHandle,
						OnSet: func(v float64, coal string) bool {
							return applyUpdate(map[string]any{"y": v}, coal)
						},
						OnStep: func(delta float64, coal string) {
							applyStepMixed("y", delta, nil, coal)
						},
					})
				})
			}

			rotVal, rotMixed := numericPropAcross(infos, func(e selectedNodeEntry) float64 {
				if e.nd.Local != nil {
					return e.nd.Local.Rotation
				}
				return asFloat(e.nd.Node["rotation"])
			})
			opVal, opMixed := numericPropAcross(infos, func(e selectedNodeEntry) float64 {
				if raw, ok := e.nd.Node["opacity"]; ok && raw != nil {
					return math.Round(asFloat(raw) * 100)
				}
				return 100
			})
			ui.Row(c.Key("pos-rot-op")).FillWidth().Gap(8).Children(func() {
				ScrubNumField(c, NumFieldOpts{
					Key:       "inspector-rotation",
					AriaLabel: "Rotation",
					Prefix:    "↻",
					Value:     rotVal,
					Mixed:     rotMixed,
					Step:      1,
					Grow:      true,
					FocusBack: &a.CanvasHandle,
					OnSet: func(toDeg float64, coal string) bool {
						for _, inf := range infos {
							lp, _ := inf.nd.Node["layoutPosition"].(string)
							inF := (inf.nd.ParentLayout == "horizontal" || inf.nd.ParentLayout == "vertical") && lp != "absolute"
							props := map[string]any{"rotation": toDeg}
							if !inF && inf.nd.Box != nil {
								fromDeg, fx, fy, bx, by := 0.0, false, false, 0.0, 0.0
								if inf.nd.Local != nil {
									fromDeg = inf.nd.Local.Rotation
									fx = inf.nd.Local.FlipX
									fy = inf.nd.Local.FlipY
									bx = inf.nd.Local.BX
									by = inf.nd.Local.BY
								}
								orig := TurnAroundCenter(asFloat(inf.nd.Node["x"]), asFloat(inf.nd.Node["y"]), inf.nd.Box.W, inf.nd.Box.H, fromDeg, toDeg, fx, fy, bx, by)
								props["x"] = orig[0]
								props["y"] = orig[1]
							}
							var st CoreState
							if err := a.Bridge.Call("update", map[string]any{
								"ids":      []string{inf.id},
								"props":    props,
								"coalesce": coal,
							}, &st); err != nil {
								a.ShowToast(BadInspectorValueMsg, true)
								return false
							}
							a.State = st
						}
						a.syncWindowTitle()
						return true
					},
				})
				ScrubNumField(c, NumFieldOpts{
					Key:       "inspector-opacity",
					AriaLabel: "Opacity",
					Prefix:    "α",
					Value:     opVal,
					Mixed:     opMixed,
					Step:      1,
					HasMin:    true,
					Min:       0,
					HasMax:    true,
					Max:       100,
					Grow:      true,
					FocusBack: &a.CanvasHandle,
					OnSet: func(v float64, coal string) bool {
						var opProp any
						if v < 100 {
							opProp = math.Round(v) / 100.0
						}
						return applyUpdate(map[string]any{"opacity": opProp}, coal)
					},
				})
			})
		})

		// 2. Section: Size
		sizeTitle := "Size"
		if allInFlow {
			sizeTitle = "Size (auto layout child)"
		}
		InspectorSection(c, "sec-size", sizeTitle, nil, func() {
			if !allGroup {
				wVal, wMixed := numericPropAcross(infos, func(e selectedNodeEntry) float64 {
					if e.nd.Box != nil {
						return e.nd.Box.W
					}
					return asFloat(e.nd.Node["width"])
				})
				hVal, hMixed := numericPropAcross(infos, func(e selectedNodeEntry) float64 {
					if e.nd.Box != nil {
						return e.nd.Box.H
					}
					return asFloat(e.nd.Node["height"])
				})
				zero := 0.0
				ui.Row(c.Key("size-wh")).FillWidth().Gap(8).Children(func() {
					ScrubNumField(c, NumFieldOpts{
						Key:       "inspector-w",
						AriaLabel: "Width",
						Prefix:    "W",
						Value:     wVal,
						Mixed:     wMixed,
						Step:      1,
						HasMin:    true,
						Min:       0,
						Grow:      true,
						FocusBack: &a.CanvasHandle,
						OnSet: func(v float64, coal string) bool {
							props := map[string]any{"width": v}
							if !multi && firstType == "text" {
								tg, _ := first.nd.Node["textGrowth"].(string)
								if tg == "" || tg == "auto" {
									props["textGrowth"] = "fixed-width"
								}
							}
							return applyUpdate(props, coal)
						},
						OnStep: func(delta float64, coal string) {
							applyStepMixed("width", delta, &zero, coal)
						},
					})
					ScrubNumField(c, NumFieldOpts{
						Key:       "inspector-h",
						AriaLabel: "Height",
						Prefix:    "H",
						Value:     hVal,
						Mixed:     hMixed,
						Step:      1,
						HasMin:    true,
						Min:       0,
						Grow:      true,
						FocusBack: &a.CanvasHandle,
						OnSet: func(v float64, coal string) bool {
							props := map[string]any{"height": v}
							if !multi && firstType == "text" {
								props["textGrowth"] = "fixed-width-height"
								if first.nd.Box != nil && first.nd.Node["width"] == nil {
									props["width"] = first.nd.Box.W
								}
							}
							return applyUpdate(props, coal)
						},
						OnStep: func(delta float64, coal string) {
							applyStepMixed("height", delta, &zero, coal)
						},
					})
				})
			}

			if (allAutoFrame || (allInFlow && !allGroup)) {
				ui.Row(c.Key("size-chips")).FillWidth().Wrap().Gap(6).Children(func() {
					if allAutoFrame {
						hugW := allPropPrefix(infos, "width", "fit_content")
						if _, clicked := ToggleChip(c, "hug-width", "Hug W", hugW); clicked {
							if hugW {
								w := 100.0
								if first.nd.Box != nil {
									w = first.nd.Box.W
								}
								applyUpdate(map[string]any{"width": w}, "")
							} else {
								applyUpdate(map[string]any{"width": "fit_content"}, "")
							}
						}
						hugH := allPropPrefix(infos, "height", "fit_content")
						if _, clicked := ToggleChip(c, "hug-height", "Hug H", hugH); clicked {
							if hugH {
								h := 100.0
								if first.nd.Box != nil {
									h = first.nd.Box.H
								}
								applyUpdate(map[string]any{"height": h}, "")
							} else {
								applyUpdate(map[string]any{"height": "fit_content"}, "")
							}
						}
					}
					if allInFlow && !allGroup {
						fillW := allPropPrefix(infos, "width", "fill_container")
						if _, clicked := ToggleChip(c, "fill-width", "Fill W", fillW); clicked {
							if fillW {
								w := 100.0
								if first.nd.Box != nil {
									w = first.nd.Box.W
								}
								applyUpdate(map[string]any{"width": w}, "")
							} else {
								applyUpdate(map[string]any{"width": "fill_container"}, "")
							}
						}
						fillH := allPropPrefix(infos, "height", "fill_container")
						if _, clicked := ToggleChip(c, "fill-height", "Fill H", fillH); clicked {
							if fillH {
								h := 100.0
								if first.nd.Box != nil {
									h = first.nd.Box.H
								}
								applyUpdate(map[string]any{"height": h}, "")
							} else {
								applyUpdate(map[string]any{"height": "fill_container"}, "")
							}
						}
					}
				})
			}

			if !multi && first.nd.Box != nil {
				prefix := "Rendered"
				if en, ok := first.nd.Node["enabled"].(bool); ok && !en {
					prefix = "Hidden"
				}
				ui.Text(c, fmt.Sprintf("%s %s × %s · drag a label to scrub, ↑/↓ steps",
					prefix, formatNum(first.nd.Box.W), formatNum(first.nd.Box.H))).
					FontSize(11).TextColor(C.Ghost).Wrap()
			}
		})

		// 3. Section: Layout (single frame)
		if !multi && firstType == "frame" {
			curLayout, _ := first.nd.Node["layout"].(string)
			if curLayout == "" {
				curLayout = "none"
			}
			InspectorSection(c, "sec-layout", "Layout", nil, func() {
				nextLay, layChanged := SegmentedTabs(c, "inspector-layout-mode", "Layout direction", curLayout, []SegOption[string]{
					{Key: "layout-free", Value: "none", Label: "Free"},
					{Key: "layout-row", Value: "horizontal", Label: "Row"},
					{Key: "layout-col", Value: "vertical", Label: "Column"},
				})
				if layChanged {
					var st CoreState
					var err error
					if nextLay == "none" {
						err = a.Bridge.Call("remove_layout", map[string]any{"ids": a.State.Selection}, &st)
					} else {
						err = a.Bridge.Call("set_layout", map[string]any{"ids": a.State.Selection, "layout": nextLay}, &st)
					}
					if err == nil {
						a.State = st
						a.syncWindowTitle()
					}
				}

				if curLayout == "horizontal" || curLayout == "vertical" {
					gapVal := asFloat(first.nd.Node["gap"])
					padStr := formatValueString(first.nd.Node["padding"])
					ui.Row(c.Key("layout-gap-pad")).FillWidth().Gap(8).Children(func() {
						ScrubNumField(c, NumFieldOpts{
							Key:       "inspector-gap",
							AriaLabel: "Gap",
							Prefix:    "G",
							Value:     gapVal,
							Step:      1,
							HasMin:    true,
							Min:       0,
							Grow:      true,
							FocusBack: &a.CanvasHandle,
							OnSet: func(v float64, coal string) bool {
								return applyUpdate(map[string]any{"gap": v}, coal)
							},
						})
						CommittedField(c, FieldOpts{
							Key:         "inspector-padding",
							AriaLabel:   "Padding",
							Prefix:      "P",
							Value:       padStr,
							Placeholder: "0",
							Grow:        true,
							FocusBack:   &a.CanvasHandle,
							OnCommit: func(val string) bool {
								parsed, ok := SidesOf(val, vars, []int{1, 2, 4})
								if !ok {
									a.ShowToast(BadInspectorValueMsg, true)
									return false
								}
								return applyUpdate(map[string]any{"padding": parsed}, "")
							},
						})
					})

					justCur, _ := first.nd.Node["justifyContent"].(string)
					if justCur == "" {
						justCur = "start"
					}
					nextJust, justChanged := SegmentedTabs(c, "inspector-distribute", "Distribute", justCur, []SegOption[string]{
						{Value: "start", Label: "Start"},
						{Value: "center", Label: "Mid"},
						{Value: "end", Label: "End"},
						{Value: "space_between", Label: "Between"},
					})
					if justChanged {
						applyUpdate(map[string]any{"justifyContent": nextJust}, "")
					}

					alignCur, _ := first.nd.Node["alignItems"].(string)
					if alignCur == "" {
						alignCur = "start"
					}
					startLbl, endLbl := "Top", "Bottom"
					if curLayout == "vertical" {
						startLbl, endLbl = "Left", "Right"
					}
					nextAl, alChanged := SegmentedTabs(c, "inspector-align-items", "Align items", alignCur, []SegOption[string]{
						{Value: "start", Label: startLbl},
						{Value: "center", Label: "Center"},
						{Value: "end", Label: endLbl},
					})
					if alChanged {
						applyUpdate(map[string]any{"alignItems": nextAl}, "")
					}
				} else {
					if ActionBtn(c, "add-auto-layout", "Add auto layout (Shift+A)", "", "").Clicked() {
						var res struct {
							ID    string    `json:"id"`
							State CoreState `json:"state"`
						}
						if err := a.Bridge.Call("add_layout", map[string]any{"ids": a.State.Selection}, &res); err == nil {
							a.State = res.State
							a.syncWindowTitle()
						}
					}
				}

				clipCur := "visible"
				if cl, ok := first.nd.Node["clip"].(bool); ok && cl {
					clipCur = "clip"
				}
				nextClip, clipChanged := SegmentedTabs(c, "inspector-clip", "Overflow", clipCur, []SegOption[string]{
					{Value: "visible", Label: "Overflow visible"},
					{Value: "clip", Label: "Clip content"},
				})
				if clipChanged {
					applyUpdate(map[string]any{"clip": nextClip == "clip"}, "")
				}
			})
		}

		// 4. Section: Text (single text node)
		if !multi && firstType == "text" {
			contentStr, _ := first.nd.Node["content"].(string)
			if a.Inspector.TextDraftID != first.id || contentStr != a.Inspector.TextSynced {
				a.Inspector.TextDraftID = first.id
				a.Inspector.TextDraft = contentStr
				a.Inspector.TextSynced = contentStr
			}
			InspectorSection(c, "sec-text", "Text", nil, func() {
				ta := ui.TextArea(c.Key("inspector-text-content"), &a.Inspector.TextDraft).
					FillWidth().
					Padding(6, 8).
					Radius(6).
					Background(C.Panel2).
					Border(1, C.Field).
					TextColor(C.Text).
					CaretColor(C.Accent).
					FontSize(12).
					Lines(2, 8).
					Label("Text content")
				ta.OnTextKey(func(k ui.TextKey) bool {
					if k.Key == ui.KeyEnter && (k.Mods&ui.Shift == 0) {
						val := a.Inspector.TextDraft
						if applyUpdate(map[string]any{"content": val}, "") {
							a.Inspector.TextSynced = val
						}
						a.CanvasHandle.Focus()
						return true
					}
					return false
				})
				if ta.FocusLeft() && a.Inspector.TextDraft != contentStr {
					val := a.Inspector.TextDraft
					if applyUpdate(map[string]any{"content": val}, "") {
						a.Inspector.TextSynced = val
					}
				}

				ff, _ := first.nd.Node["fontFamily"].(string)
				if ff == "" {
					ff = "Inter"
				}
				CommittedField(c, FieldOpts{
					Key:       "inspector-font-family",
					AriaLabel: "Font family",
					Value:     ff,
					FocusBack: &a.CanvasHandle,
					OnCommit: func(val string) bool {
						trimmed := strings.TrimSpace(val)
						if trimmed == "" {
							trimmed = "Inter"
						}
						return applyUpdate(map[string]any{"fontFamily": trimmed}, "")
					},
				})

				fs := asFloat(first.nd.Node["fontSize"])
				if fs <= 0 {
					fs = 16
				}
				fwStr := formatValueString(first.nd.Node["fontWeight"])
				ui.Row(c.Key("text-size-weight")).FillWidth().Gap(8).Children(func() {
					ScrubNumField(c, NumFieldOpts{
						Key:       "inspector-font-size",
						AriaLabel: "Font size",
						Prefix:    "S",
						Value:     fs,
						Step:      1,
						HasMin:    true,
						Min:       1,
						Grow:      true,
						FocusBack: &a.CanvasHandle,
						OnSet: func(v float64, coal string) bool {
							return applyUpdate(map[string]any{"fontSize": v}, coal)
						},
					})
					CommittedField(c, FieldOpts{
						Key:         "inspector-font-weight",
						AriaLabel:   "Font weight",
						Prefix:      "W",
						Value:       fwStr,
						Placeholder: "400",
						Grow:        true,
						FocusBack:   &a.CanvasHandle,
						OnCommit: func(val string) bool {
							parsed, ok := FontWeightOf(val, vars)
							if !ok {
								a.ShowToast(BadInspectorValueMsg, true)
								return false
							}
							return applyUpdate(map[string]any{"fontWeight": parsed}, "")
						},
					})
				})

				lhStr := formatValueString(first.nd.Node["lineHeight"])
				lsVal := asFloat(first.nd.Node["letterSpacing"])
				ui.Row(c.Key("text-lh-ls")).FillWidth().Gap(8).Children(func() {
					CommittedField(c, FieldOpts{
						Key:         "inspector-line-height",
						AriaLabel:   "Line height",
						Prefix:      "L",
						Value:       lhStr,
						Placeholder: "auto",
						Grow:        true,
						FocusBack:   &a.CanvasHandle,
						OnCommit: func(val string) bool {
							parsed, ok := LengthOf(val, vars)
							if !ok {
								a.ShowToast(BadInspectorValueMsg, true)
								return false
							}
							return applyUpdate(map[string]any{"lineHeight": parsed}, "")
						},
					})
					ScrubNumField(c, NumFieldOpts{
						Key:       "inspector-letter-spacing",
						AriaLabel: "Letter spacing",
						Prefix:    "K",
						Value:     lsVal,
						Step:      0.1,
						Grow:      true,
						FocusBack: &a.CanvasHandle,
						OnSet: func(v float64, coal string) bool {
							return applyUpdate(map[string]any{"letterSpacing": v}, coal)
						},
					})
				})

				taCur, _ := first.nd.Node["textAlign"].(string)
				if taCur == "" {
					taCur = "left"
				}
				nextTA, taChanged := SegmentedTabs(c, "inspector-text-align", "Text alignment", taCur, []SegOption[string]{
					{Value: "left", Label: "Left"},
					{Value: "center", Label: "Center"},
					{Value: "right", Label: "Right"},
				})
				if taChanged {
					applyUpdate(map[string]any{"textAlign": nextTA}, "")
				}

				tgCur, _ := first.nd.Node["textGrowth"].(string)
				if tgCur == "" {
					tgCur = "auto"
				}
				nextTG, tgChanged := SegmentedTabs(c, "inspector-text-growth", "Text resizing", tgCur, []SegOption[string]{
					{Value: "auto", Label: "Auto"},
					{Value: "fixed-width", Label: "Wrap"},
					{Value: "fixed-width-height", Label: "Fixed"},
				})
				if tgChanged {
					props := map[string]any{"textGrowth": nextTG}
					if nextTG == "auto" {
						props["width"] = nil
						props["height"] = nil
					} else if first.nd.Box != nil {
						props["width"] = first.nd.Box.W
						if nextTG == "fixed-width-height" {
							props["height"] = first.nd.Box.H
						}
					}
					applyUpdate(props, "")
				}
			})
		}

		// 5. Section: Icon (single icon node)
		if !multi && (firstType == "icon" || firstType == "icon_font") {
			InspectorSection(c, "sec-icon", "Icon", nil, func() {
				icName, _ := first.nd.Node["icon"].(string)
				if icName == "" {
					icName, _ = first.nd.Node["iconFontName"].(string)
				}
				libName, _ := first.nd.Node["library"].(string)
				if libName == "" {
					libName = "lucide"
				}
				CommittedField(c, FieldOpts{
					Key:       "inspector-icon-name",
					AriaLabel: "Icon name",
					Value:     icName,
					FocusBack: &a.CanvasHandle,
					OnCommit: func(val string) bool {
						return applyUpdate(map[string]any{"icon": strings.TrimSpace(val)}, "")
					},
				})
				CommittedField(c, FieldOpts{
					Key:       "inspector-icon-library",
					AriaLabel: "Icon library",
					Value:     libName,
					FocusBack: &a.CanvasHandle,
					OnCommit: func(val string) bool {
						return applyUpdate(map[string]any{"library": strings.TrimSpace(val)}, "")
					},
				})
			})
		}

		// 6. Section: Fill (shown when at least one selected node supports fill)
		anySupportsFill := false
		for _, inf := range infos {
			nt, _ := inf.nd.Node["type"].(string)
			if nt != "group" && !slices.Contains(noFillTypes, nt) {
				anySupportsFill = true
				break
			}
		}
		if anySupportsFill {
			fillVal, fillMixed := rawPropAcross(infos, "fill")
			if !multi && fillVal == nil && first.nd.Inherited != nil {
				fillVal = first.nd.Inherited["fill"]
			}
			fillStr := ""
			if !fillMixed {
				fillStr = formatValueString(fillVal)
			}
			InspectorSection(c, "sec-fill", "Fill", nil, func() {
				ui.Row(c.Key("fill-row")).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
					if !fillMixed {
						if swCol, ok := SolidSwatchHex(fillVal, vars); ok {
							ColorSwatch(c, "inspector-fill-swatch", swCol)
						}
					}
					ph := "#D9D9D9 or none"
					if fillMixed {
						ph = "Mixed"
					}
					CommittedField(c, FieldOpts{
						Key:         "inspector-fill",
						AriaLabel:   "Fill",
						Value:       fillStr,
						Placeholder: ph,
						Mono:        true,
						Grow:        true,
						FocusBack:   &a.CanvasHandle,
						OnCommit: func(val string) bool {
							if !ValidPaint(val, vars) {
								a.ShowToast(BadInspectorValueMsg, true)
								return false
							}
							return applyUpdate(map[string]any{"fill": ParseInspectorJSONOrScalar(val)}, "")
						},
					})
				})
			})
		}

		// 7. Section: Stroke & corners (single shape/frame/path/line)
		if !multi && slices.Contains(strokeTypes, firstType) {
			hasCorners := slices.Contains(radiusTypes, firstType)
			secTitle := "Stroke"
			if hasCorners {
				secTitle = "Stroke & corners"
			}
			strokeVal := first.nd.Node["stroke"]
			if strokeVal == nil && first.nd.Inherited != nil {
				strokeVal = first.nd.Inherited["stroke"]
			}
			swVal := first.nd.Node["strokeWidth"]
			if swVal == nil && first.nd.Inherited != nil {
				swVal = first.nd.Inherited["strokeWidth"]
			}
			InspectorSection(c, "sec-stroke", secTitle, nil, func() {
				ui.Row(c.Key("stroke-row")).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
					if swCol, ok := SolidSwatchHex(strokeVal, vars); ok {
						ColorSwatch(c, "inspector-stroke-swatch", swCol)
					}
					CommittedField(c, FieldOpts{
						Key:         "inspector-stroke",
						AriaLabel:   "Stroke color",
						Value:       formatValueString(strokeVal),
						Placeholder: "none",
						Mono:        true,
						Grow:        true,
						FocusBack:   &a.CanvasHandle,
						OnCommit: func(val string) bool {
							if !ValidPaint(val, vars) {
								a.ShowToast(BadInspectorValueMsg, true)
								return false
							}
							parsed := ParseInspectorJSONOrScalar(val)
							props := map[string]any{"stroke": parsed}
							if parsed != nil && first.nd.Node["strokeWidth"] == nil {
								props["strokeWidth"] = 1
							}
							return applyUpdate(props, "")
						},
					})
					CommittedField(c, FieldOpts{
						Key:         "inspector-stroke-width",
						AriaLabel:   "Stroke width",
						Value:       formatValueString(swVal),
						Placeholder: "0",
						Width:       58,
						FocusBack:   &a.CanvasHandle,
						OnCommit: func(val string) bool {
							parsed, ok := StrokeWidthOf(val, vars)
							if !ok {
								a.ShowToast(BadInspectorValueMsg, true)
								return false
							}
							props := map[string]any{"strokeWidth": parsed}
							if parsed != nil && first.nd.Node["stroke"] == nil {
								props["stroke"] = "#000000"
							}
							return applyUpdate(props, "")
						},
					})
				})

				if hasCorners {
					rawR := first.nd.Node["cornerRadius"]
					_, isComplexR := rawR.([]any)
					if strR, ok := rawR.(string); ok && strings.HasPrefix(strR, "$") {
						isComplexR = true
					}
					if isComplexR {
						CommittedField(c, FieldOpts{
							Key:       "inspector-radii",
							AriaLabel: "Corner radius",
							Prefix:    "R",
							Value:     formatValueString(rawR),
							FocusBack: &a.CanvasHandle,
							OnCommit: func(val string) bool {
								parsed, ok := SidesOf(val, vars, []int{1, 4})
								if !ok {
									a.ShowToast(BadInspectorValueMsg, true)
									return false
								}
								return applyUpdate(map[string]any{"cornerRadius": parsed}, "")
							},
						})
					} else {
						ScrubNumField(c, NumFieldOpts{
							Key:       "inspector-radius",
							AriaLabel: "Corner radius",
							Prefix:    "R",
							Value:     max(0.0, asFloat(rawR)),
							Step:      1,
							HasMin:    true,
							Min:       0,
							FocusBack: &a.CanvasHandle,
							OnSet: func(v float64, coal string) bool {
								return applyUpdate(map[string]any{"cornerRadius": v}, coal)
							},
						})
					}
				}
			})
		}

		// 8. Section: Effect (!multi)
		if !multi {
			effVal := first.nd.Node["effect"]
			InspectorSection(c, "sec-effect", "Effect", nil, func() {
				CommittedField(c, FieldOpts{
					Key:         "inspector-effect",
					AriaLabel:   "Effect",
					Value:       formatValueString(effVal),
					Placeholder: `{"type":"shadow","blur":12,"color":"#00000033"}`,
					Mono:        true,
					FocusBack:   &a.CanvasHandle,
					OnCommit: func(val string) bool {
						parsed, ok := EffectOf(val, vars)
						if !ok {
							a.ShowToast(BadInspectorValueMsg, true)
							return false
						}
						return applyUpdate(map[string]any{"effect": parsed}, "")
					},
				})
			})
		}

		// 9. Section: Layer
		InspectorSection(c, "sec-layer", "Layer", nil, func() {
			if !multi {
				ui.Row(c.Key("layer-comp-vis")).FillWidth().Gap(6).Children(func() {
					if !hasInstanceChild && (firstType == "frame" || firstType == "group" || firstReusable) {
						compLbl := "Make component"
						if firstReusable {
							compLbl = "Detach component"
						}
						if ActionBtn(c, "inspector-component", compLbl, "component", "").Clicked() {
							var nextReusable any
							if !firstReusable {
								nextReusable = true
							}
							applyUpdate(map[string]any{"reusable": nextReusable}, "")
						}
					}
					isHidden := false
					if en, ok := first.nd.Node["enabled"].(bool); ok && !en {
						isHidden = true
					}
					visLbl := "Hide"
					visIcon := "hidden"
					if isHidden {
						visLbl = "Show"
						visIcon = "eye"
					}
					if ActionBtn(c, "inspector-visibility", visLbl, visIcon, "").Clicked() {
						var nextEn any
						if !isHidden {
							nextEn = false
						}
						applyUpdate(map[string]any{"enabled": nextEn}, "")
						a.CanvasHandle.Focus()
					}
				})
			}

			if hasInstanceChild {
				ui.Row(c.Key("layer-inst-hide")).FillWidth().Gap(6).Children(func() {
					if ActionBtn(c, "hide-instance-child", "Hide", "hidden", "").Clicked() {
						applyUpdate(map[string]any{"enabled": false}, "")
						a.Select([]string{})
						a.CanvasHandle.Focus()
					}
				})
			} else {
				ui.Row(c.Key("layer-dup-del")).FillWidth().Gap(6).Children(func() {
					if ActionBtn(c, "inspector-duplicate", "Duplicate", "copy", "").Clicked() {
						a.DuplicateSelection()
					}
					if ActionBtn(c, "inspector-delete", "Delete", "trash", "danger").Clicked() {
						a.DeleteSelection()
					}
				})
			}

			ui.Row(c.Key("layer-order")).FillWidth().Gap(6).Children(func() {
				if ActionBtn(c, "inspector-forward", "Bring forward", "", "").Clicked() {
					a.reorderSelection("up")
				}
				if ActionBtn(c, "inspector-backward", "Send backward", "", "").Clicked() {
					a.reorderSelection("down")
				}
			})
		})

		// 10. Section: Export
		InspectorSection(c, "sec-export", "Export", nil, func() {
			exportFmt := func(key, label, format string) {
				if ActionBtn(c, key, label, "download", "").Clicked() {
					var files []string
					if err := a.Bridge.Call("export", map[string]any{
						"ids":    a.State.Selection,
						"format": format,
						"path":   "",
						"opts":   map[string]any{"scale": 2},
					}, &files); err != nil {
						a.ShowToast(err.Error(), true)
						return
					}
					if len(files) > 0 {
						a.ShowToast("Exported "+strings.Join(files, ", "), false)
					}
				}
			}
			ui.Row(c.Key("export-row-1")).FillWidth().Gap(6).Children(func() {
				exportFmt("export-png", "PNG", "png")
				exportFmt("export-jpeg", "JPEG", "jpeg")
				exportFmt("export-webp", "WebP", "webp")
			})
			ui.Row(c.Key("export-row-2")).FillWidth().Gap(6).Children(func() {
				exportFmt("export-svg", "SVG", "svg")
				exportFmt("export-html", "HTML", "html-css")
			})
		})
	})
}

func numericPropAcross(infos []selectedNodeEntry, fn func(selectedNodeEntry) float64) (float64, bool) {
	if len(infos) == 0 {
		return 0, false
	}
	first := math.Round(fn(infos[0])*100) / 100
	for _, inf := range infos[1:] {
		v := math.Round(fn(inf)*100) / 100
		if math.Abs(v-first) > 1e-6 {
			return 0, true
		}
	}
	return first, false
}

func rawPropAcross(infos []selectedNodeEntry, prop string) (any, bool) {
	if len(infos) == 0 {
		return nil, false
	}
	first := infos[0].nd.Node[prop]
	s0 := formatValueString(first)
	for _, inf := range infos[1:] {
		if formatValueString(inf.nd.Node[prop]) != s0 {
			return nil, true
		}
	}
	return first, false
}

func allPropPrefix(infos []selectedNodeEntry, prop, prefix string) bool {
	if len(infos) == 0 {
		return false
	}
	for _, inf := range infos {
		s, ok := inf.nd.Node[prop].(string)
		if !ok || !strings.HasPrefix(s, prefix) {
			return false
		}
	}
	return true
}
