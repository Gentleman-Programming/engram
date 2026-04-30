package llm

// ─── Token estimate constants ─────────────────────────────────────────────────

// EstimatedInputTokensPerPair is the estimated number of input tokens consumed
// per observation pair comparison call. This value was calibrated from live
// runs against claude haiku with the locked canonical prompt template.
//
// LOCKED: Do not change without updating cost warning output and design docs.
const EstimatedInputTokensPerPair = 300

// EstimatedOutputTokensPerPair is the estimated number of output tokens produced
// per observation pair comparison call. The single-line JSON verdict is compact
// by design (Relation + Confidence + short Reasoning ≤ 200 chars).
//
// LOCKED: Do not change without updating cost warning output and design docs.
const EstimatedOutputTokensPerPair = 50

// ─── Cost estimation ──────────────────────────────────────────────────────────

// EstimateScanCost returns the estimated total input and output token counts
// for a semantic scan over pairCount observation pairs.
//
// These are estimates for user-facing cost warnings. Actual token usage depends
// on observation content length and model variability.
//
// Returns (inputTokens, outputTokens).
func EstimateScanCost(pairCount int) (inputTokens, outputTokens int) {
	return pairCount * EstimatedInputTokensPerPair, pairCount * EstimatedOutputTokensPerPair
}
