package main

import (
	"fmt"
	"math"

	"github.com/egoist/mygo/ui"
)

type toolButtonDef struct {
	ID    string
	Icon  string
	Label string
}

var editorTools = []toolButtonDef{
	{ID: "move", Icon: "cursor", Label: "Move (V)"},
	{ID: "frame", Icon: "frame", Label: "Frame (F / A)"},
	{ID: "rectangle", Icon: "square", Label: "Rectangle (R)"},
	{ID: "ellipse", Icon: "circle", Label: "Ellipse (O)"},
	{ID: "text", Icon: "type", Label: "Text (T)"},
	{ID: "pen", Icon: "pen", Label: "Pen (P)"},
	{ID: "hand", Icon: "hand", Label: "Hand (H)"},
}

func (a *NalarupaApp) viewToolbar(c *ui.Context) {
	bar := ui.Row(c.Key("toolbar")).
		FillWidth().
		Height(TopH).
		Padding(0, 10, 0, 8).
		Gap(4).
		AlignItems(ui.Center).
		Background(C.Panel).
		BorderWidth(0, 0, 1, 0).
		BorderColor(C.Border).
		Role(ui.RoleToolbar).
		Label("Editor toolbar")

	bar.HandleInput(func(ev ui.InputEvent) bool {
		if ev.Kind == ui.InputKeyDown {
			return a.HandleEditorShortcut(ev.Mods, ev.Key)
		}
		return false
	})

	bar.Children(func() {
		homeBtn := IconBtn(c, "home", "home", "Home", 28, false)
		if homeBtn.Clicked() {
			a.RefreshHomeRecents()
			a.Screen = "home"
		}

		// Document title + dirty dot
		ui.Row(c.Key("title-block")).
			Width(LeftW - 48).
			Padding(0, 4).
			Gap(6).
			AlignItems(ui.Center).
			Children(func() {
				docTitle := a.State.Title
				if docTitle == "" {
					docTitle = "Untitled"
				}
				ui.Text(c, docTitle).
					FontSize(13).
					FontWeight(600).
					TextColor(C.Text).
					MaxLines(1).
					Shrink(1)
				if a.State.Dirty {
					ui.Box(c.Key("unsaved-dot")).
						Size(6, 6).
						Shrink(0).
						Radius(3).
						Background(C.Accent).
						Label("Unsaved changes")
				}
			})

		// 7 Canvas tools
		for _, td := range editorTools {
			btn := IconBtn(c, "tool-"+td.ID, td.Icon, td.Label, 28, a.Tool == td.ID)
			if btn.Clicked() {
				// Return focus to canvas when clicked with pointer; keep focus on button if activated via keyboard
				ptrClick := !btn.FocusVisible()
				a.SetTool(td.ID, ptrClick)
			}
		}

		toolbarDivider(c, "div-history")

		undoBtn := IconBtn(c, "undo", "undo", "Undo (Ctrl+Z)", 28, false)
		if undoBtn.Clicked() {
			a.Undo()
		}
		redoBtn := IconBtn(c, "redo", "redo", "Redo (Ctrl+Shift+Z)", 28, false)
		if redoBtn.Clicked() {
			a.Redo()
		}

		ui.Spacer(c)

		// Zoom controls
		zoomOut := IconBtn(c, "zoom-out", "minus", "Zoom out (-)", 28, false)
		if zoomOut.Clicked() {
			a.ZoomBy(0.8)
		}

		pct := fmt.Sprintf("%d%%", int(math.Round(a.Cam.Zoom*100)))
		zoomLvl := ui.ButtonBase(c.Key("zoom-level")).
			Size(52, 28).
			Radius(6).
			Center().
			Label("Zoom to fit (Shift+1): " + pct).
			Tooltip("Zoom to fit (Shift+1)").
			Cursor(ui.CursorPointer)
		if zoomLvl.Hovered() {
			zoomLvl.Background(C.Raised)
		}
		zoomLvl.Children(func() {
			ui.Text(c, pct).FontSize(11).FontFeatures("tnum").TextColor(C.Muted).MaxLines(1)
		})
		if zoomLvl.Clicked() {
			a.FitTo(nil)
		}

		zoomIn := IconBtn(c, "zoom-in", "plus", "Zoom in (+)", 28, false)
		if zoomIn.Clicked() {
			a.ZoomBy(1.25)
		}

		fitBtn := IconBtn(c, "fit", "maximize", "Zoom to fit (Shift+1)", 28, false)
		if fitBtn.Clicked() {
			a.FitTo(nil)
		}

		toolbarDivider(c, "div-actions")

		presentBtn := IconBtn(c, "present", "play", "Present (Ctrl+Enter)", 28, false)
		if presentBtn.Clicked() {
			a.EnterPresent()
		}
		saveBtn := IconBtn(c, "save", "save", "Save (Ctrl+S)", 28, false)
		if saveBtn.Clicked() {
			a.SaveDocument("")
		}
		saveAsBtn := IconBtn(c, "save-as", "saveAs", "Save as… (Ctrl+Shift+S)", 28, false)
		if saveAsBtn.Clicked() {
			a.TriggerSaveAsDialog()
		}
	})
}

func toolbarDivider(c *ui.Context, key string) {
	ui.Box(c.Key(key)).
		Size(1, 20).
		Margin(0, 6).
		Background(C.Border)
}
