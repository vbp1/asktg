package embeddings

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

const (
	requestTimeout = 3 * time.Second
	httpTimeout    = 4 * time.Second
	retries        = 2
	retryBackoff   = 300 * time.Millisecond
)

type Client interface {
	Embed(ctx context.Context, input []string) ([][]float32, error)
}

type HTTPClient struct {
	baseURL    string
	apiKey     string
	model      string
	dimensions int
	client     *http.Client
}

func NewHTTPClient(baseURL, apiKey, model string, dimensions int) *HTTPClient {
	cleanBase := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if cleanBase == "" {
		cleanBase = "https://api.openai.com/v1"
	}
	cleanModel := strings.TrimSpace(model)
	if cleanModel == "" {
		cleanModel = "text-embedding-3-large"
	}
	return &HTTPClient{
		baseURL:    cleanBase,
		apiKey:     strings.TrimSpace(apiKey),
		model:      cleanModel,
		dimensions: dimensions,
		client: &http.Client{
			Timeout: httpTimeout,
		},
	}
}

func (c *HTTPClient) Configured() bool {
	return strings.TrimSpace(c.apiKey) != ""
}

type embeddingsRequest struct {
	Model          string   `json:"model"`
	Input          []string `json:"input"`
	EncodingFormat string   `json:"encoding_format,omitempty"`
	Dimensions     int      `json:"dimensions,omitempty"`
}

type embeddingsResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Model string `json:"model"`
}

func (c *HTTPClient) Embed(ctx context.Context, input []string) ([][]float32, error) {
	if len(input) == 0 {
		return nil, errors.New("input is required")
	}
	if !c.Configured() {
		return nil, errors.New("embeddings api key is not configured")
	}

	requestBody := embeddingsRequest{
		Model:          c.model,
		Input:          input,
		EncodingFormat: "float",
	}
	if c.dimensions > 0 {
		requestBody.Dimensions = c.dimensions
	}
	encoded, err := json.Marshal(requestBody)
	if err != nil {
		return nil, err
	}

	var lastErr error
	for attempt := 0; attempt <= retries; attempt++ {
		vectors, embedErr := c.embedOnce(ctx, encoded, len(input))
		if embedErr == nil {
			return vectors, nil
		}
		lastErr = embedErr
		if attempt >= retries || !shouldRetryEmbedError(embedErr) {
			break
		}
		backoff := time.Duration(attempt+1) * retryBackoff
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, lastErr
}

type requestError struct {
	StatusCode int
	Payload    map[string]any
}

func (e *requestError) Error() string {
	return fmt.Sprintf("embeddings request failed: status=%d body=%v", e.StatusCode, e.Payload)
}

func (c *HTTPClient) embedOnce(ctx context.Context, encoded []byte, inputCount int) ([][]float32, error) {
	reqCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.baseURL+"/embeddings", bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var payload map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&payload)
		return nil, &requestError{
			StatusCode: resp.StatusCode,
			Payload:    payload,
		}
	}

	var parsed embeddingsResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	if len(parsed.Data) == 0 {
		return nil, errors.New("empty embeddings response")
	}
	out := make([][]float32, inputCount)
	for _, item := range parsed.Data {
		if item.Index < 0 || item.Index >= inputCount {
			continue
		}
		out[item.Index] = item.Embedding
	}
	for idx, vector := range out {
		if len(vector) == 0 {
			return nil, fmt.Errorf("missing embedding for item %d", idx)
		}
	}
	return out, nil
}

func shouldRetryEmbedError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	var statusErr *requestError
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

type StubClient struct{}

func NewStubClient() *StubClient { return &StubClient{} }

func (s *StubClient) Embed(_ context.Context, input []string) ([][]float32, error) {
	out := make([][]float32, len(input))
	for idx := range input {
		out[idx] = []float32{}
	}
	return out, nil
}
