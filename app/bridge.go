package main

import (
	"encoding/json"
	"fmt"
	"image"
	"log"

	"github.com/naddrzz/nalarupa/core-go/agent"
	_ "github.com/naddrzz/nalarupa/core-go/editor"
	"github.com/naddrzz/nalarupa/core-go/engine"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

// Matrix6 represents core-go's 2D affine matrix [a, b, c, d, e, f]:
// x' = a*x + c*y + e, y' = b*x + d*y + f.
type Matrix6 [6]float64

// RectD is a rectangle in float64 document or screen coordinates.
type RectD struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

// CoreState mirrors engine.Engine.State().
type CoreState struct {
	Version      uint64   `json:"version"`
	Title        string   `json:"title"`
	Path         *string  `json:"path"`
	Dirty        bool     `json:"dirty"`
	Selection    []string `json:"selection"`
	AgentRunning bool     `json:"agentRunning"`
	Executing    bool     `json:"executing"`
	PenDrawing   bool     `json:"penDrawing"`
	MCPPort      int      `json:"mcpPort"`
	Animating    bool     `json:"animating"`
}

// InitResult is returned by the "init" command.
type InitResult struct {
	MCPPort   int       `json:"mcpPort"`
	MCPURL    string    `json:"mcpUrl"`
	DataDir   string    `json:"dataDir"`
	State     CoreState `json:"state"`
	Recent    []string  `json:"recent"`
	HasAPIKey bool      `json:"hasApiKey"`
}

// PollResult is returned by the "poll" command.
type PollResult struct {
	State  CoreState        `json:"state"`
	Events []map[string]any `json:"events"`
}

// Box represents a laid-out node box from frames, boxes, children_boxes, snap_targets, or bounds.
type Box struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	X           float64  `json:"x"`
	Y           float64  `json:"y"`
	W           float64  `json:"w"`
	H           float64  `json:"h"`
	Parent      *string  `json:"parent,omitempty"`
	Depth       int      `json:"depth,omitempty"`
	M           *Matrix6 `json:"m,omitempty"`
	Placeholder bool     `json:"placeholder,omitempty"`
	Pending     bool     `json:"pending,omitempty"`
}

// RectOf returns Box's local RectD.
func (b Box) RectOf() RectD {
	return RectD{X: b.X, Y: b.Y, W: b.W, H: b.H}
}

// LayerRow represents a flattened layer entry from the "layers" command.
type LayerRow struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Depth    int    `json:"depth"`
	Parent   string `json:"parent"`
	Index    int    `json:"index"`
	Layout   bool   `json:"layout"`
	Reusable bool   `json:"reusable"`
	Hidden   bool   `json:"hidden"`
	Children int    `json:"children"`
}

// LocalTransform holds local group bounds offset and orientation from "node".
type LocalTransform struct {
	BX       float64 `json:"bx"`
	BY       float64 `json:"by"`
	Rotation float64 `json:"rotation"`
	FlipX    bool    `json:"flipX"`
	FlipY    bool    `json:"flipY"`
}

// NodeDetail is returned by the "node" command.
type NodeDetail struct {
	Exists       bool            `json:"exists"`
	Node         map[string]any  `json:"node"`
	Inherited    map[string]any  `json:"inherited"`
	Box          *Box            `json:"box"`
	Local        *LocalTransform `json:"local"`
	Parent       string          `json:"parent"`
	ParentLayout string          `json:"parentLayout"`
	Variables    map[string]any  `json:"variables"`
}

// DropInfo is returned by "drop_target".
type DropInfo struct {
	ID     string      `json:"id"`
	Layout string      `json:"layout"`
	X      float64     `json:"x"`
	Y      float64     `json:"y"`
	W      float64     `json:"w"`
	H      float64     `json:"h"`
	M      *Matrix6    `json:"m,omitempty"`
	Index  *int        `json:"index,omitempty"`
	Line   *[4]float64 `json:"line,omitempty"`
}

// LiveCut is a rounded ancestor clip cut on a live node.
type LiveCut struct {
	X float64    `json:"x"`
	Y float64    `json:"y"`
	W float64    `json:"w"`
	H float64    `json:"h"`
	R [4]float64 `json:"r"`
}

