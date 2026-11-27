package main

import (
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/egoist/mygo/ui"
)

// SkillItem represents a locally installed agent skill fetched from core-go.
type SkillItem struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Path        string `json:"path"`
	Dir         string `json:"dir"`
	Source      string `json:"source"`
}

type effortOpt struct {
	ID    string
	Label string
	Short string
}

var reasoningEffortOpts = []effortOpt{
	{ID: "low", Label: "Low", Short: "Low"},
	{ID: "medium", Label: "Medium", Short: "Med"},
	{ID: "high", Label: "High", Short: "High"},
	{ID: "xhigh", Label: "Extra high", Short: "XHigh"},
	{ID: "max", Label: "Max", Short: "Max"},
}

func (a *NalarupaApp) effectiveEffort() string {
	switch strings.ToLower(strings.TrimSpace(a.AgentUI.Effort)) {
	case "low", "medium", "high", "xhigh", "max":
		return strings.ToLower(strings.TrimSpace(a.AgentUI.Effort))
	default:
		return "high"
	}
}

func effortLabel(id string) string {
	for _, o := range reasoningEffortOpts {
		if o.ID == id {
			return o.Label
		}
	}
	return "High"
}

func effortShortLabel(id string) string {
	for _, o := range reasoningEffortOpts {
		if o.ID == id {
			return o.Short
		}
	}
	return "High"
}

func (a *NalarupaApp) requestFetchModels(target, provID, format, baseURL, apiKey string, persist bool, editSeq uint64) {
	a.AgentUI.ModelFetchSeq++
	reqID := fmt.Sprintf("mf_%d", a.AgentUI.ModelFetchSeq)
	a.AgentUI.FetchingModels = true
	a.AgentUI.ActiveFetchReqID = reqID
	a.AgentUI.ActiveFetchEditSeq = editSeq
	a.AgentUI.ActiveFetchProvID = provID

	args := map[string]any{
		"target":  target,
		"req_id":  reqID,
		"persist": persist,
	}
	if provID != "" {
		args["id"] = provID
	}
	if format != "" {
		args["format"] = format
	}
	if baseURL != "" {
		args["base_url"] = baseURL
	}
	if apiKey != "" {
		args["api_key"] = apiKey
	}
	if err := a.Bridge.Call("models_fetch", args, nil); err != nil {
		if a.AgentUI.ActiveFetchReqID == reqID {
			a.AgentUI.FetchingModels = false
			a.AgentUI.ActiveFetchReqID = ""
		}
		a.ShowToast(err.Error(), true)
	}
}

func (a *NalarupaApp) onModelsFetched(ev map[string]any) {
	reqID, _ := ev["req_id"].(string)
	if reqID != "" && a.AgentUI.ActiveFetchReqID != "" && reqID != a.AgentUI.ActiveFetchReqID {
		return
	}
	editSeq := a.AgentUI.ActiveFetchEditSeq
	expectedProvID := a.AgentUI.ActiveFetchProvID
	a.AgentUI.FetchingModels = false
	a.AgentUI.ActiveFetchReqID = ""

	okVal, _ := ev["ok"].(bool)
	if !okVal {
		msg, _ := ev["error"].(string)
		if msg == "" {
			msg = "Failed to fetch model list."
		}
		a.ShowToast(msg, true)
		return
	}
	rawModels, _ := ev["models"].([]any)
	var models []string
	for _, raw := range rawModels {
		if s, ok := raw.(string); ok && strings.TrimSpace(s) != "" {
			models = append(models, strings.TrimSpace(s))
		}
	}
	target, _ := ev["target"].(string)
	if target == "api" {
		a.AgentUI.APIModels = models
		a.ShowToast(fmt.Sprintf("Loaded %d Anthropic models", len(models)), false)
		return
	}
	if len(models) > 0 && editSeq > 0 && a.AgentUI.EditingProv != nil &&
		a.AgentUI.ProvEditSeq == editSeq && a.AgentUI.EditingProv.ID == expectedProvID {
		var existing []string
		seen := map[string]bool{}
		for _, part := range strings.Split(a.AgentUI.ProvModelsCSV, ",") {
			if m := strings.TrimSpace(part); m != "" && !seen[m] {
				seen[m] = true
				existing = append(existing, m)
			}
		}
		for _, m := range models {
			if !seen[m] {
				seen[m] = true
				existing = append(existing, m)
			}
		}
		a.AgentUI.ProvModelsCSV = strings.Join(existing, ", ")
	}
	a.reloadAgentSettings()
	a.ShowToast(fmt.Sprintf("Loaded %d models", len(models)), false)
}

