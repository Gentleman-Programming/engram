package embedding

import (
	"math"
	"testing"
)

func TestCosineSimilarityIdentical(t *testing.T) {
	a := []float32{1, 2, 3}
	sim := CosineSimilarity(a, a)
	if math.Abs(float64(sim-1.0)) > 0.0001 {
		t.Errorf("identical vectors: got %f, want 1.0", sim)
	}
}

func TestCosineSimilarityOrthogonal(t *testing.T) {
	a := []float32{1, 0, 0}
	b := []float32{0, 1, 0}
	sim := CosineSimilarity(a, b)
	if math.Abs(float64(sim)) > 0.0001 {
		t.Errorf("orthogonal vectors: got %f, want 0.0", sim)
	}
}

func TestCosineSimilarityOpposite(t *testing.T) {
	a := []float32{1, 2, 3}
	b := []float32{-1, -2, -3}
	sim := CosineSimilarity(a, b)
	if math.Abs(float64(sim+1.0)) > 0.0001 {
		t.Errorf("opposite vectors: got %f, want -1.0", sim)
	}
}

func TestCosineSimilarityZeroVector(t *testing.T) {
	a := []float32{0, 0, 0}
	b := []float32{1, 2, 3}
	sim := CosineSimilarity(a, b)
	if sim != 0 {
		t.Errorf("zero vector: got %f, want 0.0", sim)
	}
}

func TestCosineSimilarityDifferentLength(t *testing.T) {
	a := []float32{1, 2}
	b := []float32{1, 2, 3}
	sim := CosineSimilarity(a, b)
	if sim != 0 {
		t.Errorf("different lengths: got %f, want 0.0", sim)
	}
}

func TestCosineSimilarityEmpty(t *testing.T) {
	sim := CosineSimilarity(nil, nil)
	if sim != 0 {
		t.Errorf("empty vectors: got %f, want 0.0", sim)
	}
}

func TestSerializeDeserializeFloat32(t *testing.T) {
	original := []float32{0.1, 0.2, -0.3, 1.5, 0.0}
	blob := SerializeFloat32(original)

	if len(blob) != len(original)*4 {
		t.Fatalf("blob size = %d, want %d", len(blob), len(original)*4)
	}

	restored := DeserializeFloat32(blob)
	if len(restored) != len(original) {
		t.Fatalf("restored length = %d, want %d", len(restored), len(original))
	}

	for i := range original {
		if restored[i] != original[i] {
			t.Errorf("[%d] = %f, want %f", i, restored[i], original[i])
		}
	}
}

func TestDeserializeFloat32BadLength(t *testing.T) {
	result := DeserializeFloat32([]byte{1, 2, 3}) // not a multiple of 4
	if result != nil {
		t.Errorf("expected nil for bad length, got %v", result)
	}
}

func TestDeserializeFloat32Empty(t *testing.T) {
	result := DeserializeFloat32(nil)
	if result != nil {
		t.Errorf("expected nil for nil input, got %v", result)
	}
}

func TestMergeRRF(t *testing.T) {
	ftsIDs := []int64{10, 20, 30}
	vecIDs := []int64{20, 40, 10}

	scores := MergeRRF(ftsIDs, vecIDs, 60)

	// ID 10: appears in FTS rank 0 and vec rank 2
	// FTS: 1/(60+1) = 0.01639, vec: 1/(60+3) = 0.01587
	// Combined: 0.03226
	if scores[10] < 0.032 || scores[10] > 0.033 {
		t.Errorf("ID 10 score = %f, expected ~0.0323", scores[10])
	}

	// ID 20: appears in FTS rank 1 and vec rank 0
	// FTS: 1/(60+2) = 0.01613, vec: 1/(60+1) = 0.01639
	// Combined: 0.03252
	if scores[20] < 0.032 || scores[20] > 0.033 {
		t.Errorf("ID 20 score = %f, expected ~0.0325", scores[20])
	}

	// ID 30: only in FTS rank 2
	// FTS: 1/(60+3) = 0.01587
	if scores[30] < 0.015 || scores[30] > 0.016 {
		t.Errorf("ID 30 score = %f, expected ~0.0159", scores[30])
	}

	// ID 40: only in vec rank 1
	// vec: 1/(60+2) = 0.01613
	if scores[40] < 0.016 || scores[40] > 0.017 {
		t.Errorf("ID 40 score = %f, expected ~0.0161", scores[40])
	}

	// ID 20 should have the highest score (appears high in both)
	if scores[20] <= scores[30] {
		t.Error("ID 20 should score higher than ID 30")
	}
	if scores[20] <= scores[40] {
		t.Error("ID 20 should score higher than ID 40")
	}
}

func TestMergeRRFEmpty(t *testing.T) {
	scores := MergeRRF(nil, nil, 60)
	if len(scores) != 0 {
		t.Errorf("expected empty scores, got %d", len(scores))
	}
}

func BenchmarkCosineSimilarity768(b *testing.B) {
	a := make([]float32, 768)
	c := make([]float32, 768)
	for i := range a {
		a[i] = float32(i) / 768
		c[i] = float32(768-i) / 768
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		CosineSimilarity(a, c)
	}
}
