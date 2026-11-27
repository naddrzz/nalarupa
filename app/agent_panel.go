package main

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

var agentSuggestions = []string{
	"a SaaS pricing page with 3 tiers and an annual toggle",
	"a mobile coffee-shop ordering screen with warm editorial type",
	"a 5-slide pitch deck for a climate hardware startup",
	"a personal finance dashboard with spending breakdown cards",
	"an independent bookshop landing page hero and featured shelf",
	"a music player mobile screen with tracklist and lyrics",
}

type claudeModelOpt struct {
	ID    string
	Label string
}

var builtinClaudeModels = []claudeModelOpt{
	{ID: "claude-fable-5-2", Label: "Fable 5.2"},
	{ID: "claude-opus-5-5", Label: "Opus 5.5"},
	{ID: "claude-sonnet-5-5", Label: "Sonnet 5.5"},
	{ID: "claude-haiku-5-5", Label: "Haiku 5.5"},
}

var claudeCLIAliases = []claudeModelOpt{
	{ID: "fable", Label: "Fable (latest)"},
	{ID: "opus", Label: "Opus (latest)"},
	{ID: "sonnet", Label: "Sonnet (latest)"},
	{ID: "haiku", Label: "Haiku (latest)"},
}

var fastCapableModels = []string{
	"claude-fable-5-2",
	"claude-opus-5-5",
	"claude-opus-5",
	"claude-opus-4-8",
	"fable",
	"opus",
}

// ProviderConfig mirrors core-go BYOK provider configuration.
type ProviderConfig struct {
	ID            string   `json:"id,omitempty"`
	Name          string   `json:"name"`
	Format        string   `json:"format"` // "chat" | "responses" | "anthropic"
	BaseURL       string   `json:"base_url"`
	APIKey        string   `json:"api_key,omitempty"`
	Models        []string `json:"models"`
	Vision        bool     `json:"vision"`
	ContextWindow int      `json:"context_window,omitempty"`
}

// ChatSummary represents a saved chat entry returned by "chat_list".
type ChatSummary struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	UpdatedAt int64  `json:"updatedAt"`
}

type cachedToolImage struct {
	bmp    *ui.Bitmap
	failed bool
}

const maxCachedToolImages = 64

// AgentPanelState holds persistent UI state for the Agent right-hand panel.
type AgentPanelState struct {
	Draft         string
	InputHandle   ui.Handle
	Parallel      int    // 1..4
	ParallelMode  string // "split" | "side"
	Backend       string // "claude-code" | "api" | "provider"
	AgentModel    string
	ProviderModel string
	Fast          bool
	CookDesigns   int    // 1..6
	CookIterate   string // "layout" | "style"
	AutoPerms     bool
	HasAPIKey     bool
	BaseURL       string
	Effort        string
	ActiveProvID  string
	Providers     []ProviderConfig

	APIModels          []string
	FetchingModels     bool
	ModelFetchSeq      uint64
	ActiveFetchReqID   string
	ActiveFetchEditSeq uint64
	ActiveFetchProvID  string
	CustomModelDraft   string

	Skills         []SkillItem
	SkillsLoaded   bool
	SkillsLoading  bool
	SkillsReqSeq   uint64
	SkillsOpen     bool
	SkillsFilter   string
	SkillsScroll   ui.ScrollState
	ModelScroll    ui.ScrollState
	SlashIndex     int
	DismissedSlash string

	SettingsOpen bool
	HistoryOpen  bool
	ModelOpen    bool
	EffortOpen   bool
	CookOpen     bool
	PlanOpen     bool

	HistoryBtnHandle ui.Handle
	CookBtnHandle    ui.Handle
	ModelBtnHandle   ui.Handle
	EffortBtnHandle  ui.Handle
	SkillsBtnHandle  ui.Handle
	CanvasAttachBtn  ui.Handle

	PendingContexts  []PendingCanvasContext
	PendingImages    []PendingImageAttachment
	NextAttachSeq    uint64
	CanvasAttachOpen bool
	PickingCanvasCtx bool
	attachMu         sync.Mutex
	attachPending    int
	attachBatches    []attachBatchResult
	pickImageFilesFn func(onFiles func([]string))

	Chats          []ChatSummary
	CurrentChatID  string
	ArmedRemoveID  string
	ConfirmClear   bool
	AuthLoggingIn  bool

	EditingProv          *ProviderConfig
	ProvEditSeq          uint64
	ProvKeyDraft         string
	ProvModelsCSV        string
	ProvContextDraft     string
	TestingProvID        string
	GlobalContextWindow  int
	GlobalContextDraft   string
	ResolvedContextLimit int

	TypeSafeEnabled        bool
	TypeSafeAutoRevise     bool
	TypeSafeMaxRevisions   int
	HasTypeSafeKey         bool
	HasSavedTypeSafeKey    bool
	TypeSafeModel          string
	TypeSafeBaseURL        string
	TypeSafeMinScore       int
	TypeSafeModels         []string
	TypeSafeCustomModDraft string
	TypeSafeFetchingModels bool

	ExpandedCards      map[int]bool
	FullOutputCards    map[int]bool
	RevealedItems      map[int]bool
	StreamMountedMinID int
	ToolInputCache     map[int]string
	ImageCache         map[string]cachedToolImage
	ImageOrder         []string

	TranscriptScroll ui.ScrollState
	SettingsScroll   ui.ScrollState
	PlanScroll       ui.ScrollState
	StickToBottom    bool
	LastScrollMaxY   float32
	LastItemCount    int
	LastStreamSig    uint64
	LastSubmitAt     time.Time

	MDOpts        markdown.Options
	ThoughtMDOpts markdown.Options
}

func (a *NalarupaApp) initAgentPanelState() {
	a.AgentUI = AgentPanelState{
		Parallel:        1,
		ParallelMode:    "split",
		Backend:         "claude-code",
		AgentModel:      "",
		CookDesigns:     1,
		CookIterate:     "layout",
		Effort:               "high",
		TypeSafeAutoRevise:   true,
		TypeSafeMaxRevisions: 2,
		TypeSafeModel:        "jev-latest",
		TypeSafeMinScore:     75,
		PlanOpen:             true,
		ExpandedCards:   map[int]bool{},
		FullOutputCards: map[int]bool{},
		RevealedItems:   map[int]bool{},
		ToolInputCache:  map[int]string{},
		ImageCache:      map[string]cachedToolImage{},
		StickToBottom:   true,
		MDOpts: markdown.Options{
			Reveal: &markdown.Reveal{
				CatchUp: 145 * time.Millisecond,
				Fade:    175 * time.Millisecond,
			},
		},
		ThoughtMDOpts: markdown.Options{
			Reveal: &markdown.Reveal{
				CatchUp: 130 * time.Millisecond,
				Fade:    160 * time.Millisecond,
			},
		},
	}
}

func (a *NalarupaApp) clearAgentImageCache() {
	for _, entry := range a.AgentUI.ImageCache {
		if entry.bmp != nil {
			entry.bmp.ReleasePixels()
		}
	}
	a.AgentUI.ImageCache = map[string]cachedToolImage{}
	a.AgentUI.ImageOrder = nil
	a.AgentUI.FullOutputCards = map[int]bool{}
	a.AgentUI.RevealedItems = map[int]bool{}
	a.AgentUI.ToolInputCache = map[int]string{}
}

func (a *NalarupaApp) reloadAgentSettings() {
	var s map[string]any
	if !a.Bridge.TryCall("settings_get", nil, &s) || s == nil {
		return
	}
	if b, ok := s["backend"].(string); ok && b != "" {
		a.AgentUI.Backend = b
	} else if b, ok := s["agent_backend"].(string); ok && b != "" {
		a.AgentUI.Backend = b
	}
	if m, ok := s["agent_model"].(string); ok {
		a.AgentUI.AgentModel = m
	} else {
		a.AgentUI.AgentModel = ""
	}
	if pm, ok := s["provider_model"].(string); ok {
		a.AgentUI.ProviderModel = pm
	}
	if fast, ok := s["agent_fast"].(bool); ok {
		a.AgentUI.Fast = fast
	}
	if d := int(asFloat(s["agent_designs"])); d >= 1 && d <= 6 {
		a.AgentUI.CookDesigns = d
	}
	if it, ok := s["agent_iterate"].(string); ok && (it == "layout" || it == "style") {
		a.AgentUI.CookIterate = it
	}
	if ap, ok := s["agent_auto_permissions"].(bool); ok {
		a.AgentUI.AutoPerms = ap
	}
	if k, ok := s["anthropic_api_key"].(string); ok && k != "" {
		a.AgentUI.HasAPIKey = true
	} else {
		a.AgentUI.HasAPIKey = false
	}
	a.AgentUI.BaseURL, _ = s["anthropic_base_url"].(string)
	if eff, ok := s["agent_effort"].(string); ok && eff != "" {
		a.AgentUI.Effort = eff
	}
	a.AgentUI.ActiveProvID, _ = s["agent_provider"].(string)
	var provs []ProviderConfig
	if rawP, ok := s["providers"]; ok && rawP != nil {
		redecode(rawP, &provs)
	}
	a.AgentUI.Providers = provs
	var apiMods []string
	if rawM, ok := s["anthropic_models"]; ok && rawM != nil {
		redecode(rawM, &apiMods)
	}
	a.AgentUI.APIModels = apiMods
	a.AgentUI.GlobalContextWindow = int(asFloat(s["agent_context_window"]))
	if a.AgentUI.GlobalContextWindow > 0 {
		a.AgentUI.GlobalContextDraft = strconv.Itoa(a.AgentUI.GlobalContextWindow)
	} else {
		a.AgentUI.GlobalContextDraft = ""
	}
	if rl := a.Bridge.EffectiveContextWindowDirect(); rl > 0 {
		a.AgentUI.ResolvedContextLimit = rl
	} else if rl2 := int(asFloat(s["context_window_limit"])); rl2 > 0 {
		a.AgentUI.ResolvedContextLimit = rl2
	} else {
		a.AgentUI.ResolvedContextLimit = defaultUIContextWindow(a.AgentUI.Backend, a.effectiveModelID())
	}

	a.AgentUI.TypeSafeEnabled, _ = s["typesafe_enabled"].(bool)
	if ar, hasAR := s["typesafe_auto_revise"].(bool); hasAR {
		a.AgentUI.TypeSafeAutoRevise = ar
	} else {
		a.AgentUI.TypeSafeAutoRevise = true
	}
	if mr := int(asFloat(s["typesafe_max_revisions"])); mr >= 1 && mr <= 5 {
		a.AgentUI.TypeSafeMaxRevisions = mr
	} else {
		a.AgentUI.TypeSafeMaxRevisions = 2
	}
	a.AgentUI.HasTypeSafeKey, _ = s["has_typesafe_key"].(bool)
	if tsk, ok := s["typesafe_api_key"].(string); ok && tsk != "" {
		a.AgentUI.HasSavedTypeSafeKey = true
		a.AgentUI.HasTypeSafeKey = true
	} else {
		a.AgentUI.HasSavedTypeSafeKey = false
	}
	if tsm, ok := s["typesafe_model"].(string); ok && strings.TrimSpace(tsm) != "" {
		a.AgentUI.TypeSafeModel = strings.TrimSpace(tsm)
	} else {
		a.AgentUI.TypeSafeModel = "jev-latest"
	}
	a.AgentUI.TypeSafeBaseURL, _ = s["typesafe_base_url"].(string)
	if ms := int(asFloat(s["typesafe_min_score"])); ms >= 1 && ms <= 100 {
		a.AgentUI.TypeSafeMinScore = ms
	} else {
		a.AgentUI.TypeSafeMinScore = 75
	}
	var tsMods []string
	if rawTSM, ok := s["typesafe_models_list"]; ok && rawTSM != nil {
		redecode(rawTSM, &tsMods)
	}
	a.AgentUI.TypeSafeModels = tsMods
}

func formatTokenCount(n int) string {
	if n <= 0 {
		return "0"
	}
	if n >= 1_000_000 {
		if n%1_000_000 == 0 {
			return fmt.Sprintf("%dM", n/1_000_000)
		}
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000.0)
	}
	if n >= 1000 {
		if n%1000 == 0 {
			return fmt.Sprintf("%dk", n/1000)
		}
		return fmt.Sprintf("%.1fk", float64(n)/1000.0)
	}
	return strconv.Itoa(n)
}

func parseUIContextWindow(raw string) (int, bool) {
	s := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(raw, ",", "")))
	s = strings.ReplaceAll(s, "_", "")
	if s == "" || s == "auto" || s == "default" || s == "0" {
		return 0, true
	}
	mult := 1.0
	switch {
	case strings.HasSuffix(s, "m"):
		mult = 1_000_000
		s = strings.TrimSpace(strings.TrimSuffix(s, "m"))
	case strings.HasSuffix(s, "k"):
		mult = 1_000
		s = strings.TrimSpace(strings.TrimSuffix(s, "k"))
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f <= 0 {
		return 0, false
	}
	scaled := f * mult
	if math.IsNaN(scaled) || math.IsInf(scaled, 0) {
		return 0, false
	}
	clamped := math.Min(math.Max(math.Round(scaled), 4096), 4_000_000)
	return int(clamped), true
}

func defaultUIContextWindow(backend, modelID string) int {
	m := strings.ToLower(strings.TrimSpace(modelID))
	base := m
	if slash := strings.LastIndexByte(m, '/'); slash >= 0 {
		base = m[slash+1:]
	}
	switch {
	case strings.Contains(m, "1m") || strings.Contains(m, "gemini-"):
		return 1_000_000
	case strings.Contains(m, "gpt-5") || strings.Contains(m, "gpt-6"):
		return 400_000
	case strings.Contains(m, "qwen3") || strings.Contains(m, "qwen4") || strings.Contains(m, "kimi") || strings.Contains(m, "codestral"):
		return 262_144
	case strings.Contains(m, "claude") || strings.Contains(m, "fable") || strings.Contains(m, "opus") ||
		strings.Contains(m, "sonnet") || strings.Contains(m, "haiku") || strings.HasPrefix(base, "o3") || strings.HasPrefix(base, "o4"):
		return 200_000
	case backend == "api" || backend == "claude-code":
		return 200_000
	default:
		return 128_000
	}
}

func (a *NalarupaApp) effectiveContextLimit() int {
	globalCap := a.AgentUI.GlobalContextWindow
	if a.AgentUI.Backend == "provider" {
		if ap := a.activeProvider(); ap != nil && ap.ContextWindow > 0 {
			if globalCap > 0 && globalCap < ap.ContextWindow {
				return globalCap
			}
			return ap.ContextWindow
		}
	}
	if globalCap > 0 {
		return globalCap
	}
	if a.AgentUI.ResolvedContextLimit > 0 {
		return a.AgentUI.ResolvedContextLimit
	}
	return defaultUIContextWindow(a.AgentUI.Backend, a.effectiveModelID())
}

