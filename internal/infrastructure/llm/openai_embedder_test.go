package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNewOpenAIEmbedderUsesIndependentConfiguration(t *testing.T) {
	requests := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" {
			t.Errorf("request path = %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer embedding-test-key" {
			t.Error("request did not use embedding API key")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		requests <- body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"embedding":[0.5,0.25],"index":0}],"model":"embedding-test-model","usage":{"prompt_tokens":1,"total_tokens":1}}`))
	}))
	defer server.Close()

	embedder, err := NewOpenAIEmbedder(context.Background(), EmbeddingConfig{
		APIKey:  "embedding-test-key",
		BaseURL: server.URL,
		Model:   "embedding-test-model",
		Timeout: time.Second,
	})
	if err != nil {
		t.Fatalf("NewOpenAIEmbedder returned error: %v", err)
	}
	vector, err := embedder.EmbedText(context.Background(), "text")
	if err != nil {
		t.Fatalf("EmbedText returned error: %v", err)
	}
	if len(vector) != 2 || vector[0] != 0.5 || vector[1] != 0.25 {
		t.Fatalf("vector = %#v", vector)
	}
	body := <-requests
	if body["model"] != "embedding-test-model" {
		t.Fatalf("request body = %#v", body)
	}
}

func TestNewOpenAIEmbedderAppliesTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()

	embedder, err := NewOpenAIEmbedder(context.Background(), EmbeddingConfig{
		APIKey:  "embedding-test-key",
		BaseURL: server.URL,
		Model:   "embedding-test-model",
		Timeout: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewOpenAIEmbedder returned error: %v", err)
	}
	_, err = embedder.EmbedText(context.Background(), "text")
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) || !providerErr.Retryable {
		t.Fatalf("EmbedText error = %v, want retryable provider timeout", err)
	}
}
