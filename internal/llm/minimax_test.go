package llm

// This test file lives in package llm (not llm_test) so it can inject the
// unexported doRequest seam and exercise the request/response internals
// without performing real network calls.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// ─── helpers ──────────────────────────────────────────────────────────────────

// fakeHTTPResponse builds an *http.Response with the given status and body.
func fakeHTTPResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

// fakeDo returns a doRequest func that always returns the given response/error.
func fakeDo(resp *http.Response, err error) func(*http.Request) (*http.Response, error) {
	return func(*http.Request) (*http.Response, error) {
		return resp, err
	}
}

// chatEnvelope renders a chat completions response body wrapping innerContent.
func chatEnvelope(model, innerContent string) string {
	return fmt.Sprintf(
		`{"model":%q,"choices":[{"message":{"role":"assistant","content":%q}}],"base_resp":{"status_code":0,"status_msg":"success"}}`,
		model, innerContent,
	)
}

func newTestRunner(do func(*http.Request) (*http.Response, error)) *MiniMaxRunner {
	return &MiniMaxRunner{
		baseURL:   miniMaxBaseURLGlobal,
		apiKey:    "test-key",
		model:     MiniMaxDefaultModel,
		doRequest: do,
	}
}

// ─── MiniMaxRunner tests ────────────────────────────────────────────────────────

// TestMiniMaxRunner_CompileTimeCheck verifies MiniMaxRunner satisfies AgentRunner.
var _ AgentRunner = (*MiniMaxRunner)(nil)

func TestMiniMaxRunner_GoldenResponse(t *testing.T) {
	inner := `{"Relation":"supersedes","Confidence":0.91,"Reasoning":"A replaces B"}`
	body := chatEnvelope("MiniMax-M3", inner)

	r := newTestRunner(fakeDo(fakeHTTPResponse(http.StatusOK, body), nil))
	v, err := r.Compare(context.Background(), "compare these two")
	if err != nil {
		t.Fatalf("Compare: unexpected error: %v", err)
	}
	if v.Relation != "supersedes" {
		t.Errorf("Relation = %q; want %q", v.Relation, "supersedes")
	}
	if v.Confidence != 0.91 {
		t.Errorf("Confidence = %v; want 0.91", v.Confidence)
	}
	if v.Reasoning != "A replaces B" {
		t.Errorf("Reasoning = %q; want %q", v.Reasoning, "A replaces B")
	}
	if v.Model != "MiniMax-M3" {
		t.Errorf("Model = %q; want %q", v.Model, "MiniMax-M3")
	}
}

func TestMiniMaxRunner_FenceStripping(t *testing.T) {
	inner := "```json\n{\"Relation\":\"compatible\",\"Confidence\":0.8,\"Reasoning\":\"They agree\"}\n```"
	body := chatEnvelope("MiniMax-M2.7", inner)

	r := newTestRunner(fakeDo(fakeHTTPResponse(http.StatusOK, body), nil))
	v, err := r.Compare(context.Background(), "compare")
	if err != nil {
		t.Fatalf("Compare with fence: unexpected error: %v", err)
	}
	if v.Relation != "compatible" {
		t.Errorf("Relation = %q; want %q", v.Relation, "compatible")
	}
	if v.Model != "MiniMax-M2.7" {
		t.Errorf("Model = %q; want %q", v.Model, "MiniMax-M2.7")
	}
}

func TestMiniMaxRunner_RequestShape(t *testing.T) {
	var captured *http.Request
	var capturedBody []byte
	do := func(req *http.Request) (*http.Response, error) {
		captured = req
		capturedBody, _ = io.ReadAll(req.Body)
		inner := `{"Relation":"related","Confidence":0.6,"Reasoning":"shared topic"}`
		return fakeHTTPResponse(http.StatusOK, chatEnvelope("MiniMax-M3", inner)), nil
	}

	r := newTestRunner(do)
	if _, err := r.Compare(context.Background(), "the prompt body"); err != nil {
		t.Fatalf("Compare: unexpected error: %v", err)
	}

	if captured.Method != http.MethodPost {
		t.Errorf("method = %q; want POST", captured.Method)
	}
	if !strings.HasSuffix(captured.URL.String(), "/chat/completions") {
		t.Errorf("URL = %q; want suffix /chat/completions", captured.URL.String())
	}
	if got := captured.Header.Get("Authorization"); got != "Bearer test-key" {
		t.Errorf("Authorization = %q; want %q", got, "Bearer test-key")
	}
	if got := captured.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q; want application/json", got)
	}

	var reqBody miniMaxChatRequest
	if err := json.Unmarshal(capturedBody, &reqBody); err != nil {
		t.Fatalf("request body not valid JSON: %v", err)
	}
	if reqBody.Model != MiniMaxDefaultModel {
		t.Errorf("request model = %q; want %q", reqBody.Model, MiniMaxDefaultModel)
	}
	if len(reqBody.Messages) != 1 || reqBody.Messages[0].Content != "the prompt body" {
		t.Errorf("request messages = %+v; want single user message with the prompt", reqBody.Messages)
	}
}

