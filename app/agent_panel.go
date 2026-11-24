package main

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

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
	{ID: "claude-opus-5-5", Label: "Opus 5.5"},
	{ID: "claude-sonnet-5-5", Label: "Sonnet 5.5"},
	{ID: "claude-haiku-5-5", Label: "Haiku 5.5"},
	{ID: "claude-fable-5-1", Label: "Fable 5.1"},
}

var fastCapableModels = []string{
	"claude-opus-5-5",
	"claude-opus-5",
	"claude-opus-4-8",
}

// ProviderConfig mirrors core-go BYOK provider configuration.
type ProviderConfig struct {
	ID      string   `json:"id,omitempty"`
	Name    string   `json:"name"`
	Format  string   `json:"format"` // "chat" | "responses" | "anthropic"
	BaseURL string   `json:"base_url"`
	APIKey  string   `json:"api_key,omitempty"`
	Models  []string `json:"models"`
	Vision  bool     `json:"vision"`
}

// ChatSummary represents a saved chat entry returned by "chat_list".
type ChatSummary struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	UpdatedAt int64  `json:"updatedAt"`
}

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

	SettingsOpen bool
	HistoryOpen  bool
	ModelOpen    bool
	CookOpen     bool
	PlanOpen     bool

	HistoryBtnHandle ui.Handle
	CookBtnHandle    ui.Handle
	ModelBtnHandle   ui.Handle

	Chats          []ChatSummary
	CurrentChatID  string
	ArmedRemoveID  string
	ConfirmClear   bool
	AuthLoggingIn  bool

	EditingProv    *ProviderConfig
	ProvKeyDraft   string
	ProvModelsCSV  string
	TestingProvID  string

	ExpandedCards  map[int]bool
	ImageCache     map[string]*ui.Bitmap

	TranscriptScroll ui.ScrollState
	SettingsScroll   ui.ScrollState
	PlanScroll       ui.ScrollState
	StickToBottom    bool
	LastItemCount    int
	LastSubmitAt     time.Time

	MDOpts        markdown.Options
	ThoughtMDOpts markdown.Options
}

