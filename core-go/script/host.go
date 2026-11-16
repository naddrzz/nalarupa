package script

import (
	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/resolve"
	"github.com/naddrzz/nalarupa/core-go/svg"
	"github.com/naddrzz/nalarupa/core-go/text"
)

// Host is what the execute sandbox needs from the engine. The engine implements it over its document,
// renderer, fonts and assets; everything else an execute call does around the run (the history commit, undo,
// events, streams, the AI Pen, the gate, screenshots and exports) stays with the engine. A Host is used from the
// goroutine running the call; in shared mode (execute_shared) the engine locks itself per call (see Locker).
type Host interface {
	// Doc is the document the script edits.
	Doc() *doc.Doc
	// Changed marks the document changed (`e.version += 1`).
	Changed()
	// SlideIn lets a node an Insert placed below the top level slide into place.
	SlideIn(id string)
	// LaidOut is the top-level node at index resolved and laid out, its script nodes run; nil when there is none.
	LaidOut(index int) *resolve.RNode
	// FindEmptySpace is render.rs `find_empty_space` on the synced renderer: a free spot of w×h toward dir,
	// next to the laid-out node anchor (none when "" or not found) or the whole document.
	FindEmptySpace(w, h float64, dir string, pad float64, anchor string) (x, y float64)
	// Generate starts the background download of the image fill the sandbox has just pointed at pending
	// (`Generate("ai")` and `Generate("stock")`), replacing it with the image once it lands. An error (a host
	// without downloads) fails the call with its text, which rolls the fill back.
	Generate(kind, target, prompt, pending string) error
	// IDs is the generator the call's ids come from: Execute makes it the document's (doc.Doc.IDs) for the call.
	IDs() *doc.IDSource
	// Fonts and Assets answer the validation's font and icon checks.
	Fonts() *text.Fonts
	Assets() *svg.Assets
}

// Locker is the engine lock of a shared-mode call (`execute_shared`): Execute is entered with it held, releases it
// while the JavaScript runs and takes it again around each native (Host is then used only under it), so polls,
// renders and Stop keep answering while a long script runs.
type Locker interface {
	Lock()
	Unlock()
}
