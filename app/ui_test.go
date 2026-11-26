package main

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
)

func newTestApp(t *testing.T, opts ui.TesterOptions) (*NalarupaApp, *ui.Tester) {
	t.Helper()
	HeadlessClipboard = true
	t.Cleanup(func() { HeadlessClipboard = false })

	app := NewNalarupaApp(t.TempDir())
	app.Init()
	t.Cleanup(app.Close)

	opts.Dark = true
	tt := ui.NewTesterWith(app.View, DefaultWinW, DefaultWinH, opts)
	return app, tt
}

// assertAllInteractiveControlsNamed audits every accessible element on screen and fails if any
// interactive control (button, toggle, tab, textField, checkBox, radio, switch, menuButton, treeItem) lacks a Name.
func assertAllInteractiveControlsNamed(t *testing.T, tt *ui.Tester, screenDesc string) {
	t.Helper()
	interactiveRoles := map[ui.Role]bool{
		ui.RoleButton:       true,
		ui.RoleToggleButton: true,
		ui.RoleTab:          true,
		ui.RoleTextField:    true,
		ui.RoleCheckBox:     true,
		ui.RoleRadio:        true,
		ui.RoleSwitch:       true,
		ui.RoleMenuButton:   true,
		ui.RolePopUpButton:  true,
		ui.RoleComboBox:     true,
		ui.RoleTreeItem:     true,
	}
	all := tt.AccessAll()
	if len(all) == 0 {
		t.Fatalf("[%s] AccessAll returned empty accessibility tree", screenDesc)
	}
	for _, node := range all {
		if interactiveRoles[node.Role] && strings.TrimSpace(node.Name) == "" {
			t.Errorf("[%s] unnamed interactive control role=%v bounds=%+v placeholder=%q", screenDesc, node.Role, node.Bounds, node.Placeholder)
		}
	}
}

func TestUIHomeTemplatesPresentLayersAndAccessibilityAudit(t *testing.T) {
	app, tt := newTestApp(t, ui.TesterOptions{})

	// 1. Home screen & A11y audit
	if !tt.HasText("Nalarupa") || !tt.HasText("Start a new document") {
		t.Fatalf("Home screen missing brand/template header: texts=%v", tt.Texts())
	}
	assertAllInteractiveControlsNamed(t, tt, "Home")

	// 2. Click "New slide deck: 16:9 slides with tokens"
	if err := tt.Click("New slide deck: 16:9 slides with tokens"); err != nil {
		t.Fatalf("failed clicking slide deck template: %v", err)
	}
	if app.Screen != "editor" {
		t.Fatalf("expected Screen=editor, got %q", app.Screen)
	}
	assertAllInteractiveControlsNamed(t, tt, "Editor (Agent tab)")

	// 3. Open Agent Settings + Chat History + CookMenu and audit accessibility of each
	if err := tt.Click("Agent settings"); err != nil {
		t.Fatalf("click Agent settings failed: %v", err)
	}
	assertAllInteractiveControlsNamed(t, tt, "Editor (Agent settings open)")
	_ = tt.Click("Agent settings") // close settings

	if err := tt.Click("Let it cook: off"); err != nil {
		t.Fatalf("click Let it cook failed: %v", err)
	}
	assertAllInteractiveControlsNamed(t, tt, "Editor (CookMenu open)")
	if err := tt.Click("3"); err != nil {
		t.Fatalf("click cook count 3 failed: %v", err)
	}
	if app.AgentUI.CookDesigns != 3 {
		t.Fatalf("expected CookDesigns=3, got %d", app.AgentUI.CookDesigns)
	}
	tt.Key(0, ui.KeyEscape)

	// 4. Select a slide -> switches right tab to Design when transcript is empty, audit Design Inspector a11y!
	slides := app.topLevelSlides()
	if len(slides) < 2 {
		t.Fatalf("expected slides template to create >=2 slides, got %d", len(slides))
	}
	app.Select([]string{slides[0].ID})
	tt.Frame()
	if app.RightTab != "design" {
		t.Fatalf("expected selection with empty chat to open Design tab, got %q", app.RightTab)
	}
	assertAllInteractiveControlsNamed(t, tt, "Editor (Design Inspector)")

	// 5. Switch Left Panel to Slides tab, audit a11y, and test Present mode + Esc restoration
	if err := tt.Click("Slides"); err != nil {
		t.Fatalf("click Slides tab failed: %v", err)
	}
	assertAllInteractiveControlsNamed(t, tt, "Editor (Slides tab)")

	if err := tt.Click("Present (Ctrl+Enter)"); err != nil {
		t.Fatalf("click Present failed: %v", err)
	}
	if app.Screen != "present" {
		t.Fatalf("expected Screen=present, got %q", app.Screen)
	}
	assertAllInteractiveControlsNamed(t, tt, "Presentation mode")

	// Navigate slides in Present mode with Right / Left / End / Home / Esc
	tt.Key(0, ui.KeyRight)
	if app.Present.SlideIndex != 1 {
		t.Fatalf("expected SlideIndex=1 after Right arrow, got %d", app.Present.SlideIndex)
	}
	tt.Key(0, ui.KeyLeft)
	if app.Present.SlideIndex != 0 {
		t.Fatalf("expected SlideIndex=0 after Left arrow, got %d", app.Present.SlideIndex)
	}
	tt.Key(0, ui.KeyEscape)
	if app.Screen != "editor" {
		t.Fatalf("expected Esc to exit Present back to editor, got %q", app.Screen)
	}

	// 6. Switch back to Layers tab and test 20-level deep hierarchy stays within 248px panel width!
	_ = tt.Click("Layers")
	deepJS := `
let p = "` + slides[0].ID + `";
for (let d = 1; d <= 20; d++) {
  p = Insert(p, { id: "deep_" + d, type: "frame", name: "Level " + d, x: 4, y: 4, width: 100, height: 60 });
}
`
	if _, err := app.Bridge.ExecuteJS(deepJS); err != nil {
		t.Fatalf("deep hierarchy script failed: %v", err)
	}
	app.RefreshState()
	var allLayers []LayerRow
	_ = app.Bridge.Call("layers", nil, &allLayers)
	var leafID string
	for _, lr := range allLayers {
		if lr.Name == "Level 20" {
			leafID = lr.ID
			break
		}
	}
	if leafID == "" {
		t.Fatal("Level 20 not found in layers")
	}
	app.Select([]string{leafID})
	tt.Frame()
	if leafInfo, ok := tt.Access("Level 20"); !ok {
		t.Fatalf("expected Level 20 to be revealed and accessible in Layers tree")
	} else if leafInfo.Bounds.X+leafInfo.Bounds.W > LeftW+1 {
		t.Fatalf("Level 20 overflowed 248px Layers panel: bounds=%+v", leafInfo.Bounds)
	}
}