func TestMiniMaxRunner_MissingAPIKey(t *testing.T) {
	r := &MiniMaxRunner{baseURL: miniMaxBaseURLGlobal, model: MiniMaxDefaultModel, doRequest: fakeDo(nil, nil)}
	_, err := r.Compare(context.Background(), "compare")
	if !errors.Is(err, ErrCLIAuthMissing) {
		t.Errorf("expected ErrCLIAuthMissing; got %v", err)
	}
}

func TestMiniMaxRunner_BaseRespError(t *testing.T) {
	body := `{"model":"MiniMax-M3","choices":[],"base_resp":{"status_code":1004,"status_msg":"auth failed"}}`
	r := newTestRunner(fakeDo(fakeHTTPResponse(http.StatusOK, body), nil))
	_, err := r.Compare(context.Background(), "compare")
	if err == nil || !strings.Contains(err.Error(), "1004") {
		t.Errorf("expected API error mentioning status_code 1004; got %v", err)
	}
}

func TestMiniMaxRunner_NoChoices(t *testing.T) {
	body := `{"model":"MiniMax-M3","choices":[],"base_resp":{"status_code":0}}`
	r := newTestRunner(fakeDo(fakeHTTPResponse(http.StatusOK, body), nil))
	_, err := r.Compare(context.Background(), "compare")
	if !errors.Is(err, ErrInvalidJSON) {
		t.Errorf("expected ErrInvalidJSON; got %v", err)
	}
}

func TestMiniMaxRunner_InvalidInnerJSON(t *testing.T) {
	body := chatEnvelope("MiniMax-M3", "not valid json")
	r := newTestRunner(fakeDo(fakeHTTPResponse(http.StatusOK, body), nil))
	_, err := r.Compare(context.Background(), "compare")
	if !errors.Is(err, ErrInvalidJSON) {
		t.Errorf("expected ErrInvalidJSON; got %v", err)
	}
}

func TestMiniMaxRunner_UnknownRelation(t *testing.T) {
	inner := `{"Relation":"maybe_conflict","Confidence":0.5,"Reasoning":"dunno"}`
	body := chatEnvelope("MiniMax-M3", inner)
	r := newTestRunner(fakeDo(fakeHTTPResponse(http.StatusOK, body), nil))
	_, err := r.Compare(context.Background(), "compare")
	if !errors.Is(err, ErrUnknownRelation) {
		t.Errorf("expected ErrUnknownRelation; got %v", err)
	}
}

func TestMiniMaxRunner_Non200(t *testing.T) {
	r := newTestRunner(fakeDo(fakeHTTPResponse(http.StatusInternalServerError, "boom"), nil))
	_, err := r.Compare(context.Background(), "compare")
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Errorf("expected error mentioning status 500; got %v", err)
	}
}

func TestMiniMaxRunner_TransportError(t *testing.T) {
	transportErr := errors.New("connection refused")
	r := newTestRunner(fakeDo(nil, transportErr))
	_, err := r.Compare(context.Background(), "compare")
	if !errors.Is(err, transportErr) {
		t.Errorf("expected wrapped transport error; got %v", err)
	}
}

func TestMiniMaxRunner_Timeout(t *testing.T) {
	timeoutErr := fmt.Errorf("Post: %w", context.DeadlineExceeded)
	r := newTestRunner(fakeDo(nil, timeoutErr))
	_, err := r.Compare(context.Background(), "compare")
	if !errors.Is(err, ErrTimeout) {
		t.Errorf("expected ErrTimeout; got %v", err)
	}
}

// ─── configuration resolution ──────────────────────────────────────────────────

func TestMiniMaxResolveBaseURL(t *testing.T) {
	t.Run("default is global", func(t *testing.T) {
		t.Setenv("ENGRAM_MINIMAX_BASE_URL", "")
		t.Setenv("ENGRAM_MINIMAX_REGION", "")
		if got := miniMaxResolveBaseURL(); got != miniMaxBaseURLGlobal {
			t.Errorf("baseURL = %q; want %q", got, miniMaxBaseURLGlobal)
		}
	})
	t.Run("china region", func(t *testing.T) {
		t.Setenv("ENGRAM_MINIMAX_BASE_URL", "")
		t.Setenv("ENGRAM_MINIMAX_REGION", MiniMaxRegionChina)
		if got := miniMaxResolveBaseURL(); got != miniMaxBaseURLChina {
			t.Errorf("baseURL = %q; want %q", got, miniMaxBaseURLChina)
		}
	})
	t.Run("explicit override wins", func(t *testing.T) {
		t.Setenv("ENGRAM_MINIMAX_REGION", MiniMaxRegionChina)
		t.Setenv("ENGRAM_MINIMAX_BASE_URL", "https://example.test/api/")
		if got := miniMaxResolveBaseURL(); got != "https://example.test/api" {
			t.Errorf("baseURL = %q; want trimmed override", got)
		}
	})
}

func TestMiniMaxResolveModel(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		t.Setenv("ENGRAM_MINIMAX_MODEL", "")
		if got := miniMaxResolveModel(); got != MiniMaxDefaultModel {
			t.Errorf("model = %q; want %q", got, MiniMaxDefaultModel)
		}
	})
	t.Run("override", func(t *testing.T) {
		t.Setenv("ENGRAM_MINIMAX_MODEL", MiniMaxModelM27)
		if got := miniMaxResolveModel(); got != MiniMaxModelM27 {
			t.Errorf("model = %q; want %q", got, MiniMaxModelM27)
		}
	})
}
