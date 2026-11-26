package main

import (
	"encoding/base64"
	"encoding/json"
	"math"
	"math/rand/v2"
	"strings"
	"time"
)

const (
	FadeInMS    = 200.0
	FadeDelayMS = 5000.0
	FadeDurMS   = 1500.0
	GoneMS      = FadeDelayMS + FadeDurMS
	PillMS      = 3000.0
	ActivityMS  = 2000.0

	PresenceHex = "#607dff"
)

// AgentProfile identifies an agent in chat and on canvas.
type AgentProfile struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Color  string `json:"color"`
	Parent string `json:"parent,omitempty"`
}

var FallbackAgentProfile = AgentProfile{
	ID:    "agent",
	Name:  "Agent",
	Color: "#808080",
}

// ReplyAuthor records the backend and model that produced an agent reply.
type ReplyAuthor struct {
	Backend string
	Model   string
}

// TodoItem represents an entry in the agent's work plan.
type TodoItem struct {
	Content    string `json:"content"`
	Status     string `json:"status"`
	ActiveForm string `json:"activeForm,omitempty"`
}

// UsageStats holds token/turn/cost statistics for a completed agent run.
type UsageStats struct {
	Input        int     `json:"input,omitempty"`
	Output       int     `json:"output,omitempty"`
	CacheRead    int     `json:"cacheRead,omitempty"`
	CacheWrite   int     `json:"cacheWrite,omitempty"`
	Turns        int     `json:"turns,omitempty"`
	CostUSD      float64 `json:"costUsd,omitempty"`
	ContextUsed  int     `json:"contextUsed,omitempty"`
	ContextLimit int     `json:"contextLimit,omitempty"`
}

// ClaudeAuth holds Claude Code CLI authentication status.
type ClaudeAuth struct {
	Installed bool   `json:"installed"`
	SignedIn  bool   `json:"signedIn"`
	Method    string `json:"method,omitempty"`
	Account   string `json:"account,omitempty"`
	Plan      string `json:"plan,omitempty"`
}

// ProviderTestEvent reports the outcome of testing a BYOK provider.
type ProviderTestEvent struct {
	ID    string `json:"id"`
	OK    bool   `json:"ok"`
	Model string `json:"model"`
	Reply string `json:"reply,omitempty"`
	Error string `json:"error,omitempty"`
}

// ChatItem is a polymorphic entry in the agent transcript.
type ChatItem struct {
	Kind string // "user","queued","assistant","thinking","tool","usage","note","error","external","permission"
	ID   int

	Agent    AgentProfile
	HasAgent bool
	Header   bool
	By       *ReplyAuthor

	Text      string
	Streaming bool

	StartedAt float64
	EndedAt   *float64

	ToolID  string
	Name    string
	Running bool
	Lines   int
	Input   any
	Output  string
	Images  []string
	Error   bool

	DurationMS int64
	Usage      UsageStats
	Stopped    bool
	Failed     bool

	SignIn bool
	AuthAt int

	ReqID     string
	PermTool  string
	PermInput map[string]any
}

// CursorWaypoint is one target box queued along an agent cursor's sequential creation path.
type CursorWaypoint struct {
	X          float64
	Y          float64
	BoxID      string
	ReleaseIDs []string
	Box        Box
	HasBox     bool
	Op         string
	HoldMS     float64
}

// CursorState tracks an agent's animated canvas cursor.
type CursorState struct {
	Profile        AgentProfile
	X              float64
	Y              float64
	VX             float64
	VY             float64
	TX             float64
	TY             float64
	AnchorX        float64
	AnchorY        float64
	HasAnchor      bool
	AppearAt       float64
	LastActivity   float64
	Wobble         float64
	Phase          float64
	ActiveWP       CursorWaypoint
	HasActiveWP    bool
	ActiveTouched  bool
	ActiveReleased bool
	Waypoints      []CursorWaypoint
	DwellUntil     float64
	NextWaypointAt float64
	PlacePulseAt   float64
	Trail          [7][2]float64
	TrailLen       int
}

// PenCursorState tracks an active AI Pen nib on a frame.
type PenCursorState struct {
	X        float64
	Y        float64
	TX       float64
	TY       float64
	AppearAt float64
}

// NodeFlash represents a highlight ring + scan band on a canvas node touched by an agent.
type NodeFlash struct {
	ID            string
	AgentID       string
	Box           Box
	HasBox        bool
	Color         string
	Op            string
	StartAt       float64
	PaintStartAt  float64
	HoldMS        float64
	MinVer        uint64
	MinReleaseSeq uint64
}