func (a *NalarupaApp) onTypeSafeModelsFetched(ev map[string]any) {
	a.AgentUI.TypeSafeFetchingModels = false
	okVal, _ := ev["ok"].(bool)
	if !okVal {
		msg, _ := ev["error"].(string)
		if msg == "" {
			msg = "Failed to fetch TypeSafe models."
		}
		a.ShowToast(msg, true)
		return
	}
	rawModels, _ := ev["models"].([]any)
	var models []string
	for _, raw := range rawModels {
		if s, ok := raw.(string); ok && strings.TrimSpace(s) != "" {
			models = append(models, strings.TrimSpace(s))
		}
	}
	a.AgentUI.TypeSafeModels = models
	a.reloadAgentSettings()
	a.ShowToast(fmt.Sprintf("Loaded %d TypeSafe models", len(models)), false)
}

func (a *NalarupaApp) ensureSkillsLoaded(force bool) {
	if !force && (a.AgentUI.SkillsLoaded || a.AgentUI.SkillsLoading) {
		return
	}
	if a.Bridge == nil || a.Bridge.Engine() == nil {
		return
	}
	a.AgentUI.SkillsReqSeq++
	reqSeq := a.AgentUI.SkillsReqSeq
	// Run synchronously when running headless without a window (ui.Tester), async in live desktop UI.
	if a.Window == nil {
		var list []SkillItem
		if a.Bridge.TryCall("skills_list", map[string]any{"refresh": force}, &list) {
			a.AgentUI.Skills = list
			a.AgentUI.SkillsLoaded = true
			a.AgentUI.SkillsLoading = false
		}
		return
	}
	a.AgentUI.SkillsLoading = true
	go func() {
		var list []SkillItem
		ok := a.Bridge.TryCall("skills_list", map[string]any{"refresh": force}, &list)
		if a.Window != nil && !a.Window.IsDestroyed() {
			a.Window.Update(func() {
				if a.AgentUI.SkillsReqSeq != reqSeq {
					return
				}
				a.AgentUI.SkillsLoading = false
				if ok {
					a.AgentUI.Skills = list
					a.AgentUI.SkillsLoaded = true
				}
			})
		}
	}()
}

func isAllowedBeforeDraftSlash(r rune) bool {
	return unicode.IsSpace(r) || r == '(' || r == '[' || r == '{' || r == ',' || r == ';' || r == '"' || r == '\''
}

func isDraftSkillChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '-' || b == '_' || b == '.'
}

func activeDraftSkills(draft string, catalog []SkillItem) []SkillItem {
	if len(catalog) == 0 || !strings.Contains(draft, "/") {
		return nil
	}
	byName := make(map[string]SkillItem, len(catalog))
	for _, s := range catalog {
		byName[strings.ToLower(strings.TrimSpace(s.Name))] = s
	}
	seen := map[string]bool{}
	var out []SkillItem
	for i := 0; i < len(draft); i++ {
		if draft[i] != '/' {
			continue
		}
		if i > 0 {
			prevRune := rune(draft[i-1])
			if draft[i-1] >= 0x80 {
				for _, r := range draft[:i] {
					prevRune = r
				}
			}
			if !isAllowedBeforeDraftSlash(prevRune) {
				continue
			}
		}
		end := i + 1
		for end < len(draft) && isDraftSkillChar(draft[end]) {
			end++
		}
		if end == i+1 {
			continue
		}
		tok := strings.ToLower(draft[i+1 : end])
		if sk, ok := byName[tok]; ok && !seen[tok] {
			seen[tok] = true
			out = append(out, sk)
		}
		i = end - 1
	}
	return out
}

