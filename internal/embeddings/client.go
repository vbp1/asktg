package embeddings

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
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
			Timeout: 45 * time.Second,
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

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/embeddings", bytes.NewReader(encoded))
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
		return nil, fmt.Errorf("embeddings request failed: status=%d body=%v", resp.StatusCode, payload)
	}

	var parsed embeddingsResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	if len(parsed.Data) == 0 {
		return nil, errors.New("empty embeddings response")
	}
	out := make([][]float32, len(input))
	for _, item := range parsed.Data {
		if item.Index < 0 || item.Index >= len(input) {
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

type StubClient struct{}

func NewStubClient() *StubClient { return &StubClient{} }

func (s *StubClient) Embed(_ context.Context, input []string) ([][]float32, error) {
	out := make([][]float32, len(input))
	for idx := range input {
		out[idx] = []float32{}
	}
	return out, nil
}
