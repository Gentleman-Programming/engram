package cloudconfig

import (
	"testing"
)

// TestValidateServerURL is the table-driven test for the
// ValidateServerURL contract documented in REQ-1 of the spec:
//
//   - http or https scheme only
//   - host required (non-empty)
//   - query and fragment CLEARED on success (not rejected)
//   - trailing slash preserved as-given
//   - error on empty, whitespace-only, missing-scheme, missing-host,
//     or unparseable input
//
// The test uses url.Parse semantics (permissive); the scheme and
// host checks are what enforce validation, not url.Parse itself.
//
// The port-out-of-range case is intentionally absent: Go's url.Parse
// does not validate port ranges (https://example.com:99999 parses
// successfully), and the spec does not require it. Adding a port
// range check would expand scope beyond the spec.
func TestValidateServerURL(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		// --- happy path: http and https ---
		{"https_basic", "https://example.com", "https://example.com", false},
		{"http_basic", "http://example.com", "http://example.com", false},
		{"https_with_port", "https://example.com:8080", "https://example.com:8080", false},
		{"https_with_trailing_slash", "https://example.com/", "https://example.com/", false},
		{"https_with_path", "https://example.com/path", "https://example.com/path", false},
		{"https_with_deep_path", "https://example.com/api/v1", "https://example.com/api/v1", false},

		// --- query/fragment cleared on success (TRIANGULATE) ---
		{"https_query_cleared", "https://example.com?q=1", "https://example.com", false},
		{"https_fragment_cleared", "https://example.com#frag", "https://example.com", false},
		{"https_path_query_fragment", "https://example.com/path?q=1#frag", "https://example.com/path", false},
		{"https_multi_query_cleared", "https://example.com?a=1&b=2#frag", "https://example.com", false},

		// --- error: bad scheme ---
		{"ftp_scheme", "ftp://example.com", "", true},
		{"file_scheme", "file://example.com", "", true},

		// --- error: missing scheme ---
		{"missing_scheme", "://example.com", "", true},

		// --- error: missing host ---
		{"https_no_host", "https://", "", true},
		{"http_no_host", "http://", "", true},
		{"https_empty_host_with_query", "https://?q=1", "", true},

		// --- error: not actually a URL ---
		{"just_fragment", "#frag", "", true},
		{"just_query", "?q=1", "", true},
		{"empty", "", "", true},
		{"whitespace_only", "   ", "", true},
		{"unparseable", "not a url", "", true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ValidateServerURL(tc.input)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("ValidateServerURL(%q) = (%q, nil), want error", tc.input, got)
			}
			// The returned string is undefined on error; do not assert on it.
			return
		}
			if err != nil {
				t.Fatalf("ValidateServerURL(%q) returned err=%v, want nil", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("ValidateServerURL(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// TestValidateServerURLPreservesTrailingSlash is a prominent
// regression test for the design's intent: the trailing slash is
// preserved exactly as the user provided it, not normalized away.
// The table-driven test above also covers this case; this test makes
// the contract explicit and is the first thing a reviewer sees when
// they ask "does the function mangle trailing slashes?".
func TestValidateServerURLPreservesTrailingSlash(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"https_with_slash", "https://example.com/", "https://example.com/"},
		{"https_without_slash", "https://example.com", "https://example.com"},
		{"https_with_path_and_slash", "https://example.com/api/", "https://example.com/api/"},
		{"https_with_path_no_slash", "https://example.com/api", "https://example.com/api"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ValidateServerURL(tc.input)
			if err != nil {
				t.Fatalf("ValidateServerURL(%q) returned err=%v, want nil", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("ValidateServerURL(%q) = %q, want %q (trailing slash must be preserved)", tc.input, got, tc.want)
			}
		})
	}
}

// TestValidateServerURLClearsQueryAndFragment is a prominent
// regression test for the design's intent: query and fragment
// components are CLEARED on success, not rejected. A URL with
// `?q=1` or `#frag` is valid; the components just don't make it
// into the stored config.
func TestValidateServerURLClearsQueryAndFragment(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"query_only", "https://example.com?q=1", "https://example.com"},
		{"fragment_only", "https://example.com#frag", "https://example.com"},
		{"multi_query", "https://example.com?a=1&b=2", "https://example.com"},
		{"path_with_query", "https://example.com/api?q=1", "https://example.com/api"},
		{"path_with_fragment", "https://example.com/api#frag", "https://example.com/api"},
		{"path_with_query_and_fragment", "https://example.com/api?q=1#frag", "https://example.com/api"},
		{"trailing_slash_with_query", "https://example.com/?q=1", "https://example.com/"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ValidateServerURL(tc.input)
			if err != nil {
				t.Fatalf("ValidateServerURL(%q) returned err=%v, want nil", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("ValidateServerURL(%q) = %q, want %q (query/fragment must be cleared)", tc.input, got, tc.want)
			}
			// Sanity: the returned string must not contain a query or fragment marker.
			if got != "" {
				for _, marker := range []string{"?", "#"} {
					if containsAny(got, marker) {
						t.Errorf("ValidateServerURL(%q) = %q, must not contain %q after clearing", tc.input, got, marker)
					}
				}
			}
		})
	}
}