// ActivityReq is an unconsumed agent_activity event awaiting box resolution on Canvas.
type ActivityReq struct {
	Agent string
	IDs   []string
	Op    string
	At    float64
}

// PillEntry holds the latest streaming assistant snippet for bottom-center canvas pills.
type PillEntry struct {
	Text string
	At   float64
}

// AgentStore manages all reactive agent transcript, plan, auth, and canvas presence state.
type AgentStore struct {
	nextID         int
	Items          []ChatItem
	Todos          []TodoItem
	TodosOwner     string
	Profiles       map[string]AgentProfile
	ProfileOrder   []string
	Running        map[string]bool
	Authors        map[string]ReplyAuthor
	Cursors        map[string]*CursorState
	Pens           map[string]*PenCursorState
	Flashes        []NodeFlash
	Pills          map[string]PillEntry
	Activity       []ActivityReq
	ViewCenter     Pt
	ViewZoom       float64
	ClaudeAuth         *ClaudeAuth
	ClaudeAuthAt       int
	ProviderTest       *ProviderTestEvent
	ProviderTestAt     int
	ContextUsed        int
	ContextLimit       int
	ContextStreamExtra int
	contextStreamBytes int
}

// NewAgentStore constructs an initialized AgentStore.
func NewAgentStore() *AgentStore {
	s := &AgentStore{nextID: 1}
	s.ResetChat()
	s.ClearPresence()
	return s
}

func (s *AgentStore) allocID() int {
	id := s.nextID
	s.nextID++
	return id
}

// ProfileOf returns the recorded profile for id, external fallback, or FallbackAgentProfile.
func (s *AgentStore) ProfileOf(id string) AgentProfile {
	if p, ok := s.Profiles[id]; ok {
		return p
	}
	if id == "external" {
		return AgentProfile{ID: id, Name: "External agent", Color: PresenceHex}
	}
	return FallbackAgentProfile
}

func (s *AgentStore) putProfile(p AgentProfile) {
	if _, exists := s.Profiles[p.ID]; !exists {
		s.ProfileOrder = append(s.ProfileOrder, p.ID)
	}
	s.Profiles[p.ID] = p
}

// PillShown reports whether agent id's message pill should show on the canvas.
func (s *AgentStore) PillShown(id string, chatOpen bool) bool {
	return !(chatOpen && s.ProfileOf(id).Parent == "")
}

// NeedsFrame reports whether AgentLayer must schedule another animation frame.
func (s *AgentStore) NeedsFrame(now float64, reduced bool, liveCount int, painted float64, chatOpen bool) bool {
	pills := false
	for id, p := range s.Pills {
		if !s.PillShown(id, chatOpen) {
			continue
		}
		if min(now, painted)-p.At < PillMS {
			pills = true
			break
		}
	}
	if len(s.Activity) > 0 || len(s.Flashes) > 0 || pills {
		return true
	}
	if !reduced {
		if liveCount > 0 || len(s.Pens) > 0 {
			return true
		}
		for _, c := range s.Cursors {
			if s.Running[c.Profile.ID] || c.HasActiveWP || len(c.Waypoints) > 0 || now-c.LastActivity < GoneMS {
				return true
			}
		}
		return false
	}
	for _, p := range s.Pens {
		if now-p.AppearAt < FadeInMS {
			return true
		}
	}
	for _, c := range s.Cursors {
		idle := now - c.LastActivity
		if now-c.AppearAt < FadeInMS || (!s.Running[c.Profile.ID] && idle > FadeDelayMS && idle < GoneMS) {
			return true
		}
	}
	return false
}

