package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/embedding"
)

type semanticFake struct {
	id           embedding.Identity
	calls        [][]string
	resolveCalls int
	onEmbed      func()
	onResolve    func(int)
	err          error
	vectors      [][]float64
}

func (p *semanticFake) Resolve(ctx context.Context) (embedding.Identity, error) {
	p.resolveCalls++
	if p.onResolve != nil {
		p.onResolve(p.resolveCalls)
	}
	if err := ctx.Err(); err != nil {
		return p.id, err
	}
	return p.id, p.err
}
func (p *semanticFake) Embed(ctx context.Context, id embedding.Identity, texts []string) ([][]float64, error) {
	p.calls = append(p.calls, append([]string(nil), texts...))
	if p.onEmbed != nil {
		p.onEmbed()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.err != nil {
		return nil, p.err
	}
	if p.vectors != nil {
		return p.vectors, nil
	}
	out := make([][]float64, len(texts))
	for i, text := range texts {
		switch {
		case strings.HasPrefix(text, "negative"):
			out[i] = []float64{-1, 0}
		case strings.HasPrefix(text, "zero"):
			out[i] = []float64{0, 1}
		default:
			out[i] = []float64{1, 0}
		}
	}
	return out, nil
}
func syntheticTempDir(t *testing.T) string { t.Helper(); return t.TempDir() }
func semanticStore(t *testing.T, p SemanticProvider) *Store {
	t.Helper()
	cfg := FallbackConfig(syntheticTempDir(t))
	cfg.EmbeddingProvider = p
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.CreateSession("semantic-test", "engram", ""); err != nil {
		t.Fatal(err)
	}
	return s
}
func semanticProviderFake() *semanticFake {
	return &semanticFake{id: embedding.Identity{Model: "test:latest", Digest: "sha256:test"}}
}
func semanticAdd(t *testing.T, s *Store, title, project, scope, kind string) int64 {
	t.Helper()
	id, err := s.AddObservation(AddObservationParams{SessionID: "semantic-test", Title: title, Content: strings.Repeat("界", 310) + " " + title, Project: project, Scope: scope, Type: kind})
	if err != nil {
		t.Fatal(err)
	}
	return id
}
func semanticGet(t *testing.T, s *Store, id int64) *Observation {
	t.Helper()
	o, err := s.GetObservation(id)
	if err != nil {
		t.Fatal(err)
	}
	return o
}
func semanticCacheCount(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM observations WHERE embedding IS NOT NULL`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func semanticMutationCount(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM sync_mutations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSemanticRoundtripExplicitOnly(t *testing.T) {
	p := semanticProviderFake()
	s := semanticStore(t, p)
	neg := semanticAdd(t, s, "negative", "engram", "project", "note")
	zero := semanticAdd(t, s, "zero", "engram", "project", "note")
	pos := semanticAdd(t, s, "positive", "engram", "project", "note")
	before := semanticGet(t, s, pos)
	mutations := semanticMutationCount(t, s)
	lexical, err := s.Search("positive", SearchOptions{})
	if err != nil || len(lexical) != 1 {
		t.Fatalf("lexical: %v %v", lexical, err)
	}
	if len(p.calls) != 0 || p.resolveCalls != 0 || semanticCacheCount(t, s) != 0 {
		t.Fatal("implicit provider/index work")
	}
	if _, err := s.Search("question", SearchOptions{SearchMode: "semantic"}); !errors.Is(err, ErrSemanticIndex) {
		t.Fatalf("missing: %v", err)
	}
	if len(p.calls) != 0 {
		t.Fatal("missing coverage embedded query")
	}
	count, err := s.BuildSemanticIndex(context.Background(), SearchOptions{Limit: 1})
	if err != nil || count.Indexed != 3 {
		t.Fatalf("index: %+v %v", count, err)
	}
	if !reflect.DeepEqual(before, semanticGet(t, s, pos)) || semanticMutationCount(t, s) != mutations {
		t.Fatal("cache write mutated source or sync")
	}
	results, err := s.Search("unrelated question", SearchOptions{SearchMode: "semantic"})
	if err != nil {
		t.Fatal(err)
	}
	want := []int64{pos, zero, neg}
	scores := []float64{1, 0, -1}
	for i, r := range results {
		if r.ID != want[i] || r.Similarity == nil || *r.Similarity != scores[i] || r.RetrievalMode != "semantic" {
			t.Fatalf("rank[%d]=%+v", i, r)
		}
	}
	if len(results) != 3 {
		t.Fatalf("coverage %d", len(results))
	}
	limited, err := s.Search("question", SearchOptions{SearchMode: "semantic", Limit: 1})
	if err != nil || len(limited) != 1 || limited[0].ID != pos {
		t.Fatalf("limit before rank: %v %v", limited, err)
	}
	previews, err := s.SearchPreviewsContext(context.Background(), "question", SearchOptions{SearchMode: "semantic"})
	if err != nil {
		t.Fatal(err)
	}
	if previews[0].Preview != strings.Repeat("界", 300) || !previews[0].Truncated {
		t.Fatalf("Unicode preview: %+v", previews[0])
	}
	projected := CompactSearchProjection(previews, nil)
	if projected[1]["similarity"] != float64(0) || projected[1]["retrieval_mode"] != "semantic" {
		t.Fatalf("zero score lost: %v", projected)
	}
	b, _ := json.Marshal(previews[1])
	if !strings.Contains(string(b), `"similarity":0`) {
		t.Fatalf("score JSON: %s", b)
	}
	after, err := s.Search("positive", SearchOptions{})
	if err != nil || !reflect.DeepEqual(lexical, after) {
		t.Fatalf("lexical changed: %v %v", after, err)
	}
	b, _ = json.Marshal(after)
	if strings.Contains(string(b), "similarity") || strings.Contains(string(b), "retrieval_mode") {
		t.Fatalf("lexical JSON changed %s", b)
	}
	if len(p.calls[len(p.calls)-1]) != 1 || p.calls[len(p.calls)-1][0] != "question" {
		t.Fatalf("search embedded sources: %v", p.calls)
	}
}

func TestSemanticFiltersAndTies(t *testing.T) {
	p := semanticProviderFake()
	s := semanticStore(t, p)
	a := semanticAdd(t, s, "one", "engram", "project", "note")
	b := semanticAdd(t, s, "two", "engram", "project", "note")
	semanticAdd(t, s, "other project", "other", "project", "note")
	semanticAdd(t, s, "personal", "engram", "personal", "note")
	semanticAdd(t, s, "global", "engram", "global", "note")
	semanticAdd(t, s, "decision", "engram", "project", "decision")
	dead := semanticAdd(t, s, "deleted", "engram", "project", "note")
	if err := s.DeleteObservation(dead, false); err != nil {
		t.Fatal(err)
	}
	opts := SearchOptions{SearchMode: "semantic", Project: "EnGrAm", Scope: " PROJECT ", Type: "note", Limit: 1}
	count, err := s.BuildSemanticIndex(context.Background(), opts)
	if err != nil || count.Indexed != 2 {
		t.Fatalf("filtered indexing: %+v %v", count, err)
	}
	if len(p.calls) != 1 || len(p.calls[0]) != 2 {
		t.Fatalf("provider leakage %v", p.calls)
	}
	opts.Limit = 20
	got, err := s.Search("question", opts)
	if err != nil || len(got) != 2 || got[0].ID != a || got[1].ID != b {
		t.Fatalf("ties/filters: %v %v", got, err)
	}
	opts.Project = "other"
	if _, err := s.Search("question", opts); !errors.Is(err, ErrSemanticIndex) {
		t.Fatalf("other project: %v", err)
	}
	for _, scope := range []string{"personal", "global"} {
		opts.Project = "engram"
		opts.Scope = scope
		n, err := s.BuildSemanticIndex(context.Background(), opts)
		if err != nil || n.Indexed != 1 {
			t.Fatalf("scope %s: %+v %v", scope, n, err)
		}
		got, err := s.Search("question", opts)
		if err != nil || len(got) != 1 || got[0].Scope != scope {
			t.Fatalf("scope results: %v %v", got, err)
		}
	}
}

func TestSemanticInputAndProviderFailures(t *testing.T) {
	s := semanticStore(t, nil)
	id := semanticAdd(t, s, "source", "engram", "project", "note")
	before := semanticGet(t, s, id)
	for _, opts := range []SearchOptions{{SearchMode: "semantic"}, {SearchMode: "semantic", MatchMode: "all"}, {SearchMode: "nonsense"}} {
		_, err := s.Search("question", opts)
		if err == nil {
			t.Fatal("expected error")
		}
		_, err = s.BuildSemanticIndex(context.Background(), opts)
		if err == nil {
			t.Fatal("expected index error")
		}
	}
	if _, err := s.Search("question", SearchOptions{SearchMode: "semantic"}); !errors.Is(err, ErrSemanticUnconfigured) {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, semanticGet(t, s, id)) || semanticCacheCount(t, s) != 0 {
		t.Fatal("rejected input mutated source/cache")
	}
	p := semanticProviderFake()
	s2 := semanticStore(t, p)
	id2 := semanticAdd(t, s2, "source", "engram", "project", "note")
	before2 := semanticGet(t, s2, id2)
	failure := errors.New("synthetic provider unavailable")
	p.err = failure
	if _, err := s2.BuildSemanticIndex(context.Background(), SearchOptions{}); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s2.BuildSemanticIndex(ctx, SearchOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := s2.SearchContext(ctx, "question", SearchOptions{SearchMode: "semantic"}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before2, semanticGet(t, s2, id2)) || semanticCacheCount(t, s2) != 0 {
		t.Fatal("provider failure mutated source/cache")
	}
}

func TestSemanticInvalidVectorsAndCaches(t *testing.T) {
	for _, v := range [][][]float64{{}, {{0, 0}}, {{math.NaN(), 1}}, {{math.Inf(1), 0}}, {{1, 0}, {1, 0}}} {
		t.Run("vector", func(t *testing.T) {
			p := semanticProviderFake()
			p.vectors = v
			s := semanticStore(t, p)
			id := semanticAdd(t, s, "source", "engram", "project", "note")
			before := semanticGet(t, s, id)
			if _, err := s.BuildSemanticIndex(context.Background(), SearchOptions{}); !errors.Is(err, ErrSemanticVector) {
				t.Fatalf("%v", err)
			}
			if semanticCacheCount(t, s) != 0 || !reflect.DeepEqual(before, semanticGet(t, s, id)) {
				t.Fatal("invalid vector persisted")
			}
		})
	}
	p := semanticProviderFake()
	s := semanticStore(t, p)
	id := semanticAdd(t, s, "source", "engram", "project", "note")
	for _, corrupt := range []string{`{`, `{"version":2}`, `{"version":1,"dimensions":768}`} {
		if _, err := s.db.Exec(`UPDATE observations SET embedding=?,embedding_model='test:latest',embedding_created_at='now' WHERE id=?`, []byte(corrupt), id); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Search("question", SearchOptions{SearchMode: "semantic"}); !errors.Is(err, ErrSemanticIndex) {
			t.Fatalf("cache %s: %v", corrupt, err)
		}
	}
	if _, err := s.BuildSemanticIndex(context.Background(), SearchOptions{}); err != nil {
		t.Fatal(err)
	}
	p.id.Digest = "sha256:replacement"
	if _, err := s.Search("question", SearchOptions{SearchMode: "semantic"}); !errors.Is(err, ErrSemanticIndex) {
		t.Fatalf("model mismatch: %v", err)
	}
	if _, err := s.BuildSemanticIndex(context.Background(), SearchOptions{}); err != nil {
		t.Fatal(err)
	}
	content := "changed source"
	if _, err := s.UpdateObservation(id, UpdateObservationParams{Content: &content}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Search("question", SearchOptions{SearchMode: "semantic"}); !errors.Is(err, ErrSemanticIndex) {
		t.Fatalf("stale: %v", err)
	}
	if semanticGet(t, s, id).Content != content {
		t.Fatal("lost edit")
	}
}

func TestSemanticConcurrentSnapshots(t *testing.T) {
	for _, action := range []string{"content", "metadata", "delete", "recreate", "new", "model", "cache"} {
		for _, search := range []bool{false, true} {
			t.Run(action+map[bool]string{false: "/build", true: "/search"}[search], func(t *testing.T) {
				p := semanticProviderFake()
				s := semanticStore(t, p)
				s.db.SetMaxOpenConns(1)
				id := semanticAdd(t, s, "source", "engram", "project", "note")
				if search {
					if _, err := s.BuildSemanticIndex(context.Background(), SearchOptions{}); err != nil {
						t.Fatal(err)
					}
				}
				changed := false
				p.onEmbed = func() {
					if changed {
						return
					}
					changed = true
					switch action {
					case "content":
						v := "externally changed"
						if _, err := s.UpdateObservation(id, UpdateObservationParams{Content: &v}); err != nil {
							t.Fatal(err)
						}
					case "metadata":
						v := "personal"
						if _, err := s.UpdateObservation(id, UpdateObservationParams{Scope: &v}); err != nil {
							t.Fatal(err)
						}
					case "delete":
						if err := s.DeleteObservation(id, false); err != nil {
							t.Fatal(err)
						}
					case "recreate":
						if err := s.DeleteObservation(id, true); err != nil {
							t.Fatal(err)
						}
						semanticAdd(t, s, "source", "engram", "project", "note")
					case "new":
						semanticAdd(t, s, "new source", "engram", "project", "note")
					case "model":
						p.id.Digest = "sha256:changed"
					case "cache":
						p.onEmbed = nil
						if _, err := s.BuildSemanticIndex(context.Background(), SearchOptions{}); err != nil {
							t.Fatal(err)
						}
					}
				}
				var err error
				if search {
					_, err = s.Search("question", SearchOptions{SearchMode: "semantic"})
				} else {
					_, err = s.BuildSemanticIndex(context.Background(), SearchOptions{})
				}
				if !errors.Is(err, ErrSemanticChanged) {
					t.Fatalf("expected drift: %v", err)
				}
				switch action {
				case "content":
					if semanticGet(t, s, id).Content != "externally changed" {
						t.Fatal("lost external content")
					}
				case "metadata":
					if semanticGet(t, s, id).Scope != "personal" {
						t.Fatal("lost external metadata")
					}
				case "new":
					rows, err := s.Search("new source", SearchOptions{})
					if err != nil || len(rows) == 0 {
						t.Fatalf("lost new row %v %v", rows, err)
					}
				}
				if !search && action != "cache" && semanticCacheCount(t, s) != 0 {
					t.Fatal("rejected build persisted cache")
				}
			})
		}
	}
}

func TestSemanticCancellationDuringEmbedding(t *testing.T) {
	for _, search := range []bool{false, true} {
		p := semanticProviderFake()
		s := semanticStore(t, p)
		id := semanticAdd(t, s, "source", "engram", "project", "note")
		if search {
			if _, err := s.BuildSemanticIndex(context.Background(), SearchOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		before := semanticGet(t, s, id)
		mutations := semanticMutationCount(t, s)
		cache := semanticCacheCount(t, s)
		ctx, cancel := context.WithCancel(context.Background())
		p.onEmbed = cancel
		var err error
		if search {
			_, err = s.SearchContext(ctx, "question", SearchOptions{SearchMode: "semantic"})
		} else {
			_, err = s.BuildSemanticIndex(ctx, SearchOptions{})
		}
		cancel()
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation: %v", err)
		}
		if !reflect.DeepEqual(before, semanticGet(t, s, id)) || semanticMutationCount(t, s) != mutations || semanticCacheCount(t, s) != cache {
			t.Fatal("cancellation changed source/cache/sync")
		}
	}
}

func TestSemanticMixedCancellationClassification(t *testing.T) {
	p := semanticProviderFake()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	failure := errors.New("synthetic transport failure")
	p.onEmbed = func() { cancel(); p.err = failure }
	// This fake deliberately returns a transport error alongside cancellation.
	mixed := &semanticMixedFailure{semanticFake: p, failure: failure}
	s := semanticStore(t, mixed)
	id := semanticAdd(t, s, "source", "engram", "project", "note")
	before := semanticGet(t, s, id)
	_, err := s.BuildSemanticIndex(ctx, SearchOptions{})
	if !errors.Is(err, context.Canceled) || !errors.Is(err, failure) {
		t.Fatalf("both classifications required: %v", err)
	}
	if semanticCacheCount(t, s) != 0 || !reflect.DeepEqual(before, semanticGet(t, s, id)) {
		t.Fatal("mixed failure mutated data")
	}
}

type semanticMixedFailure struct {
	*semanticFake
	failure error
}

func (p *semanticMixedFailure) Embed(ctx context.Context, id embedding.Identity, texts []string) ([][]float64, error) {
	p.onEmbed()
	return nil, p.failure
}

func TestSemanticBatchCoverageAndPersistence(t *testing.T) {
	p := semanticProviderFake()
	s := semanticStore(t, p)
	var best int64
	for i := 0; i < 35; i++ {
		title := fmt.Sprintf("zero source %02d", i)
		if i == 34 {
			title = "positive last candidate"
		}
		best = semanticAdd(t, s, title, "engram", "project", "note")
	}
	n, err := s.BuildSemanticIndex(context.Background(), SearchOptions{Limit: 1})
	if err != nil || n.Indexed != 35 {
		t.Fatalf("batch index: %+v %v", n, err)
	}
	if len(p.calls) != 2 || len(p.calls[0]) != 32 || len(p.calls[1]) != 3 {
		t.Fatalf("batches: %v", p.calls)
	}
	for _, limit := range []int{0, 1, 100} {
		rows, err := s.Search("question", SearchOptions{SearchMode: "semantic", Limit: limit})
		want := 10
		if limit == 1 {
			want = 1
		}
		if limit == 100 {
			want = 20
		}
		if err != nil || len(rows) != want || rows[0].ID != best {
			t.Fatalf("limit %d: %d %v (best must be last source %d)", limit, len(rows), err, best)
		}
	}
	dir := s.DataDir()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	cfg := FallbackConfig(dir)
	cfg.EmbeddingProvider = p
	reopened, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	rows, err := reopened.Search("question", SearchOptions{SearchMode: "semantic", Limit: 100})
	if err != nil || len(rows) != 20 {
		t.Fatalf("persisted cache: %d %v", len(rows), err)
	}
}

func TestSemanticCacheCorruptionAndAtomicFailure(t *testing.T) {
	p := semanticProviderFake()
	s := semanticStore(t, p)
	id := semanticAdd(t, s, "source", "engram", "project", "note")
	if _, err := s.BuildSemanticIndex(context.Background(), SearchOptions{}); err != nil {
		t.Fatal(err)
	}
	before := semanticGet(t, s, id)
	mutations := semanticMutationCount(t, s)
	var original []byte
	if err := s.db.QueryRow(`SELECT embedding FROM observations WHERE id=?`, id).Scan(&original); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"version", "source", "identity", "dimensions", "zero", "nonfinite", "model"} {
		var e semanticEnvelope
		if err := json.Unmarshal(original, &e); err != nil {
			t.Fatal(err)
		}
		model := p.id.Model
		switch kind {
		case "version":
			e.Version = 9
		case "source":
			e.Source = "wrong"
		case "identity":
			e.Identity.Digest = "wrong"
		case "dimensions":
			e.Dimensions = 3
		case "zero":
			e.Vector = []float64{0, 0}
		case "model":
			model = "wrong"
		}
		b, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		if kind == "nonfinite" {
			b = []byte(strings.Replace(string(b), `"vector":[1,0]`, `"vector":[1e999,0]`, 1))
		}
		if _, err := s.db.Exec(`UPDATE observations SET embedding=?,embedding_model=? WHERE id=?`, b, model, id); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Search("question", SearchOptions{SearchMode: "semantic"}); !errors.Is(err, ErrSemanticIndex) {
			t.Fatalf("%s: %v", kind, err)
		}
	}
	if _, err := s.BuildSemanticIndex(context.Background(), SearchOptions{}); err != nil {
		t.Fatal(err)
	}
	p.vectors = [][]float64{{0, 0}}
	if _, err := s.BuildSemanticIndex(context.Background(), SearchOptions{}); !errors.Is(err, ErrSemanticVector) {
		t.Fatal(err)
	}
	p.vectors = nil
	if _, err := s.Search("question", SearchOptions{SearchMode: "semantic"}); err != nil {
		t.Fatalf("failed rebuild damaged previous cache: %v", err)
	}
	if !reflect.DeepEqual(before, semanticGet(t, s, id)) || semanticMutationCount(t, s) != mutations {
		t.Fatal("failure altered source/sync")
	}
}

func TestSemanticModelChangeAtResolveAndQueryFailure(t *testing.T) {
	p := semanticProviderFake()
	s := semanticStore(t, p)
	id := semanticAdd(t, s, "source", "engram", "project", "note")
	p.onResolve = func(n int) {
		if n == 2 {
			p.id.Digest = "replacement"
		}
	}
	if _, err := s.BuildSemanticIndex(context.Background(), SearchOptions{}); !errors.Is(err, ErrSemanticChanged) {
		t.Fatal(err)
	}
	if semanticCacheCount(t, s) != 0 {
		t.Fatal("model flip persisted")
	}
	p.onResolve = nil
	if _, err := s.BuildSemanticIndex(context.Background(), SearchOptions{}); err != nil {
		t.Fatal(err)
	}
	before := semanticGet(t, s, id)
	failure := errors.New("provider disconnected")
	p.onEmbed = func() { p.err = failure }
	if _, err := s.Search("question", SearchOptions{SearchMode: "semantic"}); !errors.Is(err, failure) {
		t.Fatalf("query failure: %v", err)
	}
	if !reflect.DeepEqual(before, semanticGet(t, s, id)) {
		t.Fatal("query failure altered source")
	}
}

func TestSemanticPreviewRelationsAndMetadata(t *testing.T) {
	p := semanticProviderFake()
	s := semanticStore(t, p)
	a := semanticAdd(t, s, "relation source", "engram", "project", "decision")
	b := semanticAdd(t, s, "relation target", "engram", "project", "note")
	topic := "decision/semantic"
	if _, err := s.UpdateObservation(a, UpdateObservationParams{TopicKey: &topic}); err != nil {
		t.Fatal(err)
	}
	oa, ob := semanticGet(t, s, a), semanticGet(t, s, b)
	if _, err := s.SaveRelation(SaveRelationParams{SyncID: "rel-semantic-test", SourceID: oa.SyncID, TargetID: ob.SyncID}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BuildSemanticIndex(context.Background(), SearchOptions{}); err != nil {
		t.Fatal(err)
	}
	previews, err := s.SearchPreviewsContext(context.Background(), "question", SearchOptions{SearchMode: "semantic"})
	if err != nil {
		t.Fatal(err)
	}
	relations, err := s.SearchPreviewRelationsContext(context.Background(), previews)
	if err != nil {
		t.Fatal(err)
	}
	projection := CompactSearchProjection(previews, relations)
	if projection[0]["topic_key"] != topic || projection[0]["review_after"] == nil || projection[0]["relations"] == nil || projection[0]["similarity"] != float64(1) {
		t.Fatalf("metadata: %#v", projection)
	}
}

func TestSemanticLargeVectorsAndEmptyCorpus(t *testing.T) {
	p := semanticProviderFake()
	s := semanticStore(t, p)
	rows, err := s.Search("question", SearchOptions{SearchMode: "semantic"})
	if err != nil || len(rows) != 0 || len(p.calls) != 0 {
		t.Fatalf("empty: %v %v", rows, err)
	}
	semanticAdd(t, s, "large", "engram", "project", "note")
	p.vectors = [][]float64{{math.MaxFloat64, math.MaxFloat64}}
	if _, err := s.BuildSemanticIndex(context.Background(), SearchOptions{}); err != nil {
		t.Fatal(err)
	}
	rows, err = s.Search("question", SearchOptions{SearchMode: "semantic"})
	if err != nil || len(rows) != 1 || math.Abs(*rows[0].Similarity-1) > 1e-15 {
		t.Fatalf("large: %v %v", rows, err)
	}
	p.vectors = [][]float64{{1, 0, 0}}
	if _, err := s.Search("question", SearchOptions{SearchMode: "semantic"}); !errors.Is(err, ErrSemanticVector) {
		t.Fatalf("dimension: %v", err)
	}
}
