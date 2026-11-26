package main

import (
	"fmt"
	"math"
	"strings"

	"github.com/egoist/mygo/ui"
)

const (
	CursorDecayPerSec = 14.0
	CursorSpringOmega = 22.0
	PenDecayPerSec    = 40.0
	MaxActiveFlashes  = 140
	MaxCursorWaypts   = 64
)

func expApproach(cur, target, ratePerSec, dtSec float64, reduced bool) float64 {
	if reduced || dtSec <= 0 {
		return target
	}
	return target + (cur-target)*math.Exp(-ratePerSec*dtSec)
}

// stepCriticallyDampedSpring advances a 1D critically-damped spring (zeta = 1.0) analytically for any dtSec > 0,
// preserving C1 velocity continuity across mid-flight target updates with zero overshoot or oscillation.
func stepCriticallyDampedSpring(pos, vel, target, omega, dtSec float64) (nextPos, nextVel float64) {
	if dtSec <= 0 {
		return pos, vel
	}
	d := pos - target
	e := math.Exp(-omega * dtSec)
	c := vel + omega*d
	nextD := (d + c*dtSec) * e
	nextVel = (vel - c*omega*dtSec) * e
	return target + nextD, nextVel
}

func (a *NalarupaApp) pushNodeFlash(boxID, colorHex, op string, startAt, holdMS float64) {
	a.pushNodeFlashWithBox(boxID, nil, "", colorHex, op, startAt, holdMS)
}

func (a *NalarupaApp) pushNodeFlashWithBox(boxID string, box *Box, agentID, colorHex, op string, startAt, holdMS float64) {
	if boxID == "" {
		return
	}
	if op == "write" && holdMS > 70.0 {
		holdMS = 70.0
	}
	fl := NodeFlash{
		ID:            boxID,
		AgentID:       agentID,
		Color:         colorHex,
		Op:            op,
		StartAt:       startAt,
		HoldMS:        holdMS,
		MinVer:        max(a.State.Version, a.Bridge.VersionDirect()),
		MinReleaseSeq: a.Bridge.SlideReleaseSeqDirect(),
	}
	if box != nil {
		fl.Box = *box
		fl.HasBox = true
	} else if got, ok := a.Bridge.TryBoxesDirect([]string{boxID}); ok && len(got) > 0 {
		fl.Box = got[0]
		fl.HasBox = true
	}
	if agentID != "" && op == "write" {
		for i := range a.Store.Flashes {
			if a.Store.Flashes[i].AgentID == agentID && a.Store.Flashes[i].Op == "write" {
				a.Store.Flashes[i] = fl
				return
			}
		}
	}
	if len(a.Store.Flashes) >= MaxActiveFlashes {
		a.Store.Flashes = a.Store.Flashes[1:]
	}
	a.Store.Flashes = append(a.Store.Flashes, fl)
}

func popNextCursorWaypoint(cur *CursorState) bool {
	if len(cur.Waypoints) == 0 {
		cur.HasActiveWP = false
		cur.ActiveTouched = false
		cur.ActiveReleased = false
		return false
	}
	wp := cur.Waypoints[0]
	cur.Waypoints = cur.Waypoints[1:]
	cur.ActiveWP = wp
	cur.HasActiveWP = true
	cur.ActiveTouched = false
	cur.ActiveReleased = false
	cur.TX = wp.X
	cur.TY = wp.Y
	cur.AnchorX = wp.X
	cur.AnchorY = wp.Y
	cur.HasAnchor = true
	cur.DwellUntil = 0
	return true
}

func collectPendingHoldIDs(cur *CursorState, includeActive bool) []string {
	n := len(cur.Waypoints)
	if includeActive && cur.HasActiveWP && !cur.ActiveTouched && !cur.ActiveReleased && cur.ActiveWP.BoxID != "" {
		n++
	}
	if n == 0 {
		return nil
	}
	out := make([]string, 0, n)
	if includeActive && cur.HasActiveWP && !cur.ActiveTouched && !cur.ActiveReleased && cur.ActiveWP.BoxID != "" {
		out = append(out, cur.ActiveWP.BoxID)
	}
	for i := range cur.Waypoints {
		if id := cur.Waypoints[i].BoxID; id != "" {
			out = append(out, id)
		}
	}
	return out
}

func releaseActiveWaypointEarly(a *NalarupaApp, cur *CursorState) {
	if !cur.HasActiveWP || cur.ActiveReleased {
		return
	}
	cur.ActiveReleased = true
	wp := cur.ActiveWP
	relIDs := wp.ReleaseIDs
	if len(relIDs) == 0 && wp.BoxID != "" {
		relIDs = []string{wp.BoxID}
	}
	if wp.Op == "write" && len(relIDs) > 0 {
		a.Bridge.QueueCursorUpdateDirect(relIDs, collectPendingHoldIDs(cur, false))
	}
}

