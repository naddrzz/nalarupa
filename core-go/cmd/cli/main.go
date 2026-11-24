// Command cli is headless Nalarupa: render and export documents, run execute scripts, serve MCP, benchmark
// canvas rendering, and generate brand icons. Build it as out/nalarupa-cli with `bash build.sh cli` in core-go:
// like the core it needs `go generate ./shader/gpu` first and cgo (MinGW gcc as CC on Windows, where build.sh
// also links statically with -extldflags=-static).
//
//	nalarupa-cli render <doc.pen> <out-dir> [scale] [--offline]   one PNG per top-level node
//	nalarupa-cli svg <doc.pen> <node-id> <out.svg> [--offline]
//	nalarupa-cli html <doc.pen> <node-id> <out.html> [--offline]
//	nalarupa-cli exec <doc.pen> <script.js> [--save] [--seed <n>] [--globals <json>] [--preview] [--offline]
//	nalarupa-cli bench <doc.pen> [w] [h] [png-prefix]
//	nalarupa-cli icon <nalarupa.svg> <nalarupa-small.svg> <out-dir>
//	nalarupa-cli mcp                                              serve MCP until Ctrl+C
package main

import (
	"bytes"
	"cmp"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/naddrzz/nalarupa/core-go/agent"
	"github.com/naddrzz/nalarupa/core-go/engine"
	"github.com/naddrzz/nalarupa/core-go/export"
	"github.com/naddrzz/nalarupa/core-go/html"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/resolve"
	"github.com/naddrzz/nalarupa/core-go/resvg"
	"github.com/naddrzz/nalarupa/core-go/script"
	"github.com/naddrzz/nalarupa/core-go/skia"
	"github.com/naddrzz/nalarupa/core-go/svg"
	"github.com/naddrzz/nalarupa/core-go/usvg"
)

func main() {
	if err := run(os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		os.Exit(1)
	}
}

// flagFrom reports whether name is among args[from:].
func flagFrom(args []string, from int, name string) bool {
	return from < len(args) && slices.Contains(args[from:], name)
}

// value is the argument after flag name, if any.
func value(args []string, name string) (string, bool) {
	if i := slices.Index(args, name); i >= 0 && i+1 < len(args) {
		return args[i+1], true
	}
	return "", false
}

func run(args []string) error {
	return runWith(args, engine.Global)
}

func runWith(args []string, getEngine func() *engine.Engine) error {
	arg := func(i int) (string, error) {
		if i < len(args) {
			return args[i], nil
		}
		return "", errors.New("missing argument (see --help in cmd/cli/main.go)")
	}
	cmd, err := arg(1)
	if err != nil {
		return err
	}
	switch cmd {
	case "icon":
		return buildIcons(args, arg)
	case "render", "svg", "html":
		e := getEngine()
		agent.Attach(e)
		return export1(e, cmd, args, arg)
	case "exec":
		e := getEngine()
		agent.Attach(e)
		return execute(e, args, arg)
	case "bench":
		e := getEngine()
		agent.Attach(e)
		return bench(e, args, arg)
	case "mcp":
		e := getEngine()
		agent.Attach(e)
		port, err := agent.StartMCP(e)
		if err != nil {
			return err
		}
		e.Locked(func() { e.MCPPort = port })
		fmt.Printf("Nalarupa MCP at %s\n", agent.MCPURL(port))
		select {}
	default:
		return fmt.Errorf("unknown command '%s'", cmd)
	}
}

