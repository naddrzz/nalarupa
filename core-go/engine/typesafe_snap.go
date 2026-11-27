package engine

import (
	"hash/fnv"
	"slices"
	"strings"
	"time"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/export"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/resolve"
	"github.com/naddrzz/nalarupa/core-go/script"
	"github.com/naddrzz/nalarupa/core-go/typesafe"
	"github.com/naddrzz/nalarupa/core-go/usvg"
)

// MaxTypeSafeRevisions caps automatic visual revision rounds per screen per turn so an agent never loops indefinitely.
const MaxTypeSafeRevisions = 2

// ScreenEvalPlan captures everything needed under Engine.Lock to render and evaluate a single finished/edited
// screen completely off-lock (never rendering the whole multi-screen canvas or re-evaluating untouched screens).
type ScreenEvalPlan struct {
	ID           string
	Name         string
	Hash         uint64
	SourceDoc    *doc.Doc
	Tree         *usvg.Tree
	Scale        float64
	Downloads    []string
	Runs         *script.Runs
	CallDoc      *doc.Doc
	CallLibs     []resolve.Lib
	CachedRevise *typesafe.Decision
}

// CollectReusableComponents indexes all local `reusable: true` nodes in docRoot by ID so callers hashing
// multiple screens on the same document root only walk the document once.
func CollectReusableComponents(docRoot *jsonv.Object) map[string]jsonv.Value {
	reusableByID := map[string]jsonv.Value{}
	if docRoot == nil {
		return reusableByID
	}
	doc.Walk(jsonv.Items(jsonv.Get(docRoot, "children")), func(n jsonv.Value) {
		if jsonv.IsTrue(jsonv.Get(n, "reusable")) {
			if cid := jsonv.StrOr(jsonv.Get(n, "id")); cid != "" {
				reusableByID[cid] = n
			}
		}
	})
	return reusableByID
}

// ScreenVisualHash computes a deterministic 64-bit visual content hash for a single top-level screen frame,
// ignoring its root canvas placement (`x`, `y`) while folding in any local reusable components and resolved
// `$variables` referenced inside its subtree so edits via tokens or components are accurately detected.
func ScreenVisualHash(docRoot *jsonv.Object, screen jsonv.Value) uint64 {
	return ScreenVisualHashWithComponents(docRoot, screen, CollectReusableComponents(docRoot))
}

