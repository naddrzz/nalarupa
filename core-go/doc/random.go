package doc

import (
	"sync"
	"time"
)

// IDSource is the xorshift (13/7/17) generator ids are drawn from (doc.rs `random_u64`). Rust keeps one per
// thread; an execute run owns its own, seeded per call, so concurrent runs never shift each other's ids. The
// same seed gives the same ids as Rust. It is safe for concurrent use; the zero value draws as seed 1.
type IDSource struct {
	mu   sync.Mutex
	seed uint64
}

// NewIDSource is a generator seeded with seed (made odd, as `reseed` does).
func NewIDSource(seed uint64) *IDSource { return &IDSource{seed: seed | 1} }

// Reseed makes the next ids deterministic. Streaming previews and the final `execute` of the same call use
// the same seed, so nodes keep their ids.
func (s *IDSource) Reseed(seed uint64) {
	s.mu.Lock()
	s.seed = seed | 1
	s.mu.Unlock()
}

// Next is the next value of the sequence.
func (s *IDSource) Next() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.next()
}

// next is Next with s.mu held.
func (s *IDSource) next() uint64 {
	// Only the zero value holds 0 (seeds are made odd and xorshift never reaches 0), which would stay 0: it is
	// seed 0 made odd as `reseed` makes it.
	x := s.seed
	if x == 0 {
		x = 1
	}
	x ^= x << 13
	x ^= x >> 7
	x ^= x << 17
	s.seed = x
	return x
}

// State is where the sequence stands, for SetState.
func (s *IDSource) State() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seed
}

// SetState puts the sequence back where State found it.
func (s *IDSource) SetState(state uint64) {
	s.mu.Lock()
	s.seed = state
	s.mu.Unlock()
}

// Resume restores the sequence of seed after draws values (script.rs `resume_ids`).
func (s *IDSource) Resume(seed, draws uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resume(seed, draws)
}

// resume is Resume with s.mu held.
func (s *IDSource) resume(seed, draws uint64) {
	s.seed = seed | 1
	for range draws {
		s.next()
	}
}

// idsReach is how far down the sequence Drawn looks for its probe.
const idsReach = 1 << 20

// Drawn is how many values the sequence of seed has produced, given it stood at from before (script.rs
// `ids_drawn`): it draws a probe and finds it further down the sequence; false when it is out of reach. It
// leaves the sequence right after the probe's position. It holds the lock throughout, so no other draw lands
// between the probe and the search.
func (s *IDSource) Drawn(seed, from uint64) (uint64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	probe := s.next()
	s.resume(seed, from)
	for n := from; n < from+idsReach; n++ {
		if s.next() == probe {
			return n, true
		}
	}
	return 0, false
}

// global is the process-wide generator of code that runs outside an execute call.
var global = NewIDSource(uint64(time.Now().UnixNano()))

// Reseed reseeds the process-wide generator.
func Reseed(s uint64) { global.Reseed(s) }

// RandomU64 is the next value of the process-wide generator.
func RandomU64() uint64 { return global.Next() }

// randomID is a five-character id from d's generator, or the process-wide one when d has none.
func (d *Doc) randomID() string {
	ids := d.IDs
	if ids == nil {
		ids = global
	}
	return idFrom(ids.Next())
}
