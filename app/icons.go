package main

import (
	"embed"
	"sync"

	"github.com/egoist/mygo/ui"
)

//go:embed assets/icons/*.svg assets/brand/nalarupa.svg assets/brand/nalarupa-small.svg assets/brand/icon-256.png
var embeddedAssets embed.FS

var (
	iconsOnce    sync.Once
	parsedIcons  map[string]*ui.SVG
	brandLogo    *ui.SVG
	brandSmall   *ui.SVG
	brandPNGData []byte
	snapCrossSVG *ui.SVG
	cursorFill   *ui.SVG
	cursorStroke *ui.SVG
)

func strokeSVG(inner string) string {
	return `<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">` + inner + `</svg>`
}

var iconFiles = map[string]string{
	"chevronDown":  "assets/icons/chevron-down.svg",
	"chevronRight": "assets/icons/chevron-right.svg",
	"alert":        "assets/icons/circle-alert.svg",
	"stop":         "assets/icons/circle-stop.svg",
	"circle":       "assets/icons/circle.svg",
	"component":    "assets/icons/component.svg",
	"copy":         "assets/icons/copy.svg",
	"download":     "assets/icons/download.svg",
	"hidden":       "assets/icons/eye-off.svg",
	"filePlus":     "assets/icons/file-plus.svg",
	"open":         "assets/icons/folder-open.svg",
	"frame":        "assets/icons/frame.svg",
	"hand":         "assets/icons/hand.svg",
	"history":      "assets/icons/history.svg",
	"home":         "assets/icons/house.svg",
	"image":        "assets/icons/image.svg",
	"layers":       "assets/icons/layers.svg",
	"maximize":     "assets/icons/maximize.svg",
	"minus":        "assets/icons/minus.svg",
	"cursor":       "assets/icons/mouse-pointer-2.svg",
	"pen":          "assets/icons/pen-tool.svg",
	"play":         "assets/icons/play.svg",
	"plug":         "assets/icons/plug.svg",
	"plus":         "assets/icons/plus.svg",
	"slides":       "assets/icons/presentation.svg",
	"redo":         "assets/icons/redo-2.svg",
	"save":         "assets/icons/save.svg",
	"send":         "assets/icons/send.svg",
	"settings":     "assets/icons/settings.svg",
	"icon":         "assets/icons/smile.svg",
	"sparkles":     "assets/icons/sparkles.svg",
	"square":       "assets/icons/square.svg",
	"trash":        "assets/icons/trash-2.svg",
	"type":         "assets/icons/type.svg",
	"undo":         "assets/icons/undo-2.svg",
	"x":            "assets/icons/x.svg",
}

var inlineIcons = map[string]string{
	"alignLeft":    strokeSVG(`<line x1="4" y1="3" x2="4" y2="21"/><rect x="8" y="6" width="12" height="4" rx="1"/><rect x="8" y="14" width="7" height="4" rx="1"/>`),
	"alignHCenter": strokeSVG(`<line x1="12" y1="3" x2="12" y2="21"/><rect x="5" y="6" width="14" height="4" rx="1"/><rect x="7" y="14" width="10" height="4" rx="1"/>`),
	"alignRight":   strokeSVG(`<line x1="20" y1="3" x2="20" y2="21"/><rect x="4" y="6" width="12" height="4" rx="1"/><rect x="9" y="14" width="7" height="4" rx="1"/>`),
	"alignTop":     strokeSVG(`<line x1="3" y1="4" x2="21" y2="4"/><rect x="6" y="8" width="4" height="12" rx="1"/><rect x="14" y="8" width="4" height="7" rx="1"/>`),
	"alignVCenter": strokeSVG(`<line x1="3" y1="12" x2="21" y2="12"/><rect x="6" y="5" width="4" height="14" rx="1"/><rect x="14" y="7" width="4" height="10" rx="1"/>`),
	"alignBottom":  strokeSVG(`<line x1="3" y1="20" x2="21" y2="20"/><rect x="6" y="4" width="4" height="12" rx="1"/><rect x="14" y="9" width="4" height="7" rx="1"/>`),
	"note":         strokeSVG(`<path d="M15 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V7Z"/><path d="M14 2v4a2 2 0 0 0 2 2h4"/><line x1="8" y1="13" x2="16" y2="13"/><line x1="8" y1="17" x2="13" y2="17"/>`),
	"globe":        strokeSVG(`<circle cx="12" cy="12" r="10"/><path d="M2 12h20"/><path d="M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z"/>`),
	"paperclip":    strokeSVG(`<path d="m21.44 11.05-9.19 9.19a6 6 0 0 1-8.49-8.49l8.57-8.57A4 4 0 1 1 18 8.84l-8.59 8.57a2 2 0 0 1-2.83-2.83l8.49-8.48"/>`),
	"saveAs":       strokeSVG(`<path d="M15.2 3a2 2 0 0 1 1.4.6l3.8 3.8a2 2 0 0 1 .6 1.4V15"/><path d="M17 3v5H7V3"/><path d="M5 21a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11"/><path d="M7 21v-8h6"/><line x1="19" y1="16" x2="19" y2="22"/><line x1="16" y1="19" x2="22" y2="19"/>`),
	"eye":          strokeSVG(`<path d="M2.062 12.348a1 1 0 0 1 0-.696 10.75 10.75 0 0 1 19.876 0 1 1 0 0 1 0 .696 10.75 10.75 0 0 1-19.876 0"/><circle cx="12" cy="12" r="3"/>`),
	"line":         strokeSVG(`<line x1="5" y1="19" x2="19" y2="5"/>`),
	"polygon":      strokeSVG(`<polygon points="12 3 21 19 3 19"/>`),
}

