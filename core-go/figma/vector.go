package figma

import (
	"encoding/binary"
	"math"
	"strconv"
	"strings"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

// rf writes a float as Rust's `{}` does: the shortest digits that round-trip, never an exponent.
func rf(v float64) string {
	switch {
	case math.IsInf(v, 1):
		return "inf"
	case math.IsInf(v, -1):
		return "-inf"
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

type seg struct {
	a, z   int
	ad, zd [2]float64
}

// vector turns a vector network blob into SVG path data and a fill rule; ok is false when the blob is
// missing or malformed.
func (d *doc) vector(p jsonv.Value) (string, string, bool) {
	idx, ok := u64(get(p, "vectorData", "vectorNetworkBlob"))
	if !ok || idx >= uint64(len(d.blobs)) {
		return "", "", false
	}
	b := d.blobs[idx]
	bad := false
	u32 := func(i int) int {
		if i+4 > len(b) {
			bad = true
			return 0
		}
		return int(binary.LittleEndian.Uint32(b[i:]))
	}
	f32 := func(i int) float64 { return float64(math.Float32frombits(uint32(u32(i)))) }
	nv, ns, nr := u32(0), u32(4), u32(8)
	if bad {
		return "", "", false
	}
	i := 12
	var verts [][2]float64
	for range nv {
		v := [2]float64{f32(i + 4), f32(i + 8)}
		if bad {
			return "", "", false
		}
		verts = append(verts, v)
		i += 12
	}
	var segs []seg
	for range ns {
		a, z := u32(i+4), u32(i+16)
		if bad {
			return "", "", false
		}
		if a >= nv || z >= nv {
			return "", "", false
		}
		s := seg{a: a, ad: [2]float64{f32(i + 8), f32(i + 12)}, z: z, zd: [2]float64{f32(i + 20), f32(i + 24)}}
		if bad {
			return "", "", false
		}
		segs = append(segs, s)
		i += 28
	}
	type region struct {
		rule  string
		loops [][]seg
	}
	var regions []region
	for range nr {
		rule := "evenodd"
		if u32(i)&1 == 1 {
			rule = "nonzero"
		}
		loops := u32(i + 4)
		if bad {
			return "", "", false
		}
		i += 8
		var ls [][]seg
		for range loops {
			n := u32(i)
			if bad {
				return "", "", false
			}
			i += 4
			var l []seg
			for range n {
				s := u32(i)
				if bad {
					return "", "", false
				}
				if s >= ns {
					return "", "", false
				}
				l = append(l, segs[s])
				i += 4
			}
			ls = append(ls, l)
		}
		regions = append(regions, region{rule, ls})
	}
	var out []string
	rule := "nonzero"
	if len(regions) == 0 {
		out = trace(out, verts, chain(segs))
	}
	for _, r := range regions {
		for _, l := range r.loops {
			if len(l) > 0 {
				out = trace(out, verts, chain(l))
			}
		}
		rule = r.rule
	}
	return strings.Join(out, " "), rule, true
}

// chain orients segments head to tail.
func chain(s []seg) []seg {
	flip := func(x *seg) { *x = seg{a: x.z, ad: x.zd, z: x.a, zd: x.ad} }
	if len(s) >= 2 && s[0].z != s[1].a && s[0].z != s[1].z {
		flip(&s[0])
	}
	for i := 1; i < len(s); i++ {
		if s[i-1].z != s[i].a {
			flip(&s[i])
		}
	}
	return s
}

// trace writes segments as path commands, closing when a run returns to its start.
func trace(d []string, v [][2]float64, segs []seg) []string {
	r := func(x float64) string { return rf(math.Round(x*1e5) / 1e5) }
	start, last := -1, -1
	for _, s := range segs {
		a, z := v[s.a], v[s.z]
		if last != s.a {
			d = append(d, "M"+r(a[0])+" "+r(a[1]))
			start = s.a
		}
		if s.ad == [2]float64{} && s.zd == [2]float64{} {
			d = append(d, "L"+r(z[0])+" "+r(z[1]))
		} else {
			d = append(d, "C"+r(a[0]+s.ad[0])+" "+r(a[1]+s.ad[1])+" "+r(z[0]+s.zd[0])+" "+r(z[1]+s.zd[1])+" "+r(z[0])+" "+r(z[1]))
		}
		last = s.z
		if start == s.z {
			d = append(d, "Z")
			start, last = -1, -1
		}
	}
	return d
}
