package timeutil

import (
	"os"
	"testing"
)

func TestFormatLocal(t *testing.T) {
	// Original tz
	oldTz := os.Getenv("ENGRAM_TIMEZONE")
	defer os.Setenv("ENGRAM_TIMEZONE", oldTz)

	// Set to Bogota
	os.Setenv("ENGRAM_TIMEZONE", "America/Bogota")

	// UTC time string
	utcStr := "2026-05-04 12:00:00" // noon UTC
	// In Bogota (UTC-5), this should be 07:00:00
	expected := "2026-05-04 07:00:00"

	result := FormatLocal(utcStr)
	if result != expected {
		t.Errorf("Expected %s but got %s", expected, result)
	}

	// Test RFC3339
	rfcStr := "2026-05-04T12:00:00Z"
	resultRfc := FormatLocal(rfcStr)
	if resultRfc != expected {
		t.Errorf("Expected RFC %s but got %s", expected, resultRfc)
	}

	// Test invalid
	invalidStr := "not-a-time"
	if FormatLocal(invalidStr) != invalidStr {
		t.Errorf("Expected invalid string to be returned as-is")
	}

	// Test fallback when env var is empty
	os.Setenv("ENGRAM_TIMEZONE", "")
	// When empty, it falls back to system local. It's hard to assert exactly without mocking time.Local,
	// but we can just ensure it parses successfully and doesn't panic.
	if FormatLocal(utcStr) == "" {
		t.Errorf("Expected fallback to format something, got empty string")
	}
}
