// Command cli is headless Nalarupa (cli.rs, its user commands): render and export documents, run execute scripts,
// serve MCP. Build it as out/nalarupa-cli with `bash build.sh cli` in core-go: like the core it needs `go generate
// ./shader/gpu` first and cgo (MinGW gcc as CC on Windows, where build.sh also links statically with
// -extldflags=-static).
//
//	nalarupa-cli render <doc.pen> <out-dir> [scale] [--offline]   one PNG per top-level node
//	nalarupa-cli svg <doc.pen> <node-id> <out.svg> [--offline]
//	nalarupa-cli html <doc.pen> <node-id> <out.html> [--offline]
//	nalarupa-cli exec <doc.pen> <script.js> [--save] [--seed <n>] [--globals <json>] [--preview] [--offline]
//	nalarupa-cli mcp                                    serve MCP until Ctrl+C
//
// The parity oracle commands of the Rust CLI stay there.
package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"github.com/naddrzz/nalarupa/core-go/agent"
	"github.com/naddrzz/nalarupa/core-go/engine"
	"github.com/naddrzz/nalarupa/core-go/export"
	"github.com/naddrzz/nalarupa/core-go/html"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/resolve"
	"github.com/naddrzz/nalarupa/core-go/script"
	"github.com/naddrzz/nalarupa/core-go/svg"
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
	e := engine.Global()
	agent.Attach(e)
	switch cmd {
	case "render", "svg", "html":
		return export1(e, cmd, args, arg)
	case "exec":
		return execute(e, args, arg)
	case "mcp":
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
	// `--offline`: no font or asset downloads (the export parity oracles).
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
			png, err := ctx.NodePNG(id, scale)
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(out, id+".png"), png, 0o666); err != nil {
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
		// The script nodes run as a canvas export runs them, waiting for each.
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

// execute is `exec`. Test-only flags (the execute parity oracle): `--seed <n>` sets `__seed`, `--globals <json>` the
// globals earlier calls kept, `--preview` the 2 s budget of a streaming step, `--offline` no font or asset
// downloads. The globals after the call are printed on a line starting `=== globals: `.
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
