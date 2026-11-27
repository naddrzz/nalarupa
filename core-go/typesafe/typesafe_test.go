package typesafe

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

func makeTestPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, color.NRGBA{
				R: uint8((x * 255) / max(1, w)),
				G: uint8((y * 255) / max(1, h)),
				B: 180,
				A: 255,
			})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestPrepareScreenPNGEnforcesTypeSafeBounds(t *testing.T) {
	cases := []struct {
		name string
		w, h int
	}{
		{"desktop_standard", 1440, 900},
		{"mobile_standard", 390, 844},
		{"4k_wide", 3840, 2160},
		{"ultra_tall_mobile", 390, 4200},
		{"ultra_wide_banner", 4200, 320},
		{"small_card", 180, 140},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := makeTestPNG(t, tc.w, tc.h)
			out, fw, fh, err := PrepareScreenPNG(raw)
			if err != nil {
				t.Fatalf("PrepareScreenPNG(%dx%d): %v", tc.w, tc.h, err)
			}
			if fw < MinImageSide || fw > MaxImageSide || fh < MinImageSide || fh > MaxImageSide {
				t.Fatalf("got %dx%d, want both sides in [%d, %d]", fw, fh, MinImageSide, MaxImageSide)
			}
			area := fw * fh
			if area < 78_400 || area > 2_458_624 {
				t.Fatalf("got area %d (%dx%d), outside [78400, 2458624]", area, fw, fh)
			}
			cfg, err := png.DecodeConfig(bytes.NewReader(out))
			if err != nil {
				t.Fatalf("prepared bytes not valid PNG: %v", err)
			}
			if cfg.Width != fw || cfg.Height != fh {
				t.Fatalf("decoded %dx%d != reported %dx%d", cfg.Width, cfg.Height, fw, fh)
			}
		})
	}
}

func TestComputeDecisionCompositeScoreAndTruthTable(t *testing.T) {
	goodResp := jsonv.MustParse(`{
		"model": "jev-1.14.0",
		"answers": {
			"visual_hierarchy": {"type": "score", "score": 2.7},
			"accessibility_contrast": {"type": "score", "score": 2.8},
			"color_harmony": {"type": "score", "score": 2.6},
			"spacing_alignment": {"type": "score", "score": 2.5},
			"typography_craft": {"type": "score", "score": 2.6},
			"has_overlap_or_clipping": {"type": "noul", "noul": 0.04},
			"has_color_clash": {"type": "noul", "noul": 0.05},
			"is_ai_slop": {"type": "noul", "noul": 0.08}
		},
		"usage": {"input_tokens": 920, "output_tokens": 64}
	}`)
	dec, err := ComputeDecision(goodResp, 75)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Verdict != "pass" || dec.Score < 85 || len(dec.Blockers) != 0 {
		t.Fatalf("expected strong pass, got score=%d verdict=%s blockers=%v", dec.Score, dec.Verdict, dec.Blockers)
	}
	if dec.Model != "jev-1.14.0" || dec.Usage.InputTokens != 920 {
		t.Fatalf("unexpected metadata: %+v", dec)
	}

	// Even if some dimensions are decent, severe text overlap + AI slop must trigger "revise" with concrete blockers.
	slopOverlapResp := jsonv.MustParse(`{
		"model": "jev-1.14.0",
		"answers": {
			"visual_hierarchy": {"type": "score", "score": 2.1},
			"accessibility_contrast": {"type": "score", "score": 2.2},
			"color_harmony": {"type": "score", "score": 2.0},
			"spacing_alignment": {"type": "score", "score": 1.8},
			"typography_craft": {"type": "score", "score": 1.9},
			"has_overlap_or_clipping": {"type": "noul", "noul": 0.89},
			"has_color_clash": {"type": "noul", "noul": 0.78},
			"is_ai_slop": {"type": "noul", "noul": 0.84}
		},
		"usage": {"input_tokens": 900, "output_tokens": 64}
	}`)
	badDec, err := ComputeDecision(slopOverlapResp, 75)
	if err != nil {
		t.Fatal(err)
	}
	if badDec.Verdict != "revise" {
		t.Fatalf("expected revise verdict, got %s (score=%d)", badDec.Verdict, badDec.Score)
	}
	if len(badDec.Blockers) != 3 {
		t.Fatalf("expected 3 critical blockers (overlap, color clash, AI slop), got %v", badDec.Blockers)
	}
	issues := badDec.ActionableIssues()
	joined := strings.Join(issues, "\n")
	for _, want := range []string{"Overlapping or clipped", "Clashing colors", "AI slop"} {
		if !strings.Contains(joined, want) {
			t.Errorf("expected actionable issues to mention %q, got:\n%s", want, joined)
		}
	}

	// Custom high threshold (e.g. 95/100) must mark an ~88-scoring clean screen as "revise"
	// and emit targeted polish guidance on its lowest-scoring sub-dimensions.
	highBarDec, err := ComputeDecision(goodResp, 95)
	if err != nil {
		t.Fatal(err)
	}
	if highBarDec.Verdict != "revise" || highBarDec.PassScore != 95 {
		t.Fatalf("expected revise under 95/100 threshold, got score=%d verdict=%s passScore=%d",
			highBarDec.Score, highBarDec.Verdict, highBarDec.PassScore)
	}
	highBarIssues := strings.Join(highBarDec.ActionableIssues(), "\n")
	if !strings.Contains(highBarIssues, "Spacing & grid") {
		t.Fatalf("expected high-threshold actionable issues to target weakest dimension (Spacing & grid), got:\n%s", highBarIssues)
	}

	if ClampPassScore(0) != DefaultPassScore || ClampPassScore(12) != 12 || ClampPassScore(99) != 99 || ClampPassScore(150) != 100 {
		t.Fatalf("unexpected ClampPassScore bounds")
	}
	if ClampMaxRevisions(0) != DefaultMaxRevisions || ClampMaxRevisions(4) != 4 || ClampMaxRevisions(99) != 5 {
		t.Fatalf("unexpected ClampMaxRevisions bounds")
	}
}

