package shader

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
	"sync"
	"time"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/shader/gpu"
	"github.com/naddrzz/nalarupa/core-go/skia"
	"github.com/naddrzz/nalarupa/core-go/svg"
)

// Scale is the pixels per unit of a shader fill's image: rendered at the canvas DPI (2 on Retina) and
// scaled when zoomed in; exports use the same image.
const Scale = 2.0

// BackdropMax caps the `@backdrop` texture at 2048 px per side.
const BackdropMax = 2048.0

// The caches are cleared when they outgrow these (shader.rs `PROGRAMS`, `PNGS`).
const (
	maxPrograms = 64
	maxPNGs     = 32
)

// Painter runs shader fills on the GPU; it is the default svg.ShaderPainter.
type Painter struct{}

func init() { svg.DefaultShader = Painter{} }

// UsesBackdrop is UsesBackdrop with the compile check of shader.rs `uses_backdrop`.
func (Painter) UsesBackdrop(src string) bool {
	c := program(src)
	return c.err == nil && c.prog.UsesBackdrop()
}

// PNG is the package's PNG.
func (Painter) PNG(src string, uniforms *jsonv.Object, w, h float64, outline svg.Outline, load func(string) (*svg.Pixmap, bool), backdrop *svg.Backdrop) (string, error) {
	return PNG(src, uniforms, w, h, outline, load, backdrop)
}

// compiled is a cached `compile` result: the program, or why it failed.
type compiled struct {
	key  uint64
	prog *Program
	err  error
}

var programs struct {
	sync.Mutex
	m map[uint64]*compiled
}

// program is shader.rs `program`: Parse plus naga's check on the GPU device, cached by source. An error
// starting the device is not cached here (the device keeps it). The cache lock is not held while compiling,
// which waits for the GPU: a lookup never queues behind a draw.
func program(src string) *compiled {
	key := svg.FxHash(src)
	programs.Lock()
	if programs.m == nil || len(programs.m) > maxPrograms {
		programs.m = map[uint64]*compiled{}
	}
	c, ok := programs.m[key]
	programs.Unlock()
	if ok {
		return c
	}
	c = &compiled{key: key}
	c.prog, c.err = Parse(src)
	if c.err == nil {
		checked, err := gpu.Compile(key, c.prog.GLSL)
		if !checked {
			return &compiled{key: key, err: err}
		}
		c.err = err
	}
	if c.err != nil {
		c.prog = nil
	}
	programs.Lock()
	defer programs.Unlock()
	if programs.m == nil {
		programs.m = map[uint64]*compiled{}
	}
	if prev, ok := programs.m[key]; ok {
		return prev // compiled meanwhile by another goroutine
	}
	programs.m[key] = c
	return c
}

// Adapter is the name of the adapter shader fills run on (starts the device if needed).
func Adapter() (string, error) { return gpu.Adapter() }

var pngs struct {
	sync.Mutex
	m map[uint64]string
}

// PNG is Render at Scale as a base64 PNG, cached by everything that changes the pixels (shader.rs `png`).
// backdrop, when set, is what lies behind the node.
func PNG(src string, uniforms *jsonv.Object, w, h float64, outline svg.Outline, load func(string) (*svg.Pixmap, bool), backdrop *svg.Backdrop) (string, error) {
	var given string
	if uniforms != nil {
		given = string(jsonv.Marshal(uniforms))
	}
	var behind uint64
	var paint func(w, h int) (*skia.Pixmap, bool)
	if backdrop != nil {
		behind, paint = backdrop.Key, backdrop.Paint
	}
	key := svg.FxHash(fmt.Sprintf("%s\x00%s\x00%vx%v\x00%+v\x00%d", src, given, w, h, outline, behind))
	pngs.Lock()
	data, ok := pngs.m[key]
	pngs.Unlock()
	if ok {
		return data, nil
	}
	t := time.Now()
	pm, err := Render(src, uniforms, w, h, Scale, outline, load, paint)
	if err != nil {
		return "", err
	}
	drawn := time.Since(t)
	b, err := encodePNG(pm)
	if err != nil {
		return "", err
	}
	data = base64.StdEncoding.EncodeToString(b)
	if gpu.Trace {
		fmt.Fprintf(os.Stderr, "shader fill %dx%d: render %v, png %v\n", pm.Width(), pm.Height(), drawn, time.Since(t)-drawn)
	}
	pngs.Lock()
	if pngs.m == nil || len(pngs.m) > maxPNGs {
		pngs.m = map[uint64]string{}
	}
	pngs.m[key] = data
	pngs.Unlock()
	return data, nil
}

