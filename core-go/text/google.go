package text

// Google Fonts downloads, ported from text.rs (family_axes, download) and net.rs: the variable font from
// the google/fonts repository when the metadata lists axes for the family, else the static instance the
// CSS API links to. Files land in the same cache paths as Rust's, so either core reuses the other's.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// HTTP limits of net.rs.
const (
	fetchTimeout = 8 * time.Second
	userAgent    = "nalarupa/0.1"
	maxBody      = 32 << 20
)

// downloader fetches font files into a cache folder, one download at a time: they share the metadata
// file and a slow link. Its fields other than mu and axes are set before the first download.
type downloader struct {
	metadataURL string // the family list with axes, behind an anti-XSSI prefix
	githubURL   string // the google/fonts repository: {githubURL}/{license}/{dir}/{file}
	cssURL      string // the CSS API: {cssURL}?family=...
	client      *http.Client

	mu sync.Mutex
	// Axis tags per lowercase family, read once (nil until then).
	axes map[string][]string

	// prepared holds the faces a download parsed, by file, until Fonts.loadCached takes them.
	preparedMu sync.Mutex
	prepared   map[string]preparedFile
}

type preparedFile struct {
	data  []byte
	faces []*faceInfo
}

// prepare parses the first of files that exists (prepareFaces), for take.
func (d *downloader) prepare(files ...string) {
	for _, file := range files {
		b, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		faces := prepareFaces(b)
		d.preparedMu.Lock()
		if d.prepared == nil {
			d.prepared = map[string]preparedFile{}
		}
		d.prepared[file] = preparedFile{b, faces}
		d.preparedMu.Unlock()
		return
	}
}

// drop forgets the preparations of files (a Fonts without a downloader has none).
func (d *downloader) drop(files ...string) {
	if d == nil {
		return
	}
	d.preparedMu.Lock()
	defer d.preparedMu.Unlock()
	for _, file := range files {
		delete(d.prepared, file)
	}
}

// pending is how many preparations are waiting to be taken.
func (d *downloader) pending() int {
	d.preparedMu.Lock()
	defer d.preparedMu.Unlock()
	return len(d.prepared)
}

// take returns, once, the faces prepared from file when it still holds data; nil otherwise.
func (d *downloader) take(file string, data []byte) []*faceInfo {
	if d == nil {
		return nil
	}
	d.preparedMu.Lock()
	defer d.preparedMu.Unlock()
	p, ok := d.prepared[file]
	if !ok {
		return nil
	}
	delete(d.prepared, file)
	if !bytes.Equal(p.data, data) {
		return nil
	}
	return p.faces
}

func newDownloader() *downloader {
	return &downloader{
		metadataURL: "https://fonts.google.com/metadata/fonts",
		githubURL:   "https://raw.githubusercontent.com/google/fonts/main",
		cssURL:      "https://fonts.googleapis.com/css2",
		client:      &http.Client{Timeout: fetchTimeout},
	}
}

// defaultDownloader is shared by every Fonts, so downloads run one at a time per process, as in Rust.
var defaultDownloader = newDownloader()

// get is net.rs get_bytes: any transport error, non-2xx status or body over 32 MiB is a failure.
func (d *downloader) get(url string) ([]byte, bool) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, false
	}
	req.Header.Set("User-Agent", userAgent)
	res, err := d.client.Do(req)
	if err != nil {
		return nil, false
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return nil, false
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, maxBody+1))
	if err != nil || len(b) > maxBody {
		return nil, false
	}
	return b, true
}

// getText is net.rs get_text: the body must be UTF-8.
func (d *downloader) getText(url string) (string, bool) {
	b, ok := d.get(url)
	if !ok || !utf8.Valid(b) {
		return "", false
	}
	return string(b), true
}

