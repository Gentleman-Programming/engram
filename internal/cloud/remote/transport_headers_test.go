package remote

import (
	"bytes"
	"log"
	"os"
	"testing"
)

// TestParseExtraHeaders_SinglePair verifies a single valid pair is parsed correctly.
func TestParseExtraHeaders_SinglePair(t *testing.T) {
	result := parseExtraHeaders("X-Tenant-ID: abc")
	if result == nil {
		t.Fatal("expected non-nil map, got nil")
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 entry, got %d: %v", len(result), result)
	}
	// http.CanonicalHeaderKey("X-Tenant-ID") = "X-Tenant-Id"
	if v, ok := result["X-Tenant-Id"]; !ok || v != "abc" {
		t.Fatalf("expected X-Tenant-Id=abc, got %v", result)
	}
}

// TestParseExtraHeaders_MultiPair verifies two valid pairs produce a two-entry map.
func TestParseExtraHeaders_MultiPair(t *testing.T) {
	result := parseExtraHeaders("X-A: 1, X-B: 2")
	if result == nil {
		t.Fatal("expected non-nil map, got nil")
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 entries, got %d: %v", len(result), result)
	}
	if result["X-A"] != "1" {
		t.Fatalf("expected X-A=1, got %q", result["X-A"])
	}
	if result["X-B"] != "2" {
		t.Fatalf("expected X-B=2, got %q", result["X-B"])
	}
}

// TestParseExtraHeaders_EmptyString verifies empty string returns nil.
func TestParseExtraHeaders_EmptyString(t *testing.T) {
	result := parseExtraHeaders("")
	if result != nil {
		t.Fatalf("expected nil for empty string, got %v", result)
	}
}

// TestParseExtraHeaders_WhitespaceOnly verifies whitespace-only input returns nil.
func TestParseExtraHeaders_WhitespaceOnly(t *testing.T) {
	result := parseExtraHeaders("   \t  ")
	if result != nil {
		t.Fatalf("expected nil for whitespace-only input, got %v", result)
	}
}

// TestParseExtraHeaders_MalformedPairSkipped verifies malformed pairs are skipped with a warning,
// and remaining valid pairs are returned.
func TestParseExtraHeaders_MalformedPairSkipped(t *testing.T) {
	orig := log.Writer()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(orig)

	result := parseExtraHeaders("X-A: 1, badpair, X-B: 2")
	if result == nil {
		t.Fatal("expected non-nil map, got nil")
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 entries, got %d: %v", len(result), result)
	}
	if result["X-A"] != "1" {
		t.Fatalf("expected X-A=1, got %q", result["X-A"])
	}
	if result["X-B"] != "2" {
		t.Fatalf("expected X-B=2, got %q", result["X-B"])
	}
	logOut := buf.String()
	if logOut == "" {
		t.Fatal("expected warning log for malformed pair, got empty")
	}
	// The log should reference the malformed pair "badpair"
	if !contains(logOut, "badpair") {
		t.Fatalf("expected log to mention 'badpair', got: %q", logOut)
	}
}

// TestParseExtraHeaders_AllMalformed verifies all-malformed input returns nil.
func TestParseExtraHeaders_AllMalformed(t *testing.T) {
	orig := log.Writer()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(orig)

	result := parseExtraHeaders("nocolon, alsono")
	if result != nil {
		t.Fatalf("expected nil for all-malformed input, got %v", result)
	}
}

// TestParseExtraHeaders_AuthorizationRejected_ExactCase verifies exact-case "Authorization" is rejected
// and the secret value is never logged.
func TestParseExtraHeaders_AuthorizationRejected_ExactCase(t *testing.T) {
	orig := log.Writer()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(orig)

	result := parseExtraHeaders("Authorization: secret")
	if result != nil {
		t.Fatalf("expected nil when Authorization is rejected, got %v", result)
	}
	logOut := buf.String()
	if contains(logOut, "secret") {
		t.Fatalf("secret value must NOT appear in log, got: %q", logOut)
	}
}

// TestParseExtraHeaders_AuthorizationRejected_UpperCase verifies uppercase AUTHORIZATION is rejected.
func TestParseExtraHeaders_AuthorizationRejected_UpperCase(t *testing.T) {
	orig := log.Writer()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(orig)

	result := parseExtraHeaders("AUTHORIZATION: secret")
	if result != nil {
		t.Fatalf("expected nil when AUTHORIZATION is rejected, got %v", result)
	}
	logOut := buf.String()
	if contains(logOut, "secret") {
		t.Fatalf("secret value must NOT appear in log, got: %q", logOut)
	}
}

// TestParseExtraHeaders_AuthorizationRejected_LowerCase verifies lowercase authorization is rejected.
func TestParseExtraHeaders_AuthorizationRejected_LowerCase(t *testing.T) {
	orig := log.Writer()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(orig)

	result := parseExtraHeaders("authorization: secret")
	if result != nil {
		t.Fatalf("expected nil when authorization is rejected, got %v", result)
	}
	logOut := buf.String()
	if contains(logOut, "secret") {
		t.Fatalf("secret value must NOT appear in log, got: %q", logOut)
	}
}

// TestParseExtraHeaders_WhitespaceTrim verifies leading/trailing whitespace is trimmed from keys and values.
func TestParseExtraHeaders_WhitespaceTrim(t *testing.T) {
	result := parseExtraHeaders("  X-Foo :  bar  ")
	if result == nil {
		t.Fatal("expected non-nil map, got nil")
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 entry, got %d: %v", len(result), result)
	}
	if v, ok := result["X-Foo"]; !ok || v != "bar" {
		t.Fatalf("expected X-Foo=bar, got %v", result)
	}
}

// TestParseExtraHeaders_ValueWithColon verifies values containing colons are preserved
// (only the first colon acts as separator).
func TestParseExtraHeaders_ValueWithColon(t *testing.T) {
	result := parseExtraHeaders("X-Trace-Id: req:abc:123")
	if result == nil {
		t.Fatal("expected non-nil map, got nil")
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 entry, got %d: %v", len(result), result)
	}
	if v, ok := result["X-Trace-Id"]; !ok || v != "req:abc:123" {
		t.Fatalf("expected X-Trace-Id=req:abc:123, got %v", result)
	}
}

// TestParseExtraHeaders_MultiHeaderMixedValidMalformed verifies that valid pairs are kept,
// malformed are skipped, and Authorization is rejected; only X-A and X-B survive.
func TestParseExtraHeaders_MultiHeaderMixedValidMalformed(t *testing.T) {
	orig := log.Writer()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(orig)

	result := parseExtraHeaders("X-A: 1, nocolon, X-B: 2, Authorization: bad")
	if result == nil {
		t.Fatal("expected non-nil map, got nil")
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 entries (X-A, X-B), got %d: %v", len(result), result)
	}
	if result["X-A"] != "1" {
		t.Fatalf("expected X-A=1, got %q", result["X-A"])
	}
	if result["X-B"] != "2" {
		t.Fatalf("expected X-B=2, got %q", result["X-B"])
	}
	if _, ok := result["Authorization"]; ok {
		t.Fatal("Authorization must not appear in result")
	}
	logOut := buf.String()
	if contains(logOut, "bad") {
		t.Fatalf("Authorization value must NOT appear in log, got: %q", logOut)
	}
}

// contains is a helper to avoid importing strings in the test name collision.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 || containsStr(s, substr))
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// Suppress unused import warning - os is used by other tests that may reference it.
var _ = os.Stderr
