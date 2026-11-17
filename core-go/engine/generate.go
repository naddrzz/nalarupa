package engine

// `Generate("ai")` and `Generate("stock")` (script.rs `n_generate`): the fill already points at a pending url
// (the canvas shows a shimmer skeleton); a background download fetches a placeholder photo seeded by the prompt
// (there is no image-generation backend) and ResolvePending swaps the real image in when it lands.

import (
	"fmt"
	"os"
	"strings"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/svg"
)

// generate starts the download of the image fill pending stands for, under the engine lock. The file goes to the
// document's `images` folder (linked relative to the document) or, for an unsaved document, to the data
// folder's (linked by its absolute path); a photo already there is not fetched again.
func (e *Engine) generate(prompt, pending string) {
	seed := fmt.Sprintf("%x", svg.FxHash(prompt))
	e.Inflight[pending] = true
	dir, relative := doc.JoinPath(e.DataDir, "images"), false
	if d, ok := doc.ParentPath(e.Doc.Path); ok {
		dir, relative = doc.JoinPath(d, "images"), true
	}
	url := strings.TrimRight(e.ImageBase, "/") + "/seed/" + seed + "/1600/1000"
	client := e.Assets.Client
	go func() {
		result := ""
		defer func() {
			// A panic counts as a failed download, so the node settles.
			if recover() != nil {
				result = ""
			}
			e.guard(func() { e.ResolvePending(pending, result) })
		}()
		file := doc.JoinPath(dir, seed+".jpg")
		ok := exists(file)
		if !ok && doc.CreateDirAll(dir) == nil {
			if b, fetched := svg.Fetch(client, url); fetched {
				ok = os.WriteFile(file, b, 0o666) == nil
			}
		}
		switch {
		case !ok:
		case relative:
			result = "./images/" + seed + ".jpg"
		default:
			result = strings.ReplaceAll(file, `\`, "/")
		}
	}()
}
