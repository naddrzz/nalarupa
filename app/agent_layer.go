package main

import (
	"fmt"
	"math"
	"strings"

	"github.com/egoist/mygo/ui"
)

const (
	CursorDecayPerSec = 2.5
	PenDecayPerSec    = 40.0
	MaxActiveFlashes  = 120
)

func expApproach(cur, target, ratePerSec, dtSec float64, reduced bool) float64 {
	if reduced || dtSec <= 0 {
		return target
	}
	return target + (cur-target)*math.Exp(-ratePerSec*dtSec)
}

func (a *NalarupaApp) consumeCanvasActivity(nowMS float64) {
	reqs := a.Store.TakeActivity(nowMS)
	if len(reqs) == 0 {
		return
	}
	for _, req := range reqs {
		prof := a.Store.ProfileOf(req.Agent)
		a.Store.Profiles[prof.ID] = prof
		var boxes []Box
		if len(req.IDs) > 0 {
			a.Bridge.TryCall("boxes", map[string]any{"ids": req.IDs}, &boxes)
		}
		if len(boxes) == 0 {
			a.Store.Poke(prof, nowMS)
			continue
		}
		// Move agent cursor toward center of last touched box
		lastBox := boxes[len(boxes)-1]
		center := PointIn(lastBox, 0.5, 0.5)
		hadCursor := false
		if cur, ok := a.Store.Cursors[prof.ID]; ok {
			hadCursor = !a.Store.CursorFaded(prof.ID, cur, nowMS)
			a.Store.WakeCursor(prof.ID, cur, nowMS)
			cur.Profile = prof
			cur.TX = center[0]
			cur.TY = center[1]
		} else {
			a.Store.Cursors[prof.ID] = &CursorState{
				Profile:      prof,
				X:            center[0],
				Y:            center[1],
				TX:           center[0],
				TY:           center[1],
				AppearAt:     nowMS,
				LastActivity: nowMS,
			}
		}

		delay := 0.0
		if hadCursor {
			delay = 300.0
		}
		hold := 400.0
		if req.Op == "read" {
			hold = 1500.0
		}
		for _, b := range boxes {
			if len(a.Store.Flashes) >= MaxActiveFlashes {
				a.Store.Flashes = a.Store.Flashes[1:]
			}
			a.Store.Flashes = append(a.Store.Flashes, NodeFlash{
				ID:      b.ID,
				Color:   prof.Color,
				Op:      req.Op,
				StartAt: nowMS + delay,
				HoldMS:  hold,
			})
		}
	}
}

