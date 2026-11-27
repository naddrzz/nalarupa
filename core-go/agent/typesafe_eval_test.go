package agent

import (
	"bytes"
	"fmt"
	"image/png"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/engine"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/typesafe"
)

type recordedTSCall struct {
	stateKeys []string
	imgW      int
	imgH      int
	questions []string
}

func newFakeTypeSafeServer(t *testing.T, responder func(callIdx int) string) (*httptest.Server, func() []recordedTSCall) {
	t.Helper()
	var mu sync.Mutex
	var calls []recordedTSCall

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"models":[{"name":"jev-latest","description":"Newest Jev","release_date":"2026-10-14"},{"name":"jev-1.14.0","description":"Jev 1.14","release_date":"2026-10-14"}]}`)
			return
		}
		if r.URL.Path != "/v1/systemone" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer ts-byok-test" {
			http.Error(w, `{"detail":"unauthorized"}`, http.StatusUnauthorized)
			return
		}

		ct := r.Header.Get("Content-Type")
		mt, params, err := mime.ParseMediaType(ct)
		if err == nil && mt == "multipart/form-data" {
			if r.Header.Get("TypeSafe-Danger-Zone-Images") != "true" {
				http.Error(w, `{"detail":"missing TypeSafe-Danger-Zone-Images"}`, http.StatusBadRequest)
				return
			}
			mr := multipart.NewReader(r.Body, params["boundary"])
			p1, err := mr.NextPart()
			if err != nil || p1.FormName() != "request" {
				http.Error(w, `{"detail":"missing request part"}`, http.StatusBadRequest)
				return
			}
			reqBytes, _ := io.ReadAll(p1)
			reqJSON, err := jsonv.Parse(reqBytes)
			if err != nil {
				http.Error(w, `{"detail":"bad json"}`, http.StatusBadRequest)
				return
			}
			stObj := jsonv.Obj(jsonv.Get(reqJSON, "state"))
			qObj := jsonv.Obj(jsonv.Get(reqJSON, "questions"))

			p2, err := mr.NextPart()
			if err != nil || p2.FormName() != "screen_image" {
				http.Error(w, `{"detail":"missing screen_image part"}`, http.StatusBadRequest)
				return
			}
			imgBytes, _ := io.ReadAll(p2)
			cfg, err := png.DecodeConfig(bytes.NewReader(imgBytes))
			if err != nil {
				http.Error(w, `{"detail":"invalid png"}`, http.StatusBadRequest)
				return
			}

			mu.Lock()
			idx := len(calls)
			var sk, qk []string
			if stObj != nil {
				sk = stObj.Keys()
			}
			if qObj != nil {
				qk = qObj.Keys()
			}
			calls = append(calls, recordedTSCall{
				stateKeys: sk,
				imgW:      cfg.Width,
				imgH:      cfg.Height,
				questions: qk,
			})
			mu.Unlock()

			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, responder(idx))
			return
		}

		// Text-only TestConnection probe
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"model":"jev-1.14.0","answers":{"probe":{"type":"noul","noul":0.97}},"usage":{"input_tokens":24,"output_tokens":4}}`)
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(typesafe.SetTestTransport(srv.Client().Transport))

	return srv, func() []recordedTSCall {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(calls)
	}
}

func tsJSONResponse(hierarchy, a11y, harmony, spacing, typo, overlapNoul, clashNoul, slopNoul float64) string {
	return fmt.Sprintf(`{
		"model": "jev-1.14.0",
		"answers": {
			"visual_hierarchy": {"type": "score", "score": %.2f},
			"accessibility_contrast": {"type": "score", "score": %.2f},
			"color_harmony": {"type": "score", "score": %.2f},
			"spacing_alignment": {"type": "score", "score": %.2f},
			"typography_craft": {"type": "score", "score": %.2f},
			"has_overlap_or_clipping": {"type": "noul", "noul": %.2f},
			"has_color_clash": {"type": "noul", "noul": %.2f},
			"is_ai_slop": {"type": "noul", "noul": %.2f}
		},
		"usage": {"input_tokens": 880, "output_tokens": 56}
	}`, hierarchy, a11y, harmony, spacing, typo, overlapNoul, clashNoul, slopNoul)
}

