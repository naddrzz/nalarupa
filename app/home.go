package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/egoist/mygo/ui"
)

type starterTemplate struct {
	ID       string
	Key      string
	Icon     string
	Title    string
	Subtitle string
	A11yName string
}

var homeTemplates = []starterTemplate{
	{
		ID:       "blank",
		Key:      "template-blank",
		Icon:     "frame",
		Title:    "Design",
		Subtitle: "A 1440 desktop frame",
		A11yName: "New design: A 1440 desktop frame",
	},
	{
		ID:       "slides",
		Key:      "template-slides",
		Icon:     "slides",
		Title:    "Slide deck",
		Subtitle: "16:9 slides with tokens",
		A11yName: "New slide deck: 16:9 slides with tokens",
	},
	{
		ID:       "mobile",
		Key:      "template-mobile",
		Icon:     "square",
		Title:    "Mobile app",
		Subtitle: "A 393×852 phone frame",
		A11yName: "New mobile app: A 393×852 phone frame",
	},
	{
		ID:       "empty",
		Key:      "template-empty",
		Icon:     "plus",
		Title:    "Empty canvas",
		Subtitle: "Start from nothing",
		A11yName: "New empty canvas: Start from nothing",
	},
}

// RefreshHomeRecents loads recent document paths from core settings and filters to existing files on disk.
func (a *NalarupaApp) RefreshHomeRecents() {
	out := make([]string, 0, 8)
	var settings map[string]any
	if !a.Bridge.TryCall("settings_get", nil, &settings) {
		a.HomeRecents = out
		return
	}
	raw, ok := settings["recent"].([]any)
	if !ok {
		a.HomeRecents = out
		return
	}
	for _, item := range raw {
		p, ok := item.(string)
		if !ok || p == "" {
			continue
		}
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			out = append(out, p)
			if len(out) >= 8 {
				break
			}
		}
	}
	a.HomeRecents = out
}

