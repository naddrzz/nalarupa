package script_test

// Execute golden tests: each case in testdata/exec/cases.json copies a document and runs its steps in
// turn, comparing report text, error flag, globals and saved document with the committed goldens
// (testdata/exec/golden). Cases marked slow (the 30 s budget) are skipped with -short.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/script"
)

var execData = filepath.Join("..", "testdata", "exec")

type execStep struct {
	Script  string `json:"script"`
	Seed    uint64 `json:"seed"`
	Preview bool   `json:"preview"`
}

type execCase struct {
	Name  string     `json:"name"`
	Doc   string     `json:"doc"`
	Live  bool       `json:"live"`
	Slow  bool       `json:"slow"`
	Steps []execStep `json:"steps"`
}

type stepResult struct {
	Text    string          `json:"text"`
	Error   bool            `json:"error"`
	Globals json.RawMessage `json:"globals"`
}

type execResult struct {
	Steps []stepResult `json:"steps"`
	Saved string       `json:"saved"`
}

// copyDoc copies the case's document into a fresh folder: both sides save over their copy. With no name it is a
// path with no file yet, which `exec` opens as the empty template.
func copyDoc(t *testing.T, name string) string {
	t.Helper()
	if name == "" {
		return filepath.Join(t.TempDir(), "new.pen")
	}
	b, err := os.ReadFile(filepath.Join(execData, name))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, b, 0o666); err != nil {
		t.Fatal(err)
	}
	return path
}

// runGo runs the case through script.Execute as the CLI runs it through the engine.
func runGo(t *testing.T, c execCase) execResult {
	t.Helper()
	path := copyDoc(t, c.Doc)
	globals := jsonv.NewObject()
	var res execResult
	for _, s := range c.Steps {
		src, err := os.ReadFile(filepath.Join(execData, s.Script))
		if err != nil {
			t.Fatal(err)
		}
		var h *docHost
		if _, err := os.Stat(path); err == nil {
			h = openHost(t, path)
		} else {
			h = emptyHost(t)
		}
		args := jsonv.NewObject()
		args.Set("input", string(src))
		args.Set("__seed", jsonv.Number(strconv.FormatUint(s.Seed, 10)))
		call := script.Call{Globals: globals}
		if s.Preview {
			call.Budget = script.PreviewTimeout
		}
		out := script.Execute(h, args, call)
		if !out.Error {
			out.Globals.Each(func(k string, v jsonv.Value) { globals.Set(k, v) })
			h.doc.Root.Set("version", doc.FormatVersion)
			if err := h.doc.Save(path); err != nil {
				t.Fatal(err)
			}
		}
		res.Steps = append(res.Steps, stepResult{Text: out.Text, Error: out.Error, Globals: jsonv.Marshal(globals)})
	}
	res.Saved = savedText(t, path)
	return res
}

// sameJSON compares two JSON texts as values (key order and number spelling aside).
func sameJSON(a, b []byte) bool {
	var x, y any
	return json.Unmarshal(a, &x) == nil && json.Unmarshal(b, &y) == nil && reflect.DeepEqual(x, y)
}

func TestExecParity(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(execData, "cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []execCase
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	saved := map[string]string{}
	for _, c := range cases {
		if c.Live {
			continue
		}
		t.Run(c.Name, func(t *testing.T) {
			if c.Slow && testing.Short() {
				t.Skip("the 30 s budget is skipped with -short")
			}
			golden := filepath.Join(execData, "golden", c.Name+".json")
			var want execResult
			b, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(b, &want); err != nil {
				t.Fatal(err)
			}
			got := runGo(t, c)
			saved[c.Name] = got.Saved
			for i, w := range want.Steps {
				g := got.Steps[i]
				at := c.Steps[i].Script
				if g.Text != w.Text {
					t.Errorf("%s: text\n--- got\n%s\n--- want\n%s", at, g.Text, w.Text)
				}
				if g.Error != w.Error {
					t.Errorf("%s: error %v, want %v", at, g.Error, w.Error)
				}
				if !sameJSON(g.Globals, w.Globals) {
					t.Errorf("%s: globals\n got %s\nwant %s", at, g.Globals, w.Globals)
				}
			}
			if got.Saved != want.Saved {
				t.Errorf("saved document\n--- got\n%s\n--- want\n%s", got.Saved, want.Saved)
			}
		})
	}
	// The same seed gives the same ids, whatever the budget; another seed others.
	one, preview, two := saved["seed-1"], saved["seed-1-preview"], saved["seed-2"]
	if one != "" && preview != "" && two != "" && (one != preview || one == two) {
		t.Error("seed 1 must save what seed 1 with the preview budget saves, and seed 2 something else")
	}
}

// savedText is the document file after the case, "" when no call saved one.
func savedText(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}