func TestTypeSafePerScreenOnlySkipsUntouchedScreensAndTriggersRevisions(t *testing.T) {
	tsSrv, getTSCalls := newFakeTypeSafeServer(t, func(callIdx int) string {
		switch callIdx {
		case 0:
			// First evaluation of new Screen B: fails due to overlap + low contrast (triggers agent revision!)
			return tsJSONResponse(1.6, 1.1, 2.0, 1.8, 2.0, 0.86, 0.12, 0.15)
		default:
			// Subsequent evaluations pass cleanly
			return tsJSONResponse(2.8, 2.8, 2.7, 2.6, 2.7, 0.03, 0.04, 0.06)
		}
	})

	var tsCallsMidBuild int
	_, claudeURL := newFakeAPI(t, func(n int, w http.ResponseWriter, r *http.Request) string {
		switch n {
		// ── Turn 1: Build Screen B across 3 incremental `execute` ("Designed") calls (components + sections + screenshots).
		// TypeSafe Jev MUST NOT fire during any of these mid-build `execute` steps — ONLY when the screen turn completes!
		case 0:
			return toolReply("Creating reusable navbar & button components + starting Screen B.", "t1_c1",
				`navComp = Insert(document, {type:"frame", name:"Navbar", reusable:true, x:1600, y:-200, width:1280, height:64, fill:"#FFFFFF"});`+
					`Insert(navComp, {type:"text", name:"Brand", content:"Acme", fontSize:18, fill:"#111111"});`+
					`b = Insert(document, {type:"frame", name:"Pricing Screen", x:1600, y:0, width:1280, height:800, fill:"#FAFAFA", layout:"vertical", padding:48});`+
					`Insert(b, {type:"ref", ref:navComp, name:"TopNav", width:"fill_container"});`+
					`TakeScreenshot([b]);`)
		case 1:
			return toolReply("Adding Hero section on Screen B.", "t1_c2",
				`Insert(b, {type:"text", name:"Hero", content:"Simple plans", fontSize:48, fill:"#777777"});`+
					`TakeScreenshot("document");`)
		case 2:
			return toolReply("Adding Pricing card section on Screen B.", "t1_c3",
				`Insert(b, {type:"rectangle", name:"PlanCard", width:360, height:240, fill:"#FFFFFF"});`+
					`TakeScreenshot([b]);`)
		case 3:
			// Record how many TypeSafe calls happened across all 3 "Designed" (`execute`) steps (must be 0!).
			tsCallsMidBuild = len(getTSCalls())
			// Agent concludes initial Screen B build -> RunTurnEndTypeSafeGate fires TypeSafe Call #0 (returns REVISE!)
			return textReply("Initial Pricing screen ready.", "end_turn")
		case 4:
			// Auto-revision round 1/2: agent fixes contrast & spacing on Screen B!
			return toolReply("Fixing contrast and overlap on Screen B.", "t1_c4",
				`Update(b, {gap:24});`+
					`Insert(b, {type:"rectangle", name:"CTA", width:200, height:48, fill:"#111111"});`)
		case 5:
			// Agent concludes auto-revision -> RunTurnEndTypeSafeGate evaluates updated Screen B (TypeSafe Call #1 -> PASS!)
			return textReply("Pricing screen completed.", "end_turn")

		// ── Turn 2: User asks to edit Screen A ("existing_home") only — Screen B must NOT be re-evaluated!
		case 6:
			return toolReply("Updating Home headline.", "t2_c1",
				`Insert("existing_home", {type:"rectangle", name:"AccentBar", width:320, height:12, fill:"#0F766E"});`)
		default:
			return textReply("Updated Home.", "end_turn")
		}
	})

	e := apiEngine(t, claudeURL)
	e.Locked(func() {
		// Seed an existing finished screen ("existing_home") on the canvas BEFORE Turn 1.
		e.Doc.Root.Set("children", jsonv.MustParse(`[
			{"id":"existing_home","type":"frame","name":"Home Screen","x":0,"y":0,"width":1440,"height":900,"fill":"#F5F2EB","layout":"vertical","padding":64,"children":[
				{"id":"h1","type":"text","name":"Title","content":"Existing Home","fontSize":56,"fill":"#1A1A1A"}
			]}
		]`))
		e.Touch()
		e.Settings.Set("typesafe_enabled", true)
		e.Settings.Set("typesafe_api_key", "ts-byok-test")
		e.Settings.Set("typesafe_base_url", tsSrv.URL)
		e.Settings.Set("typesafe_model", "jev-latest")
		e.Settings.Set("typesafe_min_score", jsonv.Int(75))

		// Verify SettingsView masks typesafe_api_key!
		view := e.SettingsView()
		if !jsonv.StrIs(jsonv.Get(view, "typesafe_api_key"), engine.Mask) {
			t.Fatalf("typesafe_api_key was not masked in SettingsView: %s", string(jsonv.Marshal(view)))
		}
		if !jsonv.IsTrue(jsonv.Get(view, "has_typesafe_key")) {
			t.Fatalf("expected has_typesafe_key=true in SettingsView")
		}
	})

	// Turn 1: Build Screen B
	if err := Send(e, "Add a Pricing screen next to Home", 1, "split", 1, "layout"); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, e)

	if tsCallsMidBuild != 0 {
		t.Fatalf("expected ZERO TypeSafe Jev calls during mid-build execute steps, got %d", tsCallsMidBuild)
	}

	callsAfterTurn1 := getTSCalls()
	// Expect exactly 2 TypeSafe calls in Turn 1:
	//   #0 when Screen B transitioned placeholder:true -> false (1280x800, NOT 1440x900 Home or combined 2880px canvas!)
	//   #1 at end of Turn 1 after Screen B was revised (1280x800)
	if len(callsAfterTurn1) != 2 {
		t.Fatalf("expected 2 TypeSafe calls in Turn 1 (initial finish + post-revision), got %d: %+v", len(callsAfterTurn1), callsAfterTurn1)
	}
	for i, c := range callsAfterTurn1 {
		if !slices.Equal(c.stateKeys, []string{"screen"}) {
			t.Fatalf("call %d sent non-visual state keys: %v", i, c.stateKeys)
		}
		// Pricing Screen is 1280x800; Home Screen is 1440x900. Verifies ONLY Pricing Screen was rendered!
		if c.imgW != 1280 || c.imgH != 800 {
			t.Fatalf("call %d rendered %dx%d, want 1280x800 (Pricing Screen only, not Home or full canvas)", i, c.imgW, c.imgH)
		}
	}

	// Turn 2: Edit existing Home Screen (1440x900) — Pricing Screen (1280x800) must NOT be re-evaluated!
	if err := Send(e, "Add an accent bar on Home Screen", 1, "split", 1, "layout"); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, e)

	callsAfterTurn2 := getTSCalls()
	if len(callsAfterTurn2) != 3 {
		t.Fatalf("expected exactly 1 new TypeSafe call in Turn 2 (for edited Home Screen only), total=%d", len(callsAfterTurn2))
	}
	lastCall := callsAfterTurn2[2]
	if lastCall.imgW != 1440 || lastCall.imgH != 900 {
		t.Fatalf("Turn 2 call rendered %dx%d, want 1440x900 (edited Home Screen only)", lastCall.imgW, lastCall.imgH)
	}

	// Verify TypeSafe decision cards were emitted into agent events with 0–100 score metadata.
	var tsDoneCount int
	for _, ev := range agentEvents(e) {
		if jsonv.StrIs(jsonv.Get(ev, "kind"), "tool_done") && jsonv.StrIs(jsonv.Get(ev, "name"), "typesafe_decision") {
			tsDoneCount++
			meta := jsonv.Get(ev, "typesafe")
			if meta == nil {
				t.Fatalf("expected typesafe metadata on tool_done event: %s", string(jsonv.Marshal(ev)))
			}
			sc, ok := jsonv.F64(jsonv.Get(meta, "score"))
			if !ok || sc < 0 || sc > 100 {
				t.Fatalf("expected 0..100 score on typesafe metadata, got %v", jsonv.Get(meta, "score"))
			}
		}
	}
	if tsDoneCount != 3 {
		t.Fatalf("expected 3 typesafe_decision tool_done cards in chat, got %d", tsDoneCount)
	}
	_ = strings.Contains
}

