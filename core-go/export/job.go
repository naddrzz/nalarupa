package export

// The export job (html.rs `plan`, `ExportJob`): planning turns ids, a format and an output path into the files
// to write, with only layout and SVG trees built; Run rasterises, encodes and writes them, and needs no
// document state, so the engine calls it after releasing its lock.

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/script"
	"github.com/naddrzz/nalarupa/core-go/svg"
	"github.com/naddrzz/nalarupa/core-go/usvg"
)

// HTMLDocument is the HTML export of nodes (html.Document). Package html sets it when linked: it builds on
// this package, so this package cannot call it directly.
var HTMLDocument func(c *Ctx, ids []string, opts jsonv.Value) (string, error)

// Waiter waits until the downloads of keys (icon keys and image urls) land or until passes. The engine
// supplies it; nil does not wait.
type Waiter func(keys []string, until time.Time)

// Job is an export whose rasterising, encoding and writing still have to run.
type Job struct {
	files []jobFile
	// script: a script's export (ClaimScriptFiles), which creates new files and replaces only its own.
	script bool
	// Waits are the icon keys and image urls the files draw that were still downloading: they are drawn
	// without them, so the caller waits for these (without the lock) and plans again (Replan).
	Waits []string
	// Runs are the script runs the files drew as running: the caller runs them and plans again.
	Runs *script.Runs
	// What was asked for, for Replan.
	ids         []string
	format, out string
	opts        jsonv.Value
}

// jobFile is one file of a job and what it holds: exactly one of bytes, svg (with the image files it inlines
// once written), a raster tree, or PDF page trees.
type jobFile struct {
	path     string
	bytes    []byte
	svg      *string
	embedded svg.Embedded
	tree     *usvg.Tree
	format   string
	scale    float64
	quality  uint8
	pages    []*usvg.Tree
}

// Files is the paths the job writes, in order.
func (j *Job) Files() []string {
	out := make([]string, len(j.files))
	for i, f := range j.files {
		out[i] = f.path
	}
	return out
}

// Stale reports whether the files lack downloads or script output still to come: Settle, then plan again.
func (j *Job) Stale() bool {
	return len(j.Waits) > 0 || j.Runs != nil && !j.Runs.IsEmpty()
}

// Settle runs the script nodes and waits for the downloads (until until) the files need. Call it without
// holding the engine lock.
func (j *Job) Settle(wait Waiter, until time.Time) {
	if j.Runs != nil {
		j.Runs.Run()
	}
	if wait != nil && len(j.Waits) > 0 {
		wait(j.Waits, until)
	}
}

// Replan plans the same export again from c (once Waits have landed). A script's export stays one.
func (j *Job) Replan(c *Ctx, dataDir string) (*Job, error) {
	next, err := Plan(c, dataDir, j.ids, j.format, j.out, j.opts)
	if err != nil {
		return nil, err
	}
	next.script = j.script
	return next, nil
}

// writing orders writes: two requests for one path must not interleave them.
var writing sync.Mutex

// Run renders and writes every file and returns the written paths.
func (j *Job) Run() ([]string, error) {
	writing.Lock()
	defer writing.Unlock()
	var written []string
	for _, f := range j.files {
		if dir, _ := parent(f.path); dir != "" {
			if err := os.MkdirAll(dir, 0o777); err != nil {
				return written, err
			}
		}
		b, err := f.encode()
		if err != nil {
			return written, err
		}
		if j.script && !scriptWrote(f.path) {
			// Created exclusively: a file (or link) that appeared since the check is never replaced.
			w, err := os.OpenFile(f.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
			if err != nil {
				if errors.Is(err, os.ErrExist) {
					return written, taken(f.path)
				}
				return written, err
			}
			noteScriptWrote(f.path)
			_, err = w.Write(b)
			if cerr := w.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return written, err
			}
		} else if err := os.WriteFile(f.path, b, 0o666); err != nil {
			return written, err
		}
		written = append(written, f.path)
	}
	return written, nil
}

