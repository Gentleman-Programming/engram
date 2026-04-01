package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// OllamaProvider generates embeddings via the Ollama REST API.
type OllamaProvider struct {
	url    string
	model  string
	dims   int
	client *http.Client
}

type ollamaRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

type ollamaResponse struct {
	Embedding []float64 `json:"embedding"`
}

// NewOllamaProvider creates a provider that calls Ollama's /api/embeddings endpoint.
// The dimensions are probed on first call and cached.
func NewOllamaProvider(url, model string) (*OllamaProvider, error) {
	return &OllamaProvider{
		url:    url,
		model:  model,
		client: &http.Client{},
	}, nil
}

func (p *OllamaProvider) Embed(ctx context.Context, text string) ([]float32, error) {
	body, err := json.Marshal(ollamaRequest{
		Model:  p.model,
		Prompt: text,
	})
	if err != nil {
		return nil, fmt.Errorf("ollama: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url+"/api/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ollama: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ollama: HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var result ollamaResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("ollama: decode response: %w", err)
	}

	if len(result.Embedding) == 0 {
		return nil, fmt.Errorf("ollama: empty embedding returned")
	}

	// Convert float64 to float32
	vec := make([]float32, len(result.Embedding))
	for i, v := range result.Embedding {
		vec[i] = float32(v)
	}

	// Cache dimensions from first successful response
	if p.dims == 0 {
		p.dims = len(vec)
	}

	return vec, nil
}

func (p *OllamaProvider) Dimensions() int {
	return p.dims
}

func (p *OllamaProvider) ModelName() string {
	return p.model
}

// MaxChars returns a conservative character limit based on the model's token context.
// Ollama models vary widely: nomic-embed-text=8192 tokens, mxbai-embed-large=512 tokens.
func (p *OllamaProvider) MaxChars() int {
	return ollamaModelMaxChars(p.model)
}

// ollamaModelMaxChars returns the max character limit for known Ollama embedding models.
// Token-to-char ratios vary wildly: English prose ~4 chars/token, but markdown with
// code blocks, pipes, and special characters can be ~1.5 chars/token. We use empirically
// tested limits that work with real-world mixed content.
func ollamaModelMaxChars(model string) int {
	// Empirically tested max chars for known models (real markdown/code content).
	known := map[string]int{
		"nomic-embed-text":       6000, // 8192 tokens, tested with markdown/code
		"mxbai-embed-large":      500,  // 512 tokens, very limited
		"all-minilm":             250,  // 256 tokens
		"snowflake-arctic-embed": 500,  // 512 tokens
	}
	if maxChars, ok := known[model]; ok {
		return maxChars
	}
	// Unknown model — conservative default.
	return 6000
}