func TestTypeSafeAutoRevisionConfigurableThresholdToggleAndMaxRounds(t *testing.T) {
	t.Run("custom_high_threshold_triggers_auto_revision_until_met", func(t *testing.T) {
		// Call #0 scores ~87 (clean screen, passes default 75, but BELOW custom 92 threshold!).
		// Call #1 after auto-revision scores ~96 (>= 92 -> PASS!).
		tsSrv, getTSCalls := newFakeTypeSafeServer(t, func(callIdx int) string {
			if callIdx == 0 {
				return tsJSONResponse(2.7, 2.7, 2.6, 2.5, 2.6, 0.04, 0.04, 0.06)
			}
			return tsJSONResponse(2.95, 2.95, 2.92, 2.90, 2.92, 0.01, 0.01, 0.02)
		})

		api, claudeURL := newFakeAPI(t, func(n int, w http.ResponseWriter, r *http.Request) string {
			switch n {
			case 0:
				return toolReply("Creating Hero Screen.", "c1",
					`h = Insert(document, {type:"frame", name:"Hero Screen", x:0, y:0, width:1280, height:800, fill:"#FFFFFF", layout:"vertical", padding:40});`+
						`Insert(h, {type:"text", name:"Title", content:"Welcome", fontSize:44, fill:"#18181B"});`)
			case 1:
				// Concludes initial build -> RunTurnEndTypeSafeGate evaluates h (~87 < 92) and injects auto-revision prompt!
				return textReply("Initial Hero Screen ready.", "end_turn")
			case 2:
				// Auto-revision round 1/2: agent polishes spacing & rhythm
				return toolReply("Refining spacing to reach ≥ 92.", "c2",
					`Update(h, {padding:64, gap:24});`)
			default:
				return textReply("Polished Hero Screen to meet 92/100 bar.", "end_turn")
			}
		})

		e := apiEngine(t, claudeURL)
		e.Locked(func() {
			e.Settings.Set("typesafe_enabled", true)
			e.Settings.Set("typesafe_auto_revise", true)
			e.Settings.Set("typesafe_max_revisions", jsonv.Int(2))
			e.Settings.Set("typesafe_min_score", jsonv.Int(92))
			e.Settings.Set("typesafe_api_key", "ts-byok-test")
			e.Settings.Set("typesafe_base_url", tsSrv.URL)
		})

		if err := Send(e, "Design a Hero Screen", 1, "split", 1, "layout"); err != nil {
			t.Fatal(err)
		}
		waitIdle(t, e)

		calls := getTSCalls()
		if len(calls) != 2 {
			t.Fatalf("expected 2 TypeSafe evaluations (87 < 92 auto-revise, then 96 >= 92 pass), got %d", len(calls))
		}
		api.mu.Lock()
		var thirdBody string
		if len(api.reqs) > 2 {
			thirdBody = string(jsonv.Marshal(api.reqs[2].body))
		}
		reqCount := len(api.reqs)
		api.mu.Unlock()
		if reqCount < 3 || !strings.Contains(thirdBody, "92/100") || !strings.Contains(thirdBody, "auto-revision 1/2") {
			t.Fatalf("expected auto-revision prompt with custom 92/100 threshold on API call #2 (reqs=%d): %s", reqCount, thirdBody)
		}
		var sawAutoReviseNote bool
		for _, ev := range agentEvents(e) {
			if jsonv.StrIs(jsonv.Get(ev, "kind"), "note") && strings.Contains(jsonv.StrOr(jsonv.Get(ev, "text")), "92/100 threshold · pass 1/2") {
				sawAutoReviseNote = true
			}
		}
		if !sawAutoReviseNote {
			t.Fatalf("expected auto-revision note event mentioning 92/100 threshold · pass 1/2")
		}
	})

	t.Run("auto_revise_disabled_scores_without_triggering_revision_turn", func(t *testing.T) {
		tsSrv, getTSCalls := newFakeTypeSafeServer(t, func(callIdx int) string {
			// Scores ~58 (< 80 threshold)
			return tsJSONResponse(1.8, 1.7, 1.8, 1.7, 1.8, 0.20, 0.15, 0.20)
		})

		var apiTurns int
		_, claudeURL := newFakeAPI(t, func(n int, w http.ResponseWriter, r *http.Request) string {
			apiTurns++
			if n == 0 {
				return toolReply("Creating Settings Screen.", "c1",
					`s = Insert(document, {id:"set_scr", type:"frame", name:"Settings Screen", x:0, y:0, width:1024, height:768, fill:"#F8FAFC", layout:"vertical", padding:32});`+
						`Insert(s, {type:"text", name:"Header", content:"Settings", fontSize:32, fill:"#334155"});`)
			}
			return textReply("Done.", "end_turn")
		})

		e := apiEngine(t, claudeURL)
		e.Locked(func() {
			e.Settings.Set("typesafe_enabled", true)
			e.Settings.Set("typesafe_auto_revise", false)
			e.Settings.Set("typesafe_min_score", jsonv.Int(80))
			e.Settings.Set("typesafe_api_key", "ts-byok-test")
			e.Settings.Set("typesafe_base_url", tsSrv.URL)
		})

		if err := Send(e, "Build Settings Screen", 1, "split", 1, "layout"); err != nil {
			t.Fatal(err)
		}
		waitIdle(t, e)

		if len(getTSCalls()) != 1 {
			t.Fatalf("expected exactly 1 TypeSafe score call with auto_revise=false, got %d", len(getTSCalls()))
		}
		if apiTurns != 2 {
			t.Fatalf("expected zero extra auto-revision API turns when typesafe_auto_revise=false, got apiTurns=%d", apiTurns)
		}
	})

	t.Run("max_revisions_caps_auto_revision_attempts", func(t *testing.T) {
		tsSrv, getTSCalls := newFakeTypeSafeServer(t, func(callIdx int) string {
			// Always returns ~65 (< 90 threshold)
			return tsJSONResponse(2.0, 2.0, 2.0, 1.9, 2.0, 0.10, 0.10, 0.10)
		})

		_, claudeURL := newFakeAPI(t, func(n int, w http.ResponseWriter, r *http.Request) string {
			switch n {
			case 0:
				return toolReply("Initial screen.", "c1",
					`cap = Insert(document, {type:"frame", name:"Cap Screen", x:0, y:0, width:1024, height:768, fill:"#FFFFFF", layout:"vertical", padding:24});`+
						`Insert(cap, {type:"text", name:"Heading", content:"Cap", fontSize:36, fill:"#111111"});`)
			case 1:
				return textReply("Initial done.", "end_turn")
			case 2:
				// Revision 1/1 edits the screen
				return toolReply("Revision 1.", "c2", `Update(cap, {padding:48});`)
			default:
				return textReply("Revision 1 done.", "end_turn")
			}
		})

		e := apiEngine(t, claudeURL)
		e.Locked(func() {
			e.Settings.Set("typesafe_enabled", true)
			e.Settings.Set("typesafe_auto_revise", true)
			e.Settings.Set("typesafe_max_revisions", jsonv.Int(1))
			e.Settings.Set("typesafe_min_score", jsonv.Int(90))
			e.Settings.Set("typesafe_api_key", "ts-byok-test")
			e.Settings.Set("typesafe_base_url", tsSrv.URL)
		})

		if err := Send(e, "Build Cap Screen", 1, "split", 1, "layout"); err != nil {
			t.Fatal(err)
		}
		waitIdle(t, e)

		// 1 initial eval + 1 post-revision eval, then capped at max_revisions=1!
		if got := len(getTSCalls()); got != 2 {
			t.Fatalf("expected 2 TypeSafe calls with max_revisions=1 (initial + 1st revision), got %d", got)
		}
	})
}

