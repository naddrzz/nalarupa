package skia

// Sparse run-length encoded coverage (alpha_runs.rs). An AlphaRun (Option<NonZeroU16>) is a uint16 here,
// with 0 for None.

type alphaRuns struct {
	runs  []uint16
	alpha []uint8
}

func newAlphaRuns(width uint32) *alphaRuns {
	r := &alphaRuns{runs: make([]uint16, width+1), alpha: make([]uint8, width+1)}
	r.reset(width)
	return r
}

// alphaRunsCatchOverflow returns 0-255 given 0-256.
func alphaRunsCatchOverflow(alpha uint16) uint8 { return uint8(alpha - (alpha >> 8)) }

// isEmpty reports whether the scanline holds only a single run of alpha 0.
func (r *alphaRuns) isEmpty() bool {
	if run := r.runs[0]; run != 0 {
		return r.alpha[0] == 0 && r.runs[run] == 0
	}
	return true
}

// reset reinitializes for a new scanline.
func (r *alphaRuns) reset(width uint32) {
	if width > 0xffff {
		panic("alpha runs: width does not fit in u16") // u16::try_from(..).unwrap()
	}
	r.runs[0] = uint16(width)
	r.runs[width] = 0
	r.alpha[0] = 0
}

// add inserts a run starting at x-offsetX: one pixel += startAlpha (if > 0), middleCount pixels +=
// maxValue, one pixel += stopAlpha (if > 0). It returns the offsetX for the next call on the same
// scanline (0 when switching scanlines).
func (r *alphaRuns) add(x uint32, startAlpha uint8, middleCount int, stopAlpha uint8, maxValue uint8, offsetX int) int {
	xi := int(x)

	runsOffset := offsetX
	alphaOffset := offsetX
	lastAlphaOffset := offsetX
	xi -= offsetX

	if startAlpha != 0 {
		alphaRunsBreakRun(r.runs[runsOffset:], r.alpha[alphaOffset:], xi, 1)
		// If the trailing edge of the previous span and the leading edge of the current span round to
		// the same super-sampled x value, this add can reach 256, hence the subtract.
		tmp := uint16(r.alpha[alphaOffset+xi]) + uint16(startAlpha)
		r.alpha[alphaOffset+xi] = uint8(tmp - (tmp >> 8))

		runsOffset += xi + 1
		alphaOffset += xi + 1
		xi = 0
	}

	if middleCount != 0 {
		alphaRunsBreakRun(r.runs[runsOffset:], r.alpha[alphaOffset:], xi, middleCount)
		alphaOffset += xi
		runsOffset += xi
		xi = 0
		for {
			r.alpha[alphaOffset] = alphaRunsCatchOverflow(uint16(r.alpha[alphaOffset]) + uint16(maxValue))

			n := alphaRunsUnwrap(r.runs[runsOffset])
			alphaOffset += n
			runsOffset += n
			middleCount -= n

			if middleCount == 0 {
				break
			}
		}

		lastAlphaOffset = alphaOffset
	}

	if stopAlpha != 0 {
		alphaRunsBreakRun(r.runs[runsOffset:], r.alpha[alphaOffset:], xi, 1)
		alphaOffset += xi
		r.alpha[alphaOffset] += stopAlpha // wraps, as in a Rust release build
		lastAlphaOffset = alphaOffset
	}

	return lastAlphaOffset
}

func alphaRunsUnwrap(n uint16) int {
	if n == 0 {
		panic("alpha runs: unwrap on None")
	}
	return int(n)
}

// alphaRunsBreakRun breaks the runs at offsets x and x+count, updating the runs to the right and left.
func alphaRunsBreakRun(runs []uint16, alpha []uint8, x, count int) {
	origX := x
	runsOffset := 0
	alphaOffset := 0

	for x > 0 {
		n := alphaRunsUnwrap(runs[runsOffset])
		if x < n {
			alpha[alphaOffset+x] = alpha[alphaOffset]
			runs[runsOffset] = uint16(x)
			runs[runsOffset+x] = uint16(n - x)
			break
		}
		runsOffset += n
		alphaOffset += n
		x -= n
	}

	runsOffset = origX
	alphaOffset = origX
	x = count

	for {
		n := alphaRunsUnwrap(runs[runsOffset])
		if x < n {
			alpha[alphaOffset+x] = alpha[alphaOffset]
			runs[runsOffset] = uint16(x)
			runs[runsOffset+x] = uint16(n - x)
			break
		}

		x -= n
		if x == 0 {
			break
		}

		runsOffset += n
		alphaOffset += n
	}
}

// alphaRunsBreakAt cuts (at offset x) a run into two shorter runs with matching alpha values. As in
// Rust (and Skia), the new run lengths are written at runs[0] and runs[x].
func alphaRunsBreakAt(alpha []uint8, runs []uint16, x int32) {
	alphaI := 0
	runI := 0
	for x > 0 {
		n := alphaRunsUnwrap(runs[runI])
		if x < int32(n) {
			alpha[alphaI+int(x)] = alpha[alphaI]
			runs[0] = uint16(x)
			runs[x] = uint16(int32(n) - x)
			break
		}

		runI += n
		alphaI += n
		x -= int32(n)
	}
}
