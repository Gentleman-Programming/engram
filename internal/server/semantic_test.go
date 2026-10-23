package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/embedding"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

type semanticHTTPProvider struct {
	err   error
	texts [][]string
}

func (p *semanticHTTPProvider) Resolve(ctx context.Context) (embedding.Identity, error) {
	if err := ctx.Err(); err != nil {
		return embedding.Identity{}, err
	}
	return embedding.Identity{Model: "local:latest", Digest: strings.Repeat("a", 64)}, p.err
}
func (p *semanticHTTPProvider) Embed(ctx context.Context, id embedding.Identity, texts []string) ([][]float64, error) {
	p.texts = append(p.texts, append([]string(nil), texts...))
	vectors := make([][]float64, len(texts))
	for i := range vectors {
		vectors[i] = []float64{1, 0}
	}
	return vectors, nil
}

func TestSemanticHTTPRoundTripAndFailures(t *testing.T) {
	p := &semanticHTTPProvider{}
	cfg, err := store.DefaultConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.DataDir = t.TempDir()
	cfg.EmbeddingProvider = p
	st, err := store.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if err = st.CreateSession("semantic", "alpha", "/synthetic"); err != nil {
		t.Fatal(err)
	}
	id, err := st.AddObservation(store.AddObservationParams{SessionID: "semantic", Title: "source", Content: "alpha content", Type: "decision", Project: "alpha", Scope: "project"})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(New(st, 0).Handler())
	defer ts.Close()
	get := func(path string) (int, string) {
		t.Helper()
		r, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = r.Body.Close() }()
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		return r.StatusCode, string(b)
	}
	path := "/search?q=unrelated&project=alpha&scope=project&type=decision&search_mode=semantic"
	code, body := get(path)
	if code != 409 || !strings.Contains(body, "engram embed") || len(p.texts) != 0 {
		t.Fatalf("missing %d %s calls=%v", code, body, p.texts)
	}
	if result, err := st.BuildSemanticIndex(context.Background(), store.SearchOptions{Project: "alpha", Scope: "project", Type: "decision"}); err != nil || result.Indexed != 1 {
		t.Fatalf("build %v %v", result, err)
	}
	for _, suffix := range []string{"", "&response_format=compact"} {
		code, body = get(path + suffix)
		if code != 200 || !strings.Contains(body, `"similarity":1`) || !strings.Contains(body, `"retrieval_mode":"semantic"`) {
			t.Fatalf("success %d %s", code, body)
		}
		if suffix != "" && !strings.Contains(body, "potentially related") {
			t.Fatal(body)
		}
	}
	if len(p.texts) != 3 || !reflect.DeepEqual(p.texts[1], []string{"unrelated"}) || !reflect.DeepEqual(p.texts[2], []string{"unrelated"}) {
		t.Fatalf("query reembedded sources: %v", p.texts)
	}
	before, err := st.GetObservation(id)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		err    error
		status int
	}{
		{embedding.ErrUnavailable, 503}, {embedding.ErrConfig, 503}, {embedding.ErrModel, 503}, {embedding.ErrRemoteModel, 503},
		{embedding.ErrModelChanged, 409}, {embedding.ErrResponse, 502}, {embedding.ErrVectors, 502}, {store.ErrSemanticVector, 502},
		{context.Canceled, 408}, {context.DeadlineExceeded, 504}, {embedding.ErrInput, 400},
		{errors.Join(store.ErrSemanticIndex, store.ErrSemanticVector), 409},
	} {
		p.err = fmt.Errorf("synthetic failure: %w", tc.err)
		code, body = get(path)
		if code != tc.status || !strings.Contains(body, "engram embed") || strings.Contains(body, `"results"`) {
			t.Errorf("error %v: %d %s", tc.err, code, body)
		}
	}
	p.err = nil
	// Inject cancellation into real incoming requests to verify the full semantic
	// route forwards r.Context(), while the legacy full route remains unchanged.
	for _, deadline := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		want := 408
		if deadline {
			ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			defer cancel()
			want = 504
		}
		h := New(st, 0).Handler()
		canceledServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.ServeHTTP(w, r.WithContext(ctx)) }))
		r, err := http.Get(canceledServer.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		canceledServer.Close()
		if r.StatusCode != want || !strings.Contains(string(b), "engram embed") {
			t.Fatalf("context: %d %s", r.StatusCode, b)
		}
	}
	after, err := st.GetObservation(id)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("query changed source: %v", err)
	}
	if len(p.texts) != 3 {
		t.Fatalf("failed operations embedded documents: %v", p.texts)
	}
	// Lexical routes retain their shape and never resolve the provider.
	p.err = embedding.ErrUnavailable
	for _, mode := range []string{"", "&search_mode=lexical"} {
		code, body = get("/search?q=alpha&all_projects=true" + mode)
		if code != 200 || strings.Contains(body, "similarity") || !strings.HasPrefix(body, "[") {
			t.Errorf("lexical: %d %s", code, body)
		}
	}
	p.err = nil
	content := "changed source"
	if _, err := st.UpdateObservation(id, store.UpdateObservationParams{Content: &content}); err != nil {
		t.Fatal(err)
	}
	code, body = get(path)
	if code != 409 || !strings.Contains(body, "engram embed") || len(p.texts) != 3 {
		t.Fatalf("stale %d %s", code, body)
	}
}

func TestSemanticHTTPValidationAndCapability(t *testing.T) {
	s := newServerTestStore(t)
	ts := httptest.NewServer(New(s, 0).Handler())
	defer ts.Close()
	for _, tc := range []struct {
		path     string
		status   int
		contains string
	}{
		{"/search?q=hello&all_projects=true&search_mode=wrong", 400, "search_mode"},
		{"/search?q=+++&all_projects=true&search_mode=semantic", 400, "q parameter"},
		{"/search?q=hello&all_projects=true&search_mode=semantic&match_mode=all", 400, "match_mode"},
		{"/search?q=hello&all_projects=true&search_mode=semantic", 503, "ENGRAM_EMBEDDING_MODEL"},
	} {
		r, err := http.Get(ts.URL + tc.path)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		if r.StatusCode != tc.status || !strings.Contains(string(b), tc.contains) {
			t.Errorf("%s: %d %s", tc.path, r.StatusCode, b)
		}
	}
	r, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Body.Close() }()
	var body struct {
		Capabilities map[string]bool `json:"capabilities"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !body.Capabilities["semantic_search"] {
		t.Fatal("semantic capability must advertise protocol support without a provider")
	}
}
