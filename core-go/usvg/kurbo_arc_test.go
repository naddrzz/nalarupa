package usvg

import (
	"math"
	"strconv"
	"strings"
	"testing"
)

// kurboArcCases are SVG arcs as usvg builds them (f32 inputs, rotation in degrees): from x y, to x y,
// radii x y, rotation, large-arc, sweep.
const kurboArcCases = `
0 0 100 0 50 50 0 0 1
0 0 100 0 50 50 0 1 0
10 20 30 40 5 5 0 0 0
10 20 30 40 5 10 45 1 1
0 0 10 10 20 5 30 0 1
5.5 7.25 -3.75 12.5 8 3 -120 1 0
0 0 1 1 0 5 0 0 1
0 0 0 0 5 5 0 0 1
100 100 200 150 300 40 720.5 0 0
1.2 3.4 5.6 7.8 0.5 0.25 10 1 1
-50 -50 50 50 70.7 70.7 0 0 1
0 0 1000 0 400 900 15 1 1
3 4 3.001 4 1 1 0 1 0
0 0 20 0 -10 -10 0 0 1
`

// kurboArcWant is the output of kurbo 0.13.1 for kurboArcCases: "none", or "arc" with the Arc fields
// (center, radii, start, sweep, rotation) and the points of to_cubic_beziers(0.1) as f64 bits in hex.
const kurboArcWant = `
arc 4049000000000000 0 4049000000000000 4049000000000000 400921fb54442d18 400921fb54442d18 0 bd00000000000000 c03b9d3eab1223d2 403662c154eddc29 c048ffffffffffff 4048ffffffffffff c049000000000000 4053674faac488f4 c049000000000001 4059000000000000 c03b9d3eab1223d7 4059000000000000 bd0b939bafcfcfcb
arc 4049000000000000 0 4049000000000000 4049000000000000 400921fb54442d18 c00921fb54442d18 0 3d00000000000000 403b9d3eab1223d6 403662c154eddc2d 4049000000000000 4049000000000000 4049000000000000 4053674faac488f5 4049000000000000 4059000000000000 403b9d3eab1223d4 4059000000000000 0
arc 4034000003333333 403dfffffccccccd 402c48c6001f0ac0 402c48c6001f0ac0 c002d97c81c27dfb c00921fb4f2574c6 0 4011e89ab66948e4 403985d955ee8aca 4011e89abc245ba6 40413d1354515842 4024000006666665 4043fffffe666666 402f0bb2aeba9ef7 4046c2eca87b748b 40387a26aa117532 4046c2eca932d6e4 403dfffffffffffe 4044000000000001
arc 4034000000000000 403e000000000000 402c48c6001f0ac0 403c48c6001f0ac0 400921fb54442d18 400921fb54442d18 3fe921fb54442d18 40350bb2aad40e54 4021e89aaa57e354 40413d13554afc6a 4011e89aaa57e350 4044000000000000 4023fffffffffffc 4046c2ecaab50396 402f0bb2aad40e50 404485d9556a072b 403cf44d552bf1aa 403e000000000002 4044000000000000
arc c01dc222aa9f5746 3ff3a43f4b6e86d0 4034000000000000 4014000000000000 bff4669b352f3b07 3ff0c70174165408 3fe0c152382d7365 4016aae9bb68b364 400efd89fd9d801d 4022f5dec793ce51 401ebf98d28da950 4023fffffffffffd 4023ffffffffffff
arc 3febffffcff27961 4023bffffac920e2 402c5cb080d912d8 4015458460a2ce22 3ff92d4ca4edcc3d c00921fb57a933cc c000c152382d7365 3ff974f9ab6bf652 3fdda672ee784c00 c00d300578689700 c00ee7ccb5adf90c c018cfa19bc0d999 c0034eb78091e21c c02183a03da6b3d9 bfeed6892dd72cb0 c01ea2c1997ae4c0 4016da67315d73eb c00dfffffffffffe 4029000000000000
none
none
arc 407754e7a99e0364 405db56a9f916857 4072c00000000000 4044000000000000 c005be0cdac7f7e9 bff613cce846c84c 4029267325ecc1bf 40445f5238d11258 405d3cf1906e106c 4054abe26a598130 40613d871bb34634 4069000000000004 4062c00000000003
arc 400b333330000000 4016666670000000 40118ae61a9e9cf7 40018ae61a9e9cf7 c00186f981d0d39a 400921fb54442d18 3fc657184ae74487 400a1b7e14859217 400864c4ab8ef43c 4017af3d8f089bca 400dff9706ce5484 401c8b6d7125aa6a 4013dbfb69ef4d26 4020b3cea9a15c85 4018b82b5077700b 401ea7589a42c90b 401dcbfbf5c77a1d 4016666660000000 401f333340000000
arc beb1bc779ef0d4e8 3eb1bc779ef0d4e8 4051ad7bc01366b7 4051ad7bc01366b7 c002d97c7c5ca848 400921fb4e973a04 0 c03662c1520f6458 c053674fa6965629 403662c14a1e8e38 c053674fa49a20a1 4048fffff721c430 c048fffff721c430 4053674fa49a20a1 c03662c14a1e8e38 4053674fa6965629 403662c1520f6458 4048fffffffffffe 4048fffffffffffc
arc 407f400004e9978c bef72ba43fff3717 407e66025068790f 409119614d3ac418 40082f3b4e289f8b 400921fb59d6a23b 3fd0c152382d7365 40542880b776d4d0 c077c517b17d65b7 406f4e15bb98f1f9 c086ddf9999f9981 407bd7506082c3a9 c08ccf3f8730d334 408403caf19c872b c0916042ba610673 4089f8958b6cbbc9 c0916042ba610673 408d8ba83365a540 c08ccf3f8730d335 40908f5d6daf475b c086ddf9999f9984 4090e2880d691b5a c077c517b17d65b4 408f400000000000 bd33f37170000000
arc 4008010620000000 4013fffff79cd3d7 3ff0000000000000 3ff0000000000000 bff9240794459b85 c01920f5344375e2 0 4003951ef376c270 401000485efb8720 400000bdc535e904 4011cad0f5df1c57 4000010624319616 401400417f9cf631 4000014e832d4326 401635b2095ad00c 400396250a337a3c 4017fffff79cd3d7 4008010620000000 4017fffff79cd3d7 400c6be735cc85c2 4017fffff79cd3d7 4010005ede695e6c 401635b2095ad00d 401000830de734f5 401400417f9cf632 401000a73d650b7e 4011cad0f5df1c57 400c6ced4c893d92 401000485efb8721 4008020c40000002 4010000000000000
arc 4024000000000000 0 4024000000000000 4024000000000000 400921fb54442d18 400921fb54442d18 0 bce0000000000000 c016176555a81ca8 4011e89aaa57e355 c023ffffffffffff 4023ffffffffffff c024000000000000 402f0bb2aad40e54 c024000000000001 4034000000000000 c016176555a81cac 4034000000000000 bce60fafbfd97309
`