func (a *NalarupaApp) effectiveContextStats() (used, limit, remaining int, ratio float64) {
	limit = max(4096, a.effectiveContextLimit())
	used = a.Store.ContextUsed + a.Store.ContextStreamExtra
	if d := strings.TrimSpace(a.AgentUI.Draft); d != "" {
		used += int(math.Ceil(float64(len(d)) / 3.6))
	}
	if len(a.AgentUI.PendingImages) > 0 {
		used += len(a.AgentUI.PendingImages) * 1250
	}
	if len(a.AgentUI.PendingContexts) > 0 {
		used += len(a.AgentUI.PendingContexts) * 450
	}
	if used < 0 {
		used = 0
	}
	remaining = max(0, limit-used)
	ratio = max(0.0, min(1.0, float64(used)/float64(limit)))
	return used, limit, remaining, ratio
}

func (a *NalarupaApp) setSetting(key string, val any) {
	a.Bridge.TryCall("settings_set", map[string]any{"key": key, "value": val}, nil)
	a.reloadAgentSettings()
}

func (a *NalarupaApp) refreshChatHistory() {
	var res struct {
		Current *string       `json:"current"`
		Chats   []ChatSummary `json:"chats"`
	}
	if a.Bridge.TryCall("chat_list", nil, &res) {
		a.AgentUI.Chats = res.Chats
		if res.Current != nil {
			a.AgentUI.CurrentChatID = *res.Current
		} else {
			a.AgentUI.CurrentChatID = ""
		}
	}
}

func (a *NalarupaApp) refreshClaudeAuth() {
	var st ClaudeAuth
	if a.Bridge.TryCall("claude_auth_status", nil, &st) {
		a.Store.SetClaudeAuth(st)
	}
}

func (a *NalarupaApp) effectiveModelID() string {
	switch a.AgentUI.Backend {
	case "provider":
		if a.AgentUI.ProviderModel != "" {
			return a.AgentUI.ProviderModel
		}
		if p := a.activeProvider(); p != nil && len(p.Models) > 0 {
			return p.Models[0]
		}
		return ""
	case "api":
		if a.AgentUI.AgentModel != "" {
			return a.AgentUI.AgentModel
		}
		return "claude-opus-5-5"
	default:
		return a.AgentUI.AgentModel
	}
}

func (a *NalarupaApp) effectiveModelLabel() string {
	id := a.effectiveModelID()
	if a.AgentUI.Backend == "claude-code" && id == "" {
		return "Default"
	}
	for _, m := range builtinClaudeModels {
		if m.ID == id {
			return m.Label
		}
	}
	for _, m := range claudeCLIAliases {
		if m.ID == id {
			return m.Label
		}
	}
	if id == "claude-fable-5-1" {
		return "Fable 5.1"
	}
	if id == "" {
		return "Default"
	}
	return id
}

func (a *NalarupaApp) activeProvider() *ProviderConfig {
	for i := range a.AgentUI.Providers {
		if a.AgentUI.Providers[i].ID == a.AgentUI.ActiveProvID {
			return &a.AgentUI.Providers[i]
		}
	}
	if len(a.AgentUI.Providers) > 0 {
		return &a.AgentUI.Providers[0]
	}
	return nil
}

func (a *NalarupaApp) submitAgentPrompt(prompt string, returnFocusToCanvas bool) bool {
	trimmed := strings.TrimSpace(prompt)
	if trimmed == "" && !a.hasPendingAttachments() {
		return false
	}
	now := time.Now()
	if now.Sub(a.AgentUI.LastSubmitAt) < 80*time.Millisecond {
		return false
	}

	payload := map[string]any{
		"text":     trimmed,
		"parallel": a.AgentUI.Parallel,
		"mode":     a.AgentUI.ParallelMode,
		"designs":  a.AgentUI.CookDesigns,
		"iterate":  a.AgentUI.CookIterate,
	}
	if len(a.AgentUI.PendingContexts) > 0 {
		ctxs := make([]map[string]any, 0, len(a.AgentUI.PendingContexts))
		for _, ctx := range a.AgentUI.PendingContexts {
			m := map[string]any{
				"kind":     ctx.Kind,
				"id":       ctx.ID,
				"name":     ctx.Name,
				"nodeType": ctx.NodeType,
				"w":        ctx.W,
				"h":        ctx.H,
			}
			if ctx.Viewport != nil {
				m["viewport"] = []float64{ctx.Viewport[0], ctx.Viewport[1], ctx.Viewport[2], ctx.Viewport[3]}
			}
			ctxs = append(ctxs, m)
		}
		payload["contexts"] = ctxs
	}
	if len(a.AgentUI.PendingImages) > 0 {
		imgs := make([]map[string]any, 0, len(a.AgentUI.PendingImages))
		for _, im := range a.AgentUI.PendingImages {
			imgs = append(imgs, map[string]any{
				"name":      im.Name,
				"mediaType": im.MediaType,
				"data":      im.Data,
				"width":     im.Width,
				"height":    im.Height,
			})
		}
		payload["images"] = imgs
	}

	if err := a.Bridge.Call("agent_send", payload, nil); err != nil {
		a.ShowToast(err.Error(), true)
		return false
	}
	a.AgentUI.LastSubmitAt = now
	a.AgentUI.Draft = ""
	a.AgentUI.DismissedSlash = ""
	a.AgentUI.SlashIndex = 0
	a.AgentUI.PickingCanvasCtx = false
	a.AgentUI.CanvasAttachOpen = false
	a.clearPendingAttachments()
	a.AgentUI.StickToBottom = true
	a.PollNow(float64(now.UnixMilli()))
	if returnFocusToCanvas {
		a.CanvasHandle.Focus()
	}
	return true
}

func (a *NalarupaApp) viewAgentPanel(c *ui.Context) {
	a.ensureSkillsLoaded(false)
	reduced := EffectiveReducedMotion(c)
	nowMS := time.Now().UnixMilli()

	panel := ui.Column(c.Key("agent-panel")).FillWidth().Grow(1).MinHeight(0)
	dragOver := panel.FileDragOver()
	if dropped := panel.DroppedFiles(); len(dropped) > 0 {
		a.AttachImageFiles(dropped)
	}
	panel.Children(func() {
		// 1. Header bar (fixed 40px)
		var histBtn ui.Element
		ui.Row(c.Key("agent-header")).
			FillWidth().
			Height(40).
			Shrink(0).
			Padding(0, 10).
			Gap(4).
			AlignItems(ui.Center).
			BorderWidth(0, 0, 1, 0).
			BorderColor(C.Border).
			Children(func() {
				ui.Icon(c, IconSVG("sparkles")).FontSize(14).TextColor(C.Accent).Shrink(0)
				ui.Text(c, "Design agent").FontSize(12).FontWeight(600).TextColor(C.Text).Grow(1)

				histBtn = IconBtn(c, "agent-history", "history", "Chat history", 26, a.AgentUI.HistoryOpen)
				histBtn.Bind(&a.AgentUI.HistoryBtnHandle)
				if histBtn.Clicked() {
					a.AgentUI.HistoryOpen = !a.AgentUI.HistoryOpen
					if a.AgentUI.HistoryOpen {
						a.AgentUI.ArmedRemoveID = ""
						a.AgentUI.ConfirmClear = false
						a.refreshChatHistory()
					}
				}

				newBtn := IconBtn(c, "agent-new", "plus", "New agent chat", 26, false)
				if newBtn.Clicked() {
					if err := a.Bridge.Call("agent_reset", nil, nil); err != nil {
						a.ShowToast(err.Error(), true)
					} else {
						a.Store.ResetChat()
						a.AgentUI.ExpandedCards = map[int]bool{}
						a.clearAgentImageCache()
					}
				}

				setBtn := IconBtn(c, "agent-settings", "settings", "Agent settings", 26, a.AgentUI.SettingsOpen)
				if setBtn.Clicked() {
					a.AgentUI.SettingsOpen = !a.AgentUI.SettingsOpen
					if a.AgentUI.SettingsOpen {
						a.reloadAgentSettings()
						if a.AgentUI.Backend == "claude-code" {
							a.refreshClaudeAuth()
						}
					}
				}
			})

		// ChatHistory Popover (origin-aware from top-right history button)
		if histBtn.Valid() {
			pop := AnimatedPopover(c, histBtn, &a.AgentUI.HistoryOpen, 0.85, 0, func() {
				a.viewChatHistoryPopover(c)
			})
			if pop.Dismissed() {
				a.AgentUI.ArmedRemoveID = ""
				a.AgentUI.ConfirmClear = false
				a.AgentUI.HistoryBtnHandle.Focus()
			}
		}

		// 2. Collapsible AgentSettings panel
		if a.AgentUI.SettingsOpen {
			a.viewAgentSettingsPanel(c)
		}

		// 3. Multi-agent bar
		bar := a.Store.BarAgents(8)
		hasHelper := false
		for _, p := range bar.All {
			if p.Parent != "" {
				hasHelper = true
				break
			}
		}
		if len(bar.All) > 1 && (len(bar.Running) > 0 || hasHelper) {
			ui.Column(c.Key("agent-bar")).
				FillWidth().
				Shrink(0).
				Padding(8, 10).
				Gap(6).
				Background(C.Panel2).
				BorderWidth(0, 0, 1, 0).
				BorderColor(C.Border).
				Children(func() {
					ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
						statusTxt := "Finished"
						if len(bar.Running) > 0 {
							statusTxt = fmt.Sprintf("%d/%d agents running", len(bar.Running), len(bar.All))
						}
						ui.Text(c, statusTxt).FontSize(11).FontWeight(600).TextColor(C.Text).Grow(1)
						if len(bar.Running) > 0 {
							if ActionBtn(c, "agent-stop-all", "Stop all agents", "stop", "danger").Clicked() {
								a.Bridge.TryCall("agent_stop", nil, nil)
							}
						}
					})
					ui.Row(c).FillWidth().Wrap().Gap(4).Children(func() {
						for _, prof := range bar.Shown {
							r, g, b, _, ok := ParseHexRGBA(prof.Color)
							if !ok {
								r, g, b = 96, 125, 255
							}
							ui.Row(c.Key("bar-pill-" + prof.ID)).
								Padding(2, 7).
								Gap(5).
								AlignItems(ui.Center).
								Radius(10).
								Background(C.Raised).
								Border(1, C.Border).
								Children(func() {
									ui.Box(c).Size(6, 6).Radius(3).Background(ui.RGB(r, g, b))
									ui.Text(c, prof.Name).FontSize(10).TextColor(C.Text).MaxLines(1)
								})
						}
						if bar.More > 0 {
							ui.Text(c, fmt.Sprintf("+%d", bar.More)).FontSize(10).TextColor(C.Ghost)
						}
					})
				})
		}

		// 4. Empty state OR Transcript
		if len(a.Store.Items) == 0 && !a.State.AgentRunning {
			ui.Scroll(c.Key("agent-empty")).
				FillWidth().
				Grow(1).
				MinHeight(0).
				KeepScrollbars(true).
				Padding(14).
				Gap(10).
				Children(func() {
					ui.Text(c, "Ask me to design anything").FontSize(14).FontWeight(600).TextColor(C.Text)
					for i, s := range agentSuggestions {
						sug := s
						btn := ui.ButtonBase(c.Key(fmt.Sprintf("agent-sug-%d", i))).
							FillWidth().
							Padding(8, 10).
							Radius(8).
							Row().
							AlignItems(ui.Center).
							Label("Design " + sug).
							Cursor(ui.CursorPointer)
						if btn.Hovered() {
							btn.Background(C.Raised).Border(1, C.Accent)
						} else {
							btn.Background(C.Panel2).Border(1, C.Border)
						}
						btn.Children(func() {
							ui.Text(c, sug).FontSize(11).TextColor(C.Muted).Grow(1).Wrap()
						})
						if btn.Clicked() {
							a.submitAgentPrompt("Design "+sug, true)
						}
					}
					ui.Text(c, "Each agent gets a cursor on the canvas — run up to 4× in parallel.").
						FontSize(11).TextColor(C.Ghost).Wrap()
				})
		} else {
			a.viewAgentTranscript(c, reduced, nowMS)
		}

		// 5. Collapsible Plan (todos)
		if len(a.Store.Todos) > 0 {
			a.viewAgentPlan(c)
		}

		// 6. Composer
		a.viewAgentComposer(c, dragOver)
	})
}