// FrameSchedule locks all active cursor motion (including pre-design scouting, waypoint traversal, and fade-out),
// live node glows/shimmers, AI pens, and node flashes to hardware VSync (Painter.AnimationFrame, which repaints
// the GPU canvas layer without rebuilding View(c)) for zero timer-beat stutter.
func (s *AgentStore) FrameSchedule(now float64, reduced bool, liveCount int, painted float64, chatOpen bool) (animFrame bool, after time.Duration) {
	if reduced {
		if s.NeedsFrame(now, true, liveCount, painted, chatOpen) {
			return false, 33 * time.Millisecond
		}
		return false, 0
	}
	if len(s.Flashes) > 0 || len(s.Activity) > 0 || liveCount > 0 || len(s.Pens) > 0 {
		return true, 0
	}
	for _, c := range s.Cursors {
		if s.Running[c.Profile.ID] || c.HasActiveWP || len(c.Waypoints) > 0 || now-c.LastActivity < GoneMS {
			return true, 0
		}
	}
	hasPillAnim := false
	hasActivePill := false
	nextWakeMS := 80.0
	for id, p := range s.Pills {
		if !s.PillShown(id, chatOpen) {
			continue
		}
		elapsed := min(now, painted) - p.At
		if elapsed < PillMS {
			hasActivePill = true
			if elapsed < FadeInMS || elapsed > PillMS-FadeDurMS {
				hasPillAnim = true
				break
			}
			toFade := (PillMS - FadeDurMS) - elapsed
			if toFade > 0 && toFade < nextWakeMS {
				nextWakeMS = toFade
			}
		}
	}
	if hasPillAnim {
		return true, 0
	}
	if hasActivePill {
		return false, time.Duration(max(5.0, min(80.0, nextWakeMS+2.0))) * time.Millisecond
	}
	return false, 0
}

// EaseOutCubic evaluates 1 - (1 - t)^3 clamped to [0, 1].
func EaseOutCubic(t float64) float64 {
	t = max(0, min(1, t))
	u := 1 - t
	return 1 - u*u*u
}

// CursorFaded reports whether agent's cursor has completed its idle fade-out.
func (s *AgentStore) CursorFaded(agentID string, c *CursorState, now float64) bool {
	if c == nil {
		return true
	}
	if s.Running[agentID] || c.HasActiveWP || len(c.Waypoints) > 0 {
		return false
	}
	return now-c.LastActivity > GoneMS
}

// CursorOpacity computes c's current opacity in [0, 1].
func CursorOpacity(c *CursorState, now float64, running bool) float64 {
	if c == nil {
		return 0
	}
	sa := now - c.AppearAt
	si := 0.0
	if !running && !c.HasActiveWP && len(c.Waypoints) == 0 {
		si = now - c.LastActivity
	}
	if sa < FadeInMS {
		return EaseOutCubic(sa / FadeInMS)
	}
	if si > FadeDelayMS {
		return max(0, 1-EaseOutCubic((si-FadeDelayMS)/FadeDurMS))
	}
	return 1
}

// WakeCursor refreshes c's activity timestamp, smoothly continuing from partial fade-out opacity.
func (s *AgentStore) WakeCursor(agentID string, c *CursorState, now float64) {
	o := CursorOpacity(c, now, s.Running[agentID])
	if o < 1 {
		c.AppearAt = now - FadeInMS*(1-math.Cbrt(1-o))
	}
	c.LastActivity = now
}

// Poke wakes or spawns an agent cursor near ViewCenter with a smooth diagonal entrance glide.
func (s *AgentStore) Poke(p AgentProfile, now float64) {
	if c, ok := s.Cursors[p.ID]; ok {
		s.WakeCursor(p.ID, c, now)
		c.Profile = p
		return
	}
	phase := rand.Float64() * math.Pi * 2
	ax := s.ViewCenter[0] + math.Cos(phase)*44.0
	ay := s.ViewCenter[1] + math.Sin(phase)*28.0
	s.Cursors[p.ID] = &CursorState{
		Profile:      p,
		X:            ax - 96.0,
		Y:            ay + 64.0,
		TX:           ax,
		TY:           ay,
		AnchorX:      ax,
		AnchorY:      ay,
		HasAnchor:    true,
		AppearAt:     now,
		LastActivity: now,
		Phase:        phase,
	}
}

// PokeFormation spawns or wakes member idx of a parallel team in a clean horizontal formation around ViewCenter.
func (s *AgentStore) PokeFormation(p AgentProfile, idx, total int, now float64) {
	total = max(1, total)
	z := s.ViewZoom
	if z <= 0 {
		z = 0.5
	}
	effZ := max(0.04, z)
	spacing := max(320.0, 140.0/effZ)
	rowShift := max(36.0, 28.0/effZ)
	offsetX := (float64(idx) - float64(total-1)*0.5) * spacing
	offsetY := float64((idx%2)*2-1) * rowShift
	phase := float64(idx) * (math.Pi * 0.55)
	ax := s.ViewCenter[0] + offsetX
	ay := s.ViewCenter[1] + offsetY
	if c, ok := s.Cursors[p.ID]; ok {
		s.WakeCursor(p.ID, c, now)
		c.Profile = p
		c.AnchorX = ax
		c.AnchorY = ay
		c.HasAnchor = true
		c.TX = ax
		c.TY = ay
		c.Phase = phase
		return
	}
	s.Cursors[p.ID] = &CursorState{
		Profile:      p,
		X:            ax - max(72.0, 56.0/effZ),
		Y:            ay + max(54.0, 42.0/effZ),
		TX:           ax,
		TY:           ay,
		AnchorX:      ax,
		AnchorY:      ay,
		HasAnchor:    true,
		AppearAt:     now,
		LastActivity: now,
		Phase:        phase,
	}
}

