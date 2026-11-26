package engine

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/layout"
	"github.com/naddrzz/nalarupa/core-go/resolve"
	"github.com/naddrzz/nalarupa/core-go/svg"
)

type cachedTopBound struct {
	x, y, w, h float64
	varsHash   uint64
}

func isUntouchedTemplateStarter(e *Engine, kids []jsonv.Value, ch jsonv.Value) bool {
	if e.Dirty() || e.Doc == nil || e.Doc.Path != "" || len(kids) != 1 {
		return false
	}
	if !jsonv.StrIs(jsonv.Get(ch, "type"), "frame") || jsonv.IsTrue(jsonv.Get(ch, "reusable")) {
		return false
	}
	ck := jsonv.Arr(jsonv.Get(ch, "children"))
	if ck != nil && len(ck.Items) > 0 {
		return false
	}
	id := jsonv.StrOr(jsonv.Get(ch, "id"))
	return (id == "frm01" || id == "mob01") && jsonv.StrIs(jsonv.Get(ch, "fill"), "#FFFFFF")
}

// BeginTurnSnapshot captures existing top-level node IDs and document variables at the start of an agent turn,
// protecting finished screens from accidental variable clobbering or unintended edits when new screens are added.
func (e *Engine) BeginTurnSnapshot() {
	e.Agent.PreTurnTopIDs = map[string]bool{}
	e.Agent.ProtectedVars = nil
	e.Agent.TurnEditedPreTop = map[string]bool{}
	e.Agent.TurnNewTopIDs = nil
	e.Agent.TurnScreenTeam = map[string]string{}
	e.Agent.FixedTopBounds = nil
	if e.Doc == nil {
		e.Agent.PreTurnVars = nil
		return
	}
	kids := e.Doc.Children()
	for _, ch := range kids {
		id := jsonv.StrOr(jsonv.Get(ch, "id"))
		if id == "" {
			continue
		}
		// Exclude only the untouched blank/mobile template starter frame so Turn 1 can edit or replace it;
		// finished screens (including empty background-only frames using $ground) remain protected.
		if isUntouchedTemplateStarter(e, kids, ch) {
			continue
		}
		e.Agent.PreTurnTopIDs[id] = true
	}
	if rawVars, _ := e.Doc.Root.Get("variables"); jsonv.Obj(rawVars) != nil {
		e.Agent.PreTurnVars = jsonv.Obj(jsonv.Clone(rawVars))
	} else {
		e.Agent.PreTurnVars = jsonv.NewObject()
	}
}

// SetAgentContainerScope restricts child agentID to editing only inside containerIDs (plus creating reusable components).
func (e *Engine) SetAgentContainerScope(agentID string, containerIDs []string) {
	if agentID == "" {
		return
	}
	if e.Agent.ChildScopes == nil {
		e.Agent.ChildScopes = map[string]map[string]bool{}
	}
	if len(containerIDs) == 0 {
		delete(e.Agent.ChildScopes, agentID)
		return
	}
	m := make(map[string]bool, len(containerIDs))
	for _, id := range containerIDs {
		if id != "" {
			m[id] = true
		}
	}
	e.Agent.ChildScopes[agentID] = m
}

func scopesOverlap(d *doc.Doc, a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	ra, rb := rootTargetID(a), rootTargetID(b)
	if ra == rb {
		return true
	}
	return isNodeWithinScope(d, map[string]bool{ra: true}, rb) ||
		isNodeWithinScope(d, map[string]bool{rb: true}, ra)
}

// ValidateChildScopes checks that new spawn container assignments are disjoint from each other and from active child scopes.
func (e *Engine) ValidateChildScopes(containers []string) error {
	if e.Doc == nil || len(containers) == 0 {
		return nil
	}
	for i := 0; i < len(containers); i++ {
		ci := containers[i]
		if ci == "" {
			continue
		}
		for j := i + 1; j < len(containers); j++ {
			if scopesOverlap(e.Doc, ci, containers[j]) {
				return fmt.Errorf("overlapping container assignment %q and %q: each spawned designer must own a disjoint frame", ci, containers[j])
			}
		}
		for _, activeScope := range e.Agent.ChildScopes {
			for existing := range activeScope {
				if scopesOverlap(e.Doc, ci, existing) {
					return fmt.Errorf("container %q overlaps active spawned designer scope %q", ci, existing)
				}
			}
		}
	}
	return nil
}

func isNodeWithinScope(d *doc.Doc, scope map[string]bool, targetID string) bool {
	if d == nil || targetID == "" {
		return false
	}
	cur := rootTargetID(targetID)
	for range doc.MaxLevels + 2 {
		if scope[cur] {
			return true
		}
		p, _, ok := d.ParentOf(cur)
		if !ok || p == "" || p == doc.Root {
			return false
		}
		cur = p
	}
	return false
}

