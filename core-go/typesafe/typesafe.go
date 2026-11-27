package typesafe

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

const (
	defaultRequestTimeout = 45 * time.Second
	maxErrorBodyBytes     = 16 * 1024
)

// Client calls the TypeSafe AI HTTP API (`POST /v1/systemone` and `GET /v1/models`).
type Client struct {
	BaseURL    string
	APIKey     string
	Model      string
	HTTPClient *http.Client
}

// NewClient constructs a TypeSafe AI client with sensible defaults.
func NewClient(apiKey, baseURL, model string) *Client {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		base = DefaultBaseURL
	}
	mod := strings.TrimSpace(model)
	if mod == "" {
		mod = DefaultModel
	}
	return &Client{
		BaseURL: base,
		APIKey:  strings.TrimSpace(apiKey),
		Model:   mod,
		HTTPClient: &http.Client{
			Timeout: defaultRequestTimeout,
		},
	}
}

func newAssetRef() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("ts-img-%d", time.Now().UnixNano())
	}
	// UUID-style opaque reference that cannot collide with English words in instructions/criteria.
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		hex.EncodeToString(b[0:4]),
		hex.EncodeToString(b[4:6]),
		hex.EncodeToString(b[6:8]),
		hex.EncodeToString(b[8:10]),
		hex.EncodeToString(b[10:16]),
	)
}

