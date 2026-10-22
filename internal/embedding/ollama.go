// Package embedding provides explicit, local-only Ollama embedding operations.
package embedding

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Config is process-local configuration; New never reads environment variables.
type Config struct{ BaseURL, Model string }

// Client owns a proxy-free transport. Construction performs no network I/O.
type Client struct {
	base  string
	model string
	http  *http.Client
}

// Identity binds a canonical installed model name to its full content digest.
type Identity struct{ Model, Digest string }

var (
	ErrInput        = errors.New("embedding: provide nonempty input texts")
	ErrUnavailable  = errors.New("embedding: local Ollama unavailable; start the configured daemon")
	ErrModel        = errors.New("embedding: model missing, ambiguous, or invalid; select an installed local model")
	ErrRemoteModel  = errors.New("embedding: cloud-backed models are prohibited; select an installed local model")
	ErrModelChanged = errors.New("embedding: model identity changed; resolve and rebuild the index")
	ErrResponse     = errors.New("embedding: invalid provider response")
	ErrVectors      = errors.New("embedding: invalid vector count, dimensions, or values")
)

const maxBody = 4 * 1024 * 1024

func validIdentity(id Identity) bool {
	digest, err := hex.DecodeString(id.Digest)
	if err != nil || len(digest) != 32 {
		return false
	}
	// A canonical installed name includes a nonempty tag after the final path segment.
	segment := id.Model[strings.LastIndex(id.Model, "/")+1:]
	colon := strings.LastIndex(segment, ":")
	if colon <= 0 || colon == len(segment)-1 {
		return false
	}
	for _, r := range id.Model {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && !strings.ContainsRune("/._-:", r) {
			return false
		}
	}
	return true
}

func compatible(name, canonical string) bool {
	return name == canonical || name+":latest" == canonical
}

// Resolve reads trusted daemon metadata without installing or pulling anything.
func (c *Client) Resolve(ctx context.Context) (Identity, error) {
	var response struct {
		Models []struct {
			Name        string `json:"name"`
			Model       string `json:"model"`
			Digest      string `json:"digest"`
			RemoteModel string `json:"remote_model"`
			RemoteHost  string `json:"remote_host"`
		} `json:"models"`
	}
	if err := c.request(ctx, "/api/tags", nil, &response); err != nil {
		return Identity{}, err
	}
	var found Identity
	count := 0
	for _, m := range response.Models {
		if !compatible(c.model, m.Name) && !compatible(c.model, m.Model) {
			continue
		}
		count++
		if m.RemoteModel != "" || m.RemoteHost != "" {
			return Identity{}, ErrRemoteModel
		}
		id := Identity{Model: m.Name, Digest: strings.ToLower(m.Digest)}
		if !validIdentity(id) || (m.Model != "" && m.Model != m.Name) {
			return Identity{}, ErrModel
		}
		found = id
	}
	if count != 1 {
		return Identity{}, ErrModel
	}
	return found, nil
}

// Embed checks identity before and after provider work and never returns rejected vectors.
func (c *Client) Embed(ctx context.Context, expected Identity, texts []string) ([][]float64, error) {
	if len(texts) == 0 {
		return nil, ErrInput
	}
	for _, text := range texts {
		if strings.TrimSpace(text) == "" {
			return nil, ErrInput
		}
	}
	if !validIdentity(expected) || !compatible(c.model, expected.Model) {
		return nil, ErrModelChanged
	}
	before, err := c.Resolve(ctx)
	if err != nil {
		return nil, err
	}
	if before != expected {
		return nil, ErrModelChanged
	}
	payload, err := json.Marshal(struct {
		Model    string   `json:"model"`
		Input    []string `json:"input"`
		Truncate bool     `json:"truncate"`
	}{expected.Model, texts, false})
	if err != nil {
		return nil, ErrInput
	}
	// Pointers distinguish JSON null from a legitimate zero component.
	var response struct {
		Model      string       `json:"model"`
		Embeddings [][]*float64 `json:"embeddings"`
	}
	if err := c.request(ctx, "/api/embed", payload, &response); err != nil {
		return nil, err
	}
	if !compatible(response.Model, expected.Model) {
		return nil, ErrModelChanged
	}
	if len(response.Embeddings) != len(texts) {
		return nil, ErrVectors
	}
	vectors := make([][]float64, len(texts))
	dimension := 0
	for i, row := range response.Embeddings {
		if i == 0 {
			dimension = len(row)
		}
		if dimension == 0 || len(row) != dimension {
			return nil, ErrVectors
		}
		vectors[i] = make([]float64, dimension)
		norm := 0.0
		for j, value := range row {
			if value == nil || math.IsNaN(*value) || math.IsInf(*value, 0) {
				return nil, ErrVectors
			}
			vectors[i][j] = *value
			norm = math.Hypot(norm, *value)
		}
		if norm == 0 || math.IsInf(norm, 0) {
			return nil, ErrVectors
		}
	}
	after, err := c.Resolve(ctx)
	if err != nil {
		return nil, err
	}
	if after != expected {
		return nil, ErrModelChanged
	}
	return vectors, nil
}

func (c *Client) request(ctx context.Context, path string, payload []byte, output any) error {
	method := http.MethodGet
	if payload != nil {
		method = http.MethodPost
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(payload))
	if err != nil {
		return ErrConfig
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Preserve deadline classification without exposing transport URLs or bodies.
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("%w: %w", ErrUnavailable, context.DeadlineExceeded)
		}
		return ErrUnavailable
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("%w: HTTP %d", ErrUnavailable, response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBody+1))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("%w: %w", ErrUnavailable, context.DeadlineExceeded)
		}
		return ErrResponse
	}
	if len(body) > maxBody {
		return ErrResponse
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil || envelope == nil {
		return ErrResponse
	}
	if _, failed := envelope["error"]; failed {
		return ErrResponse
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(output); err != nil {
		return ErrResponse
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return ErrResponse
	}
	return nil
}

var ErrConfig = errors.New("embedding: require a model and a root HTTP loopback URL without credentials")

func New(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.Model) == "" {
		return nil, ErrConfig
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "http://127.0.0.1:11434"
	}
	u, err := url.Parse(cfg.BaseURL)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Opaque != "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(cfg.BaseURL, "#") {
		return nil, ErrConfig
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, ErrConfig
	}
	if strings.HasSuffix(u.Host, ":") {
		return nil, ErrConfig
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return nil, ErrConfig
		}
	}
	transport := &http.Transport{
		Proxy: nil,
		// Disabling connection reuse also prevents transparent retries on stale connections.
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			hostname, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, ErrConfig
			}
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, hostname)
			if err != nil {
				return nil, errors.New("embedding: loopback resolution failed")
			}
			if len(ips) == 0 {
				return nil, ErrConfig
			}
			for _, ip := range ips {
				if !ip.IP.IsLoopback() {
					return nil, ErrConfig
				}
			}
			return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
		},
	}
	return &Client{base: strings.TrimSuffix(u.String(), "/"), model: cfg.Model, http: &http.Client{
		Transport: transport, Timeout: 120 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("embedding: redirects prohibited") },
	}}, nil
}
