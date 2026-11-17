package export_test

// The export job tests of html.rs, plus the script claims, the path checks, file naming and a stale job.

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/export"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/resolve"
	"github.com/naddrzz/nalarupa/core-go/script"
	"github.com/naddrzz/nalarupa/core-go/svg"
)

// jobCtx is an offline export context for d, with its own asset cache.
func jobCtx(t *testing.T, d *doc.Doc) *export.Ctx {
	t.Helper()
	assets := svg.NewAssets(filepath.Join(t.TempDir(), "cache"))
	assets.Online = false
	return &export.Ctx{Doc: d, Fonts: fonts(t), Assets: assets}
}

// exportNow is html.rs's test `export`: plan, then run.
func exportNow(c *export.Ctx, data string, ids []string, format, out, opts string) ([]string, error) {
	job, err := export.Plan(c, data, ids, format, out, jsonv.MustParse(opts))
	if err != nil {
		return nil, err
	}
	return job.Run()
}

func insert(t *testing.T, d *doc.Doc, node string) string {
	t.Helper()
	id, err := d.Insert("", jsonv.MustParse(node), -1)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func loadDoc(t *testing.T, path string) *doc.Doc {
	t.Helper()
	d, err := doc.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	resolve.Migrate(d.Root)
	d.EnsureIDs()
	return d
}

func readFile(t *testing.T, f string) []byte {
	t.Helper()
	b, err := os.ReadFile(f)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

const tinyPNG = "iVBORw0KGgoAAAANSUhEUgAAAAUAAAAFCAYAAACNbyblAAAAHElEQVQI12P4//8/w38GIAXDIBKE0DHxgljNBAAO9TXL0Y4OHwAAAABJRU5ErkJggg=="

// svg_export_reads_images_after_planning: an SVG export inlines its image files when it is written, not while
// it is planned (under the engine lock). A file gone by then is left out, as is an SVG nested past the depth
// limit, and text that looks like a placeholder stays as written.
func TestSVGExportReadsImagesAfterPlanning(t *testing.T) {
	dir := t.TempDir()
	png, _ := base64.StdEncoding.DecodeString(tinyPNG)
	for _, f := range []string{"photo.png", "gone.png"} {
		if err := os.WriteFile(filepath.Join(dir, f), png, 0o666); err != nil {
			t.Fatal(err)
		}
	}
	deep := 6000
	svgText := `<svg xmlns="http://www.w3.org/2000/svg" width="8" height="8">` + strings.Repeat("<g>", deep) + `<rect width="5" height="5"/>` + strings.Repeat("</g>", deep) + `</svg>`
	if err := os.WriteFile(filepath.Join(dir, "deep.svg"), []byte(svgText), 0o666); err != nil {
		t.Fatal(err)
	}
	d := doc.New()
	d.Path = filepath.Join(dir, "doc.nala")
	var ids []string
	for i, f := range []string{"photo.png", "gone.png", "deep.svg"} {
		ids = append(ids, insert(t, d, fmt.Sprintf(`{"type": "rectangle", "x": %d, "y": 0, "width": 50, "height": 50,
			"fill": {"type": "image", "url": "./%s", "mode": "fill"}}`, i*60, f)))
	}
	job, err := export.Plan(jobCtx(t, d), filepath.Join(dir, "data"), ids, "svg", "out", jsonv.NewObject())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range export.PlannedSVG(job) {
		if s == "" || strings.Contains(s, "data:image/png") {
			t.Fatal("an SVG export plans an SVG and planning reads no image file")
		}
	}
	if err := os.Remove(filepath.Join(dir, "gone.png")); err != nil {
		t.Fatal(err)
	}
	files, err := job.Run()
	if err != nil {
		t.Fatal(err)
	}
	read := func(i int) string { return string(readFile(t, files[i])) }
	if !strings.Contains(read(0), "data:image/png;base64,") || strings.Contains(read(0), "nalarupa-embed-") {
		t.Error("the written SVG inlines the image")
	}
	if strings.Contains(read(1), "gone.png") || strings.Contains(read(1), "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAUA") {
		t.Error("a file gone before the write is left out, not linked by its local path")
	}
	for i := range files {
		if strings.Contains(read(i), `xlink:href=""`) {
			t.Error("a left-out image never gets an empty href")
		}
	}
	if strings.Contains(read(2), "deep.svg") || strings.Contains(read(2), "data:image/svg") {
		t.Error("an SVG nested too deep is left out, as when rendering")
	}
	fake := svg.Embedded{Tag: "nalarupa-embed-0000000000000001:", Hrefs: []string{filepath.Join(dir, "photo.png")}}
	if got := svg.InlineEmbedded("nalarupa-embed:0 and nalarupa-embed-0000000000000002:0", fake); got != "nalarupa-embed:0 and nalarupa-embed-0000000000000002:0" {
		t.Errorf("only this export's keyed tokens are replaced: %s", got)
	}
}

// pdfPageCount counts the pages of a PDF (pdfPage does not match `/Pages`).
func pdfPageCount(b []byte) int {
	return len(pdfPage.FindAll(b, -1))
}

// exports_slides_deck_and_rasters: the deck is one page per visible top-level frame; jpg is JPEG; unknown
// formats fail.
func TestExportsSlidesDeckAndRasters(t *testing.T) {
	out := t.TempDir()
	d := loadDoc(t, filepath.Join(jobDir, "live", "slides.pen"))
	d.Path = "" // as a template: unsaved
	c := jobCtx(t, d)
	data := t.TempDir()
	files, err := exportNow(c, data, nil, "pdf", filepath.Join(out, "deck.pdf"), `{}`)
	if err != nil {
		t.Fatal(err)
	}
	pdf := readFile(t, files[0])
	if !bytes.HasPrefix(pdf, []byte("%PDF-")) || pdfPageCount(pdf) != 3 {
		t.Fatalf("one page per top-level frame: %d", pdfPageCount(pdf))
	}
	if err := d.Update("sld02", jsonv.MustParse(`{"enabled": false}`)); err != nil {
		t.Fatal(err)
	}
	files, err = exportNow(c, data, nil, "pdf", filepath.Join(out, "partial.pdf"), `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if n := pdfPageCount(readFile(t, files[0])); n != 2 {
		t.Errorf("a hidden slide is skipped: %d pages", n)
	}
	if err := d.Update("sld02", jsonv.MustParse(`{"enabled": null}`)); err != nil {
		t.Fatal(err)
	}
	ids := []string{"sld01"}
	jpg, err := exportNow(c, data, ids, "jpg", out, `{"scale": 0.25}`)
	if err != nil || filepath.Base(jpg[0]) != "sld01.jpeg" || !bytes.HasPrefix(readFile(t, jpg[0]), []byte{0xFF, 0xD8, 0xFF}) {
		t.Errorf("jpg is a JPEG named .jpeg: %v %v", jpg, err)
	}
	webp, err := exportNow(c, data, ids, "webp", out, `{"scale": 0.25}`)
	if err != nil {
		t.Fatal(err)
	}
	if b := readFile(t, webp[0]); !bytes.HasPrefix(b, []byte("RIFF")) || string(b[8:12]) != "WEBP" {
		t.Error("webp is a WebP")
	}
	if _, err := exportNow(c, data, ids, "tiff", "", `{}`); err == nil {
		t.Error("tiff is refused")
	}
	// With no path, an unsaved document's PDF goes to the data folder's exports, named after the title.
	files, err = exportNow(c, data, nil, "pdf", "", `{}`)
	if err != nil || files[0] != filepath.Join(data, "exports", "Untitled.pdf") {
		t.Errorf("default PDF path %v %v", files, err)
	}
}

// pdf_export_skips_damaged_images: a truncated or CRC-damaged image fill is left out of the PDF like in every
// other export, instead of failing the document.
func TestPDFExportSkipsDamagedImages(t *testing.T) {
	dir := t.TempDir()
	ok := readFile(t, filepath.Join(jobDir, "ok.png"))
	images := map[string][]byte{"ok.png": ok, "cut.png": ok[:len(ok)/2], "crc.png": readFile(t, filepath.Join(jobDir, "crc.png"))}
	// A WebP from the export itself, whole and cut.
	src := doc.New()
	pic := insert(t, src, `{"type": "rectangle", "x": 0, "y": 0, "width": 64, "height": 32, "fill": {"type": "gradient", "gradientType": "linear", "colors": [{"color": "#f00", "position": 0}, {"color": "#00f", "position": 1}]}}`)
	webp, err := exportNow(jobCtx(t, src), t.TempDir(), []string{pic}, "webp", t.TempDir(), `{"scale": 1}`)
	if err != nil {
		t.Fatal(err)
	}
	w := readFile(t, webp[0])
	images["ok.webp"], images["cut.webp"] = w, w[:len(w)/2]
	for name, b := range images {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o666); err != nil {
			t.Fatal(err)
		}
	}
	d := doc.New()
	c := jobCtx(t, d)
	for name := range images {
		url := filepath.ToSlash(filepath.Join(dir, name))
		id := insert(t, d, `{"type": "frame", "width": 120, "height": 80, "layout": "none", "children": [
			{"type": "rectangle", "x": 0, "y": 0, "width": 100, "height": 50, "fill": {"type": "image", "url": "`+url+`", "mode": "fill"}}]}`)
		for _, format := range []string{"png", "svg", "html-css", "pdf"} {
			if _, err := exportNow(c, t.TempDir(), []string{id}, format, filepath.Join(dir, name+"-"+format), `{}`); err != nil {
				t.Errorf("%s %s: %v", name, format, err)
			}
		}
	}
}

// hidden_layer_export_says_hidden: a hidden layer (or one inside a hidden parent) says so instead of "node not
// found", in every format.
func TestHiddenLayerExportSaysHidden(t *testing.T) {
	out := t.TempDir()
	d := loadDoc(t, filepath.Join(jobDir, "live", "slides.pen"))
	if err := d.Update("sld02", jsonv.MustParse(`{"enabled": false}`)); err != nil {
		t.Fatal(err)
	}
	child, _ := jsonv.Str(jsonv.Get(jsonv.Items(jsonv.Get(d.Find("sld02"), "children"))[0], "id"))
	c := jobCtx(t, d)
	for _, id := range []string{"sld02", child} {
		for _, format := range []string{"png", "svg", "pdf", "html-css"} {
			_, err := exportNow(c, t.TempDir(), []string{id}, format, out, `{}`)
			if err == nil || !strings.Contains(err.Error(), "hidden") || strings.Contains(err.Error(), "node not found") {
				t.Errorf("%s %s: %v", format, id, err)
			}
		}
		if _, err := c.NodePNG(id, 1); err == nil || !strings.Contains(err.Error(), "hidden") {
			t.Errorf("NodePNG %s: %v", id, err)
		}
	}
	if _, err := exportNow(c, t.TempDir(), []string{"nope"}, "png", out, `{}`); err == nil || !strings.Contains(err.Error(), "node not found: nope") {
		t.Errorf("missing: %v", err)
	}
}

// hidden_instance_child_export_says_hidden: a component child hidden through an instance override is addressed
// as `instanceId/childId`.
func TestHiddenInstanceChildExportSaysHidden(t *testing.T) {
	out := t.TempDir()
	d := doc.New()
	comp := insert(t, d, `{"type":"frame","reusable":true,"width":100,"height":50,"children":[{"id":"kid","type":"rectangle","width":10,"height":10}]}`)
	kid, _ := jsonv.Str(jsonv.Get(jsonv.Items(jsonv.Get(d.Find(comp), "children"))[0], "id"))
	inst := insert(t, d, `{"type":"ref","ref":"`+comp+`","descendants":{"`+kid+`":{"enabled":false}}}`)
	c := jobCtx(t, d)
	_, err := exportNow(c, t.TempDir(), []string{inst + "/" + kid}, "png", out, `{}`)
	if err == nil || !strings.Contains(err.Error(), "hidden") || strings.Contains(err.Error(), "node not found") {
		t.Errorf("hidden child: %v", err)
	}
	_, err = exportNow(c, t.TempDir(), []string{inst + "/nope"}, "png", out, `{}`)
	if err == nil || !strings.Contains(err.Error(), "node not found") || strings.Contains(err.Error(), "hidden") {
		t.Errorf("missing child: %v", err)
	}
}

// Files are named after the ids, one per node, under out; an empty selection writes nothing.
func TestExportFileNames(t *testing.T) {
	for id, want := range map[string]string{"abc": "abc", "a/b": "a_b", "../x": "___x", "é-ok_1": "_-ok_1", "a b.c": "a_b_c"} {
		if got := export.FileStem(id); got != want {
			t.Errorf("FileStem(%q) = %q, want %q", id, got, want)
		}
	}
	sep := string(filepath.Separator)
	for p, want := range map[string]string{"a" + sep + "b.pdf": "b.pdf", "a" + sep + "b" + sep: "b", "a" + sep + "..": "", "": "", "x" + sep + ".": "x"} {
		if got := export.FileName(p); got != want {
			t.Errorf("FileName(%q) = %q, want %q", p, got, want)
		}
	}
	d := loadDoc(t, filepath.Join(jobDir, "deck.pen"))
	out := t.TempDir()
	job, err := export.Plan(jobCtx(t, d), t.TempDir(), []string{"s1", "inst/kid", "s1"}, "webp", out, jsonv.NewObject())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{out + sep + "s1.webp", out + sep + "inst_kid.webp", out + sep + "s1.webp"}
	if got := job.Files(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("files %v, want %v", got, want)
	}
	if job, err := export.Plan(jobCtx(t, d), t.TempDir(), nil, "png", out, jsonv.NewObject()); err != nil || len(job.Files()) != 0 {
		t.Errorf("no ids: %v %v", job, err)
	}
}

// A script's export: a bad name, the policy, a file it did not write, and its own files again.
func TestScriptExportClaims(t *testing.T) {
	dir := t.TempDir()
	d := loadDoc(t, filepath.Join(jobDir, "deck.pen"))
	d.Path = filepath.Join(dir, "deck.pen")
	data := t.TempDir()
	check := func(format, out string, policy export.PathPolicy) error {
		return export.CheckScriptExport(d, data, format, out, policy)
	}
	for _, c := range []struct{ format, out, want string }{
		{"pdf", "deck.txt", "is not a pdf file name; use a .pdf path"},
		{"pdf", ".pdf", "is not a pdf file name"},
		{"html", "page", "is not a html file name; use a .html path"},
		{"html-css", ".a.html", "is not a html-css file name"},
		{"png", ".hidden", "is not a png file name; use a .html path"},
	} {
		if err := check(c.format, c.out, nil); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s %s: %v, want %q", c.format, c.out, err, c.want)
		}
	}
	for _, c := range []struct{ format, out string }{{"pdf", ""}, {"pdf", "x.PDF"}, {"html", "a.HTM"}, {"html-tailwind", ""}, {"svg", ""}, {"png", "shots"}} {
		if err := check(c.format, c.out, nil); err != nil {
			t.Errorf("%s %q: %v", c.format, c.out, err)
		}
	}
	// The policy gets the file, the base and the document; refused, it names the base.
	var asked []string
	err := check("pdf", "", func(file, base, docPath string) bool {
		asked = []string{file, base, docPath}
		return false
	})
	if want := []string{filepath.Join(dir, "deck.pdf"), dir, d.Path}; strings.Join(asked, "|") != strings.Join(want, "|") {
		t.Errorf("policy asked %v, want %v", asked, want)
	}
	if want := "Export writes only inside " + dir + " and never over the open document; ask the user to export elsewhere"; err == nil || err.Error() != want {
		t.Errorf("refused: %v", err)
	}
	// An unsaved document's script exports are checked against the data folder's exports.
	if err := export.CheckScriptExport(doc.New(), data, "pdf", "", func(file, base, _ string) bool {
		return file == filepath.Join(data, "exports", "Untitled.pdf") && base == filepath.Join(data, "exports")
	}); err != nil {
		t.Errorf("unsaved: %v", err)
	}

	c := jobCtx(t, d)
	plan := func(ids []string, format, out string) *export.Job {
		t.Helper()
		job, err := export.Plan(c, data, ids, format, out, jsonv.MustParse(`{"scale": 1}`))
		if err != nil {
			t.Fatal(err)
		}
		return job
	}
	// A new file is claimed and written; a second script export of it may replace it (it wrote it).
	for range 2 {
		job := plan([]string{"r"}, "png", "shots")
		if err := export.ClaimScriptFiles(job); err != nil {
			t.Fatal(err)
		}
		if _, err := job.Run(); err != nil {
			t.Fatal(err)
		}
	}
	// A file it did not write is refused at the claim, and one that appears after the claim at the write.
	other := filepath.Join(dir, "other.html")
	if err := os.WriteFile(other, []byte("mine"), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := export.ClaimScriptFiles(plan([]string{"s1"}, "html", "other.html")); err == nil || !strings.Contains(err.Error(), "other.html already exists and Export did not write it") {
		t.Errorf("claim of an existing file: %v", err)
	}
	job := plan([]string{"s1"}, "html", "late.html")
	if err := export.ClaimScriptFiles(job); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "late.html"), []byte("mine"), 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := job.Run(); err == nil || !strings.Contains(err.Error(), "late.html already exists") {
		t.Errorf("a file that appeared after the claim: %v", err)
	}
	if string(readFile(t, filepath.Join(dir, "late.html"))) != "mine" {
		t.Error("the file that appeared is kept")
	}
	// A replanned script export stays one.
	again, err := job.Replan(c, data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := again.Run(); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("replanned script export replaced a file: %v", err)
	}
	// Not a script's export: it replaces the file.
	if _, err := plan([]string{"s1"}, "html", "other.html").Run(); err != nil || string(readFile(t, other)) == "mine" {
		t.Errorf("a user's export replaces the file: %v", err)
	}
}

// A link counts as an existing file even when it dangles.
func TestScriptExportRefusesLinks(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink(filepath.Join(dir, "nowhere.html"), filepath.Join(dir, "link.html")); err != nil {
		t.Skip("cannot create a symlink here:", err)
	}
	d := loadDoc(t, filepath.Join(jobDir, "deck.pen"))
	d.Path = filepath.Join(dir, "deck.pen")
	job, err := export.Plan(jobCtx(t, d), t.TempDir(), []string{"s1"}, "html", "link.html", jsonv.NewObject())
	if err != nil {
		t.Fatal(err)
	}
	if err := export.ClaimScriptFiles(job); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("dangling link: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "nowhere.html")); !errors.Is(err, os.ErrNotExist) {
		t.Error("nothing was written through the link")
	}
}

// A script node's run is noted while planning (nothing waits for it), Settle runs it, and the replanned job
// draws its output.
func TestStaleJobSettlesAndReplans(t *testing.T) {
	dir := t.TempDir()
	js := fmt.Sprintf("/** @schema 2.19 */\n// %d\nreturn [{type: 'rectangle', width: nalarupa.width, height: nalarupa.height, fill: '#ff0000'}];", time.Now().UnixNano())
	if err := os.WriteFile(filepath.Join(dir, "red.js"), []byte(js), 0o666); err != nil {
		t.Fatal(err)
	}
	d, err := doc.FromJSON([]byte(`{"version": "2.19", "children": [
		{"type": "frame", "id": "page", "x": 0, "y": 0, "width": 40, "height": 40, "layout": "none", "fill": "#ffffff", "children": [
			{"type": "script", "id": "s", "x": 0, "y": 0, "width": 40, "height": 40, "scriptUri": "./red.js"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	d.Path = filepath.Join(dir, "doc.pen")
	c := jobCtx(t, d)
	data := t.TempDir()
	job, err := export.Plan(c, data, []string{"page"}, "svg", "out", jsonv.NewObject())
	if err != nil {
		t.Fatal(err)
	}
	if !job.Stale() || len(job.Waits) != 0 {
		t.Fatalf("the script run is noted: stale %v, waits %v", job.Stale(), job.Waits)
	}
	if strings.Contains(export.PlannedSVG(job)[0], `fill="#ff0000"`) {
		t.Fatal("planning drew the script's output without running it")
	}
	if c.Runs != nil {
		t.Error("planning leaves the context's record mode as it was")
	}
	waited := false
	job.Settle(func([]string, time.Time) { waited = true }, time.Now())
	if waited {
		t.Error("nothing to wait for: the waiter is not called")
	}
	job, err = job.Replan(c, data)
	if err != nil {
		t.Fatal(err)
	}
	if job.Stale() {
		t.Fatal("the replanned job has what it needs")
	}
	files, err := job.Run()
	if err != nil {
		t.Fatal(err)
	}
	if s := string(readFile(t, files[0])); !strings.Contains(s, `fill="#ff0000"`) {
		t.Errorf("the script's output is in the export:\n%s", s)
	}
}

// Settle hands the waiter the keys still downloading and the deadline.
func TestSettleWaitsForDownloads(t *testing.T) {
	job := &export.Job{Waits: []string{"icon:x"}, Runs: &script.Runs{}}
	if !job.Stale() {
		t.Error("a job waiting for downloads is stale")
	}
	until := time.Now().Add(time.Second)
	var got []string
	var gotUntil time.Time
	job.Settle(func(keys []string, u time.Time) { got, gotUntil = keys, u }, until)
	if strings.Join(got, ",") != "icon:x" || !gotUntil.Equal(until) {
		t.Errorf("waited for %v until %v", got, gotUntil)
	}
	job.Settle(nil, until) // no waiter: no waiting
}

// A custom script hook gets the runs to note while planning, so its job is stale too.
func TestStaleJobWithScriptsHook(t *testing.T) {
	dir := t.TempDir()
	js := fmt.Sprintf("/** @schema 2.19 */\n// hook %d\nreturn [{type: 'rectangle', width: nalarupa.width, height: nalarupa.height, fill: '#00ff00'}];", time.Now().UnixNano())
	if err := os.WriteFile(filepath.Join(dir, "green.js"), []byte(js), 0o666); err != nil {
		t.Fatal(err)
	}
	d, err := doc.FromJSON([]byte(`{"version": "2.19", "children": [
		{"type": "frame", "id": "page", "x": 0, "y": 0, "width": 40, "height": 40, "layout": "none", "children": [
			{"type": "script", "id": "s", "x": 0, "y": 0, "width": 40, "height": 40, "scriptUri": "./green.js"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	d.Path = filepath.Join(dir, "doc.pen")
	c := jobCtx(t, d)
	var recs []*script.Runs
	c.Scripts = func(n *resolve.RNode, r *resolve.Resolver, rec *script.Runs) {
		recs = append(recs, rec)
		script.ExpandScripts(n, r, c.Fonts, d.Dir(), d.Path, false, rec)
	}
	job, err := export.Plan(c, t.TempDir(), []string{"page"}, "svg", "out", jsonv.NewObject())
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) == 0 || recs[0] == nil || !job.Stale() {
		t.Fatalf("the hook records into the job's runs: %v, stale %v", recs, job.Stale())
	}
	job.Settle(nil, time.Now())
	if job, err = job.Replan(c, t.TempDir()); err != nil || job.Stale() {
		t.Fatalf("replanned: %v, stale %v", err, job != nil && job.Stale())
	}
	if !strings.Contains(export.PlannedSVG(job)[0], `fill="#00ff00"`) {
		t.Error("the replanned job draws the hook's output")
	}
}

// A relative document path is its folder as written, joined twice onto a default destination as Rust's
// out_path does; a bare file name has an empty folder.
func TestRelativeDocumentBase(t *testing.T) {
	d := loadDoc(t, filepath.Join(jobDir, "deck.pen"))
	sep := string(filepath.Separator)
	abs, absDir := "/x/deck.pen", "/x"
	if runtime.GOOS == "windows" {
		abs, absDir = "C:/x/deck.pen", "C:/x"
	}
	for _, c := range []struct{ path, pdf, png string }{
		{filepath.Join("docs", "deck.pen"), "docs" + sep + "docs" + sep + "deck.pdf", "docs" + sep + "out" + sep + "s1.png"},
		{"docs/deck.pen", "docs" + sep + "docs" + sep + "deck.pdf", "docs" + sep + "out" + sep + "s1.png"},
		{"deck.pen", "deck.pdf", "out" + sep + "s1.png"},
		// Forward slashes stay as written.
		{abs, absDir + sep + "deck.pdf", absDir + sep + "out" + sep + "s1.png"},
	} {
		d.Path = c.path
		for format, want := range map[string]string{"pdf": c.pdf, "png": c.png} {
			out := "out"
			if format == "pdf" {
				out = ""
			}
			job, err := export.Plan(jobCtx(t, d), t.TempDir(), []string{"s1"}, format, out, jsonv.NewObject())
			if err != nil {
				t.Fatal(err)
			}
			if got := job.Files(); len(got) != 1 || got[0] != want {
				t.Errorf("%s %s: %v, want %s", c.path, format, got, want)
			}
		}
	}
}

// The script-written set keys a file by where the OS writes it: through a link, `..` comes after the link
// (POSIX), so `sub/link/..` is the link target's parent, and a user's file at `sub` stays protected.
func TestScriptWrittenFollowsLinksBeforeDotDot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows resolves `..` before links, as Rust's std::path::absolute does there")
	}
	dir := t.TempDir()
	for _, sub := range []string{"sub", filepath.Join("other", "deep")} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o777); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(dir, "other", "deep"), filepath.Join(dir, "sub", "link")); err != nil {
		t.Skip("cannot create a symlink here:", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "s1.png"), []byte("mine"), 0o666); err != nil {
		t.Fatal(err)
	}
	d := loadDoc(t, filepath.Join(jobDir, "deck.pen"))
	d.Path = filepath.Join(dir, "deck.pen")
	c := jobCtx(t, d)
	opts := jsonv.MustParse(`{"scale": 1}`)
	job, err := export.Plan(c, t.TempDir(), []string{"s1"}, "png", "sub/link/..", opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := export.ClaimScriptFiles(job); err != nil {
		t.Fatal(err)
	}
	if _, err := job.Run(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "other", "s1.png")); err != nil {
		t.Fatal("the OS wrote through the link:", err)
	}
	job, err = export.Plan(c, t.TempDir(), []string{"s1"}, "png", "sub", opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := export.ClaimScriptFiles(job); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("the user's sub/s1.png is not the file the script wrote: %v", err)
	}
}
