package remote

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

func TestParseExtraHeadersUnsetOrBlank(t *testing.T) {
	for _, raw := range []string{"", "   ", " , , "} {
		if got := parseExtraHeaders(raw); len(got) != 0 {
			t.Fatalf("parseExtraHeaders(%q)=%+v, want none", raw, got)
		}
	}
}

func TestParseExtraHeadersValidPairs(t *testing.T) {
	got := parseExtraHeaders("CF-Access-Client-Id: abc.access, CF-Access-Client-Secret: secret")
	if len(got) != 2 {
		t.Fatalf("parsed=%+v, want 2 pairs", got)
	}
	want := []extraHeader{
		{key: "CF-Access-Client-Id", value: "abc.access"},
		{key: "CF-Access-Client-Secret", value: "secret"},
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("parsed[%d]=%+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestParseExtraHeadersTrimsSurroundingWhitespace(t *testing.T) {
	got := parseExtraHeaders("  X-Id :  abc.access , X-Secret: secret  ")
	if len(got) != 2 || got[0] != (extraHeader{key: "X-Id", value: "abc.access"}) || got[1] != (extraHeader{key: "X-Secret", value: "secret"}) {
		t.Fatalf("parsed=%+v", got)
	}
}

func TestParseExtraHeadersPreservesColonInsideValue(t *testing.T) {
	got := parseExtraHeaders("X-Token: abc:def")
	if len(got) != 1 || got[0] != (extraHeader{key: "X-Token", value: "abc:def"}) {
		t.Fatalf("parsed=%+v", got)
	}
}

func TestParseExtraHeadersSkipsMalformedPairs(t *testing.T) {
	got := parseExtraHeaders("X-Empty-Value:, :no-key, no-colon-here, Good: value")
	if len(got) != 1 || got[0] != (extraHeader{key: "Good", value: "value"}) {
		t.Fatalf("parsed=%+v, want only the well-formed pair", got)
	}
}

func TestParseExtraHeadersRejectsAuthorization(t *testing.T) {
	got := parseExtraHeaders("authorization: injected, AUTHORIZATION: injected-2, X-Real: yes")
	if len(got) != 1 || got[0] != (extraHeader{key: "X-Real", value: "yes"}) {
		t.Fatalf("parsed=%+v, want Authorization pairs rejected", got)
	}
}

func TestParseExtraHeadersLaterDuplicateWins(t *testing.T) {
	got := parseExtraHeaders("X-Dup: first, X-Dup: second")
	if len(got) != 1 || got[0] != (extraHeader{key: "X-Dup", value: "second"}) {
		t.Fatalf("parsed=%+v, want last duplicate to win", got)
	}
}

func TestParseExtraHeadersWarningNeverLogsValues(t *testing.T) {
	var buf bytes.Buffer
	oldOutput := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(oldOutput) })

	parseExtraHeaders("X-Real: visible-value, secret-value-without-colon, EMPTY-VALUE:, Authorization: should-never-appear")

	output := buf.String()
	if strings.Contains(output, "visible-value") || strings.Contains(output, "secret-value-without-colon") || strings.Contains(output, "should-never-appear") {
		t.Fatalf("log output leaked header values: %q", output)
	}
	if !strings.Contains(output, "Authorization") || !strings.Contains(output, "ENGRAM_CLOUD_EXTRA_HEADERS") {
		t.Fatalf("log output=%q, want a warning naming the variables without values", output)
	}
}