func triggerWaypointArrival(a *NalarupaApp, cur *CursorState, nowMS float64) {
	if !cur.HasActiveWP || cur.ActiveTouched {
		return
	}
	releaseActiveWaypointEarly(a, cur)
	cur.ActiveTouched = true
	wp := cur.ActiveWP
	cadence := 42.0
	if n := len(cur.Waypoints); n > 3 {
		cadence = max(24.0, 42.0-float64(n)*2.5)
	}
	cur.NextWaypointAt = nowMS + cadence
	cur.PlacePulseAt = nowMS
	cur.LastActivity = nowMS
	var bPtr *Box
	if wp.BoxID != "" {
		if a.Window != nil {
			if fbs, ok := a.Bridge.TryBoxesDirect([]string{wp.BoxID}); ok {
				if len(fbs) > 0 {
					cp := fbs[0]
					bPtr = &cp
				}
			} else if wp.HasBox {
				cp := wp.Box
				bPtr = &cp
			}
		} else if fbs := a.Bridge.BoxesDirect([]string{wp.BoxID}); len(fbs) > 0 {
			cp := fbs[0]
			bPtr = &cp
		}
	} else if wp.HasBox {
		cp := wp.Box
		bPtr = &cp
	}
	a.pushNodeFlashWithBox(wp.BoxID, bPtr, cur.Profile.ID, cur.Profile.Color, wp.Op, nowMS, wp.HoldMS)
}

func advanceCursorWaypoint(a *NalarupaApp, cur *CursorState, nowMS float64) {
	if cur.HasActiveWP && !cur.ActiveTouched {
		triggerWaypointArrival(a, cur, nowMS)
	}
	popNextCursorWaypoint(cur)
}

type filteredChoreoItem struct {
	box        Box
	releaseIDs []string
}

func filterChoreographyBoxes(boxes []Box) []filteredChoreoItem {
	if len(boxes) == 0 {
		return nil
	}
	if len(boxes) == 1 {
		return []filteredChoreoItem{{box: boxes[0], releaseIDs: []string{boxes[0].ID}}}
	}
	// When a top-level screen container is accompanied by its inner child sections/cards,
	// skip the outer screen center so the cursor glides top-to-bottom across the actual elements being built.
	startIdx := 0
	var pendingExact []string
	if (boxes[0].Parent == nil || *boxes[0].Parent == "" || *boxes[0].Parent == "document") && boxes[0].ID != "" && len(boxes) > 1 {
		anc := map[string]bool{boxes[0].ID: true}
		hasDescendant := false
		for j := 1; j < len(boxes); j++ {
			if boxes[j].Parent != nil && anc[*boxes[j].Parent] {
				if boxes[j].ID != "" {
					anc[boxes[j].ID] = true
				}
				hasDescendant = true
			}
		}
		if hasDescendant {
			startIdx = 1
			pendingExact = append(pendingExact, "@exact:"+boxes[0].ID)
		}
	}
	out := make([]filteredChoreoItem, 0, len(boxes)-startIdx)
	for i := startIdx; i < len(boxes); i++ {
		b := boxes[i]
		if b.W < 2 || b.H < 2 {
			if b.ID != "" {
				pendingExact = append(pendingExact, "@exact:"+b.ID)
			}
			continue
		}
		// If this is a pure layout row/wrapper whose next item is its immediate child, prefer visiting the children directly.
		if b.Type == "frame" && i+1 < len(boxes) && boxes[i+1].Parent != nil && *boxes[i+1].Parent == b.ID {
			childCount := 0
			for j := i + 1; j < len(boxes); j++ {
				if boxes[j].Parent != nil && *boxes[j].Parent == b.ID {
					childCount++
				}
			}
			if childCount >= 2 {
				if b.ID != "" {
					pendingExact = append(pendingExact, "@exact:"+b.ID)
				}
				continue
			}
		}
		c := PointIn(b, 0.5, 0.5)
		if len(out) > 0 {
			prev := PointIn(out[len(out)-1].box, 0.5, 0.5)
			if math.Hypot(c[0]-prev[0], c[1]-prev[1]) < 14.0 {
				if b.ID != "" {
					out[len(out)-1].releaseIDs = append(out[len(out)-1].releaseIDs, b.ID)
				}
				continue
			}
		}
		rels := make([]string, 0, len(pendingExact)+1)
		rels = append(rels, pendingExact...)
		pendingExact = pendingExact[:0]
		if b.ID != "" {
			rels = append(rels, b.ID)
		}
		out = append(out, filteredChoreoItem{box: b, releaseIDs: rels})
	}
	if len(out) == 0 {
		rels := make([]string, 0, len(pendingExact)+1)
		if boxes[0].ID != "" {
			rels = append(rels, boxes[0].ID)
		}
		return []filteredChoreoItem{{box: boxes[0], releaseIDs: rels}}
	}
	if len(pendingExact) > 0 {
		out[len(out)-1].releaseIDs = append(out[len(out)-1].releaseIDs, pendingExact...)
	}
	return out
}

func normalizeActivityRootIDs(ids []string) []string {
	var out []string
	for _, id := range ids {
		if idx := strings.IndexByte(id, '/'); idx > 0 {
			out = append(out, id[:idx])
		} else if id != "" {
			out = append(out, id)
		}
	}
	return out
}

