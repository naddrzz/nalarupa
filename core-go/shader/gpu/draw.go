package gpu

/*
#include "glue.h"
*/
import "C"

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"
	"unsafe"

	"github.com/naddrzz/nalarupa/core-go/skia"
)

// Tile is the rows per GPU submission: heavy shaders stay far from the driver's 2 s watchdog.
const Tile = 256

// timeout bounds each wait for the GPU, as shader.rs's `PollType::Wait` does.
const timeout = 20 * time.Second

// AddressMode is how a texture repeats outside [0, 1].
type AddressMode int

const (
	ClampToEdge AddressMode = iota
	Repeat
	MirrorRepeat
)

// Texture is a texture handed to the shader: RGBA8 (premultiplied, 4 bytes per texel) or, when Half,
// RGBA16F (8 bytes per texel); rows in the order the shader samples them.
type Texture struct {
	W, H uint32
	Half bool
	Data []byte
	Mode AddressMode
}

func (m AddressMode) c() C.WGPUAddressMode {
	switch m {
	case Repeat:
		return C.WGPUAddressMode_Repeat
	case MirrorRepeat:
		return C.WGPUAddressMode_MirrorRepeat
	}
	return C.WGPUAddressMode_ClampToEdge
}

// Trace is NALARUPA_SHADER_TRACE: timings on stderr.
var Trace = os.Getenv("NALARUPA_SHADER_TRACE") != ""

// Draw runs the compiled shader key (glsl) over a pw×ph target with the uniform block ubo and the
// textures bound in order, and reads it back with each channel clamped to alpha (shader.rs `render`'s GPU
// part and `draw`). The device starts if needed; a `GPU:` error drops it, so the next draw starts afresh.
func Draw(key uint64, glsl string, ubo []byte, textures []Texture, pw, ph uint32) (*skia.Pixmap, error) {
	mu.Lock()
	defer mu.Unlock()
	d, err := current()
	if err != nil {
		return nil, err
	}
	t := time.Now()
	pm, err := d.draw(key, glsl, ubo, textures, pw, ph)
	if Trace {
		fmt.Fprintf(os.Stderr, "  gpu %v on %s\n", time.Since(t), d.adapter)
	}
	if err != nil && strings.HasPrefix(err.Error(), "GPU:") {
		reset()
	}
	return pm, err
}

func (d *device) draw(key uint64, glsl string, ubo []byte, textures []Texture, pw, ph uint32) (*skia.Pixmap, error) {
	t := time.Now()
	p, err := d.pipeline(key, glsl, len(textures))
	if err != nil {
		return nil, err
	}
	if Trace {
		fmt.Fprintf(os.Stderr, "  pipeline %v\n", time.Since(t))
	}
	pm, ok := skia.NewPixmap(pw, ph)
	if !ok {
		return nil, errors.New("image too large")
	}
	var pin runtime.Pinner
	defer pin.Unpin()
	texs := make([]C.nr_tex, max(len(textures), 1))
	for i, tx := range textures {
		bpp, half := 4, C.int(0)
		if tx.Half {
			bpp, half = 8, 1
		}
		if want := int(tx.W) * int(tx.H) * bpp; len(tx.Data) != want || want == 0 {
			return nil, fmt.Errorf("texture %d: %d bytes for %dx%d", i, len(tx.Data), tx.W, tx.H)
		}
		pin.Pin(&tx.Data[0])
		texs[i] = C.nr_tex{w: C.uint32_t(tx.W), h: C.uint32_t(tx.H), half: half, data: unsafe.Pointer(&tx.Data[0]), mode: tx.Mode.c()}
	}
	block := ubo
	if len(block) == 0 {
		block = make([]byte, 16)
	}
	nd := (*C.nr_draw)(C.calloc(1, C.size_t(unsafe.Sizeof(C.nr_draw{}))))
	if nd == nil {
		return nil, errors.New("out of memory")
	}
	defer func() {
		C.nr_draw_end(nd)
		C.free(unsafe.Pointer(nd))
	}()
	C.nr_draw_begin(&d.g, p, nd, unsafe.Pointer(&block[0]), C.size_t(len(block)), &texs[0], C.int(len(textures)), C.uint32_t(pw), C.uint32_t(ph))
	for y0 := uint32(0); y0 < ph; y0 += Tile {
		C.nr_draw_tile(&d.g, p, nd, C.uint32_t(y0), C.uint32_t(min(Tile, ph-y0)))
		if err := d.wait(nil); err != nil {
			return nil, err
		}
	}
	C.nr_draw_copy(&d.g, nd)
	if err := d.wait(func() bool { return nd.mapped != 0 }); err != nil {
		return nil, err
	}
	if nd.mapped != C.WGPUMapAsyncStatus_Success {
		return nil, errors.New("GPU: Error occurred when trying to async map a buffer")
	}
	src := C.nr_draw_mapped(nd)
	if src == nil {
		return nil, errors.New("GPU: the mapped range is not available")
	}
	row := int(nd.row)
	data := unsafe.Slice((*byte)(unsafe.Pointer(src)), row*int(ph))
	out := pm.DataMut()
	w := int(pw) * 4
	for r := range int(ph) {
		in, o := data[r*row:r*row+w], out[r*w:(r+1)*w]
		for i := 0; i < w; i += 4 {
			// SkSL output is premultiplied; keep it a valid premultiplied pixel.
			a := in[i+3]
			o[i], o[i+1], o[i+2], o[i+3] = min(in[i], a), min(in[i+1], a), min(in[i+2], a), a
		}
	}
	return pm, nil
}

// wait polls the device until done (or, when done is nil, until its queue is empty), for at most timeout.
func (d *device) wait(done func() bool) error {
	start := time.Now()
	for spin := 0; ; spin++ {
		empty := C.nr_poll(d.g.device) != 0
		if done != nil && done() || done == nil && empty {
			return nil
		}
		if time.Since(start) > timeout {
			return errors.New("GPU: The requested Wait timed out before the submission was completed.")
		}
		if spin < 64 {
			runtime.Gosched()
		} else {
			time.Sleep(100 * time.Microsecond)
		}
	}
}
