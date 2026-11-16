package engine

import (
	"fmt"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/render"
	"github.com/naddrzz/nalarupa/core-go/resolve"
	"github.com/naddrzz/nalarupa/core-go/script"
	"github.com/naddrzz/nalarupa/core-go/svg"
	"github.com/naddrzz/nalarupa/core-go/text"
)

// host is the engine as the execute sandbox's host (script.rs's natives on `Engine`). Its methods are the
// engine's under the names script.Host gives them; the engine's own Doc, Fonts and Assets fields would clash
// with them on Engine itself. The caller holds the engine lock.
type host struct{ e *Engine }

var _ script.Host = host{}

// Host is the engine as script.Host.
func (e *Engine) Host() script.Host { return host{e} }

func (h host) Doc() *doc.Doc       { return h.e.Doc }
func (h host) Changed()            { h.e.Version++ }
func (h host) SlideIn(id string)   { h.e.SlideIn(id) }
func (h host) IDs() *doc.IDSource  { return h.e.ids }
func (h host) Fonts() *text.Fonts  { return h.e.Fonts }
func (h host) Assets() *svg.Assets { return h.e.Assets }

func (h host) LaidOut(index int) *resolve.RNode { return h.e.Ctx().LaidOut(index) }

func (h host) FindEmptySpace(w, hh float64, dir string, pad float64, anchor string) (float64, float64) {
	return h.e.FindEmptySpace(w, hh, dir, pad, anchor)
}

// Generate: the background download of a generated image and its ResolvePending come with phase 8c; until then
// the call fails with Rust's text for a Generate type it does not have, and no pending fill is set.
func (h host) Generate(kind, target, prompt, pending string) error {
	return fmt.Errorf("Generate type '%s' is not available in Nalarupa; use fills, icons and paths instead", kind)
}

// FindEmptySpace is a free spot of w×h toward dir on the synced canvas, next to the laid-out node anchor (none
// when "" or not found) or the whole document.
func (e *Engine) FindEmptySpace(w, h float64, dir string, pad float64, anchor string) (float64, float64) {
	e.Sync()
	var a *render.Flat
	if anchor != "" {
		a = e.Renderer.Find(anchor)
	}
	return render.FindEmptySpace(&e.Renderer, w, h, dir, pad, a)
}
