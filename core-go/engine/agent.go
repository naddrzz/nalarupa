package engine

// The built-in agent's state as the engine holds it (agent.rs `AgentState`, `Profile` and `Chat`, with the methods
// that only touch that state). The run loop, chats on disk and Claude Code are in package agent, which reaches the
// engine through these fields and AgentHooks.

import (
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

// Flag is a cancel flag (Rust's `Arc<AtomicBool>`): once set it stays set, and Done is closed.
type Flag struct {
	set  atomic.Bool
	once sync.Once
	done chan struct{}
}

// NewFlag is a flag not yet set.
func NewFlag() *Flag { return &Flag{done: make(chan struct{})} }

// Set sets the flag.
func (f *Flag) Set() {
	f.set.Store(true)
	f.once.Do(func() { close(f.done) })
}

// IsSet reports whether the flag is set.
func (f *Flag) IsSet() bool { return f.set.Load() }

// Done is closed once the flag is set.
func (f *Flag) Done() <-chan struct{} { return f.done }

// Pen is an AI Pen still drawing into Frame. Its Cancel is its own: the pen can outlive its run, whose flag the
// next turn replaces, and Stop still ends it.
type Pen struct {
	Frame  string
	Cancel *Flag
}

// Profile is an agent's identity (agent.rs `Profile`): the canvas cursor and the chat show its name and color.
// Parent is the main agent's id for a spawned designer, "" for a main agent.
type Profile struct {
	ID, Name, Color, Parent string
}

// JSON is `{id, name, color, parent}`, parent null for a main agent.
func (p Profile) JSON() *jsonv.Object {
	var parent jsonv.Value
	if p.Parent != "" {
		parent = p.Parent
	}
	return Object("id", p.ID, "name", p.Name, "color", p.Color, "parent", parent)
}

// Chat is the conversation being saved to `<data_dir>/sessions/<ID>.json`.
type Chat struct {
	ID string
	// Title is the first prompt, cut to 100 characters.
	Title string
	// Events are the agent events as the UI folds them (deltas merged, progress dropped); the UI replays them on
	// load.
	Events []jsonv.Value
}

// Permission is a permission request waiting for the user's answer: the asking agent, and where the answer goes
// (buffered, so answering never blocks).
type Permission struct {
	Agent  string
	Answer chan bool
}

// QueueOpts are the options of the latest queued prompt, as `agent_send` takes them.
type QueueOpts struct {
	Parallel uint64
	Mode     string
	Designs  uint64
	Iterate  string
}

// AgentState is the built-in agent's state (agent.rs `AgentState`). Empty strings stand for Rust's `None`.
type AgentState struct {
	// Messages is the Claude API history of the main conversation.
	Messages []jsonv.Value
	// Session is the Claude Code session of the main conversation.
	Session string
	// Running: an agent (main or child) is working.
	Running bool
	// Active counts the run goroutines.
	Active int
	// Cancel is the current turn's Stop flag; each turn gets a fresh one.
	Cancel   *Flag
	Profiles []Profile
	Main     string
	// Queue holds prompts sent while agents were busy; they are sent as one message afterwards.
	Queue     []string
	QueueOpts QueueOpts
	// Parallel and Mode are the parallel agent setting of the current turn: a count and "split" | "side".
	Parallel int
	Mode     string
	// Spawned counts the children started in the current turn; Parallel - 1 is all a turn may start.
	Spawned int
	// Chat is the conversation being saved; nil until the first prompt.
	Chat *Chat
	// Permissions are the requests waiting for the user's answer, by request id.
	Permissions map[string]Permission
	// Cook and Iterate are "Let it cook": designs left in this run (counting the current one) and "layout" |
	// "style".
	Cook    int
	Iterate string
	// Pens are the AI Pens still drawing; their frames' `placeholder` flag is theirs to clear.
	Pens []Pen
	// Flagged are the frames the built-in agents flagged `placeholder` in this run: the only ones the run's end
	// clears. An external agent's frames are its own to clear.
	Flagged []string
	// Stops counts the user's Stops: a Stop while a call's script runs still reaches the pens it starts.
	Stops uint64
}

// PenFrames are the frames the pens draw into.
func (a *AgentState) PenFrames() []string {
	out := make([]string, len(a.Pens))
	for i, p := range a.Pens {
		out[i] = p.Frame
	}
	return out
}

// Profile is the agent with this id.
func (a *AgentState) Profile(id string) (Profile, bool) {
	i := slices.IndexFunc(a.Profiles, func(p Profile) bool { return p.ID == id })
	if i < 0 {
		return Profile{}, false
	}
	return a.Profiles[i], true
}

// Record keeps an agent event of the current chat. True when it ends a message or a turn: time to save.
func (a *AgentState) Record(ev jsonv.Value) bool {
	if a.Chat == nil {
		return false
	}
	kind := jsonv.StrOr(jsonv.Get(ev, "kind"))
	switch kind {
	case "tool_progress", "all_done", "permission", "permission_done":
		// Permission cards only matter while they wait.
		return false
	case "text_delta", "thinking_delta":
		id := jsonv.Get(jsonv.Get(ev, "agent"), "id")
		for i := len(a.Chat.Events) - 1; i >= 0; i-- {
			last := a.Chat.Events[i]
			if !jsonv.Equal(jsonv.Get(jsonv.Get(last, "agent"), "id"), id) {
				continue
			}
			if jsonv.StrIs(jsonv.Get(last, "kind"), kind) {
				setKey(last, "text", jsonv.StrOr(jsonv.Get(last, "text"))+jsonv.StrOr(jsonv.Get(ev, "text")))
				return false
			}
			break
		}
	}
	c := setKey(jsonv.Clone(ev), "at", u64(NowMs()))
	a.Chat.Events = append(a.Chat.Events, c)
	return kind == "user" || kind == "message_end" || kind == "done" || kind == "dequeued"
}

// setKey is serde's `v[k] = x`: null becomes an object first; any other non-object panics, as serde's IndexMut
// does.
func setKey(v jsonv.Value, k string, x jsonv.Value) jsonv.Value {
	if v == nil {
		v = jsonv.NewObject()
	}
	o, ok := v.(*jsonv.Object)
	if !ok {
		panic(fmt.Errorf("cannot access key %s in a JSON non-object", k))
	}
	o.Set(k, x)
	return o
}

// NowMs is the time in milliseconds since the epoch.
func NowMs() uint64 {
	ms := time.Now().UnixMilli()
	if ms < 0 {
		return 0
	}
	return uint64(ms)
}

// Clear starts over with no conversation (the old one stays saved).
func (a *AgentState) Clear() {
	a.Messages = nil
	a.Session = ""
	a.Main = ""
	a.Profiles = nil
	a.Queue = nil
	a.Chat = nil
	a.Cook = 1
}

// Restore continues a saved chat: backend resume state, main agent identity and transcript.
func (a *AgentState) Restore(id string, saved jsonv.Value) {
	a.Clear()
	a.Messages = cloneItems(jsonv.Get(saved, "messages"))
	a.Session, _ = jsonv.Str(jsonv.Get(saved, "session"))
	a.Main = a.addSaved(jsonv.Get(saved, "agent"))
	events := cloneItems(jsonv.Get(saved, "events"))
	// Every agent of the chat keeps its name, so agents spawned from here on get other names.
	for _, ev := range events {
		a.addSaved(jsonv.Get(ev, "agent"))
	}
	title, _ := jsonv.Str(jsonv.Get(saved, "title"))
	a.Chat = &Chat{ID: id, Title: title, Events: events}
}

func cloneItems(v jsonv.Value) []jsonv.Value {
	items := jsonv.Items(v)
	out := make([]jsonv.Value, len(items))
	for i, it := range items {
		out[i] = jsonv.Clone(it)
	}
	return out
}

// addSaved adds a saved agent identity (Profile.JSON) once per id and returns its id ("" when it is not one).
func (a *AgentState) addSaved(v jsonv.Value) string {
	id, ok1 := jsonv.Str(jsonv.Get(v, "id"))
	name, ok2 := jsonv.Str(jsonv.Get(v, "name"))
	color, ok3 := jsonv.Str(jsonv.Get(v, "color"))
	if !ok1 || !ok2 || !ok3 {
		return ""
	}
	if _, ok := a.Profile(id); !ok {
		parent, _ := jsonv.Str(jsonv.Get(v, "parent"))
		a.Profiles = append(a.Profiles, Profile{ID: id, Name: name, Color: color, Parent: parent})
	}
	return id
}

var (
	agentNames  = []string{"Arunika", "Bayu", "Candra", "Dewi", "Gita", "Langit", "Mega", "Rama", "Sekar", "Tirta", "Wulan", "Surya"}
	agentColors = []string{"#7C9CFF", "#4FD1C5", "#FFB347", "#B794F4", "#FF7AB6", "#63B3ED", "#F6AD55", "#68D391", "#FC8181", "#F6E05E", "#9F7AEA", "#38B2AC"}
	// nextAgentID counts profile ids up for the whole process, not per conversation: the script globals of an
	// earlier conversation's main agent stay keyed by its id.
	nextAgentID atomic.Uint64
)

// AgentColors is how many agent colors there are.
func AgentColors() int { return len(agentColors) }

// NewProfile adds an agent of this conversation (parent "" for a main agent).
func (a *AgentState) NewProfile(parent string) Profile {
	used := func(name string) bool {
		return slices.ContainsFunc(a.Profiles, func(p Profile) bool { return p.Name == name })
	}
	n := uint64(len(agentNames))
	pick := doc.RandomU64() % n
	// Names stay unique within a conversation: once all are taken, count up ("Surya 2").
	name := ""
	for i := uint64(0); i < n; i++ {
		if c := agentNames[(pick+i)%n]; !used(c) {
			name = c
			break
		}
	}
	for k := 2; name == ""; k++ {
		if c := fmt.Sprintf("%s %d", agentNames[pick], k); !used(c) {
			name = c
		}
	}
	// Colors tell agents apart on the canvas and in the chat: start at a random one, take the next not yet used.
	m := uint64(len(agentColors))
	start := doc.RandomU64() % m
	color := agentColors[start]
	for i := uint64(0); i < m; i++ {
		c := agentColors[(start+i)%m]
		if !slices.ContainsFunc(a.Profiles, func(p Profile) bool { return p.Color == c }) {
			color = c
			break
		}
	}
	// The fixed-width suffix keeps two counts from spelling one id.
	id := fmt.Sprintf("ag%d%03x", nextAgentID.Add(1)-1, doc.RandomU64()&0xfff)
	p := Profile{ID: id, Name: name, Color: color, Parent: parent}
	a.Profiles = append(a.Profiles, p)
	return p
}
