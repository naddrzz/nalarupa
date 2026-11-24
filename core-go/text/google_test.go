package text

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/naddrzz/nalarupa/core-go/layout"
)

// fakeGoogle stands in for the Google Fonts hosts. It answers request URIs from its routes (anything else is
// a 404), records every request and its user agent, and notes requests that overlap.
type fakeGoogle struct {
	*httptest.Server
	mu      sync.Mutex
	routes  map[string][]byte // request URI → 200 body
	hang    map[string]bool   // request URIs that never answer
	reqs    []string
	agents  []string
	busy    atomic.Int32
	overlap atomic.Bool
}

func newFake(t *testing.T, routes map[string][]byte) *fakeGoogle {
	t.Helper()
	g := &fakeGoogle{routes: map[string][]byte{}, hang: map[string]bool{}}
	for k, v := range routes {
		g.routes[k] = v
	}
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if g.busy.Add(1) > 1 {
			g.overlap.Store(true)
		}
		defer g.busy.Add(-1)
		g.mu.Lock()
		g.reqs = append(g.reqs, r.RequestURI)
		g.agents = append(g.agents, r.UserAgent())
		body, ok := g.routes[r.RequestURI]
		hang := g.hang[r.RequestURI]
		g.mu.Unlock()
		time.Sleep(5 * time.Millisecond) // widens the window an overlapping request would fall in
		if hang {
			select {
			case <-r.Context().Done():
			case <-time.After(10 * time.Second):
			}
			return
		}
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(body)
	}))
	t.Cleanup(g.Close)
	return g
}

func (g *fakeGoogle) set(uri string, body []byte) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.routes[uri] = body
}

func (g *fakeGoogle) requests() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.reqs)
}

func (g *fakeGoogle) downloader() *downloader {
	return &downloader{
		metadataURL: g.URL + "/metadata/fonts",
		githubURL:   g.URL + "/gh",
		cssURL:      g.URL + "/css2",
		client:      &http.Client{Timeout: 5 * time.Second},
	}
}

// metadata is the metadata endpoint's answer, anti-XSSI prefix included. A nil axes list leaves the key out.
func metadata(t *testing.T, families map[string][]string) []byte {
	t.Helper()
	type axis struct {
		Tag string  `json:"tag"`
		Min float64 `json:"min"`
		Max float64 `json:"max"`
	}
	type family struct {
		Family string  `json:"family"`
		Axes   *[]axis `json:"axes,omitempty"`
	}
	var list []family
	for name, tags := range families {
		fam := family{Family: name}
		if tags != nil {
			axes := []axis{}
			for _, tag := range tags {
				axes = append(axes, axis{tag, 100, 900})
			}
			fam.Axes = &axes
		}
		list = append(list, fam)
	}
	b, err := json.Marshal(map[string]any{"familyMetadataList": list})
	if err != nil {
		t.Fatal(err)
	}
	return append([]byte(")]}'\n"), b...)
}

// onlineFonts is an online Fonts without any face, using dl; OnReady reports to the returned channel.
func onlineFonts(t *testing.T, dl *downloader) (*Fonts, chan loadKey) {
	f := newFonts(newDB(), t.TempDir())
	f.Online, f.dl = true, dl
	ready := make(chan loadKey, 8)
	f.OnReady = func(family string, weight uint16, italic bool) { ready <- loadKey{family, weight, italic} }
	return f, ready
}

func next(t *testing.T, ready chan loadKey) loadKey {
	t.Helper()
	select {
	case k := <-ready:
		return k
	case <-time.After(60 * time.Second):
		t.Fatal("OnReady never fired")
		return loadKey{}
	}
}

func noMore(t *testing.T, ready chan loadKey) {
	t.Helper()
	select {
	case k := <-ready:
		t.Fatalf("unexpected OnReady %v", k)
	default:
	}
}