func (a *NalarupaApp) consumeCanvasActivity(nowMS float64) {
	reqs := a.Store.TakeActivity(nowMS)
	if len(reqs) == 0 {
		return
	}
	var deferred []ActivityReq
	for _, req := range reqs {
		prof := a.Store.ProfileOf(req.Agent)
		a.Store.Profiles[prof.ID] = prof
		lookupIDs := req.IDs
		if req.Op == "write" {
			lookupIDs = normalizeActivityRootIDs(req.IDs)
		}
		var boxes []Box
		if len(lookupIDs) > 0 {
			if a.Window != nil {
				contended := false
				if req.Op == "write" {
					got, ok := a.Bridge.TrySubtreeBoxesDirect(lookupIDs, 64)
					if !ok {
						contended = true
					} else {
						boxes = got
					}
				}
				if !contended && len(boxes) == 0 {
					got, ok := a.Bridge.TryBoxesDirect(lookupIDs)
					if !ok {
						contended = true
					} else {
						boxes = got
					}
				}
				if contended {
					deferred = append(deferred, req)
					continue
				}
			} else {
				if req.Op == "write" {
					boxes = a.Bridge.SubtreeBoxesDirect(lookupIDs, 64)
				}
				if len(boxes) == 0 {
					boxes = a.Bridge.BoxesDirect(lookupIDs)
				}
			}
		}
		var items []filteredChoreoItem
		if req.Op == "write" && len(boxes) > 0 {
			items = filterChoreographyBoxes(boxes)
		} else {
			items = make([]filteredChoreoItem, len(boxes))
			for i, b := range boxes {
				var rel []string
				if b.ID != "" {
					rel = []string{b.ID}
				}
				items[i] = filteredChoreoItem{box: b, releaseIDs: rel}
			}
		}
		if len(items) == 0 {
			a.Store.Poke(prof, nowMS)
			continue
		}

		hold := 260.0
		if req.Op == "read" {
			hold = 700.0
		}

		invZ := 1.0 / max(0.25, min(2.5, a.Cam.Zoom))
		firstCenter := PointIn(items[0].box, 0.5, 0.5)
		cur, ok := a.Store.Cursors[prof.ID]
		if !ok || a.Store.CursorFaded(prof.ID, cur, nowMS) {
			cur = &CursorState{
				Profile:      prof,
				X:            firstCenter[0] - 148*invZ,
				Y:            firstCenter[1] - 96*invZ,
				TX:           firstCenter[0],
				TY:           firstCenter[1],
				AnchorX:      firstCenter[0],
				AnchorY:      firstCenter[1],
				HasAnchor:    true,
				AppearAt:     nowMS,
				LastActivity: nowMS,
			}
			a.Store.Cursors[prof.ID] = cur
		} else {
			a.Store.WakeCursor(prof.ID, cur, nowMS)
			cur.Profile = prof
		}

		for _, it := range items {
			b := it.box
			c := PointIn(b, 0.5, 0.5)
			if n := len(cur.Waypoints); n > 0 && cur.Waypoints[n-1].BoxID == b.ID && cur.Waypoints[n-1].Op == req.Op {
				cur.Waypoints[n-1].X = c[0]
				cur.Waypoints[n-1].Y = c[1]
				cur.Waypoints[n-1].Box = b
				cur.Waypoints[n-1].HasBox = true
				cur.Waypoints[n-1].ReleaseIDs = append(cur.Waypoints[n-1].ReleaseIDs, it.releaseIDs...)
				continue
			}
			if cur.HasActiveWP && len(cur.Waypoints) == 0 && cur.ActiveWP.BoxID == b.ID && cur.ActiveWP.Op == req.Op {
				cur.ActiveWP.X = c[0]
				cur.ActiveWP.Y = c[1]
				cur.ActiveWP.Box = b
				cur.ActiveWP.HasBox = true
				cur.ActiveWP.ReleaseIDs = append(cur.ActiveWP.ReleaseIDs, it.releaseIDs...)
				cur.TX = c[0]
				cur.TY = c[1]
				if cur.ActiveReleased && req.Op == "write" && len(it.releaseIDs) > 0 {
					a.Bridge.QueueCursorUpdateDirect(it.releaseIDs, collectPendingHoldIDs(cur, false))
				}
				continue
			}
			if len(cur.Waypoints) >= MaxCursorWaypts {
				dropped := cur.Waypoints[0]
				cur.Waypoints = cur.Waypoints[1:]
				if dropped.Op == "write" && len(dropped.ReleaseIDs) > 0 {
					a.Bridge.QueueCursorUpdateDirect(dropped.ReleaseIDs, nil)
				}
			}
			cur.Waypoints = append(cur.Waypoints, CursorWaypoint{
				X:          c[0],
				Y:          c[1],
				BoxID:      b.ID,
				ReleaseIDs: it.releaseIDs,
				Box:        b,
				HasBox:     true,
				Op:         req.Op,
				HoldMS:     hold,
			})
		}

		// Compress rapid streaming bursts so the cursor never accumulates a multi-second backlog behind live chunks.
		if req.Op == "write" && len(cur.Waypoints) > 5 {
			var burstRel []string
			for len(cur.Waypoints) > 4 {
				dropped := cur.Waypoints[0]
				cur.Waypoints = cur.Waypoints[1:]
				if dropped.Op == "write" {
					if len(dropped.ReleaseIDs) > 0 {
						burstRel = append(burstRel, dropped.ReleaseIDs...)
					} else if dropped.BoxID != "" {
						burstRel = append(burstRel, dropped.BoxID)
					}
				}
			}
			if len(burstRel) > 0 {
				a.Bridge.QueueCursorUpdateDirect(burstRel, collectPendingHoldIDs(cur, true))
			}
		}

		if !cur.HasActiveWP && len(cur.Waypoints) > 0 {
			popNextCursorWaypoint(cur)
		}
		if req.Op == "write" {
			if holdIDs := collectPendingHoldIDs(cur, true); len(holdIDs) > 0 {
				a.Bridge.QueueCursorUpdateDirect(nil, holdIDs)
			}
		}
	}
	if len(deferred) > 0 {
		a.Store.Activity = append(deferred, a.Store.Activity...)
	}
}