func topAncestorID(d *doc.Doc, targetID string) string {
	if d == nil || targetID == "" || targetID == doc.Root {
		return ""
	}
	idx := d.TopIndex(rootTargetID(targetID))
	if idx < 0 {
		return ""
	}
	kids := d.Children()
	if idx >= len(kids) {
		return ""
	}
	return jsonv.StrOr(jsonv.Get(kids[idx], "id"))
}

// CheckEditTarget validates whether the currently executing agent may mutate targetID without side effects.
func (e *Engine) CheckEditTarget(op, targetID string, isReusableRoot bool) error {
	agentID := e.currentExecAgent
	d := e.Doc
	if agentID != "" && len(e.Agent.ChildScopes) > 0 {
		if scope := e.Agent.ChildScopes[agentID]; len(scope) > 0 {
			switch op {
			case "insert", "copy":
				if targetID == doc.Root {
					if !isReusableRoot {
						return errors.New("spawned designer may only insert inside its assigned container frame(s)")
					}
					return nil
				}
				if !isNodeWithinScope(d, scope, targetID) {
					return errors.New("spawned designer may only insert inside its assigned container frame(s)")
				}
				return nil
			default:
				if targetID == doc.Root || !isNodeWithinScope(d, scope, targetID) {
					return errors.New("spawned designer may only edit inside its assigned container frame(s)")
				}
				return nil
			}
		}
	}

	if d != nil && e.Agent.Running && e.Agent.IsVariationTurn && len(e.Agent.PreTurnTopIDs) > 0 &&
		(op == "update" || op == "replace" || op == "delete" || op == "move" || op == "insert" || op == "copy") {
		topID := topAncestorID(d, targetID)
		if topID != "" && e.Agent.PreTurnTopIDs[topID] {
			return errors.New("do not modify earlier finished screens during variation generation; build inside the new frame")
		}
	}
	return nil
}

// RecordEditTarget marks targetID's pre-turn top-level ancestor as intentionally edited after a mutation succeeds.
func (e *Engine) RecordEditTarget(op, targetID string) {
	if e.Doc == nil || targetID == "" || targetID == doc.Root {
		return
	}
	topID := topAncestorID(e.Doc, targetID)
	if topID == "" {
		return
	}
	if len(e.Agent.FixedTopBounds) > 0 {
		delete(e.Agent.FixedTopBounds, topID)
	}
	if len(e.Agent.PreTurnTopIDs) > 0 && e.Agent.PreTurnTopIDs[topID] &&
		(op == "update" || op == "replace" || op == "delete" || op == "move" || op == "insert" || op == "copy") {
		if e.Agent.TurnEditedPreTop == nil {
			e.Agent.TurnEditedPreTop = map[string]bool{}
		}
		e.Agent.TurnEditedPreTop[topID] = true
	}
}

// FilterSetVars prevents child agents from overwriting shared variables and prevents replace=true from dropping
// pre-turn or pinned variables while preserving in-turn updates and clearing unprotected temporary keys.
func (e *Engine) FilterSetVars(vars jsonv.Value, replace bool) (jsonv.Value, bool) {
	vs := jsonv.Obj(vars)
	if vs == nil || e.Doc == nil {
		return vars, replace
	}
	agentID := e.currentExecAgent
	if agentID != "" && len(e.Agent.ChildScopes[agentID]) > 0 {
		rawExisting, _ := e.Doc.Root.Get("variables")
		existing := jsonv.Obj(rawExisting)
		filtered := jsonv.NewObject()
		vs.Each(func(k string, v jsonv.Value) {
			if _, pinned := e.Agent.ProtectedVars[k]; pinned {
				return
			}
			if existing == nil || !existing.Has(k) {
				filtered.Set(k, jsonv.Clone(v))
			}
		})
		return filtered, false
	}
	hasPreTurn := e.Agent.PreTurnVars != nil && e.Agent.PreTurnVars.Len() > 0
	hasPinned := len(e.Agent.ProtectedVars) > 0
	if replace && (hasPreTurn || hasPinned) {
		rawCur, _ := e.Doc.Root.Get("variables")
		curVars := jsonv.Obj(rawCur)
		merged := jsonv.NewObject()
		if hasPreTurn {
			e.Agent.PreTurnVars.Each(func(k string, origDef jsonv.Value) {
				if curVars != nil {
					if curDef, ok := curVars.Get(k); ok {
						merged.Set(k, jsonv.Clone(curDef))
						return
					}
				}
				merged.Set(k, jsonv.Clone(origDef))
			})
		}
		vs.Each(func(k string, v jsonv.Value) {
			merged.Set(k, jsonv.Clone(v))
		})
		for pin, def := range e.Agent.ProtectedVars {
			merged.Set(pin, jsonv.Clone(def))
		}
		return merged, true
	}
	if !replace && hasPinned {
		overwritesPinned := false
		for pin := range e.Agent.ProtectedVars {
			if vs.Has(pin) {
				overwritesPinned = true
				break
			}
		}
		if overwritesPinned {
			cloned := jsonv.Obj(jsonv.Clone(vs))
			for pin, def := range e.Agent.ProtectedVars {
				if cloned.Has(pin) {
					cloned.Set(pin, jsonv.Clone(def))
				}
			}
			return cloned, false
		}
	}
	return vars, replace
}

