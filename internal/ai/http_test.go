package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAIRequestAndReply(t *testing.T) {
	var got struct {
		Model    string        `json:"model"`
		Messages []chatMessage `json:"messages"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer secret" {
			t.Errorf("Authorization = %q", auth)
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		io.WriteString(w, `{"choices":[{"message":{"content":"feat: from a model\n\nA body that is too much."}}]}`)
	}))
	defer server.Close()
	cfg := Config{Provider: ProviderOpenAI, BaseURL: server.URL + "/v1", Model: "test-model", APIKey: "secret", SubjectOnly: true}
	gotMessage, err := Generate(context.Background(), cfg, Request{Root: "/tmp/repo", Diff: "+a\n"})
	if err != nil {
		t.Fatal(err)
	}
	if want := "feat: from a model"; gotMessage != want {
		t.Errorf("message = %q, want %q", gotMessage, want)
	}
	if got.Model != "test-model" || len(got.Messages) != 2 {
		t.Errorf("request = %+v", got)
	}
	if !strings.Contains(got.Messages[1].Content, "+a") {
		t.Errorf("diff missing: %q", got.Messages[1].Content)
	}
}

func TestAnthropicRequestAndReply(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if key := r.Header.Get("x-api-key"); key != "secret" {
			t.Errorf("x-api-key = %q", key)
		}
		if version := r.Header.Get("anthropic-version"); version == "" {
			t.Error("no anthropic-version")
		}
		io.WriteString(w, `{"content":[{"type":"text","text":"fix: from anthropic"}]}`)
	}))
	defer server.Close()
	cfg := Config{Provider: ProviderAnthropic, BaseURL: server.URL, Model: "claude", APIKey: "secret"}
	got, err := Generate(context.Background(), cfg, Request{Root: "/tmp/repo", Diff: "+a\n"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "fix: from anthropic" {
		t.Errorf("message = %q", got)
	}
}

func TestProviderError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"message":"invalid key"}}`)
	}))
	defer server.Close()
	cfg := Config{Provider: ProviderCompatible, BaseURL: server.URL, Model: "m"}
	_, err := Generate(context.Background(), cfg, Request{})
	if err == nil || !strings.Contains(err.Error(), "invalid key") {
		t.Errorf("err = %v", err)
	}
}

func TestCheckHTTP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[{"message":{"content":"OK"}}]}`)
	}))
	defer server.Close()
	cfg := Config{Provider: ProviderOpenAI, BaseURL: server.URL + "/v1", Model: "test-model", APIKey: "secret"}
	detail, err := Check(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if detail != "OpenAI · test-model" {
		t.Errorf("detail = %q", detail)
	}
}