func TestTypeSafeAutoRevisionOnClaudeCodeBackend(t *testing.T) {
	tsSrv, getTSCalls := newFakeTypeSafeServer(t, func(callIdx int) string {
		if callIdx == 0 {
			// First screen build scores below 85 -> triggers --resume auto-revision in claude-code!
			return tsJSONResponse(1.8, 1.6, 2.0, 1.8, 1.9, 0.65, 0.10, 0.15)
		}
		return tsJSONResponse(2.85, 2.85, 2.80, 2.75, 2.80, 0.02, 0.03, 0.04)
	})

	e, log := ccEngine(t, "ts-mcp")
	oldFirstPort := firstMCPPort
	firstMCPPort = 47820
	t.Cleanup(func() { firstMCPPort = oldFirstPort })
	port, mcpSrv, err := listenMCP(e)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mcpSrv.Close() })

	e.Locked(func() {
		e.MCPPort = port
		e.Settings.Set("typesafe_enabled", true)
		e.Settings.Set("typesafe_auto_revise", true)
		e.Settings.Set("typesafe_max_revisions", jsonv.Int(2))
		e.Settings.Set("typesafe_min_score", jsonv.Int(85))
		e.Settings.Set("typesafe_api_key", "ts-byok-test")
		e.Settings.Set("typesafe_base_url", tsSrv.URL)
	})

	if err := Send(e, "Design Analytics Screen", 1, "split", 1, "layout"); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, e)

	loggedRuns := runs(t, log)
	if len(loggedRuns) != 2 {
		t.Fatalf("expected 2 Claude Code CLI invocations (initial + auto-revise --resume), got %d", len(loggedRuns))
	}
	secondArgs := argsOf(loggedRuns[1])
	if !slices.Contains(secondArgs, "--resume") || !slices.Contains(secondArgs, "s-ts") {
		t.Fatalf("expected 2nd Claude Code invocation to pass --resume s-ts, got %v", secondArgs)
	}
	secondStdin := jsonv.StrOr(jsonv.Get(loggedRuns[1], "stdin"))
	if !strings.Contains(secondStdin, "TypeSafe AI auto-revision triggered") {
		t.Fatalf("expected 2nd Claude Code stdin to contain auto-revision directive, got %q", secondStdin)
	}
	if len(getTSCalls()) != 2 {
		t.Fatalf("expected 2 TypeSafe visual evaluations across Claude Code auto-revision, got %d", len(getTSCalls()))
	}
}

