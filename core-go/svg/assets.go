package svg

// Icon SVGs and remote images, fetched once and cached on disk (svg.rs `Assets`), with the limits that
// keep a hostile image from freezing a render.

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	_ "image/jpeg" // registered for DecodeImage
	"image/png"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/image/webp"
)

const (
	// maxDownloads run at the same time; the rest of a document's missing assets wait their turn.
	maxDownloads = 8
	// MaxImagePixels is the largest raster image decoded, in pixels (RGBA: 400 MB).
	MaxImagePixels = 100_000_000
	// MaxSVGDepth is the deepest element nesting an SVG image may have (usvg overflows its stack at ~3500).
	MaxSVGDepth = 256
	// maxSVGZBytes is the largest SVGZ image the SVG export decompresses.
	maxSVGZBytes = 256 << 20
)

// HTTP limits of net.rs.
const (
	fetchTimeout = 8 * time.Second
	userAgent    = "nalarupa/0.1"
	maxBody      = 32 << 20
)

type queued struct {
	key string
	job func() bool
}

type cachedIcon struct {
	svg string
	ok  bool
}

type deepVerdict struct {
	stamp uint64
	deep  bool
}

// Assets is the icon and image store. It is not safe for concurrent use: downloads run on their own
// goroutines, touch only the disk and report through OnReady; the owner then calls Downloaded.
type Assets struct {
	dir   string
	icons map[string]cachedIcon
	// failed holds icon keys and image urls whose download failed this session (not retried).
	failed map[string]bool
	// loading holds downloads in flight or queued (icon key or image url).
	loading map[string]bool
	// queue waits for a free slot (at most maxDownloads run at once); running is how many run now.
	queue   []queued
	running int
	// Generation goes up when a download ends; the renderer rebuilds every layer when it changes.
	Generation uint64
	// dataOK is whether a `data:` image is within the size limit, by url hash.
	dataOK map[uint64]bool
	// deep holds too-deep verdicts of SVG images, valid while the stamp matches.
	deep map[string]deepVerdict
	// verify refuses a raster image whose data does not fully decode (PDF export).
	verify  bool
	decoded map[uint64]bool
	// Online lets a missing icon or remote image start a download (New sets it).
	Online bool
	// asked holds, while recording, the downloads in flight icons and images asked for.
	asked     []string
	recording bool
	// OnReady is called on the download's goroutine when a download ends (ok when it was stored); the
	// owner then calls Downloaded with the same arguments. Without it a download stays Loading.
	OnReady func(key string, ok bool)
	// Client fetches icons and images; IconBase is the package CDN icon urls start with. Tests replace them.
	Client   *http.Client
	IconBase string
}

// NewAssets returns a store caching under dir (its `icons` and `images` folders are created).
func NewAssets(dir string) *Assets {
	os.MkdirAll(filepath.Join(dir, "icons"), 0o777)
	os.MkdirAll(filepath.Join(dir, "images"), 0o777)
	return &Assets{
		dir: dir, icons: map[string]cachedIcon{}, failed: map[string]bool{}, loading: map[string]bool{},
		dataOK: map[uint64]bool{}, deep: map[string]deepVerdict{}, decoded: map[uint64]bool{}, Online: true,
		Client: &http.Client{Timeout: fetchTimeout}, IconBase: "https://unpkg.com",
	}
}

// Verify turns the full-decode check of Image on or off; each switch drops the cached verdicts.
func (a *Assets) Verify(on bool) {
	a.verify = on
	a.decoded = map[uint64]bool{}
}

// Icon is the icon at the default weight (200; only Material Symbols has weights).
func (a *Assets) Icon(library, name string) (string, bool) { return a.IconWeight(library, name, 200) }