func (a *NalarupaApp) initAgentPanelState() {
	a.AgentUI = AgentPanelState{
		Parallel:      1,
		ParallelMode:  "split",
		Backend:       "claude-code",
		AgentModel:    "",
		CookDesigns:   1,
		CookIterate:   "layout",
		Effort:        "high",
		PlanOpen:      true,
		ExpandedCards: map[int]bool{},
		ImageCache:    map[string]*ui.Bitmap{},
		StickToBottom: true,
	}
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

func (a *NalarupaApp) submitAgentPrompt(prompt string, returnFocusToCanvas bool) {
	trimmed := strings.TrimSpace(prompt)
	if trimmed == "" {
		return
	}
	now := time.Now()
	if now.Sub(a.AgentUI.LastSubmitAt) < 80*time.Millisecond {
		return
	}
	a.AgentUI.LastSubmitAt = now
	a.AgentUI.Draft = ""
	a.AgentUI.StickToBottom = true

	if err := a.Bridge.Call("agent_send", map[string]any{
		"text":     trimmed,
		"parallel": a.AgentUI.Parallel,
		"mode":     a.AgentUI.ParallelMode,
		"designs":  a.AgentUI.CookDesigns,
		"iterate":  a.AgentUI.CookIterate,
	}, nil); err != nil {
		a.ShowToast(err.Error(), true)
		return
	}
	a.PollNow(float64(now.UnixMilli()))
	if returnFocusToCanvas {
		a.CanvasHandle.Focus()
	}
}

func (a *NalarupaApp) viewAgentPanel(c *ui.Context) {
	reduced := EffectiveReducedMotion(c)
	nowMS := time.Now().UnixMilli()

	ui.Column(c.Key("agent-panel")).Fill().Children(func() {
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

		// ChatHistory Popover
		if histBtn.Valid() {
			pop := ui.Popover(c, histBtn, &a.AgentUI.HistoryOpen, func() {
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
				Fill().
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
		a.viewAgentComposer(c)
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
								a.Store.RestoreChat(loaded.Events)
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
	ui.Scroll(c.Key("agent-settings-panel")).
		FillWidth().
		MaxHeight(260).
		Shrink(1).
		TrackScroll(&a.AgentUI.SettingsScroll).
		Padding(10, 12).
		Gap(10).
		Background(C.Panel2).
		BorderWidth(0, 0, 1, 0).
		BorderColor(C.Border).
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
				CommittedField(c, FieldOpts{
					Key:       "api-effort",
					AriaLabel: "Reasoning effort",
					Value:     a.AgentUI.Effort,
					Width:     90,
					OnCommit: func(val string) bool {
						trimmed := strings.TrimSpace(val)
						if trimmed == "" {
							trimmed = "high"
						}
						a.setSetting("agent_effort", trimmed)
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
				Placeholder: "gpt-5, gpt-5-mini",
				Mono:        true,
				OnCommit: func(v string) bool {
					a.AgentUI.ProvModelsCSV = v
					return true
				},
			})
			if _, clicked := ToggleChip(c, "provider-vision", "The model can see images (screenshots)", ed.Vision); clicked {
				ed.Vision = !ed.Vision
			}
			ui.Row(c).Gap(6).Children(func() {
				if ActionBtn(c, "provider-save", "Save", "", "primary").Clicked() {
					var models []string
					for _, part := range strings.Split(a.AgentUI.ProvModelsCSV, ",") {
						if m := strings.TrimSpace(part); m != "" {
							models = append(models, m)
						}
					}
					payload := map[string]any{
						"name":     ed.Name,
						"format":   ed.Format,
						"base_url": ed.BaseURL,
						"models":   models,
						"vision":   ed.Vision,
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
						a.AgentUI.EditingProv = nil
					}
				}
				if ActionBtn(c, "provider-cancel", "Cancel", "", "").Clicked() {
					a.AgentUI.EditingProv = nil
				}
				if ed.ID != "" {
					if ActionBtn(c, "provider-remove", "Remove", "", "danger").Clicked() {
						a.Bridge.TryCall("provider_remove", map[string]any{"id": ed.ID}, nil)
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
			a.AgentUI.EditingProv = &ProviderConfig{Format: "chat", Vision: true}
			a.AgentUI.ProvKeyDraft = ""
			a.AgentUI.ProvModelsCSV = ""
		}
		if ap := a.activeProvider(); ap != nil {
			if ActionBtn(c, "provider-edit", "Edit", "", "").Clicked() {
				cp := *ap
				a.AgentUI.EditingProv = &cp
				a.AgentUI.ProvKeyDraft = ""
				a.AgentUI.ProvModelsCSV = strings.Join(ap.Models, ", ")
			}
			if ActionBtn(c, "provider-test", "Test", "", "").Clicked() {
				a.AgentUI.TestingProvID = ap.ID
				a.Bridge.TryCall("provider_test", map[string]any{"id": ap.ID}, nil)
			}
		}
	})

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

func (a *NalarupaApp) viewAgentTranscript(c *ui.Context, reduced bool, nowMS int64) {
	hasTickingItem := false
	for _, it := range a.Store.Items {
		if ItemChanging(it) && !reduced {
			hasTickingItem = true
			break
		}
	}

	sc := ui.Scroll(c.Key("agent-transcript")).
		Fill().
		TrackScroll(&a.AgentUI.TranscriptScroll).
		Padding(10, 12).
		Gap(10).
		Role(ui.RoleLog).
		Label("Conversation transcript").
		Focusable()

	sc.HandleInput(func(ev ui.InputEvent) bool {
		if ev.Kind == ui.InputScroll {
			if ev.DY < 0 {
				a.AgentUI.StickToBottom = false
			} else if a.AgentUI.TranscriptScroll.Y >= a.AgentUI.TranscriptScroll.MaxY-2 {
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

	if len(a.Store.Items) != a.AgentUI.LastItemCount {
		a.AgentUI.LastItemCount = len(a.Store.Items)
		if a.AgentUI.StickToBottom {
			a.AgentUI.TranscriptScroll.Y = math.MaxFloat32
		}
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
	switch it.Kind {
	case "user", "queued":
		isQueued := it.Kind == "queued"
		ui.Column(c.Key(itemKey)).FillWidth().AlignItems(ui.End).Gap(3).Children(func() {
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
			if isQueued {
				ui.Text(c, "Queued — sent when the agents finish").FontSize(10).TextColor(C.Ghost)
			}
		})

	case "assistant":
		ui.Column(c.Key(itemKey)).FillWidth().Gap(4).Children(func() {
			if it.Header {
				a.viewAgentHeader(c, it.Agent, it.By)
			}
			if it.Streaming && isLatest && !reduced {
				markdown.Stream(c, it.Text, &a.AgentUI.MDOpts)
			} else {
				markdown.View(c, it.Text, &a.AgentUI.MDOpts)
			}
		})

	case "thinking":
		isOpen := a.AgentUI.ExpandedCards[it.ID]
		ui.Column(c.Key(itemKey)).FillWidth().Gap(4).Children(func() {
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
				chev := "chevronRight"
				if isOpen {
					chev = "chevronDown"
				}
				ui.Icon(c, IconSVG(chev)).FontSize(12).TextColor(C.Ghost)
				ui.Text(c, hdrLbl).FontSize(11).TextColor(C.Ghost)
			})
			if btn.Clicked() {
				a.AgentUI.ExpandedCards[it.ID] = !isOpen
			}
			if isOpen {
				// 2px left rule in C.Border (rgb(43,43,49)) with muted text
				ui.Box(c.Key(fmt.Sprintf("thought-body-%d", it.ID))).
					FillWidth().
					Padding(2, 0, 2, 11).
					BorderWidth(0, 0, 0, 2).
					BorderColor(C.Border).
					Children(func() {
						ui.Text(c, it.Text).FontSize(12).FixedLineHeight(18).TextColor(C.Muted).Wrap().Selectable()
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
		if it.Running && !reduced {
			title += EllipsisAt(nowMS)
		} else if it.Running {
			title += "…"
		}
		ui.Column(c.Key(itemKey)).FillWidth().Gap(4).Children(func() {
			if it.Header {
				a.viewAgentHeader(c, it.Agent, it.By)
			}
			card := ui.Column(c.Key(fmt.Sprintf("tool-card-%d", it.ID))).
				FillWidth().
				Radius(7).
				Background(C.Panel2).
				Border(1, C.Border)
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
					alpha := float32(1.0)
					if it.Running && !reduced {
						alpha = float32(0.35 + 0.65*PulseAt(nowMS))
					}
					ui.Box(c).Size(7, 7).Shrink(0).Radius(3.5).Background(ui.RGBA(r, g, b, alpha))
					ui.Text(c, title).FontSize(11).FontWeight(500).TextColor(C.Text).Grow(1).MaxLines(1)
					if it.Lines > 0 && it.Running {
						ui.Text(c, fmt.Sprintf("%d lines", it.Lines)).FontSize(10).TextColor(C.Ghost)
					}
					if !it.Running {
						badge := "✓"
						badgeCol := C.OK
						if it.Error {
							badge = "✕"
							badgeCol = C.Danger
						}
						ui.Text(c, badge).FontSize(11).FontWeight(600).TextColor(badgeCol)
					}
				})
				if hdr.Clicked() {
					a.AgentUI.ExpandedCards[it.ID] = !isOpen
				}

				// Inline tool screenshots
				for imgIdx, b64 := range it.Images {
					cacheKey := fmt.Sprintf("%d:%d", it.ID, imgIdx)
					bmp, ok := a.AgentUI.ImageCache[cacheKey]
					if !ok {
						if raw, err := DecodeBase64Image(b64); err == nil {
							if decoded, err := ui.DecodeBitmap(raw); err == nil {
								bmp = decoded
								a.AgentUI.ImageCache[cacheKey] = bmp
							}
						}
					}
					if bmp != nil {
						ui.Box(c.Key(fmt.Sprintf("tool-img-%d-%d", it.ID, imgIdx))).
							Padding(0, 8, 6, 8).
							Children(func() {
								ui.Image(c, bmp).Size(252, 158).Radius(5).Fit(ui.Contain)
							})
					}
				}

				if isOpen {
					ui.Column(c.Key("tool-detail")).
						FillWidth().
						Padding(6, 8, 8, 8).
						Gap(6).
						BorderWidth(1, 0, 0, 0).
						BorderColor(C.Border).
						Children(func() {
							if it.Output != "" {
								ui.Text(c, "OUTPUT").FontSize(9).FontWeight(700).TextColor(C.Ghost)
								outCol := C.Muted
								if it.Error {
									outCol = C.Danger
								}
								ui.Text(c, it.Output).FontSize(11).Font(MonoFont()).TextColor(outCol).Wrap().Selectable()
							}
							if it.Input != nil {
								inText := formatToolInput(it.Input)
								if inText != "" {
									hdrName := "INPUT"
									if strings.HasSuffix(it.Name, "execute") {
										hdrName = "SCRIPT"
									}
									ui.Text(c, hdrName).FontSize(9).FontWeight(700).TextColor(C.Ghost)
									ui.Text(c, inText).FontSize(11).Font(MonoFont()).TextColor(C.Muted).Wrap().Selectable()
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
		ui.Column(c.Key(itemKey)).FillWidth().Gap(4).Children(func() {
			btn := ui.ButtonBase(c.Key(fmt.Sprintf("usage-toggle-%d", it.ID))).
				Row().
				Gap(4).
				AlignItems(ui.Center).
				Expanded(isOpen).
				Label(summary).
				Cursor(ui.CursorPointer)
			btn.Children(func() {
				chev := "chevronRight"
				if isOpen {
					chev = "chevronDown"
				}
				ui.Icon(c, IconSVG(chev)).FontSize(11).TextColor(C.Ghost)
				ui.Text(c, summary).FontSize(11).TextColor(C.Ghost)
			})
			if btn.Clicked() {
				a.AgentUI.ExpandedCards[it.ID] = !isOpen
			}
			if isOpen {
				ui.Column(c).Padding(4, 14).Gap(2).Children(func() {
					ui.Text(c, fmt.Sprintf("Input tokens: %d · Output tokens: %d", it.Usage.Input, it.Usage.Output)).FontSize(10).TextColor(C.Ghost)
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
		ui.Box(c.Key(itemKey)).FillWidth().Children(func() {
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
			Children(func() {
				ui.Text(c, it.Text).FontSize(11).TextColor(C.Danger).Wrap().Selectable()
				if it.SignIn && (a.Store.ClaudeAuth == nil || !a.Store.ClaudeAuth.SignedIn) {
					if ActionBtn(c, "error-sign-in", "Sign in to Claude Code", "", "primary").Clicked() {
						a.Bridge.TryCall("claude_auth_login", map[string]any{"console": false}, nil)
					}
				}
			})

	case "external":
		ui.Row(c.Key(itemKey)).Gap(6).AlignItems(ui.Center).Children(func() {
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
				chev := "chevronRight"
				if a.AgentUI.PlanOpen {
					chev = "chevronDown"
				}
				ui.Icon(c, IconSVG(chev)).FontSize(12).TextColor(C.Muted)
				ui.Text(c, "Plan").FontSize(11).FontWeight(600).TextColor(C.Text).Grow(1)
				ui.Text(c, fmt.Sprintf("%d/%d", doneCount, len(a.Store.Todos))).FontSize(11).FontFeatures("tnum").TextColor(C.Ghost)
			})
			if tog.Clicked() {
				a.AgentUI.PlanOpen = !a.AgentUI.PlanOpen
			}

			if a.AgentUI.PlanOpen {
				ui.Scroll(c.Key("plan-todos")).
					FillWidth().
					MaxHeight(240).
					TrackScroll(&a.AgentUI.PlanScroll).
					Padding(2, 10, 8, 10).
					Gap(4).
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

func (a *NalarupaApp) viewAgentComposer(c *ui.Context) {
	ui.Column(c.Key("agent-composer")).
		FillWidth().
		Shrink(0).
		Padding(8, 10, 10, 10).
		Gap(8).
		Background(C.Panel).
		BorderWidth(1, 0, 0, 0).
		BorderColor(C.Border).
		Children(func() {
			ph := "Design anything…"
			if a.State.AgentRunning {
				ph = "Queue a follow-up…"
			}
			ta := ui.TextArea(c.Key("agent-input"), &a.AgentUI.Draft).
				FillWidth().
				Padding(6, 8).
				Radius(8).
				Background(C.Panel2).
				Border(1, C.Field).
				TextColor(C.Text).
				CaretColor(C.Accent).
				FontSize(12).
				Lines(2, 8).
				Placeholder(ph).
				PlaceholderColor(C.Ghost).
				Label("Message the design agent").
				Bind(&a.AgentUI.InputHandle)

			ta.OnTextKey(func(k ui.TextKey) bool {
				if k.Key == ui.KeyTab {
					// Never insert literal tabs into composer; allow Tab focus traversal
					return false
				}
				if k.Key == ui.KeyEnter && (k.Mods&ui.Shift == 0) {
					txt := k.Text()
					k.SetText("")
					a.submitAgentPrompt(txt, true)
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
					nextMode, modeChanged := SegmentedTabs(c, "composer-parallel-mode", "Parallel mode", a.AgentUI.ParallelMode, []SegOption[string]{
						{Key: "mode-split", Value: "split", Label: "Split"},
						{Key: "mode-side", Value: "side", Label: "Side by side"},
					})
					if modeChanged {
						a.AgentUI.ParallelMode = nextMode
					}
				}
			})

			// Bottom controls row: ModelPicker + Fast + CookMenu + Stop/Send
			var modelBtn, cookBtn ui.Element
			ui.Row(c.Key("composer-bottom")).FillWidth().Gap(4).AlignItems(ui.Center).Children(func() {
				modLabel := a.effectiveModelLabel()
				modelBtn = ui.ButtonBase(c.Key("agent-model")).
					Height(26).
					Padding(0, 8).
					Radius(6).
					Row().
					Gap(4).
					AlignItems(ui.Center).
					Shrink(1).
					Label("Model: " + modLabel).
					Bind(&a.AgentUI.ModelBtnHandle).
					Cursor(ui.CursorPointer)
				if modelBtn.Hovered() || a.AgentUI.ModelOpen {
					modelBtn.Background(C.Raised)
				} else {
					modelBtn.Background(C.Panel2)
				}
				modelBtn.Border(1, C.Border).Children(func() {
					ui.Text(c, modLabel).FontSize(11).TextColor(C.Text).MaxLines(1)
					ui.Icon(c, IconSVG("chevronDown")).FontSize(11).TextColor(C.Ghost)
				})
				if modelBtn.Clicked() {
					a.AgentUI.ModelOpen = !a.AgentUI.ModelOpen
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
					Padding(0, 8).
					Radius(6).
					Row().
					Gap(4).
					AlignItems(ui.Center).
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
					stopBtn := DangerIconBtn(c, "agent-stop", "stop", "Stop", 28)
					if stopBtn.Clicked() {
						a.Bridge.TryCall("agent_stop", nil, nil)
					}
				}

				sendDisabled := strings.TrimSpace(a.AgentUI.Draft) == ""
				sendBtn := AccentIconBtn(c, "agent-send", "send", "Send", 28, sendDisabled)
				if sendBtn.Clicked() && !sendDisabled {
					a.submitAgentPrompt(a.AgentUI.Draft, true)
				}
			})

			// ModelPicker popover
			if modelBtn.Valid() {
				mp := ui.Popover(c, modelBtn, &a.AgentUI.ModelOpen, func() {
					ui.Column(c.Key("model-menu")).Width(190).Padding(6).Gap(2).Background(C.Panel2).Radius(8).Children(func() {
						if a.AgentUI.Backend == "provider" {
							ap := a.activeProvider()
							if ap == nil || len(ap.Models) == 0 {
								ui.Text(c, "Configure models in Agent settings.").FontSize(11).TextColor(C.Ghost).Padding(6)
								return
							}
							for _, m := range ap.Models {
								mod := m
								sel := a.effectiveModelID() == mod
								b := ui.ButtonBase(c.Key("model-opt-" + mod)).
									FillWidth().Height(26).Padding(0, 8).Radius(5).Row().AlignItems(ui.Center).
									Selected(sel).Label(mod).Cursor(ui.CursorPointer)
								if sel || b.Hovered() {
									b.Background(C.Raised)
								}
								b.Children(func() {
									ui.Text(c, mod).FontSize(12).TextColor(C.Text).MaxLines(1)
								})
								if b.Clicked() {
									a.setSetting("provider_model", mod)
									a.AgentUI.ModelOpen = false
								}
							}
							return
						}

						if a.AgentUI.Backend == "claude-code" {
							sel := a.AgentUI.AgentModel == ""
							b := ui.ButtonBase(c.Key("model-opt-default")).
								FillWidth().Height(26).Padding(0, 8).Radius(5).Row().AlignItems(ui.Center).
								Selected(sel).Label("Default").Cursor(ui.CursorPointer)
							if sel || b.Hovered() {
								b.Background(C.Raised)
							}
							b.Children(func() {
								ui.Text(c, "Default").FontSize(12).TextColor(C.Text)
							})
							if b.Clicked() {
								a.setSetting("agent_model", nil)
								a.AgentUI.ModelOpen = false
							}
						}
						for _, cm := range builtinClaudeModels {
							opt := cm
							sel := a.effectiveModelID() == opt.ID
							b := ui.ButtonBase(c.Key("model-opt-" + opt.ID)).
								FillWidth().Height(26).Padding(0, 8).Radius(5).Row().AlignItems(ui.Center).
								Selected(sel).Label(opt.Label).Cursor(ui.CursorPointer)
							if sel || b.Hovered() {
								b.Background(C.Raised)
							}
							b.Children(func() {
								ui.Text(c, opt.Label).FontSize(12).TextColor(C.Text)
							})
							if b.Clicked() {
								a.setSetting("agent_model", opt.ID)
								a.AgentUI.ModelOpen = false
							}
						}
					})
				})
				if mp.Dismissed() {
					a.AgentUI.ModelBtnHandle.Focus()
				}
			}

			// CookMenu ("Let it cook") popover
			if cookBtn.Valid() {
				cp := ui.Popover(c, cookBtn, &a.AgentUI.CookOpen, func() {
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