func (a *NalarupaApp) viewChatHistoryPopover(c *ui.Context) {
	ui.Column(c.Key("chat-history")).
		Width(280).
		MaxHeight(440).
		Padding(8).
		Gap(6).
		Background(C.Panel2).
		Radius(8).
		Children(func() {
			ui.Text(c, "Saved chats").FontSize(11).FontWeight(600).TextColor(C.Muted).Padding(2, 4)
			if len(a.AgentUI.Chats) == 0 {
				ui.Text(c, "No saved chats yet.").FontSize(12).TextColor(C.Ghost).Padding(8, 4)
				return
			}
			now := time.Now()
			todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
			yesterdayStart := todayStart.AddDate(0, 0, -1)
			weekStart := todayStart.AddDate(0, 0, -7)

			bucketOf := func(ts int64) string {
				var t time.Time
				if ts > 1e11 {
					t = time.UnixMilli(ts)
				} else {
					t = time.Unix(ts, 0)
				}
				switch {
				case !t.Before(todayStart):
					return "Today"
				case !t.Before(yesterdayStart):
					return "Yesterday"
				case !t.Before(weekStart):
					return "Previous 7 days"
				default:
					return "Older"
				}
			}

			ui.Scroll(c.Key("chat-history-scroll")).FillWidth().MaxHeight(340).Gap(2).Children(func() {
				lastBucket := ""
				for _, ch := range a.AgentUI.Chats {
					chat := ch
					b := bucketOf(chat.UpdatedAt)
					if b != lastBucket {
						lastBucket = b
						ui.Text(c, b).FontSize(10).FontWeight(600).TextColor(C.Ghost).Padding(6, 4, 2, 4)
					}
					cleanTitle := strings.Join(strings.Fields(chat.Title), " ")
					if cleanTitle == "" {
						cleanTitle = "Untitled chat"
					}
					isCurrent := chat.ID == a.AgentUI.CurrentChatID
					rowWrap := ui.Row(c.Key("chat-row-" + chat.ID)).
						FillWidth().
						Gap(4).
						AlignItems(ui.Center)
					rowHovered := rowWrap.Hovered()

					rowWrap.Children(func() {
						openBtn := ui.ButtonBase(c.Key("chat-" + chat.ID)).
							Grow(1).
							Basis(0).
							Height(28).
							Padding(0, 8).
							Radius(6).
							Row().
							Gap(6).
							AlignItems(ui.Center).
							Label(cleanTitle).
							Cursor(ui.CursorPointer)
						if openBtn.Hovered() || isCurrent {
							openBtn.Background(C.Raised)
						}
						openBtn.Children(func() {
							if isCurrent && a.State.AgentRunning {
								ui.Box(c).Size(6, 6).Shrink(0).Radius(3).Background(C.Accent)
							}
							ui.Text(c, cleanTitle).FontSize(12).TextColor(C.Text).Grow(1).MaxLines(1)
						})
						if openBtn.Clicked() {
							var loaded struct {
								Events []map[string]any `json:"events"`
							}
							if err := a.Bridge.Call("chat_load", map[string]any{"id": chat.ID}, &loaded); err != nil {
								a.ShowToast("Unable to open this chat. Pick another chat or start a new one.", true)
								a.refreshChatHistory()
							} else {
								a.clearAgentImageCache()
								a.AgentUI.ExpandedCards = map[int]bool{}
								a.Store.RestoreChat(loaded.Events)
								// Historical messages must render immediately at rest (Emil's rule: don't animate restored history!).
								if n := len(a.Store.Items); n > 0 {
									a.AgentUI.StreamMountedMinID = a.Store.Items[n-1].ID + 1
								}
								a.AgentUI.CurrentChatID = chat.ID
								a.AgentUI.HistoryOpen = false
								a.AgentUI.HistoryBtnHandle.Focus()
							}
						}

						if a.AgentUI.ArmedRemoveID == chat.ID {
							if ActionBtn(c, "chat-remove-confirm-"+chat.ID, "Remove", "", "danger").Clicked() {
								if err := a.Bridge.Call("chat_delete", map[string]any{"id": chat.ID}, nil); err != nil {
									a.ShowToast(err.Error(), true)
								} else {
									if a.AgentUI.CurrentChatID == chat.ID {
										a.Store.ResetChat()
										a.AgentUI.ExpandedCards = map[int]bool{}
										a.clearAgentImageCache()
									}
									a.AgentUI.ArmedRemoveID = ""
									a.refreshChatHistory()
								}
							}
						} else if rowHovered || openBtn.FocusVisible() {
							rmBtn := IconBtn(c, "chat-remove-"+chat.ID, "x", "Remove chat", 24, false)
							if rmBtn.Clicked() {
								a.AgentUI.ArmedRemoveID = chat.ID
							}
						}
					})
				}
			})

			// Footer: Clear history
			ui.Row(c.Key("chat-history-footer")).
				FillWidth().
				Padding(6, 2, 0, 2).
				Gap(6).
				AlignItems(ui.Center).
				BorderWidth(1, 0, 0, 0).
				BorderColor(C.Border).
				Children(func() {
					if a.AgentUI.ConfirmClear {
						ui.Text(c, "Clear all chats?").FontSize(11).TextColor(C.Danger).Grow(1)
						if ActionBtn(c, "chat-clear-cancel", "Cancel", "", "").Clicked() {
							a.AgentUI.ConfirmClear = false
						}
						if ActionBtn(c, "chat-clear-confirm", "Clear", "", "danger").Clicked() {
							var clearedCurrent bool
							if err := a.Bridge.Call("chat_clear", nil, &clearedCurrent); err != nil {
								a.ShowToast(err.Error(), true)
							} else {
								if clearedCurrent {
									a.Store.ResetChat()
									a.AgentUI.ExpandedCards = map[int]bool{}
									a.clearAgentImageCache()
								}
								a.AgentUI.ConfirmClear = false
								a.refreshChatHistory()
							}
						}
					} else {
						if ActionBtn(c, "chat-clear", "Clear history", "trash", "").Clicked() {
							a.AgentUI.ConfirmClear = true
						}
					}
				})
		})
}

func (a *NalarupaApp) viewAgentSettingsPanel(c *ui.Context) {
	reduced := EffectiveReducedMotion(c)
	ui.Scroll(c.Key("agent-settings-panel")).
		FillWidth().
		MaxHeight(400).
		MaxHeightPercent(58).
		Shrink(1).
		KeepScrollbars(true).
		TrackScroll(&a.AgentUI.SettingsScroll).
		Padding(10, 12).
		Gap(10).
		Background(C.Panel2).
		BorderWidth(0, 0, 1, 0).
		BorderColor(C.Border).
		Transition(DrawerCollapseTransition(reduced)).
		Children(func() {
			nextBack, backChanged := SegmentedTabs(c, "agent-backend-tabs", "Agent backend", a.AgentUI.Backend, []SegOption[string]{
				{Key: "backend-api", Value: "api", Label: "Claude API"},
				{Key: "backend-claude-code", Value: "claude-code", Label: "Claude Code"},
				{Key: "backend-provider", Value: "provider", Label: "Your provider"},
			})
			if backChanged {
				a.setSetting("agent_backend", nextBack)
				if nextBack == "claude-code" {
					a.refreshClaudeAuth()
				}
			}

			switch a.AgentUI.Backend {
			case "api":
				ph := "sk-ant-..."
				if a.AgentUI.HasAPIKey {
					ph = "•••••••• (saved)"
				}
				ui.Row(c.Key("api-key-row")).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
					CommittedField(c, FieldOpts{
						Key:         "api-key",
						AriaLabel:   "Anthropic API key",
						Value:       "",
						Placeholder: ph,
						Password:    true,
						Mono:        true,
						Grow:        true,
						OnCommit: func(val string) bool {
							trimmed := strings.TrimSpace(val)
							if trimmed == "" {
								return false
							}
							a.setSetting("anthropic_api_key", trimmed)
							return true
						},
					})
					if a.AgentUI.HasAPIKey {
						if ActionBtn(c, "api-key-remove", "Remove", "", "danger").Clicked() {
							a.setSetting("anthropic_api_key", nil)
						}
					}
				})
				CommittedField(c, FieldOpts{
					Key:         "api-base-url",
					AriaLabel:   "Anthropic base URL",
					Value:       a.AgentUI.BaseURL,
					Placeholder: "https://api.anthropic.com (optional)",
					Mono:        true,
					OnCommit: func(val string) bool {
						trimmed := strings.TrimSpace(val)
						if trimmed == "" {
							a.setSetting("anthropic_base_url", nil)
						} else {
							a.setSetting("anthropic_base_url", trimmed)
						}
						return true
					},
				})

			case "claude-code":
				auth := a.Store.ClaudeAuth
				statusLine := "Checking Claude Code CLI..."
				if auth != nil {
					switch {
					case !auth.Installed:
						statusLine = "Claude Code CLI not found on PATH."
					case auth.SignedIn:
						acc := auth.Account
						if acc == "" {
							acc = "Signed in"
						}
						if auth.Plan != "" {
							statusLine = fmt.Sprintf("%s (%s)", acc, auth.Plan)
						} else {
							statusLine = acc
						}
					default:
						statusLine = "Not signed in to Claude Code."
					}
				}
				ui.Text(c, statusLine).FontSize(11).TextColor(C.Muted).Wrap()
				ui.Row(c.Key("claude-auth-btns")).FillWidth().Wrap().Gap(6).Children(func() {
					if auth != nil && auth.SignedIn {
						if ActionBtn(c, "claude-sign-out", "Sign out", "", "").Clicked() {
							var st ClaudeAuth
							if a.Bridge.TryCall("claude_auth_logout", nil, &st) {
								a.Store.SetClaudeAuth(st)
							}
						}
					} else {
						if ActionBtn(c, "claude-sign-in", "Sign in", "", "primary").Clicked() {
							a.AgentUI.AuthLoggingIn = true
							a.Bridge.TryCall("claude_auth_login", map[string]any{"console": false}, nil)
						}
						if ActionBtn(c, "claude-sign-in-console", "Console account", "", "").Clicked() {
							a.AgentUI.AuthLoggingIn = true
							a.Bridge.TryCall("claude_auth_login", map[string]any{"console": true}, nil)
						}
						if a.AgentUI.AuthLoggingIn {
							if ActionBtn(c, "claude-sign-in-cancel", "Cancel", "", "").Clicked() {
								a.AgentUI.AuthLoggingIn = false
								a.Bridge.TryCall("claude_auth_cancel", nil, nil)
							}
						}
					}
				})

				permMode := "ask"
				if a.AgentUI.AutoPerms {
					permMode = "auto"
				}
				ui.Text(c, "Other tools permission").FontSize(11).FontWeight(600).TextColor(C.Muted)
				nextPerm, permChanged := SegmentedTabs(c, "claude-perm-mode", "Other tools permission", permMode, []SegOption[string]{
					{Value: "ask", Label: "Ask me"},
					{Value: "auto", Label: "Auto mode"},
				})
				if permChanged {
					a.setSetting("agent_auto_permissions", nextPerm == "auto")
				}

			case "provider":
				a.viewBYOKProviderSettings(c)
			}

			// Shared reasoning effort across all backends (claude-code, api, provider)
			ui.Column(c.Key("effort-settings-sec")).FillWidth().Padding(4, 0, 0, 0).Gap(4).Children(func() {
				ui.Text(c, "Reasoning effort").FontSize(11).FontWeight(600).TextColor(C.Muted)
				effSegOpts := make([]SegOption[string], len(reasoningEffortOpts))
				for i, o := range reasoningEffortOpts {
					effSegOpts[i] = SegOption[string]{Key: "set-eff-" + o.ID, Value: o.ID, Label: o.Short}
				}
				nextEff, effChanged := SegmentedTabs(c, "settings-effort", "Reasoning effort", a.effectiveEffort(), effSegOpts)
				if effChanged {
					a.setSetting("agent_effort", nextEff)
				}
			})

			// Context window limit (cap across all backends; custom providers also support per-provider custom windows)
			ui.Column(c.Key("context-settings-sec")).FillWidth().Padding(4, 0, 0, 0).Gap(5).Children(func() {
				effLim := a.effectiveContextLimit()
				hdrTxt := fmt.Sprintf("Context window limit (%s active)", formatTokenCount(effLim))
				ui.Text(c, hdrTxt).FontSize(11).FontWeight(600).TextColor(C.Muted)
				presets := []struct {
					label string
					val   int
				}{
					{"Auto", 0},
					{"64k", 64_000},
					{"128k", 128_000},
					{"200k", 200_000},
					{"256k", 256_000},
					{"1M", 1_000_000},
				}
				ui.Row(c.Key("settings-ctx-presets")).FillWidth().Wrap().Gap(4).Children(func() {
					for _, pr := range presets {
						preset := pr
						sel := a.AgentUI.GlobalContextWindow == preset.val
						chip := ui.ButtonBase(c.Key("set-ctx-" + preset.label)).
							Height(22).Padding(0, 7).Radius(5).Center().
							Selected(sel).
							Label("Context window limit: " + preset.label).
							Cursor(ui.CursorPointer)
						if sel {
							chip.Background(C.AccentSoft).Border(1, C.Accent)
						} else if chip.Hovered() {
							chip.Background(C.Raised).Border(1, C.Border)
						} else {
							chip.Background(C.Panel).Border(1, C.Border)
						}
						chip.Children(func() {
							col := C.Muted
							if sel {
								col = C.Accent
							}
							ui.Text(c, preset.label).FontSize(10).FontFeatures("tnum").TextColor(col)
						})
						if chip.Clicked() {
							if preset.val <= 0 {
								a.setSetting("agent_context_window", nil)
							} else {
								a.setSetting("agent_context_window", preset.val)
							}
						}
					}
				})
				CommittedField(c, FieldOpts{
					Key:         "settings-context-window",
					AriaLabel:   "Custom context window limit (tokens)",
					Value:       a.AgentUI.GlobalContextDraft,
					Placeholder: "Custom limit (e.g. 128000 or 256k), blank = Auto",
					Mono:        true,
					OnCommit: func(v string) bool {
						parsed, ok := parseUIContextWindow(v)
						if !ok {
							a.ShowToast("Enter a valid token count (e.g. 128000, 128k, 1M) or leave blank for Auto.", true)
							return false
						}
						if parsed <= 0 {
							a.setSetting("agent_context_window", nil)
						} else {
							a.setSetting("agent_context_window", parsed)
						}
						return true
					},
				})
			})

			// Optional TypeSafe AI per-screen visual decision gate (BYOK)
			a.viewTypeSafeSettingsSection(c)

			// Connect External Agents (MCP)
			ui.Column(c.Key("mcp-connect-sec")).
				FillWidth().
				Padding(8, 0, 0, 0).
				Gap(6).
				BorderWidth(1, 0, 0, 0).
				BorderColor(C.Border).
				Children(func() {
					ui.Text(c, "Connect external agents (MCP)").FontSize(11).FontWeight(600).TextColor(C.Muted)
					if a.MCPURL != "" {
						ui.Text(c, a.MCPURL).FontSize(11).Font(MonoFont()).TextColor(C.Ghost).Selectable()
					}
					ui.Row(c.Key("mcp-buttons")).FillWidth().Wrap().Gap(6).Children(func() {
						installMCP := func(key, label, client string) {
							if ActionBtn(c, key, label, "plug", "").Clicked() {
								var msg string
								if err := a.Bridge.Call("mcp_install", map[string]any{"client": client}, &msg); err != nil {
									a.ShowToast(err.Error(), true)
								} else {
									a.ShowToast(msg, false)
								}
							}
						}
						installMCP("mcp-claude-code", "Claude Code", "claude-code")
						installMCP("mcp-cursor", "Cursor", "cursor")
						installMCP("mcp-codex", "Codex", "codex")
					})
				})
		})
}

