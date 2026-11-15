// Text parity with usvg 0.47: each document in testdata/usvg/text is parsed here and by `nalarupa-cli
// usvg-tree --fonts` with the fonts of this machine (the resolver the Rust core passes, offline), and the
// trees are compared as JSON with every f32 as its bit pattern: chunks and spans, positioned glyphs, and the
// flattened paths. The exports of testdata/svg/live/text.pen (svg.rs output) are compared the same way.
// Text depends on the installed fonts, so the comparison is live only: it is skipped without NALARUPA_CLI.
package usvg_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/svg"
	"github.com/naddrzz/nalarupa/core-go/text"
	"github.com/naddrzz/nalarupa/core-go/usvg"
)

var textCorpus = filepath.Join("..", "testdata", "usvg", "text")

func textLive(t *testing.T) string {
	cli := os.Getenv("NALARUPA_CLI")
	if cli == "" {
		t.Skip("text parity is live only: set NALARUPA_CLI to the release nalarupa-cli")
	}
	// A file in the module written during the run keeps the test cache from replaying a live pass.
	if err := os.WriteFile(".parity-live", nil, 0o666); err != nil {
		t.Fatal(err)
	}
	return cli
}

// compareText parses file with the CLI and with Go, both with the fonts of cache, and compares the trees.
func compareText(t *testing.T, cli, file, cache string, fonts *text.Fonts) {
	t.Helper()
	var stderr bytes.Buffer
	cmd := exec.Command(cli, "usvg-tree", file, "--resources", filepath.Dir(file), "--fonts", cache)
	cmd.Stderr = &stderr
	want, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s usvg-tree: %v\n%s", cli, err, stderr.String())
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	opt := usvg.DefaultOptions()
	opt.ResourcesDir = filepath.Dir(file)
	opt.ImageHrefResolver = svg.ImageHrefResolver()
	opt.FontDB = fonts.Database()
	opt.FontResolver = usvg.FontsResolver(fonts)
	var got any
	if tree, err := usvg.FromData(data, &opt); err != nil {
		got = dumpError(err)
	} else {
		textDB = tree.FontDB()
		got = dumpTree(tree)
	}
	if path, g, w := diff(t, roundTrip(t, got), roundTrip(t, want)); path != "" {
		t.Errorf("first difference at %s: got %s, want %s", path, g, w)
	}
}

func TestTextParity(t *testing.T) {
	cli := textLive(t)
	docs, err := filepath.Glob(filepath.Join(textCorpus, "*.svg"))
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) < 30 {
		t.Fatalf("%d documents in %s, want at least 30", len(docs), textCorpus)
	}
	cache := t.TempDir()
	fonts := text.New(cache)
	fonts.Online = false
	emoji := fonts.HasFamily("Noto Emoji")
	for _, file := range docs {
		stem := strings.TrimSuffix(filepath.Base(file), ".svg")
		t.Run(stem, func(t *testing.T) {
			if strings.HasPrefix(stem, "emoji") && !emoji {
				// Without monochrome Noto Emoji, emoji fall back to a color font (COLR glyphs), which the port
				// does not draw.
				t.Skip("Noto Emoji is not installed")
			}
			compareText(t, cli, file, cache, fonts)
		})
	}
}

// TestTextParityExports compares the trees of svg.rs's exports of the live text fixtures.
func TestTextParityExports(t *testing.T) {
	cli := textLive(t)
	fixture := filepath.Join("..", "testdata", "svg", "live", "text.pen")
	d, err := doc.Load(fixture)
	if err != nil {
		t.Fatal(err)
	}
	data := t.TempDir()
	cache := filepath.Join(data, "fonts")
	fonts := text.New(cache)
	fonts.Online = false
	for _, n := range d.Children() {
		id, ok := jsonv.Str(jsonv.Get(n, "id"))
		if !ok {
			continue
		}
		t.Run(id, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), id+".svg")
			var stderr bytes.Buffer
			cmd := exec.Command(cli, "svg", fixture, id, out, "--offline")
			cmd.Env = append(os.Environ(), "NALARUPA_DATA="+data)
			cmd.Stderr = &stderr
			if err := cmd.Run(); err != nil {
				t.Fatalf("%s svg: %v\n%s", cli, err, stderr.String())
			}
			compareText(t, cli, out, cache, fonts)
		})
	}
}