// TestValidateServerURLPreservesPath is a regression test for the
// design's intent: the path component is preserved as-given. The
// spec only clears query and fragment; everything else (scheme,
// host, port, path) round-trips.
func TestValidateServerURLPreservesPath(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"single_segment", "https://example.com/api", "https://example.com/api"},
		{"deep_path", "https://example.com/api/v1/sessions", "https://example.com/api/v1/sessions"},
		{"path_with_dash", "https://example.com/api-v1/sessions", "https://example.com/api-v1/sessions"},
		{"path_with_underscore", "https://example.com/api_v1/sessions", "https://example.com/api_v1/sessions"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ValidateServerURL(tc.input)
			if err != nil {
				t.Fatalf("ValidateServerURL(%q) returned err=%v, want nil", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("ValidateServerURL(%q) = %q, want %q (path must be preserved)", tc.input, got, tc.want)
			}
		})
	}
}

// TestValidateServerURLPreservesUserInfo is a regression test for
// Go's url.Parse behavior: userinfo in the form user:pass@host is
// preserved in the URL. The spec doesn't require validation of
// userinfo, and the existing CLI's validateCloudServerURL also
// preserves it (it does not strip userinfo). This test pins the
// behavior so a future "validation tightening" change does not
// silently strip credentials.
func TestValidateServerURLPreservesUserInfo(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"user_pass", "https://user:pass@example.com", "https://user:pass@example.com"},
		{"user_only", "https://user@example.com", "https://user@example.com"},
		{"user_pass_with_path", "https://user:pass@example.com/api", "https://user:pass@example.com/api"},
		{"user_pass_with_port", "https://user:pass@example.com:8080", "https://user:pass@example.com:8080"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ValidateServerURL(tc.input)
			if err != nil {
				t.Fatalf("ValidateServerURL(%q) returned err=%v, want nil", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("ValidateServerURL(%q) = %q, want %q (userinfo must be preserved)", tc.input, got, tc.want)
			}
		})
	}
}

// TestValidateServerURLIPv6 is a regression test for Go's url.Parse
// behavior: IPv6 hosts are wrapped in brackets and must round-trip
// correctly. This is the format Go itself emits when normalizing an
// IPv6 URL, so the validator must accept and return it intact.
func TestValidateServerURLIPv6(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"loopback_with_port", "https://[::1]:8080", "https://[::1]:8080"},
		{"loopback_no_port", "https://[::1]", "https://[::1]"},
		{"ipv6_with_path", "https://[::1]/api", "https://[::1]/api"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ValidateServerURL(tc.input)
			if err != nil {
				t.Fatalf("ValidateServerURL(%q) returned err=%v, want nil", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("ValidateServerURL(%q) = %q, want %q (IPv6 host must round-trip)", tc.input, got, tc.want)
			}
		})
	}
}

// containsAny is a tiny helper to keep the post-clear sanity check
// readable. It is unexported because nothing outside the package
// needs it.
func containsAny(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