// AfterRootInsert runs after Insert/Copy into document root: records team screen ownership, deconflicts overlaps, and isolates pre-turn variables.
func (e *Engine) AfterRootInsert(id string) {
	if e.Doc == nil || id == "" {
		return
	}
	if !e.Agent.PreTurnTopIDs[id] && !slices.Contains(e.Agent.TurnNewTopIDs, id) {
		e.Agent.TurnNewTopIDs = append(e.Agent.TurnNewTopIDs, id)
		if n := e.Doc.Find(id); n != nil && !jsonv.IsTrue(jsonv.Get(n, "reusable")) {
			e.assignScreenTeamOwner(id)
		}
	}
	if e.Agent.Running {
		e.DeconflictTopLevelNodes(e.Doc)
	}
	e.reconcilePreTurnScreenIsolation(e.Doc)
}

// AfterSetVars reconciles variable isolation on pre-existing screens if new screens also exist in this turn.
func (e *Engine) AfterSetVars() {
	if e.Doc == nil {
		return
	}
	e.reconcilePreTurnScreenIsolation(e.Doc)
}

func (e *Engine) assignScreenTeamOwner(screenID string) {
	if e.Agent.Parallel <= 1 || len(e.Agent.Team) == 0 {
		return
	}
	if e.Agent.TurnScreenTeam == nil {
		e.Agent.TurnScreenTeam = map[string]string{}
	}
	if _, exists := e.Agent.TurnScreenTeam[screenID]; exists {
		return
	}
	idx := len(e.Agent.TurnScreenTeam)
	if idx == 0 {
		e.Agent.TurnScreenTeam[screenID] = e.Agent.Main
		return
	}
	teamIdx := (idx - 1) % len(e.Agent.Team)
	e.Agent.TurnScreenTeam[screenID] = e.Agent.Team[teamIdx].ID
}

func isNonVarNodeKey(k string) bool {
	switch k {
	case "id", "name", "type", "ref", "theme", "slot", "version", "imports":
		return true
	}
	return false
}

func valueUsesVar(v jsonv.Value, dollarName string) bool {
	switch x := v.(type) {
	case string:
		return x == dollarName
	case *jsonv.Array:
		for _, it := range x.Items {
			if valueUsesVar(it, dollarName) {
				return true
			}
		}
	case *jsonv.Object:
		found := false
		x.Each(func(_ string, val jsonv.Value) {
			if !found && valueUsesVar(val, dollarName) {
				found = true
			}
		})
		return found
	}
	return false
}

func nodeUsesVar(node jsonv.Value, dollarName string) bool {
	obj := jsonv.Obj(node)
	if obj == nil {
		return false
	}
	found := false
	obj.Each(func(k string, val jsonv.Value) {
		if found || isNonVarNodeKey(k) {
			return
		}
		switch k {
		case "children":
			for _, kid := range jsonv.Items(val) {
				if nodeUsesVar(kid, dollarName) {
					found = true
					return
				}
			}
		case "descendants":
			if dObj := jsonv.Obj(val); dObj != nil {
				dObj.Each(func(_ string, over jsonv.Value) {
					if !found && nodeUsesVar(over, dollarName) {
						found = true
					}
				})
			}
		default:
			if valueUsesVar(val, dollarName) {
				found = true
			}
		}
	})
	return found
}

func rewriteVarRefsInValue(v jsonv.Value, fromDollar, toDollar string) jsonv.Value {
	switch x := v.(type) {
	case string:
		if x == fromDollar {
			return toDollar
		}
		return v
	case *jsonv.Array:
		for i, it := range x.Items {
			x.Items[i] = rewriteVarRefsInValue(it, fromDollar, toDollar)
		}
		return x
	case *jsonv.Object:
		for _, k := range x.Keys() {
			ref := x.Ref(k)
			*ref = rewriteVarRefsInValue(*ref, fromDollar, toDollar)
		}
		return x
	}
	return v
}

func rewriteVarRefsInNode(node jsonv.Value, fromDollar, toDollar string) {
	obj := jsonv.Obj(node)
	if obj == nil {
		return
	}
	for _, k := range obj.Keys() {
		if isNonVarNodeKey(k) {
			continue
		}
		switch k {
		case "children":
			for _, kid := range jsonv.Items(*obj.Ref(k)) {
				rewriteVarRefsInNode(kid, fromDollar, toDollar)
			}
		case "descendants":
			if dObj := jsonv.Obj(*obj.Ref(k)); dObj != nil {
				for _, dk := range dObj.Keys() {
					rewriteVarRefsInNode(*dObj.Ref(dk), fromDollar, toDollar)
				}
			}
		default:
			ref := obj.Ref(k)
			*ref = rewriteVarRefsInValue(*ref, fromDollar, toDollar)
		}
	}
}

