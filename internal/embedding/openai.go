package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// OpenAIProvider generates embeddings via the OpenAI API.
type OpenAIProvider struct {
	apiKey string
	model  string
	dims   int
	client *http.Client
}

type openAIRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

type openAIResponse struct {
	Data []struct {
		Embedding []float64 `json:"embedding"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// NewOpenAIProvider creates a provider that calls the OpenAI embeddings API.
func NewOpenAIProvider(apiKey, model string) (*OpenAIProvider, error) {
	return &OpenAIProvider{
		apiKey: apiKey,
		model:  model,
		client: &http.Client{},
	}, nil
}

func (p *OpenAIProvider) Embed(ctx context.Context, text string) ([]float32, error) {
	body, err := json.Marshal(openAIRequest{
		Model: p.model,
		Input: text,
	})
	if err != nil {
		return nil, fmt.Errorf("openai: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.openai.com/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("openai: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("openai: HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var result openAIResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("openai: decode response: %w", err)
	}

	if result.Error != nil {
		return nil, fmt.Errorf("openai: API error: %s", result.Error.Message)
	}

	if len(result.Data) == 0 || len(result.Data[0].Embedding) == 0 {
		return nil, fmt.Errorf("openai: empty embedding returned")
	}

	// Convert float64 to float32
	vec := make([]float32, len(result.Data[0].Embedding))
	for i, v := range result.Data[0].Embedding {
		vec[i] = float32(v)
	}

	if p.dims == 0 {
		p.dims = len(vec)
	}

	return vec, nil
}

func (p *OpenAIProvider) Dimensions() int {
	return p.dims
}

func (p *OpenAIProvider) ModelName() string {
	return p.model
}

// MaxChars returns a conservative character limit for OpenAI embedding models.
// All current OpenAI embedding models support 8,191 tokens.
func (p *OpenAIProvider) MaxChars() int {
	return 8191 * 2 // ~2 chars per token for mixed code/prose
}