// export1 runs render, svg or html under the engine lock.
func export1(e *engine.Engine, cmd string, args []string, arg func(int) (string, error)) error {
	e.Lock()
	defer e.Unlock()
	if flagFrom(args, map[string]int{"render": 4, "svg": 5, "html": 5}[cmd], "--offline") {
		e.Offline()
	}
	path, err := arg(2)
	if err != nil {
		return err
	}
	if err := e.Open(path); err != nil {
		return err
	}
	ctx := &export.Ctx{Doc: e.Doc, Libs: e.Libs, Fonts: e.Fonts, Assets: e.Assets}
	if cmd == "render" {
		out, err := arg(3)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(out, 0o777); err != nil {
			return err
		}
		scale := 1.0
		if len(args) > 4 {
			if s, err := strconv.ParseFloat(args[4], 64); err == nil {
				scale = s
			}
		}
		for _, id := range e.SelectTopLevel() {
			t := time.Now()
			pngBytes, err := ctx.NodePNG(id, scale)
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(out, id+".png"), pngBytes, 0o666); err != nil {
				return err
			}
			fmt.Printf("%s.png  %s\n", id, time.Since(t))
		}
		return nil
	}
	id, err := arg(3)
	if err != nil {
		return err
	}
	to, err := arg(4)
	if err != nil {
		return err
	}
	var text string
	if cmd == "svg" {
		expand := func(n *resolve.RNode, r *resolve.Resolver) {
			script.ExpandScripts(n, r, e.Fonts, e.Doc.Dir(), e.Doc.Path, false, nil)
		}
		markup, embedded, ok := svg.Export(e.Doc, e.Libs, id, e.Fonts, e.Assets, expand)
		if !ok {
			return errors.New("node not found")
		}
		text = svg.InlineEmbedded(markup, embedded)
	} else if text, err = html.Document(ctx, []string{id}, jsonv.NewObject()); err != nil {
		return err
	}
	return os.WriteFile(to, []byte(text), 0o666)
}

// execute is `exec`. Flags: `--seed <n>` sets `__seed`, `--globals <json>` the globals earlier calls kept,
// `--preview` the 2 s budget of a streaming step, `--offline` no font or asset downloads.
func execute(e *engine.Engine, args []string, arg func(int) (string, error)) error {
	path, err := arg(2)
	if err != nil {
		return err
	}
	scriptPath, err := arg(3)
	if err != nil {
		return err
	}
	e.Lock()
	if slices.Contains(args, "--offline") {
		e.Offline()
	}
	if _, err := os.Stat(path); err == nil {
		if err := e.Open(path); err != nil {
			e.Unlock()
			return err
		}
	} else {
		e.LoadTemplate("empty")
	}
	src, err := os.ReadFile(scriptPath)
	if err != nil {
		e.Unlock()
		return err
	}
	call := jsonv.NewObject()
	call.Set("input", string(src))
	if seed, ok := value(args, "--seed"); ok {
		n, err := strconv.ParseUint(seed, 10, 64)
		if err != nil {
			e.Unlock()
			return fmt.Errorf("--seed: %w", err)
		}
		call.Set("__seed", jsonv.Number(strconv.FormatUint(n, 10)))
	}
	if globals, ok := value(args, "--globals"); ok {
		v, err := jsonv.Parse([]byte(globals))
		o := jsonv.Obj(v)
		if err != nil || o == nil {
			e.Unlock()
			return errors.New("--globals takes a JSON object")
		}
		if e.Script.Globals == nil {
			e.Script.Globals = map[string]*jsonv.Object{}
		}
		e.Script.Globals["agent"] = o
	}
	var out engine.ToolOut
	if slices.Contains(args, "--preview") {
		out = e.ExecuteWithin(call, script.PreviewTimeout)
	} else {
		out = e.Tool("execute", call)
	}
	globals := e.Script.Globals["agent"]
	if globals == nil {
		globals = jsonv.NewObject()
	}
	e.Unlock()
	out = out.Finish(e)
	fmt.Println(out.Text)
	fmt.Printf("=== globals: %s\n", jsonv.Marshal(globals))
	for i, img := range out.Images {
		b, err := base64.StdEncoding.DecodeString(img)
		if err != nil {
			return err
		}
		p := fmt.Sprintf("shot-%d.png", i)
		if err := os.WriteFile(p, b, 0o666); err != nil {
			return err
		}
		fmt.Printf("screenshot -> %s\n", p)
	}
	if slices.Contains(args, "--save") && !out.Error {
		var saved string
		e.Locked(func() { saved, err = e.Save(path) })
		if err != nil {
			return err
		}
		fmt.Printf("saved %s\n", saved)
	}
	if out.Error {
		return errors.New("execute failed")
	}
	return nil
}

