package llm_test

import (
	"testing"

	"github.com/Gentleman-Programming/engram/internal/llm"
)

// ─── A.5 tests ────────────────────────────────────────────────────────────────

// TestEstimatedTokenConstants verifies the per-pair token constants are locked
// at the design-specified values (300 in, 50 out).
func TestEstimatedTokenConstants(t *testing.T) {
	if llm.EstimatedInputTokensPerPair != 300 {
		t.Errorf("EstimatedInputTokensPerPair = %d; want 300", llm.EstimatedInputTokensPerPair)
	}
	if llm.EstimatedOutputTokensPerPair != 50 {
		t.Errorf("EstimatedOutputTokensPerPair = %d; want 50", llm.EstimatedOutputTokensPerPair)
	}
}

// TestEstimateScanCost_Zero verifies that zero pairs produces zero tokens.
func TestEstimateScanCost_Zero(t *testing.T) {
	in, out := llm.EstimateScanCost(0)
	if in != 0 {
		t.Errorf("EstimateScanCost(0): input tokens = %d; want 0", in)
	}
	if out != 0 {
		t.Errorf("EstimateScanCost(0): output tokens = %d; want 0", out)
	}
}

// TestEstimateScanCost_OnePair verifies the math for a single pair.
func TestEstimateScanCost_OnePair(t *testing.T) {
	in, out := llm.EstimateScanCost(1)
	if in != 300 {
		t.Errorf("EstimateScanCost(1): input tokens = %d; want 300", in)
	}
	if out != 50 {
		t.Errorf("EstimateScanCost(1): output tokens = %d; want 50", out)
	}
}

// TestEstimateScanCost_TenPairs verifies the math for 10 pairs.
func TestEstimateScanCost_TenPairs(t *testing.T) {
	in, out := llm.EstimateScanCost(10)
	wantIn := 10 * 300
	wantOut := 10 * 50
	if in != wantIn {
		t.Errorf("EstimateScanCost(10): input tokens = %d; want %d", in, wantIn)
	}
	if out != wantOut {
		t.Errorf("EstimateScanCost(10): output tokens = %d; want %d", out, wantOut)
	}
}

// TestEstimateScanCost_LargePairCount verifies linearity at scale (no overflow risk).
func TestEstimateScanCost_LargePairCount(t *testing.T) {
	pairCount := 1000
	in, out := llm.EstimateScanCost(pairCount)
	wantIn := pairCount * llm.EstimatedInputTokensPerPair
	wantOut := pairCount * llm.EstimatedOutputTokensPerPair
	if in != wantIn {
		t.Errorf("EstimateScanCost(%d): input tokens = %d; want %d", pairCount, in, wantIn)
	}
	if out != wantOut {
		t.Errorf("EstimateScanCost(%d): output tokens = %d; want %d", pairCount, out, wantOut)
	}
}

// TestEstimateScanCost_LinearityInvariant verifies that doubling pairs doubles tokens.
func TestEstimateScanCost_LinearityInvariant(t *testing.T) {
	in5, out5 := llm.EstimateScanCost(5)
	in10, out10 := llm.EstimateScanCost(10)

	if in10 != 2*in5 {
		t.Errorf("EstimateScanCost: expected input(10)=2*input(5); got %d != 2*%d", in10, in5)
	}
	if out10 != 2*out5 {
		t.Errorf("EstimateScanCost: expected output(10)=2*output(5); got %d != 2*%d", out10, out5)
	}
}
