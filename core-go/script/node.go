// Package script is script.rs's script nodes and the JavaScript engine they share with the execute sandbox.
//
// A script node (`type: "script"`) names a `.js` file (`scriptUri`, relative to the document) whose body
// returns an array of nodes; they become the node's children, placed in its local space (no layout) and
// never saved. Each run gets a fresh 16 MB QuickJS runtime with the call depth rquickjs reaches in 512 KB,
// 2 s (compiling is not timed), `Math.random` = mulberry32 seeded 0, and at most 1000 output nodes. Runs are
// cached by (source, inputs, size), so a script re-runs exactly when its file, its inputs or its size
// changed. Laid out live (under the engine lock) a run that is not cached goes to a worker goroutine and
// the node shows its previous output, or a "running" box, until it finishes.
package script

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/layout"
	"github.com/naddrzz/nalarupa/core-go/resolve"
	"github.com/naddrzz/nalarupa/core-go/svg"
)

// Script node limits.
const (
	scriptMemory  = 16 << 20
	scriptTimeout = 2 * time.Second
	scriptNodes   = 1000
	// workers is the most runs going at once: a document with many script nodes queues the rest.
	workers = 8
	// slots is the most slots kept; past it the longest-unused idle quarter is dropped.
	slots = 1024
	// cacheSize: past it the result cache is cleared before the next insert.
	cacheSize = 256
)

// Status is what a script node shows instead of its output.
type Status struct {
	// Kind is "error", "empty" or "running".
	Kind        string
	Title       string
	Description *string
	Code        *string
}

func errStatus(title, description string) *Status {
	return &Status{Kind: "error", Title: title, Description: &description}
}

func errTitle(title string) *Status { return &Status{Kind: "error", Title: title} }

func running() *Status { return &Status{Kind: "running", Title: "Running script"} }

func optStr(s *string) jsonv.Value {
	if s == nil {
		return nil
	}
	return *s
}

// JSON is the node's `__status` property.
func (s *Status) JSON() *jsonv.Object {
	o := jsonv.NewObject()
	o.Set("kind", s.Kind)
	o.Set("title", s.Title)
	o.Set("description", optStr(s.Description))
	o.Set("code", optStr(s.Code))
	return o
}

// Result is a script node's output nodes, or the Status it shows instead.
type Result struct {
	Nodes  []jsonv.Value
	Status *Status
}

func (r Result) clone() Result {
	nodes := make([]jsonv.Value, len(r.Nodes))
	for i, n := range r.Nodes {
		nodes[i] = jsonv.Clone(n)
	}
	return Result{Nodes: nodes, Status: r.Status}
}

// scriptPrelude makes `Math.random` mulberry32, reset to seed 0 before every run.
const scriptPrelude = "(function () { let s = 0; globalThis.__seed = function (v) { s = v | 0; }; Math.random = function () { s = (s + 0x6D2B79F5) | 0; let t = Math.imul(s ^ (s >>> 15), 1 | s); t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t; return ((t ^ (t >>> 14)) >>> 0) / 4294967296; }; })()"

// scriptError is the error text ("Name: message" plus the stack) and the source lines around the failing
// one.
func scriptError(err error, src string) (string, *string) {
	var ex *Exception
	if !errors.As(err, &ex) {
		return err.Error(), nil
	}
	if !ex.IsError {
		return ex.Text, nil
	}
	text := ex.Name + ": " + ex.Message
	if strings.TrimSpace(ex.Stack) != "" {
		text += "\n" + strings.TrimRightFunc(ex.Stack, unicode.IsSpace)
	}
	parts := strings.Split(ex.Stack, ":")
	for _, p := range parts[1:] {
		a, ok := parseUsize(p)
		if !ok {
			continue
		}
		ls := lines(src)
		var out []string
		for d := max(a-3, 0); d < min(a+2, len(ls)); d++ {
			mark := " "
			if d+1 == a {
				mark = ">"
			}
			out = append(out, fmt.Sprintf("%s %d: %s", mark, d+1, ls[d]))
		}
		code := strings.Join(out, "\n")
		return text, &code
	}
	return text, nil
}

