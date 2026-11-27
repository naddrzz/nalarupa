package typesafe

import (
	"bytes"
	"context"
	"errors"
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

	// A single critical blocker (e.g. hierarchy 1.32 < 1.45 failFloor) MUST force Verdict="revise"
	// even if all other dimensions are 3.0/3 and the composite rounds above passScore!
	blockerHighComposite := jsonv.MustParse(`{
		"model": "jev-1.14.0",
		"answers": {
			"visual_hierarchy": {"type": "score", "score": 1.32},
			"accessibility_contrast": {"type": "score", "score": 3.0},
			"color_harmony": {"type": "score", "score": 3.0},
			"spacing_alignment": {"type": "score", "score": 3.0},
			"typography_craft": {"type": "score", "score": 3.0},
			"has_overlap_or_clipping": {"type": "noul", "noul": 0.0},
			"has_color_clash": {"type": "noul", "noul": 0.0},
			"is_ai_slop": {"type": "noul", "noul": 0.0}
		}
	}`)
	blkDec, err := ComputeDecision(blockerHighComposite, 75)
	if err != nil {
		t.Fatal(err)
	}
	if blkDec.Verdict != "revise" || len(blkDec.Blockers) == 0 || len(blkDec.ActionableIssues()) == 0 {
		t.Fatalf("expected critical blocker to force revise with actionable issues, got score=%d verdict=%s blockers=%v",
			blkDec.Score, blkDec.Verdict, blkDec.Blockers)
	}

	// Threshold-driven revision with a warning present must still include weaker dimensions and deduplicate by dimension.
	warnPlusWeakDims := jsonv.MustParse(`{
		"model": "jev-1.14.0",
		"answers": {
			"visual_hierarchy": {"type": "score", "score": 2.04},
			"accessibility_contrast": {"type": "score", "score": 3.0},
			"color_harmony": {"type": "score", "score": 3.0},
			"spacing_alignment": {"type": "score", "score": 2.04},
			"typography_craft": {"type": "score", "score": 3.0},
			"has_overlap_or_clipping": {"type": "noul", "noul": 0.0},
			"has_color_clash": {"type": "noul", "noul": 0.31},
			"is_ai_slop": {"type": "noul", "noul": 0.0}
		}
	}`)
	wDec, err := ComputeDecision(warnPlusWeakDims, 95)
	if err != nil {
		t.Fatal(err)
	}
	wIssues := strings.Join(wDec.ActionableIssues(), "\n")
	for _, want := range []string{"Hierarchy (", "Spacing & grid (", "No color clash ("} {
		if strings.Count(wIssues, want) != 1 {
			t.Fatalf("expected %q exactly once in actionable issues, got:\n%s", want, wIssues)
		}
	}
}

func TestPrepareScreenPNGRejectsTruncatedPNGAndPremultipliesAlpha(t *testing.T) {
	// 1. Valid 800x600 PNG truncated right after IHDR must be rejected (not passed through unchecked).
	valid800 := makeTestPNG(t, 800, 600)
	truncated := valid800[:33] // 8-byte PNG signature + 25-byte IHDR chunk only
	if _, _, _, err := PrepareScreenPNG(truncated); err == nil {
		t.Fatalf("expected PrepareScreenPNG to reject truncated PNG with valid IHDR")
	}

	// 2. Opaque white (255,255,255,255) next to transparent black (0,0,0,0) must not bleed dark gray fringes
	// in either upscaling (bilinear) or downscaling (box filter).
	for _, tc := range []struct {
		name string
		w, h int
	}{
		{"upscale_bilinear", 140, 140},
		{"downscale_box", 2000, 2000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			img := image.NewNRGBA(image.Rect(0, 0, tc.w, tc.h))
			for y := 0; y < tc.h; y++ {
				for x := 0; x < tc.w; x++ {
					if x < tc.w/2 {
						img.SetNRGBA(x, y, color.NRGBA{R: 255, G: 255, B: 255, A: 255})
					} else {
						img.SetNRGBA(x, y, color.NRGBA{R: 0, G: 0, B: 0, A: 0})
					}
				}
			}
			var raw bytes.Buffer
			if err := png.Encode(&raw, img); err != nil {
				t.Fatal(err)
			}
			outBytes, _, _, err := PrepareScreenPNG(raw.Bytes())
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := png.Decode(bytes.NewReader(outBytes))
			if err != nil {
				t.Fatal(err)
			}
			nrgba := toNRGBA(decoded)
			db := nrgba.Bounds()
			for x := db.Min.X; x < db.Max.X; x++ {
				px := nrgba.NRGBAAt(x, db.Dy()/2)
				if px.A > 0 && (px.R < 254 || px.G < 254 || px.B < 254) {
					t.Fatalf("dark fringe at x=%d: got RGBA=%+v, want pure white RGB on semi-transparent seam", x, px)
				}
			}
		})
	}
}