func substVarDefValue(v jsonv.Value, vars *jsonv.Object, depth int) jsonv.Value {
	if depth > 16 || vars == nil {
		return v
	}
	switch x := v.(type) {
	case string:
		if strings.HasPrefix(x, "$") {
			if targetDef, ok := vars.Get(x[1:]); ok {
				if tv := jsonv.Get(targetDef, "value"); tv != nil && jsonv.Arr(tv) == nil {
					return substVarDefValue(jsonv.Clone(tv), vars, depth+1)
				}
			}
		}
		return x
	case *jsonv.Array:
		out := make([]jsonv.Value, len(x.Items))
		for i, it := range x.Items {
			out[i] = substVarDefValue(it, vars, depth)
		}
		return &jsonv.Array{Items: out}
	case *jsonv.Object:
		out := jsonv.NewObject()
		x.Each(func(k string, it jsonv.Value) {
			if k == "theme" {
				out.Set(k, jsonv.Clone(it))
				return
			}
			out.Set(k, substVarDefValue(it, vars, depth))
		})
		return out
	}
	return v
}

func resolveVarDefInTable(vars *jsonv.Object, def jsonv.Value) jsonv.Value {
	cloned := jsonv.Clone(def)
	if obj := jsonv.Obj(cloned); obj != nil {
		if val, ok := obj.Get("value"); ok {
			obj.Set("value", substVarDefValue(val, vars, 0))
		}
		return obj
	}
	return substVarDefValue(cloned, vars, 0)
}

func collectNodeRefTargets(node jsonv.Value, out map[string]bool) {
	doc.Walk([]jsonv.Value{node}, func(n jsonv.Value) {
		if jsonv.StrIs(jsonv.Get(n, "type"), "ref") {
			ref := jsonv.StrOr(jsonv.Get(n, "ref"))
			if ref != "" && !strings.Contains(ref, ":") {
				out[ref] = true
			}
		}
	})
}

// reconcilePreTurnScreenIsolation pins original variable values onto untouched pre-turn screens (and only the
// untouched pre-turn reusable components transitively instantiated by those screens) whenever a turn adds new
// top-level nodes and redefines variables directly or transitively.
func (e *Engine) reconcilePreTurnScreenIsolation(d *doc.Doc) {
	if d == nil || e.Agent.PreTurnVars == nil || e.Agent.PreTurnVars.Len() == 0 || len(e.Agent.PreTurnTopIDs) == 0 {
		return
	}
	rawCurVars, _ := d.Root.Get("variables")
	curVars := jsonv.Obj(rawCurVars)
	if curVars == nil {
		curVars = jsonv.NewObject()
		d.Root.Set("variables", curVars)
	}
	for pin, def := range e.Agent.ProtectedVars {
		curVars.Set(pin, jsonv.Clone(def))
	}

	hasNewTop := e.Agent.IsVariationTurn
	if !hasNewTop {
		for _, ch := range d.Children() {
			cid := jsonv.StrOr(jsonv.Get(ch, "id"))
			if cid != "" && !e.Agent.PreTurnTopIDs[cid] {
				hasNewTop = true
				break
			}
		}
	}
	if !hasNewTop {
		return
	}

	type changedVar struct {
		name        string
		origResolved jsonv.Value
	}
	var changed []changedVar
	e.Agent.PreTurnVars.Each(func(k string, origDef jsonv.Value) {
		origRes := resolveVarDefInTable(e.Agent.PreTurnVars, origDef)
		nowDef, ok := curVars.Get(k)
		if !ok {
			changed = append(changed, changedVar{name: k, origResolved: origRes})
			return
		}
		nowRes := resolveVarDefInTable(curVars, nowDef)
		if !jsonv.Equal(origRes, nowRes) {
			changed = append(changed, changedVar{name: k, origResolved: origRes})
		}
	})
	if len(changed) == 0 {
		return
	}

	// Build protected dependency graph: untouched pre-turn non-reusable nodes + transitively referenced untouched reusable components.
	topByID := map[string]jsonv.Value{}
	reusableByID := map[string]jsonv.Value{}
	doc.Walk(d.Children(), func(n jsonv.Value) {
		if jsonv.IsTrue(jsonv.Get(n, "reusable")) {
			if id := jsonv.StrOr(jsonv.Get(n, "id")); id != "" {
				reusableByID[id] = n
			}
		}
	})
	protectedIDs := map[string]bool{}
	var queue []jsonv.Value
	for _, ch := range d.Children() {
		cid := jsonv.StrOr(jsonv.Get(ch, "id"))
		if cid == "" {
			continue
		}
		topByID[cid] = ch
		if e.Agent.PreTurnTopIDs[cid] && !e.Agent.TurnEditedPreTop[cid] && !jsonv.IsTrue(jsonv.Get(ch, "reusable")) {
			protectedIDs[cid] = true
			queue = append(queue, ch)
		}
	}
	if len(protectedIDs) == 0 {
		return
	}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		refs := map[string]bool{}
		collectNodeRefTargets(cur, refs)
		for refID := range refs {
			topComp := topAncestorID(d, refID)
			if topComp == "" || !e.Agent.PreTurnTopIDs[topComp] || e.Agent.TurnEditedPreTop[topComp] || protectedIDs[topComp] {
				continue
			}
			if compNode, ok := topByID[topComp]; ok && jsonv.IsTrue(jsonv.Get(compNode, "reusable")) {
				protectedIDs[topComp] = true
				queue = append(queue, compNode)
			} else if subComp, ok := reusableByID[refID]; ok && !protectedIDs[refID] {
				protectedIDs[refID] = true
				queue = append(queue, subComp)
			}
		}
	}

	for _, cv := range changed {
		dollarOld := "$" + cv.name
		used := false
		for _, ch := range d.Children() {
			cid := jsonv.StrOr(jsonv.Get(ch, "id"))
			if protectedIDs[cid] && nodeUsesVar(ch, dollarOld) {
				used = true
				break
			}
		}
		if !used {
			continue
		}
		pinName := "preserved_" + cv.name
		for suffix := 2; ; suffix++ {
			if existing, exists := curVars.Get(pinName); !exists || jsonv.Equal(existing, cv.origResolved) {
				break
			}
			pinName = fmt.Sprintf("preserved_%d_%s", suffix, cv.name)
		}
		curVars.Set(pinName, jsonv.Clone(cv.origResolved))
		if e.Agent.ProtectedVars == nil {
			e.Agent.ProtectedVars = map[string]jsonv.Value{}
		}
		e.Agent.ProtectedVars[pinName] = jsonv.Clone(cv.origResolved)
		dollarPin := "$" + pinName
		for _, ch := range d.Children() {
			cid := jsonv.StrOr(jsonv.Get(ch, "id"))
			if protectedIDs[cid] {
				rewriteVarRefsInNode(ch, dollarOld, dollarPin)
			}
		}
	}
}