// kurboUlps is the distance in ulps between two finite floats of the same sign (or both zero).
func kurboUlps(a, b float64) uint64 {
	if a == b {
		return 0
	}
	ia, ib := int64(math.Float64bits(a)), int64(math.Float64bits(b))
	if (ia < 0) != (ib < 0) {
		return math.MaxUint64
	}
	if ia > ib {
		return uint64(ia - ib)
	}
	return uint64(ib - ia)
}

// TestKurboArcParity compares Arc::from_svg_arc and to_cubic_beziers with kurbo 0.13.1 built for Windows
// (UCRT libm). The cubic points, which usvg rounds to f32, must be within one f32 ulp. The f64 values
// are only logged: rotated arcs whose radii were scaled up end in a cancellation that amplifies libm's
// last bit, and UCRT's sin and cos are not always correctly rounded (sin(pi/4) here), so they can differ
// far beyond an f64 ulp, and an angle of pi can come out as -pi.
func TestKurboArcParity(t *testing.T) {
	cases := strings.Split(strings.TrimSpace(kurboArcCases), "\n")
	want := strings.Split(strings.TrimSpace(kurboArcWant), "\n")
	if len(cases) != len(want) {
		t.Fatalf("%d cases, %d expectations", len(cases), len(want))
	}
	exact64, exact32 := 0, 0
	for i, line := range cases {
		var v [9]float64
		for j, f := range strings.Fields(line) {
			x, err := strconv.ParseFloat(f, 64)
			if err != nil {
				t.Fatal(err)
			}
			v[j] = float64(float32(x)) // usvg passes f32 values
		}
		arc := kurboSvgArc{
			From:      kurboPoint{v[0], v[1]},
			To:        kurboPoint{v[2], v[3]},
			Radii:     kurboVec2{v[4], v[5]},
			XRotation: stToRadians(v[6]),
			LargeArc:  v[7] != 0,
			Sweep:     v[8] != 0,
		}
		a, ok := kurboArcFromSvgArc(&arc)
		if !ok {
			if want[i] != "none" {
				t.Errorf("case %d (%s): got none, want an arc", i, line)
			}
			exact64++
			exact32++
			continue
		}
		got := []float64{a.Center.X, a.Center.Y, a.Radii.X, a.Radii.Y, a.StartAngle, a.SweepAngle, a.XRotation}
		a.ToCubicBeziers(0.1, func(p1, p2, p3 kurboPoint) {
			got = append(got, p1.X, p1.Y, p2.X, p2.Y, p3.X, p3.Y)
		})
		fields := strings.Fields(want[i])
		if fields[0] != "arc" || len(fields)-1 != len(got) {
			t.Errorf("case %d (%s): got %d values, want %q", i, line, len(got), want[i])
			continue
		}
		same64, same32 := true, true
		var worst64 uint64
		for j, h := range fields[1:] {
			bits, err := strconv.ParseUint(h, 16, 64)
			if err != nil {
				t.Fatal(err)
			}
			w := math.Float64frombits(bits)
			if got[j] != w {
				same64 = false
				worst64 = max(worst64, kurboUlps(got[j], w))
			}
			if j < 7 {
				continue
			}
			g32, w32 := float32(got[j]), float32(w)
			if g32 != w32 {
				same32 = false
				if !approxEqUlpsF32(g32, w32, 1) {
					t.Errorf("case %d (%s) point value %d: got %v, want %v as f32", i, line, j-7, g32, w32)
				}
			}
		}
		if same64 {
			exact64++
		} else {
			t.Logf("case %d (%s): f64 values differ by up to %d ulps", i, line, worst64)
		}
		if same32 {
			exact32++
		}
	}
	t.Logf("%d of %d arcs bit-identical in f64, %d in f32", exact64, len(cases), exact32)
}