func TestTypeSafeSkipsStandaloneComponentsAndEvaluatesOnlyFinishedScreens(t *testing.T) {
	tsSrv, getTSCalls := newFakeTypeSafeServer(t, func(callIdx int) string {
		return tsJSONResponse(2.8, 2.8, 2.7, 2.7, 2.7, 0.03, 0.03, 0.05)
	})

	_, claudeURL := newFakeAPI(t, func(n int, w http.ResponseWriter, r *http.Request) string {
		switch n {
		// Turn 1: User asks for reusable components only -> must NOT call Jev!
		case 0:
			return toolReply("Creating Button & Input reusable components.", "c_t1",
				`btn = Insert(document, {type:"frame", name:"Primary Button", reusable:true, x:0, y:0, width:180, height:48, fill:"#2563EB", cornerRadius:8});`+
					`Insert(btn, {type:"text", name:"Label", content:"Continue", fontSize:15, fill:"#FFFFFF"});`)
		case 1:
			return textReply("Reusable button ready.", "end_turn")

		// Turn 2: User asks for standalone non-reusable components (Navbar 1440x80, Card 360x420, Modal 480x340, UI Kit 1440x900) -> must NOT call Jev!
		case 2:
			return toolReply("Creating standalone Navbar, Card, Modal, and UI Kit sheet.", "c_t2",
				`nb = Insert(document, {type:"frame", name:"Navbar", x:0, y:120, width:1440, height:80, fill:"#FFFFFF"});`+
					`Insert(nb, {type:"text", name:"Brand", content:"Logo", fontSize:18, fill:"#111111"});`+
					`cd = Insert(document, {type:"frame", name:"Product Card", x:0, y:260, width:360, height:420, fill:"#FFFFFF"});`+
					`Insert(cd, {type:"text", name:"CardTitle", content:"Pro Plan", fontSize:22, fill:"#111111"});`+
					`md = Insert(document, {type:"frame", name:"Confirm Modal", x:420, y:260, width:480, height:340, fill:"#FFFFFF"});`+
					`Insert(md, {type:"text", name:"ModalTitle", content:"Delete item?", fontSize:20, fill:"#111111"});`+
					`kit = Insert(document, {type:"frame", name:"UI Kit", x:0, y:760, width:1440, height:900, fill:"#F8FAFC"});`+
					`Insert(kit, {type:"text", name:"KitHeader", content:"Buttons & Badges", fontSize:28, fill:"#0F172A"});`)
		case 3:
			return textReply("Standalone components created.", "end_turn")

		// Turn 3: 1 tiny isolated button placed inside a generic "Frame 1" or "Button Preview" (1440x900) -> must NOT call Jev!
		case 4:
			return toolReply("Placing 1 small button inside Frame 1 and Button Preview.", "c_t3",
				`f1 = Insert(document, {type:"frame", name:"Frame 1", x:1800, y:0, width:1440, height:900, fill:"#FFFFFF"});`+
					`b1 = Insert(f1, {type:"frame", name:"Btn", x:40, y:40, width:160, height:44, fill:"#111111"});`+
					`Insert(b1, {type:"text", name:"Txt", content:"Save", fontSize:14, fill:"#FFFFFF"});`+
					`bp = Insert(document, {type:"frame", name:"Button Preview", x:1800, y:1050, width:1440, height:900, fill:"#FFFFFF"});`+
					`b2 = Insert(bp, {type:"frame", name:"Btn2", x:40, y:40, width:160, height:44, fill:"#2563EB"});`+
					`Insert(b2, {type:"text", name:"Txt2", content:"Submit", fontSize:14, fill:"#FFFFFF"});`)
		case 5:
			return textReply("Added button.", "end_turn")

		// Turn 4: Full mobile screen ("Checkout Screen", 393x852) -> MUST call Jev ONCE at Turn End!
		case 6:
			return toolReply("Building full Checkout Screen.", "c_t4",
				`mob = Insert(document, {type:"frame", name:"Checkout Screen", x:3600, y:0, width:393, height:852, fill:"#FFFFFF", layout:"vertical", padding:24, gap:20});`+
					`Insert(mob, {type:"text", name:"TopTitle", content:"Checkout", fontSize:28, fill:"#111111"});`+
					`Insert(mob, {type:"rectangle", name:"SummaryCard", width:345, height:220, fill:"#F4F4F5"});`+
					`Insert(mob, {type:"rectangle", name:"PayButton", width:345, height:52, fill:"#111111"});`)
		default:
			return textReply("Checkout mobile screen complete.", "end_turn")
		}
	})

	e := apiEngine(t, claudeURL)
	e.Locked(func() {
		e.Settings.Set("typesafe_enabled", true)
		e.Settings.Set("typesafe_auto_revise", true)
		e.Settings.Set("typesafe_min_score", jsonv.Int(75))
		e.Settings.Set("typesafe_api_key", "ts-byok-test")
		e.Settings.Set("typesafe_base_url", tsSrv.URL)
	})

	for i, prompt := range []string{
		"Make a reusable button",
		"Make standalone navbar, card, modal, and UI kit",
		"Make 1 small button in Frame 1",
	} {
		if err := Send(e, prompt, 1, "split", 1, "layout"); err != nil {
			t.Fatal(err)
		}
		waitIdle(t, e)
		if got := len(getTSCalls()); got != 0 {
			t.Fatalf("after component-only turn %d (%q), expected 0 TypeSafe Jev calls, got %d", i+1, prompt, got)
		}
	}

	// Turn 4: Full mobile Checkout Screen -> exactly 1 TypeSafe Jev call for 393x852!
	if err := Send(e, "Design mobile Checkout Screen", 1, "split", 1, "layout"); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, e)

	calls := getTSCalls()
	if len(calls) != 1 {
		t.Fatalf("expected exactly 1 TypeSafe Jev call for finished Checkout Screen, got %d", len(calls))
	}
	if calls[0].imgW != 393 || calls[0].imgH != 852 {
		// Note: 393*2 = 786, 852*2 = 1704 (> 1568 max side), so IdealRasterScale scales proportionally within [280, 1568]!
		if calls[0].imgW < 280 || calls[0].imgH < 568 {
			t.Fatalf("unexpected mobile Checkout Screen image dimensions %dx%d", calls[0].imgW, calls[0].imgH)
		}
	}
}

