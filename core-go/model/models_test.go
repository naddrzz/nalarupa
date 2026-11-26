package model

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
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
		var mu sync.Mutex
		var calls []string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			calls = append(calls, r.URL.RequestURI())
			mu.Unlock()
			w.Header().Set("content-type", "application/json")
			if r.URL.Query().Get("after_id") == "" || r.URL.Query().Get("after_id") == "seed" {
				io.WriteString(w, `{"data":[{"id":"claude-fable-5-2"},{"id":"claude-opus-5-5"}],"has_more":true,"last_id":"claude-opus-5-5"}`)
				return
			}
			io.WriteString(w, `{"data":[{"id":"claude-opus-5-5"},{"id":"claude-sonnet-5-5"}],"has_more":false,"last_id":"claude-sonnet-5-5"}`)
		}))
		defer srv.Close()

		models, err := FetchModels("anthropic", srv.URL+"/v1?after_id=seed&via=proxy", "sk-ant-page")
		if err != nil {
			t.Fatalf("FetchModels anthropic pagination: %v", err)
		}
		mu.Lock()
		callsSnap := slices.Clone(calls)
		mu.Unlock()
		if len(callsSnap) != 2 {
			t.Fatalf("expected 2 pagination requests, got %v", callsSnap)
		}
		parsedReq2, err := url.ParseRequestURI(callsSnap[1])
		if err != nil {
			t.Fatalf("parse request 2 URI %q: %v", callsSnap[1], err)
		}
		afterVals := parsedReq2.Query()["after_id"]
		if len(afterVals) != 1 || afterVals[0] != "claude-opus-5-5" || parsedReq2.Query().Get("via") != "proxy" {
			t.Fatalf("unexpected pagination query on request 2: %q (after_id=%v)", callsSnap[1], afterVals)
		}
		want := []string{"claude-fable-5-2", "claude-opus-5-5", "claude-sonnet-5-5"}
		if !slices.Equal(models, want) {
			t.Fatalf("paginated models = %v, want %v", models, want)
		}
	})

	t.Run("anthropic_cross_origin_redirect_blocks_key", func(t *testing.T) {
		var leakedKey atomic.Bool
		evilSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("x-api-key") != "" || r.Header.Get("authorization") != "" {
				leakedKey.Store(true)
			}
			w.Header().Set("content-type", "application/json")
			io.WriteString(w, `{"data":[{"id":"evil-model"}]}`)
		}))
		defer evilSrv.Close()

		originSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, evilSrv.URL+"/v1/models", http.StatusFound)
		}))
		defer originSrv.Close()

		if _, err := FetchModels("anthropic", originSrv.URL+"/v1", "sk-secret-ant"); err == nil {
			t.Fatalf("expected cross-origin redirect to be rejected during authenticated model fetch")
		}
		if leakedKey.Load() {
			t.Fatalf("x-api-key leaked across cross-origin redirect!")
		}
	})

	t.Run("anthropic_pagination_exceeding_max_pages_errors", func(t *testing.T) {
		var pageCount atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n := pageCount.Add(1)
			w.Header().Set("content-type", "application/json")
			fmt.Fprintf(w, `{"data":[{"id":"m-%02d"}],"has_more":true,"last_id":"m-%02d"}`, n, n)
		}))
		defer srv.Close()

		if _, err := FetchModels("anthropic", srv.URL+"/v1", "sk-ant-many"); err == nil || !strings.Contains(err.Error(), "exceeded") {
			t.Fatalf("expected incomplete catalog error past maxPages, got %v", err)
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
