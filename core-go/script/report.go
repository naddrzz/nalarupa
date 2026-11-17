package script

// One execute call (script.rs `execute_within`, `prepare` and the text half of `finish`): one call is one
// transaction. Any error rolls the document back and answers with an editId to repair; success answers with the
// report the agent reads: notes, the nodes it created by name, the globals that carry over, its Print output and
// the issues found in what it touched.

import (
	"fmt"
	"strings"
	"time"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

// Call is what an execute call brings besides its tool arguments.
type Call struct {
	// Globals are the globals the agent's earlier calls kept (nil: none).
	Globals *jsonv.Object
	// Failed is the snippet a failed call stored under its editId.
	Failed func(editID string) (string, bool)
	// Budget is the wall-clock budget; 0 is ExecuteTimeout.
	Budget time.Duration
	// Preview: a streaming preview step, with no network side effects (Generate is skipped).
	Preview bool
	// Stop, when closed, interrupts the script (the agent's Stop button).
	Stop <-chan struct{}
	// Locker, when set, is the engine lock the caller holds: it is released while the JavaScript runs and taken
	// around each native (execute_shared). nil: the caller holds the host for the whole call.
	Locker Locker
	// Ran, when set, is called once the JavaScript has run (with the Locker taken again), before the rollback or
	// the report: the engine stops reporting the call as running there (script.rs `bind`, `execute_shared`).
	Ran func()
}

// Outcome is what an execute call did. The engine commits it (history, events, the globals, the stored snippet,
// the AI Pen, screenshots and exports) or, on an error, finishes the rollback (slides, the clean state).
type Outcome struct {
	// Text is what the agent reads; Error marks a failed call.
	Text  string
	Error bool
	// Before is the document root before the call: the undo step on success; on an error the document's root is
	// Before again (the same object). nil when the arguments were refused before anything ran.
	Before *jsonv.Object
	// EditID is the editId a failed call's Source is stored under; Repaired is the editId a successful repair
	// fixed (its stored snippet is no longer needed).
	EditID, Source, Repaired string
	// Globals are the globals the call left (success only): the agent's later calls get them.
	Globals *jsonv.Object
	// Touched are the nodes the call wrote, Read the nodes it read.
	Touched, Read []string
	// Shots, Exports and Pens are the screenshots, exports and AI Pen drawings to make once the call commits;
	// Icons the icons still downloading whose "not found" warning waits for them.
	Shots   []string
	Exports []Export
	Pens    []Pen
	Icons   []Icon
	// Limit: the time or memory limit (or Stop) ended the script.
	Limit bool
}

// Execute runs one execute call against h's document. args are the tool's arguments: `input`, or `editId` and
// `edits`, and `__seed`, which reseeds h's ids so a streamed call's preview and final run make the same ids.
func Execute(h Host, args jsonv.Value, c Call) *Outcome {
	src, editID, err := source(args, c.Failed)
	if err != nil {
		return &Outcome{Text: err.Error(), Error: true}
	}
	d := h.Doc()
	// The call's ids all come from h.IDs(), whatever generator the document had.
	defer func(prev *doc.IDSource) { d.IDs = prev }(d.IDs)
	d.IDs = h.IDs()
	before := jsonv.Obj(jsonv.Clone(d.Root))
	if n, ok := jsonv.Get(args, "__seed").(jsonv.Number); ok {
		if seed, ok := n.Uint64(); ok {
			h.IDs().Reseed(seed)
		}
	}
	budget := c.Budget
	if budget <= 0 {
		budget = ExecuteTimeout
	}
	ex := &exec{host: h, budget: budget, deadline: time.Now().Add(budget), preview: c.Preview, lock: c.Locker}
	if c.Locker != nil {
		c.Locker.Unlock()
	}
	globals, err := ex.run(src, c.Globals, c.Stop)
	if c.Locker != nil {
		c.Locker.Lock()
	}
	if c.Ran != nil {
		c.Ran()
	}
	out := &Outcome{
		Before: before, Touched: ex.touched, Read: ex.read, Shots: ex.shots, Exports: ex.exports, Pens: ex.pens,
		Limit: ex.limit,
	}
	if err != nil {
		// Rolled back on the document the host has now (Rust's `e.doc.root = before`).
		h.Doc().Root = before
		id := editID
		if id == "" {
			id = fmt.Sprintf("e%x", h.IDs().Next()&0xffffff)
		}
		out.Text, out.Error, out.EditID, out.Source = RollbackText(err.Error(), id), true, id, src
		return out
	}
	out.Repaired, out.Globals = editID, globals
	out.Text, out.Icons = ex.report(globals)
	return out
}

// RollbackText is the answer to a failed call.
func RollbackText(err, editID string) string {
	return fmt.Sprintf("Error: %s\nAll operations in this call have been rolled back.\nIMPORTANT: fix this by calling execute again with `editId: \"%s\"` and an `edits` list of {find, replace} patches — do not resend the whole snippet.", err, editID)
}

// report is the answer to a successful call, up to its issues; the engine appends what its screenshots and
// exports report.
func (ex *exec) report(globals *jsonv.Object) (string, []Icon) {
	var b strings.Builder
	b.WriteString("OK\n")
	for _, n := range ex.notes {
		b.WriteString("Note: " + n + "\n")
	}
	if len(ex.names) > 0 {
		b.WriteString("\n## Created nodes by name\n")
		for _, nm := range ex.names {
			name, id := nm[0], nm[1]
			global := ""
			for _, k := range globals.Keys() {
				if v, _ := globals.Get(k); jsonv.StrIs(v, id) {
					global = " (= " + k + ")"
					break
				}
			}
			fmt.Fprintf(&b, "\"%s\": \"%s\"%s\n", name, id, global)
		}
	}
	if globals.Len() > 0 {
		b.WriteString("\nGlobal variables carry over to later calls: " + strings.Join(globals.Keys(), ", ") + "\n")
	}
	if len(ex.prints) > 0 {
		b.WriteString("\n## Print output\n" + strings.Join(ex.prints, "\n") + "\n")
	}
	warnings, icons := ex.validate()
	if len(warnings) > 0 {
		b.WriteString("\n## Issues detected (fix them in the next call)\n")
		for _, w := range warnings {
			b.WriteString(IssueLine(w))
		}
	}
	return b.String(), icons
}

// IssueLine is one bullet of the "Issues detected" list. One line per warning: a node name may hold a line break,
// and readers (the agent, `tool_done` events) stop at the first line that is not a bullet.
func IssueLine(warning string) string {
	return "- " + strings.NewReplacer("\r", " ", "\n", " ").Replace(warning) + "\n"
}