// IconWeight is the icon's SVG, or false while it downloads in the background.
func (a *Assets) IconWeight(library, name string, weight float64) (string, bool) {
	url, tag := iconURL(a.IconBase, library, name, weight)
	key := tag + "/" + name
	if v, ok := a.icons[key]; ok {
		return v.svg, v.ok
	}
	file := filepath.Join(a.dir, "icons", tag+"-"+strings.NewReplacer("/", "_", `\`, "_", ".", "_").Replace(name)+".svg")
	b, err := os.ReadFile(file)
	ok := err == nil && utf8.Valid(b)
	svg := ""
	if ok {
		svg = string(b)
	}
	if !ok && a.Online && name != "" && !a.failed[key] {
		client := a.Client
		a.spawn(key, func() bool {
			t, ok := fetch(client, url)
			return ok && utf8.Valid(t) && bytes.Contains(t, []byte("<svg")) && store(file, t)
		})
		a.note(key)
		return "", false
	}
	a.icons[key] = cachedIcon{svg, ok}
	return svg, ok
}

// IconLoading reports that this icon is still downloading, so a missing IconWeight does not mean it does not exist.
func (a *Assets) IconLoading(library, name string, weight float64) bool {
	_, tag := iconURL(a.IconBase, library, name, weight)
	return a.Loading(tag + "/" + name)
}

// Loading reports that a download of this icon key or image url is still running or queued.
func (a *Assets) Loading(key string) bool { return a.loading[key] }

// Record starts noting the downloads in flight that icons and images ask for from now on (a screenshot's).
func (a *Assets) Record() {
	a.asked, a.recording = nil, true
}

// Recorded stops noting and returns the keys asked for since Record that were still downloading.
func (a *Assets) Recorded() []string {
	out := a.asked
	a.asked, a.recording = nil, false
	return out
}

func (a *Assets) note(key string) {
	if !a.recording || !a.loading[key] {
		return
	}
	for _, k := range a.asked {
		if k == key {
			return
		}
	}
	a.asked = append(a.asked, key)
}

// spawn queues job (a download that stores its result on disk, true on success) unless this key is
// already loading or failed; past maxDownloads running it waits until one ends.
func (a *Assets) spawn(key string, job func() bool) {
	if a.failed[key] || a.loading[key] {
		return
	}
	a.loading[key] = true
	a.queue = append(a.queue, queued{key, job})
	a.startQueued()
}

func (a *Assets) startQueued() {
	for a.running < maxDownloads && len(a.queue) > 0 {
		q := a.queue[0]
		a.queue = a.queue[1:]
		a.running++
		ready := a.OnReady
		go func() {
			ok := q.job()
			if ready != nil {
				ready(q.key, ok)
			}
		}()
	}
}

// Downloaded reports that a download ended: a failed one is not retried this session. It always bumps
// Generation (a failed image draws as unavailable instead of blank).
func (a *Assets) Downloaded(key string, ok bool) {
	delete(a.loading, key)
	a.running = max(a.running-1, 0)
	if !ok {
		a.failed[key] = true
	}
	a.Generation++
	a.startQueued()
}

// Image resolves an image fill url to a local file path (or the `data:` url itself). A raster image over
// MaxImagePixels is refused like a missing one, and so is an SVG nested deeper than MaxSVGDepth.
func (a *Assets) Image(url, base string) (string, bool) {
	href, ok := a.ImageUnscanned(url, base)
	if !ok {
		return "", false
	}
	tooDeep := false
	if key, stamp, ok := deepKey(href); ok {
		if v, ok := a.deep[key]; ok && v.stamp == stamp {
			tooDeep = v.deep
		} else {
			tooDeep = svgTooDeep(href)
			a.deep[key] = deepVerdict{stamp, tooDeep}
		}
	}
	return href, !tooDeep
}

// ImageUnscanned is Image without the depth scan of an SVG file, which reads the whole file: an SVG
// export leaves that scan to InlineEmbedded.
func (a *Assets) ImageUnscanned(url, base string) (string, bool) {
	href, ok := a.resolveImage(url, base)
	if !ok || !a.withinPixelLimit(href, url) {
		return "", false
	}
	if a.verify {
		k := fxhash(href)
		good, seen := a.decoded[k]
		if !seen {
			good = decodes(href)
			if !good {
				shown := url
				if strings.HasPrefix(href, "data:") {
					shown = "<data URL>"
				}
				fmt.Fprintf(os.Stderr, "image %s: damaged, left out of the export\n", shown)
			}
			a.decoded[k] = good
		}
		if !good {
			return "", false
		}
	}
	return href, true
}

// withinPixelLimit reports whether a raster image's declared size is within MaxImagePixels.
func (a *Assets) withinPixelLimit(href, url string) bool {
	isData := strings.HasPrefix(href, "data:")
	key := fxhash(href)
	if isData {
		if ok, seen := a.dataOK[key]; seen {
			return ok
		}
	}
	w, h, sized := hrefSize(href)
	ok := !sized || uint64(w)*uint64(h) <= MaxImagePixels
	if !ok {
		shown := url
		if isData {
			shown = "<data URL>"
		}
		fmt.Fprintf(os.Stderr, "image %s: %dx%d px is over the %d megapixel limit\n", shown, w, h, MaxImagePixels/1_000_000)
	}
	if isData {
		a.dataOK[key] = ok
	}
	return ok
}

func (a *Assets) resolveImage(url, base string) (string, bool) {
	if url == "" || strings.HasPrefix(url, "pencil:") {
		return "", false
	}
	if strings.HasPrefix(url, "data:") {
		return url, true
	}
	if strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://") {
		// resvg picks the decoder from the file extension, so cached files need one (extension-less files
		// from older versions are renamed on first use).
		stem := filepath.Join(a.dir, "images", fmt.Sprintf("%016x", fxhash(url)))
		file := ""
		for _, e := range []string{"png", "jpg", "gif", "webp", "svg"} {
			if exists(stem + "." + e) {
				file = stem + "." + e
				break
			}
		}
		if file == "" {
			b, err := os.ReadFile(stem)
			if err != nil {
				// Not on disk yet: download in the background (the fill draws nothing until it lands).
				if a.Online {
					client := a.Client
					a.spawn(url, func() bool {
						b, ok := fetch(client, url)
						return ok && store(stem+"."+imageExt(b), b)
					})
				}
				a.note(url)
				return "", false
			}
			file = stem + "." + imageExt(b)
			if os.WriteFile(file, b, 0o666) != nil {
				return "", false
			}
			os.Remove(stem)
		}
		return strings.ReplaceAll(file, `\`, "/"), true
	}
	rel := strings.TrimPrefix(url, "file://")
	full := rel
	if !filepath.IsAbs(rel) {
		if base == "" {
			return "", false
		}
		full = joinPath(base, rel)
	}
	if !exists(full) {
		return "", false
	}
	return strings.ReplaceAll(full, `\`, "/"), true
}

// joinPath is Rust's PathBuf::join for a relative path: a separator between, no cleaning.
func joinPath(base, rel string) string {
	if strings.HasSuffix(base, "/") || strings.HasSuffix(base, string(filepath.Separator)) {
		return base + rel
	}
	return base + string(filepath.Separator) + rel
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// iconURL is the download url and cache tag of an icon.
func iconURL(base, library, name string, weight float64) (string, string) {
	lib := strings.ToLower(library)
	switch {
	case lib == "feather":
		return base + "/feather-icons@4.29.2/dist/icons/" + name + ".svg", "feather"
	case lib == "phosphor":
		// Phosphor names carry the style: "house-fill", "gear-thin".
		style := "regular"
		for _, s := range []string{"thin", "light", "bold", "fill", "duotone"} {
			if strings.HasSuffix(name, "-"+s) {
				style = s
				break
			}
		}
		return base + "/@phosphor-icons/core@2.1.1/assets/" + style + "/" + name + ".svg", "phosphor"
	case strings.HasPrefix(lib, "material"):
		variant := "outlined"
		if strings.Contains(lib, "rounded") {
			variant = "rounded"
		} else if strings.Contains(lib, "sharp") {
			variant = "sharp"
		}
		w := itoa(int(satU16(clamp(math.Round(weight/100)*100, 100, 700))))
		return base + "/@material-symbols/svg-" + w + "@0.27.0/" + variant + "/" + name + ".svg", "material_" + variant + "_" + w
	}
	return base + "/lucide-static@0.563.0/icons/" + name + ".svg", "lucide_0.563"
}

// satU16 is Rust's `f as u16`.
func satU16(v float64) uint16 {
	switch {
	case !(v > 0):
		return 0
	case v >= 65535:
		return 65535
	}
	return uint16(v)
}

// fetch is net.rs get_bytes: any transport error, non-2xx status or body over 32 MiB is a failure.
func fetch(client *http.Client, url string) ([]byte, bool) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, false
	}
	req.Header.Set("User-Agent", userAgent)
	res, err := client.Do(req)
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

// store writes a downloaded file under a temporary name first, so a render never reads it half written.
func store(file string, b []byte) bool {
	tmp := strings.TrimSuffix(file, filepath.Ext(file)) + ".part"
	return os.WriteFile(tmp, b, 0o666) == nil && os.Rename(tmp, file) == nil
}

// decodes reports whether a PNG, GIF or WebP image decodes completely (a truncated or damaged one passes
// the header check but fails here); other formats count as fine.
func decodes(href string) bool {
	var b []byte
	if strings.HasPrefix(href, "data:") {
		var ok bool
		if b, ok = decodeDataURL(href); !ok {
			return true
		}
	} else {
		var err error
		if b, err = os.ReadFile(href); err != nil {
			return true
		}
	}
	r := bytes.NewReader(b)
	var err error
	switch imageExt(b) {
	case "png":
		_, err = png.Decode(r)
	case "gif":
		_, err = gif.Decode(r)
	case "webp":
		_, err = webp.Decode(r)
	}
	return err == nil
}

// DecodeImage is the pixels of a raster image file, premultiplied; false for SVG or undecodable data.
func DecodeImage(path string) (*Pixmap, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	img, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, false
	}
	// Straight RGBA8, premultiplied the way tiny-skia does it.
	bounds := img.Bounds()
	pm := newPixmap(bounds.Dx(), bounds.Dy())
	i := 0
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			c := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
			p := premultiply([4]uint8{c.R, c.G, c.B, c.A})
			copy(pm.Pix[i:i+4], p[:])
			i += 4
		}
	}
	return pm, true
}

// hrefSize is the declared pixel size of a raster image (a file path or a `data:` url), read from its
// header without decoding; false for SVG or anything unrecognised.
func hrefSize(href string) (uint32, uint32, bool) {
	if strings.HasPrefix(href, "data:") {
		b, ok := decodeDataURL(href)
		if !ok {
			return 0, 0, false
		}
		return imageSize(bytes.NewReader(b))
	}
	f, err := os.Open(href)
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()
	return imageSize(f)
}

// imageSize is the width and height from a PNG, GIF, JPEG or WebP header.
func imageSize(r io.ReadSeeker) (uint32, uint32, bool) {
	var head [24]byte
	n, _ := io.ReadFull(r, head[:])
	be16 := func(b []byte) uint32 { return uint32(binary.BigEndian.Uint16(b)) }
	pngSig := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0, 0, 0, 13, 'I', 'H', 'D', 'R'}
	switch {
	case bytes.Equal(head[:16], pngSig) && n >= 24:
		return binary.BigEndian.Uint32(head[16:20]), binary.BigEndian.Uint32(head[20:24]), true
	case string(head[:4]) == "GIF8" && n >= 10:
		return uint32(binary.LittleEndian.Uint16(head[6:8])), uint32(binary.LittleEndian.Uint16(head[8:10])), true
	case string(head[:4]) == "RIFF" && string(head[8:12]) == "WEBP":
		if _, err := r.Seek(0, io.SeekStart); err != nil {
			return 0, 0, false
		}
		c, err := webp.DecodeConfig(r)
		if err != nil {
			return 0, 0, false
		}
		return uint32(c.Width), uint32(c.Height), true
	case head[0] == 0xFF && head[1] == 0xD8:
		if _, err := r.Seek(2, io.SeekStart); err != nil {
			return 0, 0, false
		}
		// Walk the marker segments to the first start-of-frame (C0-CF but not DHT, JPG, DAC).
		for {
			var m [2]byte
			if _, err := io.ReadFull(r, m[:]); err != nil {
				return 0, 0, false
			}
			switch t := m[1]; {
			case m[0] == 0xFF && t == 0xFF:
				if _, err := r.Seek(-1, io.SeekCurrent); err != nil {
					return 0, 0, false
				}
			case m[0] == 0xFF && (t == 0x01 || t >= 0xD0 && t <= 0xD7):
			case m[0] == 0xFF && (t == 0xD9 || t == 0xDA), t == 0x00:
				return 0, 0, false
			case m[0] == 0xFF:
				var seg [7]byte
				if _, err := io.ReadFull(r, seg[:2]); err != nil {
					return 0, 0, false
				}
				l := int64(be16(seg[:2]))
				if t >= 0xC0 && t <= 0xCF && t != 0xC4 && t != 0xC8 && t != 0xCC {
					if _, err := io.ReadFull(r, seg[2:7]); err != nil {
						return 0, 0, false
					}
					return be16(seg[5:7]), be16(seg[3:5]), true
				}
				if _, err := r.Seek(l-2, io.SeekCurrent); err != nil {
					return 0, 0, false
				}
			default:
				return 0, 0, false
			}
		}
	}
	return 0, 0, false
}

// deepKey is the cache key and change stamp of an image svgTooDeep would scan: a `data:` url (by hash)
// or an `.svg` file (by path, stamped with its size and modification time).
func deepKey(href string) (string, uint64, bool) {
	if strings.HasPrefix(href, "data:") {
		return fmt.Sprintf("%016x", fxhash(href)), 0, true
	}
	if !strings.HasSuffix(strings.ToLower(href), ".svg") {
		return "", 0, false
	}
	st, err := os.Stat(href)
	if err != nil {
		return "", 0, false
	}
	nanos := uint64(0)
	if t := st.ModTime().UnixNano(); t > 0 {
		nanos = uint64(t)
	}
	return href, uint64(st.Size()) + nanos, true
}

// svgTooDeep reports whether the image behind a `data:` url or a file path is an SVG nested deeper than MaxSVGDepth.
func svgTooDeep(href string) bool {
	var b []byte
	if rest, ok := strings.CutPrefix(href, "data:"); ok {
		head, payload, ok := strings.Cut(rest, ",")
		if !ok {
			return false
		}
		// Only what usvg parses as markup: other MIME types are skipped, or ignored by usvg.
		switch mime, _, _ := strings.Cut(head, ";"); mime {
		case "image/svg+xml", "text/plain", "":
		default:
			return false
		}
		if strings.HasSuffix(head, ";base64") {
			p := strings.TrimSpace(payload)
			if strings.ContainsAny(p, "\r\n") {
				return false
			}
			var err error
			if b, err = base64.StdEncoding.Strict().DecodeString(p); err != nil {
				return false
			}
		} else {
			b = percentDecode(payload)
		}
		if imageExt(b) != "svg" {
			return false
		}
	} else if strings.HasSuffix(strings.ToLower(href), ".svg") {
		var err error
		if b, err = os.ReadFile(href); err != nil {
			return false
		}
	} else {
		return false
	}
	return maxDepth(b, MaxSVGDepth) > MaxSVGDepth
}

func unhex(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

func percentDecode(s string) []byte {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); {
		if s[i] == '%' && i+2 < len(s) {
			h, ok1 := unhex(s[i+1])
			l, ok2 := unhex(s[i+2])
			if ok1 && ok2 {
				out = append(out, h*16+l)
				i += 3
				continue
			}
		}
		out = append(out, s[i])
		i++
	}
	return out
}

// decodeDataURL is the payload of a `data:` url as the data-url crate decodes it: percent-decoded, then
// forgiving base64 when the media type ends in `;base64`. Control characters and spaces around it are
// trimmed, and the body ends at the fragment (`#`).
func decodeDataURL(href string) ([]byte, bool) {
	c0 := func(r rune) bool { return r <= ' ' }
	rest := strings.TrimRightFunc(strings.TrimLeftFunc(href, c0), c0)
	if len(rest) < 5 || !strings.EqualFold(rest[:5], "data:") {
		return nil, false
	}
	rest = strings.NewReplacer("\t", "", "\n", "", "\r", "").Replace(rest[5:])
	comma, hash := strings.IndexByte(rest, ','), strings.IndexByte(rest, '#')
	if comma < 0 || (hash >= 0 && hash < comma) {
		return nil, false
	}
	head, body := rest[:comma], rest[comma+1:]
	if hash >= 0 {
		body = rest[comma+1 : hash]
	}
	b := percentDecode(body)
	h := strings.TrimRight(head, " ")
	if len(h) >= 6 && strings.EqualFold(h[len(h)-6:], "base64") && strings.HasSuffix(strings.TrimRight(h[:len(h)-6], " "), ";") {
		return forgivingBase64(b)
	}
	return b, true
}

// forgivingBase64 is WHATWG forgiving-base64 decode.
func forgivingBase64(in []byte) ([]byte, bool) {
	s := make([]byte, 0, len(in))
	for _, c := range in {
		if c != ' ' && c != '\t' && c != '\n' && c != '\f' && c != '\r' {
			s = append(s, c)
		}
	}
	if len(s)%4 == 0 {
		s = bytes.TrimSuffix(s, []byte("="))
		s = bytes.TrimSuffix(s, []byte("="))
	}
	if len(s)%4 == 1 {
		return nil, false
	}
	for _, c := range s {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '+' || c == '/') {
			return nil, false
		}
	}
	out, err := base64.RawStdEncoding.DecodeString(string(s))
	return out, err == nil
}

// maxDepth is how deeply markup nests elements (comments, CDATA, PIs and the doctype skipped); it stops
// at limit + 1 once that is exceeded.
func maxDepth(b []byte, limit int) int {
	find := func(from int, pat string) int {
		if p := bytes.Index(b[from:], []byte(pat)); p >= 0 {
			return from + p + len(pat)
		}
		return len(b)
	}
	i, depth, peak := 0, 0, 0
	for i < len(b) {
		switch {
		case b[i] != '<':
			i++
		case bytes.HasPrefix(b[i:], []byte("<!--")):
			i = find(i+4, "-->")
		case bytes.HasPrefix(b[i:], []byte("<![CDATA[")):
			i = find(i+9, "]]>")
		case bytes.HasPrefix(b[i:], []byte("<?")):
			i = find(i+2, "?>")
		case bytes.HasPrefix(b[i:], []byte("<!")):
			// A doctype may hold an internal subset in brackets.
			j, sq := i+2, 0
			for j < len(b) && (b[j] != '>' || sq > 0) {
				switch b[j] {
				case '[':
					sq++
				case ']':
					sq = max(sq-1, 0)
				}
				j++
			}
			i = j + 1
		default:
			closing := i+1 < len(b) && b[i+1] == '/'
			j, quote := i+1, byte(0)
			for j < len(b) && (b[j] != '>' || quote != 0) {
				switch {
				case quote == 0 && (b[j] == '"' || b[j] == '\''):
					quote = b[j]
				case quote != 0 && b[j] == quote:
					quote = 0
				}
				j++
			}
			if closing {
				depth = max(depth-1, 0)
			} else if b[min(j, len(b))-1] != '/' {
				depth++
				peak = max(peak, depth)
				if depth > limit {
					return depth
				}
			}
			i = j + 1
		}
	}
	return peak
}

// imageExt is the file extension for downloaded image bytes (resvg decodes by extension).
func imageExt(b []byte) string {
	switch {
	case len(b) >= 4 && b[0] == 0x89 && string(b[1:4]) == "PNG":
		return "png"
	case len(b) >= 2 && b[0] == 0xFF && b[1] == 0xD8:
		return "jpg"
	case len(b) >= 4 && string(b[:4]) == "GIF8":
		return "gif"
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return "webp"
	}
	return "svg"
}

// gunzipCapped is gz decompressed; false when it is broken or larger than maxSVGZBytes.
func gunzipCapped(gz []byte) ([]byte, bool) {
	r, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return nil, false
	}
	r.Multistream(false)
	out, err := io.ReadAll(io.LimitReader(r, maxSVGZBytes+1))
	if err != nil || len(out) > maxSVGZBytes {
		return nil, false
	}
	return out, true
}

// fxhash is FNV-1a 64 of the string's bytes.
func fxhash(s string) uint64 {
	h := uint64(0xcbf29ce484222325)
	for i := 0; i < len(s); i++ {
		h = (h ^ uint64(s[i])) * 0x100000001b3
	}
	return h
}
