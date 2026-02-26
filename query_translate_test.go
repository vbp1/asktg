package main

import (
	"context"
	"testing"
)

func TestQueryHasCyrillic(t *testing.T) {
	if !queryHasCyrillic("логи postgres") {
		t.Fatal("expected cyrillic query to be detected")
	}
	if queryHasCyrillic("postgres logs") {
		t.Fatal("did not expect cyrillic letters in latin query")
	}
}

func TestParseQueryTranslation(t *testing.T) {
	content := "```json\n{\"translation\":\"postgres logs\"}\n```"
	got, err := parseQueryTranslation(content)
	if err != nil {
		t.Fatalf("parseQueryTranslation() error: %v", err)
	}
	if got != "postgres logs" {
		t.Fatalf("translation=%q want=%q", got, "postgres logs")
	}
}

func TestSanitizeQueryTranslation(t *testing.T) {
	if got := sanitizeQueryTranslation("логи postgres", "  postgres logs  "); got != "postgres logs" {
		t.Fatalf("unexpected sanitized translation: %q", got)
	}
	if got := sanitizeQueryTranslation("postgres logs", "POSTGRES LOGS"); got != "" {
		t.Fatalf("expected empty translation for unchanged query, got %q", got)
	}
}

func TestQueryTranslateConfigFromEnv(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "env-key")
	app := NewApp()
	cfg := app.queryTranslateConfig(context.Background())
	if cfg.APIKey != "env-key" {
		t.Fatalf("api key mismatch: got=%q want=%q", cfg.APIKey, "env-key")
	}
	if cfg.Model != queryTranslateModelDefault {
		t.Fatalf("model mismatch: got=%q want=%q", cfg.Model, queryTranslateModelDefault)
	}
}