func (a *NalarupaApp) viewBYOKProviderSettings(c *ui.Context) {
	if ed := a.AgentUI.EditingProv; ed != nil {
		ui.Column(c.Key("provider-editor")).FillWidth().Gap(6).Children(func() {
			nextFmt, fmtChanged := SegmentedTabs(c, "provider-format", "Provider API format", ed.Format, []SegOption[string]{
				{Value: "chat", Label: "Chat"},
				{Value: "responses", Label: "Responses"},
				{Value: "anthropic", Label: "Anthropic"},
			})
			if fmtChanged {
				ed.Format = nextFmt
			}
			CommittedField(c, FieldOpts{
				Key:         "provider-name",
				AriaLabel:   "Provider name",
				Value:       ed.Name,
				Placeholder: "OpenRouter / Local",
				OnCommit: func(v string) bool {
					ed.Name = strings.TrimSpace(v)
					return true
				},
			})
			CommittedField(c, FieldOpts{
				Key:         "provider-base-url",
				AriaLabel:   "Base URL",
				Value:       ed.BaseURL,
				Placeholder: "https://api.openai.com/v1",
				Mono:        true,
				OnCommit: func(v string) bool {
					ed.BaseURL = strings.TrimSpace(v)
					return true
				},
			})
			CommittedField(c, FieldOpts{
				Key:         "provider-key",
				AriaLabel:   "API key",
				Value:       a.AgentUI.ProvKeyDraft,
				Placeholder: "sk-...",
				Password:    true,
				Mono:        true,
				OnCommit: func(v string) bool {
					a.AgentUI.ProvKeyDraft = strings.TrimSpace(v)
					return true
				},
			})
			CommittedField(c, FieldOpts{
				Key:         "provider-models",
				AriaLabel:   "Models (comma-separated)",
				Value:       a.AgentUI.ProvModelsCSV,
				Placeholder: "gpt-6.2-astra, gpt-6.2-sol, gemini-3.8-flash",
				Mono:        true,
				OnCommit: func(v string) bool {
					a.AgentUI.ProvModelsCSV = v
					return true
				},
			})
			if _, clicked := ToggleChip(c, "provider-vision", "The model can see images (screenshots)", ed.Vision); clicked {
				ed.Vision = !ed.Vision
			}
			ui.Column(c.Key("provider-ctx-sec")).FillWidth().Gap(4).Children(func() {
				ui.Text(c, "Provider context window").FontSize(10).FontWeight(600).TextColor(C.Muted)
				provPresets := []struct {
					label string
					val   int
				}{
					{"Auto", 0},
					{"32k", 32_000},
					{"64k", 64_000},
					{"128k", 128_000},
					{"200k", 200_000},
					{"256k", 256_000},
					{"1M", 1_000_000},
				}
				ui.Row(c.Key("provider-ctx-presets")).FillWidth().Wrap().Gap(4).Children(func() {
					for _, pr := range provPresets {
						preset := pr
						sel := ed.ContextWindow == preset.val
						chip := ui.ButtonBase(c.Key("prov-ctx-" + preset.label)).
							Height(22).Padding(0, 6).Radius(5).Center().
							Selected(sel).
							Label("Provider context window: " + preset.label).
							Cursor(ui.CursorPointer)
						if sel {
							chip.Background(C.AccentSoft).Border(1, C.Accent)
						} else if chip.Hovered() {
							chip.Background(C.Raised).Border(1, C.Border)
						} else {
							chip.Background(C.Panel).Border(1, C.Border)
						}
						chip.Children(func() {
							col := C.Muted
							if sel {
								col = C.Accent
							}
							ui.Text(c, preset.label).FontSize(10).FontFeatures("tnum").TextColor(col)
						})
						if chip.Clicked() {
							ed.ContextWindow = preset.val
							if preset.val > 0 {
								a.AgentUI.ProvContextDraft = strconv.Itoa(preset.val)
							} else {
								a.AgentUI.ProvContextDraft = ""
							}
						}
					}
				})
				CommittedField(c, FieldOpts{
					Key:         "provider-context-window",
					AriaLabel:   "Custom provider context window tokens",
					Value:       a.AgentUI.ProvContextDraft,
					Placeholder: "Custom context tokens (e.g. 131072 or 256k), blank = Auto",
					Mono:        true,
					OnCommit: func(v string) bool {
						parsed, ok := parseUIContextWindow(v)
						if !ok {
							a.ShowToast("Enter a valid context window (e.g. 128000, 128k, 1M) or leave blank for Auto.", true)
							return false
						}
						ed.ContextWindow = parsed
						if parsed > 0 {
							a.AgentUI.ProvContextDraft = strconv.Itoa(parsed)
						} else {
							a.AgentUI.ProvContextDraft = ""
						}
						return true
					},
				})
			})
			ui.Row(c).FillWidth().Wrap().Gap(6).Children(func() {
				fetchLabel := "Fetch models"
				if a.AgentUI.FetchingModels {
					fetchLabel = "Fetching…"
				}
				if ActionBtn(c, "provider-fetch-models", fetchLabel, "", "").Clicked() {
					a.requestFetchModels("provider", ed.ID, ed.Format, ed.BaseURL, a.AgentUI.ProvKeyDraft, false, a.AgentUI.ProvEditSeq)
				}
				if ActionBtn(c, "provider-save", "Save", "", "primary").Clicked() {
					var models []string
					for _, part := range strings.Split(a.AgentUI.ProvModelsCSV, ",") {
						if m := strings.TrimSpace(part); m != "" {
							models = append(models, m)
						}
					}
					if strings.TrimSpace(a.AgentUI.ProvContextDraft) != "" {
						if parsed, ok := parseUIContextWindow(a.AgentUI.ProvContextDraft); ok {
							ed.ContextWindow = parsed
						}
					}
					payload := map[string]any{
						"name":           ed.Name,
						"format":         ed.Format,
						"base_url":       ed.BaseURL,
						"models":         models,
						"vision":         ed.Vision,
						"context_window": ed.ContextWindow,
					}
					if ed.ID != "" {
						payload["id"] = ed.ID
					}
					if a.AgentUI.ProvKeyDraft != "" {
						payload["api_key"] = a.AgentUI.ProvKeyDraft
					}
					var savedID string
					if err := a.Bridge.Call("provider_save", map[string]any{"provider": payload}, &savedID); err != nil {
						a.ShowToast(err.Error(), true)
					} else {
						a.setSetting("agent_provider", savedID)
						a.AgentUI.ProvEditSeq++
						if a.AgentUI.ActiveFetchEditSeq > 0 {
							a.AgentUI.FetchingModels = false
							a.AgentUI.ActiveFetchReqID = ""
						}
						a.AgentUI.EditingProv = nil
					}
				}
				if ActionBtn(c, "provider-cancel", "Cancel", "", "").Clicked() {
					a.AgentUI.ProvEditSeq++
					if a.AgentUI.ActiveFetchEditSeq > 0 {
						a.AgentUI.FetchingModels = false
						a.AgentUI.ActiveFetchReqID = ""
					}
					a.AgentUI.EditingProv = nil
				}
				if ed.ID != "" {
					if ActionBtn(c, "provider-remove", "Remove", "", "danger").Clicked() {
						a.Bridge.TryCall("provider_remove", map[string]any{"id": ed.ID}, nil)
						a.AgentUI.ProvEditSeq++
						if a.AgentUI.ActiveFetchEditSeq > 0 {
							a.AgentUI.FetchingModels = false
							a.AgentUI.ActiveFetchReqID = ""
						}
						a.AgentUI.EditingProv = nil
						a.reloadAgentSettings()
					}
				}
			})
		})
		return
	}

	if len(a.AgentUI.Providers) > 0 {
		var opts []SegOption[string]
		curID := ""
		if ap := a.activeProvider(); ap != nil {
			curID = ap.ID
		}
		for _, p := range a.AgentUI.Providers {
			lbl := p.Name
			if lbl == "" {
				lbl = p.ID
			}
			opts = append(opts, SegOption[string]{Value: p.ID, Label: lbl})
		}
		nextID, changed := SegmentedTabs(c, "provider-select", "Active provider", curID, opts)
		if changed {
			a.setSetting("agent_provider", nextID)
		}
	}

	ui.Row(c.Key("provider-actions")).Wrap().Gap(6).Children(func() {
		if ActionBtn(c, "provider-add", "Add provider", "plus", "").Clicked() {
			a.AgentUI.ProvEditSeq++
			if a.AgentUI.ActiveFetchEditSeq > 0 {
				a.AgentUI.FetchingModels = false
				a.AgentUI.ActiveFetchReqID = ""
			}
			a.AgentUI.EditingProv = &ProviderConfig{Format: "chat", Vision: true}
			a.AgentUI.ProvKeyDraft = ""
			a.AgentUI.ProvModelsCSV = ""
			a.AgentUI.ProvContextDraft = ""
		}
		if ap := a.activeProvider(); ap != nil {
			if ActionBtn(c, "provider-edit", "Edit", "", "").Clicked() {
				a.AgentUI.ProvEditSeq++
				if a.AgentUI.ActiveFetchEditSeq > 0 {
					a.AgentUI.FetchingModels = false
					a.AgentUI.ActiveFetchReqID = ""
				}
				cp := *ap
				a.AgentUI.EditingProv = &cp
				a.AgentUI.ProvKeyDraft = ""
				a.AgentUI.ProvModelsCSV = strings.Join(ap.Models, ", ")
				if ap.ContextWindow > 0 {
					a.AgentUI.ProvContextDraft = strconv.Itoa(ap.ContextWindow)
				} else {
					a.AgentUI.ProvContextDraft = ""
				}
			}
			if ActionBtn(c, "provider-test", "Test", "", "").Clicked() {
				a.AgentUI.TestingProvID = ap.ID
				a.Bridge.TryCall("provider_test", map[string]any{"id": ap.ID}, nil)
			}
		}
	})
	if ap := a.activeProvider(); ap != nil {
		ctxSummary := fmt.Sprintf("Context window: %s (auto)", formatTokenCount(a.effectiveContextLimit()))
		if ap.ContextWindow > 0 {
			ctxSummary = fmt.Sprintf("Context window: %s (custom)", formatTokenCount(ap.ContextWindow))
		}
		ui.Text(c, ctxSummary).FontSize(10).FontFeatures("tnum").TextColor(C.Ghost)
	}

	if pt := a.Store.ProviderTest; pt != nil {
		col := C.OK
		msg := fmt.Sprintf("✓ %s: %s", pt.Model, pt.Reply)
		if !pt.OK {
			col = C.Danger
			msg = "✕ " + pt.Error
		}
		ui.Text(c, msg).FontSize(11).TextColor(col).Wrap()
	}
}

func (a *NalarupaApp) viewTypeSafeSettingsSection(c *ui.Context) {
	ui.Column(c.Key("typesafe-settings-sec")).
		FillWidth().
		Padding(8, 0, 0, 0).
		Gap(6).
		BorderWidth(1, 0, 0, 0).
		BorderColor(C.Border).
		Children(func() {
			ui.Text(c, "TypeSafe AI visual gate (optional)").FontSize(11).FontWeight(600).TextColor(C.Muted)
			if _, clicked := ToggleChip(c, "typesafe-enabled", "Score each finished screen image (0–100)", a.AgentUI.TypeSafeEnabled); clicked {
				next := !a.AgentUI.TypeSafeEnabled
				a.setSetting("typesafe_enabled", next)
			}
			ui.Text(c, "Evaluates only the rendered image of each completed full screen at the end of a turn (skips individual component/section steps, code, and untouched screens).").
				FontSize(10).TextColor(C.Ghost).Wrap()

			tsPlaceholder := "TypeSafe API key (BYOK)…"
			if a.AgentUI.HasSavedTypeSafeKey {
				tsPlaceholder = "•••••••• (saved)"
			} else if a.AgentUI.HasTypeSafeKey {
				tsPlaceholder = "Using TYPESAFE_API_KEY from environment"
			}
			ui.Row(c.Key("typesafe-key-row")).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
				CommittedField(c, FieldOpts{
					Key:         "typesafe-api-key",
					AriaLabel:   "TypeSafe AI API key",
					Value:       "",
					Placeholder: tsPlaceholder,
					Password:    true,
					Mono:        true,
					Grow:        true,
					OnCommit: func(val string) bool {
						trimmed := strings.TrimSpace(val)
						if trimmed == "" {
							return false
						}
						a.setSetting("typesafe_api_key", trimmed)
						if !a.AgentUI.TypeSafeEnabled {
							a.setSetting("typesafe_enabled", true)
						}
						return true
					},
				})
				if a.AgentUI.HasTypeSafeKey {
					if ActionBtn(c, "typesafe-test-btn", "Test TypeSafe key", "", "").Clicked() {
						if err := a.Bridge.Call("typesafe_test", nil, nil); err != nil {
							a.ShowToast(err.Error(), true)
						}
					}
				}
				if a.AgentUI.HasSavedTypeSafeKey {
					if ActionBtn(c, "typesafe-key-remove", "Remove TypeSafe key", "", "danger").Clicked() {
						a.setSetting("typesafe_api_key", nil)
					}
				}
			})

			if tst := a.Store.TypeSafeTest; tst != nil {
				col := C.OK
				msg := fmt.Sprintf("✓ TypeSafe ready (%s)", tst.Model)
				if !tst.OK {
					col = C.Danger
					msg = "✕ " + tst.Error
				}
				ui.Text(c, msg).FontSize(11).TextColor(col).Wrap()
			}

			// Model selection (defaults to jev-latest -> newest Jev release)
			ui.Row(c.Key("typesafe-model-row")).FillWidth().Wrap().Gap(4).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "Model:").FontSize(10).FontWeight(600).TextColor(C.Muted)
				modelChoices := []string{"jev-latest", "jev-1.14.0"}
				for _, extra := range a.AgentUI.TypeSafeModels {
					if extra != "" && !slices.Contains(modelChoices, extra) {
						modelChoices = append(modelChoices, extra)
					}
				}
				if a.AgentUI.TypeSafeModel != "" && !slices.Contains(modelChoices, a.AgentUI.TypeSafeModel) {
					modelChoices = append(modelChoices, a.AgentUI.TypeSafeModel)
				}
				for _, m := range modelChoices {
					modID := m
					sel := a.AgentUI.TypeSafeModel == modID
					chip := ui.ButtonBase(c.Key("ts-mod-" + modID)).
						Height(22).Padding(0, 7).Radius(5).Center().
						Selected(sel).
						Label("TypeSafe model: " + modID).
						Cursor(ui.CursorPointer)
					if sel {
						chip.Background(C.AccentSoft).Border(1, C.Accent)
					} else if chip.Hovered() {
						chip.Background(C.Raised).Border(1, C.Border)
					} else {
						chip.Background(C.Panel).Border(1, C.Border)
					}
					chip.Children(func() {
						col := C.Muted
						if sel {
							col = C.Accent
						}
						ui.Text(c, modID).FontSize(10).Font(MonoFont()).TextColor(col)
					})
					if chip.Clicked() {
						a.setSetting("typesafe_model", modID)
					}
				}
				if a.AgentUI.HasTypeSafeKey {
					refLbl := "Refresh"
					if a.AgentUI.TypeSafeFetchingModels {
						refLbl = "Refreshing…"
					}
					refBtn := ui.ButtonBase(c.Key("ts-mod-refresh")).
						Height(22).Padding(0, 7).Radius(5).Center().
						Label("Refresh TypeSafe models").
						Cursor(ui.CursorPointer)
					if refBtn.Hovered() {
						refBtn.Background(C.Raised).Border(1, C.Border)
					} else {
						refBtn.Background(C.Panel).Border(1, C.Border)
					}
					refBtn.Children(func() {
						ui.Text(c, refLbl).FontSize(10).TextColor(C.Accent)
					})
					if refBtn.Clicked() && !a.AgentUI.TypeSafeFetchingModels {
						a.AgentUI.TypeSafeFetchingModels = true
						if err := a.Bridge.Call("typesafe_models", nil, nil); err != nil {
							a.AgentUI.TypeSafeFetchingModels = false
							a.ShowToast(err.Error(), true)
						}
					}
				}
			})

			// Auto-revision toggle & configurable revision score threshold (1–100)
			ui.Column(c.Key("typesafe-autorevise-sec")).FillWidth().Gap(5).Children(func() {
				if _, clicked := ToggleChip(c, "typesafe-auto-revise", "Auto-revise screens below score threshold", a.AgentUI.TypeSafeAutoRevise); clicked {
					a.setSetting("typesafe_auto_revise", !a.AgentUI.TypeSafeAutoRevise)
				}

				ui.Row(c.Key("typesafe-score-input-row")).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "Revision score threshold (1–100):").
						FontSize(10).FontWeight(600).TextColor(C.Muted).Grow(1)
					ScrubNumField(c, NumFieldOpts{
						Key:       "typesafe-min-score-field",
						AriaLabel: "TypeSafe revision score threshold",
						Prefix:    "≥",
						Value:     float64(a.AgentUI.TypeSafeMinScore),
						Step:      1,
						HasMin:    true,
						Min:       1,
						HasMax:    true,
						Max:       100,
						Width:     68,
						OnSet: func(val float64, _ string) bool {
							r := math.Round(val)
							if math.IsNaN(r) || math.IsInf(r, 0) || r < 1 {
								r = 1
							} else if r > 100 {
								r = 100
							}
							a.setSetting("typesafe_min_score", int(r))
							return true
						},
					})
				})

				ui.Row(c.Key("typesafe-score-row")).FillWidth().Wrap().Gap(4).AlignItems(ui.Center).Children(func() {
					for _, thresh := range []int{60, 70, 75, 80, 85, 90} {
						tVal := thresh
						sel := a.AgentUI.TypeSafeMinScore == tVal
						lbl := fmt.Sprintf("%d", tVal)
						chip := ui.ButtonBase(c.Key(fmt.Sprintf("ts-thresh-%d", tVal))).
							Height(22).Padding(0, 7).Radius(5).Center().
							Selected(sel).
							Label(fmt.Sprintf("TypeSafe minimum passing score: %d out of 100", tVal)).
							Cursor(ui.CursorPointer)
						if sel {
							chip.Background(C.AccentSoft).Border(1, C.Accent)
						} else if chip.Hovered() {
							chip.Background(C.Raised).Border(1, C.Border)
						} else {
							chip.Background(C.Panel).Border(1, C.Border)
						}
						chip.Children(func() {
							col := C.Muted
							if sel {
								col = C.Accent
							}
							ui.Text(c, lbl).FontSize(10).FontFeatures("tnum").TextColor(col)
						})
						if chip.Clicked() {
							a.setSetting("typesafe_min_score", tVal)
						}
					}
				})

				if a.AgentUI.TypeSafeAutoRevise {
					ui.Row(c.Key("typesafe-max-rev-row")).FillWidth().Wrap().Gap(4).AlignItems(ui.Center).Children(func() {
						ui.Text(c, "Max auto-revisions:").FontSize(10).FontWeight(600).TextColor(C.Muted)
						for _, mr := range []int{1, 2, 3, 5} {
							revCnt := mr
							sel := a.AgentUI.TypeSafeMaxRevisions == revCnt
							lbl := fmt.Sprintf("%d×", revCnt)
							chip := ui.ButtonBase(c.Key(fmt.Sprintf("ts-maxrev-%d", revCnt))).
								Height(22).Padding(0, 7).Radius(5).Center().
								Selected(sel).
								Label(fmt.Sprintf("Maximum auto-revisions per screen: %d", revCnt)).
								Cursor(ui.CursorPointer)
							if sel {
								chip.Background(C.AccentSoft).Border(1, C.Accent)
							} else if chip.Hovered() {
								chip.Background(C.Raised).Border(1, C.Border)
							} else {
								chip.Background(C.Panel).Border(1, C.Border)
							}
							chip.Children(func() {
								col := C.Muted
								if sel {
									col = C.Accent
								}
								ui.Text(c, lbl).FontSize(10).FontFeatures("tnum").TextColor(col)
							})
							if chip.Clicked() {
								a.setSetting("typesafe_max_revisions", revCnt)
							}
						}
					})
				}
			})
		})
}

