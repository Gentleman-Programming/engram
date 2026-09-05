package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// ─── MiniMax provider configuration ────────────────────────────────────────────

// Text model identifiers accepted by the MiniMax runner.
const (
	// MiniMaxDefaultModel is used when ENGRAM_MINIMAX_MODEL is unset.
	MiniMaxDefaultModel = "MiniMax-M3"

	// MiniMaxModelM3 is the flagship text model (1,000,000-token context).
	MiniMaxModelM3 = "MiniMax-M3"

	// MiniMaxModelM27 is the compact text model (204,800-token context).
	MiniMaxModelM27 = "MiniMax-M2.7"
)

// MiniMaxModelIDs lists the text models this runner is configured for.
var MiniMaxModelIDs = []string{MiniMaxModelM3, MiniMaxModelM27}

// Regional API configuration. ENGRAM_MINIMAX_REGION selects a region and
// ENGRAM_MINIMAX_BASE_URL overrides the resolved base URL entirely.
const (
	// MiniMaxRegionGlobal is the default region key.
	MiniMaxRegionGlobal = "global_en"

	// MiniMaxRegionChina is the alternate region key.
	MiniMaxRegionChina = "cn_zh"

	miniMaxBaseURLGlobal = "https://api.minimax.io/v1"
	miniMaxBaseURLChina  = "https://api.minimaxi.com/v1"

	// miniMaxChatPath is appended to the resolved base URL to reach the
	// chat completions endpoint.
	miniMaxChatPath = "/chat/completions"
)

// miniMaxHTTPClient is the shared client used for outbound requests. It carries
// a conservative backstop timeout; per-call deadlines come from the context.
var miniMaxHTTPClient = &http.Client{Timeout: 60 * time.Second}

// ─── MiniMaxRunner ──────────────────────────────────────────────────────────────

// MiniMaxRunner implements AgentRunner by calling the MiniMax hosted chat
// completions API directly over HTTP. It sends the canonical comparison prompt
// as a single user message and parses the returned Verdict JSON.
type MiniMaxRunner struct {
	// baseURL is the API base (without the chat completions path).
	baseURL string

	// apiKey authenticates the request via a bearer token.
	apiKey string

	// model is the text model identifier sent with each request.
	model string

	// doRequest is the HTTP round-trip seam. Defaults to miniMaxHTTPClient.Do.
	// Tests inject a fake implementation to avoid real network calls.
	doRequest func(*http.Request) (*http.Response, error)
}

// NewMiniMaxRunner constructs a MiniMaxRunner from the environment:
//
//	MINIMAX_API_KEY         bearer token (required at Compare time)
//	ENGRAM_MINIMAX_MODEL    text model id; defaults to MiniMaxDefaultModel
//	ENGRAM_MINIMAX_REGION   "global_en" (default) or "cn_zh"
//	ENGRAM_MINIMAX_BASE_URL optional explicit base URL override
//
// Missing credentials are reported as an error from Compare rather than at
// construction, mirroring the other runners' lazy-failure behavior.
func NewMiniMaxRunner() *MiniMaxRunner {
	return &MiniMaxRunner{
		baseURL:   miniMaxResolveBaseURL(),
		apiKey:    strings.TrimSpace(os.Getenv("MINIMAX_API_KEY")),
		model:     miniMaxResolveModel(),
		doRequest: miniMaxHTTPClient.Do,
	}
}

// miniMaxResolveBaseURL selects the base URL from the environment.
func miniMaxResolveBaseURL() string {
	if v := strings.TrimSpace(os.Getenv("ENGRAM_MINIMAX_BASE_URL")); v != "" {
		return strings.TrimRight(v, "/")
	}
	switch strings.TrimSpace(os.Getenv("ENGRAM_MINIMAX_REGION")) {
	case MiniMaxRegionChina:
		return miniMaxBaseURLChina
	default:
		return miniMaxBaseURLGlobal
	}
}

// miniMaxResolveModel selects the model id from the environment.
func miniMaxResolveModel() string {
	if v := strings.TrimSpace(os.Getenv("ENGRAM_MINIMAX_MODEL")); v != "" {
		return v
	}
	return MiniMaxDefaultModel
}