func (a *NalarupaApp) drawAgentLayer(p *ui.Painter, vpRect ui.Rect, liveNodes []LiveNode, reduced bool, nowMS float64) {
	prevMS := a.Canvas.LastPaintMS
	if prevMS <= 0 {
		prevMS = nowMS
	}
	dtSec := max(0.0, min(0.25, (nowMS-prevMS)/1000.0))

	a.consumeCanvasActivity(nowMS)

	ox, oy := vpRect.X, vpRect.Y
	toWin := func(dx, dy float64) (float32, float32) {
		sx, sy := a.docToScreen(dx, dy)
		return ox + sx, oy + sy
	}
	toWinRect := func(r RectD) ui.Rect {
		sr := a.docRectToScreen(r)
		sr.X += ox
		sr.Y += oy
		return sr
	}

	// 1. Live placeholder frames ("Generating: <name>" glow) & pending-image shimmers
	for _, ln := range liveNodes {
		sr := toWinRect(Painted(ln.Box))
		if sr.W < 1 || sr.H < 1 {
			continue
		}
		radii := FlipRadii(ln.Radius, ln.M)
		radPx := float32(radii[0] * a.Cam.Zoom)
		if ln.Type == "ellipse" {
			radPx = min(sr.W, sr.H) / 2
		}

		if ln.Pending {
			// Pending image shimmer (clipped to ancestor clip + node shape)
			drawShimmer := func() {
				p.Fill(sr, ui.Hex("#00000040"), radPx)
				prog := 0.5
				if !reduced {
					prog = math.Mod(nowMS/1600.0, 1.0)
				}
				band := 0.5 * max(float64(sr.W), float64(sr.H))
				pos := float64(sr.X) - band + prog*(float64(sr.W)+band*2)
				h1, h2 := ShimmerHalves(pos, band)
				p.Clip(sr, radPx, func() {
					if h1[1] > 0 {
						p.FillGradient(ui.Rect{X: float32(h1[0]), Y: sr.Y, W: float32(h1[1]), H: sr.H}, ui.LinearGradient{
							From:  ui.RGBA(255, 255, 255, 0),
							To:    ui.RGBA(255, 255, 255, 0.22),
							Angle: 90,
						}, 0)
					}
					if h2[1] > 0 {
						p.FillGradient(ui.Rect{X: float32(h2[0]), Y: sr.Y, W: float32(h2[1]), H: sr.H}, ui.LinearGradient{
							From:  ui.RGBA(255, 255, 255, 0.22),
							To:    ui.RGBA(255, 255, 255, 0),
							Angle: 90,
						}, 0)
					}
				})
			}
			if ln.Clip != nil {
				clipRect := toWinRect(*ln.Clip)
				clipRad := float32(0)
				if len(ln.Cuts) > 0 {
					clipRad = float32(ln.Cuts[0].R[0] * a.Cam.Zoom)
				}
				p.Clip(clipRect, clipRad, drawShimmer)
			} else {
				drawShimmer()
			}
		} else if ln.Placeholder {
			// Counter-orbiting dual glow + 2px presence border
			theta := 0.8
			if !reduced {
				theta += nowMS / 2000.0
			}
			cx, cy := sr.X+sr.W/2, sr.Y+sr.H/2
			rx, ry := sr.W*0.32, sr.H*0.32
			blobR := float32(0.4 * max(float64(sr.W), float64(sr.H)))
			p.Clip(sr, radPx, func() {
				for _, dir := range [2]float64{1, -1} {
					ang := theta * dir
					bx := cx + rx*float32(math.Cos(ang))
					by := cy + ry*float32(math.Sin(ang))
					br := ui.Rect{X: bx - blobR, Y: by - blobR, W: blobR * 2, H: blobR * 2}
					p.FillRadialGradient(br, ui.RadialGradient{
						Stops: []ui.GradientStop{
							{Color: ui.RGBA(96, 125, 255, 0.22), At: 0},
							{Color: ui.RGBA(96, 125, 255, 0), At: 1},
						},
					}, blobR)
				}
			})
			if Turned(ln.M) {
				cs := RectCorners(ln.RectOf(), ln.M)
				a.Canvas.overlayPath.Reset()
				x0, y0 := toWin(cs[0][0], cs[0][1])
				a.Canvas.overlayPath.MoveTo(x0, y0)
				for i := 1; i < 4; i++ {
					xi, yi := toWin(cs[i][0], cs[i][1])
					a.Canvas.overlayPath.LineTo(xi, yi)
				}
				a.Canvas.overlayPath.Close()
				p.StrokePath(&a.Canvas.overlayPath, 2, C.Presence)
			} else {
				p.Stroke(sr, C.Presence, radPx, 2)
			}
		}
	}

	// 2. Touched-node flashes (prune expired or deleted nodes immediately)
	if len(a.Store.Flashes) > 0 {
		var flashIDs []string
		for _, fl := range a.Store.Flashes {
			flashIDs = append(flashIDs, fl.ID)
		}
		var fbs []Box
		a.Bridge.TryCall("boxes", map[string]any{"ids": flashIDs}, &fbs)
		boxMap := make(map[string]Box, len(fbs))
		for _, b := range fbs {
			boxMap[b.ID] = b
		}
		kept := a.Store.Flashes[:0]
		for _, fl := range a.Store.Flashes {
			b, exists := boxMap[fl.ID]
			if !exists {
				continue
			}
			totalMS := 200.0 + fl.HoldMS + 300.0
			elapsed := nowMS - fl.StartAt
			if elapsed >= totalMS {
				continue
			}
			kept = append(kept, fl)
			if elapsed < 0 {
				continue
			}
			alpha := 1.0
			pad := float32(0)
			switch {
			case elapsed < 200.0:
				u := elapsed / 200.0
				alpha = u
				if !reduced {
					pad = float32((1 - EaseOutCubic(u)) * 20.0)
				}
			case elapsed > 200.0+fl.HoldMS:
				alpha = max(0.0, 1.0-(elapsed-200.0-fl.HoldMS)/300.0)
			}
			r, g, bl, _, ok := ParseHexRGBA(fl.Color)
			if !ok {
				r, g, bl = 96, 125, 255
			}
			col := ui.RGBA(r, g, bl, float32(alpha*0.85))
			sr := toWinRect(Painted(b))
			fr := ui.Rect{X: sr.X - pad, Y: sr.Y - pad, W: sr.W + pad*2, H: sr.H + pad*2}
			if Turned(b.M) {
				cs := RectCorners(b.RectOf(), b.M)
				a.Canvas.overlayPath.Reset()
				x0, y0 := toWin(cs[0][0], cs[0][1])
				a.Canvas.overlayPath.MoveTo(x0, y0)
				for i := 1; i < 4; i++ {
					xi, yi := toWin(cs[i][0], cs[i][1])
					a.Canvas.overlayPath.LineTo(xi, yi)
				}
				a.Canvas.overlayPath.Close()
				p.StrokePath(&a.Canvas.overlayPath, 2, col)
			} else {
				p.Stroke(fr, col, 4, 2)
				if !reduced && elapsed < 200.0+fl.HoldMS {
					scanU := min(1.0, elapsed/(200.0+fl.HoldMS))
					bandH := min(sr.H, max(12, sr.H*0.25))
					bandY := sr.Y + float32(scanU)*max(0, sr.H-bandH)
					p.Fill(ui.Rect{X: sr.X, Y: bandY, W: sr.W, H: bandH}, ui.RGBA(r, g, bl, float32(alpha*0.14)), 0)
				}
			}
		}
		a.Store.Flashes = kept
	}

	// 3. Agent cursors
	for aid, cur := range a.Store.Cursors {
		if a.Store.CursorFaded(aid, cur, nowMS) {
			delete(a.Store.Cursors, aid)
			continue
		}
		cur.X = expApproach(cur.X, cur.TX, CursorDecayPerSec, dtSec, reduced)
		cur.Y = expApproach(cur.Y, cur.TY, CursorDecayPerSec, dtSec, reduced)
		wobX, wobY := 0.0, 0.0
		if !reduced && math.Hypot(cur.TX-cur.X, cur.TY-cur.Y) < 2.0 {
			wobX = math.Sin(nowMS/550.0+cur.Phase) * 3.0
			wobY = math.Cos(nowMS/700.0+cur.Phase) * 3.0
		}
		sx, sy := toWin(cur.X+wobX, cur.Y+wobY)
		op := CursorOpacity(cur, nowMS, a.Store.Running[aid])
		if op <= 0.01 {
			continue
		}
		cr, cg, cb, _, ok := ParseHexRGBA(cur.Profile.Color)
		if !ok {
			cr, cg, cb = 96, 125, 255
		}
		fillCol := ui.RGBA(cr, cg, cb, float32(op))
		outCol := ui.RGBA(255, 255, 255, float32(op))
		ar := ui.Rect{X: sx - 2, Y: sy - 2, W: 20, H: 20}
		if cursorFill != nil {
			p.Icon(cursorFill, ar, fillCol)
		}
		if cursorStroke != nil {
			p.Icon(cursorStroke, ar, outCol)
		}
	}

	// 4. AI Pen cursors
	for _, pen := range a.Store.Pens {
		pen.X = expApproach(pen.X, pen.TX, PenDecayPerSec, dtSec, reduced)
		pen.Y = expApproach(pen.Y, pen.TY, PenDecayPerSec, dtSec, reduced)
		sx, sy := toWin(pen.X, pen.Y)
		p.Icon(IconSVG("pen"), ui.Rect{X: sx - 2, Y: sy - 16, W: 18, H: 18}, C.Presence)
	}

	chatOpen := a.RightTab == "agent"
	if a.Store.NeedsFrame(nowMS, reduced, len(liveNodes), prevMS, chatOpen) {
		p.AnimationFrame()
	}
	a.Canvas.LastPaintMS = nowMS
}