func (a *NalarupaApp) viewAgentTranscript(c *ui.Context, reduced bool, nowMS int64) {
	hasTickingItem := false
	for _, it := range a.Store.Items {
		if ItemChanging(it) && !reduced {
			hasTickingItem = true
			break
		}
	}

	// Detect user scrollbar thumb drag upward or back to bottom across frames
	if a.AgentUI.TranscriptScroll.MaxY > 0 && math.Abs(float64(a.AgentUI.TranscriptScroll.MaxY-a.AgentUI.LastScrollMaxY)) < 1.0 {
		if a.AgentUI.TranscriptScroll.Y < a.AgentUI.TranscriptScroll.MaxY-24 {
			a.AgentUI.StickToBottom = false
		} else if a.AgentUI.TranscriptScroll.Y >= a.AgentUI.TranscriptScroll.MaxY-8 {
			a.AgentUI.StickToBottom = true
		}
	}
	a.AgentUI.LastScrollMaxY = a.AgentUI.TranscriptScroll.MaxY

	sc := ui.Scroll(c.Key("agent-transcript")).
		FillWidth().
		Grow(1).
		MinHeight(0).
		KeepScrollbars(true).
		TrackScroll(&a.AgentUI.TranscriptScroll).
		Padding(10, 12, 16, 12).
		Gap(10).
		Role(ui.RoleLog).
		Label("Conversation transcript").
		Focusable()

	sc.HandleInput(func(ev ui.InputEvent) bool {
		if ev.Kind == ui.InputScroll {
			if ev.DY < 0 {
				a.AgentUI.StickToBottom = false
			} else if ev.DY > 0 && a.AgentUI.TranscriptScroll.Y+ev.DY >= a.AgentUI.TranscriptScroll.MaxY-16 {
				a.AgentUI.StickToBottom = true
			}
		}
		if ev.Kind == ui.InputKeyDown {
			curY := a.AgentUI.TranscriptScroll.Y
			switch ev.Key {
			case ui.KeyUp:
				a.AgentUI.StickToBottom = false
				a.AgentUI.TranscriptScroll.Y = max(0, curY-40)
				return true
			case ui.KeyDown:
				a.AgentUI.TranscriptScroll.Y = curY + 40
				return true
			case ui.KeyPageUp:
				a.AgentUI.StickToBottom = false
				a.AgentUI.TranscriptScroll.Y = max(0, curY-240)
				return true
			case ui.KeyPageDown:
				a.AgentUI.TranscriptScroll.Y = curY + 240
				return true
			case ui.KeyHome:
				a.AgentUI.StickToBottom = false
				a.AgentUI.TranscriptScroll.Y = 0
				return true
			case ui.KeyEnd:
				a.AgentUI.StickToBottom = true
				a.AgentUI.TranscriptScroll.Y = math.MaxFloat32
				return true
			}
		}
		return false
	})

	if hasTickingItem || (a.State.AgentRunning && !reduced) {
		sc.Draw(func(p *ui.Painter, _ ui.Rect) {
			p.After(IndicatorTickMS * time.Millisecond)
		})
	}

	lastContent := LastContentID(a.Store.Items)

	sc.Children(func() {
		for _, item := range a.Store.Items {
			it := item
			a.viewChatItem(c, it, it.ID == lastContent, reduced, nowMS)
		}
		if a.State.AgentRunning {
			anySpinning := false
			for _, it := range a.Store.Items {
				if ItemChanging(it) {
					anySpinning = true
					break
				}
			}
			if !anySpinning {
				dots := "…"
				if !reduced {
					dots = EllipsisAt(nowMS) + EllipsisPadAt(nowMS)
				}
				ui.Text(c, "Planning next moves"+dots).FontSize(11).TextColor(C.Ghost)
			}
		}
	})

	streamSig := uint64(len(a.Store.Items))
	if n := len(a.Store.Items); n > 0 {
		last := a.Store.Items[n-1]
		endedFlag := uint64(0)
		if last.EndedAt != nil {
			endedFlag = 1
		}
		runFlag := uint64(0)
		if last.Running || last.Streaming {
			runFlag = 1
		}
		streamSig = (streamSig << 40) ^ (uint64(last.ID) << 24) ^ (uint64(len(last.Text)+len(last.Output)+last.Lines+len(last.Images)+len(last.Contexts)) << 2) ^ (endedFlag << 1) ^ runFlag
	}
	if len(a.Store.Items) != a.AgentUI.LastItemCount || streamSig != a.AgentUI.LastStreamSig {
		a.AgentUI.LastItemCount = len(a.Store.Items)
		a.AgentUI.LastStreamSig = streamSig
		if a.AgentUI.StickToBottom {
			a.AgentUI.TranscriptScroll.Y = math.MaxFloat32
		}
	}

	if !a.AgentUI.StickToBottom && a.AgentUI.TranscriptScroll.MaxY > 24 && a.AgentUI.TranscriptScroll.Y < a.AgentUI.TranscriptScroll.MaxY-24 {
		ui.Row(c.Key("agent-jump-bottom-wrap")).
			FillWidth().
			Shrink(0).
			Padding(4, 10).
			Justify(ui.Center).
			Background(C.Panel).
			BorderWidth(1, 0, 0, 0).
			BorderColor(C.Border).
			Transition(ChipTransition(reduced)).
			Children(func() {
				if ActionBtn(c, "agent-jump-bottom", "Scroll to bottom", "chevronDown", "").Clicked() {
					a.AgentUI.StickToBottom = true
					a.AgentUI.TranscriptScroll.Y = math.MaxFloat32
				}
			})
	}
}

func (a *NalarupaApp) viewAgentHeader(c *ui.Context, prof AgentProfile, by *ReplyAuthor) {
	r, g, b, _, ok := ParseHexRGBA(prof.Color)
	if !ok {
		r, g, b = 96, 125, 255
	}
	ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
		ui.Box(c).Size(7, 7).Shrink(0).Radius(3.5).Background(ui.RGB(r, g, b))
		label := prof.Name
		if prof.Parent != "" {
			parentProf := a.Store.ProfileOf(prof.Parent)
			label += " · helping " + parentProf.Name
		}
		ui.Text(c, label).FontSize(11).FontWeight(600).TextColor(C.Text)
		if by != nil && by.Model != "" {
			ui.Text(c, by.Model).FontSize(10).TextColor(C.Ghost).MaxLines(1)
		}
	})
}