// Compare sends prompt to the MiniMax chat completions endpoint and returns a
// structured Verdict.
//
// The request body carries the model and a single user message; the response's
// first choice message content is parsed as the Verdict JSON (markdown code
// fences are stripped before parsing).
func (r *MiniMaxRunner) Compare(ctx context.Context, prompt string) (Verdict, error) {
	if r.apiKey == "" {
		return Verdict{}, fmt.Errorf("%w: MINIMAX_API_KEY is not set", ErrCLIAuthMissing)
	}

	payload, err := json.Marshal(miniMaxChatRequest{
		Model:  r.model,
		Stream: false,
		Messages: []miniMaxMessage{
			{Role: "user", Content: prompt},
		},
	})
	if err != nil {
		return Verdict{}, fmt.Errorf("minimax: encode request: %v", err)
	}

	start := time.Now()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, r.baseURL+miniMaxChatPath, bytes.NewReader(payload))
	if err != nil {
		return Verdict{}, fmt.Errorf("minimax: build request: %v", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+r.apiKey)

	do := r.doRequest
	if do == nil {
		do = miniMaxHTTPClient.Do
	}
	resp, err := do(httpReq)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return Verdict{}, fmt.Errorf("%w: minimax request", ErrTimeout)
		}
		return Verdict{}, fmt.Errorf("minimax: request failed: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return Verdict{}, fmt.Errorf("minimax: read response: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		return Verdict{}, fmt.Errorf("minimax: API returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	return parseMiniMaxResponse(raw, time.Since(start).Milliseconds())
}

// ─── Compile-time interface satisfaction ───────────────────────────────────────

var _ AgentRunner = (*MiniMaxRunner)(nil)

// ─── Request / response shapes ──────────────────────────────────────────────────

// miniMaxChatRequest is the chat completions request body.
type miniMaxChatRequest struct {
	Model    string           `json:"model"`
	Stream   bool             `json:"stream"`
	Messages []miniMaxMessage `json:"messages"`
}

// miniMaxMessage is a single chat message.
type miniMaxMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// miniMaxChatResponse is the subset of the chat completions response we read.
type miniMaxChatResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	BaseResp struct {
		StatusCode int    `json:"status_code"`
		StatusMsg  string `json:"status_msg"`
	} `json:"base_resp"`
}

// parseMiniMaxResponse decodes the chat completions response and returns a Verdict.
func parseMiniMaxResponse(raw []byte, durationMS int64) (Verdict, error) {
	var env miniMaxChatResponse
	if err := json.Unmarshal(raw, &env); err != nil {
		return Verdict{}, fmt.Errorf("%w: response envelope: %v", ErrInvalidJSON, err)
	}

	// A non-zero status_code signals an application-level failure even when the
	// HTTP status is 200.
	if env.BaseResp.StatusCode != 0 {
		return Verdict{}, fmt.Errorf("minimax: API error %d: %s", env.BaseResp.StatusCode, env.BaseResp.StatusMsg)
	}

	if len(env.Choices) == 0 {
		return Verdict{}, fmt.Errorf("%w: response contained no choices", ErrInvalidJSON)
	}

	// Strip markdown fences from the message content before JSON parsing.
	content := strings.TrimSpace(env.Choices[0].Message.Content)
	if m := fenceRE.FindStringSubmatch(content); len(m) == 2 {
		content = strings.TrimSpace(m[1])
	}

	var iv innerVerdict
	if err := json.Unmarshal([]byte(content), &iv); err != nil {
		return Verdict{}, fmt.Errorf("%w: inner verdict: %v", ErrInvalidJSON, err)
	}

	if !validRelations[iv.Relation] {
		return Verdict{}, fmt.Errorf("%w: %q", ErrUnknownRelation, iv.Relation)
	}

	// Prefer the model reported by the API, falling back to the inner field.
	model := env.Model
	if model == "" {
		model = iv.Model
	}

	return Verdict{
		Relation:   iv.Relation,
		Confidence: iv.Confidence,
		Reasoning:  iv.Reasoning,
		Model:      model,
		DurationMS: durationMS,
	}, nil
}