type topBoxInfo struct {
	idx      int
	id       string
	obj      *jsonv.Object
	isFrame  bool
	reusable bool
	fixed    bool
	x, y     float64
	w, h     float64
}

func rectsOverlapWithGap(ax, ay, aw, ah, bx, by, bw, bh, gap float64) bool {
	return ax < bx+bw+gap && ax+aw+gap > bx && ay < by+bh+gap && ay+ah+gap > by
}

const (
	screenGridColGap = 160.0
	screenGridRowGap = 260.0
)

func screenGridRowLimits(w float64) (maxCols int, maxSpan float64) {
	if w < 700.0 {
		return 6, 3800.0
	}
	return 4, 6500.0
}

type screenRowBand struct {
	topY     float64
	bottomY  float64
	minX     float64
	maxRight float64
	count    int
}

func groupScreenRows(screens []topBoxInfo) []screenRowBand {
	if len(screens) == 0 {
		return nil
	}
	sorted := append([]topBoxInfo(nil), screens...)
	slices.SortFunc(sorted, func(a, b topBoxInfo) int {
		if a.y < b.y {
			return -1
		}
		if a.y > b.y {
			return 1
		}
		if a.x < b.x {
			return -1
		}
		if a.x > b.x {
			return 1
		}
		return 0
	})
	var rows []screenRowBand
	for _, s := range sorted {
		matched := -1
		for ri := range rows {
			tol := max(220.0, 0.35*s.h)
			if math.Abs(s.y-rows[ri].topY) <= tol {
				matched = ri
				break
			}
		}
		if matched < 0 {
			rows = append(rows, screenRowBand{
				topY:     s.y,
				bottomY:  s.y + s.h,
				minX:     s.x,
				maxRight: s.x + s.w,
				count:    1,
			})
			continue
		}
		rw := &rows[matched]
		rw.bottomY = max(rw.bottomY, s.y+s.h)
		rw.minX = min(rw.minX, s.x)
		rw.maxRight = max(rw.maxRight, s.x+s.w)
		rw.count++
	}
	return rows
}