func TestClientEvaluateScreenSendsVisualMultipartOnlyAndRetries429(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if r.URL.Path != "/v1/systemone" {
			t.Errorf("unexpected path %s", r.URL.Path)
			http.Error(w, "bad path", http.StatusBadRequest)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer ts-secret-key" {
			t.Errorf("bad auth header: %q", got)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if got := r.Header.Get("TypeSafe-Danger-Zone-Images"); got != "true" {
			t.Errorf("missing TypeSafe-Danger-Zone-Images header: %q", got)
			http.Error(w, "missing header", http.StatusBadRequest)
			return
		}

		mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "multipart/form-data" {
			t.Errorf("expected multipart/form-data, got %q (%v)", r.Header.Get("Content-Type"), err)
			http.Error(w, "bad content-type", http.StatusBadRequest)
			return
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		p1, err := mr.NextPart()
		if err != nil || p1.FormName() != "request" {
			t.Errorf("first part must be 'request', got %v (%v)", p1, err)
			http.Error(w, "bad first part", http.StatusBadRequest)
			return
		}
		reqBytes, _ := io.ReadAll(p1)
		reqJSON, err := jsonv.Parse(reqBytes)
		if err != nil {
			t.Errorf("invalid request JSON: %v", err)
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		// Verify ONLY `screen` image placeholder is in `state` (zero code, zero scene JSON!).
		stObj := jsonv.Obj(jsonv.Get(reqJSON, "state"))
		if stObj == nil || stObj.Len() != 1 || !stObj.Has("screen") {
			t.Errorf("state must hold only `screen` image placeholder, got %s", string(reqBytes))
			http.Error(w, "bad state", http.StatusBadRequest)
			return
		}
		ref := jsonv.StrOr(jsonv.Get(stObj, "screen"))
		asset := jsonv.Get(jsonv.Get(reqJSON, "assets"), ref)
		if !jsonv.StrIs(jsonv.Get(asset, "part"), "screen_image") {
			t.Errorf("asset must reference multipart 'screen_image', got %s", string(reqBytes))
			http.Error(w, "bad asset", http.StatusBadRequest)
			return
		}

		p2, err := mr.NextPart()
		if err != nil || p2.FormName() != "screen_image" {
			t.Errorf("second part must be 'screen_image', got %v (%v)", p2, err)
			http.Error(w, "bad second part", http.StatusBadRequest)
			return
		}
		imgBytes, _ := io.ReadAll(p2)
		if _, err := png.DecodeConfig(bytes.NewReader(imgBytes)); err != nil {
			t.Errorf("second part is not valid PNG: %v", err)
			http.Error(w, "invalid png", http.StatusBadRequest)
			return
		}
		if extraPart, eofErr := mr.NextPart(); eofErr != io.EOF {
			t.Errorf("expected io.EOF after screen_image part, got part=%v err=%v", extraPart, eofErr)
			http.Error(w, "unexpected extra multipart part", http.StatusBadRequest)
			return
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
	t.Cleanup(SetTestTransport(srv.Client().Transport))

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

type failingBody struct{}

func (failingBody) Read([]byte) (int, error) { return 0, errors.New("simulated stream read error") }
func (failingBody) Close() error             { return nil }

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestClientEnforcesHTTPSRedirectPolicyAndListModelsReadError(t *testing.T) {
	// 1. Plaintext http:// endpoints must be rejected before any request is sent.
	plainClient := NewClient("ts-secret", "http://api.typesafe.ai", DefaultModel)
	if _, err := plainClient.TestConnection(context.Background()); err == nil || !strings.Contains(err.Error(), "https://") {
		t.Fatalf("expected TestConnection to reject http:// endpoint, got %v", err)
	}
	if _, err := plainClient.ListModels(context.Background()); err == nil || !strings.Contains(err.Error(), "https://") {
		t.Fatalf("expected ListModels to reject http:// endpoint, got %v", err)
	}
	if _, err := plainClient.EvaluateScreen(context.Background(), makeTestPNG(t, 400, 400), 75); err == nil || !strings.Contains(err.Error(), "https://") {
		t.Fatalf("expected EvaluateScreen to reject http:// endpoint, got %v", err)
	}

	// 2. Cross-origin / HTTP downgrade / POST redirects must be rejected, including on fallback HTTPClient=nil.
	redirectSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://evil.example.com/v1/models", http.StatusFound)
	}))
	defer redirectSrv.Close()
	t.Cleanup(SetTestTransport(redirectSrv.Client().Transport))

	fallbackClient := &Client{BaseURL: redirectSrv.URL, APIKey: "ts-secret", HTTPClient: nil}
	if _, err := fallbackClient.ListModels(context.Background()); err == nil {
		t.Fatalf("expected cross-origin/non-HTTPS redirect to fail on fallback client")
	}

	// 3. ListModels must propagate body read errors rather than ignoring them.
	errBodyClient := NewClient("ts-secret", "https://api.typesafe.ai", DefaultModel)
	errBodyClient.HTTPClient = &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       failingBody{},
				Header:     make(http.Header),
			}, nil
		}),
	}
	if _, err := errBodyClient.ListModels(context.Background()); err == nil || !strings.Contains(err.Error(), "simulated stream read error") {
		t.Fatalf("expected ListModels to propagate body read error, got %v", err)
	}
}
