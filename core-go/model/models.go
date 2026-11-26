package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

const modelsTimeout = 15 * time.Second

const defaultAnthropicBase = "https://api.anthropic.com"
const defaultOpenAIBase = "https://api.openai.com/v1"

// FetchModels queries the remote provider's live model catalog endpoint without holding any engine lock.
// format may be "anthropic", "chat", or "responses".
func FetchModels(format, baseURL, apiKey string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), modelsTimeout)
	defer cancel()
	return FetchModelsContext(ctx, format, baseURL, apiKey)
}

// FetchModelsContext queries the provider's model listing endpoint with caller context, following Anthropic
// has_more / last_id pagination until all pages are collected.
func FetchModelsContext(ctx context.Context, format, baseURL, apiKey string) ([]string, error) {
	fmtKind := strings.ToLower(strings.TrimSpace(format))
	key := strings.TrimSpace(apiKey)
	base := strings.TrimSpace(baseURL)

	var baseEndpoint string
	isAnthropic := false
	switch fmtKind {
	case "", "anthropic":
		isAnthropic = true
		if base == "" {
			base = defaultAnthropicBase
		}
		b, _, _ := strings.Cut(base, "#")
		root, query, _ := strings.Cut(b, "?")
		baseEndpoint = trimAllSuffix(strings.TrimRight(root, "/"), "/v1") + "/v1/models"
		if query != "" {
			baseEndpoint += "?" + query
		}
	case "chat", "responses":
		if base == "" {
			base = defaultOpenAIBase
		}
		baseEndpoint = Endpoint(base, "/models")
	default:
		return nil, fmt.Errorf("unknown format %q", format)
	}

	parsedBase, err := url.Parse(baseEndpoint)
	if err != nil {
		return nil, err
	}
	originScheme := strings.ToLower(parsedBase.Scheme)
	originHost := strings.ToLower(parsedBase.Host)

	client := &http.Client{
		Timeout: modelsTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}
			if key != "" && (strings.ToLower(req.URL.Scheme) != originScheme || strings.ToLower(req.URL.Host) != originHost) {
				return errors.New("refusing cross-origin redirect during authenticated model discovery")
			}
			return nil
		},
	}
	seen := map[string]struct{}{}
	var out []string
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if _, dup := seen[id]; dup {
			return
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}

	afterID := ""
	const maxPages = 20
	for page := 0; page < maxPages; page++ {
		pageURL := baseEndpoint
		if isAnthropic && afterID != "" {
			u := *parsedBase
			q := u.Query()
			q.Set("after_id", afterID)
			u.RawQuery = q.Encode()
			pageURL = u.String()
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
		if err != nil {
			return nil, err
		}
		if isAnthropic {
			req.Header.Set("anthropic-version", "2023-06-01")
			if key != "" {
				req.Header.Set("x-api-key", key)
			}
		} else if key != "" {
			req.Header.Set("authorization", "Bearer "+key)
		}

		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		if resp.StatusCode >= 400 {
			msg := strings.TrimSpace(string(body))
			var errPayload struct {
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
				Message string `json:"message"`
			}
			if json.Unmarshal(body, &errPayload) == nil {
				if errPayload.Error.Message != "" {
					msg = errPayload.Error.Message
				} else if errPayload.Message != "" {
					msg = errPayload.Message
				}
			}
			if len(msg) > 300 {
				msg = msg[:300]
			}
			return nil, errors.New(providerError(resp.StatusCode, msg))
		}

		var parsed struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
			Models []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"models"`
			HasMore bool   `json:"has_more"`
			LastID  string `json:"last_id"`
		}
		if err := json.Unmarshal(body, &parsed); err != nil {
			return nil, fmt.Errorf("invalid models response: %w", err)
		}

		for _, item := range parsed.Data {
			add(item.ID)
		}
		for _, item := range parsed.Models {
			if item.ID != "" {
				add(item.ID)
			} else {
				add(strings.TrimPrefix(item.Name, "models/"))
			}
		}

		nextCursor := strings.TrimSpace(parsed.LastID)
		if nextCursor == "" && len(parsed.Data) > 0 {
			nextCursor = strings.TrimSpace(parsed.Data[len(parsed.Data)-1].ID)
		}
		if !isAnthropic || !parsed.HasMore || nextCursor == "" || nextCursor == afterID {
			break
		}
		if page == maxPages-1 {
			return nil, fmt.Errorf("model catalog exceeded %d pages; narrow the endpoint or enter the model ID manually", maxPages)
		}
		afterID = nextCursor
	}

	slices.Sort(out)
	return out, nil
}
