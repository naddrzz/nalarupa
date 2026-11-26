package model

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func TestFetchModelsAnthropicAndOpenAI(t *testing.T) {
	t.Run("anthropic", func(t *testing.T) {
		var gotPath, gotKey, gotVer string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.RequestURI()
			gotKey = r.Header.Get("x-api-key")
			gotVer = r.Header.Get("anthropic-version")
			w.Header().Set("content-type", "application/json")
			io.WriteString(w, `{"data":[{"id":"claude-fable-5-2"},{"id":"claude-opus-5-5"},{"id":"claude-opus-5-5"}]}`)
		}))
		defer srv.Close()

		models, err := FetchModels("anthropic", srv.URL+"/v1?via=proxy", "sk-ant-123")
		if err != nil {
			t.Fatalf("FetchModels anthropic: %v", err)
		}
		if gotPath != "/v1/models?via=proxy" {
			t.Fatalf("path = %q, want /v1/models?via=proxy", gotPath)
		}
		if gotKey != "sk-ant-123" || gotVer != "2023-06-01" {
			t.Fatalf("headers key=%q ver=%q", gotKey, gotVer)
		}
		want := []string{"claude-fable-5-2", "claude-opus-5-5"}
		if !slices.Equal(models, want) {
			t.Fatalf("models = %v, want %v", models, want)
		}
	})

	t.Run("anthropic_pagination", func(t *testing.T) {
		var calls []string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls = append(calls, r.URL.RequestURI())
			w.Header().Set("content-type", "application/json")
			if r.URL.Query().Get("after_id") == "" {
				io.WriteString(w, `{"data":[{"id":"claude-fable-5-2"},{"id":"claude-opus-5-5"}],"has_more":true,"last_id":"claude-opus-5-5"}`)
				return
			}
			io.WriteString(w, `{"data":[{"id":"claude-opus-5-5"},{"id":"claude-sonnet-5-5"}],"has_more":false,"last_id":"claude-sonnet-5-5"}`)
		}))
		defer srv.Close()

		models, err := FetchModels("anthropic", srv.URL+"/v1", "sk-ant-page")
		if err != nil {
			t.Fatalf("FetchModels anthropic pagination: %v", err)
		}
		if len(calls) != 2 || !strings.Contains(calls[1], "after_id=claude-opus-5-5") {
			t.Fatalf("unexpected pagination requests: %v", calls)
		}
		want := []string{"claude-fable-5-2", "claude-opus-5-5", "claude-sonnet-5-5"}
		if !slices.Equal(models, want) {
			t.Fatalf("paginated models = %v, want %v", models, want)
		}
	})

	t.Run("openai_chat_and_error", func(t *testing.T) {
		var gotAuth, gotPath string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			gotAuth = r.Header.Get("authorization")
			if r.URL.Query().Get("fail") == "1" {
				w.WriteHeader(http.StatusUnauthorized)
				io.WriteString(w, `{"error":{"message":"Bad API key"}}`)
				return
			}
			w.Header().Set("content-type", "application/json")
			io.WriteString(w, `{"data":[{"id":"gpt-6.2-sol"},{"id":"gpt-6.2-astra"}]}`)
		}))
		defer srv.Close()

		models, err := FetchModels("chat", srv.URL+"/v1", "sk-test")
		if err != nil {
			t.Fatalf("FetchModels chat: %v", err)
		}
		if gotPath != "/v1/models" || gotAuth != "Bearer sk-test" {
			t.Fatalf("path=%q auth=%q", gotPath, gotAuth)
		}
		want := []string{"gpt-6.2-astra", "gpt-6.2-sol"}
		if !slices.Equal(models, want) {
			t.Fatalf("models = %v, want %v", models, want)
		}

		_, err = FetchModels("responses", srv.URL+"/v1?fail=1", "bad")
		if err == nil || !strings.Contains(err.Error(), "rejected this key") {
			t.Fatalf("expected clean 401 message, got %v", err)
		}
	})
}