// LiveNode is an active placeholder frame or pending image returned by "live".
type LiveNode struct {
	Box
	Radius [4]float64 `json:"radius"`
	Clip   *RectD     `json:"clip,omitempty"`
	Cuts   []LiveCut  `json:"cuts,omitempty"`
}

// CopyPayload is returned by "copy".
type CopyPayload struct {
	HTML string   `json:"html"`
	Text string   `json:"text"`
	IDs  []string `json:"ids"`
}

// Bridge wraps an in-process *engine.Engine without any FFI / DLL boundary.
type Bridge struct {
	eng *engine.Engine
}

// NewBridge creates and attaches an in-process *engine.Engine using dataDir (or engine.DataDir() if empty).
func NewBridge(dataDir string) *Bridge {
	if dataDir == "" {
		dataDir = engine.DataDir()
	}
	eng := engine.New(dataDir)
	agent.Attach(eng)
	return &Bridge{eng: eng}
}

// Engine returns the underlying *engine.Engine.
func (b *Bridge) Engine() *engine.Engine {
	return b.eng
}

// Shutdown flushes background chat writes and stops any active agents/subprocesses.
func (b *Bridge) Shutdown() {
	if b == nil || b.eng == nil {
		return
	}
	if err := agent.Shutdown(b.eng); err != nil {
		log.Printf("[core] shutdown: %v", err)
	}
}

// CallRaw dispatches cmd + args directly through engine.Dispatch and returns jsonv.Value.
func (b *Bridge) CallRaw(cmd string, args map[string]any) (jsonv.Value, error) {
	payload := make(map[string]any, len(args)+1)
	for k, v := range args {
		payload[k] = v
	}
	payload["cmd"] = cmd
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal %s: %w", cmd, err)
	}
	req, err := jsonv.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", cmd, err)
	}
	return b.eng.Dispatch(req)
}

// Call dispatches cmd + args and JSON-unmarshals the result into out (if out != nil).
func (b *Bridge) Call(cmd string, args map[string]any, out any) error {
	val, err := b.CallRaw(cmd, args)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	enc := jsonv.Marshal(val)
	if err := json.Unmarshal(enc, out); err != nil {
		return fmt.Errorf("decode %s: %w", cmd, err)
	}
	return nil
}

// TryCall dispatches cmd + args, logging any error and returning false on failure.
func (b *Bridge) TryCall(cmd string, args map[string]any, out any) bool {
	if err := b.Call(cmd, args, out); err != nil {
		log.Printf("[core] %s: %v", cmd, err)
		return false
	}
	return true
}

// RenderInto renders a frame of (w x h) physical pixels under the engine lock and copies the
// opaque RGBA8 pixels into *dst so the engine lock is released immediately.
func (b *Bridge) RenderInto(dst **image.RGBA, w, h int, camX, camY, zoom float64, bg [3]uint8, solo string, sharp bool) *image.RGBA {
	cw := max(1, min(8192, w))
	ch := max(1, min(8192, h))
	if *dst == nil || (*dst).Bounds().Dx() != cw || (*dst).Bounds().Dy() != ch {
		*dst = image.NewRGBA(image.Rect(0, 0, cw, ch))
	}
	b.eng.Lock()
	px := b.eng.Render(uint32(cw), uint32(ch), camX, camY, zoom, bg, solo, sharp)
	copy((*dst).Pix, px)
	b.eng.Unlock()
	return *dst
}

// ExecuteJS runs an execute tool JavaScript snippet against the open document via Engine.CallTool
// (which releases the engine lock during JS execution and settles outputs in Finish).
func (b *Bridge) ExecuteJS(input string) (engine.ToolOut, error) {
	raw, err := json.Marshal(map[string]any{"input": input})
	if err != nil {
		return engine.ToolOut{}, err
	}
	args, err := jsonv.Parse(raw)
	if err != nil {
		return engine.ToolOut{}, err
	}
	out := b.eng.CallTool("execute", args).Finish(b.eng)
	if out.Error {
		return out, fmt.Errorf("%s", out.Text)
	}
	return out, nil
}