// stepAgentChoreography advances agent & AI Pen cursor physics at VSync and releases gated canvas slides/edits
// at the exact frame the cursor reaches each target — BEFORE the canvas render pass snapshots the scene!
func (a *NalarupaApp) stepAgentChoreography(nowMS float64, reduced bool, liveNodes []LiveNode) float64 {
	prevMS := a.Canvas.LastPaintMS
	if prevMS <= 0 {
		prevMS = nowMS
	}
	dtSec := max(0.0, min(0.25, (nowMS-prevMS)/1000.0))

	a.consumeCanvasActivity(nowMS)

	invZ := 1.0 / max(0.25, min(2.5, a.Cam.Zoom))
	touchRadius := max(26.0, min(52.0, 36.0*invZ))
	leadReleaseRadius := max(46.0, min(84.0, 60.0*invZ))

	runningSlot := map[string]int{}
	slotCount := 0
	for _, pid := range a.Store.ProfileOrder {
		if a.Store.Running[pid] {
			runningSlot[pid] = slotCount
			slotCount++
		}
	}

	for aid, cur := range a.Store.Cursors {
		if a.Store.CursorFaded(aid, cur, nowMS) {
			delete(a.Store.Cursors, aid)
			continue
		}
		if !cur.HasActiveWP && len(cur.Waypoints) > 0 {
			popNextCursorWaypoint(cur)
		}
		prevX, prevY := cur.X, cur.Y
		if reduced {
			for cur.HasActiveWP {
				triggerWaypointArrival(a, cur, nowMS)
				cur.X, cur.Y = cur.ActiveWP.X, cur.ActiveWP.Y
				if !popNextCursorWaypoint(cur) {
					break
				}
			}
			cur.X, cur.Y = cur.TX, cur.TY
			cur.VX, cur.VY = 0, 0
			cur.TrailLen = 0
			continue
		}

		if cur.HasActiveWP {
			cur.TX = cur.ActiveWP.X
			cur.TY = cur.ActiveWP.Y
			cur.AnchorX = cur.TX
			cur.AnchorY = cur.TY
			cur.HasAnchor = true
		} else {
			if !cur.HasAnchor {
				cur.AnchorX, cur.AnchorY, cur.HasAnchor = cur.TX, cur.TY, true
			}
			if len(liveNodes) > 0 && a.Store.Running[aid] {
				slot := runningSlot[aid]
				var lc Pt
				if slot < len(liveNodes) {
					lc = PointIn(liveNodes[slot].Box, 0.5, 0.38)
				} else {
					lastBox := liveNodes[len(liveNodes)-1].Box
					extra := float64(slot - len(liveNodes) + 1)
					gap := max(120.0+lastBox.W, 140.0/max(0.04, a.Cam.Zoom))
					lc = Pt{lastBox.X + lastBox.W*0.5 + gap*extra, lastBox.Y + lastBox.H*0.38}
				}
				cur.AnchorX = expApproach(cur.AnchorX, lc[0], 3.2, dtSec, false)
				cur.AnchorY = expApproach(cur.AnchorY, lc[1], 3.2, dtSec, false)
			}
			t := nowMS/1000.0 + cur.Phase
			if a.Store.Running[aid] {
				// Organic figure-8 pre-design / planning glide fed smoothly through the C1 spring (zero snap!).
				cur.TX = cur.AnchorX + (math.Sin(t*1.75)*30.0+math.Cos(t*0.80)*11.0)*invZ
				cur.TY = cur.AnchorY + (math.Sin(t*2.30)*16.0+math.Sin(t*0.65)*8.0)*invZ
			} else {
				cur.TX = cur.AnchorX + math.Sin(t*1.15)*4.0*invZ
				cur.TY = cur.AnchorY + math.Cos(t*0.90)*3.0*invZ
			}
		}

		cur.X, cur.VX = stepCriticallyDampedSpring(cur.X, cur.VX, cur.TX, CursorSpringOmega, dtSec)
		cur.Y, cur.VY = stepCriticallyDampedSpring(cur.Y, cur.VY, cur.TY, CursorSpringOmega, dtSec)

		if cur.HasActiveWP {
			dist := math.Hypot(cur.TX-cur.X, cur.TY-cur.Y)
			if !cur.ActiveTouched {
				if dist <= touchRadius {
					triggerWaypointArrival(a, cur, nowMS)
				} else if dist <= leadReleaseRadius {
					releaseActiveWaypointEarly(a, cur)
				} else if cur.ActiveWP.Op == "write" {
					if holdIDs := collectPendingHoldIDs(cur, true); len(holdIDs) > 0 {
						a.Bridge.QueueCursorUpdateDirect(nil, holdIDs)
					}
				}
			} else {
				if len(cur.Waypoints) > 0 {
					if nowMS >= cur.NextWaypointAt || dist <= 18.0*invZ {
						popNextCursorWaypoint(cur)
					}
				} else if dist <= 6.0*invZ {
					cur.HasActiveWP = false
					cur.ActiveTouched = false
					cur.ActiveReleased = false
				}
			}
		}

		stepDist := math.Hypot(cur.X-prevX, cur.Y-prevY)
		if stepDist*a.Cam.Zoom > 0.85 {
			for ti := len(cur.Trail) - 1; ti > 0; ti-- {
				cur.Trail[ti] = cur.Trail[ti-1]
			}
			cur.Trail[0] = [2]float64{prevX, prevY}
			if cur.TrailLen < len(cur.Trail) {
				cur.TrailLen++
			}
		} else if cur.TrailLen > 0 && (!cur.HasActiveWP && len(cur.Waypoints) == 0 || stepDist*a.Cam.Zoom < 0.35) {
			cur.TrailLen--
		}
	}

	for _, pen := range a.Store.Pens {
		pen.X = expApproach(pen.X, pen.TX, PenDecayPerSec, dtSec, reduced)
		pen.Y = expApproach(pen.Y, pen.TY, PenDecayPerSec, dtSec, reduced)
	}

	a.Canvas.LastPaintMS = nowMS
	return prevMS
}