// SetClaudeAuth updates Claude Code auth status and bumps SignIn error items.
func (s *AgentStore) SetClaudeAuth(st ClaudeAuth) {
	cp := st
	s.ClaudeAuth = &cp
	s.ClaudeAuthAt++
	for i := range s.Items {
		if s.Items[i].Kind == "error" && s.Items[i].SignIn {
			s.Items[i].AuthAt = s.ClaudeAuthAt
		}
	}
}

func (s *AgentStore) lastOf(agentID string, kinds ...string) *ChatItem {
	for i := len(s.Items) - 1; i >= 0; i-- {
		it := &s.Items[i]
		if !it.HasAgent {
			continue
		}
		if it.Agent.ID != agentID {
			continue
		}
		for _, k := range kinds {
			if it.Kind == k {
				return it
			}
		}
		return nil
	}
	return nil
}

func (s *AgentStore) opensReply(agentID string) bool {
	for i := len(s.Items) - 1; i >= 0; i-- {
		it := &s.Items[i]
		if it.Kind == "note" || it.Kind == "queued" {
			continue
		}
		if it.Kind == "user" || it.Kind == "usage" || !it.HasAgent || it.Agent.ID != agentID {
			return true
		}
		return false
	}
	return true
}

func (s *AgentStore) endStreaming(agentID string, now float64) {
	for i := range s.Items {
		it := &s.Items[i]
		if it.Kind == "assistant" && it.Agent.ID == agentID {
			it.Streaming = false
		}
		if it.Kind == "thinking" && it.Agent.ID == agentID && it.EndedAt == nil {
			end := now
			it.EndedAt = &end
		}
	}
}

func (s *AgentStore) failedRun(agentID string) bool {
	for i := len(s.Items) - 1; i >= 0; i-- {
		it := &s.Items[i]
		if it.Kind == "usage" && it.Agent.ID == agentID {
			return false
		}
		if it.Kind == "error" {
			owner := "agent"
			if it.HasAgent && it.Agent.ID != "" {
				owner = it.Agent.ID
			}
			if owner == agentID {
				return true
			}
		}
	}
	return false
}