// FindScreenGridSpot computes the next aligned artboard position (ignoring reusable components above screens
// so their negative Y never pulls new screens off-baseline, and wrapping cleanly to a new row below once the
// active row reaches maxCols or maxSpan).
func (e *Engine) FindScreenGridSpot(w, h, pad float64) (float64, float64, bool) {
	if e.Doc == nil {
		return 0, 0, false
	}
	kids := e.Doc.Children()
	if len(kids) == 0 {
		return 0, 0, false
	}
	hasReusable := false
	var screens []topBoxInfo
	var allBoxes []topBoxInfo
	for i, ch := range kids {
		id := jsonv.StrOr(jsonv.Get(ch, "id"))
		isFrame := jsonv.StrIs(jsonv.Get(ch, "type"), "frame")
		reusable := jsonv.IsTrue(jsonv.Get(ch, "reusable"))
		if reusable {
			hasReusable = true
		}
		x, _ := resolve.Num(jsonv.Get(ch, "x"))
		y, _ := resolve.Num(jsonv.Get(ch, "y"))
		bw, _ := resolve.Num(jsonv.Get(ch, "width"))
		bh, _ := resolve.Num(jsonv.Get(ch, "height"))
		if f := e.Renderer.Find(id); f != nil {
			x, y, bw, bh = f.X, f.Y, f.W, f.H
		}
		tb := topBoxInfo{idx: i, id: id, isFrame: isFrame, reusable: reusable, x: x, y: y, w: max(bw, 1), h: max(bh, 1)}
		allBoxes = append(allBoxes, tb)
		if isFrame && !reusable {
			screens = append(screens, tb)
		}
	}
	if len(screens) == 0 {
		return 0, 0, false
	}
	if !e.Agent.Running && !hasReusable && len(screens) < 4 {
		return 0, 0, false
	}
	rows := groupScreenRows(screens)
	if len(rows) == 0 {
		return 0, 0, false
	}
	originX := rows[0].minX
	last := rows[len(rows)-1]
	colGap := pad
	if e.Agent.Running && colGap < screenGridColGap {
		colGap = screenGridColGap
	}
	maxCols, maxSpan := screenGridRowLimits(max(w, 64))
	candX := last.maxRight + colGap
	candY := last.topY
	if last.count >= maxCols || (last.count >= 2 && candX+w-originX > maxSpan) {
		candX = originX
		candY = last.bottomY + max(pad, screenGridRowGap)
	}
	foundFree := false
	for range 300 {
		busy := false
		for _, b := range allBoxes {
			if rectsOverlapWithGap(candX, candY, w, h, b.x, b.y, b.w, b.h, max(pad, 24.0)) {
				busy = true
				candX = max(candX+40.0, b.x+b.w+colGap)
				break
			}
		}
		if !busy {
			foundFree = true
			break
		}
	}
	if !foundFree {
		return 0, 0, false
	}
	return math.Round(candX), math.Round(candY), true
}

func docVarsAndReusableHash(d *doc.Doc) uint64 {
	var b strings.Builder
	if v, ok := d.Root.Get("variables"); ok {
		b.Write(jsonv.Marshal(v))
	}
	if t, ok := d.Root.Get("themes"); ok {
		b.Write(jsonv.Marshal(t))
	}
	for _, ch := range d.Children() {
		if jsonv.IsTrue(jsonv.Get(ch, "reusable")) {
			b.Write(jsonv.Marshal(ch))
		}
	}
	return svg.FxHash(b.String())
}

