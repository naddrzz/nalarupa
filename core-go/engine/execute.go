package engine

// The engine half of `execute` (engine.rs `call_tool`, `begin_execute`, `end_execute` and script.rs
// `execute_within`, `execute_shared` and `finish`): one call is one transaction around script.Execute. Success is
// one undo step with the call's globals kept and its screenshots and exports planned; an error rolls the document
// back, keeps it clean if it was, and stores the snippet under the editId the agent repairs it with.

import (
	"maps"
	"time"

	"github.com/naddrzz/nalarupa/core-go/export"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/script"
)

// AgentHooks are the built-in agent's parts of the engine (agent.rs), filled by package agent's Attach. A nil
// hook is inert. Each is called under the engine lock, except StartMCP.
type AgentHooks struct {
	// Stop is the Stop signal of a call by agent (closed when the user stops it), or nil when Stop does not reach
	// it: Rust raises it only for a running built-in agent's call.
	Stop func(agent string) <-chan struct{}
	// NoteFlagged is agent.rs `note_flagged`: the frames agent's committed call newly flagged `placeholder`
	// (before is the document before the call).
	NoteFlagged func(agent string, before *jsonv.Object)
	// StartPen starts the AI Pen drawing prompt into frame (w×h) once agent's call committed (agent.rs
	// `stopped_since` with stops, the agent's Stop count when the call began, and pen.rs `start`).
	StartPen func(agent string, stops uint64, frame, prompt string, w, h float64)
	// SaveChat saves the current chat in the background (agent.rs `save_chat`): Emit calls it when an agent event
	// ends a message or a turn.
	SaveChat func() error
	// StartMCP starts the MCP server and returns its port (mcp.rs `start`); `init` calls it, without the lock,
	// while no server runs.
	StartMCP func() (uint16, error)
	// ExportAllowed is the path rule of a script's Export (agent.rs `path_allowed` for "Write" inside the export
	// folder); nil is export.WritePolicy.
	ExportAllowed export.PathPolicy
}

// executeCall is one `execute` call between beginExecute and endExecute.
type executeCall struct {
	args      jsonv.Value
	seen      int
	agent     string
	skipEdits map[string]bool
}

// beginExecute starts an `execute` call: it retires the stream the call belongs to, and then runs with that
// stream's id seed, so the previewed nodes keep their ids.
func (e *Engine) beginExecute(args jsonv.Value) executeCall {
	key, hasKey := jsonv.Str(jsonv.Get(args, "__stream"))
	agent, hasAgent := jsonv.Str(jsonv.Get(args, "__agent"))
	input, _ := jsonv.Str(jsonv.Get(args, "input"))
	c := executeCall{args: args, agent: agent}
	if !hasAgent {
		c.agent = "agent"
	}
	if s := e.takeStream(key, hasKey, agent, hasAgent, input); s != nil {
		if !hasAgent && s.Agent != "" {
			c.agent = s.Agent
		}
		if o := jsonv.Obj(args); o != nil {
			o = jsonv.Obj(jsonv.Clone(o))
			o.Set("__seed", u64(s.Seed))
			if !hasAgent && s.Agent != "" {
				o.Set("__agent", s.Agent)
			}
			c.args = o
		}
		c.seen = s.Touched
		c.skipEdits = s.Edited
	}
	e.Script.edit = nil
	return c
}

// endExecute reports what a finished `execute` call wrote and read. The ids come from the call's `agent_edit`
// event, not from Events: a poll may have drained them while the script ran.
func (e *Engine) endExecute(c executeCall) {
	ev := e.Script.edit
	e.Script.edit = nil
	if ev == nil {
		return
	}
	if ids := jsonv.Items(jsonv.Get(ev, "ids")); len(ids) > c.seen {
		strIDs := make([]string, 0, len(ids)-c.seen)
		for _, v := range ids[c.seen:] {
			if s, ok := jsonv.Str(v); ok {
				strIDs = append(strIDs, s)
			}
		}
		e.EmitWriteActivityWithTeam(c.agent, e.Doc, strIDs, false)
	}
	if read := jsonv.Items(jsonv.Get(ev, "read")); len(read) > 0 {
		e.Emit(Object("type", "agent_activity", "agent", c.agent, "op", "read", "ids", jsonv.Get(ev, "read"), "streaming", false))
	}
}

