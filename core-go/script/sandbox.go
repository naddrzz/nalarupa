package script

// The execute sandbox (script.rs `sandbox`, `settle`, `describe`, `run`): every call gets a fresh quickjs-ng
// runtime with a wall-clock deadline, a heap cap and a stack cap, the natives and the prelude, then the globals
// the agent's earlier calls kept. The agent's code runs as sloppy global code named `input`, so errors can point
// at its line; the jobs it queued run within the same deadline, and a promise left rejected fails the call.

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

const (
	// ExecuteTimeout is the wall-clock budget of one execute call.
	ExecuteTimeout = 30 * time.Second
	// PreviewTimeout is the budget of one streaming preview step: it runs while the editor waits, so it is short;
	// the final execute still gets the full budget.
	PreviewTimeout = 2 * time.Second
	// MemoryLimit is the JS heap cap per call. Agent snippets hold node JSON and Get results (a 10k-node document
	// is ~25 MB once parsed), so 64 MB fits large reads while a runaway allocation stays small next to the app.
	MemoryLimit = 64 << 20
	// stackLimit is the native stack JavaScript may use: half of the 1 MB Windows main-thread stack, leaving room
	// for the layout code natives run on the same stack.
	stackLimit = 512 << 10
	// A job queue is checked against the heap cap every heapCheckJobs jobs; a heap within oomSlack of the cap
	// counts as out of memory. Jobs allocate a few hundred bytes each, so the slack outruns the growth between
	// checks.
	heapCheckJobs = 1024
	oomSlack      = 4 << 20
)

// Export is one `Export` call: the engine plans and writes it once the call has committed.
type Export struct {
	IDs         []string
	Format, Out string
	Opts        jsonv.Value
}

// Pen is one `Generate("svg")` call: the AI Pen draws Prompt into Frame once the call has committed.
type Pen struct{ Frame, Prompt string }

// Icon is an icon still downloading when the call validated: Warning goes to the issues once it turns out to be
// missing from Library (the engine waits for it).
type Icon struct {
	Warning, Library, Name string
	Weight                 uint32
}

// exec is what one execute call's natives share (script.rs `Exec`).
type exec struct {
	host Host
	// lock, when set, is taken around each native (shared mode).
	lock     Locker
	js       *JS
	budget   time.Duration
	deadline time.Time
	// preview: a streaming preview step, with no network side effects (Generate is skipped).
	preview bool
	// limit: the script was stopped by the time or memory limit (a streamed call then stops re-running).
	limit   bool
	prints  []string
	names   [][2]string
	shots   []string
	exports []Export
	notes   []string
	touched []string
	// read holds the nodes the script read (Get, TakeScreenshot); the canvas flashes them longer.
	read []string
	pens []Pen
}

// panicNative, set by tests, adds `__PANIC`, a native that panics (script.rs's test-only `__PANIC`).
var panicNative bool

// run is script.rs `run`: the snippet in a fresh sandbox with the globals earlier calls kept; it returns the
// globals the snippet leaves. A panic while it runs (a native's included) is an ordinary error, so the call
// rolls back and the agent gets an editId to repair.
func (ex *exec) run(source string, globals *jsonv.Object, stop <-chan struct{}) (out *jsonv.Object, err error) {
	defer func() {
		if recover() != nil {
			out, err = nil, errorString("internal error (panic) while running the script")
		}
	}()
	return ex.runUnguarded(source, globals, stop)
}

func (ex *exec) runUnguarded(source string, globals *jsonv.Object, stop <-chan struct{}) (*jsonv.Object, error) {
	js, err := NewJS(Limits{Memory: MemoryLimit, Stack: stackLimit})
	if err != nil {
		return nil, err
	}
	defer js.Close()
	ex.js = js
	js.Arm(ex.deadline)
	if stop != nil {
		// Stop may come from another goroutine at any time; it must not reach a closed runtime.
		var mu sync.Mutex
		closed, done := false, make(chan struct{})
		go func() {
			select {
			case <-stop:
				mu.Lock()
				if !closed {
					js.Cancel()
				}
				mu.Unlock()
			case <-done:
			}
		}()
		defer func() {
			close(done)
			mu.Lock()
			closed = true
			mu.Unlock()
		}()
	}
	if err := ex.sandbox(globals); err != nil {
		return nil, err
	}
	if i := strings.IndexByte(source, 0); i >= 0 {
		// rquickjs hands the source to C as a C string.
		return nil, fmt.Errorf("String contained internal null bytes: nul byte found in provided data at position: %d", i)
	}
	if err := js.Exec(source, "input"); err != nil {
		return nil, errorString(ex.describe(err))
	}
	if err := ex.settle(); err != nil {
		return nil, err
	}
	v, err := js.Eval(exportGlobals, "eval_script")
	if err != nil {
		return nil, errorString(ex.describe(err))
	}
	defer js.Free(v)
	text, err := js.String(v)
	if err != nil {
		return nil, err
	}
	parsed, perr := jsonv.Parse([]byte(text))
	if o := jsonv.Obj(parsed); perr == nil && o != nil {
		serdeNumbers(o)
		return o, nil
	}
	return jsonv.NewObject(), nil
}