func TestUICanvasDrawingEditingFigmaPasteAndPen(t *testing.T) {
	app, tt := newTestApp(t, ui.TesterOptions{})

	if err := tt.Click("New design: A 1440 desktop frame"); err != nil {
		t.Fatal(err)
	}

	// Canvas bounds in 1440x900 window: X in [248, 1140], Y in [44, 900]
	cx, cy := float32(LeftW+200), float32(TopH+200)

	// 1. Draw a Rectangle on the canvas with tool R
	if err := tt.Click("Rectangle (R)"); err != nil {
		t.Fatal(err)
	}
	if app.Tool != "rectangle" {
		t.Fatalf("expected tool=rectangle, got %s", app.Tool)
	}
	tt.Press(cx, cy)
	tt.Move(cx+120, cy+80)
	tt.Release(cx+120, cy+80)

	if app.Tool != "move" || len(app.State.Selection) != 1 {
		t.Fatalf("expected drawing rectangle to select it and return to move tool: tool=%s sel=%v", app.Tool, app.State.Selection)
	}
	rectID := app.State.Selection[0]

	// 2. Nudge with Shift+Right (10px) and verify box moved, then Undo
	var b0 []Box
	_ = app.Bridge.Call("boxes", map[string]any{"ids": []string{rectID}}, &b0)
	tt.Key(ui.Shift, ui.KeyRight)
	var b1 []Box
	_ = app.Bridge.Call("boxes", map[string]any{"ids": []string{rectID}}, &b1)
	if len(b0) == 1 && len(b1) == 1 && b1[0].X <= b0[0].X {
		t.Fatalf("expected Shift+Right nudge to increase X: before=%v after=%v", b0[0].X, b1[0].X)
	}
	tt.Key(ui.Ctrl, ui.KeyZ)
	var bUndo []Box
	_ = app.Bridge.Call("boxes", map[string]any{"ids": []string{rectID}}, &bUndo)
	if len(bUndo) == 1 && bUndo[0].X != b0[0].X {
		t.Fatalf("expected Ctrl+Z to restore X=%v, got %v", b0[0].X, bUndo[0].X)
	}

	// 3. Copy & Paste + Figma HTML clipboard paste
	app.CopySelection()
	app.PasteClipboard()
	if len(app.State.Selection) != 1 || app.State.Selection[0] == rectID {
		t.Fatalf("expected PasteClipboard to create and select a duplicate node, got %v", app.State.Selection)
	}

	figmaFixture := filepath.Join("..", "core-go", "testdata", "figma", "card.html")
	if figmaBytes, err := os.ReadFile(figmaFixture); err == nil {
		tt.SetClipboardData(transfer.New(transfer.NewItem(
			transfer.Bytes(transfer.HTML, figmaBytes),
		)))
		prevVer := app.State.Version
		app.PasteClipboard()
		if app.State.Version <= prevVer {
			t.Fatalf("expected Figma HTML clipboard paste to insert nodes and advance document version")
		}
	}

	// 4. Pen tool: draw a 3-point triangle path and close it on first anchor!
	if err := tt.Click("Pen (P)"); err != nil {
		t.Fatal(err)
	}
	px, py := float32(LeftW+420), float32(TopH+320)
	tt.ClickAt(px, py)
	tt.ClickAt(px+90, py)
	tt.ClickAt(px+45, py+75)
	tt.ClickAt(px, py) // close contour on start anchor!

	if app.Canvas.pen == nil || app.Canvas.pen.pathID == "" {
		t.Fatalf("expected Pen clicks to create a vector path node")
	}
	if len(app.Canvas.pen.contours) != 1 || !app.Canvas.pen.contours[0].Closed || len(app.Canvas.pen.contours[0].Anchors) != 3 {
		t.Fatalf("expected closed 3-anchor contour, got %+v", app.Canvas.pen.contours)
	}
	// Exit pen edit with Escape twice
	tt.Key(0, ui.KeyEscape)
	tt.Key(0, ui.KeyEscape)
	tt.Key(0, ui.KeyEscape)
	if app.Canvas.pen != nil {
		t.Fatalf("expected Escape sequence to exit Pen edit mode")
	}

	// 5. Draw inside an offset unrotated frame and verify parent-local coordinates!
	var parentFrameID string
	var frameRes struct {
		ID string `json:"id"`
	}
	if err := app.Bridge.Call("insert", map[string]any{
		"parent": "document",
		"node": map[string]any{
			"type":   "frame",
			"name":   "Offset Frame",
			"x":      200.0,
			"y":      150.0,
			"width":  300.0,
			"height": 240.0,
			"fill":   "#FFFFFF",
		},
	}, &frameRes); err != nil {
		t.Fatal(err)
	}
	parentFrameID = frameRes.ID
	app.FitTo([]string{parentFrameID})
	tt.Frame()

	if err := tt.Click("Rectangle (R)"); err != nil {
		t.Fatal(err)
	}
	fx0, fy0 := app.docToScreen(240, 180)
	fx1, fy1 := app.docToScreen(320, 240)
	tt.Press(float32(LeftW+fx0), float32(TopH+fy0))
	tt.Move(float32(LeftW+fx1), float32(TopH+fy1))
	tt.Release(float32(LeftW+fx1), float32(TopH+fy1))

	if len(app.State.Selection) != 1 {
		t.Fatalf("expected drawn child rectangle to be selected, got %v", app.State.Selection)
	}
	var childND NodeDetail
	if err := app.Bridge.Call("node", map[string]any{"id": app.State.Selection[0]}, &childND); err != nil {
		t.Fatal(err)
	}
	if childND.Parent != parentFrameID {
		t.Fatalf("expected drawn rectangle parent=%s, got %s", parentFrameID, childND.Parent)
	}
	if gotX, gotY := asFloat(childND.Node["x"]), asFloat(childND.Node["y"]); math.Abs(gotX-40) > 2 || math.Abs(gotY-30) > 2 {
		t.Fatalf("expected parent-local (x,y) near (40,30), got (%v,%v)", gotX, gotY)
	}
}

