package engine

// What an `execute` call leaves for after the engine lock (engine.rs `ToolOut::finish`, script.rs `finish`'s
// screenshot and export half, `shot_tree`): screenshot trees and export jobs are planned under the lock;
// rasterizing, encoding, writing and waiting for the icons, images and script runs they show happen in Finish,
// without it, so the canvas keeps drawing while the agent looks at its work.

import (
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/export"
	// A script's html exports (it sets export.HTMLDocument).
	_ "github.com/naddrzz/nalarupa/core-go/html"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/resolve"
	"github.com/naddrzz/nalarupa/core-go/script"
	"github.com/naddrzz/nalarupa/core-go/usvg"
)

// settleWait is how long Finish waits for the downloads and icons a call's output shows.
const settleWait = 12 * time.Second

// pollEvery is how often a wait looks again (the lock is free in between, so the downloads can land).
const pollEvery = 20 * time.Millisecond

// shot is a screenshot still to rasterize: the node, its tree and scale.
type shot struct {
	id    string
	tree  *usvg.Tree
	scale float64
}

// pendingOut is what an `execute` answer still has to do once the lock is released.
type pendingOut struct {
	shots []shot
	// icons are the icons still downloading when the call ended: Finish waits for them and reports the ones that
	// turn out not to exist.
	icons []script.Icon
	// downloads are the icons and images the screenshots show that were still downloading.
	downloads []string
	// runs are the script runs the screenshots drew as running.
	runs *script.Runs
	// callDoc and callLibs are the document and libraries the screenshots and exports were made of, kept only when
	// something was still downloading or running: Finish redraws and replans from them, since the live ones may have
	// changed (or been switched) meanwhile.
	callDoc  *doc.Doc
	callLibs []resolve.Lib
	// exports are a script's `Export` calls, planned under the lock.
	exports []*export.Job
}

// exportCtx is the export context of d and libs with the engine's fonts and assets, noting script runs in runs
// (record mode) when it is not nil.
func (e *Engine) exportCtx(d *doc.Doc, libs []resolve.Lib, runs *script.Runs) *export.Ctx {
	return &export.Ctx{Doc: d, Libs: libs, Fonts: e.Fonts, Assets: e.Assets, Runs: runs}
}

// shotTarget is the node a screenshot of id draws: `document` is the first top-level node.
func shotTarget(d *doc.Doc, id string) string {
	if id != "document" {
		return id
	}
	if kids := d.Children(); len(kids) > 0 {
		s, _ := jsonv.Str(jsonv.Get(kids[0], "id"))
		return s
	}
	return ""
}

// shotTree is script.rs `shot_tree`: the render tree and scale of a screenshot of id, the icons and images it
// shows that are still downloading and the script runs it drew as running.
func (e *Engine) shotTree(id string) (*usvg.Tree, float64, []string, *script.Runs, error) {
	target := shotTarget(e.Doc, id)
	e.Sync()
	scale := 1.0
	if f := e.Renderer.Find(target); f != nil {
		scale = fmin(1400/fmax(fmax(f.W, f.H), 1), 2)
	}
	scale = fclamp(scale, 0.05, 8)
	e.Assets.Record()
	runs := &script.Runs{}
	tree, err := e.exportCtx(e.Doc, e.Libs, runs).NodeTreeAt(target, scale)
	waits := e.Assets.Recorded()
	return tree, scale, waits, runs, err
}

// planOutputs is the end of script.rs `finish`: the call's report with its screenshots' trees built and its
// exports planned (nothing is written for a refused path).
func (e *Engine) planOutputs(out *script.Outcome) ToolOut {
	text := out.Text
	p := pendingOut{icons: out.Icons, runs: &script.Runs{}}
	for _, id := range out.Shots {
		tree, scale, waits, runs, err := e.shotTree(id)
		if err != nil {
			text += fmt.Sprintf("Screenshot of %s failed: %s\n", id, err)
			continue
		}
		p.shots = append(p.shots, shot{id, tree, scale})
		p.downloads = append(p.downloads, waits...)
		p.runs.Extend(runs)
	}
	allowed := e.Hooks.ExportAllowed
	if allowed == nil {
		allowed = export.WritePolicy
	}
	stale := false
	for _, x := range out.Exports {
		// Checked before the job is planned, so nothing is written for a refused path.
		err := export.CheckScriptExport(e.Doc, e.DataDir, x.Format, x.Out, allowed)
		var job *export.Job
		if err == nil {
			job, err = export.Plan(e.exportCtx(e.Doc, e.Libs, nil), e.DataDir, x.IDs, x.Format, x.Out, x.Opts)
		}
		if err == nil {
			err = export.ClaimScriptFiles(job)
		}
		if err != nil {
			text += fmt.Sprintf("\nExport failed: %s\n", err)
			continue
		}
		p.exports = append(p.exports, job)
		stale = stale || job.Stale()
	}
	redraw := len(p.shots) > 0 && (len(p.icons) > 0 || len(p.downloads) > 0 || !p.runs.IsEmpty())
	if redraw || stale {
		p.callDoc = &doc.Doc{Root: cloneRoot(e.Doc.Root), Path: e.Doc.Path}
		p.callLibs = make([]resolve.Lib, len(e.Libs))
		for i, l := range e.Libs {
			p.callLibs[i] = resolve.Lib{Alias: l.Alias, Root: jsonv.Clone(l.Root)}
		}
	}
	return ToolOut{Text: text, pending: p}
}