func TestTypeSafeSharedComponentEditThresholdInvalidationEndpointBindingAndDocSwap(t *testing.T) {
	tsSrv, getTSCalls := newFakeTypeSafeServer(t, func(callIdx int) string {
		// Scores ~87 (passes 75, fails 94)
		return tsJSONResponse(2.7, 2.7, 2.6, 2.5, 2.6, 0.04, 0.04, 0.06)
	})

	_, claudeURL := newFakeAPI(t, func(n int, w http.ResponseWriter, r *http.Request) string {
		switch n {
		case 0:
			// Edit ONLY the shared reusable component `shared_nav` (NOT `home_scr` directly!)
			return toolReply("Updating shared Navbar component.", "sc_1",
				`Update("shared_nav", {fill:"#0F172A"});`)
		default:
			return textReply("Updated shared Navbar.", "end_turn")
		}
	})

	e := apiEngine(t, claudeURL)
	e.Locked(func() {
		e.Doc.Root.Set("children", jsonv.MustParse(`[
			{"id":"shared_nav","type":"frame","name":"SharedNav","reusable":true,"x":0,"y":-200,"width":1440,"height":72,"fill":"#FFFFFF","children":[
				{"id":"sn_txt","type":"text","name":"Brand","content":"Acme","fontSize":20,"fill":"#111111"}
			]},
			{"id":"home_scr","type":"frame","name":"Home Screen","x":0,"y":0,"width":1440,"height":900,"fill":"#F8FAFC","layout":"vertical","children":[
				{"id":"nav_inst","type":"ref","ref":"shared_nav","name":"Header","width":"fill_container"},
				{"id":"hero_txt","type":"text","name":"Hero","content":"Dashboard","fontSize":44,"fill":"#0F172A"}
			]}
		]`))
		e.Touch()
		e.Settings.Set("typesafe_enabled", true)
		e.Settings.Set("typesafe_auto_revise", false)
		e.Settings.Set("typesafe_min_score", jsonv.Int(75))
		e.Settings.Set("typesafe_api_key", "ts-byok-test")
		e.Settings.Set("typesafe_base_url", tsSrv.URL)
	})

	// 1. Editing shared_nav changes home_scr's ScreenVisualHash -> must evaluate home_scr!
	if err := Send(e, "Darken shared navbar", 1, "split", 1, "layout"); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, e)
	if got := len(getTSCalls()); got != 1 {
		t.Fatalf("expected 1 TypeSafe evaluation for pre-existing screen updated via shared component, got %d", got)
	}

	e.Locked(func() {
		// 2. Threshold mismatch invalidates cached screen decision (75 -> 94)
		e.Agent.TurnEditedPreTop = map[string]bool{"home_scr": true}
		e.Settings.Set("typesafe_min_score", jsonv.Int(94))
		plans := e.PlanTurnTypeSafeEvaluations("ag1")
		if len(plans) != 1 || plans[0].CachedRevise != nil {
			t.Fatalf("expected cache miss when active threshold changed from 75 to 94, got %+v", plans)
		}

		// 3. Huge float64 settings clamp before int conversion (1e20 -> 100 and 5)
		e.Settings.Set("typesafe_min_score", jsonv.Float(1e20))
		e.Settings.Set("typesafe_max_revisions", jsonv.Float(1e20))
		if got := e.TypeSafeMinScore(); got != 100 {
			t.Fatalf("expected 1e20 min_score to clamp to 100, got %d", got)
		}
		if got := e.TypeSafeMaxRevisions(); got != 5 {
			t.Fatalf("expected 1e20 max_revisions to clamp to 5, got %d", got)
		}

		// 4. Stale SourceDoc after document replacement must not record decisions
		staleDoc := &doc.Doc{Root: jsonv.NewObject()}
		_, _, _, sameDoc := e.RecordScreenEvaluation(staleDoc, "home_scr", 12345, &typesafe.Decision{Score: 50, Verdict: "revise"}, true)
		if sameDoc {
			t.Fatalf("expected RecordScreenEvaluation to reject stale SourceDoc")
		}
	})

	// 5. typesafe_test and typesafe_models must refuse sending saved key to an edited base_url
	if _, err := e.Dispatch(engine.Object("cmd", "typesafe_test", "base_url", "https://other.example.com", "api_key", engine.Mask)); err == nil {
		t.Fatalf("expected typesafe_test to reject masked saved key when base_url differs from saved base URL")
	}
	if _, err := e.Dispatch(engine.Object("cmd", "typesafe_models", "base_url", "https://other.example.com", "api_key", "")); err == nil {
		t.Fatalf("expected typesafe_models to reject empty key fallback when base_url differs from saved base URL")
	}
}
