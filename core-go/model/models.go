package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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

// FetchModelsContext queries the provider's model listing endpoint with caller context.
func FetchModelsContext(ctx context.Context, format, baseURL, apiKey string) ([]string, error) {
	fmtKind := strings.ToLower(strings.TrimSpace(format))
	key := strings.TrimSpace(apiKey)
	base := strings.TrimSpace(baseURL)

	var url string
	switch fmtKind {
	case "", "anthropic":
		if base == "" {
			base = defaultAnthropicBase
		}
		b, _, _ := strings.Cut(base, "#")
		root, query, _ := strings.Cut(b, "?")
		url = trimAllSuffix(strings.TrimRight(root, "/"), "/v1") + "/v1/models"
		if query != "" {
			url += "?" + query
		}
	case "chat", "responses":
		if base == "" {
			base = defaultOpenAIBase
		}
		url = Endpoint(base, "/models")
	default:
		return nil, fmt.Errorf("unknown format %q", format)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if fmtKind == "" || fmtKind == "anthropic" {
		req.Header.Set("anthropic-version", "2023-06-01")
		if key != "" {
			req.Header.Set("x-api-key", key)
		}
	} else if key != "" {
		req.Header.Set("authorization", "Bearer "+key)
	}

	client := &http.Client{Timeout: modelsTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
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
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("invalid models response: %w", err)
	}

	seen := make(map[string]struct{}, len(parsed.Data)+len(parsed.Models))
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
	slices.Sort(out)
	return out, nil
}