func (f *jobFile) encode() ([]byte, error) {
	switch {
	case f.svg != nil:
		return []byte(svg.InlineEmbedded(*f.svg, f.embedded)), nil
	case f.tree != nil:
		return EncodeTree(f.tree, f.format, f.scale, f.quality)
	case f.pages != nil:
		return EncodePDF(f.pages)
	}
	return f.bytes, nil
}

// Plan plans an export of nodes ids as format ("png", "jpeg"/"jpg", "webp", "svg", "pdf", "html-css",
// "html-tailwind" or "html") to out (a folder for one file per node, a file for pdf and html; relative to
// the document's folder, or to dataDir/exports for an unsaved document); Run writes the files. opts holds
// `scale` (default 2) and `quality` (default 90) for rasters and the html options. Nothing slow runs here:
// a Stale job is meant to Settle off the engine lock and be planned again first.
func Plan(c *Ctx, dataDir string, ids []string, format, out string, opts jsonv.Value) (*Job, error) {
	runs := &script.Runs{}
	files, waits, err := func() ([]jobFile, []string, error) {
		prev := c.Runs
		c.Runs = runs
		c.Assets.Record()
		defer func() { c.Runs = prev }()
		files, err := planFiles(c, dataDir, ids, format, out, opts)
		return files, c.Assets.Recorded(), err
	}()
	if err != nil {
		return nil, err
	}
	return &Job{files: files, Waits: waits, Runs: runs, ids: append([]string(nil), ids...), format: format, out: out,
		opts: jsonv.Clone(opts)}, nil
}

func planFiles(c *Ctx, dataDir string, ids []string, format, out string, opts jsonv.Value) ([]jobFile, error) {
	base := exportBase(c.Doc.Path, dataDir)
	if format == "jpg" {
		format = "jpeg"
	}
	var files []jobFile
	switch format {
	case "png", "jpeg", "webp", "svg":
		dir := outPath(base, out, "exports")
		scale, ok := jsonv.F64(jsonv.Get(opts, "scale"))
		if !ok {
			scale = 2
		}
		quality, ok := jsonv.F64(jsonv.Get(opts, "quality"))
		if !ok {
			quality = 90
		}
		q := uint8(min(max(quality, 1), 100))
		for _, id := range ids {
			f := jobFile{path: join(dir, FileStem(id)+"."+format)}
			if format == "svg" {
				markup, embedded, ok := svg.Export(c.Doc, id, c.Fonts, c.Assets, c.expand())
				if !ok {
					return nil, errors.New(c.Missing(id))
				}
				f.svg, f.embedded = &markup, embedded
			} else {
				s := min(max(scale, 0.05), 8)
				tree, err := c.NodeTreeAt(id, s)
				if err != nil {
					return nil, err
				}
				f.tree, f.format, f.scale, f.quality = tree, format, s, q
			}
			files = append(files, f)
		}
	case "pdf":
		// No ids = the whole deck: every visible top-level frame, in document order (hidden slides are
		// skipped, as in Present).
		if len(ids) == 0 {
			ids = nil // the caller's slice is never written
			for _, n := range c.Doc.Children() {
				if !jsonv.StrIs(jsonv.Get(n, "type"), "frame") || jsonv.IsFalse(jsonv.Get(n, "enabled")) {
					continue
				}
				if id, ok := jsonv.Str(jsonv.Get(n, "id")); ok {
					ids = append(ids, id)
				}
			}
		}
		if len(ids) == 0 {
			return nil, errors.New("nothing to export: the document has no visible top-level frames")
		}
		file := outPath(base, out, c.Doc.Title()+".pdf")
		pages, err := c.PDFTrees(ids)
		if err != nil {
			return nil, err
		}
		files = append(files, jobFile{path: file, pages: pages})
	case "html-css", "html-tailwind", "html":
		file := outPath(base, out, "export.html")
		if HTMLDocument == nil {
			return nil, errors.New("html export is not linked (import core-go/html)")
		}
		h, err := HTMLDocument(c, ids, opts)
		if err != nil {
			return nil, err
		}
		files = append(files, jobFile{path: file, bytes: []byte(h)})
	default:
		return nil, fmt.Errorf("unknown export format '%s' (use png, jpeg, webp, svg, pdf or html-css)", format)
	}
	return files, nil
}