func (a *NalarupaApp) drawAgentLayer(p *ui.Painter, vpRect ui.Rect, liveNodes []LiveNode, reduced bool, nowMS, prevMS float64, engVer uint64) bool {

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

	// 1. Live placeholder frames ("Generating: <name>" aurora wash + perimeter beam) & pending-image shimmers
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
			// Pending image shimmer (constant-speed linear sweep with smooth bell-curve alpha envelope)
			drawShimmer := func() {
				p.Fill(sr, ui.Hex("#00000038"), radPx)
				prog := 0.5
				env := float32(1.0)
				if !reduced {
					prog = math.Mod(nowMS/1350.0, 1.0)
					env = float32(0.35 + 0.65*math.Sin(prog*math.Pi))
				}
				band := 0.46 * max(float64(sr.W), float64(sr.H))
				pos := float64(sr.X) - band + prog*(float64(sr.W)+band*2)
				h1, h2 := ShimmerHalves(pos, band)
				peakA := 0.24 * env
				p.Clip(sr, radPx, func() {
					if h1[1] > 0 {
						p.FillGradient(ui.Rect{X: float32(h1[0]), Y: sr.Y, W: float32(h1[1]), H: sr.H}, ui.LinearGradient{
							From:  ui.RGBA(255, 255, 255, 0),
							To:    ui.RGBA(255, 255, 255, peakA),
							Angle: 90,
						}, 0)
					}
					if h2[1] > 0 {
						p.FillGradient(ui.Rect{X: float32(h2[0]), Y: sr.Y, W: float32(h2[1]), H: sr.H}, ui.LinearGradient{
							From:  ui.RGBA(255, 255, 255, peakA),
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
			// Counter-orbiting aurora dual wash (indigo + warm accent) + crisp presence border + linear perimeter beam
			theta := 0.8
			if !reduced {
				theta += nowMS / 1700.0
			}
			cx, cy := sr.X+sr.W/2, sr.Y+sr.H/2
			rx, ry := sr.W*0.30, sr.H*0.30
			blobR := float32(0.44 * max(float64(sr.W), float64(sr.H)))
			p.Clip(sr, radPx, func() {
				p.Fill(sr, ui.RGBA(79, 70, 229, 0.05), radPx)
				// Primary presence indigo wash
				bx1 := cx + rx*float32(math.Cos(theta))
				by1 := cy + ry*float32(math.Sin(theta))
				p.FillRadialGradient(ui.Rect{X: bx1 - blobR, Y: by1 - blobR, W: blobR * 2, H: blobR * 2}, ui.RadialGradient{
					Stops: []ui.GradientStop{
						{Color: ui.RGBA(79, 70, 229, 0.20), At: 0},
						{Color: ui.RGBA(79, 70, 229, 0), At: 1},
					},
				}, blobR)
				// Counter-rotating warm terracotta-amber wash
				bx2 := cx + rx*float32(math.Cos(-theta*0.85+1.1))
				by2 := cy + ry*float32(math.Sin(-theta*0.85+1.1))
				p.FillRadialGradient(ui.Rect{X: bx2 - blobR, Y: by2 - blobR, W: blobR * 2, H: blobR * 2}, ui.RadialGradient{
					Stops: []ui.GradientStop{
						{Color: ui.RGBA(255, 106, 61, 0.14), At: 0},
						{Color: ui.RGBA(255, 106, 61, 0), At: 1},
					},
				}, blobR)
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
				p.StrokePath(&a.Canvas.overlayPath, 1.75, C.Presence)
			} else {
				p.Stroke(sr, ui.RGBA(79, 70, 229, 0.72), radPx, 1.5)
				// Constant-rate linear specular beam travelling smoothly around the frame perimeter
				perim := float64(2 * (sr.W + sr.H))
				if !reduced && perim >= 40 && radPx <= 1 {
					beamFrac := 0.18
					u0 := math.Mod(nowMS/2200.0, 1.0)
					drawRectPerimeterSegment(p, &a.Canvas.overlayPath, sr, u0, beamFrac, 2.25, ui.RGBA(79, 70, 229, 0.96))
				}
			}
		}
	}

	// 2. Touched-node flashes (160ms EaseOutQuint enter, crisp focus ring, faster 120ms exit — zero heavy box wash!)
	if len(a.Store.Flashes) > 0 {
		needRefresh := a.Canvas.cachedFlashVer != engVer
		if !needRefresh {
			for i := range a.Store.Flashes {
				if !a.Store.Flashes[i].HasBox {
					needRefresh = true
					break
				}
			}
		}
		var boxMap map[string]Box
		refreshed := false
		if needRefresh {
			flashIDs := make([]string, len(a.Store.Flashes))
			for i, fl := range a.Store.Flashes {
				flashIDs[i] = fl.ID
			}
			if fbs, ok := a.Bridge.TryBoxesDirect(flashIDs); ok {
				boxMap = make(map[string]Box, len(fbs))
				for _, b := range fbs {
					boxMap[b.ID] = b
				}
				refreshed = true
				a.Canvas.cachedFlashVer = engVer
			}
		}
		const flashEnterMS = 110.0
		const flashExitMS = 85.0
		kept := a.Store.Flashes[:0]
		for _, fl := range a.Store.Flashes {
			if refreshed {
				b, exists := boxMap[fl.ID]
				if !exists {
					continue
				}
				fl.Box = b
				fl.HasBox = true
			}
			// Never draw an empty wireframe box ahead of Canvas.Bitmap: hold the flash until the worker
			// has swapped in the rendered frame covering this slide release!
			if fl.Op == "write" && (a.Canvas.LastKey.version < fl.MinVer || a.Canvas.LastKey.releaseSeq < fl.MinReleaseSeq) {
				if nowMS-fl.StartAt < 450.0 {
					kept = append(kept, fl)
				}
				continue
			}
			if fl.PaintStartAt <= 0 {
				fl.PaintStartAt = nowMS
			}
			totalMS := flashEnterMS + fl.HoldMS + flashExitMS
			elapsed := nowMS - fl.PaintStartAt
			if elapsed >= totalMS {
				continue
			}
			kept = append(kept, fl)
			if !fl.HasBox || elapsed < 0 {
				continue
			}
			b := fl.Box
			alpha := 1.0
			pad := float32(0)
			switch {
			case elapsed < flashEnterMS:
				u := elapsed / flashEnterMS
				e := EaseOutQuint64(u)
				alpha = e
				if !reduced {
					pad = float32((1.0 - e) * 4.5)
				}
			case elapsed > flashEnterMS+fl.HoldMS:
				u := (elapsed - flashEnterMS - fl.HoldMS) / flashExitMS
				alpha = max(0.0, 1.0-u)
			}
			r, g, bl, _, ok := ParseHexRGBA(fl.Color)
			if !ok {
				r, g, bl = 96, 125, 255
			}
			ringAlpha := float32(alpha * 0.46)
			strokeW := float32(1.25)
			if fl.Op != "write" {
				ringAlpha = float32(alpha * 0.78)
				strokeW = 1.65
			}
			col := ui.RGBA(r, g, bl, ringAlpha)
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
				p.StrokePath(&a.Canvas.overlayPath, strokeW, col)
			} else {
				p.Stroke(fr, col, 4, strokeW)
				if fl.Op != "write" {
					p.Fill(sr, ui.RGBA(r, g, bl, float32(alpha*0.04)), 3)
				}
			}
		}
		a.Store.Flashes = kept
	}

	// 3. Agent cursors (physics & target arrival already stepped in stepAgentChoreography before canvas render!)
	for aid, cur := range a.Store.Cursors {
		if a.Store.CursorFaded(aid, cur, nowMS) {
			delete(a.Store.Cursors, aid)
			continue
		}

		sx, sy := toWin(cur.X, cur.Y)
		op := CursorOpacity(cur, nowMS, a.Store.Running[aid])
		if op <= 0.01 {
			continue
		}
		cr, cg, cb, _, ok := ParseHexRGBA(cur.Profile.Color)
		if !ok {
			cr, cg, cb = 96, 125, 255
		}

		// Tapered spline ribbon trail while cursor glides between elements
		if !reduced && cur.TrailLen > 0 {
			prevTX, prevTY := sx, sy
			for ti := 0; ti < cur.TrailLen; ti++ {
				tx, ty := toWin(cur.Trail[ti][0], cur.Trail[ti][1])
				segFrac := float32(cur.TrailLen-ti) / float32(len(cur.Trail)+1)
				tAlpha := float32(op) * segFrac * 0.44
				w := max(float32(1.2), segFrac*4.8)
				a.Canvas.overlayPath.Reset()
				a.Canvas.overlayPath.MoveTo(prevTX, prevTY).LineTo(tx, ty)
				p.StrokePath(&a.Canvas.overlayPath, w, ui.RGBA(cr, cg, cb, tAlpha))
				prevTX, prevTY = tx, ty
			}
		}

		// Crisp 210ms EaseOutQuint placement ripple when cursor lands on a newly created/updated node
		if !reduced && cur.PlacePulseAt > 0 && nowMS >= cur.PlacePulseAt && nowMS-cur.PlacePulseAt < 210.0 {
			u := (nowMS - cur.PlacePulseAt) / 210.0
			e := EaseOutQuint64(u)
			ringR := float32(5.0 + e*15.0)
			ringAlpha := float32((1.0 - u) * 0.68 * op)
			p.Stroke(ui.Rect{X: sx - ringR, Y: sy - ringR, W: ringR * 2, H: ringR * 2}, ui.RGBA(cr, cg, cb, ringAlpha), ringR, 1.5)
		}

		fillCol := ui.RGBA(cr, cg, cb, float32(op))
		outCol := ui.RGBA(255, 255, 255, float32(op))
		// Sub-pixel float32 GPU translation to (sx, sy), then pivot velocity-coupled bank tilt (±6 deg) around local tip (0, 0)!
		tiltDeg := float32(0)
		if !reduced {
			tiltDeg = float32(max(-6.0, min(6.0, cur.VX*a.Cam.Zoom*0.0075)))
		}
		drawCursorBody := func() {
			ar := ui.Rect{X: -2, Y: -2, W: 20, H: 20}
			if cursorStroke != nil {
				// Subtle dark drop shadow so white cursor border pops crisply against both white canvas and dark artboards
				p.Icon(cursorStroke, ui.Rect{X: -1.1, Y: -0.5, W: 20, H: 20}, ui.RGBA(15, 23, 42, float32(op*0.28)))
			}
			if cursorFill != nil {
				p.Icon(cursorFill, ar, fillCol)
			}
			if cursorStroke != nil {
				p.Icon(cursorStroke, ar, outCol)
			}
			if cur.Profile.Name != "" {
				fgCol := ContrastText(cur.Profile.Color)
				fgCol.A = uint8(math.Round(float64(fgCol.A) * op))
				nameRunes := len([]rune(cur.Profile.Name))
				pillW := float32(max(32, 14+nameRunes*7))
				pillRect := ui.Rect{X: 14, Y: 14, W: pillW, H: 19}
				p.Fill(ui.Rect{X: pillRect.X + 0.5, Y: pillRect.Y + 1.5, W: pillW, H: 19}, ui.RGBA(15, 23, 42, float32(op*0.16)), 6)
				p.Fill(pillRect, fillCol, 6)
				p.Stroke(pillRect, ui.RGBA(15, 23, 42, float32(op*0.12)), 6, 1)
				p.Text(pillRect.X+7, pillRect.Y+3, cur.Profile.Name, 11, fgCol)
			}
		}
		p.Transform(ui.Translation(sx, sy), 0, 0, func() {
			if math.Abs(float64(tiltDeg)) > 0.05 {
				p.Transform(ui.Rotation(tiltDeg), 0, 0, drawCursorBody)
			} else {
				drawCursorBody()
			}
		})
	}

	// 4. AI Pen cursors
	for _, pen := range a.Store.Pens {
		sx, sy := toWin(pen.X, pen.Y)
		p.Transform(ui.Translation(sx, sy), 0, 0, func() {
			p.Icon(IconSVG("pen"), ui.Rect{X: -2, Y: -16, W: 18, H: 18}, C.Presence)
			penPill := ui.Rect{X: 14, Y: -8, W: 48, H: 19}
			p.Fill(penPill, C.Presence, 6)
			p.Text(penPill.X+7, penPill.Y+3, "AI Pen", 11, ui.Hex("#FFFFFF"))
		})
	}

	chatOpen := a.RightTab == "agent"
	hasVisiblePills := false
	for id, pe := range a.Store.Pills {
		if nowMS-pe.At < PillMS && a.Store.PillShown(id, chatOpen) {
			hasVisiblePills = true
			break
		}
	}
	if a.Canvas.hadVisiblePills && !hasVisiblePills {
		a.Canvas.hadVisiblePills = false
		if a.Window != nil && !a.Window.IsDestroyed() {
			a.Window.Update(func() {})
		}
	} else {
		a.Canvas.hadVisiblePills = hasVisiblePills
	}
	animFrame, after := a.Store.FrameSchedule(nowMS, reduced, len(liveNodes), prevMS, chatOpen)
	if !animFrame && after > 0 {
		p.After(after)
	}
	return animFrame
}

func drawRectPerimeterSegment(p *ui.Painter, path *ui.Path, r ui.Rect, startFrac, lenFrac float64, width float32, col ui.Color) {
	w, h := float64(r.W), float64(r.H)
	total := 2 * (w + h)
	if total <= 1 {
		return
	}
	pointAt := func(d float64) (float32, float32) {
		d = math.Mod(d, total)
		if d < 0 {
			d += total
		}
		switch {
		case d < w:
			return r.X + float32(d), r.Y
		case d < w+h:
			return r.X + r.W, r.Y + float32(d-w)
		case d < 2*w+h:
			return r.X + r.W - float32(d-(w+h)), r.Y + r.H
		default:
			return r.X, r.Y + r.H - float32(d-(2*w+h))
		}
	}
	d0 := startFrac * total
	segLen := lenFrac * total
	const steps = 6
	path.Reset()
	x0, y0 := pointAt(d0)
	path.MoveTo(x0, y0)
	for s := 1; s <= steps; s++ {
		ds := d0 + segLen*float64(s)/float64(steps)
		xs, ys := pointAt(ds)
		// Avoid diagonal chord across corners when wrapping past a corner vertex:
		prevD := d0 + segLen*float64(s-1)/float64(steps)
		for _, corner := range [4]float64{w, w + h, 2*w + h, total} {
			pm := math.Mod(prevD, total)
			cm := math.Mod(ds, total)
			if pm < corner && (cm > corner || (corner == total && ds >= total && prevD < total)) {
				cx, cy := pointAt(corner)
				path.LineTo(cx, cy)
			}
		}
		path.LineTo(xs, ys)
	}
	p.StrokePath(path, width, col)
}

func (a *NalarupaApp) buildAgentLayerChildren(c *ui.Context, liveNodes []LiveNode, nowMS float64) {
	reduced := EffectiveReducedMotion(c)

	// 1. Live placeholder labels & rotated/round semantic markers
	for _, ln := range liveNodes {
		sr := a.docRectToScreen(Painted(ln.Box))
		if ln.Placeholder && (ln.Parent == nil || *ln.Parent == "" || *ln.Parent == "document") {
			maxPillW := max(float32(96), sr.W-6)
			ui.Row(c.Key("generating-label-" + ln.ID)).
				Absolute().
				Left(sr.X).
				Top(sr.Y - 28).
				MaxWidth(maxPillW).
				Padding(3, 9).
				Gap(5).
				AlignItems(ui.Center).
				Radius(6).
				Background(C.CanvasGenBg).
				Border(1, ui.RGBA(79, 70, 229, 0.45)).
				Shadow(0, 3, 10, 0, ui.RGBA(15, 23, 42, 0.10)).
				Transition(ChipTransition(reduced)).
				IgnoreInput(true).
				Children(func() {
					ui.Icon(c.Key("gen-spark-"+ln.ID), IconSVG("sparkles")).FontSize(11).TextColor(C.Presence).Shrink(0)
					ui.Text(c, "Generating: "+ln.Name).FontSize(11).FontWeight(500).TextColor(C.CanvasGenText).MaxLines(1).Shrink(1).MinWidth(0)
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

	// 2. Agent cursor accessibility markers (visual badge is painted in drawAgentLayer at locked 120Hz VSync!)
	for aid, cur := range a.Store.Cursors {
		if a.Store.CursorFaded(aid, cur, nowMS) {
			continue
		}
		op := CursorOpacity(cur, nowMS, a.Store.Running[aid])
		if op <= 0.01 {
			continue
		}
		sx, sy := a.docToScreen(cur.X, cur.Y)
		ui.Box(c.Key("agent-cursor-" + aid)).
			Absolute().
			Left(sx + 14).
			Top(sy + 14).
			Size(1, 1).
			Role(ui.RoleGroup).
			Label("Agent cursor: " + cur.Profile.Name).
			IgnoreInput(true)
	}

	// 3. AI Pen cursor accessibility markers (visual badge is painted in drawAgentLayer at VSync)
	for fid, pen := range a.Store.Pens {
		sx, sy := a.docToScreen(pen.X, pen.Y)
		ui.Box(c.Key("ai-pen-" + fid)).
			Absolute().
			Left(sx + 14).
			Top(sy - 8).
			Size(1, 1).
			Role(ui.RoleGroup).
			Label("AI Pen").
			IgnoreInput(true)
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
		if runes := []rune(snippet); len(runes) > 140 {
			snippet = string(runes[:137]) + "…"
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