func (a *NalarupaApp) viewChatItem(c *ui.Context, it ChatItem, isLatest bool, reduced bool, nowMS int64) {
	itemKey := fmt.Sprintf("chat-item-%d", it.ID)
	liveEnter := it.ID >= a.AgentUI.StreamMountedMinID
	itemTrans := InstantTransition
	if liveEnter {
		itemTrans = ChatItemTransition(reduced)
	}
	switch it.Kind {
	case "user", "queued":
		isQueued := it.Kind == "queued"
		ui.Column(c.Key(itemKey)).FillWidth().AlignItems(ui.End).Gap(4).Transition(itemTrans).Children(func() {
			if len(it.Contexts) > 0 {
				ctxRow := ui.Row(c.Key(fmt.Sprintf("user-ctxs-%d", it.ID))).MaxWidth(236).Wrap().Gap(4).Justify(ui.End)
				if isQueued {
					ctxRow.Opacity(0.6)
				}
				ctxRow.Children(func() {
					for ci, ctx := range it.Contexts {
						iconKey := NodeIconName(ctx.NodeType, false)
						if ctx.Kind == "viewport" {
							iconKey = "maximize"
						}
						lbl := formatChatContextLabel(ctx)
						ui.Row(c.Key(fmt.Sprintf("user-ctx-%d-%d", it.ID, ci))).
							Padding(2, 7).
							Gap(4).
							Radius(10).
							AlignItems(ui.Center).
							Background(C.Panel2).
							Border(1, C.Border).
							Children(func() {
								ui.Icon(c, IconSVG(iconKey)).FontSize(10).TextColor(C.Accent).Shrink(0)
								ui.Text(c, lbl).FontSize(10).FontWeight(500).TextColor(C.Muted).MaxLines(1)
							})
					}
				})
			}
			if len(it.Images) > 0 {
				imgRow := ui.Row(c.Key(fmt.Sprintf("user-imgs-%d", it.ID))).MaxWidth(236).Wrap().Gap(4).Justify(ui.End)
				if isQueued {
					imgRow.Opacity(0.6)
				}
				imgRow.Children(func() {
					for imgIdx, b64 := range it.Images {
						cacheKey := fmt.Sprintf("u:%d:%d", it.ID, imgIdx)
						bmp := a.getCachedChatImage(cacheKey, b64, 168)
						if bmp != nil {
							ui.Box(c.Key(fmt.Sprintf("user-img-%d-%d", it.ID, imgIdx))).
								Size(72, 72).
								Radius(8).
								Border(1, C.Border).
								Clip().
								Children(func() {
									ui.Image(c, bmp).Size(72, 72).Radius(7).Fit(ui.Cover)
								})
						}
					}
				})
			}
			if strings.TrimSpace(it.Text) != "" || (len(it.Contexts) == 0 && len(it.Images) == 0) {
				bub := ui.Box(c).
					MaxWidth(236).
					Padding(8, 12).
					Radius(16).
					Background(C.Raised)
				if isQueued {
					bub.Opacity(0.6)
				}
				bub.Children(func() {
					ui.Text(c, it.Text).FontSize(12).TextColor(C.Text).Wrap().Selectable()
				})
			}
			if isQueued {
				ui.Text(c, "Queued — sent when the agents finish").FontSize(10).TextColor(C.Ghost)
			}
		})

	case "assistant":
		ui.Column(c.Key(itemKey)).FillWidth().Gap(4).Transition(itemTrans).Children(func() {
			if it.Header {
				a.viewAgentHeader(c, it.Agent, it.By)
			}
			stillStreaming := isLatest && liveEnter && (it.Streaming || !a.AgentUI.RevealedItems[it.ID])
			if stillStreaming && !reduced {
				el := markdown.Stream(c, it.Text, &a.AgentUI.MDOpts)
				if !it.Streaming && markdown.Revealed(el) {
					if a.AgentUI.RevealedItems == nil {
						a.AgentUI.RevealedItems = map[int]bool{}
					}
					a.AgentUI.RevealedItems[it.ID] = true
				}
			} else {
				markdown.View(c, it.Text, &a.AgentUI.MDOpts)
			}
		})

	case "thinking":
		isOpen := a.AgentUI.ExpandedCards[it.ID]
		ui.Column(c.Key(itemKey)).FillWidth().Gap(4).Transition(itemTrans).Children(func() {
			if it.Header {
				a.viewAgentHeader(c, it.Agent, it.By)
			}
			hdrLbl := "Thinking…"
			if it.EndedAt != nil {
				secs := max(1, int(math.Round((*it.EndedAt-it.StartedAt)/1000.0)))
				hdrLbl = fmt.Sprintf("Thought for %ds", secs)
			} else if !reduced {
				hdrLbl = "Thinking" + EllipsisAt(nowMS) + EllipsisPadAt(nowMS)
			}
			btn := ui.ButtonBase(c.Key(fmt.Sprintf("thought-toggle-%d", it.ID))).
				Row().
				Gap(4).
				AlignItems(ui.Center).
				Expanded(isOpen).
				Label(hdrLbl).
				Cursor(ui.CursorPointer)
			btn.Children(func() {
				chevEl := ui.Icon(c.Key(fmt.Sprintf("thought-chev-%d", it.ID)), IconSVG("chevronRight")).FontSize(12).TextColor(C.Ghost)
				targetDeg := float32(0)
				if isOpen {
					targetDeg = 90
				}
				if reduced {
					chevEl.Rotate(targetDeg)
				} else {
					chevEl.Rotate(chevEl.AnimateWith("rot", targetDeg, 155*time.Millisecond, EaseOutQuint))
				}
				ui.Text(c, hdrLbl).FontSize(11).TextColor(C.Ghost)
			})
			if btn.Clicked() {
				a.AgentUI.ExpandedCards[it.ID] = !isOpen
			}
			if isOpen {
				// 2px left rule in C.Border (rgb(43,43,49)) with muted text + smooth accordion expand/collapse
				ui.Box(c.Key(fmt.Sprintf("thought-body-%d", it.ID))).
					FillWidth().
					Padding(2, 0, 2, 11).
					BorderWidth(0, 0, 0, 2).
					BorderColor(C.Border).
					Transition(DrawerCollapseTransition(reduced)).
					Children(func() {
						if it.EndedAt == nil && isLatest && !reduced {
							markdown.Stream(c, it.Text, &a.AgentUI.ThoughtMDOpts)
						} else {
							ui.Text(c, it.Text).FontSize(12).FixedLineHeight(18).TextColor(C.Muted).Wrap().Selectable()
						}
					})
			}
		})

	case "tool":
		isTodo := strings.HasSuffix(it.Name, "TodoWrite")
		if isTodo {
			if it.Header {
				ui.Column(c.Key(itemKey)).FillWidth().Children(func() {
					a.viewAgentHeader(c, it.Agent, it.By)
				})
			}
			return
		}
		isOpen := a.AgentUI.ExpandedCards[it.ID]
		title := friendlyToolLabel(it.Name, it.Running)
		if strings.HasSuffix(it.Name, "typesafe_decision") && it.ScreenName != "" {
			if it.Running {
				title = "Scoring screen · " + it.ScreenName
			} else {
				title = "TypeSafe · " + it.ScreenName
			}
		}
		if it.Running && !reduced {
			title += EllipsisAt(nowMS)
		} else if it.Running {
			title += "…"
		}
		ui.Column(c.Key(itemKey)).FillWidth().Gap(4).Transition(itemTrans).Children(func() {
			if it.Header {
				a.viewAgentHeader(c, it.Agent, it.By)
			}
			card := ui.Column(c.Key(fmt.Sprintf("tool-card-%d", it.ID))).
				FillWidth().
				Radius(7).
				Background(C.Panel2).
				Border(1, C.Border).
				Transition(ColorHoverTransition(reduced))
			card.Children(func() {
				hdr := ui.ButtonBase(c.Key(fmt.Sprintf("tool-btn-%d", it.ID))).
					FillWidth().
					Padding(6, 8).
					Row().
					Gap(6).
					AlignItems(ui.Center).
					Expanded(isOpen).
					Label(title).
					Cursor(ui.CursorPointer)
				hdr.Children(func() {
					r, g, b, _, ok := ParseHexRGBA(it.Agent.Color)
					if !ok {
						r, g, b = 96, 125, 255
					}
					pulse := PulseAt(nowMS)
					ui.Box(c.Key(fmt.Sprintf("tool-dot-wrap-%d", it.ID))).Size(12, 12).Shrink(0).Center().Children(func() {
						if it.Running && !reduced {
							haloAlpha := float32(0.12 + 0.24*pulse)
							haloScale := float32(0.85 + 0.30*pulse)
							ui.Box(c).Absolute().Size(12, 12).Radius(6).
								Background(ui.RGBA(r, g, b, haloAlpha)).
								Scale(haloScale, haloScale).
								IgnoreInput(true)
						}
						coreAlpha := float32(1.0)
						if it.Running && !reduced {
							coreAlpha = float32(0.55 + 0.45*pulse)
						}
						ui.Box(c).Size(6.5, 6.5).Radius(3.5).Background(ui.RGBA(r, g, b, coreAlpha))
					})
					ui.Text(c, title).FontSize(11).FontWeight(500).TextColor(C.Text).Grow(1).MaxLines(1)
					if it.Lines > 0 && it.Running {
						ui.Text(c.Key(fmt.Sprintf("tool-lines-%d", it.ID)), fmt.Sprintf("%d lines", it.Lines)).
							FontSize(10).FontFeatures("tnum").TextColor(C.Ghost)
					}
					if !it.Running {
						if it.TypeSafe != nil && !it.Error {
							ts := it.TypeSafe
							pillText := fmt.Sprintf("%d/100 · PASS", ts.Score)
							pillFg := C.OK
							pillBg := C.Raised
							if ts.Verdict != "pass" {
								if ts.AutoRevise && ts.RevisionAttempt > 0 && ts.MaxRevisions > 0 {
									pillText = fmt.Sprintf("%d/100 · AUTO-REVISE %d/%d (<%d)", ts.Score, ts.RevisionAttempt, ts.MaxRevisions, ts.PassScore)
								} else {
									pillText = fmt.Sprintf("%d/100 · BELOW %d", ts.Score, ts.PassScore)
								}
								if ts.Score < 50 {
									pillFg = C.Danger
									pillBg = C.DangerBg
								} else {
									pillFg = C.Warn
									pillBg = C.WarnBg
								}
							}
							ui.Box(c.Key(fmt.Sprintf("ts-pill-%d", it.ID))).
								Padding(2, 6).
								Radius(5).
								Background(pillBg).
								Border(1, C.Border).
								Children(func() {
									ui.Text(c, pillText).FontSize(10).FontWeight(700).FontFeatures("tnum").TextColor(pillFg)
								})
						} else {
							badge := "✓"
							badgeCol := C.OK
							if it.Error {
								badge = "✕"
								badgeCol = C.Danger
							}
							badgeEl := ui.Text(c.Key(fmt.Sprintf("tool-done-%d", it.ID)), badge).
								FontSize(11).FontWeight(600).TextColor(badgeCol)
							if !reduced && liveEnter {
								badgeEl.Transition(ui.ElementTransition{
									Animation: &IconSwapSpring,
									Enter:     &ui.Motion{Scale: 0.4, Blur: 2},
								})
							}
						}
					}
				})
				if hdr.Clicked() {
					a.AgentUI.ExpandedCards[it.ID] = !isOpen
				}

				// Inline tool screenshots
				for imgIdx, b64 := range it.Images {
					cacheKey := fmt.Sprintf("%d:%d", it.ID, imgIdx)
					entry, ok := a.AgentUI.ImageCache[cacheKey]
					if !ok {
						entry = cachedToolImage{failed: true}
						if raw, err := DecodeBase64Image(b64); err == nil {
							if decoded, err := ui.DecodeBitmap(raw); err == nil {
								entry = cachedToolImage{bmp: decoded}
							}
						}
						for len(a.AgentUI.ImageOrder) >= maxCachedToolImages {
							evictKey := a.AgentUI.ImageOrder[0]
							a.AgentUI.ImageOrder = a.AgentUI.ImageOrder[1:]
							if old, exists := a.AgentUI.ImageCache[evictKey]; exists {
								if old.bmp != nil {
									old.bmp.ReleasePixels()
								}
								delete(a.AgentUI.ImageCache, evictKey)
							}
						}
						a.AgentUI.ImageCache[cacheKey] = entry
						a.AgentUI.ImageOrder = append(a.AgentUI.ImageOrder, cacheKey)
					}
					if entry.bmp != nil {
						imgBox := ui.Box(c.Key(fmt.Sprintf("tool-img-%d-%d", it.ID, imgIdx))).
							Padding(0, 8, 6, 8)
						if !reduced && liveEnter {
							imgBox.Transition(ui.ElementTransition{
								Duration: 200 * time.Millisecond,
								Ease:     EaseOutQuint,
								Enter:    &ui.Motion{Scale: 0.97, Y: 4, Blur: 2},
							})
						}
						imgBox.Children(func() {
							ui.Image(c, entry.bmp).Size(252, 158).Radius(5).Fit(ui.Contain)
						})
					}
				}

				if it.TypeSafe != nil && len(it.TypeSafe.Dimensions) > 0 && !it.Error {
					ui.Row(c.Key(fmt.Sprintf("ts-dims-%d", it.ID))).
						FillWidth().
						Wrap().
						Padding(0, 8, 6, 8).
						Gap(4).
						Children(func() {
							for _, dm := range it.TypeSafe.Dimensions {
								dim := dm
								col := C.Muted
								switch dim.Status {
								case "good":
									col = C.OK
								case "warn":
									col = C.Warn
								case "fail":
									col = C.Danger
								}
								ui.Row(c.Key(fmt.Sprintf("ts-dim-%d-%s", it.ID, dim.Key))).
									Padding(2, 5).
									Gap(3).
									Radius(4).
									Background(C.Panel).
									Border(1, C.Border).
									AlignItems(ui.Center).
									Children(func() {
										ui.Text(c, dim.Label).FontSize(9.5).TextColor(C.Muted)
										ui.Text(c, strconv.Itoa(dim.Score)).FontSize(9.5).FontWeight(600).FontFeatures("tnum").TextColor(col)
									})
							}
						})
				}

				if isOpen {
					ui.Column(c.Key("tool-detail")).
						FillWidth().
						Padding(6, 8, 8, 8).
						Gap(6).
						BorderWidth(1, 0, 0, 0).
						BorderColor(C.Border).
						Transition(DrawerCollapseTransition(reduced)).
						Children(func() {
							const maxInlineToolChars = 3000
							showFull := a.AgentUI.FullOutputCards[it.ID]
							truncatedAny := false
							if it.Output != "" {
								ui.Text(c, "OUTPUT").FontSize(9).FontWeight(700).TextColor(C.Ghost)
								outCol := C.Muted
								if it.Error {
									outCol = C.Danger
								}
								outDisplay := it.Output
								if !showFull && len(outDisplay) > maxInlineToolChars {
									outDisplay = strings.ToValidUTF8(outDisplay[:maxInlineToolChars], "") + "\n… (truncated for smooth scrolling)"
									truncatedAny = true
								}
								ui.Text(c, outDisplay).FontSize(11).Font(MonoFont()).TextColor(outCol).Wrap().Selectable()
							}
							if it.Input != nil {
								if a.AgentUI.ToolInputCache == nil {
									a.AgentUI.ToolInputCache = map[int]string{}
								}
								inText, cached := a.AgentUI.ToolInputCache[it.ID]
								if !cached {
									inText = formatToolInput(it.Input)
									a.AgentUI.ToolInputCache[it.ID] = inText
								}
								if inText != "" {
									hdrName := "INPUT"
									if strings.HasSuffix(it.Name, "execute") {
										hdrName = "SCRIPT"
									}
									inDisplay := inText
									if !showFull && len(inDisplay) > maxInlineToolChars {
										inDisplay = strings.ToValidUTF8(inDisplay[:maxInlineToolChars], "") + "\n… (truncated for smooth scrolling)"
										truncatedAny = true
									}
									ui.Text(c, hdrName).FontSize(9).FontWeight(700).TextColor(C.Ghost)
									ui.Text(c, inDisplay).FontSize(11).Font(MonoFont()).TextColor(C.Muted).Wrap().Selectable()
								}
							}
							if truncatedAny {
								moreBtn := ui.ButtonBase(c.Key(fmt.Sprintf("tool-full-%d", it.ID))).
									Height(22).Padding(0, 8).Radius(5).Row().AlignItems(ui.Center).
									Label("Show full tool output").Cursor(ui.CursorPointer)
								if moreBtn.Hovered() {
									moreBtn.Background(C.Raised)
								} else {
									moreBtn.Background(C.Panel)
								}
								moreBtn.Children(func() {
									ui.Text(c, "Show full output").FontSize(10).TextColor(C.Accent)
								})
								if moreBtn.Clicked() {
									if a.AgentUI.FullOutputCards == nil {
										a.AgentUI.FullOutputCards = map[int]bool{}
									}
									a.AgentUI.FullOutputCards[it.ID] = true
								}
							}
						})
				}
			})
		})

	case "usage":
		isOpen := a.AgentUI.ExpandedCards[it.ID]
		secs := max(1, int(math.Round(float64(it.DurationMS)/1000.0)))
		verb := RunVerb(it.Stopped, it.Failed)
		summary := fmt.Sprintf("%s %s %ds", it.Agent.Name, verb, secs)
		ui.Column(c.Key(itemKey)).FillWidth().Gap(4).Transition(itemTrans).Children(func() {
			btn := ui.ButtonBase(c.Key(fmt.Sprintf("usage-toggle-%d", it.ID))).
				Row().
				Gap(4).
				AlignItems(ui.Center).
				Expanded(isOpen).
				Label(summary).
				Cursor(ui.CursorPointer)
			btn.Children(func() {
				chevEl := ui.Icon(c.Key(fmt.Sprintf("usage-chev-%d", it.ID)), IconSVG("chevronRight")).FontSize(11).TextColor(C.Ghost)
				targetDeg := float32(0)
				if isOpen {
					targetDeg = 90
				}
				if reduced {
					chevEl.Rotate(targetDeg)
				} else {
					chevEl.Rotate(chevEl.AnimateWith("rot", targetDeg, 155*time.Millisecond, EaseOutQuint))
				}
				ui.Text(c, summary).FontSize(11).TextColor(C.Ghost)
			})
			if btn.Clicked() {
				a.AgentUI.ExpandedCards[it.ID] = !isOpen
			}
			if isOpen {
				ui.Column(c.Key(fmt.Sprintf("usage-detail-%d", it.ID))).
					Padding(4, 14).
					Gap(2).
					Transition(DrawerCollapseTransition(reduced)).
					Children(func() {
						ui.Text(c, fmt.Sprintf("Input tokens: %d · Output tokens: %d", it.Usage.Input, it.Usage.Output)).FontSize(10).TextColor(C.Ghost)
						turnCtx := it.Usage.ContextUsed
						turnLim := it.Usage.ContextLimit
						if turnCtx <= 0 && it.Usage.Turns == 1 && (it.Usage.Input > 0 || it.Usage.Output > 0) {
							turnCtx = it.Usage.Input + it.Usage.CacheRead + it.Usage.CacheWrite + it.Usage.Output
						}
						if turnLim <= 0 {
							turnLim = a.effectiveContextLimit()
						}
						if turnCtx > 0 && turnLim > 0 {
							rem := max(0, turnLim-turnCtx)
							ui.Text(c, fmt.Sprintf("Context window: %s / %s · %s remaining", formatTokenCount(turnCtx), formatTokenCount(turnLim), formatTokenCount(rem))).FontSize(10).FontFeatures("tnum").TextColor(C.Ghost)
						}
						if it.Usage.CacheRead > 0 || it.Usage.CacheWrite > 0 {
							ui.Text(c, fmt.Sprintf("Cache reads: %d · Cache writes: %d", it.Usage.CacheRead, it.Usage.CacheWrite)).FontSize(10).TextColor(C.Ghost)
						}
						if it.Usage.Turns > 0 || it.Usage.CostUSD > 0 {
							ui.Text(c, fmt.Sprintf("Turns: %d · Cost: $%.3f", it.Usage.Turns, it.Usage.CostUSD)).FontSize(10).TextColor(C.Ghost)
						}
					})
			}
		})

	case "note":
		ui.Box(c.Key(itemKey)).FillWidth().Transition(itemTrans).Children(func() {
			ui.Text(c, it.Text).FontSize(11).Italic().TextColor(C.Ghost).Wrap()
		})

	case "error":
		ui.Column(c.Key(itemKey)).
			FillWidth().
			Padding(8, 10).
			Gap(6).
			Radius(7).
			Background(C.DangerBg).
			Border(1, C.DangerEdge).
			Transition(itemTrans).
			Children(func() {
				ui.Text(c, it.Text).FontSize(11).TextColor(C.Danger).Wrap().Selectable()
				if it.SignIn && (a.Store.ClaudeAuth == nil || !a.Store.ClaudeAuth.SignedIn) {
					if ActionBtn(c, "error-sign-in", "Sign in to Claude Code", "", "primary").Clicked() {
						a.Bridge.TryCall("claude_auth_login", map[string]any{"console": false}, nil)
					}
				}
			})

	case "external":
		ui.Row(c.Key(itemKey)).Gap(6).AlignItems(ui.Center).Transition(itemTrans).Children(func() {
			col := C.Ghost
			if it.Error {
				col = C.Danger
			}
			ui.Icon(c, IconSVG("plug")).FontSize(12).TextColor(col)
			ui.Text(c, "External agent · "+friendlyToolLabel(it.Text, false)).FontSize(11).TextColor(col).MaxLines(1)
		})

	case "permission":
		ui.Column(c.Key("permission-" + it.ReqID)).
			FillWidth().
			Padding(10).
			Gap(8).
			Radius(8).
			Background(C.WarnBg).
			Border(1, C.WarnEdge).
			Transition(itemTrans).
			Children(func() {
				ui.Text(c, fmt.Sprintf("%s wants to run %s", it.Agent.Name, it.PermTool)).
					FontSize(12).FontWeight(600).TextColor(C.Warn).Wrap()
				inStr := formatToolInput(it.PermInput)
				ui.Scroll(c.Key("permission-input-" + it.ReqID)).
					FillWidth().
					MaxHeight(240).
					Padding(6, 8).
					Radius(6).
					Background(C.Panel2).
					Border(1, C.Border).
					Children(func() {
						ui.Text(c, inStr).FontSize(11).Font(MonoFont()).TextColor(C.Text).Wrap().Selectable()
					})
				ui.Row(c).FillWidth().Wrap().Gap(6).Children(func() {
					answerPerm := func(ans string) {
						a.Bridge.TryCall("agent_permission", map[string]any{
							"id":     it.ReqID,
							"answer": ans,
						}, nil)
						if ans == "auto" {
							a.AgentUI.AutoPerms = true
						}
						a.PollNow(float64(time.Now().UnixMilli()))
					}
					if ActionBtn(c, "permission-auto", "Switch to Auto", "", "").Clicked() {
						answerPerm("auto")
					}
					if ActionBtn(c, "permission-allow", "Allow", "", "primary").Clicked() {
						answerPerm("allow")
					}
					if ActionBtn(c, "permission-deny", "Deny", "", "danger").Clicked() {
						answerPerm("deny")
					}
				})
			})
	}
}