func removeDraftSkill(draft, skillName string) string {
	target := strings.ToLower(strings.TrimSpace(skillName))
	if target == "" || !strings.Contains(draft, "/") {
		return draft
	}
	var b strings.Builder
	cur := 0
	for i := 0; i < len(draft); i++ {
		if draft[i] != '/' {
			continue
		}
		if i > 0 {
			prevRune := rune(draft[i-1])
			if draft[i-1] >= 0x80 {
				for _, r := range draft[:i] {
					prevRune = r
				}
			}
			if !isAllowedBeforeDraftSlash(prevRune) {
				continue
			}
		}
		end := i + 1
		for end < len(draft) && isDraftSkillChar(draft[end]) {
			end++
		}
		if end < len(draft) && (draft[end] == '/' || draft[end] == '\\') {
			i = end
			continue
		}
		if strings.ToLower(draft[i+1:end]) == target {
			seg := draft[cur:i]
			nextCur := end
			if nextCur < len(draft) && draft[nextCur] == ' ' {
				nextCur++
			} else if len(seg) > 0 && seg[len(seg)-1] == ' ' && (nextCur == len(draft) || draft[nextCur] == '\n' || draft[nextCur] == '\r') {
				seg = seg[:len(seg)-1]
			}
			b.WriteString(seg)
			cur = nextCur
			i = end - 1
		}
	}
	if cur == 0 {
		return draft
	}
	if cur < len(draft) {
		b.WriteString(draft[cur:])
	}
	res := b.String()
	if strings.TrimSpace(res) == "" {
		return ""
	}
	return res
}

func toggleDraftSkill(draft, skillName string) string {
	for _, s := range activeDraftSkills(draft, []SkillItem{{Name: skillName}}) {
		if strings.EqualFold(s.Name, skillName) {
			return removeDraftSkill(draft, skillName)
		}
	}
	token := "/" + skillName
	trimmed := strings.TrimSpace(draft)
	if trimmed == "" {
		return token + " "
	}
	if strings.HasSuffix(draft, " ") || strings.HasSuffix(draft, "\n") || strings.HasSuffix(draft, "\t") {
		return draft + token + " "
	}
	return draft + " " + token + " "
}

func matchingSlashSkills(draft, dismissed string, catalog []SkillItem) (matches []SkillItem, token, query string, startIdx int) {
	if len(catalog) == 0 || draft == "" {
		return nil, "", "", 0
	}
	last := draft[len(draft)-1]
	if last == ' ' || last == '\n' || last == '\t' || last == '\r' {
		return nil, "", "", 0
	}
	startIdx = len(draft)
	for startIdx > 0 {
		prevRune := rune(draft[startIdx-1])
		runeLen := 1
		if draft[startIdx-1] >= 0x80 {
			for idx, r := range draft[:startIdx] {
				prevRune = r
				runeLen = startIdx - idx
			}
		}
		if isAllowedBeforeDraftSlash(prevRune) {
			break
		}
		startIdx -= runeLen
	}
	token = draft[startIdx:]
	if !strings.HasPrefix(token, "/") {
		return nil, "", "", 0
	}
	for i := 1; i < len(token); i++ {
		if !isDraftSkillChar(token[i]) {
			return nil, "", "", 0
		}
	}
	if dismissed != "" && token == dismissed {
		return nil, token, "", startIdx
	}
	query = strings.ToLower(token[1:])
	if strings.Contains(query, "/") {
		return nil, "", "", 0
	}

	var prefixMatches, subMatches []SkillItem
	for _, s := range catalog {
		lowName := strings.ToLower(s.Name)
		if query == "" || strings.HasPrefix(lowName, query) {
			prefixMatches = append(prefixMatches, s)
		} else if strings.Contains(lowName, query) || strings.Contains(strings.ToLower(s.Description), query) {
			subMatches = append(subMatches, s)
		}
	}
	combined := append(prefixMatches, subMatches...)
	if len(combined) > 8 {
		combined = combined[:8]
	}
	return combined, token, query, startIdx
}

func completeSlashSkill(draft string, startIdx int, skillName string) string {
	if startIdx < 0 || startIdx > len(draft) {
		startIdx = len(draft)
	}
	return draft[:startIdx] + "/" + skillName + " "
}

func (a *NalarupaApp) viewActiveSkillChips(c *ui.Context, active []SkillItem) {
	if len(active) == 0 {
		return
	}
	reduced := EffectiveReducedMotion(c)
	ui.Row(c.Key("composer-active-skills")).FillWidth().Wrap().Gap(4).Transition(ChipTransition(reduced)).Children(func() {
		for _, sk := range active {
			item := sk
			chip := ui.ButtonBase(c.Key("rm-skill-" + item.Name)).
				Height(22).
				Padding(0, 8).
				Radius(11).
				Row().
				Gap(4).
				AlignItems(ui.Center).
				Label("Remove skill " + item.Name).
				Cursor(ui.CursorPointer).
				Transition(ChipTransition(reduced))
			pressed := chip.Pressed()
			if chip.Hovered() {
				chip.Background(C.Raised).Border(1, C.Accent)
			} else {
				chip.Background(C.AccentSoft).Border(1, C.Accent)
			}
			ApplyPressScale(chip, pressed, false, reduced)
			chip.Children(func() {
				ui.Text(c, "/"+item.Name).FontSize(11).FontWeight(500).TextColor(C.Accent).MaxLines(1)
				ui.Icon(c, IconSVG("x")).FontSize(10).TextColor(C.Accent)
			})
			if chip.Clicked() {
				a.AgentUI.Draft = removeDraftSkill(a.AgentUI.Draft, item.Name)
				a.AgentUI.InputHandle.Focus()
			}
		}
	})
}