// ApplyEvents folds core events into the store at timestamp now (ms), returning true if anything changed.
func (s *AgentStore) ApplyEvents(events []map[string]any, now float64) bool {
	changed := false
	for _, ev := range events {
		evType, _ := ev["type"].(string)
		switch evType {
		case "agent_team":
			if rawMembers, ok := ev["members"].([]any); ok && len(rawMembers) > 0 {
				members := make([]AgentProfile, 0, len(rawMembers))
				for _, rm := range rawMembers {
					var mp AgentProfile
					redecode(rm, &mp)
					if mp.ID != "" {
						s.putProfile(mp)
						members = append(members, mp)
					}
				}
				for idx, mp := range members {
					s.Running[mp.ID] = true
					s.PokeFormation(mp, idx, len(members), now)
				}
				changed = true
			}
			continue
		case "agent_context":
			if u := int(asFloat(ev["used"])); u >= 0 {
				s.ContextUsed = u
				s.ContextStreamExtra = 0
				s.contextStreamBytes = 0
			}
			if l := int(asFloat(ev["limit"])); l > 0 {
				s.ContextLimit = l
			}
			changed = true
			continue
		case "agent_activity":
			s.pruneStaleActivity(now)
			aid, _ := ev["agent"].(string)
			op, _ := ev["op"].(string)
			var ids []string
			if rawIDs, ok := ev["ids"].([]any); ok {
				for _, x := range rawIDs {
					if str, ok := x.(string); ok {
						ids = append(ids, str)
					}
				}
			}
			s.Activity = append(s.Activity, ActivityReq{Agent: aid, IDs: ids, Op: op, At: now})
			changed = true
			continue
		case "pen":
			id, _ := ev["id"].(string)
			x := asFloat(ev["x"])
			y := asFloat(ev["y"])
			if pen, ok := s.Pens[id]; ok {
				pen.TX, pen.TY = x, y
			} else {
				s.Pens[id] = &PenCursorState{X: x, Y: y, TX: x, TY: y, AppearAt: now}
			}
			changed = true
			continue
		case "pen_end":
			id, _ := ev["id"].(string)
			delete(s.Pens, id)
			changed = true
			continue
		case "provider_test":
			var pt ProviderTestEvent
			redecode(ev, &pt)
			s.ProviderTest = &pt
			s.ProviderTestAt++
			changed = true
			continue
		case "claude_auth":
			var st ClaudeAuth
			if sub, ok := ev["state"]; ok {
				redecode(sub, &st)
			}
			s.SetClaudeAuth(st)
			changed = true
			continue
		}
		if evType != "agent" {
			continue
		}
		changed = true
		var prof *AgentProfile
		if rawAgent, ok := ev["agent"]; ok && rawAgent != nil {
			var p AgentProfile
			redecode(rawAgent, &p)
			if p.ID != "" {
				prof = &p
				s.putProfile(p)
			}
		}
		aid := "agent"
		pVal := FallbackAgentProfile
		if prof != nil {
			aid = prof.ID
			pVal = *prof
		}
		kind, _ := ev["kind"].(string)
		switch kind {
		case "user":
			txt, _ := ev["text"].(string)
			s.Items = append(s.Items, ChatItem{Kind: "user", ID: s.allocID(), Text: txt})
			var queued []string
			for _, it := range s.Items {
				if it.Kind == "queued" {
					queued = append(queued, it.Text)
				}
			}
			if len(queued) > 0 && txt == strings.Join(queued, "\n\n") {
				s.filterOutKind("queued")
			}
		case "queued":
			txt, _ := ev["text"].(string)
			s.Items = append(s.Items, ChatItem{Kind: "queued", ID: s.allocID(), Text: txt})
		case "dequeued":
			s.filterOutKind("queued")
			s.Items = append(s.Items, ChatItem{Kind: "note", ID: s.allocID(), Text: "Queued message discarded."})
		case "start":
			if prof != nil {
				s.Poke(*prof, now)
			}
			s.Running[aid] = true
			if backend, ok := ev["backend"].(string); ok {
				model, _ := ev["model"].(string)
				s.Authors[aid] = ReplyAuthor{Backend: backend, Model: model}
			} else {
				delete(s.Authors, aid)
			}
		case "text_delta":
			txt, _ := ev["text"].(string)
			if pVal.Parent == "" && len(txt) > 0 {
				s.contextStreamBytes += len(txt)
				s.ContextStreamExtra = max(1, int(math.Ceil(float64(s.contextStreamBytes)/3.6)))
			}
			last := s.lastOf(aid, "assistant", "thinking", "tool")
			if last != nil && last.Kind == "assistant" && last.Streaming {
				last.Text += txt
			} else if strings.TrimSpace(txt) != "" {
				s.endStreaming(aid, now)
				var byPtr *ReplyAuthor
				if au, ok := s.Authors[aid]; ok {
					cp := au
					byPtr = &cp
				}
				hdr := s.opensReply(aid)
				s.Items = append(s.Items, ChatItem{
					Kind:      "assistant",
					ID:        s.allocID(),
					Agent:     pVal,
					HasAgent:  true,
					Text:      txt,
					Streaming: true,
					Header:    hdr,
					By:        byPtr,
				})
			}
			if cur := s.lastOf(aid, "assistant"); cur != nil {
				s.Pills[aid] = PillEntry{Text: cur.Text, At: now}
			}
		case "thinking_delta":
			txt, _ := ev["text"].(string)
			if pVal.Parent == "" && len(txt) > 0 {
				s.contextStreamBytes += len(txt)
				s.ContextStreamExtra = max(1, int(math.Ceil(float64(s.contextStreamBytes)/3.6)))
			}
			last := s.lastOf(aid, "thinking", "assistant", "tool")
			if last != nil && last.Kind == "thinking" && last.EndedAt == nil {
				last.Text += txt
			} else if strings.TrimSpace(txt) != "" {
				s.endStreaming(aid, now)
				var byPtr *ReplyAuthor
				if au, ok := s.Authors[aid]; ok {
					cp := au
					byPtr = &cp
				}
				hdr := s.opensReply(aid)
				s.Items = append(s.Items, ChatItem{
					Kind:      "thinking",
					ID:        s.allocID(),
					Agent:     pVal,
					HasAgent:  true,
					Text:      txt,
					StartedAt: now,
					Header:    hdr,
					By:        byPtr,
				})
				if prof != nil {
					s.Poke(*prof, now)
				}
			}
		case "tool_start":
			s.endStreaming(aid, now)
			tid, _ := ev["id"].(string)
			tname, _ := ev["name"].(string)
			exists := false
			for i := range s.Items {
				if s.Items[i].Kind == "tool" && s.Items[i].ToolID == tid {
					exists = true
					break
				}
			}
			if !exists {
				var byPtr *ReplyAuthor
				if au, ok := s.Authors[aid]; ok {
					cp := au
					byPtr = &cp
				}
				hdr := s.opensReply(aid)
				s.Items = append(s.Items, ChatItem{
					Kind:     "tool",
					ID:       s.allocID(),
					ToolID:   tid,
					Agent:    pVal,
					HasAgent: true,
					Name:     tname,
					Running:  true,
					Header:   hdr,
					By:       byPtr,
				})
			}
			if prof != nil {
				s.Poke(*prof, now)
			}
		case "tool_progress":
			tid, _ := ev["id"].(string)
			lines := int(asFloat(ev["lines"]))
			for i := range s.Items {
				if s.Items[i].Kind == "tool" && s.Items[i].ToolID == tid {
					s.Items[i].Lines = lines
					break
				}
			}
		case "tool_done":
			tid, _ := ev["id"].(string)
			tname, _ := ev["name"].(string)
			idx := -1
			for i := range s.Items {
				if s.Items[i].Kind == "tool" && s.Items[i].ToolID == tid {
					idx = i
					break
				}
			}
			if idx < 0 {
				var byPtr *ReplyAuthor
				if au, ok := s.Authors[aid]; ok {
					cp := au
					byPtr = &cp
				}
				hdr := s.opensReply(aid)
				s.Items = append(s.Items, ChatItem{
					Kind:     "tool",
					ID:       s.allocID(),
					ToolID:   tid,
					Agent:    pVal,
					HasAgent: true,
					Name:     tname,
					Running:  false,
					Header:   hdr,
					By:       byPtr,
				})
				idx = len(s.Items) - 1
			}
			it := &s.Items[idx]
			it.Running = false
			if tname != "" {
				it.Name = tname
			}
			it.Input = ev["input"]
			it.Output, _ = ev["output"].(string)
			it.Error, _ = ev["error"].(bool)
			it.Images = nil
			if imgs, ok := ev["images"].([]any); ok {
				for _, im := range imgs {
					if str, ok := im.(string); ok {
						it.Images = append(it.Images, str)
					}
				}
			}
		case "context":
			if u := int(asFloat(ev["used"])); u >= 0 {
				s.ContextUsed = u
				s.ContextStreamExtra = 0
				s.contextStreamBytes = 0
			}
			if l := int(asFloat(ev["limit"])); l > 0 {
				s.ContextLimit = l
			}
		case "message_end":
			s.endStreaming(aid, now)
		case "todos":
			var list []TodoItem
			if raw, ok := ev["todos"]; ok {
				redecode(raw, &list)
			}
			s.Todos = list
			s.TodosOwner = aid
		case "note":
			txt, _ := ev["text"].(string)
			s.Items = append(s.Items, ChatItem{Kind: "note", ID: s.allocID(), Text: txt})
		case "error":
			txt, _ := ev["text"].(string)
			signIn, _ := ev["signIn"].(bool)
			it := ChatItem{
				Kind:   "error",
				ID:     s.allocID(),
				Text:   txt,
				SignIn: signIn,
				AuthAt: s.ClaudeAuthAt,
			}
			if prof != nil {
				it.Agent = *prof
				it.HasAgent = true
			}
			s.Items = append(s.Items, it)
		case "external":
			txt, _ := ev["text"].(string)
			isErr, _ := ev["error"].(bool)
			s.Items = append(s.Items, ChatItem{Kind: "external", ID: s.allocID(), Text: txt, Error: isErr})
		case "permission":
			reqID, _ := ev["id"].(string)
			toolName, _ := ev["tool"].(string)
			inMap, _ := ev["input"].(map[string]any)
			if inMap == nil {
				inMap = map[string]any{}
			}
			s.Items = append(s.Items, ChatItem{
				Kind:      "permission",
				ID:        s.allocID(),
				ReqID:     reqID,
				Agent:     pVal,
				HasAgent:  true,
				PermTool:  toolName,
				PermInput: inMap,
			})
			if prof != nil {
				s.Poke(*prof, now)
			}
		case "permission_done":
			reqID, _ := ev["id"].(string)
			kept := s.Items[:0]
			for _, it := range s.Items {
				if it.Kind == "permission" && it.ReqID == reqID {
					continue
				}
				kept = append(kept, it)
			}
			s.Items = kept
		case "done":
			s.endStreaming(aid, now)
			delete(s.Running, aid)
			for i := range s.Items {
				it := &s.Items[i]
				if it.Kind == "tool" && it.Agent.ID == aid && it.Running {
					it.Running = false
					it.Error = true
					it.Output = "Tool call cancelled"
				}
			}
			stopped, _ := ev["stopped"].(bool)
			failed := !stopped && s.failedRun(aid)
			if s.TodosOwner == aid && (stopped || failed) {
				for i := range s.Todos {
					if s.Todos[i].Status != "completed" {
						s.Todos[i].Status = "cancelled"
					}
				}
			}
			var usage UsageStats
			if u, ok := ev["usage"]; ok {
				redecode(u, &usage)
			}
			if pVal.Parent == "" {
				if usage.ContextUsed > 0 {
					s.ContextUsed = usage.ContextUsed
				}
				s.ContextStreamExtra = 0
				s.contextStreamBytes = 0
				if usage.ContextLimit > 0 {
					s.ContextLimit = usage.ContextLimit
				}
			}
			dur := int64(asFloat(ev["durationMs"]))
			s.Items = append(s.Items, ChatItem{
				Kind:       "usage",
				ID:         s.allocID(),
				Agent:      pVal,
				HasAgent:   true,
				DurationMS: dur,
				Usage:      usage,
				Stopped:    stopped,
				Failed:     failed,
			})
			if c, ok := s.Cursors[aid]; ok {
				c.LastActivity = now
			}
			delete(s.Pills, aid)
		case "all_done":
			for id := range s.Running {
				delete(s.Running, id)
				if c, ok := s.Cursors[id]; ok {
					c.LastActivity = now
				}
			}
		}
	}
	return changed
}

