package remote

import (
	"fmt"
	"log"
	"os"
	"strings"

	"golang.org/x/net/http/httpguts"
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
		if !found || key == "" || value == "" || !httpguts.ValidHeaderFieldName(key) || !httpguts.ValidHeaderFieldValue(value) {
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

// validateExtraHeadersScheme applies the same transport-security posture as
// the bearer token: configured extra headers can carry service credentials,
// so they are never sent over a plaintext remote URL. Tokenless HTTP remains
// available for local/dev smoke mode when no extra headers are configured.
func validateExtraHeadersScheme(baseURL string, headers []extraHeader) error {
	if len(headers) == 0 || strings.HasPrefix(strings.TrimSpace(baseURL), "https://") {
		return nil
	}
	return fmt.Errorf("cloud: %s requires an HTTPS remote URL", extraHeadersEnv)
}