func (a *NalarupaApp) viewSlashSkillAutocomplete(c *ui.Context, matches []SkillItem, startIdx int) {
	if len(matches) == 0 {
		return
	}
	reduced := EffectiveReducedMotion(c)
	if a.AgentUI.SlashIndex < 0 || a.AgentUI.SlashIndex >= len(matches) {
		a.AgentUI.SlashIndex = 0
	}
	// Note: keyboard selection highlight inside the menu moves at 0ms per Emil's keyboard-navigation rule;
	// only the menu container's open/close transitions!
	ui.Column(c.Key("slash-skill-menu")).
		FillWidth().
		Padding(4).
		Gap(2).
		Radius(8).
		Background(C.Panel2).
		Border(1, C.Border).
		TransformOrigin(0, 1).
		Transition(PopoverTransition(reduced)).
		Children(func() {
			ui.Text(c, "Skills · Tab/Enter to insert · Multiple allowed").FontSize(10).FontWeight(600).TextColor(C.Ghost).Padding(2, 6)
			for i, sk := range matches {
				item := sk
				sel := i == a.AgentUI.SlashIndex
				row := ui.ButtonBase(c.Key("slash-opt-" + item.Name)).
					FillWidth().
					Padding(5, 8).
					Radius(6).
					Column().
					Gap(2).
					Selected(sel).
					Label("Use skill " + item.Name).
					Cursor(ui.CursorPointer)
				if sel || row.Hovered() {
					row.Background(C.Raised)
				}
				row.Children(func() {
					ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
						ui.Text(c, "/"+item.Name).FontSize(12).FontWeight(600).TextColor(C.Accent).MaxLines(1)
						ui.Spacer(c)
						ui.Text(c, item.Source).FontSize(10).TextColor(C.Ghost).MaxLines(1)
					})
					if item.Description != "" {
						ui.Text(c, item.Description).FontSize(11).TextColor(C.Muted).MaxLines(1)
					}
				})
				if row.Clicked() {
					a.AgentUI.Draft = completeSlashSkill(a.AgentUI.Draft, startIdx, item.Name)
					a.AgentUI.DismissedSlash = ""
					a.AgentUI.SlashIndex = 0
					a.AgentUI.InputHandle.Focus()
				}
			}
		})
}

func (a *NalarupaApp) viewEffortPopover(c *ui.Context, effortBtn ui.Element) {
	if !effortBtn.Valid() {
		return
	}
	ep := AnimatedPopover(c, effortBtn, &a.AgentUI.EffortOpen, 0.2, 1, func() {
		ui.Column(c.Key("effort-menu")).Width(170).Padding(6).Gap(2).Background(C.Panel2).Radius(8).Children(func() {
			ui.Text(c, "Reasoning effort").FontSize(11).FontWeight(600).TextColor(C.Muted).Padding(2, 6)
			cur := a.effectiveEffort()
			for _, o := range reasoningEffortOpts {
				opt := o
				sel := cur == opt.ID
				b := ui.ButtonBase(c.Key("effort-opt-" + opt.ID)).
					FillWidth().Height(26).Padding(0, 8).Radius(5).Row().AlignItems(ui.Center).
					Selected(sel).Label(opt.Label).Cursor(ui.CursorPointer)
				if sel || b.Hovered() {
					b.Background(C.Raised)
				}
				b.Children(func() {
					ui.Text(c, opt.Label).FontSize(12).TextColor(C.Text).Grow(1)
					ui.Text(c, opt.Short).FontSize(10).TextColor(C.Ghost)
				})
				if b.Clicked() {
					a.setSetting("agent_effort", opt.ID)
					a.AgentUI.EffortOpen = false
				}
			}
		})
	})
	if ep.Dismissed() {
		a.AgentUI.EffortBtnHandle.Focus()
	}
}

