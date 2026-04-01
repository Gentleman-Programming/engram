// Package embedding provides pluggable embedding providers for vector search.
//
// When configured, embeddings are generated for observations on save and used
// alongside FTS5 for hybrid search. When no provider is configured, Engram
// falls back to FTS5-only search with zero overhead.
package embedding

import (
	"context"
	"fmt"
)

// Provider generates embedding vectors for text.
// Implementations must be safe for concurrent use.
type Provider interface {
	// Embed returns a float32 vector for the given text.
	Embed(ctx context.Context, text string) ([]float32, error)

	// Dimensions returns the vector dimensionality (e.g., 768, 1536).
	Dimensions() int

	// ModelName returns the model identifier used for tracking.
	ModelName() string
}

// Config holds the configuration for an embedding provider.
type Config struct {
	Provider string // "ollama", "openai", "none", or ""
	Model    string // e.g., "nomic-embed-text", "text-embedding-3-small"
	URL      string // e.g., "http://localhost:11434" for Ollama
	APIKey   string // for OpenAI (typically from ENGRAM_EMBEDDING_API_KEY env)
}

// NewProvider creates an embedding provider from the given configuration.
// Returns nil if the provider is "none" or empty (embeddings disabled).
func NewProvider(cfg Config) (Provider, error) {
	switch cfg.Provider {
	case "", "none":
		return nil, nil
	case "ollama":
		if cfg.URL == "" {
			cfg.URL = "http://localhost:11434"
		}
		if cfg.Model == "" {
			cfg.Model = "nomic-embed-text"
		}
		return NewOllamaProvider(cfg.URL, cfg.Model)
	case "openai":
		if cfg.Model == "" {
			cfg.Model = "text-embedding-3-small"
		}
		if cfg.APIKey == "" {
			return nil, fmt.Errorf("embedding: openai provider requires API key (set ENGRAM_EMBEDDING_API_KEY)")
		}
		return NewOpenAIProvider(cfg.APIKey, cfg.Model)
	default:
		return nil, fmt.Errorf("embedding: unknown provider %q (supported: ollama, openai, none)", cfg.Provider)
	}
}