// ScreenVisualHashWithComponents computes ScreenVisualHash reusing a prebuilt reusable-component index.
func ScreenVisualHashWithComponents(docRoot *jsonv.Object, screen jsonv.Value, reusableByID map[string]jsonv.Value) uint64 {
	sObj := jsonv.Obj(screen)
	if sObj == nil {
		return 0
	}
	var varsObj, themesObj *jsonv.Object
	if docRoot != nil {
		varsObj = jsonv.Obj(jsonv.Get(docRoot, "variables"))
		themesObj = jsonv.Obj(jsonv.Get(docRoot, "themes"))
	}

	h := fnv.New64a()
	writeStr := func(s string) {
		_, _ = h.Write([]byte(s))
		_, _ = h.Write([]byte{0})
	}
	resolvedCache := map[string][]byte{}
	resolveTokenBytes := func(varName string) ([]byte, bool) {
		if varsObj == nil || varName == "" {
			return nil, false
		}
		if cached, ok := resolvedCache[varName]; ok {
			return cached, cached != nil
		}
		def, exists := varsObj.Get(varName)
		if !exists {
			resolvedCache[varName] = nil
			return nil, false
		}
		res := jsonv.Marshal(resolveVarDefInTable(varsObj, themesObj, def))
		resolvedCache[varName] = res
		return res, true
	}

	var writeVal func(v jsonv.Value, depth int)
	writeVal = func(v jsonv.Value, depth int) {
		if depth > 64 {
			return
		}
		switch x := v.(type) {
		case string:
			if strings.HasPrefix(x, "$") && len(x) > 1 {
				if tokBytes, ok := resolveTokenBytes(x[1:]); ok {
					writeStr("$resolved")
					_, _ = h.Write(tokBytes)
					_, _ = h.Write([]byte{0})
					return
				}
			}
			writeStr(x)
		case *jsonv.Array:
			writeStr("[")
			for _, it := range x.Items {
				writeVal(it, depth+1)
			}
			writeStr("]")
		case *jsonv.Object:
			writeStr("{")
			ks := slices.Clone(x.Keys())
			slices.Sort(ks)
			for _, k := range ks {
				writeStr(k)
				val, _ := x.Get(k)
				if isNonVarNodeKey(k) {
					_, _ = h.Write(jsonv.Marshal(val))
					_, _ = h.Write([]byte{0})
				} else {
					writeVal(val, depth+1)
				}
			}
			writeStr("}")
		default:
			_, _ = h.Write(jsonv.Marshal(v))
			_, _ = h.Write([]byte{0})
		}
	}

	// Hash root properties in deterministic key order, excluding canvas placement `x`/`y` and `placeholder`.
	keys := slices.Clone(sObj.Keys())
	slices.Sort(keys)
	for _, k := range keys {
		if k == "x" || k == "y" || k == "placeholder" {
			continue
		}
		v, _ := sObj.Get(k)
		writeStr(k)
		if isNonVarNodeKey(k) {
			_, _ = h.Write(jsonv.Marshal(v))
			_, _ = h.Write([]byte{0})
		} else {
			writeVal(v, 0)
		}
	}

	if docRoot == nil {
		return h.Sum64()
	}

	visitedRefs := map[string]bool{}
	var refQueue []string
	enqueueRefs := func(node jsonv.Value) {
		doc.Walk([]jsonv.Value{node}, func(n jsonv.Value) {
			if jsonv.StrIs(jsonv.Get(n, "type"), "ref") {
				ref := jsonv.StrOr(jsonv.Get(n, "ref"))
				if ref != "" && !strings.Contains(ref, ":") && !visitedRefs[ref] {
					visitedRefs[ref] = true
					refQueue = append(refQueue, ref)
				}
			}
		})
	}
	enqueueRefs(screen)
	for len(refQueue) > 0 {
		cid := refQueue[0]
		refQueue = refQueue[1:]
		if comp := reusableByID[cid]; comp != nil {
			enqueueRefs(comp)
		}
	}

	var refIDs []string
	for cid := range visitedRefs {
		if reusableByID[cid] != nil {
			refIDs = append(refIDs, cid)
		}
	}
	slices.Sort(refIDs)
	for _, cid := range refIDs {
		writeStr("comp:" + cid)
		writeVal(reusableByID[cid], 0)
	}

	if themesObj != nil && len(resolvedCache) > 0 {
		writeStr("themes")
		_, _ = h.Write(jsonv.Marshal(themesObj))
	}

	return h.Sum64()
}

func splitNameWords(s string) []string {
	return strings.FieldsFunc(strings.ToLower(strings.TrimSpace(s)), func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'))
	})
}