// runScript compiles and calls the script once; it returns its JSON result.
func runScript(src, file, input string) (string, *Status) {
	js, err := NewJS(Limits{Memory: scriptMemory, StackSlots: stackSlots})
	if err != nil {
		return "", errStatus("An internal error occurred", err.Error())
	}
	defer js.Close()
	if err := js.Exec(scriptPrelude, "<eval>"); err != nil {
		return "", errStatus("An internal error occurred", err.Error())
	}
	// The wrapper keeps the script on line 1 onwards, so error lines are the file's lines.
	wrapper := "(function (__input) { __seed(0); const __ctx = JSON.parse(__input); const result = ((nalarupa, pencil) => {" + src + "\n})(__ctx, __ctx); if (result === undefined) { throw new Error(\"Script must return an array of nodes. Add a 'return [...]' statement at the end of your script.\"); } return JSON.stringify(result); })"
	f, err := js.Eval(wrapper, file)
	if err == nil && !js.IsFunction(f) {
		err = &ConvError{From: js.TypeName(f), To: "function"}
	}
	if err != nil {
		js.Free(f)
		d, code := scriptError(err, src)
		return "", &Status{Kind: "error", Title: "Script compile error", Description: &d, Code: code}
	}
	defer js.Free(f)
	// Armed just before the call: compiling is not timed.
	js.Arm(time.Now().Add(scriptTimeout))
	r, err := js.Call(f, input)
	if err == nil {
		defer js.Free(r)
		var out string
		if out, err = js.String(r); err == nil {
			return out, nil
		}
	}
	d, code := scriptError(err, src)
	return "", &Status{Kind: "error", Title: "Script runtime error", Description: &d, Code: code}
}

// scriptOutput checks the script's output: an array, migrated from schema, at most 1000 nodes, no
// components, no connections, ids dropped.
func scriptOutput(text, schema string) ([]jsonv.Value, *Status) {
	v, err := jsonv.Parse([]byte(text))
	arr := jsonv.Arr(v)
	if err != nil || arr == nil {
		return nil, errStatus("Invalid script output", "Script must return an array of nodes.")
	}
	nodes := arr.Items
	if schema != doc.FormatVersion {
		root := jsonv.NewObject()
		root.Set("version", schema)
		root.Set("children", jsonv.NewArray(nodes...))
		resolve.Migrate(root)
		nodes = slices.Clone(jsonv.Items(jsonv.Get(root, "children")))
	}
	count := 0
	var walk func(n jsonv.Value) *Status
	walk = func(n jsonv.Value) *Status {
		count++
		if count > scriptNodes {
			return errStatus("Too many nodes", fmt.Sprintf("Script output cannot exceed %d nodes.", scriptNodes))
		}
		o := jsonv.Obj(n)
		if o == nil {
			return errStatus("Error inserting script children", "Expected a node object, got "+string(jsonv.Marshal(n)))
		}
		if jsonv.IsTrue(jsonv.Get(o, "reusable")) {
			return errStatus("Invalid script output", "Reusable nodes are not supported in script outputs.")
		}
		if jsonv.StrIs(jsonv.Get(o, "type"), "connection") {
			return errStatus("Invalid script output", "Connection nodes are not supported in script outputs.")
		}
		o.Remove("id")
		for _, k := range jsonv.Items(jsonv.Get(o, "children")) {
			if s := walk(k); s != nil {
				return s
			}
		}
		return nil
	}
	for _, n := range nodes {
		if s := walk(n); s != nil {
			return nil, s
		}
	}
	return nodes, nil
}

// job is one script run waiting for, or handed to, a worker.
type job struct {
	key                      uint64
	src, file, input, schema string
}

func runJob(j *job) Result {
	out, st := runScript(j.src, j.file, j.input)
	if st != nil {
		return Result{Status: st}
	}
	nodes, st := scriptOutput(out, j.schema)
	return Result{Nodes: nodes, Status: st}
}

