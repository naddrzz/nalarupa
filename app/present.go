package main

import (
	"fmt"
	"image"
	"math"

	"github.com/egoist/mygo/ui"
)

// PresentState holds persistent rendering state for full-window Presentation mode.
type PresentState struct {
	SlideIndex int
	RGBA       *image.RGBA
	Bitmap     *ui.Bitmap
	LastKey    presentCacheKey
}

type presentCacheKey struct {
	version uint64
	slideID string
	pw      int
	ph      int
}

func (ps *PresentState) initForApp(a *NalarupaApp, fromID string) {
	slides := a.topLevelSlides()
	ps.SlideIndex = 0
	if fromID == "" || len(slides) == 0 {
		return
	}
	rootID := a.climbToTopLevel(fromID)
	for i, s := range slides {
		if s.ID == rootID {
			ps.SlideIndex = i
			return
		}
	}
}

func (a *NalarupaApp) topLevelSlides() []Box {
	var frames []Box
	a.Bridge.TryCall("frames", nil, &frames)
	var out []Box
	for _, f := range frames {
		if f.Type == "frame" && !f.Placeholder {
			out = append(out, f)
		}
	}
	return out
}

func (a *NalarupaApp) climbToTopLevel(id string) string {
	cur := id
	for guard := 0; guard < 64 && cur != "" && cur != "document"; guard++ {
		var nd NodeDetail
		if !a.Bridge.TryCall("node", map[string]any{"id": cur}, &nd) || !nd.Exists {
			break
		}
		if nd.Parent == "" || nd.Parent == "document" {
			return cur
		}
		cur = nd.Parent
	}
	return cur
}

func (a *NalarupaApp) viewPresent(c *ui.Context) {
	slides := a.topLevelSlides()
	if a.Present.SlideIndex >= len(slides) {
		a.Present.SlideIndex = max(0, len(slides)-1)
	}

	surface := ui.Box(c.Key("presentation")).
		Fill().
		Background(ui.Hex("#000000")).
		Role(ui.RoleGroup).
		Label("Presentation").
		Focusable().
		AutoFocus()

	stepSlide := func(delta int) {
		if len(slides) == 0 {
			return
		}
		next := a.Present.SlideIndex + delta
		next = max(0, min(len(slides)-1, next))
		a.Present.SlideIndex = next
	}

	surface.HandleInput(func(ev ui.InputEvent) bool {
		switch ev.Kind {
		case ui.InputKeyDown:
			switch ev.Key {
			case ui.KeyEscape:
				a.Screen = "editor"
				return true
			case ui.KeyRight, ui.KeyDown, ui.KeySpace, ui.KeyEnter, ui.KeyPageDown:
				stepSlide(1)
				return true
			case ui.KeyLeft, ui.KeyUp, ui.KeyBackspace, ui.KeyPageUp:
				stepSlide(-1)
				return true
			case ui.KeyHome:
				a.Present.SlideIndex = 0
				return true
			case ui.KeyEnd:
				if len(slides) > 0 {
					a.Present.SlideIndex = len(slides) - 1
				}
				return true
			}
		case ui.InputPointerDown:
			if ev.Button == 0 {
				stepSlide(1)
				return true
			}
			if ev.Button == 1 || ev.Button == 2 {
				stepSlide(-1)
				return true
			}
		}
		return false
	})

	surface.Draw(func(p *ui.Painter, r ui.Rect) {
		p.Fill(r, ui.Hex("#000000"), 0)
		if len(slides) == 0 {
			return
		}
		slide := slides[a.Present.SlideIndex]
		dpr := EffectiveDPR(p.Scale())
		pw := max(1, int(math.Round(float64(r.W)*dpr)))
		ph := max(1, int(math.Round(float64(r.H)*dpr)))
		cam := SlideCam(slide, float64(r.W), float64(r.H))

		ck := presentCacheKey{
			version: a.State.Version,
			slideID: slide.ID,
			pw:      pw,
			ph:      ph,
		}
		if a.State.Animating || a.Present.Bitmap == nil || a.Present.LastKey != ck {
			rgba := a.Bridge.RenderInto(&a.Present.RGBA, pw, ph, cam.X, cam.Y, cam.Zoom*dpr, PresentBG, slide.ID, true)
			if a.Present.Bitmap == nil {
				a.Present.Bitmap = ui.NewBitmap(rgba)
			} else {
				a.Present.Bitmap.Update(rgba)
			}
			a.Present.LastKey = ck
		}
		p.Image(a.Present.Bitmap, r, ui.FillBox)
		if a.State.Animating {
			p.AnimationFrame()
		}
	})

	surface.Children(func() {
		hudText := "No slides: add top-level frames · Esc to exit"
		if len(slides) > 0 {
			hudText = fmt.Sprintf("%d / %d · Esc to exit", a.Present.SlideIndex+1, len(slides))
		}
		ui.Box(c.Key("present-hud")).
			Absolute().
			Right(16).
			Bottom(12).
			Padding(5, 10).
			Radius(6).
			Background(ui.Hex("#00000099")).
			Border(1, ui.Hex("#FFFFFF22")).
			IgnoreInput(true).
			Children(func() {
				ui.Text(c, hudText).FontSize(11).TextColor(C.Text)
			})
	})
}
