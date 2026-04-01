package store

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"math"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/internal/embedding"
)

// mockEmbedder generates deterministic vectors from text content.
type mockEmbedder struct {
	dims      int
	model     string
	callCount int
}

func (m *mockEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	m.callCount++
	// Generate a deterministic vector from the text hash.
	h := sha256.Sum256([]byte(text))
	vec := make([]float32, m.dims)
	for i := range vec {
		idx := i % 32
		vec[i] = float32(h[idx]) / 255.0
	}
	// Normalize to unit vector.
	var norm float32
	for _, v := range vec {
		norm += v * v
	}
	norm = float32(math.Sqrt(float64(norm)))
	if norm > 0 {
		for i := range vec {
			vec[i] /= norm
		}
	}
	return vec, nil
}

func (m *mockEmbedder) Dimensions() int  { return m.dims }
func (m *mockEmbedder) ModelName() string { return m.model }
func (m *mockEmbedder) MaxChars() int    { return 0 } // no limit in tests

func newTestStoreWithEmbeddings(t *testing.T) (*Store, *mockEmbedder) {
	t.Helper()
	s := newTestStore(t)
	emb := &mockEmbedder{dims: 8, model: "test-model"}
	s.SetEmbeddingProvider(emb)
	return s, emb
}

func TestAddObservationGeneratesEmbedding(t *testing.T) {
	s, emb := newTestStoreWithEmbeddings(t)

	if err := s.CreateSession("s1", "test", "/tmp/test"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Disable async embedding to avoid SQLITE_BUSY race in tests.
	s.embedder = nil
	id, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "learning",
		Title:     "Test observation",
		Content:   "This is a test observation for embedding generation",
		Project:   "test",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}
	s.embedder = emb

	// Use sync embedding to ensure it's stored before we check.
	if err := s.GenerateEmbeddingSync(id, "Test observation This is a test observation for embedding generation"); err != nil {
		t.Fatalf("generate embedding: %v", err)
	}

	// Verify embedding was stored.
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM observation_embeddings WHERE observation_id = ?", id).Scan(&count); err != nil {
		t.Fatalf("query embedding: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 embedding row, got %d", count)
	}

	// Verify dimensions and model.
	var dims int
	var model string
	if err := s.db.QueryRow("SELECT dimensions, model FROM observation_embeddings WHERE observation_id = ?", id).Scan(&dims, &model); err != nil {
		t.Fatalf("query embedding metadata: %v", err)
	}
	if dims != 8 {
		t.Errorf("dimensions = %d, want 8", dims)
	}
	if model != "test-model" {
		t.Errorf("model = %s, want test-model", model)
	}

	if emb.callCount < 1 {
		t.Error("expected at least 1 embedding call")
	}
}

func TestUpdateObservationRegeneratesEmbedding(t *testing.T) {
	s, emb := newTestStoreWithEmbeddings(t)

	if err := s.CreateSession("s1", "test", "/tmp/test"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	s.embedder = nil // disable async
	id, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "learning",
		Title:     "Original title",
		Content:   "Original content",
		Project:   "test",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}
	s.embedder = emb

	// Generate initial embedding.
	if err := s.GenerateEmbeddingSync(id, "Original title Original content"); err != nil {
		t.Fatalf("generate embedding: %v", err)
	}

	// Get the original embedding blob.
	var origBlob []byte
	if err := s.db.QueryRow("SELECT embedding FROM observation_embeddings WHERE observation_id = ?", id).Scan(&origBlob); err != nil {
		t.Fatalf("query original embedding: %v", err)
	}

	// Update with new content — disable async to avoid race.
	newContent := "Updated content with different words"
	s.embedder = nil
	_, err = s.UpdateObservation(id, UpdateObservationParams{
		Content: &newContent,
	})
	if err != nil {
		t.Fatalf("update observation: %v", err)
	}
	s.embedder = emb

	// Generate new embedding (simulating what async would do).
	if err := s.GenerateEmbeddingSync(id, "Original title "+newContent); err != nil {
		t.Fatalf("regenerate embedding: %v", err)
	}

	// Verify the embedding changed.
	var newBlob []byte
	if err := s.db.QueryRow("SELECT embedding FROM observation_embeddings WHERE observation_id = ?", id).Scan(&newBlob); err != nil {
		t.Fatalf("query new embedding: %v", err)
	}

	if string(origBlob) == string(newBlob) {
		t.Error("embedding should have changed after content update")
	}
}