func TestKurboStraightLine(t *testing.T) {
	for _, arc := range []kurboSvgArc{
		{From: kurboPoint{0, 0}, To: kurboPoint{1, 1}, Radii: kurboVec2{1e-5, 5}},
		{From: kurboPoint{0, 0}, To: kurboPoint{1, 1}, Radii: kurboVec2{5, -1e-5}},
		{From: kurboPoint{2, 3}, To: kurboPoint{2, 3}, Radii: kurboVec2{5, 5}},
	} {
		if _, ok := kurboArcFromSvgArc(&arc); ok {
			t.Errorf("%+v: want a straight line", arc)
		}
	}
	nan := math.NaN()
	if !(&kurboSvgArc{From: kurboPoint{nan, 0}, To: kurboPoint{nan, 0}, Radii: kurboVec2{1, 1}}).IsStraightLine() == false {
		t.Error("NaN points compare unequal, so the arc is not a straight line")
	}
}

func TestKurboSemicircle(t *testing.T) {
	arc := kurboSvgArc{From: kurboPoint{0, 0}, To: kurboPoint{100, 0}, Radii: kurboVec2{50, 50}, Sweep: true}
	a, ok := kurboArcFromSvgArc(&arc)
	if !ok {
		t.Fatal("want an arc")
	}
	if a.Center != (kurboPoint{50, 0}) || a.Radii != (kurboVec2{50, 50}) || a.StartAngle != math.Pi || a.SweepAngle != math.Pi {
		t.Errorf("got %+v", a)
	}
	n := 0
	var last kurboPoint
	a.ToCubicBeziers(0.1, func(_, _, p kurboPoint) { n++; last = p })
	if n != 2 || math.Abs(last.X-100) > 1e-9 || math.Abs(last.Y) > 1e-9 {
		t.Errorf("got %d segments ending at %+v", n, last)
	}
}

func TestKurboHelpers(t *testing.T) {
	if stSignum(0) != 1 || stSignum(math.Copysign(0, -1)) != -1 || !math.IsNaN(stSignum(math.NaN())) {
		t.Error("signum")
	}
	for _, c := range []struct {
		in   float64
		want uint64
	}{{math.NaN(), 0}, {-3, 0}, {0.9, 0}, {4, 4}, {1e30, math.MaxUint64}, {math.Inf(1), math.MaxUint64}} {
		if got := stF64ToUsize(c.in); got != c.want {
			t.Errorf("as usize %v: got %d, want %d", c.in, got, c.want)
		}
	}
}
