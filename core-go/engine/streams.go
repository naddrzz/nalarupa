package engine

// Streaming previews (engine.rs `stream_update`, `stream_end`, `take_stream`, `rebuild_preview` and script.rs
// `reset_streams`): while the model is still writing an `execute` call, its complete statements run once each in
// the stream's live JS context against Preview, a copy of the document, so the design appears as it is written.
// The final call retires its stream and runs with the stream's id seed, so the previewed nodes keep their ids.

import (
	"strings"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/script"
	"github.com/naddrzz/nalarupa/core-go/svg"
)

// Stream is an `execute` call the model is still writing (Key is the tool call id).
type Stream struct {
	Key, Agent string
	// Source is the call's complete statements so far.
	Source string
	// Seed is where the call's ids come from, in the preview and in the final call.
	Seed uint64
	// Touched is how many ids the stream has reported in `agent_activity` events.
	Touched int
	// Edited tracks node ids that already spawned an edit slide during streaming so final execute commit doesn't replay them.
	Edited map[string]bool
	// EditedPreTop tracks pre-turn top-level screens intentionally edited by this live stream.
	EditedPreTop map[string]bool
}

// StreamUpdate: the model streamed more of an `execute` call. Its newly completed statements run once, in the
// stream's live JS context against the preview document, so the design appears while the agent is still writing.
// Anything that is not a plain append rebuilds the preview.
func (e *Engine) StreamUpdate(key, agent, source string) {
	prefix := source[:script.CompletePrefix(source)]
	i := e.streamIndex(key)
	switch {
	case i >= 0 && e.Streams[i].Source == prefix:
		return
	case i >= 0:
		e.Streams[i].Source = prefix
	default:
		e.Streams = append(e.Streams, &Stream{Key: key, Agent: agent, Source: prefix, Seed: svg.FxHash(key)})
		i = len(e.Streams) - 1
	}
	clean := !e.Dirty()
	if e.Preview != nil {
		s := e.Streams[i]
		var touched []string
		ok := false
		prevStream := e.activeStream
		e.activeStream = s
		e.onPreview(func() { touched, ok = e.streamStep(key, agent, prefix, s.Seed) })
		e.activeStream = prevStream
		if ok {
			seen := s.Touched
			s.Touched = len(touched)
			if e.Agent.Running {
				e.DeconflictTopLevelNodes(e.Preview)
			}
			e.reconcilePreTurnScreenIsolation(e.Preview)
			if len(touched) > seen {
				e.ExpandProgressiveRootSlides(e.Preview, touched[seen:])
			}
			e.redraw(clean)
			if len(touched) > seen {
				e.EmitWriteActivityWithTeam(s.Agent, e.Preview, touched[seen:], true)
			}
			return
		}
	}
	e.rebuildPreview()
}

// StreamEnd: the stream ended without a final call (Stop, an error): its statements leave the preview.
func (e *Engine) StreamEnd(key string) {
	if i := e.streamIndex(key); i >= 0 {
		e.Streams = append(e.Streams[:i:i], e.Streams[i+1:]...)
		e.rebuildPreview()
	}
}

// StreamsOfAgentEnd ends every stream of agent.
func (e *Engine) StreamsOfAgentEnd(agent string) {
	var keys []string
	for _, s := range e.Streams {
		if s.Agent == agent {
			keys = append(keys, s.Key)
		}
	}
	for _, k := range keys {
		e.StreamEnd(k)
	}
}

func (e *Engine) streamIndex(key string) int {
	for i, s := range e.Streams {
		if s.Key == key {
			return i
		}
	}
	return -1
}

func streamWrite(agent string, ids []string) *jsonv.Object {
	return Object("type", "agent_activity", "agent", agent, "op", "write", "ids", Strings(ids), "streaming", true)
}

// onPreview runs f with the preview as the document (Doc and Preview swapped), swapping back even if f panics.
func (e *Engine) onPreview(f func()) {
	preview := e.Preview
	e.Preview = nil
	e.Doc, preview = preview, e.Doc
	defer func() {
		e.Doc, preview = preview, e.Doc
		e.Preview = preview
	}()
	f()
}