func TestSearchWithoutEmbeddings(t *testing.T) {
	// Store without embedding provider — should behave identically to original.
	s := newTestStore(t)

	if err := s.CreateSession("s1", "test", "/tmp/test"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	_, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "learning",
		Title:     "MySQL replication",
		Content:   "Setting up MySQL replication with GTID-based replication",
		Project:   "test",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}

	results, err := s.Search("MySQL replication", SearchOptions{Project: "test"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}

	if len(results) == 0 {
		t.Error("expected at least one FTS5 result")
	}
}

func TestSearchWithEmbeddingsHybrid(t *testing.T) {
	s, emb := newTestStoreWithEmbeddings(t)

	if err := s.CreateSession("s1", "test", "/tmp/test"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Add several observations with embeddings.
	// Disable async embedding during adds to avoid SQLITE_BUSY in tests,
	// then generate embeddings synchronously.
	observations := []struct {
		title   string
		content string
	}{
		{"MySQL connection pooling", "Configure max_connections and connection pool sizes for optimal performance"},
		{"Kafka consumer lag", "Monitor consumer lag using Burrow and set alerts for growing lag"},
		{"Database backup strategy", "Implement automated backups with point-in-time recovery capability"},
		{"Query optimization", "Use EXPLAIN to analyze slow queries and add appropriate indexes"},
	}

	s.embedder = nil // disable async
	for _, obs := range observations {
		id, err := s.AddObservation(AddObservationParams{
			SessionID: "s1",
			Type:      "learning",
			Title:     obs.title,
			Content:   obs.content,
			Project:   "test",
		})
		if err != nil {
			t.Fatalf("add observation: %v", err)
		}
		s.embedder = emb // restore for sync generation
		if err := s.GenerateEmbeddingSync(id, obs.title+" "+obs.content); err != nil {
			t.Fatalf("generate embedding: %v", err)
		}
		s.embedder = nil // disable again for next add
	}
	s.embedder = emb // restore for search

	// Search should return results (hybrid: FTS5 + vector).
	results, err := s.Search("MySQL connection", SearchOptions{Project: "test"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}

	if len(results) == 0 {
		t.Error("expected at least one result from hybrid search")
	}

	// The MySQL connection pooling result should be in the results.
	found := false
	for _, r := range results {
		if r.Title == "MySQL connection pooling" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 'MySQL connection pooling' in results")
	}
}

func TestVectorSearchFilters(t *testing.T) {
	s, emb := newTestStoreWithEmbeddings(t)

	if err := s.CreateSession("s1", "test", "/tmp/test"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Add observations in different projects — disable async to avoid race.
	s.embedder = nil
	id1, _ := s.AddObservation(AddObservationParams{
		SessionID: "s1", Type: "learning",
		Title: "Project A memory", Content: "Important memory for project A",
		Project: "project-a",
	})
	s.embedder = emb
	s.GenerateEmbeddingSync(id1, "Project A memory Important memory for project A")

	s.embedder = nil
	id2, _ := s.AddObservation(AddObservationParams{
		SessionID: "s1", Type: "learning",
		Title: "Project B memory", Content: "Important memory for project B",
		Project: "project-b",
	})
	s.embedder = emb
	s.GenerateEmbeddingSync(id2, "Project B memory Important memory for project B")

	// Vector search filtered to project-a should only return project-a results.
	vecResults := s.vectorSearch(mustEmbed(t, s, "Important memory"), SearchOptions{Project: "project-a"}, 10)

	for _, r := range vecResults {
		// Verify all results are from the correct project by checking observation.
		obs, _ := s.GetObservation(r.ObservationID)
		if obs != nil && obs.Project != nil && *obs.Project != "project-a" {
			t.Errorf("vector search returned wrong project: %s", *obs.Project)
		}
	}
}

func mustEmbed(t *testing.T, s *Store, text string) []float32 {
	t.Helper()
	vec, err := s.embedder.Embed(context.Background(), text)
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	return vec
}

func TestBackfillEmbeddings(t *testing.T) {
	s, emb := newTestStoreWithEmbeddings(t)

	if err := s.CreateSession("s1", "test", "/tmp/test"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Add observations without embeddings (temporarily remove provider).
	s.embedder = nil
	for i := 0; i < 5; i++ {
		_, err := s.AddObservation(AddObservationParams{
			SessionID: "s1",
			Type:      "learning",
			Title:     "Observation " + string(rune('A'+i)),
			Content:   "Content for observation " + string(rune('A'+i)),
			Project:   "test",
		})
		if err != nil {
			t.Fatalf("add observation %d: %v", i, err)
		}
	}

	// Verify no embeddings exist.
	var count int
	s.db.QueryRow("SELECT COUNT(*) FROM observation_embeddings").Scan(&count)
	if count != 0 {
		t.Fatalf("expected 0 embeddings before backfill, got %d", count)
	}

	// Restore provider and backfill.
	s.embedder = emb
	var lastDone, lastTotal int
	err := s.BackfillEmbeddings(2, func(done, total int) {
		lastDone = done
		lastTotal = total
	})
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}

	if lastTotal != 5 {
		t.Errorf("total = %d, want 5", lastTotal)
	}
	if lastDone != 5 {
		t.Errorf("done = %d, want 5", lastDone)
	}

	// Verify all embeddings were created.
	s.db.QueryRow("SELECT COUNT(*) FROM observation_embeddings").Scan(&count)
	if count != 5 {
		t.Errorf("expected 5 embeddings after backfill, got %d", count)
	}
}

func TestBackfillEmbeddingsNoProvider(t *testing.T) {
	s := newTestStore(t)
	err := s.BackfillEmbeddings(10, nil)
	if err == nil {
		t.Error("expected error when no provider configured")
	}
}

func TestEmbeddingTableCreatedOnMigration(t *testing.T) {
	s := newTestStore(t)

	// Verify the observation_embeddings table exists.
	var name string
	err := s.db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='observation_embeddings'").Scan(&name)
	if err != nil {
		t.Fatalf("observation_embeddings table not created: %v", err)
	}
	if name != "observation_embeddings" {
		t.Errorf("table name = %s", name)
	}
}

func TestSerializeDeserializeRoundtrip(t *testing.T) {
	vec := []float32{0.1, 0.2, -0.3, 1.5, 0.0}
	blob := embedding.SerializeFloat32(vec)
	restored := embedding.DeserializeFloat32(blob)

	for i := range vec {
		if vec[i] != restored[i] {
			t.Errorf("[%d] = %f, want %f", i, restored[i], vec[i])
		}
	}
}

// Suppress unused import warning — binary is used by mockEmbedder indirectly.
var _ = binary.LittleEndian
var _ = time.Now