func (a *NalarupaApp) buildAgentLayerChildren(c *ui.Context, liveNodes []LiveNode, nowMS float64) {
	reduced := EffectiveReducedMotion(c)

	// 1. Live placeholder labels & rotated/round semantic markers
	for _, ln := range liveNodes {
		sr := a.docRectToScreen(Painted(ln.Box))
		if ln.Placeholder && (ln.Parent == nil || *ln.Parent == "" || *ln.Parent == "document") {
			bob := float32(0)
			if !reduced {
				bob = float32(math.Sin(nowMS/200.0) * 1.5)
			}
			ui.Row(c.Key("generating-label-" + ln.ID)).
				Absolute().
				Left(sr.X).
				Top(sr.Y - 26 + bob).
				Padding(3, 8).
				Gap(5).
				AlignItems(ui.Center).
				Radius(6).
				Background(ui.Hex("#1E1E22EE")).
				Border(1, C.Presence).
				IgnoreInput(true).
				Children(func() {
					ui.Icon(c, IconSVG("sparkles")).FontSize(11).TextColor(C.Presence)
					ui.Text(c, "Generating: "+ln.Name).FontSize(11).FontWeight(500).TextColor(C.Text).MaxLines(1)
				})
		}
		if Turned(ln.M) {
			k := "agent-shimmer-turned"
			if ln.Placeholder {
				k = "agent-glow-turned"
			}
			ui.Box(c.Key(k + "-" + ln.ID)).
				Absolute().
				Left(sr.X).
				Top(sr.Y).
				Size(max(1, sr.W), max(1, sr.H)).
				IgnoreInput(true)
		} else if ln.Pending && (ln.Type == "ellipse" || ln.Radius[0] > 0 || (len(ln.Cuts) > 0 && ln.Cuts[0].R[0] > 0)) {
			rTarget := sr
			if ln.Clip != nil {
				rTarget = a.docRectToScreen(*ln.Clip)
			}
			ui.Box(c.Key("agent-shimmer-round-" + ln.ID)).
				Absolute().
				Left(rTarget.X).
				Top(rTarget.Y).
				Size(max(1, rTarget.W), max(1, rTarget.H)).
				IgnoreInput(true)
		}
	}

	// 2. Agent cursor badges (pointer-transparent so clicks pass right through to canvas!)
	for aid, cur := range a.Store.Cursors {
		if a.Store.CursorFaded(aid, cur, nowMS) {
			continue
		}
		op := CursorOpacity(cur, nowMS, a.Store.Running[aid])
		if op <= 0.01 {
			continue
		}
		sx, sy := a.docToScreen(cur.X, cur.Y)
		cr, cg, cb, _, ok := ParseHexRGBA(cur.Profile.Color)
		if !ok {
			cr, cg, cb = 96, 125, 255
		}
		bgCol := ui.RGBA(cr, cg, cb, float32(op))
		fgCol := ContrastText(cur.Profile.Color)
		ui.Box(c.Key("agent-cursor-" + aid)).
			Absolute().
			Left(sx + 14).
			Top(sy + 14).
			Padding(2, 7).
			Radius(6).
			Background(bgCol).
			Opacity(float32(op)).
			IgnoreInput(true).
			Children(func() {
				ui.Text(c, cur.Profile.Name).FontSize(11).FontWeight(600).TextColor(fgCol).MaxLines(1)
			})
	}

	// 3. AI Pen cursor badge
	for fid, pen := range a.Store.Pens {
		sx, sy := a.docToScreen(pen.X, pen.Y)
		ui.Box(c.Key("ai-pen-" + fid)).
			Absolute().
			Left(sx + 14).
			Top(sy - 8).
			Padding(2, 7).
			Radius(6).
			Background(C.Presence).
			IgnoreInput(true).
			Children(func() {
				ui.Text(c, "AI Pen").FontSize(11).FontWeight(600).TextColor(ui.Hex("#FFFFFF")).MaxLines(1)
			})
	}

	// 4. Bottom-center streaming message pills
	chatOpen := a.RightTab == "agent"
	var activePills []struct {
		id   string
		prof AgentProfile
		text string
	}
	for _, id := range a.Store.ProfileOrder {
		p, ok := a.Store.Pills[id]
		if !ok || nowMS-p.At >= PillMS || !a.Store.PillShown(id, chatOpen) {
			continue
		}
		snippet := strings.Join(strings.Fields(p.Text), " ")
		if len(snippet) > 140 {
			snippet = snippet[:137] + "…"
		}
		if snippet == "" {
			continue
		}
		activePills = append(activePills, struct {
			id   string
			prof AgentProfile
			text string
		}{id: id, prof: a.Store.ProfileOf(id), text: snippet})
	}
	if len(activePills) > 0 {
		ui.Column(c.Key("agent-pills")).
			Absolute().
			Left(float32(a.CanvasW)/2 - 200).
			Width(400).
			Bottom(16).
			Gap(6).
			AlignItems(ui.Center).
			IgnoreInput(true).
			Children(func() {
				for _, ap := range activePills {
					cr, cg, cb, _, ok := ParseHexRGBA(ap.prof.Color)
					if !ok {
						cr, cg, cb = 96, 125, 255
					}
					dotAlpha := float32(1.0)
					if !reduced {
						dotAlpha = float32(0.35 + 0.65*PulseAt(int64(nowMS)))
					}
					ui.Row(c.Key(fmt.Sprintf("agent-pill-%s", ap.id))).
						MaxWidth(390).
						Padding(6, 12).
						Gap(8).
						AlignItems(ui.Center).
						Radius(16).
						Background(ui.Hex("#17171AEE")).
						Border(1, C.Border).
						IgnoreInput(true).
						Children(func() {
							ui.Box(c.Key("agent-pill-dot-" + ap.id)).
								Size(8, 8).
								Shrink(0).
								Radius(4).
								Background(ui.RGBA(cr, cg, cb, dotAlpha)).
								IgnoreInput(true)
							ui.Text(c, ap.prof.Name+": "+ap.text).
								FontSize(11).
								TextColor(C.Text).
								MaxLines(1)
						})
				}
			})
	}
}