func containsWordSeq(words []string, phrase ...string) bool {
	if len(phrase) == 0 || len(words) < len(phrase) {
		return false
	}
	for i := 0; i+len(phrase) <= len(words); i++ {
		match := true
		for j, p := range phrase {
			if words[i+j] != p {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func isFullScreenDimensions(w, h float64) bool {
	// Mobile portrait screens (e.g. 320×568 iPhone SE up to 430×932+ / scrollable mobile pages)
	if w >= 320 && w <= 600 && h >= 568 {
		return true
	}
	// Tablet, desktop, web pages, 16:9 slide decks (1280×720, 1920×1080), and full social artboards (1080×1080+).
	// Rejects standalone components such as buttons, inputs, navbars (1440×80), banners (1440×360), cards (360×440), and modals (480×340).
	if w >= 720 && h >= 540 {
		return true
	}
	return false
}

func isComponentOnlyFrameName(name string) bool {
	l := strings.ToLower(strings.TrimSpace(name))
	if l == "" {
		return false
	}
	words := splitNameWords(l)
	if len(words) == 0 {
		return false
	}

	// If the name explicitly says Screen / Page / Slide / Dashboard (without being a component preview/kit),
	// keep names like "Login Screen", "Card Checkout Page", or "Sidebar Settings Screen" eligible.
	hasStrongScreenWord := false
	for _, sw := range []string{"screen", "page", "slide", "dashboard", "layar", "halaman", "beranda"} {
		if slices.Contains(words, sw) {
			hasStrongScreenWord = true
			break
		}
	}
	hasComponentBoardModifier := false
	for _, mod := range []string{"preview", "showcase", "specimen", "demo", "variants", "variant", "library", "sheet", "kit"} {
		if slices.Contains(words, mod) {
			hasComponentBoardModifier = true
			break
		}
	}
	if hasStrongScreenWord && !hasComponentBoardModifier {
		return false
	}

	for _, phrase := range [][]string{
		{"ui", "kit"},
		{"design", "system"},
		{"style", "guide"},
		{"nav", "bar"},
		{"navigation", "bar"},
		{"top", "bar"},
	} {
		if containsWordSeq(words, phrase...) {
			return true
		}
	}

	for _, kw := range []string{
		"component", "components", "uikit", "styleguide", "token", "tokens",
		"icon", "icons", "button", "buttons", "badge", "badges", "chip", "chips",
		"input", "inputs", "navbar", "topbar", "header", "footer", "sidebar",
		"card", "cards", "modal", "dialog", "popover", "dropdown", "tooltip",
		"toast", "widget", "widgets", "komponen", "tombol",
	} {
		if len(words) == 1 && words[0] == kw {
			return true
		}
		if words[0] == kw || words[len(words)-1] == kw {
			return true
		}
		if hasComponentBoardModifier && slices.Contains(words, kw) {
			return true
		}
	}
	return false
}

func hasExplicitScreenOrSlideName(name string) bool {
	l := strings.ToLower(strings.TrimSpace(name))
	if l == "" || isComponentOnlyFrameName(l) {
		return false
	}
	if strings.HasPrefix(l, "frame ") || l == "frame" || l == "mobile" || l == "desktop" || l == "canvas" || l == "artboard" {
		return false
	}
	words := splitNameWords(l)
	if len(words) == 0 {
		return false
	}
	// Never treat "* Preview" / "* Showcase" / "* Specimen" boards as explicit screen names.
	for _, mod := range []string{"preview", "showcase", "specimen"} {
		if slices.Contains(words, mod) {
			return false
		}
	}
	if containsWordSeq(words, "sign", "in") || containsWordSeq(words, "sign", "up") {
		return true
	}
	for _, kw := range []string{
		"screen", "page", "slide", "cover", "view", "dashboard", "home", "landing",
		"login", "signin", "signup", "onboarding", "checkout", "pricing", "settings",
		"profile", "analytics", "overview", "feed", "inbox", "cart", "detail",
		"layar", "halaman", "beranda",
	} {
		if slices.Contains(words, kw) {
			return true
		}
	}
	return false
}

func (e *Engine) agentOwnsTopScreen(agentID, topID string) bool {
	if topID == "" {
		return false
	}
	if len(e.Agent.ChildScopes) > 0 {
		if scope := e.Agent.ChildScopes[agentID]; len(scope) > 0 {
			return scope[topID]
		}
		// Main agent: skip screens delegated to active child scopes so only the child evaluates its own screen.
		for _, scope := range e.Agent.ChildScopes {
			if scope[topID] {
				return false
			}
		}
	}
	return true
}

func (e *Engine) isFinishedCandidateScreen(ch jsonv.Value, pens []string) (id, name string, w, h float64, ok bool) {
	if !jsonv.StrIs(jsonv.Get(ch, "type"), "frame") || jsonv.IsTrue(jsonv.Get(ch, "reusable")) {
		return "", "", 0, 0, false
	}
	if jsonv.IsTrue(jsonv.Get(ch, "placeholder")) {
		return "", "", 0, 0, false
	}
	id = jsonv.StrOr(jsonv.Get(ch, "id"))
	if id == "" || slices.Contains(pens, id) {
		return "", "", 0, 0, false
	}
	kids := jsonv.Items(jsonv.Get(ch, "children"))
	if len(kids) == 0 {
		return "", "", 0, 0, false
	}
	// Skip component-library boards whose top-level children are all reusable components.
	hasNonReusableChild := false
	for _, k := range kids {
		if !jsonv.IsTrue(jsonv.Get(k, "reusable")) {
			hasNonReusableChild = true
			break
		}
	}
	if !hasNonReusableChild {
		return "", "", 0, 0, false
	}

	f := e.Renderer.Find(id)
	if f == nil || !isFullScreenDimensions(f.W, f.H) {
		return "", "", 0, 0, false
	}
	name = f.Name
	if strings.TrimSpace(name) == "" {
		name = id
	}
	if isComponentOnlyFrameName(name) {
		return "", "", 0, 0, false
	}

	// Guard against generic starter frames (e.g. "Frame 1" 1440×900) where the user only asked for 1 isolated small component.
	if !hasExplicitScreenOrSlideName(name) {
		descCount := 0
		doc.Walk(kids, func(n jsonv.Value) {
			if !jsonv.StrIs(jsonv.Get(n, "type"), "group") {
				descCount++
			}
		})
		if descCount <= 4 {
			minX, minY := 1e18, 1e18
			maxX, maxY := -1e18, -1e18
			foundChildBox := false
			for _, k := range kids {
				kidID := jsonv.StrOr(jsonv.Get(k, "id"))
				if kf := e.Renderer.Find(kidID); kf != nil && kf.W > 0 && kf.H > 0 {
					foundChildBox = true
					if kf.X < minX {
						minX = kf.X
					}
					if kf.Y < minY {
						minY = kf.Y
					}
					if kf.X+kf.W > maxX {
						maxX = kf.X + kf.W
					}
					if kf.Y+kf.H > maxY {
						maxY = kf.Y + kf.H
					}
				}
			}
			if foundChildBox {
				spanW := maxX - minX
				spanH := maxY - minY
				frameArea := f.W * f.H
				if frameArea > 0 && (spanW*spanH)/frameArea < 0.15 && spanH/f.H < 0.35 {
					return "", "", 0, 0, false
				}
			}
		}
	}

	return id, name, f.W, f.H, true
}

func (e *Engine) buildScreenEvalPlan(id, name string, hash uint64, w, h float64) (ScreenEvalPlan, bool) {
	scale := typesafe.IdealRasterScale(w, h)
	e.Assets.Record()
	runs := &script.Runs{}
	tree, err := e.exportCtx(e.Doc, e.Libs, runs).NodeTreeAt(id, scale)
	waits := e.Assets.Recorded()
	if err != nil || tree == nil {
		return ScreenEvalPlan{}, false
	}
	plan := ScreenEvalPlan{
		ID:        id,
		Name:      name,
		Hash:      hash,
		SourceDoc: e.Doc,
		Tree:      tree,
		Scale:     scale,
		Downloads: waits,
		Runs:      runs,
	}
	if len(waits) > 0 || !runs.IsEmpty() {
		plan.CallDoc, plan.CallLibs = e.snapshot()
	}
	return plan, true
}

// PlanTurnTypeSafeEvaluations plans per-screen visual evaluations ONLY at the end of an agent's turn
// (never mid-build during individual `execute` component/section steps) for any finished screens owned by
// agentID that were newly created or edited in this turn and haven't been evaluated at their latest visual state.
// Must be called under Engine.Lock.
func (e *Engine) PlanTurnTypeSafeEvaluations(agentID string) []ScreenEvalPlan {
	if !e.TypeSafeEnabled() || e.Doc == nil || e.Doc.Root == nil {
		return nil
	}
	pens := e.Agent.PenFrames()
	reusableByID := CollectReusableComponents(e.Doc.Root)

	// If the agent finished its turn while leaving `placeholder: true` on its own finished screen (and no AI Pen
	// is drawing into it), clear the placeholder flag first so the finished screen evaluates cleanly.
	for _, ch := range e.Doc.Children() {
		if !jsonv.StrIs(jsonv.Get(ch, "type"), "frame") || jsonv.IsTrue(jsonv.Get(ch, "reusable")) {
			continue
		}
		if !jsonv.IsTrue(jsonv.Get(ch, "placeholder")) {
			continue
		}
		topID := jsonv.StrOr(jsonv.Get(ch, "id"))
		if topID == "" || slices.Contains(pens, topID) || !e.agentOwnsTopScreen(agentID, topID) {
			continue
		}
		preHash, hadPreHash := e.Agent.PreTurnScreenHashes[topID]
		hashChanged := hadPreHash && ScreenVisualHashWithComponents(e.Doc.Root, ch, reusableByID) != preHash
		inTurnScope := slices.Contains(e.Agent.TurnNewTopIDs, topID) || e.Agent.TurnEditedPreTop[topID] || hashChanged
		if !inTurnScope {
			continue
		}
		e.ApplyGenerated(topID, Object("placeholder", false), true)
	}
	e.Sync()

	activeMinScore := e.TypeSafeMinScore()
	var plans []ScreenEvalPlan
	for _, ch := range e.Doc.Children() {
		topID, name, w, h, ok := e.isFinishedCandidateScreen(ch, pens)
		if !ok || !e.agentOwnsTopScreen(agentID, topID) {
			continue
		}

		hash := ScreenVisualHashWithComponents(e.Doc.Root, ch, reusableByID)
		preHash, hadPreHash := e.Agent.PreTurnScreenHashes[topID]
		if hadPreHash && hash == preHash {
			continue
		}
		hashChangedFromPreTurn := hadPreHash && hash != preHash && !(e.Agent.IsVariationTurn && e.Agent.PreTurnTopIDs[topID])
		inTurnScope := slices.Contains(e.Agent.TurnNewTopIDs, topID) || e.Agent.TurnEditedPreTop[topID] || hashChangedFromPreTurn
		if !inTurnScope {
			continue
		}

		if e.Agent.EvaluatedScreenHashes[topID] == hash {
			prev := e.Agent.LastScreenDecisions[topID]
			if prev != nil && prev.PassScore == activeMinScore {
				// Already evaluated at this exact visual state AND active threshold: if it scored "revise" and the
				// agent tried to end a revision turn without modifying the screen yet, emit a cached revise nudge.
				maxRevs := e.TypeSafeEffectiveMaxRevisions()
				if prev.Verdict == "revise" && maxRevs > 0 && e.Agent.ScreenEvalRevisions[topID] < maxRevs {
					plans = append(plans, ScreenEvalPlan{
						ID:           topID,
						Name:         name,
						Hash:         hash,
						SourceDoc:    e.Doc,
						CachedRevise: prev,
					})
				}
				continue
			}
		}

		if plan, built := e.buildScreenEvalPlan(topID, name, hash, w, h); built {
			plans = append(plans, plan)
		}
	}
	return plans
}

// RasterizeScreenPlan settles any pending asset downloads or script nodes for p and encodes ONLY that single
// screen's visual PNG. Must be called WITHOUT holding Engine.Lock.
func (e *Engine) RasterizeScreenPlan(p ScreenEvalPlan) ([]byte, error) {
	until := time.Now().Add(settleWait)
	redraw := len(p.Downloads) > 0 || (p.Runs != nil && !p.Runs.IsEmpty())
	if p.Runs != nil && !p.Runs.IsEmpty() {
		p.Runs.Run()
	}
	if len(p.Downloads) > 0 {
		e.WaitDownloads(p.Downloads, until)
	}
	tree := p.Tree
	if p.CallDoc != nil && redraw {
		e.Locked(func() {
			if fresh, err := e.exportCtx(p.CallDoc, p.CallLibs, &script.Runs{}).NodeTreeAt(p.ID, p.Scale); err == nil && fresh != nil {
				tree = fresh
			}
		})
	}
	return export.EncodeTree(tree, "png", p.Scale, 0)
}

// RecordScreenEvaluation stores a completed TypeSafe decision for screenID at hash if sourceDoc still matches e.Doc,
// returning the current auto-revision attempt, configured max attempts, whether auto-revision should run, and whether
// sourceDoc is still the active document. Must be called under Engine.Lock.
func (e *Engine) RecordScreenEvaluation(sourceDoc *doc.Doc, screenID string, hash uint64, dec *typesafe.Decision, consumeRevision bool) (attempt int, maxRevs int, canRevise bool, sameDoc bool) {
	if sourceDoc != nil && e.Doc != sourceDoc {
		return 0, 0, false, false
	}
	if e.Agent.EvaluatedScreenHashes == nil {
		e.Agent.EvaluatedScreenHashes = map[string]uint64{}
	}
	if e.Agent.LastScreenDecisions == nil {
		e.Agent.LastScreenDecisions = map[string]*typesafe.Decision{}
	}
	if e.Agent.ScreenEvalRevisions == nil {
		e.Agent.ScreenEvalRevisions = map[string]int{}
	}
	e.Agent.EvaluatedScreenHashes[screenID] = hash
	e.Agent.LastScreenDecisions[screenID] = dec
	maxRevs = e.TypeSafeEffectiveMaxRevisions()
	cur := e.Agent.ScreenEvalRevisions[screenID]
	if dec == nil || dec.Verdict == "pass" || maxRevs <= 0 || cur >= maxRevs {
		return cur, maxRevs, false, true
	}
	if consumeRevision {
		cur++
		e.Agent.ScreenEvalRevisions[screenID] = cur
	}
	return cur, maxRevs, true, true
}

// ConsumeCachedRevise increments the revision counter for a cached revise nudge if sourceDoc still matches e.Doc
// and auto-revision budget remains. Under Engine.Lock.
func (e *Engine) ConsumeCachedRevise(sourceDoc *doc.Doc, screenID string) (attempt int, maxRevs int, canRevise bool) {
	if sourceDoc != nil && e.Doc != sourceDoc {
		return 0, 0, false
	}
	if e.Agent.ScreenEvalRevisions == nil {
		e.Agent.ScreenEvalRevisions = map[string]int{}
	}
	maxRevs = e.TypeSafeEffectiveMaxRevisions()
	cur := e.Agent.ScreenEvalRevisions[screenID]
	if maxRevs <= 0 || cur >= maxRevs {
		return cur, maxRevs, false
	}
	cur++
	e.Agent.ScreenEvalRevisions[screenID] = cur
	return cur, maxRevs, true
}