// CallTool dispatches one agent tool call without holding the engine across it (engine.rs `call_tool`). `execute`
// takes the edit gate, so no other script or gated edit changes the document meanwhile, and runs its JavaScript
// with the engine unlocked (each native locks it for its own work), so polls, renders and Stop keep answering;
// the other tools run under the lock. Take neither the lock nor the gate before calling it; call Finish on the
// answer afterwards.
func (e *Engine) CallTool(name string, args jsonv.Value) ToolOut {
	if name != "execute" {
		e.Lock()
		defer e.Unlock()
		return e.Tool(name, args)
	}
	defer e.EditGate()()
	e.Lock()
	defer e.Unlock()
	c := e.beginExecute(args)
	var stop <-chan struct{}
	if e.Hooks.Stop != nil {
		stop = e.Hooks.Stop(c.agent)
	}
	out := e.executeWithSkipEdits(c, script.Call{Stop: stop, Locker: e})
	e.endExecute(c)
	return out
}

func (e *Engine) executeWithSkipEdits(c executeCall, sc script.Call) ToolOut {
	prevSkip := e.commitSkipEdits
	e.commitSkipEdits = c.skipEdits
	defer func() { e.commitSkipEdits = prevSkip }()
	return e.execute(c.args, sc)
}

// ExecuteWithin runs one `execute` call with the wall-clock budget (script.rs `execute_within`) under the engine
// lock, which the caller holds; call Finish on the answer once the lock is released.
func (e *Engine) ExecuteWithin(args jsonv.Value, budget time.Duration) ToolOut {
	return e.execute(args, script.Call{Budget: budget})
}

// execute runs one call (script.rs `execute_within`/`execute_shared` and `finish`) under the engine lock; with
// c.Locker set the lock is released while the JavaScript runs.
func (e *Engine) execute(args jsonv.Value, c script.Call) ToolOut {
	agent, ok := jsonv.Str(jsonv.Get(args, "__agent"))
	if !ok {
		agent = "agent"
	}
	wasClean := !e.Dirty()
	stops := e.Agent.Stops
	c.Globals = e.Script.Globals[agent]
	c.Failed = func(id string) (string, bool) {
		s, ok := e.Script.Failed[id]
		return s, ok
	}
	was := e.Executing
	e.Executing = true
	prevExecAgent := e.currentExecAgent
	e.currentExecAgent = agent
	// Cleared once the JavaScript has run, before the report validates (script.rs `bind`, `execute_shared`), and
	// on a panic.
	defer func() {
		e.Executing = was
		e.currentExecAgent = prevExecAgent
	}()
	c.Ran = func() { e.Executing = was }
	marks := maps.Clone(e.Agent.TurnEditedPreTop)
	out := script.Execute(e.Host(), args, c)
	if out.Before == nil {
		// Refused before anything ran.
		return ToolOut{Text: out.Text, Error: out.Error}
	}
	if out.Error {
		e.Agent.TurnEditedPreTop = marks
		e.ForgetGoneSlides()
		e.Touch()
		e.KeepClean(wasClean)
		if e.Script.Failed == nil {
			e.Script.Failed = map[string]string{}
		}
		e.Script.Failed[out.EditID] = out.Source
		return ToolOut{Text: out.Text, Error: true}
	}
	if out.Repaired != "" {
		delete(e.Script.Failed, out.Repaired)
	}
	if e.Hooks.NoteFlagged != nil {
		e.Hooks.NoteFlagged(agent, out.Before)
	}
	if e.Script.Globals == nil {
		e.Script.Globals = map[string]*jsonv.Object{}
	}
	g := e.Script.Globals[agent]
	if g == nil {
		g = jsonv.NewObject()
		e.Script.Globals[agent] = g
	}
	out.Globals.Each(func(k string, v jsonv.Value) { g.Set(k, jsonv.Clone(v)) })
	if e.Agent.Running {
		e.DeconflictTopLevelNodes(e.Doc)
	}
	e.reconcilePreTurnScreenIsolation(e.Doc)
	e.ExpandProgressiveRootSlides(e.Doc, out.Touched)
	e.Commit(out.Before)
	edit := Object("type", "agent_edit", "ids", Strings(out.Touched), "read", Strings(out.Read))
	e.Script.edit = edit
	e.Emit(jsonv.Clone(edit))
	if len(out.Pens) > 0 {
		e.Sync()
		for _, p := range out.Pens {
			w, h := 400.0, 400.0
			if f := e.Renderer.Find(p.Frame); f != nil {
				w, h = fmax(f.W, 1), fmax(f.H, 1)
			}
			if e.Hooks.StartPen != nil {
				e.Hooks.StartPen(agent, stops, p.Frame, p.Prompt, w, h)
			}
		}
	}
	res := e.planOutputs(out)
	res.Audit = e.CaptureAuditSnapshot()
	return res
}