func (a *NalarupaApp) viewHome(c *ui.Context) {
	reduced := EffectiveReducedMotion(c)
	scroll := ui.Scroll(c.Key("home-scroll")).
		Fill().
		Background(C.BG).
		AlignItems(ui.Center)

	scroll.HandleInput(func(ev ui.InputEvent) bool {
		if ev.Kind == ui.InputKeyDown && ev.Key == ui.KeyEscape && a.UnsavedPending != nil {
			a.UnsavedPending = nil
			return true
		}
		return false
	})

	scroll.Children(func() {
		ui.Column(c.Key("home-inner")).
			Width(760).
			MaxWidthPercent(94).
			Padding(72, 20, 40, 20).
			Gap(28).
			Children(func() {
				// Brand header
				ui.Column(c.Key("home-brand")).Gap(10).Children(func() {
					ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
						if logo := BrandLogoSVG(); logo != nil {
							ui.Image(c, logo).Size(40, 40)
						}
						ui.Text(c, "Nalarupa").FontSize(30).FontWeight(700).TextColor(C.Text)
					})
					ui.Text(c, "Design on an infinite canvas with an AI agent. Go core, GPU-native UI.").
						FontSize(14).
						TextColor(C.Muted)
				})

				// Unsaved changes confirmation dialog banner
				if a.UnsavedPending != nil {
					title := a.State.Title
					if title == "" {
						title = "Untitled"
					}
					ui.Row(c.Key("unsaved-prompt")).
						FillWidth().
						Padding(14, 16).
						Gap(12).
						AlignItems(ui.Center).
						Radius(10).
						Background(C.Panel2).
						Border(1, C.Accent).
						Role(ui.RoleDialog).
						Label("Unsaved changes").
						Transition(DialogBannerTransition(reduced)).
						Children(func() {
							ui.Text(c, fmt.Sprintf("Save changes to %s first? Without saving they are lost.", title)).
								FontSize(12).
								TextColor(C.Text).
								Grow(1).
								Wrap()
							if ActionBtn(c, "unsaved-cancel", "Cancel", "", "").Clicked() {
								a.UnsavedPending = nil
							}
							if ActionBtn(c, "unsaved-discard", "Don't save", "", "danger").Clicked() {
								fn := a.UnsavedPending
								a.UnsavedPending = nil
								if fn != nil {
									fn()
								}
							}
							if ActionBtn(c, "unsaved-save", "Save", "save", "primary").Clicked() {
								if a.SaveDocument("") {
									fn := a.UnsavedPending
									a.UnsavedPending = nil
									if fn != nil {
										fn()
									}
								}
							}
						})
				}

				// 4 Starter templates
				ui.Column(c.Key("home-templates-sec")).FillWidth().Gap(10).Children(func() {
					ui.Text(c, "Start a new document").FontSize(12).FontWeight(600).TextColor(C.Muted)
					ui.Row(c.Key("home-templates")).FillWidth().Gap(12).Children(func() {
						for _, tmpl := range homeTemplates {
							t := tmpl
							card := ui.ButtonBase(c.Key(t.Key)).
								Grow(1).
								Basis(0).
								Height(130).
								Padding(16).
								Radius(12).
								Column().
								Justify(ui.End).
								AlignItems(ui.Start).
								Gap(6).
								Label(t.A11yName).
								Cursor(ui.CursorPointer).
								Transition(ColorHoverTransition(reduced))
							hovered := card.Hovered()
							pressed := card.Pressed()
							if hovered {
								card.Background(C.Panel2).Border(1, C.Accent)
							} else {
								card.Background(C.Panel).Border(1, C.Border)
							}
							ApplyPressScale(card, pressed, false, reduced)
							card.Children(func() {
								ic := ui.Icon(c.Key(t.Key+"-ic"), IconSVG(t.Icon)).FontSize(20).TextColor(C.Accent)
								if !reduced {
									liftTarget := float32(0)
									if hovered {
										liftTarget = -2
									}
									ic.Translate(0, ic.AnimateWith("lift", liftTarget, DurHover, EaseOutQuint))
								}
								ui.Spacer(c)
								ui.Text(c, t.Title).FontSize(14).FontWeight(600).TextColor(C.Text)
								ui.Text(c, t.Subtitle).FontSize(11).TextColor(C.Muted).MaxLines(2)
							})
							if card.Clicked() {
								a.StartTemplate(t.ID, false)
							}
						}
					})
				})

				// Open / Back action row
				ui.Row(c.Key("home-actions")).Gap(10).AlignItems(ui.Center).Children(func() {
					openBtn := ActionBtn(c, "open-file", "Open .nala / .pen…", "open", "")
					if openBtn.Clicked() {
						a.TriggerOpenDialog()
					}
					if a.Opened {
						docTitle := a.State.Title
						if docTitle == "" {
							docTitle = "Untitled"
						}
						backBtn := ActionBtn(c, "home-back", "Back to "+docTitle, "frame", "")
						if backBtn.Clicked() {
							a.UnsavedPending = nil
							a.Screen = "editor"
						}
					}
				})

				// Recent files (cached; refreshed on Init/Open/Save/Home transition)
				if a.HomeRecents == nil {
					a.RefreshHomeRecents()
				}
				recents := a.HomeRecents
				ui.Column(c.Key("home-recents")).FillWidth().Gap(8).Children(func() {
					ui.Text(c, "Recent files").FontSize(12).FontWeight(600).TextColor(C.Muted)
					if len(recents) == 0 {
						ui.Text(c, "No recent files yet.").FontSize(12).TextColor(C.Ghost)
						return
					}
					for _, p := range recents {
						pathCopy := p
						base := filepath.Base(pathCopy)
						rowKey := "recent-" + pathCopy
						row := ui.ButtonBase(c.Key(rowKey)).
							FillWidth().
							MinHeight(34).
							Padding(6, 10).
							Radius(8).
							Row().
							Gap(10).
							AlignItems(ui.Center).
							Label("Open " + base).
							Tooltip(pathCopy).
							Cursor(ui.CursorPointer).
							Transition(ColorHoverTransition(reduced))
						if row.Hovered() {
							row.Background(C.Panel2).Border(1, C.Border)
						} else {
							row.Background(C.Panel).Border(1, C.Border)
						}
						focused := row.FocusVisible()
						row.Children(func() {
							ui.Icon(c, IconSVG("frame")).FontSize(14).TextColor(C.Ghost)
							ui.Text(c, base).
								FontSize(12).
								FontWeight(500).
								TextColor(C.Text).
								MaxWidth(440).
								MaxLines(1)
							pathTxt := ui.Text(c, pathCopy).
								FontSize(11).
								TextColor(C.Ghost).
								Grow(1).
								Basis(40)
							if focused {
								pathTxt.Wrap()
							} else {
								pathTxt.MaxLines(1).TruncateStart()
							}
						})
						if row.Clicked() {
							a.OpenDocument(pathCopy, false)
						}
					}
				})
			})
	})
}
