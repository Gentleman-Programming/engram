package remote

import (
	"log"
	"os"
	"strings"
)

const extraHeadersEnv = "ENGRAM_CLOUD_EXTRA_HEADERS"

// extraHeader is one statically configured header applied to every outgoing
// cloud sync request. Values are intentionally not retained in any log or
// error path.
type extraHeader struct {
	key   string
	value string
}

// extraHeadersFromEnv reads ENGRAM_CLOUD_EXTRA_HEADERS once at construction
// time. The raw string is parsed into a fixed slice, so no environment read or
// parsing happens per request.
func extraHeadersFromEnv() []extraHeader {
	return parseExtraHeaders(os.Getenv(extraHeadersEnv))
}

// parseExtraHeaders parses a comma-separated "Key: Value" list into a fixed
// set of extra headers. Malformed pairs are skipped with a warning that never
// logs header values, and any Authorization pair is rejected as a security
// guardrail so the configured bearer token can never be overridden.
// Later duplicates of the same key win over earlier ones.
func parseExtraHeaders(raw string) []extraHeader {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	headers := make([]extraHeader, 0)
	seen := make(map[string]int)
	for i, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, value, found := strings.Cut(part, ":")
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if !found || key == "" || value == "" || !validExtraHeaderName(key) {
			log.Printf("[cloud] %s: skipping malformed entry %d (expected \"Key: Value\" pairs separated by commas)", extraHeadersEnv, i+1)
			continue
		}
		if strings.EqualFold(key, "Authorization") {
			log.Printf("[cloud] %s: skipping %q entry: Authorization is managed by the configured bearer token", extraHeadersEnv, key)
			continue
		}
		// HTTP header names are case-insensitive, so a later duplicate of the
		// same key replaces the earlier value at its original position.
		lowered := strings.ToLower(key)
		if index, ok := seen[lowered]; ok {
			headers[index].value = value
			continue
		}
		seen[lowered] = len(headers)
		headers = append(headers, extraHeader{key: key, value: value})
	}
	return headers
}

// validExtraHeaderName reports whether key is a usable HTTP header field name:
// printable ASCII, no colon, comma, or surrounding whitespace. It is a narrow
// deterministic check sufficient for static operator-supplied headers.
func validExtraHeaderName(key string) bool {
	if key == "" {
		return false
	}
	for _, r := range key {
		if r <= ' ' || r >= 0x7f || r == ':' || r == ',' {
			return false
		}
	}
	return true
}