// runJobSafe is runJob with a panic as an internal error, as a worker thread's `catch_unwind` makes it.
func runJobSafe(j *job) (r Result) {
	defer func() {
		if recover() != nil {
			r = Result{Status: errTitle("An internal error occurred")}
		}
	}()
	return runJob(j)
}

// slot is a script node's worker: it runs one job at a time, so a newer size or input replaces the one still
// waiting instead of queueing behind it.
type slot struct {
	running bool
	// current is the key of the job running, when hasCurrent.
	current    uint64
	hasCurrent bool
	next       *job
	// last is the last finished output: shown while a newer run is pending.
	last *Result
	// used is the tick of the last request, so eviction drops the longest-unused slots first.
	used uint64
}

// scripts is the process-wide state Rust keeps in SCRIPTS: the result cache, the slots and the workers alive.
var scripts struct {
	sync.Mutex
	cache  map[uint64]Result
	slots  map[string]*slot
	active int
	tick   uint64
}

// finished counts finished background runs, so the renderer knows when to rebuild a waiting layer.
var finished atomic.Uint64

// Finished is how many background runs have finished.
func Finished() uint64 { return finished.Load() }

// Busy reports whether a background run is still going or waiting.
func Busy() bool {
	scripts.Lock()
	defer scripts.Unlock()
	for _, s := range scripts.slots {
		if s.running || s.next != nil {
			return true
		}
	}
	return false
}

// store caches r under key; the caller holds the lock.
func store(key uint64, r Result) {
	if scripts.cache == nil || len(scripts.cache) > cacheSize {
		scripts.cache = map[uint64]Result{}
	}
	scripts.cache[key] = r
}

func remember(key uint64, r Result) {
	scripts.Lock()
	store(key, r)
	scripts.Unlock()
}

func cached(key uint64) (Result, bool) {
	scripts.Lock()
	defer scripts.Unlock()
	r, ok := scripts.cache[key]
	return r, ok
}

// Runs are the script runs an export or screenshot needed while it was planned under the engine lock
// (record mode): it drew them as running, so Run them once the lock is released and plan again.
type Runs struct {
	mu   sync.Mutex
	jobs []job
}

func (r *Runs) note(j job) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !slices.ContainsFunc(r.jobs, func(x job) bool { return x.key == j.key }) {
		r.jobs = append(r.jobs, j)
	}
}

// IsEmpty reports whether no run was noted.
func (r *Runs) IsEmpty() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.jobs) == 0
}

// Extend adds the runs of more that r does not hold yet.
func (r *Runs) Extend(more *Runs) {
	more.mu.Lock()
	jobs := slices.Clone(more.jobs)
	more.mu.Unlock()
	for _, j := range jobs {
		r.note(j)
	}
}

// Run runs each noted job (at most `workers` at once) unless its output is cached meanwhile, and caches the
// outputs. Call it without holding the engine lock.
func (r *Runs) Run() {
	r.mu.Lock()
	jobs := slices.Clone(r.jobs)
	r.mu.Unlock()
	for start := 0; start < len(jobs); start += workers {
		var wg sync.WaitGroup
		for i := start; i < min(start+workers, len(jobs)); i++ {
			if _, ok := cached(jobs[i].key); ok {
				continue
			}
			wg.Add(1)
			go func(j *job) {
				defer wg.Done()
				remember(j.key, runJobSafe(j))
			}(&jobs[i])
		}
		wg.Wait()
	}
}

// OffLock is `script::off_lock`: draw (which takes the engine lock itself) runs with the script runs it
// needs noted in rec instead of run under the lock; they then run with no lock held, and draw runs again.
func OffLock[T any](draw func(rec *Runs) T) T {
	rec := &Runs{}
	out := draw(rec)
	if rec.IsEmpty() {
		return out
	}
	rec.Run()
	return draw(&Runs{})
}