// sandbox sets up the fresh runtime: the rejection tracker, the natives, the prelude, then the globals earlier
// calls kept.
func (ex *exec) sandbox(globals *jsonv.Object) error {
	js := ex.js
	js.TrackRejections("__track")
	for _, n := range natives {
		f := n.fn
		call := func(a []string) (string, error) { return f(ex, a) }
		if ex.lock != nil {
			// script.rs `with` without a bound engine: each native locks it for its own work.
			call = func(a []string) (string, error) {
				ex.lock.Lock()
				defer ex.lock.Unlock()
				return f(ex, a)
			}
		}
		if err := js.Define(n.name, call); err != nil {
			return errorString("host functions: " + err.Error())
		}
	}
	if panicNative {
		if err := js.Define("__PANIC", func([]string) (string, error) { panic("host function panic") }); err != nil {
			return errorString("host functions: " + err.Error())
		}
	}
	if err := js.Exec(prelude, "eval_script"); err != nil {
		return errorString("prelude: " + ex.describe(err))
	}
	var restore strings.Builder
	if globals != nil {
		globals.Each(func(k string, v jsonv.Value) {
			restore.WriteString("globalThis[")
			restore.Write(jsonv.Marshal(k))
			restore.WriteString("] = ")
			restore.Write(jsonv.Marshal(v))
			restore.WriteString(";\n")
		})
	}
	if err := js.Exec(restore.String(), "eval_script"); err != nil {
		return errorString("restoring globals: " + ex.describe(err))
	}
	return nil
}

// settle finishes a snippet: it runs the jobs it queued (code after an `await`, `.then` callbacks) within the
// same deadline, then fails the call on an unhandled rejection. Running out of memory inside a job can drop the
// job without a usable error (the queue just ends and the call looks finished), so the heap is checked every
// heapCheckJobs jobs and once more when the queue drains.
func (ex *exec) settle() error {
	js := ex.js
	nearCap := func() bool { return js.HeapSize() >= MemoryLimit-oomSlack }
	jobs := 0
	for {
		ran, err := js.RunJob()
		if err != nil {
			return errorString(ex.describe(err))
		}
		if !ran {
			break
		}
		jobs++
		if js.Expired() || js.Canceled() {
			return errorString(ex.describe(nil))
		}
		if jobs%heapCheckJobs == 0 && nearCap() {
			return ex.oomStop()
		}
	}
	if nearCap() {
		return ex.oomStop()
	}
	return ex.unhandledRejection()
}

// unhandledRejection fails the call like a throw when a promise was left rejected. `__unhandled` boxes the reason
// in an array, so a rejection with an undefined reason still counts.
func (ex *exec) unhandledRejection() error {
	js := ex.js
	f, err := js.Global("__unhandled")
	if err != nil {
		return errorString(ex.describe(err))
	}
	defer js.Free(f)
	r, err := js.Call(f)
	if err != nil {
		return errorString(ex.describe(err))
	}
	defer js.Free(r)
	if js.Nullish(r) {
		return nil
	}
	if !js.IsArray(r) {
		return errorString(ex.describe(&ConvError{From: js.TypeName(r), To: "array"}))
	}
	reason, err := js.Index(r, 0)
	if err != nil {
		return errorString(ex.describe(err))
	}
	defer js.Free(reason)
	return errorString(ex.describe(js.Thrown(reason)))
}

func (ex *exec) oomStop() error {
	return errorString(ex.limitStop(fmt.Sprintf("InternalError: out of memory: the snippet used more than %d MB", MemoryLimit>>20)))
}

// limitStop flags a time or memory limit, so a streamed call stops re-running.
func (ex *exec) limitStop(msg string) string {
	ex.limit = true
	return msg
}

// describe is the agent's text for a failed evaluation (script.rs `describe`): a stop or the deadline wins over
// what was thrown; an Error reads `Name: message (line N)`, any other value `Uncaught <value>`. err is nil when
// nothing was thrown (a job loop past the deadline); an error that is not an *Exception is its own text.
func (ex *exec) describe(err error) string {
	var thrown *Exception
	if err != nil && !errors.As(err, &thrown) {
		return err.Error()
	}
	if ex.js.Canceled() {
		return ex.limitStop("the agent was stopped, so the snippet was interrupted")
	}
	if ex.js.Expired() {
		return ex.limitStop(timeLimitText(ex.budget))
	}
	if thrown == nil {
		return "Uncaught undefined"
	}
	if !thrown.IsError {
		return "Uncaught " + thrown.Text
	}
	text := thrown.Name + ": " + thrown.Message
	if _, after, ok := strings.Cut(thrown.Stack, "input:"); ok {
		n := 0
		for n < len(after) && after[n] >= '0' && after[n] <= '9' {
			n++
		}
		if n > 0 {
			text += " (line " + after[:n] + ")"
		}
	}
	if thrown.Message == "out of memory" {
		return ex.limitStop(fmt.Sprintf("%s: the snippet used more than %d MB", text, MemoryLimit>>20))
	}
	return text
}

// timeLimitText is the error of a snippet stopped at its budget, in seconds with one decimal as Rust's `{:.1}`.
func timeLimitText(budget time.Duration) string {
	return "the snippet ran past the " + strconv.FormatFloat(budget.Seconds(), 'f', 1, 64) + " s time limit and was stopped (endless loop?)"
}