func (a *NalarupaApp) viewSkillsPopover(c *ui.Context, skillsBtn ui.Element) {
	if !skillsBtn.Valid() {
		return
	}
	sp := AnimatedPopover(c, skillsBtn, &a.AgentUI.SkillsOpen, 0.3, 1, func() {
		a.ensureSkillsLoaded(false)
		active := activeDraftSkills(a.AgentUI.Draft, a.AgentUI.Skills)
		activeSet := make(map[string]bool, len(active))
		for _, s := range active {
			activeSet[strings.ToLower(s.Name)] = true
		}
		q := strings.ToLower(strings.TrimSpace(a.AgentUI.SkillsFilter))

		var filtered []SkillItem
		for _, s := range a.AgentUI.Skills {
			if q == "" || strings.Contains(strings.ToLower(s.Name), q) || strings.Contains(strings.ToLower(s.Description), q) {
				filtered = append(filtered, s)
			}
		}

		ui.Column(c.Key("skills-menu")).Width(280).MaxHeight(360).Padding(8).Gap(6).Background(C.Panel2).Radius(8).Children(func() {
			ui.Row(c.Key("skills-menu-hdr")).FillWidth().Gap(4).AlignItems(ui.Center).Children(func() {
				ui.Text(c, fmt.Sprintf("Skills (%d)", len(a.AgentUI.Skills))).FontSize(11).FontWeight(600).TextColor(C.Muted).Grow(1)
				rel := ui.ButtonBase(c.Key("skills-reload")).Height(22).Padding(0, 8).Radius(5).Row().AlignItems(ui.Center).Label("Reload skills").Cursor(ui.CursorPointer)
				if rel.Hovered() {
					rel.Background(C.Raised)
				} else {
					rel.Background(C.Panel)
				}
				rel.Children(func() {
					ui.Text(c, "Reload").FontSize(11).TextColor(C.Text)
				})
				if rel.Clicked() {
					a.ensureSkillsLoaded(true)
				}
			})

			ui.TextInput(c.Key("skills-filter"), &a.AgentUI.SkillsFilter).
				FillWidth().
				Height(26).
				Padding(0, 8).
				Radius(5).
				Background(C.Panel).
				Border(1, C.Field).
				TextColor(C.Text).
				Placeholder("Filter skills…").
				PlaceholderColor(C.Ghost).
				FontSize(11).
				Label("Filter skills")

			if len(filtered) == 0 {
				msg := "No matching skills found."
				if len(a.AgentUI.Skills) == 0 {
					if a.AgentUI.SkillsLoading {
						msg = "Scanning local skill folders…"
					} else {
						msg = "No SKILL.md skills found in ~/.claude/skills, ~/.agents/skills, ~/.cursor/skills, or project."
					}
				}
				ui.Text(c, msg).FontSize(11).TextColor(C.Ghost).Wrap().Padding(6, 2)
				return
			}

			ui.Scroll(c.Key("skills-menu-scroll")).TrackScroll(&a.AgentUI.SkillsScroll).FillWidth().MaxHeight(250).Gap(2).Children(func() {
				for _, sk := range filtered {
					item := sk
					sel := activeSet[strings.ToLower(item.Name)]
					row := ui.ButtonBase(c.Key("skill-pick-" + item.Name)).
						FillWidth().
						Padding(5, 8).
						Radius(5).
						Column().
						Gap(2).
						Selected(sel).
						Label("Toggle skill " + item.Name).
						Cursor(ui.CursorPointer)
					if sel {
						row.Background(C.AccentSoft).Border(1, C.Accent)
					} else if row.Hovered() {
						row.Background(C.Raised)
					}
					row.Children(func() {
						ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
							ui.Text(c, "/"+item.Name).FontSize(12).FontWeight(500).TextColor(C.Text).Grow(1).MaxLines(1)
							ui.Text(c, item.Source).FontSize(10).TextColor(C.Ghost).MaxLines(1)
						})
						if item.Description != "" {
							ui.Text(c, item.Description).FontSize(10).TextColor(C.Muted).MaxLines(2)
						}
					})
					if row.Clicked() {
						a.AgentUI.Draft = toggleDraftSkill(a.AgentUI.Draft, item.Name)
						if slices.ContainsFunc(activeDraftSkills(a.AgentUI.Draft, a.AgentUI.Skills), func(s SkillItem) bool { return strings.EqualFold(s.Name, item.Name) }) {
							a.AgentUI.DismissedSlash = ""
						}
					}
				}
			})
		})
	})
	if sp.Dismissed() {
		a.AgentUI.SkillsBtnHandle.Focus()
	}
}