func sameRequests(t *testing.T, got []string, want ...string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("requests\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

func fileIs(t *testing.T, path string, want []byte) {
	t.Helper()
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, want) {
		t.Fatalf("%s: %v, %d bytes, want %d", path, err, len(got), len(want))
	}
}

func TestDownloadOffline(t *testing.T) {
	if !New(t.TempDir()).Online {
		t.Fatal("New is online, as in Rust")
	}
	g := newFake(t, nil)
	f, ready := onlineFonts(t, g.downloader())
	f.Online = false
	st := layout.TextStyle{Family: "Fam Name", Size: 16, Weight: 400}
	f.Ensure(st)
	f.Width("abc", st)
	if f.Loading("Fam Name") {
		t.Fatal("offline, nothing loads")
	}
	if !f.tried["Noto Sans|400|false"] {
		t.Fatal("the missing family falls back to Noto Sans")
	}
	noMore(t, ready)
	sameRequests(t, g.requests())
}

func TestDownloadVariable(t *testing.T) {
	family, _, data := systemFace(t, fonts(t))
	compact := compactFamily(family)
	g := newFake(t, map[string][]byte{
		"/metadata/fonts": metadata(t, map[string][]string{family: {"wght", "opsz"}}),
		"/gh/ofl/" + strings.ToLower(compact) + "/" + compact + "%5Bopsz,wght%5D.ttf": data,
	})
	f, ready := onlineFonts(t, g.downloader())
	st := layout.TextStyle{Family: family, Size: 16, Weight: 700}
	f.Ensure(st)
	if !f.Loading(strings.ToUpper(family)) {
		t.Fatal("the download is in flight")
	}
	if f.tried["Noto Sans|700|false"] {
		t.Fatal("Noto Sans is not fetched while the family loads")
	}
	if k := next(t, ready); k != (loadKey{family, 700, false}) {
		t.Fatalf("OnReady %v", k)
	}
	sameRequests(t, g.requests(), "/metadata/fonts", "/gh/ofl/"+strings.ToLower(compact)+"/"+compact+"%5Bopsz,wght%5D.ttf")
	fileIs(t, filepath.Join(f.cache, "var", compact+".ttf"), data)
	if f.HasFamily(family) {
		t.Fatal("the font loads only when Downloaded is called")
	}
	if !f.Downloaded(family, 700, false) || f.Generation != 1 || f.Loading(family) || !f.HasFamily(family) {
		t.Fatalf("after Downloaded: generation %d, loading %v, has %v", f.Generation, f.Loading(family), f.HasFamily(family))
	}
	if id, ok := f.face(st); !ok || !f.db.face(id).hasName(family) {
		t.Fatal("the family does not resolve to the downloaded face")
	}
	noMore(t, ready)
}

// A downloaded font is parsed for shaping on the download's goroutine: Downloaded, which the engine calls under
// its lock, only adds the faces (a CJK font's glyph table took a second to parse on a slow machine, and every
// call waited for it). Two weights of a variable family share its file: the second request's preparation is
// dropped, not kept, once the first one loaded the face.
func TestDownloadParsesOffTheOwner(t *testing.T) {
	family, _, data := systemFace(t, fonts(t))
	compact := compactFamily(family)
	g := newFake(t, map[string][]byte{
		"/metadata/fonts": metadata(t, map[string][]string{family: {"wght", "opsz"}}),
		"/gh/ofl/" + strings.ToLower(compact) + "/" + compact + "%5Bopsz,wght%5D.ttf": data,
	})
	f, ready := onlineFonts(t, g.downloader())
	f.Ensure(layout.TextStyle{Family: family, Size: 16, Weight: 400})
	f.Ensure(layout.TextStyle{Family: family, Size: 16, Weight: 700})
	first := next(t, ready)
	// Before Downloaded: the download's goroutine has already parsed the file.
	f.dl.preparedMu.Lock()
	p, ok := f.dl.prepared[varFile(f.cache, family, false)]
	f.dl.preparedMu.Unlock()
	if !ok || len(p.faces) == 0 || p.faces[0].shaper == nil {
		t.Fatal("the font was not parsed on the download's goroutine")
	}
	if !f.Downloaded(first.family, first.weight, first.italic) {
		t.Fatal("the download adds a face")
	}
	n := 0
	for _, x := range f.db.faces() {
		if x.hasName(family) {
			n++
			if x.shaper == nil {
				t.Fatal("the face is not the prepared one")
			}
		}
	}
	if n == 0 {
		t.Fatal("no face of the family")
	}
	second := next(t, ready)
	f.Downloaded(second.family, second.weight, second.italic)
	if n := f.dl.pending(); n != 0 {
		t.Fatalf("the downloader still holds %d preparations", n)
	}
	noMore(t, ready)
}

// A preparation that lands after its variable family was loaded (by the request for another weight, which shares
// the file) is dropped by its Downloaded, which finds the face already there.
func TestLatePreparationIsDropped(t *testing.T) {
	var family, path string
	if path = os.Getenv("NALARUPA_VARIABLE_FONT"); path != "" {
		db := newDB()
		db.loadFile(path)
		for _, x := range db.faces() {
			if x.isVariable() {
				family = x.families[0].name
				break
			}
		}
	} else {
		for _, x := range fonts(t).db.faces() {
			if x.src.path != "" && x.index == 0 && x.isVariable() {
				family, path = x.families[0].name, x.src.path
				break
			}
		}
	}
	if family == "" {
		t.Skip("no variable font: set NALARUPA_VARIABLE_FONT to one")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f := newFonts(newDB(), t.TempDir())
	f.dl = newDownloader()
	file := varFile(f.cache, family, false)
	if err := os.MkdirAll(filepath.Dir(file), 0o777); err != nil || os.WriteFile(file, data, 0o666) != nil {
		t.Fatal("cannot write the cache")
	}
	f.dl.prepare(file)
	if !f.Downloaded(family, 400, false) || f.dl.pending() != 0 {
		t.Fatalf("the first weight takes the preparation (%d left)", f.dl.pending())
	}
	f.dl.prepare(file)
	f.Downloaded(family, 700, false)
	if n := f.dl.pending(); n != 0 {
		t.Fatalf("a late preparation is kept: %d", n)
	}
}

// The license folders are tried in order; the metadata is fetched once, cached on disk, and serves the
// italic file name too.
func TestDownloadLicenseFallthrough(t *testing.T) {
	meta := metadata(t, map[string][]string{"Fam Name": {"wght"}})
	g := newFake(t, map[string][]byte{
		"/metadata/fonts":                              meta,
		"/gh/apache/famname/FamName%5Bwght%5D.ttf":     []byte("apache font"),
		"/gh/ufl/famname/FamName-Italic%5Bwght%5D.ttf": []byte("ufl italic"),
	})
	dl, cache := g.downloader(), t.TempDir()
	dl.download(cache, "Fam Name", 400, false)
	dl.download(cache, "Fam Name", 400, true)
	sameRequests(t, g.requests(),
		"/metadata/fonts",
		"/gh/ofl/famname/FamName%5Bwght%5D.ttf",
		"/gh/apache/famname/FamName%5Bwght%5D.ttf",
		"/gh/ofl/famname/FamName-Italic%5Bwght%5D.ttf",
		"/gh/apache/famname/FamName-Italic%5Bwght%5D.ttf",
		"/gh/ufl/famname/FamName-Italic%5Bwght%5D.ttf",
	)
	fileIs(t, filepath.Join(cache, "var", "FamName.ttf"), []byte("apache font"))
	fileIs(t, filepath.Join(cache, "var", "FamName-Italic.ttf"), []byte("ufl italic"))
	fileIs(t, filepath.Join(cache, "google-fonts-metadata.json"), meta)
	// Present files are kept without asking again.
	dl.download(cache, "Fam Name", 700, false)
	if n := len(g.requests()); n != 6 {
		t.Fatalf("%d requests after a repeat", n)
	}
	for _, a := range g.agents {
		if a != "nalarupa/0.1" {
			t.Fatalf("user agent %q", a)
		}
	}
}

// Families without axes (an empty list or none at all) use the CSS API, and the first url(...) is saved.
func TestDownloadStaticItalic(t *testing.T) {
	g := newFake(t, map[string][]byte{"/metadata/fonts": metadata(t, map[string][]string{"Fam Name": {}, "Other": nil})})
	g.set("/css2?family=Fam+Name:ital,wght@1,700", []byte("@font-face { src: url("+g.URL+"/s/fam.ttf) format('truetype'); }\n@font-face { src: url("+g.URL+"/s/other.ttf); }"))
	g.set("/s/fam.ttf", []byte("static italic"))
	dl, cache := g.downloader(), t.TempDir()
	dl.download(cache, "Fam Name", 700, true)
	sameRequests(t, g.requests(), "/metadata/fonts", "/css2?family=Fam+Name:ital,wght@1,700", "/s/fam.ttf")
	fileIs(t, filepath.Join(cache, "Fam_Name-700i.ttf"), []byte("static italic"))
	dl.download(cache, "Other", 400, false)
	if got := g.requests()[3]; got != "/css2?family=Other:wght@400" {
		t.Fatalf("a family without an axes key is static: %s", got)
	}
}

// A failed weighted CSS request is retried without the weight; a failed metadata fetch is not cached.
func TestDownloadCSSRetry(t *testing.T) {
	g := newFake(t, nil)
	g.set("/css2?family=Fam+Name", []byte("src: url("+g.URL+"/s/fam.ttf);"))
	g.set("/s/fam.ttf", []byte("any weight"))
	dl, cache := g.downloader(), t.TempDir()
	dl.download(cache, "Fam Name", 400, false)
	sameRequests(t, g.requests(), "/metadata/fonts", "/css2?family=Fam+Name:wght@400", "/css2?family=Fam+Name", "/s/fam.ttf")
	fileIs(t, filepath.Join(cache, "Fam_Name-400.ttf"), []byte("any weight"))
	if exists(filepath.Join(cache, "google-fonts-metadata.json")) {
		t.Fatal("a failed metadata fetch was cached")
	}
}

// Every request fails (404, or a timeout): nothing is written, Downloaded adds nothing, and Noto Sans is
// fetched in its place.
func TestDownloadAllFail(t *testing.T) {
	g := newFake(t, map[string][]byte{"/metadata/fonts": metadata(t, map[string][]string{"Fam Name": {"wght"}})})
	g.hang["/gh/ufl/famname/FamName%5Bwght%5D.ttf"] = true
	dl := g.downloader()
	dl.client.Timeout = 300 * time.Millisecond
	f, ready := onlineFonts(t, dl)
	f.Ensure(layout.TextStyle{Family: "Fam Name", Size: 16, Weight: 400})
	if k := next(t, ready); k != (loadKey{"Fam Name", 400, false}) {
		t.Fatalf("OnReady %v", k)
	}
	sameRequests(t, g.requests(),
		"/metadata/fonts",
		"/gh/ofl/famname/FamName%5Bwght%5D.ttf",
		"/gh/apache/famname/FamName%5Bwght%5D.ttf",
		"/gh/ufl/famname/FamName%5Bwght%5D.ttf",
		"/css2?family=Fam+Name:wght@400",
		"/css2?family=Fam+Name",
	)
	if f.Downloaded("Fam Name", 400, false) || f.Loading("Fam Name") || f.Generation != 0 {
		t.Fatalf("after a failed download: loading %v, generation %d", f.Loading("Fam Name"), f.Generation)
	}
	if k := next(t, ready); k != (loadKey{"Noto Sans", 400, false}) {
		t.Fatalf("Noto Sans is fetched instead, got %v", k)
	}
	if f.Downloaded("Noto Sans", 400, false) {
		t.Fatal("Noto Sans failed too")
	}
	entries, err := os.ReadDir(f.cache)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "google-fonts-metadata.json" {
			t.Errorf("left %s in the cache", e.Name())
		}
	}
	noMore(t, ready)
}

// Two Fonts sharing a downloader (as every Fonts shares the default one) never download at the same time.
func TestDownloadsOneAtATime(t *testing.T) {
	g := newFake(t, map[string][]byte{"/metadata/fonts": metadata(t, map[string][]string{"Fam A": {"wght"}, "Fam B": {"wght"}})})
	dl := g.downloader()
	a, readyA := onlineFonts(t, dl)
	b, readyB := onlineFonts(t, dl)
	a.Ensure(layout.TextStyle{Family: "Fam A", Size: 16, Weight: 400})
	b.Ensure(layout.TextStyle{Family: "Fam B", Size: 16, Weight: 400})
	next(t, readyA)
	next(t, readyB)
	if g.overlap.Load() {
		t.Fatal("two requests overlapped")
	}
	reqs := g.requests()
	if len(reqs) != 11 || slices.Index(reqs, "/metadata/fonts") != 0 || slices.Index(reqs[1:], "/metadata/fonts") >= 0 {
		t.Fatalf("metadata is read once per downloader: %v", reqs)
	}
}

func TestDownloadRepeat(t *testing.T) {
	g := newFake(t, map[string][]byte{"/metadata/fonts": metadata(t, map[string][]string{"Fam Name": {"wght"}})})
	f, ready := onlineFonts(t, g.downloader())
	st := layout.TextStyle{Family: "Fam Name", Size: 16, Weight: 400}
	f.Ensure(st)
	f.Ensure(st)
	f.Width("abc", st)
	if k := next(t, ready); k != (loadKey{"Fam Name", 400, false}) {
		t.Fatalf("OnReady %v", k)
	}
	// Downloaded clears the in-flight entry (and the failed family falls back to Noto Sans, which downloads
	// too); a repeat Ensure after it must not mark the style in flight again. fetch marks it before it
	// starts the goroutine, so this check needs no waiting.
	f.Downloaded("Fam Name", 400, false)
	if k := next(t, ready); k != (loadKey{"Noto Sans", 400, false}) {
		t.Fatalf("OnReady %v, want the Noto Sans fallback", k)
	}
	f.Downloaded("Noto Sans", 400, false)
	f.Ensure(st)
	if f.Loading("Fam Name") || len(f.loading) != 0 {
		t.Fatal("a repeat Ensure started another download")
	}
	noMore(t, ready)
	// One download of the family (6 requests) plus the Noto Sans fallback's two CSS requests.
	if n := len(g.requests()); n != 8 {
		t.Fatalf("%d requests, want 8: %v", n, g.requests())
	}
}

// Opt-in: downloads a variable and a static-only family from the real Google Fonts into a temporary cache,
// then measures with them on that cache.
func TestNetworkDownloadParity(t *testing.T) {
	if os.Getenv("NALARUPA_NETWORK") != "1" {
		t.Skip("reaches Google Fonts: set NALARUPA_NETWORK=1")
	}
	cache := t.TempDir()
	f := New(cache)
	f.dl = newDownloader() // reads the metadata into this cache
	ready := make(chan loadKey, 8)
	f.OnReady = func(family string, weight uint16, italic bool) { ready <- loadKey{family, weight, italic} }
	styles := []layout.TextStyle{{Family: "DM Sans", Size: 18, Weight: 500}, {Family: "Abril Fatface", Size: 18, Weight: 400}}
	for _, st := range styles {
		if f.HasFamily(st.Family) {
			t.Skipf("%s is installed, so nothing is downloaded", st.Family)
		}
	}
	for _, st := range styles {
		f.Ensure(st)
	}
	for range styles {
		k := next(t, ready)
		if !f.Downloaded(k.family, k.weight, k.italic) {
			t.Fatalf("%s did not download", k.family)
		}
	}
	if !exists(varFile(cache, "DM Sans", false)) || !exists(staticFile(cache, "Abril Fatface", 400, false)) {
		t.Fatal("the downloaded font files are not in the expected cache paths")
	}
	for _, st := range append(styles, layout.TextStyle{Family: "DM Sans", Size: 18, Weight: 800}) {
		if w := f.Width("Downloaded AVA fonts wrap here", st); w <= 0 {
			t.Fatalf("%v measured %v", st, w)
		}
	}
	w500 := f.Width("Downloaded AVA fonts wrap here", layout.TextStyle{Family: "DM Sans", Size: 18, Weight: 500})
	w800 := f.Width("Downloaded AVA fonts wrap here", layout.TextStyle{Family: "DM Sans", Size: 18, Weight: 800})
	if w500 == w800 {
		t.Fatal("DM Sans measures the same at 500 and 800: not the variable font")
	}
}

// Metadata already in the cache (anti-XSSI prefix included) is read from disk: no metadata request, and
// its axes still pick the variable font.
func TestDownloadMetadataCached(t *testing.T) {
	g := newFake(t, map[string][]byte{"/gh/ofl/famname/FamName%5Bopsz,wght%5D.ttf": []byte("var font")})
	cache := t.TempDir()
	if err := os.WriteFile(filepath.Join(cache, "google-fonts-metadata.json"), metadata(t, map[string][]string{"Fam Name": {"wght", "opsz"}}), 0o666); err != nil {
		t.Fatal(err)
	}
	g.downloader().download(cache, "Fam Name", 400, false)
	sameRequests(t, g.requests(), "/gh/ofl/famname/FamName%5Bopsz,wght%5D.ttf")
	fileIs(t, filepath.Join(cache, "var", "FamName.ttf"), []byte("var font"))
}

type panicTransport struct{}

func (panicTransport) RoundTrip(*http.Request) (*http.Response, error) { panic("broken download") }

// A download that panics does not take the process down: it is reported as a failed one.
func TestPanickingDownloadIsReportedFailed(t *testing.T) {
	dl := newDownloader()
	dl.metadataURL, dl.githubURL, dl.cssURL = "http://fonts.test/metadata", "http://fonts.test/github", "http://fonts.test/css2"
	dl.client = &http.Client{Transport: panicTransport{}}
	f, ready := onlineFonts(t, dl)
	f.Ensure(layout.TextStyle{Family: "Boom Sans", Weight: 400})
	k := next(t, ready)
	if f.Downloaded(k.family, k.weight, k.italic) || f.HasFamily("Boom Sans") {
		t.Fatal("nothing was downloaded")
	}
}