func TestClientEvaluateScreenSendsVisualMultipartOnlyAndRetries429(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if r.URL.Path != "/v1/systemone" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer ts-secret-key" {
			t.Errorf("bad auth header: %q", got)
		}
		if got := r.Header.Get("TypeSafe-Danger-Zone-Images"); got != "true" {
			t.Errorf("missing TypeSafe-Danger-Zone-Images header: %q", got)
		}

		mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "multipart/form-data" {
			t.Fatalf("expected multipart/form-data, got %q (%v)", r.Header.Get("Content-Type"), err)
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		p1, err := mr.NextPart()
		if err != nil || p1.FormName() != "request" {
			t.Fatalf("first part must be 'request', got %v (%v)", p1, err)
		}
		reqBytes, _ := io.ReadAll(p1)
		reqJSON, err := jsonv.Parse(reqBytes)
		if err != nil {
			t.Fatalf("invalid request JSON: %v", err)
		}
		// Verify ONLY `screen` image placeholder is in `state` (zero code, zero scene JSON!).
		stObj := jsonv.Obj(jsonv.Get(reqJSON, "state"))
		if stObj == nil || stObj.Len() != 1 || !stObj.Has("screen") {
			t.Fatalf("state must hold only `screen` image placeholder, got %s", string(reqBytes))
		}
		ref := jsonv.StrOr(jsonv.Get(stObj, "screen"))
		asset := jsonv.Get(jsonv.Get(reqJSON, "assets"), ref)
		if !jsonv.StrIs(jsonv.Get(asset, "part"), "screen_image") {
			t.Fatalf("asset must reference multipart 'screen_image', got %s", string(reqBytes))
		}

		p2, err := mr.NextPart()
		if err != nil || p2.FormName() != "screen_image" {
			t.Fatalf("second part must be 'screen_image', got %v (%v)", p2, err)
		}
		imgBytes, _ := io.ReadAll(p2)
		if _, err := png.DecodeConfig(bytes.NewReader(imgBytes)); err != nil {
			t.Fatalf("second part is not valid PNG: %v", err)
		}

		if n == 1 {
			w.Header().Set("Retry-After", "0.05")
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"detail":"rate limited"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{
			"model": "jev-1.14.0",
			"answers": {
				"visual_hierarchy": {"type": "score", "score": 2.6},
				"accessibility_contrast": {"type": "score", "score": 2.7},
				"color_harmony": {"type": "score", "score": 2.5},
				"spacing_alignment": {"type": "score", "score": 2.4},
				"typography_craft": {"type": "score", "score": 2.5},
				"has_overlap_or_clipping": {"type": "noul", "noul": 0.05},
				"has_color_clash": {"type": "noul", "noul": 0.06},
				"is_ai_slop": {"type": "noul", "noul": 0.09}
			},
			"usage": {"input_tokens": 840, "output_tokens": 58}
		}`)
	}))
	defer srv.Close()

	client := NewClient("ts-secret-key", srv.URL, DefaultModel)
	dec, err := client.EvaluateScreen(context.Background(), makeTestPNG(t, 800, 600), 75)
	if err != nil {
		t.Fatal(err)
	}
	if attempts.Load() != 2 {
		t.Fatalf("expected 2 attempts (429 then 200), got %d", attempts.Load())
	}
	if dec.Verdict != "pass" || dec.Score < 80 {
		t.Fatalf("unexpected decision: %+v", dec)
	}
}