// DeconflictTopLevelNodes ensures newly created top-level frames and reusable components never collide,
// aligns sibling screens flush on the same row Y baseline, and wraps excess screens into clean rows below.
func (e *Engine) DeconflictTopLevelNodes(d *doc.Doc) {
	if !e.Agent.Running || d == nil {
		return
	}
	kids := d.Children()
	if len(kids) <= 1 {
		return
	}
	isPreviewDoc := e.Preview != nil && d == e.Preview
	varsHash := docVarsAndReusableHash(d)
	var resolver *resolve.Resolver
	getResolver := func() *resolve.Resolver {
		if resolver == nil {
			resolver = resolve.New(d.Root, e.Libs)
		}
		return resolver
	}
	infos := make([]topBoxInfo, 0, len(kids))
	hasPreTurn := len(e.Agent.PreTurnTopIDs) > 0
	seenFirstScreen := false

	for i, ch := range kids {
		obj := jsonv.Obj(ch)
		if obj == nil {
			continue
		}
		id := jsonv.StrOr(jsonv.Get(ch, "id"))
		isFrame := jsonv.StrIs(jsonv.Get(ch, "type"), "frame")
		reusable := jsonv.IsTrue(jsonv.Get(ch, "reusable"))
		fixed := false
		if hasPreTurn {
			fixed = e.Agent.PreTurnTopIDs[id]
		} else if isFrame && !reusable && !seenFirstScreen {
			fixed = true
			seenFirstScreen = true
		}

		var x, y, w, h float64
		usedCache := false
		if fixed && !e.Agent.TurnEditedPreTop[id] && e.Agent.FixedTopBounds != nil {
			if cb, ok := e.Agent.FixedTopBounds[id]; ok && cb.varsHash == varsHash {
				x, y, w, h = cb.x, cb.y, cb.w, cb.h
				usedCache = true
			}
		}
		if !usedCache {
			x, _ = resolve.Num(jsonv.Get(ch, "x"))
			y, _ = resolve.Num(jsonv.Get(ch, "y"))
			w, _ = resolve.Num(jsonv.Get(ch, "width"))
			h, _ = resolve.Num(jsonv.Get(ch, "height"))
			if rn := getResolver().Resolve(ch); rn != nil {
				layout.Layout(rn, e.Fonts)
				x, y, w, h = rn.X, rn.Y, rn.W, rn.H
			}
			w = max(w, 64.0)
			h = max(h, 40.0)
			if !isPreviewDoc && fixed && !e.Agent.TurnEditedPreTop[id] && id != "" {
				if e.Agent.FixedTopBounds == nil {
					e.Agent.FixedTopBounds = map[string]cachedTopBound{}
				}
				e.Agent.FixedTopBounds[id] = cachedTopBound{x: x, y: y, w: w, h: h, varsHash: varsHash}
			}
		}
		infos = append(infos, topBoxInfo{
			idx:      i,
			id:       id,
			obj:      obj,
			isFrame:  isFrame,
			reusable: reusable,
			fixed:    fixed,
			x:        x,
			y:        y,
			w:        w,
			h:        h,
		})
	}

	placed := make([]topBoxInfo, 0, len(infos))
	var fixedScreens []topBoxInfo
	newScreenCount := 0
	firstNewScreenW := 1440.0
	for _, inf := range infos {
		if inf.fixed {
			placed = append(placed, inf)
			if inf.isFrame && !inf.reusable {
				fixedScreens = append(fixedScreens, inf)
			}
		} else if inf.isFrame && !inf.reusable {
			if newScreenCount == 0 {
				firstNewScreenW = inf.w
			}
			newScreenCount++
		}
	}

	collidesWithPlaced := func(x, y, w, h float64, reusable bool, gap float64) (topBoxInfo, bool) {
		for _, p := range placed {
			g := gap
			if reusable != p.reusable {
				g = max(g, 240.0)
			}
			if rectsOverlapWithGap(x, y, w, h, p.x, p.y, p.w, p.h, g) {
				return p, true
			}
		}
		return topBoxInfo{}, false
	}

	rows := groupScreenRows(fixedScreens)
	hasScreen := len(rows) > 0
	minScreenY := 0.0
	maxScreenBottom := 0.0
	gridOriginX := 0.0
	var curRow screenRowBand
	if hasScreen {
		gridOriginX = rows[0].minX
		minScreenY = rows[0].topY
		for _, rw := range rows {
			minScreenY = min(minScreenY, rw.topY)
			maxScreenBottom = max(maxScreenBottom, rw.bottomY)
		}
		curRow = rows[len(rows)-1]
		// Conditional batch wrap: if the bottom-most row is already full (or nearly full with >=3 screens and
		// a new multi-screen batch wouldn't fit on that row), start the new batch cleanly on a fresh row below!
		maxCols, _ := screenGridRowLimits(firstNewScreenW)
		if curRow.count >= maxCols || (curRow.count >= 3 && newScreenCount >= 2 && curRow.count+newScreenCount > maxCols) {
			nextTop := curRow.bottomY + screenGridRowGap
			curRow = screenRowBand{
				topY:     nextTop,
				bottomY:  nextTop,
				minX:     gridOriginX,
				maxRight: gridOriginX - screenGridColGap,
				count:    0,
			}
		}
	}

	// Pass 1: Place non-reusable screen frames on flush Y row baselines, wrapping conditionally to new rows below.
	for i := range infos {
		inf := &infos[i]
		if inf.fixed || inf.reusable {
			continue
		}
		if !inf.isFrame {
			candX, candY := inf.x, inf.y
			for step := 0; step < 300; step++ {
				blocker, bad := collidesWithPlaced(candX, candY, inf.w, inf.h, false, 40.0)
				if !bad {
					break
				}
				candX = max(candX+40.0, blocker.x+blocker.w+80.0)
			}
			rx, ry := math.Round(candX), math.Round(candY)
			inf.x, inf.y = rx, ry
			inf.obj.Set("x", jsonv.Float(rx))
			inf.obj.Set("y", jsonv.Float(ry))
			placed = append(placed, *inf)
			continue
		}
		if !hasScreen {
			candX, candY := inf.x, inf.y
			for step := 0; step < 300; step++ {
				blocker, bad := collidesWithPlaced(candX, candY, inf.w, inf.h, false, 80.0)
				if !bad {
					break
				}
				// Move below any fixed component occupying the requested origin while keeping X aligned.
				if blocker.reusable {
					candY = max(candY+40.0, blocker.y+blocker.h+280.0)
				} else {
					candX = max(candX+40.0, blocker.x+blocker.w+screenGridColGap)
				}
			}
			rx, ry := math.Round(candX), math.Round(candY)
			inf.x, inf.y = rx, ry
			inf.obj.Set("x", jsonv.Float(rx))
			inf.obj.Set("y", jsonv.Float(ry))
			gridOriginX = rx
			minScreenY = ry
			maxScreenBottom = ry + inf.h
			curRow = screenRowBand{
				topY:     ry,
				bottomY:  ry + inf.h,
				minX:     rx,
				maxRight: rx + inf.w,
				count:    1,
			}
			hasScreen = true
			placed = append(placed, *inf)
			continue
		}

		maxCols, maxSpan := screenGridRowLimits(inf.w)
		// Respect intentional explicit vertical stacking below the current row; otherwise snap Y flush to curRow.topY!
		intentionalNewRow := curRow.count > 0 && inf.y >= curRow.bottomY+120.0 && inf.x <= gridOriginX+inf.w*0.5
		candX := inf.x
		if curRow.count == 0 {
			candX = gridOriginX
		} else if candX < curRow.maxRight+40.0 || candX > curRow.maxRight+360.0 {
			candX = curRow.maxRight + screenGridColGap
		}
		candY := curRow.topY

		needWrap := intentionalNewRow ||
			curRow.count >= maxCols ||
			(curRow.count >= 2 && candX+inf.w-gridOriginX > maxSpan)
		if needWrap {
			nextTop := curRow.bottomY + screenGridRowGap
			if intentionalNewRow && inf.y > nextTop {
				nextTop = inf.y
			}
			curRow = screenRowBand{
				topY:     nextTop,
				bottomY:  nextTop,
				minX:     gridOriginX,
				maxRight: gridOriginX - screenGridColGap,
				count:    0,
			}
			candX = gridOriginX
			candY = curRow.topY
		}

		for step := 0; step < 300; step++ {
			blocker, bad := collidesWithPlaced(candX, candY, inf.w, inf.h, false, 80.0)
			if !bad {
				break
			}
			candX = max(candX+40.0, blocker.x+blocker.w+screenGridColGap)
			if curRow.count >= 1 && candX+inf.w-gridOriginX > maxSpan {
				nextTop := max(curRow.bottomY, blocker.y+blocker.h) + screenGridRowGap
				curRow = screenRowBand{
					topY:     nextTop,
					bottomY:  nextTop,
					minX:     gridOriginX,
					maxRight: gridOriginX - screenGridColGap,
					count:    0,
				}
				candX = gridOriginX
				candY = curRow.topY
			}
		}

		rx, ry := math.Round(candX), math.Round(candY)
		inf.x, inf.y = rx, ry
		inf.obj.Set("x", jsonv.Float(rx))
		inf.obj.Set("y", jsonv.Float(ry))
		curRow.bottomY = max(curRow.bottomY, ry+inf.h)
		if curRow.count == 0 {
			curRow.minX = rx
		} else {
			curRow.minX = min(curRow.minX, rx)
		}
		curRow.maxRight = max(curRow.maxRight, rx+inf.w)
		curRow.count++
		minScreenY = min(minScreenY, ry)
		maxScreenBottom = max(maxScreenBottom, ry+inf.h)
		placed = append(placed, *inf)
	}

	// Pass 2: Place reusable components in a clean horizontal Design-System row above minScreenY - 280
	// so component title labels and "Generating: ..." pills never stack or overlap at any zoom level.
	for i := range infos {
		inf := &infos[i]
		if inf.fixed || !inf.reusable {
			continue
		}
		_, hit := collidesWithPlaced(inf.x, inf.y, inf.w, inf.h, true, 32.0)
		if !hit && hasScreen && inf.y+inf.h+240.0 > minScreenY && inf.y < maxScreenBottom+200.0 {
			hit = true
		}
		if !hit {
			placed = append(placed, *inf)
			continue
		}
		candX, candY := inf.x, inf.y
		if hasScreen {
			candY = minScreenY - inf.h - 280.0
		}
		for step := 0; step < 300; step++ {
			blocker, bad := collidesWithPlaced(candX, candY, inf.w, inf.h, true, 120.0)
			if !bad {
				break
			}
			candX = max(candX+40.0, blocker.x+blocker.w+160.0)
			if hasScreen {
				candY = minScreenY - inf.h - 280.0
			}
		}
		rx, ry := math.Round(candX), math.Round(candY)
		inf.x, inf.y = rx, ry
		inf.obj.Set("x", jsonv.Float(rx))
		inf.obj.Set("y", jsonv.Float(ry))
		placed = append(placed, *inf)
	}
}