func bench(e *engine.Engine, args []string, arg func(int) (string, error)) error {
	path, err := arg(2)
	if err != nil {
		return err
	}
	w, h := uint32(1600), uint32(1000)
	if len(args) > 3 {
		if v, err := strconv.ParseUint(args[3], 10, 32); err == nil && v > 0 {
			w = uint32(v)
		}
	}
	if len(args) > 4 {
		if v, err := strconv.ParseUint(args[4], 10, 32); err == nil && v > 0 {
			h = uint32(v)
		}
	}
	// Match Renderer.Render's 1..8192 pixel clamp so measurements and PNG encoding use identical dimensions.
	w, h = min(max(w, 1), 8192), min(max(h, 1), 8192)
	bg := [3]uint8{245, 245, 245}
	ms := func(t time.Time) float64 { return float64(time.Since(t).Microseconds()) / 1000.0 }

	e.Lock()
	defer e.Unlock()
	t := time.Now()
	if err := e.Open(path); err != nil {
		return err
	}
	e.Sync()
	flats := e.Renderer.Flats()
	fmt.Printf("open+sync        %8.1f ms   nodes %d\n", ms(t), len(flats))
	bx, by, bw, bh := 0.0, 0.0, 1000.0, 1000.0
	if b, ok := e.Renderer.Bounds(); ok {
		bx, by, bw, bh = b.X, b.Y, b.W, b.H
	}
	fit := min(float64(w)/bw, float64(h)/bh)
	for _, z := range []struct {
		label string
		zoom  float64
	}{{"fit", fit}, {"100%", 1.0}, {"200%", 2.0}} {
		cx := bx + bw/2.0 - float64(w)/2.0/z.zoom
		cy := by + bh/2.0 - float64(h)/2.0/z.zoom
		t := time.Now()
		e.Renderer.Render(w, h, cx, cy, z.zoom, bg, "", true)
		cold := ms(t)
		if len(args) > 5 {
			px := append([]byte(nil), e.Renderer.Render(w, h, cx, cy, z.zoom, bg, "", true)...)
			img := &image.NRGBA{Pix: px, Stride: int(w) * 4, Rect: image.Rect(0, 0, int(w), int(h))}
			var buf bytes.Buffer
			if err := png.Encode(&buf, img); err != nil {
				return err
			}
			out := strings.ReplaceAll(fmt.Sprintf("%s-%s.png", args[5], z.label), "%", "pct")
			if err := os.WriteFile(out, buf.Bytes(), 0o666); err != nil {
				return err
			}
		}
		t = time.Now()
		for i := range 10 {
			e.Renderer.Render(w, h, cx+float64(i)*8.0/z.zoom, cy, z.zoom, bg, "", false)
		}
		pan := ms(t) / 10.0
		t = time.Now()
		for i := range 10 {
			e.Renderer.Render(w, h, cx, cy, z.zoom*(1.0+float64(i)*0.03), bg, "", false)
		}
		live := ms(t) / 10.0
		fmt.Printf("%-5s cold %8.1f ms   pan %6.1f ms/frame   live zoom %6.1f ms/frame\n", z.label, cold, pan, live)
	}
	t = time.Now()
	for i := range 5 {
		e.Renderer.Render(w, h, bx, by, fit*(1.0+float64(i)*0.2), bg, "", true)
	}
	fmt.Printf("sharp zoom       %8.1f ms/frame\n", ms(t)/5.0)
	var textID string
	for _, f := range flats {
		if f.Type == "text" {
			textID = f.ID
			break
		}
	}
	if textID != "" {
		if f := e.Renderer.Find(textID); f != nil {
			fx, fy := f.X, f.Y
			e.Renderer.Render(w, h, fx-100.0, fy-100.0, 1.0, bg, "", true)
			t = time.Now()
			call := jsonv.NewObject()
			call.Set("input", fmt.Sprintf("Update(%q, {content: \"Edited\"})", textID))
			_ = e.Tool("execute", call)
			e.Sync()
			editSync := ms(t)
			t = time.Now()
			e.Renderer.Render(w, h, fx-100.0, fy-100.0, 1.0, bg, "", true)
			fmt.Printf("edit text        sync %6.1f ms   repaint %6.1f ms\n", editSync, ms(t))
		}
	}
	if os.Getenv("BENCH_LAYERS") != "" {
		type slowLayer struct {
			ms       float64
			id, name string
			w, h     float64
		}
		var slow []slowLayer
		order := append([]string(nil), e.Renderer.Order...)
		for _, id := range order {
			f := e.Renderer.Find(id)
			if f == nil {
				continue
			}
			fx, fy, fw, fh, fname := f.X, f.Y, f.W, f.H, f.Name
			t = time.Now()
			e.Renderer.Render(w, h, fx, fy, 2.0, bg, id, true)
			slow = append(slow, slowLayer{ms(t), id, fname, fw, fh})
		}
		slices.SortFunc(slow, func(a, b slowLayer) int { return cmp.Compare(b.ms, a.ms) })
		for _, s := range slow[:min(len(slow), 6)] {
			fmt.Printf("  layer %8.1f ms  %s %q %gx%g\n", s.ms, s.id, s.name, s.w, s.h)
		}
	}
	return nil
}