// familyAxes are the axis tags of a Google Fonts family in file-name order (ASCII: uppercase first), nil
// when it is not variable. The metadata is read from the cache, or downloaded into it, once per
// downloader. Called with d.mu held.
func (d *downloader) familyAxes(cache, family string) []string {
	if d.axes == nil {
		d.axes = d.readMetadata(filepath.Join(cache, "google-fonts-metadata.json"))
	}
	return d.axes[strings.ToLower(family)]
}

// readMetadata maps each lowercase family to its sorted axis tags, leaving out families without axes. It
// is never nil, so a failed read is not retried.
func (d *downloader) readMetadata(file string) map[string][]string {
	all := map[string][]string{}
	b, err := os.ReadFile(file)
	text := string(b)
	if err != nil || !utf8.Valid(b) {
		var ok bool
		if text, ok = d.getText(d.metadataURL); !ok {
			return all
		}
		writeAtomic(file, []byte(text))
	}
	// The endpoint prefixes its JSON with an anti-XSSI guard.
	i := strings.IndexByte(text, '{')
	if i < 0 {
		return all
	}
	var meta map[string]any
	if json.Unmarshal([]byte(text[i:]), &meta) != nil {
		return all
	}
	list, _ := meta["familyMetadataList"].([]any)
	for _, e := range list {
		entry, _ := e.(map[string]any)
		family, ok := entry["family"].(string)
		axes, ok2 := entry["axes"].([]any)
		if !ok || !ok2 {
			continue
		}
		var tags []string
		for _, a := range axes {
			axis, _ := a.(map[string]any)
			if tag, ok := axis["tag"].(string); ok {
				tags = append(tags, tag)
			}
		}
		if len(tags) == 0 {
			continue
		}
		sort.Strings(tags)
		all[strings.ToLower(family)] = tags
	}
	return all
}

// download fetches a family into cache: the variable font if it has one, else the static instance. A
// target file that already exists is kept. Blocks on the network: call it off the Fonts goroutine.
func (d *downloader) download(cache, family string, weight uint16, italic bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	varPath := varFile(cache, family, italic)
	if exists(varPath) {
		return
	}
	if axes := d.familyAxes(cache, family); axes != nil {
		suffix := ""
		if italic {
			suffix = "-Italic"
		}
		name := compactFamily(family) + suffix + "[" + strings.Join(axes, ",") + "].ttf"
		name = strings.NewReplacer("[", "%5B", "]", "%5D").Replace(name)
		dir := strings.ToLower(compactFamily(family))
		for _, license := range []string{"ofl", "apache", "ufl"} {
			if b, ok := d.get(d.githubURL + "/" + license + "/" + dir + "/" + name); ok {
				if os.MkdirAll(filepath.Dir(varPath), 0o777) == nil {
					writeAtomic(varPath, b)
				}
				return
			}
		}
	}
	file := staticFile(cache, family, weight, italic)
	if exists(file) {
		return
	}
	fam := strings.ReplaceAll(family, " ", "+")
	spec := fam + ":wght@" + strconv.Itoa(int(weight))
	if italic {
		spec = fam + ":ital,wght@1," + strconv.Itoa(int(weight))
	}
	css, ok := d.getText(d.cssURL + "?family=" + spec)
	if !ok {
		if css, ok = d.getText(d.cssURL + "?family=" + fam); !ok {
			return
		}
	}
	start := strings.Index(css, "url(")
	if start < 0 {
		return
	}
	start += len("url(")
	end := strings.IndexByte(css[start:], ')')
	if end < 0 {
		return
	}
	if b, ok := d.get(css[start : start+end]); ok {
		writeAtomic(file, b)
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// writeAtomic writes through a temporary file in the same folder and renames it into place, so an
// interrupted write never leaves a partial file at path. The temporary name has no font extension, so a
// leftover is never loaded as a font.
func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.part")
	if err != nil {
		return err
	}
	_, err = tmp.Write(data)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		os.Remove(tmp.Name())
	}
	return err
}
