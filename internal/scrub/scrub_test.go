package scrub

import (
	"os"
	"testing"
)

func hasCategory(fs []Finding, c Category) bool {
	for _, f := range fs {
		if f.Category == c {
			return true
		}
	}
	return false
}

func TestScan_PAN(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		// 4111 1111 1111 1111 is the canonical Visa test PAN (valid Luhn).
		{"visa test pan", "card 4111111111111111 charged", true},
		{"pan grouped spaces", "4111 1111 1111 1111", true},
		{"pan grouped dashes", "5500-0000-0000-0004", true}, // valid Luhn Mastercard test
		// A 16-digit Mongo-ish order id that fails Luhn must NOT trip PAN.
		{"order id not luhn", "order 1234567890123456 ok", false},
		{"short digit run", "ref 12345678 ok", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := hasCategory(Scan([]byte(c.in)), CategoryPAN)
			if got != c.want {
				t.Fatalf("PAN detection = %v, want %v (in=%q)", got, c.want, c.in)
			}
		})
	}
}

func TestScan_Secrets(t *testing.T) {
	cases := map[string]string{
		"private key": "-----BEGIN RSA PRIVATE KEY-----\nMIIE...",
		"jwt":         "token eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dummysig",
		"aws key":     "AKIAIOSFODNN7EXAMPLE",
		"stripe key":  "sk_live_abcdef0123456789", //gitleaks:allow (synthetic test fixture)
		"github pat":  "ghp_abcdefghijklmnopqrstuvwxyz0123456789",
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if !hasCategory(Scan([]byte(in)), CategorySecret) {
				t.Fatalf("expected secret finding for %q", in)
			}
		})
	}
}

func TestScan_PII(t *testing.T) {
	cases := map[string]struct {
		in   string
		want bool
	}{
		"email":          {"contact alice@example.com please", true},
		"curp":           {"CURP XEXX800101HDFGHJ05 found", true},
		"plain sentence": {"this is a normal architecture note about retries", false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := hasCategory(Scan([]byte(c.in)), CategoryPII); got != c.want {
				t.Fatalf("PII detection = %v, want %v (in=%q)", got, c.want, c.in)
			}
		})
	}
}

func TestScan_CleanContentNoFindings(t *testing.T) {
	clean := `{"title":"Fixed N+1 in charge listing","content":"Batched the query with includes; root cause was per-row lookups."}`
	if fs := Scan([]byte(clean)); len(fs) != 0 {
		t.Fatalf("expected no findings on clean content, got %+v", fs)
	}
}

func TestModeFromEnv(t *testing.T) {
	cases := map[string]Mode{"": ModeOff, "off": ModeOff, "warn": ModeWarn, "strict": ModeStrict, "STRICT": ModeStrict}
	for in, want := range cases {
		t.Setenv("ENGRAM_SCRUB", in)
		if got := ModeFromEnv(); got != want {
			t.Fatalf("ModeFromEnv(%q) = %v, want %v", in, got, want)
		}
	}
	_ = os.Unsetenv("ENGRAM_SCRUB")
}