// encodePNG demultiplies pm and writes it with fast compression: shader output is often noisy (grain),
// where the default level is slow.
func encodePNG(pm *skia.Pixmap) ([]byte, error) {
	img := image.NewNRGBA(image.Rect(0, 0, int(pm.Width()), int(pm.Height())))
	for i, d := 0, pm.Data(); i+4 <= len(d); i += 4 {
		c := skia.PremultipliedColorU8(d[i : i+4]).Demultiply()
		copy(img.Pix[i:i+4], c[:])
	}
	var buf bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// transparent is the 1×1 texture standing in for a missing image or backdrop.
func transparent(mode gpu.AddressMode) gpu.Texture {
	return gpu.Texture{W: 1, H: 1, Data: make([]byte, 4), Mode: mode}
}

// Render draws the shader in src over a w×h node at scale pixels per unit (shader.rs `render`). load
// returns the image (premultiplied, top row first) behind a sampler2D uniform's url; backdrop, when set,
// paints what lies behind the node into a w×h pixmap.
func Render(src string, uniforms *jsonv.Object, w, h, scale float64, outline svg.Outline, load func(string) (*svg.Pixmap, bool), backdrop func(w, h int) (*skia.Pixmap, bool)) (*skia.Pixmap, error) {
	if !(w > 0 && h > 0) {
		return nil, errors.New("empty fill")
	}
	c := program(src)
	if c.err != nil {
		return nil, c.err
	}
	prog := c.prog
	pw, ph := Pixels(w, h, scale)
	ubo := prog.Pack(uniforms, w, h, pw, ph)
	var textures []gpu.Texture
	for _, u := range prog.Uniforms {
		var given jsonv.Value
		if uniforms != nil {
			given, _ = uniforms.Get(u.Name)
		}
		switch u.Kind.Type {
		case KindImage:
			tex := transparent(gpu.Repeat)
			if url, _ := jsonv.Str(given); url != "" && load != nil {
				if pm, ok := load(url); ok && pm.W > 0 && pm.H > 0 {
					tex = gpu.Texture{W: uint32(pm.W), H: uint32(pm.H), Data: pm.Pix, Mode: gpu.Repeat}
				}
			}
			textures = append(textures, tex)
		case KindSDF:
			sw, sh, half := SDF(outline, w, h, scale)
			data := make([]byte, 2*len(half))
			for i, v := range half {
				binary.LittleEndian.PutUint16(data[2*i:], v)
			}
			textures = append(textures, gpu.Texture{W: uint32(sw), H: uint32(sh), Half: true, Data: data, Mode: gpu.ClampToEdge})
		case KindBackdrop:
			// The node's box at the render scale, at most BackdropMax px per side, GL rows.
			r := fmax(fmax(w, h)*scale, 1e-9)
			k := scale * fmin(r, BackdropMax) / r
			bw, bh := int(fmax(math.Ceil(w*k), 1)), int(fmax(math.Ceil(h*k), 1))
			tex := transparent(gpu.MirrorRepeat)
			if backdrop != nil {
				if pm, ok := backdrop(bw, bh); ok {
					tex = gpu.Texture{W: pm.Width(), H: pm.Height(), Data: flipRows(pm.Data(), int(pm.Width())*4), Mode: gpu.MirrorRepeat}
				}
			}
			textures = append(textures, tex)
		}
	}
	return gpu.Draw(c.key, prog.GLSL, ubo, textures, pw, ph)
}

// flipRows is data with its rows of row bytes in reverse order.
func flipRows(data []byte, row int) []byte {
	out := make([]byte, len(data))
	for i, n := 0, len(data)/row; i < n; i++ {
		copy(out[(n-1-i)*row:(n-i)*row], data[i*row:(i+1)*row])
	}
	return out
}
