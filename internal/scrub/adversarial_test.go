package scrub

import "testing"

// TestAdversarial asserts the security contract against evasion / coverage cases
// using only generic, non-proprietary examples.
//
// tier "high"      → must produce a finding that blocks ModeStrict.
// tier "heuristic" → must be detected (blocks ModeParanoid) but NOT ModeStrict,
//
//	because the shape is indistinguishable from benign data
//	(e.g. a 64-hex secret vs a sha256 digest).
//
// tier "undetectable" → documents a known gap: not caught by shape alone.
func TestAdversarial(t *testing.T) {
	cases := []struct {
		tier string
		desc string
		in   string
	}{
		// PAN — all high-confidence (Luhn-validated), evasions must still block.
		{"high", "contiguous valid visa", "4111111111111111"},
		{"high", "spaced groups", "4111 1111 1111 1111"},
		{"high", "dash groups", "4111-1111-1111-1111"},
		{"high", "dot separators", "4111.1111.1111.1111"},
		{"high", "amex 15-digit", "378282246310005"},
		{"high", "padded with extra leading digits", "00004111111111111111"},
		{"high", "letter-glued prefix", "ref4111111111111111"},
		{"high", "fullwidth digits", "４１１１１１１１１１１１１１１１"},

		// Card data — generic high-confidence.
		{"high", "track2 data (ISO standard)", "5500000000000004=2705"},
		{"high", "cvc by field name", `{"cvc":"123"}`},
		{"high", "card_number by field name", "card_number: 4111111111111111"},

		// Secrets — well-known provider shapes.
		{"high", "stripe live key", "sk_live_abcdef0123456789"}, //gitleaks:allow (synthetic test fixture)
		{"high", "postgres conn string (no dot host)", "postgres://admin:S3cretPass@localhost:5432/db"},
		{"high", "password assignment", "password: hunter2SuperSecretValue"},
		{"high", "bearer header", "Authorization: Bearer abc123def456ghi789jkl"},

		// PII — email + CURP are high-confidence (generic synthetic samples).
		{"high", "email", "alice@example.com"},
		{"high", "CURP uppercase", "XEXX800101HDFGHJ05"},
		{"high", "CURP lowercase", "xexx800101hdfghj05"},

		// Heuristic — detectable but shape-ambiguous, block only in paranoid.
		{"heuristic", "generic 64-hex api key", "a3f9c1d4e5b6a7c8d9e0f1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b1c2"}, //gitleaks:allow (synthetic test fixture)
		{"heuristic", "base64-wrapped secret", "c2tfbGl2ZV9hYmNkZWYwMTIzNDU2Nzg5cXdlcnR5"},                          //gitleaks:allow (synthetic test fixture)
		{"heuristic", "RFC lowercase", "xexx800101ab1"},
		{"heuristic", "mexican phone", "55 1234 5678"},
		{"heuristic", "clabe 18-digit value", "002010077777777771"},
		{"heuristic", "mongo objectid (synthetic)", "0123456789abcdef01234567"},

		// Undetectable by shape alone — documented gap.
		{"undetectable", "bare standalone CVV", "the code is 123"},
	}

	for _, c := range cases {
		findings := Scan([]byte(c.in))
		blocksStrict, blocksParanoid := false, false
		for _, f := range findings {
			if f.Blocks(ModeStrict) {
				blocksStrict = true
			}
			if f.Blocks(ModeParanoid) {
				blocksParanoid = true
			}
		}
		switch c.tier {
		case "high":
			if !blocksStrict {
				t.Errorf("HIGH must block strict but did not: %q (findings=%v)", c.desc, findings)
			}
		case "heuristic":
			if blocksStrict {
				t.Errorf("HEURISTIC must NOT block strict: %q (findings=%v)", c.desc, findings)
			}
			if !blocksParanoid {
				t.Errorf("HEURISTIC must block paranoid (be detected): %q", c.desc)
			}
		case "undetectable":
			if blocksStrict {
				t.Logf("note: %q unexpectedly blocked strict (acceptable)", c.desc)
			}
		}
	}
}

// TestCustomPatterns proves operators can add deployment-specific detectors via
// config WITHOUT any organization-specific pattern living in this OSS code. Uses
// a fictional "acme_" prefix purely as an example.
func TestCustomPatterns(t *testing.T) {
	t.Cleanup(func() { _ = SetCustomPatterns(nil) })

	// Before: a fictional internal id is not detected by the generic engine.
	if got := Scan([]byte("ref acme_7f3a9c1d in note")); len(got) != 0 {
		t.Fatalf("expected no findings before custom patterns, got %+v", got)
	}

	err := SetCustomPatterns([]CustomPattern{
		{Name: "acme_resource_id", Category: "internal_id", Severity: "heuristic", Regex: `\bacme_[0-9a-f]{8}\b`},
		{Name: "acme_secret_field", Category: "secret", Severity: "high", Regex: `(?i)\bacme_token\b\s*[:=]\s*\S`},
	})
	if err != nil {
		t.Fatalf("SetCustomPatterns: %v", err)
	}

	id := Scan([]byte("ref acme_7f3a9c1d in note"))
	if len(id) == 0 || id[0].Detector != "acme_resource_id" || id[0].Blocks(ModeStrict) {
		t.Fatalf("expected heuristic custom id finding, got %+v", id)
	}
	sec := Scan([]byte("acme_token = abc123"))
	if len(sec) == 0 || !sec[0].Blocks(ModeStrict) {
		t.Fatalf("expected high custom secret finding to block strict, got %+v", sec)
	}
}