func (s *AgentStore) filterOutKind(kind string) {
	kept := s.Items[:0]
	for _, it := range s.Items {
		if it.Kind != kind {
			kept = append(kept, it)
		}
	}
	s.Items = kept
}

func (s *AgentStore) pruneStaleActivity(now float64) {
	kept := s.Activity[:0]
	for _, a := range s.Activity {
		if now-a.At < ActivityMS {
			kept = append(kept, a)
		}
	}
	s.Activity = kept
}

// RunVerb formats the closing usage line verb ("designed for", "stopped after", "failed after").
func RunVerb(stopped, failed bool) string {
	switch {
	case stopped:
		return "stopped after"
	case failed:
		return "failed after"
	default:
		return "designed for"
	}
}

// ResetChat clears transcript, plan, profiles, running flags, authors, pills, and canvas presence.
func (s *AgentStore) ResetChat() {
	s.Items = nil
	s.Todos = nil
	s.TodosOwner = ""
	s.Pills = map[string]PillEntry{}
	s.Profiles = map[string]AgentProfile{}
	s.ProfileOrder = nil
	s.Running = map[string]bool{}
	s.Authors = map[string]ReplyAuthor{}
	s.ContextUsed = 0
	s.ContextLimit = 0
	s.ContextStreamExtra = 0
	s.contextStreamBytes = 0
	s.ClearPresence()
}

