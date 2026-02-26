package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode"
)

const (
	queryTranslateBaseURLDefault = "https://api.openai.com/v1"
	queryTranslateModelDefault   = "openai/gpt-4.1-mini"
	queryTranslateCacheTTL       = 12 * time.Hour
	queryTranslateTimeout        = 3 * time.Second
	queryTranslateHTTPTimeout    = 4 * time.Second
	queryTranslateRetries        = 2
	queryTranslateRetryBackoff   = 300 * time.Millisecond
)

type queryTranslationCacheEntry struct {
	Translation string
	ExpiresAt   time.Time
}

type queryTranslateConfig struct {
	BaseURL string
	Model   string
	APIKey  string
}

func (c queryTranslateConfig) Configured() bool {
	return strings.TrimSpace(c.BaseURL) != "" &&
		strings.TrimSpace(c.Model) != "" &&
		strings.TrimSpace(c.APIKey) != ""
}

func (a *App) translateQueryRUToEN(ctx context.Context, query string) string {
	clean := strings.TrimSpace(query)
	if clean == "" || !queryHasCyrillic(clean) {
		return ""
	}
	cacheKey := normalizeQueryTranslateKey(clean)
	if cacheKey == "" {
		return ""
	}
	if cached, ok := a.getCachedQueryTranslation(cacheKey); ok {
		return cached
	}

	cfg := a.queryTranslateConfig(ctx)
	if !cfg.Configured() {
		return ""
	}
	translated, err := requestQueryTranslation(ctx, cfg, clean)
	if err != nil {
		a.safeLogWarningf("query translation failed: %v", err)
		return ""
	}
	translated = sanitizeQueryTranslation(clean, translated)
	if translated == "" {
		return ""
	}
	a.putCachedQueryTranslation(cacheKey, translated)
	return translated
}

func normalizeQueryTranslateKey(raw string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(raw))), " ")
}

func (a *App) getCachedQueryTranslation(key string) (string, bool) {
	a.translateMu.RLock()
	entry, ok := a.translates[key]
	a.translateMu.RUnlock()
	if !ok {
		return "", false
	}
	if time.Now().After(entry.ExpiresAt) {
		a.translateMu.Lock()
		delete(a.translates, key)
		a.translateMu.Unlock()
		return "", false
	}
	return entry.Translation, entry.Translation != ""
}

func (a *App) putCachedQueryTranslation(key string, translated string) {
	if key == "" || strings.TrimSpace(translated) == "" {
		return
	}
	a.translateMu.Lock()
	if a.translates == nil {
		a.translates = make(map[string]queryTranslationCacheEntry)
	}
	a.translates[key] = queryTranslationCacheEntry{
		Translation: translated,
		ExpiresAt:   time.Now().Add(queryTranslateCacheTTL),
	}
	a.translateMu.Unlock()
}

func (a *App) queryTranslateConfig(ctx context.Context) queryTranslateConfig {
	cfg := queryTranslateConfig{
		BaseURL: queryTranslateBaseURLDefault,
		Model:   queryTranslateModelDefault,
	}
	if a.store == nil {
		cfg.APIKey = strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
		return cfg
	}

	embedBaseURL, _ := a.store.GetSetting(ctx, "embeddings_base_url", queryTranslateBaseURLDefault)
	cfg.BaseURL = strings.TrimSpace(embedBaseURL)
	embedAPIKey, embedAPIKeyErr := a.readSecretSetting(ctx, "embeddings_api_key")
	if embedAPIKeyErr == nil {
		cfg.APIKey = strings.TrimSpace(embedAPIKey)
	}

	if baseURL, err := a.store.GetSetting(ctx, "query_translation_base_url", ""); err == nil {
		override := strings.TrimSpace(baseURL)
		if override != "" {
			cfg.BaseURL = override
		}
	}
	if model, err := a.store.GetSetting(ctx, "query_translation_model", ""); err == nil {
		override := strings.TrimSpace(model)
		if override != "" {
			cfg.Model = override
		}
	}
	if apiKey, err := a.readSecretSetting(ctx, "query_translation_api_key"); err == nil {
		override := strings.TrimSpace(apiKey)
		if override != "" {
			cfg.APIKey = override
		}
	}
	if cfg.APIKey == "" {
		cfg.APIKey = strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	}
	return cfg
}

