package embedding

import (
	"encoding/binary"
	"math"
)

// CosineSimilarity computes the cosine similarity between two vectors.
// Returns a value in [-1, 1] where 1 means identical direction.
// Returns 0 if either vector has zero magnitude.
func CosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}

	var dot, normA, normB float32
	for i := range a {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}

	if normA == 0 || normB == 0 {
		return 0
	}

	return dot / float32(math.Sqrt(float64(normA)*float64(normB)))
}

// SerializeFloat32 encodes a float32 slice as a compact binary blob (4 bytes per element).
func SerializeFloat32(v []float32) []byte {
	buf := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

// DeserializeFloat32 decodes a binary blob back to a float32 slice.
func DeserializeFloat32(b []byte) []float32 {
	if len(b) == 0 || len(b)%4 != 0 {
		return nil
	}
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}

// VectorSearchResult holds an observation ID and its cosine similarity score.
type VectorSearchResult struct {
	ObservationID int64
	Similarity    float32
}

// MergeRRF merges FTS5 and vector search results using Reciprocal Rank Fusion.
// k is the RRF constant (typically 60). Higher k reduces the impact of high-ranking items.
// The returned scores are RRF combined scores (higher is better).
func MergeRRF(ftsIDs, vecIDs []int64, k int) map[int64]float64 {
	scores := make(map[int64]float64)

	for rank, id := range ftsIDs {
		scores[id] += 1.0 / float64(k+rank+1)
	}

	for rank, id := range vecIDs {
		scores[id] += 1.0 / float64(k+rank+1)
	}

	return scores
}