// EvaluateScreen sends ONLY the visual screen PNG (no code, no scene JSON) in a multipart `POST /v1/systemone`
// request, evaluates all visual criteria in parallel, and computes the 0–100 Decision.
func (c *Client) EvaluateScreen(ctx context.Context, rawScreenPNG []byte, passScore int) (*Decision, error) {
	if strings.TrimSpace(c.APIKey) == "" {
		return nil, errors.New("missing TypeSafe API key")
	}
	preparedPNG, _, _, err := PrepareScreenPNG(rawScreenPNG)
	if err != nil {
		return nil, err
	}

	ref := newAssetRef()
	const partName = "screen_image"

	stateObj := jsonv.NewObject()
	stateObj.Set("screen", ref)

	assetEntry := jsonv.NewObject()
	assetEntry.Set("type", "image")
	assetEntry.Set("part", partName)
	assetsObj := jsonv.NewObject()
	assetsObj.Set(ref, assetEntry)

	reqJSON := jsonv.NewObject()
	reqJSON.Set("model", c.effectiveModel())
	reqJSON.Set("state", stateObj)
	reqJSON.Set("assets", assetsObj)
	reqJSON.Set("questions", Questions())
	jsonPayload := jsonv.Marshal(reqJSON)

	buildBody := func() ([]byte, string, error) {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)

		// Rule 1 from TypeSafe HTTP docs: first part is named `request` with Content-Type `application/json`.
		reqHdr := make(textproto.MIMEHeader)
		reqHdr.Set("Content-Disposition", `form-data; name="request"`)
		reqHdr.Set("Content-Type", "application/json")
		pw, err := mw.CreatePart(reqHdr)
		if err != nil {
			return nil, "", err
		}
		if _, err := pw.Write(jsonPayload); err != nil {
			return nil, "", err
		}

		// Rule 2: image file part named `screen_image` with Content-Type `image/png`.
		imgHdr := make(textproto.MIMEHeader)
		imgHdr.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename="screen.png"`, partName))
		imgHdr.Set("Content-Type", "image/png")
		iw, err := mw.CreatePart(imgHdr)
		if err != nil {
			return nil, "", err
		}
		if _, err := iw.Write(preparedPNG); err != nil {
			return nil, "", err
		}

		if err := mw.Close(); err != nil {
			return nil, "", err
		}
		return buf.Bytes(), mw.FormDataContentType(), nil
	}

	bodyBytes, contentType, err := buildBody()
	if err != nil {
		return nil, fmt.Errorf("build TypeSafe multipart payload: %w", err)
	}

	respVal, err := c.postWithRetry(ctx, c.endpoint("/v1/systemone"), contentType, bodyBytes, true)
	if err != nil {
		return nil, err
	}
	return ComputeDecision(respVal, passScore)
}

// TestConnection verifies the TypeSafe BYOK API key and selected model with a lightweight 1-question System One probe.
func (c *Client) TestConnection(ctx context.Context) (string, error) {
	if strings.TrimSpace(c.APIKey) == "" {
		return "", errors.New("Enter a TypeSafe API key first.")
	}
	q := jsonv.NewObject()
	q.Set("type", "noul")
	q.Set("instructions", "Does `sample` describe dark text on a light background?")
	qs := jsonv.NewObject()
	qs.Set("probe", q)

	st := jsonv.NewObject()
	st.Set("sample", "Deep charcoal text (#18181B) on a warm off-white page (#FAF9F6).")

	reqObj := jsonv.NewObject()
	reqObj.Set("model", c.effectiveModel())
	reqObj.Set("state", st)
	reqObj.Set("questions", qs)

	respVal, err := c.postWithRetry(ctx, c.endpoint("/v1/systemone"), "application/json", jsonv.Marshal(reqObj), false)
	if err != nil {
		return "", err
	}
	if jsonv.Obj(jsonv.Get(respVal, "answers")) == nil {
		return "", errors.New("unexpected response from TypeSafe API")
	}
	modelUsed := jsonv.StrOr(jsonv.Get(respVal, "model"))
	if modelUsed == "" {
		modelUsed = c.effectiveModel()
	}
	return modelUsed, nil
}

// ListModels fetches available model aliases/IDs from `GET /v1/models`.
func (c *Client) ListModels(ctx context.Context) ([]string, error) {
	if strings.TrimSpace(c.APIKey) == "" {
		return nil, errors.New("Enter a TypeSafe API key first.")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint("/v1/models"), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("TypeSafe API unreachable: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, formatHTTPError(resp.StatusCode, raw)
	}
	parsed, err := jsonv.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid JSON from TypeSafe /v1/models: %w", err)
	}
	seen := map[string]bool{}
	var out []string
	add := func(m string) {
		m = strings.TrimSpace(m)
		if m != "" && !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	for _, item := range jsonv.Items(jsonv.Get(parsed, "models")) {
		add(jsonv.StrOr(jsonv.Get(item, "name")))
		add(jsonv.StrOr(jsonv.Get(item, "id")))
	}
	for _, item := range jsonv.Items(jsonv.Get(parsed, "data")) {
		add(jsonv.StrOr(jsonv.Get(item, "id")))
		add(jsonv.StrOr(jsonv.Get(item, "name")))
	}
	for _, km := range KnownModels {
		add(km)
	}
	return out, nil
}

func (c *Client) effectiveModel() string {
	if m := strings.TrimSpace(c.Model); m != "" {
		return m
	}
	return DefaultModel
}

func (c *Client) endpoint(path string) string {
	base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if base == "" {
		base = DefaultBaseURL
	}
	base = strings.TrimSuffix(base, "/v1")
	return base + path
}

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return &http.Client{Timeout: defaultRequestTimeout}
}

func (c *Client) postWithRetry(ctx context.Context, url, contentType string, body []byte, withImages bool) (jsonv.Value, error) {
	const maxAttempts = 3
	backoff := 600 * time.Millisecond

	for attempt := 0; attempt < maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
		req.Header.Set("Content-Type", contentType)
		req.Header.Set("Accept", "application/json")
		if withImages {
			req.Header.Set("TypeSafe-Danger-Zone-Images", "true")
		}

		resp, err := c.httpClient().Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("TypeSafe API request failed: %w", err)
		}
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
		resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("reading TypeSafe API response: %w", readErr)
		}

		if resp.StatusCode == http.StatusTooManyRequests && attempt+1 < maxAttempts {
			wait := backoff
			if ra := strings.TrimSpace(resp.Header.Get("Retry-After")); ra != "" {
				if secs, err := strconv.ParseFloat(ra, 64); err == nil && secs > 0 {
					wait = min(time.Duration(secs*float64(time.Second)), 8*time.Second)
				}
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
			backoff *= 2
			continue
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, formatHTTPError(resp.StatusCode, raw)
		}

		parsed, err := jsonv.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid JSON response from TypeSafe API: %w", err)
		}
		return parsed, nil
	}
	return nil, errors.New("TypeSafe API rate limit exceeded")
}

func formatHTTPError(status int, raw []byte) error {
	if len(raw) > maxErrorBodyBytes {
		raw = raw[:maxErrorBodyBytes]
	}
	if parsed, err := jsonv.Parse(raw); err == nil {
		if detail, ok := jsonv.Str(jsonv.Get(parsed, "detail")); ok && strings.TrimSpace(detail) != "" {
			return fmt.Errorf("TypeSafe API HTTP %d: %s", status, strings.TrimSpace(detail))
		}
		if detailObj := jsonv.Obj(jsonv.Get(parsed, "detail")); detailObj != nil {
			if msg, ok := jsonv.Str(jsonv.Get(detailObj, "message")); ok && strings.TrimSpace(msg) != "" {
				return fmt.Errorf("TypeSafe API HTTP %d: %s", status, strings.TrimSpace(msg))
			}
		}
		if items := jsonv.Items(jsonv.Get(parsed, "detail")); len(items) > 0 {
			if msg, ok := jsonv.Str(jsonv.Get(items[0], "msg")); ok && strings.TrimSpace(msg) != "" {
				return fmt.Errorf("TypeSafe API HTTP %d: %s", status, strings.TrimSpace(msg))
			}
		}
		if errMsg, ok := jsonv.Str(jsonv.Get(jsonv.Get(parsed, "error"), "message")); ok && strings.TrimSpace(errMsg) != "" {
			return fmt.Errorf("TypeSafe API HTTP %d: %s", status, strings.TrimSpace(errMsg))
		}
	}
	snippet := strings.TrimSpace(string(raw))
	if len(snippet) > 240 {
		snippet = snippet[:240] + "…"
	}
	if snippet == "" {
		return fmt.Errorf("TypeSafe API HTTP %d", status)
	}
	return fmt.Errorf("TypeSafe API HTTP %d: %s", status, snippet)
}
