package cloudconfig

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// ValidateServerURL returns the normalized server URL after enforcing
// the contract documented in REQ-1 of the spec:
//
//   - the scheme is http or https (rejected otherwise)
//   - the host is non-empty (rejected otherwise)
//   - the query and fragment components are CLEARED on success
//   - the path, port, trailing slash, and userinfo are preserved
//     as-given (no normalization beyond query/fragment stripping)
//
// ValidateServerURL returns an error for empty or whitespace-only
// input, malformed URLs that fail url.Parse, URLs whose scheme is
// not http or https, and URLs with no host.
//
// The function uses url.Parse (permissive) rather than
// url.ParseRequestURI (strict); the scheme and host checks above
// are what enforce the contract, not url.Parse itself. Trailing
// slashes are preserved naturally by url.URL.String() because
// url.Parse stores the path as-is.
func ValidateServerURL(raw string) (string, error) {
	u, err := parseURL(raw)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

// parseURL normalizes raw into a *url.URL with the scheme/host
// contract enforced and query/fragment cleared. It is the workhorse
// for ValidateServerURL; splitting it out keeps ValidateServerURL
// trivial (one parse + one stringify) and makes the validation
// steps individually testable.
//
// The order of checks matters:
//  1. whitespace-only / empty input is rejected first, so the
//     caller gets a clear "empty" error rather than a downstream
//     parse error or a silent zero-value URL.
//  2. url.Parse errors propagate (wrapped with the input for
//     debuggability).
//  3. the scheme check catches `://example.com` (which url.Parse
//     rejects) and the "not a URL" cases (`#frag`, `?q=1`, `not a
//     url`, all of which url.Parse accepts with an empty scheme).
//  4. the host check catches `https://` and `https://?q=1`.
//  5. query/fragment are cleared last so the caller gets the
//     canonical stored form.
func parseURL(raw string) (*url.URL, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("server URL is empty")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid server URL %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("server URL must use http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return nil, errors.New("server URL must have a host")
	}
	u.RawQuery = ""
	u.Fragment = ""
	return u, nil
}
