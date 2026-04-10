package turboquant

import (
	"path/filepath"
	"testing"
)

func TestComputeSimHash_Normalization(t *testing.T) {
	text1 := "¡qué bug más difícil de la base de datos!"
	text2 := "que bug mas dificil de la base de datos" // Sin tildes

	hash1 := ComputeSimHash(text1)
	hash2 := ComputeSimHash(text2)

	// Ambos deberían generar firmas muy similares ahora que normalizamos.
	// Si son idénticos, la distancia de Hamming es 0.
	dist := HammingDistance(hash1, hash2)
	if dist > 0 {
		t.Errorf("Expected 0 distance for texts differing only by accents/punctuation, got %d", dist)
	}
}

func TestTurboCache_SaveLoad(t *testing.T) {
	cache := NewTurboCache()

	dbSig := ComputeSimHash("database connection mysql query optimization")
	cache.Add(dbSig, 404)

	tmpFile := filepath.Join(t.TempDir(), "engram.tq")

	err := cache.Save(tmpFile)
	if err != nil {
		t.Fatalf("Failed to save cache: %v", err)
	}

	// Verify we can load it into a new instance
	newCache := NewTurboCache()
	err = newCache.Load(tmpFile)
	if err != nil {
		t.Fatalf("Failed to load cache: %v", err)
	}

	if val, ok := newCache.GetExact(dbSig); !ok || val != 404 {
		t.Errorf("Expected to load offset 404 for dbSig, got %d (ok: %t)", val, ok)
	}
}

func TestTurboCacheNearest(t *testing.T) {
	cache := NewTurboCache()

	// Seed cache with some known semantic blocks
	dbSig := ComputeSimHash("database connection mysql query optimization")
	cssSig := ComputeSimHash("frontend css react tailwind flexbox padding")
	authSig := ComputeSimHash("oauth jwt token login authentication secure")

	cache.Add(dbSig, 100)
	cache.Add(cssSig, 200)
	cache.Add(authSig, 300)

	// User query
	queryText := "cómo arreglamos el problema del token de login seguro"
	querySig := ComputeSimHash(queryText)

	bestMatch, offset, _ := cache.FindNearest(querySig)

	if bestMatch != authSig {
		t.Errorf("Expected query to match auth signature. Matched signature with offset: %d", offset)
	}

	if offset != 300 {
		t.Errorf("Expected offset 300 for auth matching, got %d", offset)
	}
}

func BenchmarkComputeSimHash(b *testing.B) {
	text := "this is a very standard text that simulates a reasonably sized log line or a sentence coming from an issue or commit where we want to measure the performance of sim hash directly."
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ComputeSimHash(text)
	}
}

func BenchmarkFindNearest_1000Items(b *testing.B) {
	cache := NewTurboCache()
	// Populate 1000 dummy hashes
	for i := int64(0); i < 1000; i++ {
		// Just generate arbitrary signatures
		cache.Add(BlockSignature(uint64(i)*1234567890123), i)
	}
	
	query := ComputeSimHash("another query text just to get a signature for the benchmark")
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cache.FindNearest(query)
	}
}