func friendlyToolLabel(name string, running bool) string {
	clean := name
	if idx := strings.LastIndex(clean, "__"); idx >= 0 {
		clean = clean[idx+2:]
	}
	switch clean {
	case "execute":
		if running {
			return "Designing"
		}
		return "Designed"
	case "get_editor_state":
		if running {
			return "Reading editor state"
		}
		return "Got editor state"
	case "get_style":
		if running {
			return "Reading style"
		}
		return "Got style"
	case "read_guide":
		if running {
			return "Reading design guide"
		}
		return "Read design guide"
	case "spawn_agents":
		if running {
			return "Running parallel agents"
		}
		return "Ran parallel agents"
	case "open_document":
		if running {
			return "Opening document"
		}
		return "Opened document"
	case "typesafe_decision":
		if running {
			return "Scoring screen visuals"
		}
		return "TypeSafe visual score"
	default:
		return clean
	}
}

func formatToolInput(in any) string {
	if m, ok := in.(map[string]any); ok {
		if code, ok := m["code"].(string); ok && len(m) == 1 {
			return code
		}
		if cmd, ok := m["command"].(string); ok && len(m) <= 2 {
			return cmd
		}
	}
	b, err := json.MarshalIndent(in, "", "  ")
	if err != nil {
		return fmt.Sprint(in)
	}
	return string(b)
}

func (a *NalarupaApp) viewAgentPlan(c *ui.Context) {
	reduced := EffectiveReducedMotion(c)
	doneCount := 0
	for _, td := range a.Store.Todos {
		if td.Status == "completed" {
			doneCount++
		}
	}
	ui.Column(c.Key("agent-plan")).
		FillWidth().
		Shrink(0).
		Background(C.Panel2).
		BorderWidth(1, 0, 0, 0).
		BorderColor(C.Border).
		Transition(DrawerCollapseTransition(reduced)).
		Children(func() {
			tog := ui.ButtonBase(c.Key("plan-toggle")).
				FillWidth().
				Height(28).
				Padding(0, 10).
				Row().
				Gap(6).
				AlignItems(ui.Center).
				Expanded(a.AgentUI.PlanOpen).
				Label(fmt.Sprintf("Plan %d/%d", doneCount, len(a.Store.Todos))).
				Cursor(ui.CursorPointer)
			tog.Children(func() {
				chevEl := ui.Icon(c.Key("plan-chev"), IconSVG("chevronRight")).FontSize(12).TextColor(C.Muted)
				targetDeg := float32(0)
				if a.AgentUI.PlanOpen {
					targetDeg = 90
				}
				if reduced {
					chevEl.Rotate(targetDeg)
				} else {
					chevEl.Rotate(chevEl.AnimateWith("rot", targetDeg, 155*time.Millisecond, EaseOutQuint))
				}
				ui.Text(c, "Plan").FontSize(11).FontWeight(600).TextColor(C.Text).Grow(1)
				progEl := ui.Text(c.Key("plan-count"), fmt.Sprintf("%d/%d", doneCount, len(a.Store.Todos))).
					FontSize(11).FontFeatures("tnum").TextColor(C.Ghost)
				if !reduced {
					progEl.ContentTransition(ui.ContentTransition{Numeric: true, Animation: &NumericRollAnim})
				}
			})
			if tog.Clicked() {
				a.AgentUI.PlanOpen = !a.AgentUI.PlanOpen
			}

			if a.AgentUI.PlanOpen {
				ui.Scroll(c.Key("plan-todos")).
					FillWidth().
					MaxHeight(160).
					KeepScrollbars(true).
					TrackScroll(&a.AgentUI.PlanScroll).
					Padding(2, 10, 8, 10).
					Gap(4).
					Transition(DrawerCollapseTransition(reduced)).
					Children(func() {
						for i, td := range a.Store.Todos {
							glyph := "○"
							gCol := C.Ghost
							txt := td.Content
							tCol := C.Muted
							switch td.Status {
							case "completed":
								glyph = "✓"
								gCol = C.OK
							case "in_progress":
								glyph = "◐"
								gCol = C.Accent
								tCol = C.Text
								if td.ActiveForm != "" {
									txt = td.ActiveForm
								}
							case "cancelled":
								glyph = "✕"
							}
							ui.Row(c.Key(fmt.Sprintf("todo-%d", i))).
								FillWidth().
								Gap(6).
								AlignItems(ui.Start).
								Children(func() {
									ui.Text(c, glyph).FontSize(11).FixedLineHeight(16).TextColor(gCol).Shrink(0)
									ui.Text(c, txt).FontSize(11).FixedLineHeight(16).TextColor(tCol).Grow(1).Wrap()
								})
						}
					})
			}
		})
}

