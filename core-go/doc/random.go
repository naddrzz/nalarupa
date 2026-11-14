package doc

import (
	"sync"
	"time"
)

var (
	seedMu sync.Mutex
	seed   = uint64(time.Now().UnixNano()) | 1
)

// Reseed makes the next generated ids deterministic. Streaming previews and the final `execute` of the
// same call use the same seed, so nodes keep their ids. The same seed gives the same ids as Rust.
func Reseed(s uint64) {
	seedMu.Lock()
	seed = s | 1
	seedMu.Unlock()
}

// RandomU64 is the next value of the xorshift (13/7/17) generator.
func RandomU64() uint64 {
	seedMu.Lock()
	defer seedMu.Unlock()
	x := seed
	x ^= x << 13
	x ^= x >> 7
	x ^= x << 17
	seed = x
	return x
}

func randomID() string {
	return idFrom(RandomU64())
}