func initIcons() {
	iconsOnce.Do(func() {
		parsedIcons = make(map[string]*ui.SVG, len(iconFiles)+len(inlineIcons))
		for name, rel := range iconFiles {
			data, err := embeddedAssets.ReadFile(rel)
			if err == nil {
				if s, err := ui.ParseSVG(data); err == nil {
					parsedIcons[name] = s
				}
			}
		}
		for name, raw := range inlineIcons {
			if s, err := ui.ParseSVG([]byte(raw)); err == nil {
				parsedIcons[name] = s
			}
		}
		if b, err := embeddedAssets.ReadFile("assets/brand/nalarupa.svg"); err == nil {
			brandLogo, _ = ui.ParseSVG(b)
		}
		if b, err := embeddedAssets.ReadFile("assets/brand/nalarupa-small.svg"); err == nil {
			brandSmall, _ = ui.ParseSVG(b)
		}
		brandPNGData, _ = embeddedAssets.ReadFile("assets/brand/icon-256.png")
		snapCrossSVG, _ = ui.ParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="8" height="8" viewBox="0 0 8 8"><path d="M1 1L7 7M7 1L1 7" stroke="#F24518" stroke-width="1.5" stroke-linecap="round"/></svg>`))
		cursorFill, _ = ui.ParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="20" height="20" viewBox="0 0 20 20"><path d="M3 2L17 9.5L10.2 11.2L7.5 17.5Z" fill="currentColor"/></svg>`))
		cursorStroke, _ = ui.ParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="20" height="20" viewBox="0 0 20 20"><path d="M3 2L17 9.5L10.2 11.2L7.5 17.5Z" fill="none" stroke="#FFFFFF" stroke-width="1.6" stroke-linejoin="round"/></svg>`))
	})
}

// IconSVG returns the parsed *ui.SVG for an icon key (never nil once assets exist).
func IconSVG(name string) *ui.SVG {
	initIcons()
	if s, ok := parsedIcons[name]; ok {
		return s
	}
	return parsedIcons["square"]
}

// BrandLogoSVG returns the full Nalarupa gunungan logo SVG (falling back to small mark).
func BrandLogoSVG() *ui.SVG {
	initIcons()
	if brandLogo != nil {
		return brandLogo
	}
	return brandSmall
}

// BrandIconPNG returns the embedded 256x256 PNG icon bytes for window decoration.
func BrandIconPNG() []byte {
	initIcons()
	return brandPNGData
}

// NodeIconName maps a document node's type and reusable flag to its icon key.
func NodeIconName(nodeType string, reusable bool) string {
	if reusable || nodeType == "ref" {
		return "component"
	}
	switch nodeType {
	case "frame":
		return "frame"
	case "group":
		return "layers"
	case "rectangle":
		return "square"
	case "ellipse":
		return "circle"
	case "polygon":
		return "polygon"
	case "line":
		return "line"
	case "path":
		return "pen"
	case "text":
		return "type"
	case "icon", "icon_font":
		return "icon"
	case "image":
		return "image"
	case "script":
		return "sparkles"
	case "note":
		return "note"
	case "prompt":
		return "send"
	case "context":
		return "paperclip"
	default:
		return "square"
	}
}