func buildIcons(args []string, arg func(int) (string, error)) error {
	fullPath, err := arg(2)
	if err != nil {
		return err
	}
	smallPath, err := arg(3)
	if err != nil {
		return err
	}
	outDir, err := arg(4)
	if err != nil {
		return err
	}
	fullSVG, err := os.ReadFile(fullPath)
	if err != nil {
		return err
	}
	smallSVG, err := os.ReadFile(smallPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(outDir, 0o777); err != nil {
		return err
	}
	raster := func(svgBytes []byte, size uint32) ([]byte, error) {
		opt := usvg.DefaultOptions()
		tree, err := usvg.FromData(svgBytes, &opt)
		if err != nil {
			return nil, err
		}
		pm, ok := skia.NewPixmap(size, size)
		if !ok {
			return nil, errors.New("invalid icon size")
		}
		s := float32(size) / tree.Size().Width()
		resvg.Render(tree, skia.TransformFromScale(s, s), pm.AsMut())
		img := &image.NRGBA{Pix: pm.TakeDemultiplied(), Stride: int(size) * 4, Rect: image.Rect(0, 0, int(size), int(size))}
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}
	type icoEntry struct {
		size uint32
		png  []byte
	}
	var ico []icoEntry
	for _, size := range []uint32{16, 20, 24, 32, 40, 48, 64, 128, 256, 512, 1024} {
		src := fullSVG
		if size <= 48 {
			src = smallSVG
		}
		pngBytes, err := raster(src, size)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(outDir, fmt.Sprintf("icon-%d.png", size)), pngBytes, 0o666); err != nil {
			return err
		}
		if size <= 256 {
			ico = append(ico, icoEntry{size, pngBytes})
		}
	}
	// ICONDIR, one ICONDIRENTRY per image (0 means 256), then the PNG blobs.
	out := []byte{0, 0, 1, 0}
	out = binary.LittleEndian.AppendUint16(out, uint16(len(ico)))
	offset := uint32(6 + 16*len(ico))
	for _, e := range ico {
		var d byte
		if e.size < 256 {
			d = byte(e.size)
		}
		out = append(out, d, d, 0, 0)
		out = binary.LittleEndian.AppendUint16(out, 1)
		out = binary.LittleEndian.AppendUint16(out, 32)
		out = binary.LittleEndian.AppendUint32(out, uint32(len(e.png)))
		out = binary.LittleEndian.AppendUint32(out, offset)
		offset += uint32(len(e.png))
	}
	for _, e := range ico {
		out = append(out, e.png...)
	}
	return os.WriteFile(filepath.Join(outDir, "nalarupa.ico"), out, 0o666)
}
