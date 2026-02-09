package urlfetch

import (
	"context"
	"errors"
	"testing"
)

func TestExtractURLs(t *testing.T) {
	text := "links: https://example.com/page https://example.com/page http://example.org/test."
	urls := ExtractURLs(text, 5)
	if len(urls) != 2 {
		t.Fatalf("expected 2 unique urls, got %d", len(urls))
	}
	if urls[0] != "https://example.com/page" {
		t.Fatalf("unexpected first url: %s", urls[0])
	}
	if urls[1] != "http://example.org/test" {
		t.Fatalf("unexpected second url: %s", urls[1])
	}
}

func TestFetchRejectsLocalhost(t *testing.T) {
	_, err := Fetch(context.Background(), "http://127.0.0.1/test")
	if err == nil {
		t.Fatal("expected localhost URL to be blocked")
	}
	if !errors.Is(err, ErrURLBlocked) {
		t.Fatalf("expected ErrURLBlocked, got %v", err)
	}
}

func TestExtractContentHTML(t *testing.T) {
	title, text := extractContent([]byte(`<html><head><title>Example Title</title></head><body>Hello <b>world</b></body></html>`), "text/html")
	if title != "Example Title" {
		t.Fatalf("unexpected title: %q", title)
	}
	if text == "" {
		t.Fatal("expected non-empty extracted text")
	}
}