// streamStep is script.StreamStep on the engine's document (the caller swaps in the preview), reported as running
// JavaScript (script.rs `bind`).
func (e *Engine) streamStep(key, agent, source string, seed uint64) ([]string, bool) {
	was := e.Executing
	if was {
		// Another call's script is running with the lock released (a shared execute) and draws its ids from the
		// same generator: the step leaves that sequence where it found it, as Rust's per-thread generators do.
		// Otherwise the step leaves it where its id probe stopped, as Rust's thread does when one thread does both.
		state := e.ids.State()
		defer e.ids.SetState(state)
	}
	e.Executing = true
	prevExecAgent := e.currentExecAgent
	e.currentExecAgent = agent
	defer func() {
		e.Executing = was
		e.currentExecAgent = prevExecAgent
	}()
	if e.Script.live == nil {
		e.Script.live = map[string]*script.Live{}
	}
	return script.StreamStep(e.Host(), e.Script.live, key, e.Script.Globals[agent], source, seed)
}

// takeStream retires the stream the final `execute` call belongs to and returns it (nil: none). Without a stream
// key (Claude Code over MCP) only the calling agent's own streams can match, by prefix: another agent's script may
// start with the same statement. The exact key wins over an earlier stream that merely shares a prefix.
func (e *Engine) takeStream(key string, hasKey bool, agent string, hasAgent bool, source string) *Stream {
	src := strings.TrimSpace(source)
	i := -1
	if hasKey {
		i = e.streamIndex(key)
	}
	if i < 0 {
		for k, s := range e.Streams {
			own := strings.TrimSpace(s.Source)
			if (!hasAgent || agent == s.Agent) && own != "" && strings.HasPrefix(src, own) {
				i = k
				break
			}
		}
	}
	if i < 0 {
		return nil
	}
	s := e.Streams[i]
	e.Streams = append(e.Streams[:i:i], e.Streams[i+1:]...)
	e.rebuildPreview()
	return s
}

// resetStreams: the preview is being rebuilt from the real document, so streams re-run from scratch, except stalled
// ones, which stay out of it. Contexts of ended streams are dropped.
func (e *Engine) resetStreams() {
	for k, l := range e.Script.live {
		if !l.Stalled() || e.streamIndex(k) < 0 {
			delete(e.Script.live, k)
			l.Close()
		}
	}
}

// closeLive frees every live stream context.
func (e *Engine) closeLive() {
	for k, l := range e.Script.live {
		delete(e.Script.live, k)
		l.Close()
	}
}

// rebuildPreview makes the preview from scratch: the real document plus every stream's complete statements, each
// stream in a fresh JS context.
func (e *Engine) rebuildPreview() {
	e.resetStreams()
	clean := !e.Dirty()
	if len(e.Streams) == 0 {
		if e.Preview != nil {
			e.Preview = nil
			e.redraw(clean)
		}
		return
	}
	e.Preview = &doc.Doc{Root: cloneRoot(e.Doc.Root), Path: e.Doc.Path, IDs: e.ids}
	type pendingStreamAct struct {
		agent string
		ids   []string
	}
	var activity []pendingStreamAct
	e.onPreview(func() {
		prevStream := e.activeStream
		defer func() { e.activeStream = prevStream }()
		for _, s := range append([]*Stream(nil), e.Streams...) {
			e.activeStream = s
			touched, _ := e.streamStep(s.Key, s.Agent, s.Source, s.Seed)
			if len(touched) > s.Touched {
				activity = append(activity, pendingStreamAct{
					agent: s.Agent,
					ids:   append([]string(nil), touched[s.Touched:]...),
				})
			}
			s.Touched = len(touched)
		}
	})
	if e.Agent.Running {
		e.DeconflictTopLevelNodes(e.Preview)
	}
	e.reconcilePreTurnScreenIsolation(e.Preview)
	for _, act := range activity {
		e.ExpandProgressiveRootSlides(e.Preview, act.ids)
	}
	e.redraw(clean)
	for _, act := range activity {
		e.EmitWriteActivityWithTeam(act.agent, e.Preview, act.ids, true)
	}
}
