package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"time"
)

// Client is the HTTP client wrapper for the engram cloud server.
// It handles authentication, retries with exponential backoff, and error mapping.
type Client struct {
	http        *http.Client
	baseURL     string
	apiKey      string
	version     string
	maxRetry    int
	baseBackoff int64 // milliseconds, exposed for testing
}

// NewClient creates a new cloud API client. Returns ErrUnauthorized if apiKey is empty.
func NewClient(baseURL, apiKey, version string) (*Client, error) {
	if apiKey == "" {
		return nil, ErrUnauthorized
	}

	transport := &http.Transport{
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
		DialContext: (&net.Dialer{
			Timeout: 10 * time.Second,
		}).DialContext,
	}

	return &Client{
		http: &http.Client{
			Transport: transport,
			Timeout:   30 * time.Second,
		},
		baseURL:     baseURL,
		apiKey:      apiKey,
		version:     version,
		maxRetry:    3,
		baseBackoff: 500,
	}, nil
}

// Get performs a GET request to the given path.
func (c *Client) Get(ctx context.Context, path string) (json.RawMessage, error) {
	return c.do(ctx, http.MethodGet, path, nil)
}

// Post performs a POST request to the given path with a JSON body.
func (c *Client) Post(ctx context.Context, path string, body []byte) (json.RawMessage, error) {
	return c.do(ctx, http.MethodPost, path, body)
}

// Delete performs a DELETE request to the given path.
func (c *Client) Delete(ctx context.Context, path string) (json.RawMessage, error) {
	return c.do(ctx, http.MethodDelete, path, nil)
}

func (c *Client) do(ctx context.Context, method, path string, body []byte) (json.RawMessage, error) {
	var lastErr error

	for attempt := 0; attempt < c.maxRetry; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		var bodyReader io.Reader
		if body != nil {
			bodyReader = bytes.NewReader(body)
		}

		req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
		if err != nil {
			return nil, fmt.Errorf("build request: %w", err)
		}

		req.Header.Set("Authorization", "Bearer "+c.apiKey)
		req.Header.Set("User-Agent", "engram-client/"+c.version)
		req.Header.Set("X-Engram-Protocol", "1")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if attempt < c.maxRetry-1 {
				c.sleep(ctx, attempt)
				continue
			}
			return nil, lastErr
		}
		defer resp.Body.Close()

		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("read response: %w", err)
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return json.RawMessage(respBody), nil
		}

		lastErr = c.mapError(resp.StatusCode)

		// Retry on 429 and 5xx only
		if resp.StatusCode == 429 || resp.StatusCode >= 500 {
			if attempt < c.maxRetry-1 {
				c.sleep(ctx, attempt)
				continue
			}
			return nil, lastErr
		}

		// No retry for other 4xx
		return nil, lastErr
	}

	return nil, lastErr
}

func (c *Client) mapError(status int) error {
	switch {
	case status == 401 || status == 403:
		return fmt.Errorf("HTTP %d: %w", status, ErrUnauthorized)
	case status == 404:
		return fmt.Errorf("HTTP %d: %w", status, ErrNotFound)
	case status == 429:
		return fmt.Errorf("HTTP %d: %w", status, ErrRateLimited)
	case status >= 500:
		return fmt.Errorf("HTTP %d: %w", status, ErrServerError)
	default:
		return fmt.Errorf("HTTP %d: unexpected status", status)
	}
}

func (c *Client) sleep(ctx context.Context, attempt int) {
	backoff := c.baseBackoff << uint(attempt) // exponential: base * 2^attempt
	if backoff > 30000 {
		backoff = 30000 // cap at 30s
	}
	// jitter ±10%
	jitter := backoff / 10
	backoff = backoff - jitter + rand.Int63n(2*jitter+1)

	select {
	case <-time.After(time.Duration(backoff) * time.Millisecond):
	case <-ctx.Done():
	}
}