// textDB is the font database of the tree being dumped, for the glyphs' faces.
var textDB *text.Database

var (
	fontStyles    = [...]string{"Normal", "Italic", "Oblique"}
	fontStretches = [...]string{"UltraCondensed", "ExtraCondensed", "Condensed", "SemiCondensed", "Normal",
		"SemiExpanded", "Expanded", "ExtraExpanded", "UltraExpanded"}
	dominantBaselines = [...]string{"Auto", "UseScript", "NoChange", "ResetSize", "Ideographic", "Alphabetic",
		"Hanging", "Mathematical", "Central", "Middle", "TextAfterEdge", "TextBeforeEdge"}
	alignmentBaselines = [...]string{"Auto", "Baseline", "BeforeEdge", "TextBeforeEdge", "Middle", "Central",
		"AfterEdge", "TextAfterEdge", "Ideographic", "Alphabetic", "Hanging", "Mathematical"}
	lengthAdjusts  = [...]string{"Spacing", "SpacingAndGlyphs"}
	textAnchors    = [...]string{"Start", "Middle", "End"}
	textRenderings = [...]string{"OptimizeSpeed", "OptimizeLegibility", "GeometricPrecision"}
	writingModes   = [...]string{"LeftToRight", "TopToBottom"}
	opticalSizings = [...]string{"auto", "none"}
)

func dumpFloats(v []float32) []any {
	out := make([]any, len(v))
	for i, x := range v {
		out[i] = dumpF(x)
	}
	return out
}

func dumpVariations(v []usvg.FontVariation) []any {
	out := make([]any, len(v))
	for i, x := range v {
		out[i] = []any{string(x.Tag[:]), dumpF(x.Value)}
	}
	return out
}