// ClearPresence resets canvas-coordinate agent cursors, AI Pens, flashes, and pending activity.
func (s *AgentStore) ClearPresence() {
	s.Cursors = map[string]*CursorState{}
	s.Pens = map[string]*PenCursorState{}
	s.Flashes = nil
	s.Activity = nil
}

// TakeActivity drains fresh activity requests (< ActivityMS old) and discards stale ones.
func (s *AgentStore) TakeActivity(now float64) []ActivityReq {
	var fresh []ActivityReq
	for _, a := range s.Activity {
		if now-a.At < ActivityMS {
			fresh = append(fresh, a)
		}
	}
	s.Activity = nil
	return fresh
}

// LastContentID returns the newest non-usage, non-note item ID (or 0 if none).
func LastContentID(items []ChatItem) int {
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].Kind != "usage" && items[i].Kind != "note" {
			return items[i].ID
		}
	}
	return 0
}

// ItemSpinning reports whether a chat item displays an animated ellipsis.
func ItemSpinning(it ChatItem) bool {
	return (it.Kind == "tool" && it.Running) || (it.Kind == "thinking" && it.EndedAt == nil)
}

// RowTickMS returns the row re-render interval in ms (0 under reduced motion or when static).
func RowTickMS(typing, spin, reduced bool, indicatorMS, typingMS int) int {
	if typingMS <= 0 {
		typingMS = 16
	}
	if reduced {
		return 0
	}
	if typing {
		return typingMS
	}
	if spin {
		return indicatorMS
	}
	return 0
}