// Finish settles pending icons, rasterizes pending screenshots and writes pending exports once the icons and
// images they show have downloaded and their script nodes have run (engine.rs `ToolOut::finish`). Call it without
// holding the engine lock; an answer with nothing pending is returned as it is.
func (o ToolOut) Finish(e *Engine) ToolOut {
	p := o.pending
	o.pending = pendingOut{}
	until := time.Now().Add(settleWait)
	redraw := len(p.icons) > 0 || len(p.downloads) > 0 || p.runs != nil && !p.runs.IsEmpty()
	if p.runs != nil {
		p.runs.Run()
	}
	for _, job := range p.exports {
		if job.Runs != nil {
			job.Runs.Run()
		}
	}
	o.Text = e.settleIcons(o.Text, p.icons, until)
	waits := append([]string(nil), p.downloads...)
	for _, job := range p.exports {
		waits = append(waits, job.Waits...)
	}
	e.WaitDownloads(waits, until)
	for _, s := range p.shots {
		tree := s.tree
		// The tree was built while icons or images were still downloading, so it has none of them: redraw it
		// from the same document, not whatever the live one is now.
		if p.callDoc != nil && redraw {
			target := shotTarget(p.callDoc, s.id)
			e.Locked(func() {
				// A script node whose output is gone from the cache meanwhile draws as running, not under the lock.
				if fresh, err := e.exportCtx(p.callDoc, p.callLibs, &script.Runs{}).NodeTreeAt(target, s.scale); err == nil {
					tree = fresh
				}
			})
		}
		png, err := export.EncodeTree(tree, "png", s.scale, 0)
		if err != nil {
			o.Text += fmt.Sprintf("Screenshot of %s failed: %s\n", s.id, err)
			continue
		}
		o.Images = append(o.Images, base64.StdEncoding.EncodeToString(png))
	}
	for _, job := range p.exports {
		// Planned without the icons or images still downloading then: plan it again from the same document.
		if p.callDoc != nil && job.Stale() {
			var err error
			e.Locked(func() { job, err = job.Replan(e.exportCtx(p.callDoc, p.callLibs, nil), e.DataDir) })
			if err != nil {
				o.Text += fmt.Sprintf("\nExport failed: %s\n", err)
				continue
			}
		}
		files, err := job.Run()
		if err != nil {
			o.Text += fmt.Sprintf("\nExport failed: %s\n", err)
			continue
		}
		o.Text += fmt.Sprintf("\nExported: %s\n", strings.Join(files, ", "))
	}
	return o
}

// settleIcons waits (the lock is free between polls, so the downloads can land) for the icons a call used, and
// adds the warning of each that is not in its library to text's issues list.
func (e *Engine) settleIcons(text string, icons []script.Icon, until time.Time) string {
	var missing []string
	for _, ic := range icons {
		weight := float64(ic.Weight)
		loading := func() (b bool) {
			e.Locked(func() { b = e.Assets.IconLoading(ic.Library, ic.Name, weight) })
			return b
		}
		for loading() && time.Now().Before(until) {
			time.Sleep(pollEvery)
		}
		found := false
		e.Locked(func() {
			_, has := e.Assets.IconWeight(ic.Library, ic.Name, weight)
			found = has || e.Assets.IconLoading(ic.Library, ic.Name, weight)
		})
		if !found {
			missing = append(missing, ic.Warning)
		}
	}
	if len(missing) == 0 {
		return text
	}
	const head = "## Issues detected (fix them in the next call)\n"
	var bullets strings.Builder
	for _, w := range missing {
		bullets.WriteString(script.IssueLine(w))
	}
	at := strings.Index(text, head)
	if at < 0 {
		return text + "\n" + head + bullets.String()
	}
	end := at + len(head)
	for strings.HasPrefix(text[end:], "- ") {
		if i := strings.IndexByte(text[end:], '\n'); i >= 0 {
			end += i + 1
		} else {
			end = len(text)
		}
	}
	return text[:end] + bullets.String() + text[end:]
}

// WaitDownloads waits until none of these downloads (icon keys, image urls) is running or until passes. The lock
// is free between polls, so the downloads can land: call it without holding the engine lock.
func (e *Engine) WaitDownloads(keys []string, until time.Time) {
	for _, key := range keys {
		loading := func() (b bool) {
			e.Locked(func() { b = e.Assets.Loading(key) })
			return b
		}
		for loading() && time.Now().Before(until) {
			time.Sleep(pollEvery)
		}
	}
}

// fmax, fmin and fclamp are Rust's f64 max, min and clamp: a NaN operand loses (clamp keeps NaN).
func fmax(a, b float64) float64 {
	if a != a || b > a {
		return b
	}
	return a
}

func fmin(a, b float64) float64 {
	if a != a || b < a {
		return b
	}
	return a
}

func fclamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