// work is a worker goroutine: it runs its slot's jobs, then takes over any slot still waiting for a worker.
func work(name string) {
	for {
		scripts.Lock()
		s := scripts.slots[name]
		var j *job
		if s != nil && s.next != nil {
			j, s.next = s.next, nil
			s.current, s.hasCurrent = j.key, true
		}
		if j == nil {
			if s != nil {
				s.running, s.hasCurrent = false, false
			}
			waiting, found := "", false
			for k, w := range scripts.slots {
				if !w.running && w.next != nil {
					waiting, found = k, true
					break
				}
			}
			if !found {
				scripts.active--
				scripts.Unlock()
				return
			}
			scripts.slots[waiting].running = true
			name = waiting
			scripts.Unlock()
			continue
		}
		r, ok := scripts.cache[j.key]
		scripts.Unlock()
		if !ok {
			r = runJobSafe(j)
		}
		scripts.Lock()
		store(j.key, r)
		if s := scripts.slots[name]; s != nil {
			s.last = &r
		}
		scripts.Unlock()
		finished.Add(1)
	}
}

// joinPath is Rust's `Path::join(dir, rel)` for a relative rel: the path is not cleaned, and a rooted rel
// without a volume (Windows) keeps dir's volume.
func joinPath(dir, rel string) string {
	switch {
	case rel != "" && os.IsPathSeparator(rel[0]):
		return filepath.VolumeName(dir) + rel
	case dir == "" || os.IsPathSeparator(dir[len(dir)-1]):
		return dir + rel
	}
	return dir + string(filepath.Separator) + rel
}

// scriptNode is a script node's output: it reads the file (relative to dir, "" when the document is
// unsaved), checks the header, coerces the inputs and runs it (cached). With a slot (the node's identity) a
// cache miss runs on a worker goroutine and the previous output, or a "running" status, returns at once:
// the caller holds the engine lock and must not wait. Without one, a non-nil rec notes the run and returns
// "running" (record mode); otherwise the call waits for the run.
func scriptNode(uri, dir string, inputs *jsonv.Object, w, h float64, slotName string, rec *Runs) Result {
	if uri == "" {
		return Result{Status: &Status{Kind: "empty", Title: "No script file selected"}}
	}
	rel := strings.TrimPrefix(uri, "file://")
	var path string
	switch {
	case filepath.IsAbs(rel):
		path = rel
	case dir != "":
		path = joinPath(dir, rel)
	default:
		return Result{Status: errTitle("Cannot resolve script path: " + uri)}
	}
	b, err := os.ReadFile(path)
	if err != nil || !utf8.Valid(b) {
		return Result{Status: errStatus("Script not found", "We could not find “"+rel+"” at this path. Check for typos or missing folders.")}
	}
	src := string(b)
	schema, hasSchema, decl, err := scriptHeader(src)
	if err != nil {
		return Result{Status: errTitle(err.Error())}
	}
	if !hasSchema {
		return Result{Status: errStatus("Missing @schema version tag", "Add /** @schema "+doc.FormatVersion+" */ to the top of the script.")}
	}
	in, st := scriptInputs(decl, inputs)
	if st != nil {
		return Result{Status: st}
	}
	ctx := jsonv.NewObject()
	ctx.Set("width", jsonv.Float(w))
	ctx.Set("height", jsonv.Float(h))
	ctx.Set("input", in)
	input := string(jsonv.Marshal(ctx))
	key := svg.FxHash(src + "\x00" + input + "\x00" + rel)
	j := job{key: key, src: src, file: filepath.Base(path), input: input, schema: schema}

	scripts.Lock()
	if r, ok := scripts.cache[key]; ok {
		scripts.Unlock()
		return r.clone()
	}
	if slotName == "" {
		scripts.Unlock()
		if rec != nil {
			// An export or screenshot planned under the engine lock: the run is noted for after the lock.
			rec.note(j)
			return Result{Status: running()}
		}
		r := runJob(&j)
		remember(key, r)
		return r.clone()
	}
	defer scripts.Unlock()
	if scripts.slots == nil {
		scripts.slots = map[string]*slot{}
	}
	scripts.tick++
	if len(scripts.slots) >= slots {
		// Drop the longest-unused idle slots (their node is gone or off screen); busy ones stay.
		type idle struct {
			used uint64
			name string
		}
		var list []idle
		for k, s := range scripts.slots {
			if !s.running && s.next == nil {
				list = append(list, idle{s.used, k})
			}
		}
		slices.SortFunc(list, func(a, b idle) int {
			return cmp.Or(cmp.Compare(a.used, b.used), strings.Compare(a.name, b.name))
		})
		for _, x := range list[:min(len(list), slots/4)] {
			delete(scripts.slots, x.name)
		}
	}
	tick, free := scripts.tick, scripts.active < workers
	s := scripts.slots[slotName]
	if s == nil {
		s = &slot{}
		scripts.slots[slotName] = s
	}
	s.used = tick
	if s.hasCurrent && s.current == key {
		// The size or input went back to what is running: drop the stale one waiting behind it.
		s.next = nil
	} else if s.next == nil || s.next.key != key {
		s.next = &j
	}
	if !s.running && s.next != nil && free {
		s.running = true
		scripts.active++
		go work(slotName)
	}
	if s.last != nil {
		return s.last.clone()
	}
	return Result{Status: running()}
}

