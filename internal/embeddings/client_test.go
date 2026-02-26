package embeddings

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestEmbedRetriesThenSucceeds(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := atomic.AddInt32(&calls, 1)
		if call <= 2 {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":"upstream"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[0.1,0.2]}],"model":"test"}`))
	}))
	defer server.Close()

	client := NewHTTPClient(server.URL, "test-key", "test-model", 0)
	vectors, err := client.Embed(context.Background(), []string{"hello"})
	if err != nil {
		t.Fatalf("Embed() error: %v", err)
	}
	if len(vectors) != 1 || len(vectors[0]) != 2 {
		t.Fatalf("unexpected vectors: %#v", vectors)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("expected 3 attempts, got %d", got)
	}
}

func TestEmbedFailsAfterRetries(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"upstream"}`))
	}))
	defer server.Close()

	client := NewHTTPClient(server.URL, "test-key", "test-model", 0)
	_, err := client.Embed(context.Background(), []string{"hello"})
	if err == nil {
		t.Fatal("expected Embed() error")
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("expected 3 attempts, got %d", got)
	}
}

func TestEmbedDoesNotRetryClientError(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"bad request"}`))
	}))
	defer server.Close()

	client := NewHTTPClient(server.URL, "test-key", "test-model", 0)
	_, err := client.Embed(context.Background(), []string{"hello"})
	if err == nil {
		t.Fatal("expected Embed() error")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected 1 attempt for non-retryable error, got %d", got)
	}
}