func (a *NalarupaApp) viewAgentComposer(c *ui.Context, dragOver bool) {
	composer := ui.Column(c.Key("agent-composer")).
		FillWidth().
		Shrink(0).
		Padding(8, 10, 10, 10).
		Gap(8).
		Background(C.Panel).
		BorderWidth(1, 0, 0, 0).
		BorderColor(C.Border)
	if dragOver {
		composer.BorderColor(C.Accent)
	}
	composer.Children(func() {
		activeSkills := activeDraftSkills(a.AgentUI.Draft, a.AgentUI.Skills)
		slashMatches, slashToken, _, slashStart := matchingSlashSkills(a.AgentUI.Draft, a.AgentUI.DismissedSlash, a.AgentUI.Skills)
		if a.AgentUI.DismissedSlash != "" && slashToken != a.AgentUI.DismissedSlash {
			a.AgentUI.DismissedSlash = ""
		}

		a.viewActiveSkillChips(c, activeSkills)
		a.viewSlashSkillAutocomplete(c, slashMatches, slashStart)
		a.viewComposerAttachments(c, dragOver)

			// Live Context Window status bar: displays used context, active limit, and remaining headroom
			ctxUsed, ctxLim, ctxRem, ctxRatio := a.effectiveContextStats()
			usedStr := formatTokenCount(ctxUsed)
			limStr := formatTokenCount(ctxLim)
			remStr := formatTokenCount(ctxRem)
			pctInt := int(math.Round(ctxRatio * 100.0))
			barFillCol := C.Accent
			remTextCol := C.Ghost
			if ctxRatio >= 0.85 {
				barFillCol = C.Danger
				remTextCol = C.Danger
			} else if ctxRatio >= 0.65 {
				barFillCol = C.Warn
				remTextCol = C.Warn
			}
			ctxLabel := fmt.Sprintf("Context window: %s used of %s (%d%%), %s remaining", usedStr, limStr, pctInt, remStr)
			ctxBtn := ui.ButtonBase(c.Key("agent-context-bar")).
				FillWidth().
				Height(22).
				Padding(0, 6).
				Radius(5).
				Row().
				Gap(6).
				AlignItems(ui.Center).
				Label(ctxLabel).
				Tooltip(ctxLabel + " — click to configure context window limit").
				Cursor(ui.CursorPointer)
			if ctxBtn.Hovered() {
				ctxBtn.Background(C.Raised)
			} else {
				ctxBtn.Background(C.Panel2)
			}
			ctxBtn.Children(func() {
				// 32x4 micro progress meter
				ui.Box(c).Size(32, 4).Shrink(0).Radius(2).Background(C.Field).Clip().Children(func() {
					fillW := float32(math.Round(32.0 * ctxRatio))
					if ctxUsed > 0 && fillW < 2 {
						fillW = 2
					}
					if fillW > 0 {
						ui.Box(c).Size(fillW, 4).Radius(2).Background(barFillCol)
					}
				})
				ui.Text(c, fmt.Sprintf("Context %s / %s", usedStr, limStr)).
					FontSize(10).FontFeatures("tnum").TextColor(C.Muted).MaxLines(1)
				if ctxUsed > 0 {
					ui.Text(c, fmt.Sprintf("(%d%%)", pctInt)).
						FontSize(10).FontFeatures("tnum").TextColor(C.Ghost).MaxLines(1)
				}
				ui.Spacer(c)
				ui.Text(c, fmt.Sprintf("%s remaining", remStr)).
					FontSize(10).FontFeatures("tnum").TextColor(remTextCol).MaxLines(1)
			})
			if ctxBtn.Clicked() {
				a.AgentUI.SettingsOpen = !a.AgentUI.SettingsOpen
				if a.AgentUI.SettingsOpen {
					a.reloadAgentSettings()
				}
			}

			ph := "Design anything… (/ for skills)"
			if a.State.AgentRunning {
				ph = "Queue a follow-up…"
			}
			svc := c.Services()
			ta := ui.TextArea(c.Key("agent-input"), &a.AgentUI.Draft).
				FillWidth().
				Padding(6, 8).
				Radius(8).
				Background(C.Panel2).
				Border(1, C.Field).
				TextColor(C.Text).
				CaretColor(C.Accent).
				FontSize(12).
				Lines(2, 5).
				Placeholder(ph).
				PlaceholderColor(C.Ghost).
				Label("Message the design agent").
				Bind(&a.AgentUI.InputHandle)

			ta.OnPaste(func(text string) bool {
				data := svc.ReadClipboardData(transfer.PNG, transfer.FileList)
				if raw, err := data.Read(transfer.PNG); err == nil && len(raw) > 0 {
					a.AttachImageBytes("Pasted image.png", raw)
					return true
				}
				if paths, err := data.Files(); err == nil && len(paths) > 0 {
					a.AttachImageFiles(paths)
					return true
				}
				return false
			})

			ta.OnTextKey(func(k ui.TextKey) bool {
				if k.Key == ui.KeyEscape && a.AgentUI.PickingCanvasCtx {
					a.AgentUI.PickingCanvasCtx = false
					return true
				}
				if len(slashMatches) > 0 {
					switch k.Key {
					case ui.KeyDown:
						a.AgentUI.SlashIndex = (a.AgentUI.SlashIndex + 1) % len(slashMatches)
						return true
					case ui.KeyUp:
						a.AgentUI.SlashIndex = (a.AgentUI.SlashIndex - 1 + len(slashMatches)) % len(slashMatches)
						return true
					case ui.KeyEscape:
						a.AgentUI.DismissedSlash = slashToken
						return true
					case ui.KeyTab:
						idx := a.AgentUI.SlashIndex
						if idx < 0 || idx >= len(slashMatches) {
							idx = 0
						}
						next := completeSlashSkill(k.Text(), slashStart, slashMatches[idx].Name)
						k.SetText(next)
						a.AgentUI.Draft = next
						a.AgentUI.DismissedSlash = ""
						a.AgentUI.SlashIndex = 0
						return true
					case ui.KeyEnter:
						if k.Mods&ui.Shift == 0 {
							idx := a.AgentUI.SlashIndex
							if idx < 0 || idx >= len(slashMatches) {
								idx = 0
							}
							next := completeSlashSkill(k.Text(), slashStart, slashMatches[idx].Name)
							k.SetText(next)
							a.AgentUI.Draft = next
							a.AgentUI.DismissedSlash = ""
							a.AgentUI.SlashIndex = 0
							return true
						}
					}
				}
				if k.Key == ui.KeyTab {
					// Never insert literal tabs into composer; allow Tab focus traversal
					return false
				}
				if k.Key == ui.KeyEnter && (k.Mods&ui.Shift == 0) {
					txt := k.Text()
					if a.submitAgentPrompt(txt, true) {
						k.SetText("")
					}
					return true
				}
				return false
			})

			// Parallel agents selector (1x..4x + Split / Side by side)
			ui.Column(c.Key("composer-parallel-wrap")).FillWidth().Gap(4).Children(func() {
				nextPar, parChanged := SegmentedTabs(c, "composer-parallel", "Parallel agents", a.AgentUI.Parallel, []SegOption[int]{
					{Key: "parallel-1", Value: 1, Label: "1×"},
					{Key: "parallel-2", Value: 2, Label: "2×"},
					{Key: "parallel-3", Value: 3, Label: "3×"},
					{Key: "parallel-4", Value: 4, Label: "4×"},
				})
				if parChanged {
					a.AgentUI.Parallel = nextPar
				}
				if a.AgentUI.Parallel > 1 {
					ui.Box(c.Key("composer-parallel-mode-wrap")).
						FillWidth().
						Transition(DrawerCollapseTransition(EffectiveReducedMotion(c))).
						Children(func() {
							nextMode, modeChanged := SegmentedTabs(c, "composer-parallel-mode", "Parallel mode", a.AgentUI.ParallelMode, []SegOption[string]{
								{Key: "mode-split", Value: "split", Label: "Split"},
								{Key: "mode-side", Value: "side", Label: "Side by side"},
							})
							if modeChanged {
								a.AgentUI.ParallelMode = nextMode
							}
						})
				}
			})

			// Bottom controls row: ModelPicker + Effort + Skills + Fast + CookMenu + Stop/Send
			var modelBtn, effortBtn, skillsBtn, cookBtn ui.Element
			ui.Row(c.Key("composer-bottom")).FillWidth().Gap(3).AlignItems(ui.Center).Children(func() {
				modLabel := a.effectiveModelLabel()
				modelBtn = ui.ButtonBase(c.Key("agent-model")).
					Height(26).
					MaxWidth(96).
					MinWidth(42).
					Padding(0, 6).
					Radius(6).
					Row().
					Gap(3).
					AlignItems(ui.Center).
					Shrink(1).
					Label("Model: " + modLabel).
					Tooltip("Model: " + modLabel).
					Bind(&a.AgentUI.ModelBtnHandle).
					Cursor(ui.CursorPointer)
				if modelBtn.Hovered() || a.AgentUI.ModelOpen {
					modelBtn.Background(C.Raised)
				} else {
					modelBtn.Background(C.Panel2)
				}
				modelBtn.Border(1, C.Border).Children(func() {
					ui.Text(c, modLabel).FontSize(11).TextColor(C.Text).MaxLines(1).Shrink(1).MinWidth(0)
					ui.Icon(c, IconSVG("chevronDown")).FontSize(10).TextColor(C.Ghost).Shrink(0)
				})
				if modelBtn.Clicked() {
					a.AgentUI.ModelOpen = !a.AgentUI.ModelOpen
				}

				curEff := a.effectiveEffort()
				effLabel := effortLabel(curEff)
				effShort := effortShortLabel(curEff)
				effortBtn = ui.ButtonBase(c.Key("agent-effort")).
					Height(26).
					Padding(0, 6).
					Radius(6).
					Row().
					Gap(3).
					AlignItems(ui.Center).
					Shrink(0).
					Label("Reasoning effort: " + effLabel).
					Tooltip("Reasoning effort: " + effLabel).
					Bind(&a.AgentUI.EffortBtnHandle).
					Cursor(ui.CursorPointer)
				if effortBtn.Hovered() || a.AgentUI.EffortOpen {
					effortBtn.Background(C.Raised)
				} else {
					effortBtn.Background(C.Panel2)
				}
				effortBtn.Border(1, C.Border).Children(func() {
					ui.Text(c, effShort).FontSize(11).TextColor(C.Muted).MaxLines(1)
					ui.Icon(c, IconSVG("chevronDown")).FontSize(10).TextColor(C.Ghost)
				})
				if effortBtn.Clicked() {
					a.AgentUI.EffortOpen = !a.AgentUI.EffortOpen
				}

				skillBtnText := "/ Skills"
				if len(activeSkills) > 0 {
					skillBtnText = fmt.Sprintf("/ %d", len(activeSkills))
				}
				skillsBtn = ui.ButtonBase(c.Key("agent-skills")).
					Height(26).
					Padding(0, 6).
					Radius(6).
					Row().
					Gap(3).
					AlignItems(ui.Center).
					Shrink(0).
					Label("Agent skills").
					Tooltip("Select local agent skills (/skill)").
					Bind(&a.AgentUI.SkillsBtnHandle).
					Cursor(ui.CursorPointer)
				if len(activeSkills) > 0 {
					skillsBtn.Background(C.AccentSoft).Border(1, C.Accent)
				} else if skillsBtn.Hovered() || a.AgentUI.SkillsOpen {
					skillsBtn.Background(C.Raised).Border(1, C.Border)
				} else {
					skillsBtn.Background(C.Panel2).Border(1, C.Border)
				}
				skillsBtn.Children(func() {
					col := C.Muted
					if len(activeSkills) > 0 {
						col = C.Accent
					}
					ui.Text(c, skillBtnText).FontSize(11).TextColor(col).MaxLines(1)
				})
				if skillsBtn.Clicked() {
					a.AgentUI.SkillsOpen = !a.AgentUI.SkillsOpen
					if a.AgentUI.SkillsOpen {
						a.ensureSkillsLoaded(false)
					}
				}

				if a.AgentUI.Backend != "provider" && slices.Contains(fastCapableModels, a.effectiveModelID()) {
					if _, clicked := ToggleChip(c, "agent-fast", "Fast", a.AgentUI.Fast); clicked {
						a.setSetting("agent_fast", !a.AgentUI.Fast)
					}
				}

				ui.Spacer(c)

				cookText := "1x"
				cookA11y := "Let it cook: off"
				cookActive := a.AgentUI.CookDesigns > 1
				if cookActive {
					cookText = fmt.Sprintf("%dx", a.AgentUI.CookDesigns)
					cookA11y = fmt.Sprintf("Let it cook: %d variations (%s)", a.AgentUI.CookDesigns, a.AgentUI.CookIterate)
				}
				cookBtn = ui.ButtonBase(c.Key("agent-cook")).
					Height(26).
					Padding(0, 6).
					Radius(6).
					Row().
					Gap(3).
					AlignItems(ui.Center).
					Shrink(0).
					Label(cookA11y).
					Tooltip("Let it cook").
					Bind(&a.AgentUI.CookBtnHandle).
					Cursor(ui.CursorPointer)
				if cookActive {
					cookBtn.Background(C.AccentSoft).Border(1, C.Accent)
				} else if cookBtn.Hovered() {
					cookBtn.Background(C.Raised).Border(1, C.Border)
				} else {
					cookBtn.Background(C.Panel2).Border(1, C.Border)
				}
				cookBtn.Children(func() {
					col := C.Muted
					if cookActive {
						col = C.Accent
					}
					ui.Icon(c, IconSVG("sparkles")).FontSize(12).TextColor(col)
					ui.Text(c, cookText).FontSize(11).FontFeatures("tnum").TextColor(col)
				})
				if cookBtn.Clicked() {
					a.AgentUI.CookOpen = !a.AgentUI.CookOpen
				}

				if a.State.AgentRunning || a.State.PenDrawing {
					stopBtn := DangerIconBtn(c, "agent-stop", "stop", "Stop", 26)
					if stopBtn.Clicked() {
						a.Bridge.TryCall("agent_stop", nil, nil)
					}
				}

				sendDisabled := strings.TrimSpace(a.AgentUI.Draft) == "" && !a.hasPendingAttachments()
				sendBtn := AccentIconBtn(c, "agent-send", "send", "Send", 26, sendDisabled)
				if sendBtn.Clicked() && !sendDisabled {
					a.submitAgentPrompt(a.AgentUI.Draft, true)
				}
			})

			// ModelPicker popover (origin-aware from bottom-left trigger)
			if modelBtn.Valid() {
				mp := AnimatedPopover(c, modelBtn, &a.AgentUI.ModelOpen, 0, 1, func() {
					selectModel := func(mod string) {
						if a.AgentUI.Backend == "provider" {
							if ap := a.activeProvider(); ap != nil && mod != "" && !slices.Contains(ap.Models, mod) {
								updatedModels := append(append([]string(nil), ap.Models...), mod)
								payload := map[string]any{
									"id":             ap.ID,
									"name":           ap.Name,
									"format":         ap.Format,
									"base_url":       ap.BaseURL,
									"models":         updatedModels,
									"vision":         ap.Vision,
									"context_window": ap.ContextWindow,
								}
								a.Bridge.TryCall("provider_save", map[string]any{"provider": payload}, nil)
							}
							a.setSetting("provider_model", mod)
						} else if mod == "" {
							a.setSetting("agent_model", nil)
						} else {
							a.setSetting("agent_model", mod)
						}
						a.AgentUI.ModelOpen = false
					}
					addModelOption := func(keySuffix, mod, label string, sel bool) {
						b := ui.ButtonBase(c.Key("model-opt-" + keySuffix)).
							FillWidth().Height(26).Padding(0, 8).Radius(5).Row().AlignItems(ui.Center).
							Selected(sel).Label(label).Cursor(ui.CursorPointer)
						if sel || b.Hovered() {
							b.Background(C.Raised)
						}
						b.Children(func() {
							ui.Text(c, label).FontSize(12).TextColor(C.Text).MaxLines(1)
						})
						if b.Clicked() {
							selectModel(mod)
						}
					}

					ui.Column(c.Key("model-menu")).Width(236).Padding(6).Gap(4).Background(C.Panel2).Radius(8).Children(func() {
						ui.Scroll(c.Key("model-menu-scroll")).TrackScroll(&a.AgentUI.ModelScroll).FillWidth().MaxHeight(240).Gap(2).Children(func() {
							if a.AgentUI.Backend == "provider" {
								ap := a.activeProvider()
								if ap == nil || len(ap.Models) == 0 {
									ui.Text(c, "Configure or fetch models below.").FontSize(11).TextColor(C.Ghost).Padding(6)
								} else {
									for _, m := range ap.Models {
										addModelOption(m, m, m, a.effectiveModelID() == m)
									}
								}
								if ap != nil {
									lbl := "Fetch models"
									if a.AgentUI.FetchingModels {
										lbl = "Fetching…"
									}
									rb := ui.ButtonBase(c.Key("model-refresh-prov")).
										FillWidth().Height(26).Padding(0, 8).Radius(5).Row().AlignItems(ui.Center).
										Label("Fetch models").Cursor(ui.CursorPointer)
									if rb.Hovered() {
										rb.Background(C.Raised)
									}
									rb.Children(func() {
										ui.Text(c, lbl).FontSize(11).TextColor(C.Accent)
									})
									if rb.Clicked() {
										a.requestFetchModels("provider", ap.ID, ap.Format, ap.BaseURL, "", true, 0)
									}
								}
								return
							}

							if a.AgentUI.Backend == "claude-code" {
								addModelOption("default", "", "Default", a.AgentUI.AgentModel == "")
							}
							seenIDs := map[string]bool{}
							for _, cm := range builtinClaudeModels {
								seenIDs[cm.ID] = true
								addModelOption(cm.ID, cm.ID, cm.Label, a.effectiveModelID() == cm.ID)
							}
							if a.AgentUI.Backend == "claude-code" {
								ui.Text(c, "CLI aliases").FontSize(10).FontWeight(600).TextColor(C.Ghost).Padding(4, 8, 2, 8)
								for _, al := range claudeCLIAliases {
									seenIDs[al.ID] = true
									addModelOption("alias-"+al.ID, al.ID, al.Label, a.effectiveModelID() == al.ID)
								}
							}
							if a.AgentUI.Backend == "api" {
								var extras []string
								for _, m := range a.AgentUI.APIModels {
									if !seenIDs[m] {
										seenIDs[m] = true
										extras = append(extras, m)
									}
								}
								if len(extras) > 0 {
									ui.Text(c, "Live /v1/models").FontSize(10).FontWeight(600).TextColor(C.Ghost).Padding(4, 8, 2, 8)
									for _, m := range extras {
										addModelOption("live-"+m, m, m, a.effectiveModelID() == m)
									}
								}
								lbl := "Refresh models"
								if a.AgentUI.FetchingModels {
									lbl = "Refreshing…"
								}
								rb := ui.ButtonBase(c.Key("model-refresh-api")).
									FillWidth().Height(26).Padding(0, 8).Radius(5).Row().AlignItems(ui.Center).
									Label("Refresh models").Cursor(ui.CursorPointer)
								if rb.Hovered() {
									rb.Background(C.Raised)
								}
								rb.Children(func() {
									ui.Text(c, lbl).FontSize(11).TextColor(C.Accent)
								})
								if rb.Clicked() {
									a.requestFetchModels("api", "", "anthropic", a.AgentUI.BaseURL, "", true, 0)
								}
							}
						})

						CommittedField(c, FieldOpts{
							Key:         "custom-model-id",
							AriaLabel:   "Custom model ID",
							Value:       a.AgentUI.CustomModelDraft,
							Placeholder: "Custom model ID…",
							Mono:        true,
							OnCommit: func(val string) bool {
								trimmed := strings.TrimSpace(val)
								if trimmed == "" {
									return false
								}
								a.AgentUI.CustomModelDraft = ""
								selectModel(trimmed)
								return true
							},
						})
					})
				})
				if mp.Dismissed() {
					a.AgentUI.ModelBtnHandle.Focus()
				}
			}

			a.viewEffortPopover(c, effortBtn)
			a.viewSkillsPopover(c, skillsBtn)

			// CookMenu ("Let it cook") popover (origin-aware from bottom-right trigger)
			if cookBtn.Valid() {
				cp := AnimatedPopover(c, cookBtn, &a.AgentUI.CookOpen, 0.85, 1, func() {
					ui.Column(c.Key("cook-menu")).Width(230).Padding(10).Gap(8).Background(C.Panel2).Radius(8).Children(func() {
						ui.Text(c, "Let it cook").FontSize(12).FontWeight(600).TextColor(C.Text)
						ui.Text(c, "Keep producing variations after the first design finishes.").FontSize(11).TextColor(C.Muted).Wrap()

						nextMode, modeChanged := SegmentedTabs(c, "cook-mode", "Variation axis", a.AgentUI.CookIterate, []SegOption[string]{
							{Key: "cook-mode-layout", Value: "layout", Label: "Layout"},
							{Key: "cook-mode-style", Value: "style", Label: "Style"},
						})
						if modeChanged {
							a.setSetting("agent_iterate", nextMode)
						}

						ui.Row(c.Key("cook-counts")).FillWidth().Wrap().Gap(4).Children(func() {
							for n := 1; n <= 6; n++ {
								cnt := n
								lbl := fmt.Sprintf("%d", cnt)
								if cnt == 1 {
									lbl = "Don't iterate"
								}
								sel := a.AgentUI.CookDesigns == cnt
								chip := ui.ButtonBase(c.Key(fmt.Sprintf("cook-count-%d", cnt))).
									Height(24).Padding(0, 8).Radius(5).Center().
									Selected(sel).Label(lbl).Cursor(ui.CursorPointer)
								if sel {
									chip.Background(C.AccentSoft).Border(1, C.Accent)
								} else if chip.Hovered() {
									chip.Background(C.Raised).Border(1, C.Border)
								} else {
									chip.Background(C.Panel).Border(1, C.Border)
								}
								chip.Children(func() {
									col := C.Muted
									if sel {
										col = C.Accent
									}
									ui.Text(c, lbl).FontSize(11).TextColor(col)
								})
								if chip.Clicked() {
									a.setSetting("agent_designs", cnt)
								}
							}
						})
					})
				})
				if cp.Dismissed() {
					a.AgentUI.CookBtnHandle.Focus()
				}
			}
		})
}