// EmitWriteActivityWithTeam attributes touched node IDs to their assigned team designer when Parallel > 1
// and the model has not yet spawned separate child processes, so all N team cursors visibly build their screens.
func (e *Engine) EmitWriteActivityWithTeam(defaultAgent string, d *doc.Doc, ids []string, streaming bool) {
	if len(ids) == 0 {
		return
	}
	if !e.Agent.Running || e.Agent.Parallel <= 1 || e.Agent.Spawned > 0 || len(e.Agent.TurnScreenTeam) == 0 || d == nil {
		e.Emit(Object("type", "agent_activity", "agent", defaultAgent, "op", "write", "ids", Strings(ids), "streaming", streaming))
		return
	}
	var order []string
	buckets := map[string][]string{}
	for _, id := range ids {
		owner := defaultAgent
		if topID := topAncestorID(d, id); topID != "" {
			if assigned, ok := e.Agent.TurnScreenTeam[topID]; ok && assigned != "" {
				owner = assigned
			}
		}
		if _, seen := buckets[owner]; !seen {
			order = append(order, owner)
		}
		buckets[owner] = append(buckets[owner], id)
	}
	for _, owner := range order {
		e.Emit(Object("type", "agent_activity", "agent", owner, "op", "write", "ids", Strings(buckets[owner]), "streaming", streaming))
	}
}