// ItemChanging reports whether a chat item mutates in place during streaming.
func ItemChanging(it ChatItem) bool {
	return ItemSpinning(it) || (it.Kind == "assistant" && it.Streaming)
}

// BarAgentsResult holds multi-agent bar summary lists.
type BarAgentsResult struct {
	All     []AgentProfile
	Running []AgentProfile
	Shown   []AgentProfile
	More    int
}

// BarAgents orders running agents first, then finished agents, capped at maxShown.
func (s *AgentStore) BarAgents(maxShown int) BarAgentsResult {
	if maxShown <= 0 {
		maxShown = 8
	}
	var all, running, idle []AgentProfile
	for _, id := range s.ProfileOrder {
		p := s.Profiles[id]
		all = append(all, p)
		if s.Running[id] {
			running = append(running, p)
		} else {
			idle = append(idle, p)
		}
	}
	ordered := append(append([]AgentProfile{}, running...), idle...)
	shown := ordered
	more := 0
	if len(ordered) > maxShown {
		shown = ordered[:maxShown]
		more = len(ordered) - maxShown
	}
	return BarAgentsResult{All: all, Running: running, Shown: shown, More: more}
}

// RestoreChat replays persisted session events and settles any incomplete mid-crash turn.
func (s *AgentStore) RestoreChat(events []map[string]any) {
	s.ResetChat()
	for _, ev := range events {
		at := asFloat(ev["at"])
		s.ApplyEvents([]map[string]any{ev}, at)
	}
	s.filterOutKind("queued")
	for i := range s.Items {
		it := &s.Items[i]
		switch it.Kind {
		case "assistant":
			it.Streaming = false
		case "thinking":
			if it.EndedAt == nil {
				end := it.StartedAt
				it.EndedAt = &end
			}
		case "tool":
			if it.Running {
				it.Running = false
				it.Error = true
				it.Output = "Tool call cancelled"
			}
		}
	}
	ownedBy := func(ev map[string]any, kind string) bool {
		if t, _ := ev["type"].(string); t != "agent" {
			return false
		}
		if k, _ := ev["kind"].(string); k != kind {
			return false
		}
		owner := "agent"
		if rawA, ok := ev["agent"].(map[string]any); ok {
			if id, ok := rawA["id"].(string); ok && id != "" {
				owner = id
			}
		}
		return owner == s.TodosOwner
	}
	lastPlan := -1
	for i, ev := range events {
		if ownedBy(ev, "todos") {
			lastPlan = i
		}
	}
	planDone := false
	if lastPlan >= 0 {
		for _, ev := range events[lastPlan+1:] {
			if ownedBy(ev, "done") {
				planDone = true
				break
			}
		}
	}
	if lastPlan >= 0 && !planDone && s.Running[s.TodosOwner] {
		for i := range s.Todos {
			if s.Todos[i].Status != "completed" {
				s.Todos[i].Status = "cancelled"
			}
		}
	}
	s.Running = map[string]bool{}
	s.Cursors = map[string]*CursorState{}
	s.Pills = map[string]PillEntry{}
}

// ImageDataURI sniffs base64 image prefix (PNG, JPEG, WebP, GIF) and returns its data: URI.
func ImageDataURI(b64 string) string {
	mime := "image/png"
	switch {
	case strings.HasPrefix(b64, "/9j/"):
		mime = "image/jpeg"
	case strings.HasPrefix(b64, "UklGR"):
		mime = "image/webp"
	case strings.HasPrefix(b64, "R0lGOD"):
		mime = "image/gif"
	}
	return "data:" + mime + ";base64," + b64
}

// DecodeBase64Image decodes base64 PNG/JPEG/WebP/GIF bytes for ui.DecodeBitmap.
func DecodeBase64Image(b64 string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
}

func asFloat(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case float32:
		return float64(x)
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case json.Number:
		f, _ := x.Float64()
		return f
	default:
		return 0
	}
}

func redecode(in any, out any) {
	b, err := json.Marshal(in)
	if err != nil {
		return
	}
	_ = json.Unmarshal(b, out)
}
