package cloudconfig

import (
	"fmt"
	"net/url"
	"strings"
)

// ValidateServerURL accepts HTTP(S) server URLs with a host. Query and fragment
// components are rejected so stored runtime endpoints remain unambiguous.
func ValidateServerURL(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", fmt.Errorf("server URL is required")
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil {
		return "", err
	}
	scheme := strings.ToLower(strings.TrimSpace(parsed.Scheme))
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("scheme must be http or https")
	}
	if strings.TrimSpace(parsed.Host) == "" || strings.TrimSpace(parsed.Hostname()) == "" {
		return "", fmt.Errorf("host is required")
	}
	if strings.TrimSpace(parsed.RawQuery) != "" {
		return "", fmt.Errorf("query is not allowed")
	}
	if strings.TrimSpace(parsed.Fragment) != "" {
		return "", fmt.Errorf("fragment is not allowed")
	}
	return parsed.String(), nil
}