// assignIDs gives v and its descendants the ids `sid/k` in pre-order and checks that `ref` targets resolve.
func assignIDs(v jsonv.Value, sid string, k *int, r *resolve.Resolver) *Status {
	o := jsonv.Obj(v)
	o.Set("id", fmt.Sprintf("%s/%d", sid, *k))
	*k++
	if jsonv.StrIs(jsonv.Get(o, "type"), "ref") {
		target := jsonv.StrOr(jsonv.Get(o, "ref"))
		if r.Node(target) == nil {
			return errStatus("Component not found", fmt.Sprintf("Component '%s' could not be resolved.", target))
		}
	}
	for _, c := range jsonv.Items(jsonv.Get(o, "children")) {
		if s := assignIDs(c, sid, k, r); s != nil {
			return s
		}
	}
	return nil
}

// ExpandScripts replaces every laid-out script node's children with its output (or sets its `__status`),
// laid out in the node's box. dir is the document's folder and docPath its file ("" when unsaved). live:
// the caller holds the engine lock, so runs happen in the background. rec, when not nil and not live,
// notes the runs instead of waiting for them (record mode). Ids are `scriptId/k`, so they never collide
// with saved nodes.
func ExpandScripts(n *resolve.RNode, r *resolve.Resolver, m layout.TextMeasurer, dir, docPath string, live bool, rec *Runs) {
	for _, k := range n.Kids {
		ExpandScripts(k, r, m, dir, docPath, live, rec)
	}
	if n.Type != "script" {
		return
	}
	inputs := jsonv.Obj(jsonv.Get(n.P, "inputs"))
	uri, _ := n.S("scriptUri")
	slotName := ""
	if live {
		// A slot belongs to one node of one document, so another document's output never shows here.
		slotName = docPath + "\x00" + n.ID + "\x00" + uri
	}
	res := scriptNode(uri, dir, inputs, n.W, n.H, slotName, rec)
	if res.Status == nil {
		k := 0
		for _, v := range res.Nodes {
			if st := assignIDs(v, n.ID, &k, r); st != nil {
				res = Result{Status: st}
				break
			}
		}
	}
	n.Kids = nil
	if res.Status != nil {
		n.P.Set("__status", res.Status.JSON())
		return
	}
	host := jsonv.NewObject()
	host.Set("type", "frame")
	host.Set("id", n.ID)
	host.Set("layout", "none")
	host.Set("x", jsonv.Float(n.X))
	host.Set("y", jsonv.Float(n.Y))
	host.Set("width", jsonv.Float(n.W))
	host.Set("height", jsonv.Float(n.H))
	host.Set("children", jsonv.NewArray(res.Nodes...))
	if root := r.Resolve(host); root != nil {
		layout.Layout(root, m)
		n.Kids = root.Kids
	}
}