func TestUIFileLifecycleUnsavedPromptAndAgentPanel(t *testing.T) {
	app, tt := newTestApp(t, ui.TesterOptions{})

	if err := tt.Click("New empty canvas: Start from nothing"); err != nil {
		t.Fatal(err)
	}

	// Draw a frame so document becomes Dirty
	_ = tt.Click("Frame (F / A)")
	tt.Press(float32(LeftW+160), float32(TopH+160))
	tt.Move(float32(LeftW+360), float32(TopH+300))
	tt.Release(float32(LeftW+360), float32(TopH+300))
	if !app.State.Dirty {
		t.Fatal("expected drawing a frame to mark document Dirty")
	}

	// Navigate Home -> click another template -> Unsaved changes prompt appears!
	if err := tt.Click("Home"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("New mobile app: A 393×852 phone frame"); err != nil {
		t.Fatal(err)
	}
	if app.UnsavedPending == nil || !tt.HasText("Without saving they are lost.") {
		t.Fatal("expected Unsaved changes prompt when starting new template over dirty doc")
	}

	// Cancel keeps us on Home with original doc intact
	if err := tt.Click("Cancel"); err != nil {
		t.Fatal(err)
	}
	if app.UnsavedPending != nil {
		t.Fatal("Cancel should dismiss UnsavedPending")
	}

	// Save document to temp dir, verify clean state and recent entry
	savePath := filepath.Join(t.TempDir(), "TestSavedDeck.nala")
	app.Screen = "editor"
	tt.Frame()
	if !app.SaveDocument(savePath) {
		t.Fatal("SaveDocument failed")
	}
	if app.State.Dirty {
		t.Fatal("expected Dirty=false after SaveDocument")
	}

	// Agent Panel: draft persistence across Design <-> Agent tab switch, Plan fold/unfold, Permission card
	_ = tt.Click("Agent")
	app.AgentUI.Draft = "Keep this prompt across tab switches"
	tt.Frame()
	_ = tt.Click("Design")
	_ = tt.Click("Agent")
	if app.AgentUI.Draft != "Keep this prompt across tab switches" {
		t.Fatalf("draft lost across Design/Agent tab switch: %q", app.AgentUI.Draft)
	}

	// Inject Plan todos and Permission request into AgentStore and verify UI controls
	app.Store.ApplyEvents([]map[string]any{
		{"type": "agent", "kind": "start", "agent": map[string]any{"id": "agent", "name": "Designer", "color": "#FF6A3D"}},
		{"type": "agent", "kind": "todos", "todos": []any{
			map[string]any{"content": "Build header", "status": "completed"},
			map[string]any{"content": "Add pricing cards", "status": "in_progress"},
		}},
		{"type": "agent", "kind": "permission", "id": "perm-1", "agent": map[string]any{"id": "agent", "name": "Designer", "color": "#FF6A3D"}, "tool": "Write", "input": map[string]any{"file_path": "tokens.json"}},
	}, 1000)
	tt.Frame()

	if !tt.HasText("Add pricing cards") || !tt.HasText("Designer wants to run Write") {
		t.Fatalf("expected Plan todos and PermissionCard to render in Agent panel; texts=%v", tt.Texts())
	}
	assertAllInteractiveControlsNamed(t, tt, "Agent panel with Plan & PermissionCard")
}

func TestUIReducedMotionPreference(t *testing.T) {
	appReduced, ttReduced := newTestApp(t, ui.TesterOptions{
		Preferences: ui.Preferences{ReduceMotion: true},
	})
	if appReduced.lastReducedMotion == nil || !*appReduced.lastReducedMotion {
		t.Fatal("expectedTester ReduceMotion: true to propagate to NalarupaApp and core reduce_motion")
	}

	// Spawn an agent cursor and move target: under reduced motion, cursor jumps immediately to (TX, TY) without continuous 60fps frames!
	_ = ttReduced.Click("New design: A 1440 desktop frame")
	p := AgentProfile{ID: "ag1", Name: "Designer", Color: "#FF6A3D"}
	nowMS := float64(time.Now().UnixMilli())
	appReduced.Store.Poke(p, nowMS-1000) // past 200ms fade-in
	cur := appReduced.Store.Cursors["ag1"]
	cur.X, cur.Y = 100, 100
	cur.TX, cur.TY = 500, 350
	appReduced.Store.Running["ag1"] = true

	ttReduced.Frame()
	if cur.X != 500 || cur.Y != 350 {
		t.Fatalf("expected reduced-motion cursor to jump directly to (500, 350), got (%v, %v)", cur.X, cur.Y)
	}
	if ttReduced.FramePending() {
		t.Fatal("expected zero continuous animation frames pending when ReduceMotion is true after fade-in")
	}
}

func TestUIAgentModelsEffortAndMultiSkills(t *testing.T) {
	skillsRoot := filepath.Join(t.TempDir(), "skills")
	for name, body := range map[string]string{
		"ui-craft":   "---\nname: ui-craft\ndescription: Polished typography and spacing\n---\nUse tight visual hierarchy.",
		"a11y-audit": "---\nname: a11y-audit\ndescription: WCAG contrast and keyboard checks\n---\nVerify all focus states.",
	} {
		dir := filepath.Join(skillsRoot, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("NALARUPA_SKILLS_ONLY", "1")
	t.Setenv("NALARUPA_SKILLS_DIR", skillsRoot)

	app, tt := newTestApp(t, ui.TesterOptions{})
	if err := tt.Click("New design: A 1440 desktop frame"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Agent"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()

	// 1. Model picker includes latest Fable 5.2 and CLI aliases with valid accessibility names.
	if err := tt.Click("Model: Default"); err != nil {
		t.Fatalf("open model picker: %v", err)
	}
	if !tt.HasText("Fable 5.2") || !tt.HasText("Fable (latest)") {
		t.Fatalf("expected latest models in picker, texts=%v", tt.Texts())
	}
	assertAllInteractiveControlsNamed(t, tt, "Agent model picker popover")
	if err := tt.Click("Fable 5.2"); err != nil {
		t.Fatalf("select Fable 5.2: %v", err)
	}
	if app.AgentUI.AgentModel != "claude-fable-5-2" {
		t.Fatalf("AgentModel = %q, want claude-fable-5-2", app.AgentUI.AgentModel)
	}

	// 2. Composer Reasoning effort picker updates agent_effort setting across backends.
	if err := tt.Click("Reasoning effort: High"); err != nil {
		t.Fatalf("open effort picker: %v", err)
	}
	assertAllInteractiveControlsNamed(t, tt, "Agent effort picker popover")
	if err := tt.Click("Extra high"); err != nil {
		t.Fatalf("select Extra high effort: %v", err)
	}
	if app.AgentUI.Effort != "xhigh" {
		t.Fatalf("Effort = %q, want xhigh", app.AgentUI.Effort)
	}
	var stMap map[string]any
	if err := app.Bridge.Call("settings_get", nil, &stMap); err != nil || stMap["agent_effort"] != "xhigh" {
		t.Fatalf("settings agent_effort = %v (err=%v), want xhigh", stMap["agent_effort"], err)
	}

	// 3. Skills popover supports toggling multiple skills and shows removable active skill chips.
	if err := tt.Click("Agent skills"); err != nil {
		t.Fatalf("open skills popover: %v", err)
	}
	assertAllInteractiveControlsNamed(t, tt, "Agent skills popover")
	if err := tt.Click("Toggle skill ui-craft"); err != nil {
		t.Fatalf("toggle ui-craft: %v", err)
	}
	if err := tt.Click("Toggle skill a11y-audit"); err != nil {
		t.Fatalf("toggle a11y-audit: %v", err)
	}
	if !strings.Contains(app.AgentUI.Draft, "/ui-craft") || !strings.Contains(app.AgentUI.Draft, "/a11y-audit") {
		t.Fatalf("expected both skills in Draft, got %q", app.AgentUI.Draft)
	}
	app.AgentUI.SkillsOpen = false
	tt.Frame()
	assertAllInteractiveControlsNamed(t, tt, "Agent composer with active multi-skill chips")

	// Removing one chip leaves the other intact.
	if err := tt.Click("Remove skill ui-craft"); err != nil {
		t.Fatalf("remove ui-craft chip: %v", err)
	}
	if strings.Contains(app.AgentUI.Draft, "/ui-craft") || !strings.Contains(app.AgentUI.Draft, "/a11y-audit") {
		t.Fatalf("unexpected Draft after removing ui-craft: %q", app.AgentUI.Draft)
	}

	// 4. Typing /prefix triggers inline slash skill autocomplete with full naming.
	app.AgentUI.Draft = "/ui-"
	tt.Frame()
	if !tt.HasText("/ui-craft") {
		t.Fatalf("expected /ui-craft in slash autocomplete, texts=%v", tt.Texts())
	}
	assertAllInteractiveControlsNamed(t, tt, "Agent slash skill autocomplete")
	if err := tt.Click("Use skill ui-craft"); err != nil {
		t.Fatalf("pick slash autocomplete option: %v", err)
	}
	if app.AgentUI.Draft != "/ui-craft " {
		t.Fatalf("Draft after slash autocomplete = %q, want %q", app.AgentUI.Draft, "/ui-craft ")
	}
}
