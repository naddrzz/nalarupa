package agent

import (
	"errors"
	"fmt"
	"strings"

	"github.com/naddrzz/nalarupa/core-go/engine"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

// SpawnChildren is `spawn_agents`: start child agents that each design inside their container nodes. It returns
// at once; the children report progress through their own events.
func SpawnChildren(e *engine.Engine, parent string, config jsonv.Value) engine.ToolOut {
	bad := func(text string) engine.ToolOut { return engine.ToolOut{Text: text, Error: true} }
	entries := jsonv.Items(jsonv.Get(config, "config"))
	if len(entries) == 0 {
		return bad("spawn_agents needs `config`: [{prompt, containerNodes: [ids]}]")
	}
	if len(entries) > maxChildren {
		return bad(fmt.Sprintf("at most %d agents can be spawned at once", maxChildren))
	}
	type plan struct {
		child engine.Profile
		ids   []string
		nodes []jsonv.Value
	}
	var plans []plan
	var vars jsonv.Value
	var total int
	var side bool
	// Checked, reserved and read under one lock: concurrent calls cannot both spend the same remaining slot.
	var refused error
	e.Locked(func() {
		if p, ok := e.Agent.Profile(parent); !ok || p.Parent != "" {
			refused = errors.New("only the main agent can spawn agents; do the work yourself")
			return
		}
		left := max(max(e.Agent.Parallel-1, 0)-e.Agent.Spawned, 0)
		if len(entries) > left {
			s := "s"
			if left == 1 {
				s = ""
			}
			refused = fmt.Errorf("%d more designer%s can be started this turn, but %d were requested; spawn at most %d and do the rest yourself", left, s, len(entries), left)
			return
		}
		if e.Agent.Mode == "side" {
			if refused = sideDirections(entries); refused != nil {
				return
			}
		}
		var all [][]string
		for _, entry := range entries {
			var ids []string
			for _, v := range jsonv.Items(jsonv.Get(entry, "containerNodes")) {
				if s, ok := jsonv.Str(v); ok {
					ids = append(ids, s)
				}
			}
			if len(ids) == 0 {
				refused = errors.New("each spawn_agents entry needs at least one container node id")
				return
			}
			for _, id := range ids {
				if e.Doc.Find(id) == nil {
					refused = fmt.Errorf("container node not found: %s", id)
					return
				}
			}
			all = append(all, ids)
		}
		e.Agent.Spawned += len(entries)
		// Only cloned here: serializing a large document would hold the engine lock against render and poll.
		vars = e.Doc.Variables()
		for _, ids := range all {
			child := e.Agent.NewProfile(parent)
			var nodes []jsonv.Value
			for _, id := range ids {
				if n := e.Doc.Find(id); n != nil {
					nodes = append(nodes, jsonv.Clone(n))
				}
			}
			plans = append(plans, plan{child, ids, nodes})
		}
		total, side = e.Agent.Parallel, e.Agent.Mode == "side"
	})
	if refused != nil {
		return bad(refused.Error())
	}
	varsText := truncate(string(jsonv.Marshal(vars)), 4000)
	var started []string
	for i, pl := range plans {
		context := truncate(string(jsonv.Marshal(jsonv.NewArray(pl.nodes...))), 12000) + "\n\nDocument variables: " + varsText
		task := strings.TrimSpace(jsonv.StrOr(jsonv.Get(entries[i], "prompt")))
		prompt := childPrompt(side, len(started)+2, total, pl.ids, context, task)
		label := pl.child.Name + " → " + strings.Join(pl.ids, ", ")
		if startRun(e, pl.child, prompt, parent) {
			started = append(started, label)
		}
	}
	if len(started) == 0 {
		return bad("Stopped before the designers started; nothing was spawned.")
	}
	return engine.ToolOut{Text: fmt.Sprintf("Started %d designers: %s. They are working in their containers now. Continue with your own part; do not verify or edit their work.",
		len(started), strings.Join(started, "; "))}
}

// sideDirections: side by side, the lead hands every designer a design direction of its own, none missing and
// no two the same.
func sideDirections(entries []jsonv.Value) error {
	seen := map[string]bool{}
	for _, entry := range entries {
		direction := strings.ToLower(strings.TrimSpace(jsonv.StrOr(jsonv.Get(entry, "prompt"))))
		if direction == "" {
			return errors.New("side by side, every spawn_agents entry needs its own design direction as `prompt` (concept · palette · type · signature move)")
		}
		if seen[direction] {
			return errors.New("two spawn_agents entries share one design direction; give every designer its own, apart on at least two variation axes")
		}
		seen[direction] = true
	}
	return nil
}

// childPrompt is what a spawned designer k of total is told: side by side, its own frame and the design direction
// the lead gave it; otherwise its share of the split work.
func childPrompt(side bool, k, total int, ids []string, context, task string) string {
	if side || task == "" {
		direction := ""
		if side && task != "" {
			direction = fmt.Sprintf("\n\nYour design direction, from the lead: %s\nBuild this direction; post it as your Framing / Direction / Why before building.", task)
		}
		return fmt.Sprintf("You are designer %d of %d working side by side on the same brief; each designer has their own frame.\n"+
			"Design your own take inside the frame `%s` only. Prefix the frame's name with \"%d.\" if it is not already, "+
			"never edit nodes outside it, and do not look at the other frames.%s\n\nThe frame (already read for you, no need to Get it again):\n%s",
			k, total, ids[0], k, direction, context)
	}
	quoted := make([]string, len(ids))
	for i, id := range ids {
		quoted[i] = "`" + id + "`"
	}
	return fmt.Sprintf("Design the following task inside the node(s) %s. Only edit inside them.\nTask: %s\n\n"+
		"The containers as they are now (already read for you, no need to Get them again):\n%s", strings.Join(quoted, ", "), task, context)
}

// SpawnSpec is the `spawn_agents` tool, offered to a main agent working with parallel designers.
func SpawnSpec() jsonv.Value {
	return jsonv.MustParse(`{"name": "spawn_agents", "description": "Start parallel designer agents. Each entry gets a self-contained task (side by side: that designer's own design direction) and the ids of existing container frames it may edit. Create the containers first. Returns immediately.",
           "input_schema": {"type": "object", "properties": {"config": {"type": "array", "items": {"type": "object", "properties": {
               "prompt": {"type": "string"}, "containerNodes": {"type": "array", "items": {"type": "string"}}}, "required": ["prompt", "containerNodes"]}}},
           "required": ["config"]}}`)
}