func queryHasCyrillic(raw string) bool {
	for _, r := range raw {
		if unicode.Is(unicode.Cyrillic, r) {
			return true
		}
	}
	return false
}

type queryTranslateMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type queryTranslateRequest struct {
	Model          string                  `json:"model"`
	Temperature    float64                 `json:"temperature"`
	Messages       []queryTranslateMessage `json:"messages"`
	ResponseFormat map[string]any          `json:"response_format,omitempty"`
}

type queryTranslateResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

type queryTranslateOutput struct {
	Translation string `json:"translation"`
}

type queryTranslateHTTPError struct {
	StatusCode int
	Payload    map[string]any
}

func (e *queryTranslateHTTPError) Error() string {
	return fmt.Sprintf("query translation request failed: status=%d body=%v", e.StatusCode, e.Payload)
}

func requestQueryTranslation(ctx context.Context, cfg queryTranslateConfig, query string) (string, error) {
	if !cfg.Configured() {
		return "", errors.New("query translation is not configured")
	}
	var lastErr error
	for attempt := 0; attempt <= queryTranslateRetries; attempt++ {
		translated, err := requestQueryTranslationOnce(ctx, cfg, query)
		if err == nil {
			return translated, nil
		}
		lastErr = err
		if attempt >= queryTranslateRetries || !shouldRetryQueryTranslate(err) {
			break
		}
		backoff := time.Duration(attempt+1) * queryTranslateRetryBackoff
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return "", lastErr
}

func requestQueryTranslationOnce(ctx context.Context, cfg queryTranslateConfig, query string) (string, error) {
	prompt := "Translate this search query from Russian to concise English for retrieval. " +
		"Keep product names, acronyms and code terms unchanged. " +
		"Return strict JSON object: {\"translation\":\"...\"}. Query: " + query
	body := queryTranslateRequest{
		Model:       cfg.Model,
		Temperature: 0,
		Messages: []queryTranslateMessage{
			{
				Role:    "system",
				Content: "You translate Russian technical search queries to English. Return valid JSON only.",
			},
			{Role: "user", Content: prompt},
		},
		ResponseFormat: map[string]any{"type": "json_object"},
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return "", err
	}

	reqCtx, cancel := context.WithTimeout(ctx, queryTranslateTimeout)
	defer cancel()

	endpoint := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: queryTranslateHTTPTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var payload map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&payload)
		return "", &queryTranslateHTTPError{
			StatusCode: resp.StatusCode,
			Payload:    payload,
		}
	}

	var parsed queryTranslateResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", err
	}
	if len(parsed.Choices) == 0 {
		return "", errors.New("query translation returned no choices")
	}
	content := strings.TrimSpace(parsed.Choices[0].Message.Content)
	if content == "" {
		return "", errors.New("query translation returned empty content")
	}
	return parseQueryTranslation(content)
}

func parseQueryTranslation(content string) (string, error) {
	var out queryTranslateOutput
	if err := json.Unmarshal([]byte(content), &out); err == nil {
		return strings.TrimSpace(out.Translation), nil
	}
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start < 0 || end <= start {
		return "", errors.New("query translation output is not valid json")
	}
	snippet := content[start : end+1]
	if err := json.Unmarshal([]byte(snippet), &out); err != nil {
		return "", err
	}
	return strings.TrimSpace(out.Translation), nil
}

func sanitizeQueryTranslation(source, translated string) string {
	clean := strings.Join(strings.Fields(strings.TrimSpace(translated)), " ")
	if clean == "" {
		return ""
	}
	if normalizeQueryTranslateKey(source) == normalizeQueryTranslateKey(clean) {
		return ""
	}
	return clean
}

func shouldRetryQueryTranslate(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	var statusErr *queryTranslateHTTPError
	if errors.As(err, &statusErr) {
		if statusErr.StatusCode == http.StatusTooManyRequests {
			return true
		}
		if statusErr.StatusCode >= 500 {
			return true
		}
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return netErr.Timeout() || netErr.Temporary()
	}
	return false
}
