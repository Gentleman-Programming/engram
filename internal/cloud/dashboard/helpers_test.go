package dashboard

import (
	"os"
	"strings"
	"testing"
)

// TestSafeQueryDefangsPercentEncodedQuoteAttack is the RED test for N1.
// html.EscapeString on a rawQuery containing '&' turns it into '&amp;', which
// is wrong inside an href attribute — the browser sees &amp; literally and the
// URL breaks. The fix normalises the query via url.ParseQuery + Encode, which
// produces correct percent-encoded output without HTML-entity pollution.
func TestSafeQueryDefangsPercentEncodedQuoteAttack(t *testing.T) {
	// Multi-param query: old html.EscapeString turns & into &amp; inside href.
	result := safeQuery("/dashboard/admin/audit-log/list", `contributor=alice&page=2`)
	// The result must NOT contain &amp; — that would break the URL in href.
	if strings.Contains(result, "&amp;") {
		t.Errorf("safeQuery HTML-escaped '&' in URL: %q (must not contain &amp;)", result)
	}
	// The result must still be a valid URL (starts with the path).
	if !strings.HasPrefix(result, "/dashboard/admin/audit-log/list") {
		t.Errorf("safeQuery dropped or mangled the path: %q", result)
	}
}

// TestSafeQueryRejectsLiteralDoubleQuote ensures a rawQuery with a literal
// double-quote does NOT appear unescaped in the output (XSS breakout via href).
func TestSafeQueryRejectsLiteralDoubleQuote(t *testing.T) {
	// Literal " in a rawQuery is a malformed query string. url.ParseQuery will
	// URL-encode it to %22, preventing it from breaking an HTML attribute boundary.
	result := safeQuery("/path", `q="onmouseover=alert(1)`)
	if strings.Contains(result, `"`) {
		t.Errorf("safeQuery returned unescaped double-quote in URL: %q", result)
	}
}

// TestSafeQueryPreservesNormalParams verifies that well-formed params round-trip correctly.
func TestSafeQueryPreservesNormalParams(t *testing.T) {
	result := safeQuery("/dashboard/admin/audit-log/list", "contributor=alice&page=2")
	if !strings.Contains(result, "contributor=alice") {
		t.Errorf("safeQuery dropped contributor param: %q", result)
	}
	if !strings.Contains(result, "page=2") {
		t.Errorf("safeQuery dropped page param: %q", result)
	}
}

// TestSafeQueryEmptyRawQueryReturnsPath verifies no trailing '?' is appended.
func TestSafeQueryEmptyRawQueryReturnsPath(t *testing.T) {
	result := safeQuery("/dashboard/projects", "")
	if result != "/dashboard/projects" {
		t.Errorf("safeQuery with empty rawQuery = %q, want %q", result, "/dashboard/projects")
	}
}

func TestSanitizeDashboardNextNormalizesAndConstrainsPath(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "empty next", raw: "", want: ""},
		{name: "simple dashboard path", raw: "/dashboard/projects?q=alpha", want: "/dashboard/projects?q=alpha"},
		{name: "dot segment stays in dashboard namespace", raw: "/dashboard/projects/../admin", want: "/dashboard/admin"},
		{name: "encoded dot segment stays in dashboard namespace", raw: "/dashboard/projects/%2e%2e?q=beta", want: "/dashboard?q=beta"},
		{name: "dot segment escaping dashboard rejected", raw: "/dashboard/../admin", want: ""},
		{name: "encoded dot segment escaping dashboard rejected", raw: "/dashboard/%2e%2e/admin", want: ""},
		{name: "dashboard prefix must be exact namespace", raw: "/dashboarding", want: ""},
		{name: "absolute URL rejected", raw: "https://evil.example/dashboard", want: ""},
		{name: "scheme-relative URL rejected", raw: "//evil.example/dashboard", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeDashboardNext(tt.raw); got != tt.want {
				t.Fatalf("sanitizeDashboardNext(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

// withEngramTimezone forces ENGRAM_TIMEZONE for the test body and restores
// the original value afterwards, so timezone-sensitive tests stay isolated.
func withEngramTimezone(t *testing.T, tz string) {
	t.Helper()
	old, hadOld := os.LookupEnv("ENGRAM_TIMEZONE")
	t.Setenv("ENGRAM_TIMEZONE", tz)
	t.Cleanup(func() {
		if hadOld {
			os.Setenv("ENGRAM_TIMEZONE", old)
			return
		}
		os.Unsetenv("ENGRAM_TIMEZONE")
	})
}

// TestFormatTimestampParsesSQLiteStyleInput is the regression test for the bug
// reported on #169 by @quirozino. The legacy dashboard helper called
// time.Parse(time.RFC3339Nano, ts) which silently failed on SQLite-style
// "YYYY-MM-DD HH:MM:SS" values and leaked raw UTC strings into the UI. The new
// helper delegates to timeutil and must convert these values correctly.
func TestFormatTimestampParsesSQLiteStyleInput(t *testing.T) {
	withEngramTimezone(t, "America/Bogota") // UTC-5

	// SQLite-style timestamp stored as UTC noon → 07:00 in Bogota.
	got := formatTimestamp("2026-05-22 12:00:00")
	want := "22 May 2026 07:00"
	if got != want {
		t.Fatalf("formatTimestamp(SQLite-style) = %q, want %q (regression of #169 dashboard bug)", got, want)
	}
}

func TestFormatTimestampParsesRFC3339Input(t *testing.T) {
	withEngramTimezone(t, "America/Bogota")

	got := formatTimestamp("2026-05-22T12:00:00Z")
	want := "22 May 2026 07:00"
	if got != want {
		t.Fatalf("formatTimestamp(RFC3339) = %q, want %q", got, want)
	}
}

func TestFormatTimestampHonorsEngramTimezone(t *testing.T) {
	withEngramTimezone(t, "Europe/Madrid") // UTC+2 in May (CEST)

	got := formatTimestamp("2026-05-22T09:56:00Z")
	want := "22 May 2026 11:56"
	if got != want {
		t.Fatalf("formatTimestamp under Europe/Madrid = %q, want %q", got, want)
	}
}

func TestFormatTimestampEmptyReturnsDash(t *testing.T) {
	if got := formatTimestamp(""); got != "-" {
		t.Fatalf("formatTimestamp(empty) = %q, want %q", got, "-")
	}
	if got := formatTimestamp("   "); got != "-" {
		t.Fatalf("formatTimestamp(whitespace) = %q, want %q", got, "-")
	}
}

func TestFormatTimestampUnparseableReturnsRaw(t *testing.T) {
	// Malformed input must not be lost; the helper returns it as-is so the UI
	// stays informative even when the source data is unexpected.
	in := "not-a-timestamp"
	if got := formatTimestamp(in); got != in {
		t.Fatalf("formatTimestamp(unparseable) = %q, want %q", got, in)
	}
}

func TestFormatTimestampStrEmptyReturnsNever(t *testing.T) {
	if got := formatTimestampStr(""); got != "Never" {
		t.Fatalf("formatTimestampStr(empty) = %q, want %q", got, "Never")
	}
}