func dumpText(n *usvg.Text) map[string]any {
	chunks := make([]any, len(n.Chunks()))
	for ci, c := range n.Chunks() {
		spans := make([]any, len(c.Spans()))
		for si := range c.Spans() {
			s := &c.Spans()[si]
			font := s.Font()
			families := make([]any, len(font.Families()))
			for i, f := range font.Families() {
				families[i] = f.String()
			}
			deco := func(d *usvg.TextDecorationStyle) any {
				if d == nil {
					return nil
				}
				return map[string]any{"fill": dumpFill(d.Fill()), "stroke": dumpStroke(d.Stroke())}
			}
			shifts := make([]any, len(s.BaselineShift()))
			for i, b := range s.BaselineShift() {
				switch b.Kind {
				case usvg.BaselineShiftBaseline:
					shifts[i] = "baseline"
				case usvg.BaselineShiftSubscript:
					shifts[i] = "sub"
				case usvg.BaselineShiftSuperscript:
					shifts[i] = "super"
				default:
					shifts[i] = dumpF(b.Number)
				}
			}
			var textLength any
			if v, ok := s.TextLength(); ok {
				textLength = dumpF(v)
			}
			spans[si] = map[string]any{
				"start":      s.Start(),
				"end":        s.End(),
				"fill":       dumpFill(s.Fill()),
				"stroke":     dumpStroke(s.Stroke()),
				"paintOrder": paintOrders[s.PaintOrder()],
				"font": map[string]any{
					"families":   families,
					"style":      fontStyles[font.Style()],
					"stretch":    fontStretches[font.Stretch()],
					"weight":     font.Weight(),
					"variations": dumpVariations(font.Variations()),
				},
				"fontSize":      dumpF(s.FontSize()),
				"smallCaps":     s.SmallCaps(),
				"applyKerning":  s.ApplyKerning(),
				"opticalSizing": opticalSizings[s.FontOpticalSizing()],
				"decoration": map[string]any{
					"underline":   deco(s.Decoration().Underline()),
					"overline":    deco(s.Decoration().Overline()),
					"lineThrough": deco(s.Decoration().LineThrough()),
				},
				"dominantBaseline":  dominantBaselines[s.DominantBaseline()],
				"alignmentBaseline": alignmentBaselines[s.AlignmentBaseline()],
				"baselineShift":     shifts,
				"visible":           s.IsVisible(),
				"letterSpacing":     dumpF(s.LetterSpacing()),
				"wordSpacing":       dumpF(s.WordSpacing()),
				"textLength":        textLength,
				"lengthAdjust":      lengthAdjusts[s.LengthAdjust()],
			}
		}
		var x, y, flow any
		if v, ok := c.X(); ok {
			x = dumpF(v)
		}
		if v, ok := c.Y(); ok {
			y = dumpF(v)
		}
		if p := c.TextFlow(); p != nil {
			flow = map[string]any{"id": p.ID(), "startOffset": dumpF(p.StartOffset()), "segments": pathSegments(p.Path())}
		}
		chunks[ci] = map[string]any{"x": x, "y": y, "anchor": textAnchors[c.Anchor()], "text": c.Text(), "textFlow": flow, "spans": spans}
	}
	layouted := make([]any, len(n.Layouted()))
	for si := range n.Layouted() {
		s := &n.Layouted()[si]
		glyphs := make([]any, len(s.PositionedGlyphs()))
		for gi := range s.PositionedGlyphs() {
			g := &s.PositionedGlyphs()[gi]
			var font any
			if fam, path, index, ok := textDB.FaceSource(g.Font); ok {
				file := ""
				if path != "" {
					file = filepath.Base(path)
				}
				font = map[string]any{"family": fam, "file": file, "index": index}
			}
			glyphs[gi] = map[string]any{
				"id":               g.ID,
				"text":             g.Text,
				"font":             font,
				"fontSize":         dumpF(g.FontSize()),
				"transform":        dumpTS(g.Transform()),
				"outlineTransform": dumpTS(g.OutlineTransform()),
			}
		}
		deco := func(p *usvg.Path) any {
			if p == nil {
				return nil
			}
			return dumpPath(p)
		}
		layouted[si] = map[string]any{
			"fill":          dumpFill(s.Fill()),
			"stroke":        dumpStroke(s.Stroke()),
			"paintOrder":    paintOrders[s.PaintOrder()],
			"fontSize":      dumpF(s.FontSize()),
			"variations":    dumpVariations(s.Variations()),
			"opticalSizing": opticalSizings[s.FontOpticalSizing()],
			"visible":       s.IsVisible(),
			"glyphs":        glyphs,
			"underline":     deco(s.Underline()),
			"overline":      deco(s.Overline()),
			"lineThrough":   deco(s.LineThrough()),
		}
	}
	return map[string]any{
		"kind":          "text",
		"id":            n.ID(),
		"renderingMode": textRenderings[n.RenderingMode()],
		"writingMode":   writingModes[n.WritingMode()],
		"dx":            dumpFloats(n.Dx()),
		"dy":            dumpFloats(n.Dy()),
		"rotate":        dumpFloats(n.Rotate()),
		"absTransform":  dumpTS(n.AbsTransform()),
		"bbox":          dumpRect(n.BoundingBox()),
		"absBbox":       dumpRect(n.AbsBoundingBox()),
		"strokeBbox":    dumpRect(n.StrokeBoundingBox()),
		"absStrokeBbox": dumpRect(n.AbsStrokeBoundingBox()),
		"chunks":        chunks,
		"layouted":      layouted,
		"flattened":     dumpGroup(n.Flattened()),
	}
}
