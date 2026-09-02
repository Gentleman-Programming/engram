package version

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestNormalizeVersion(t *testing.T) {
	tests := []struct{ in, want string }{
		{"v1.8.1", "1.8.1"},
		{"1.8.1", "1.8.1"},
		{" v2.0.0 ", "2.0.0"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := normalizeVersion(tt.in); got != tt.want {
			t.Errorf("normalizeVersion(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestUpdateCheckDisabled(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "unset", value: "", want: false},
		{name: "one", value: "1", want: true},
		{name: "true", value: "true", want: true},
		{name: "yes", value: "yes", want: true},
		{name: "on", value: "on", want: true},
		{name: "case and whitespace", value: " TRUE ", want: true},
		{name: "other value", value: "0", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(EnvNoUpdateCheck, tt.value)
			if got := updateCheckDisabled(); got != tt.want {
				t.Fatalf("updateCheckDisabled() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestSplitVersion(t *testing.T) {
	tests := []struct {
		in   string
		want [3]int
	}{
		{"1.8.1", [3]int{1, 8, 1}},
		{"2.0.0", [3]int{2, 0, 0}},
		{"1.0", [3]int{1, 0, 0}},
		{"", [3]int{0, 0, 0}},
		{"1.8.1-beta", [3]int{1, 8, 1}},
	}
	for _, tt := range tests {
		if got := splitVersion(tt.in); got != tt.want {
			t.Errorf("splitVersion(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestIsNewer(t *testing.T) {
	tests := []struct {
		latest, current string
		want            bool
	}{
		{"1.8.1", "1.8.0", true},
		{"2.0.0", "1.9.9", true},
		{"1.8.1", "1.8.1", false},
		{"1.7.0", "1.8.1", false},
		{"1.8.2", "1.8.1", true},
	}
	for _, tt := range tests {
		if got := isNewer(tt.latest, tt.current); got != tt.want {
			t.Errorf("isNewer(%q, %q) = %v, want %v", tt.latest, tt.current, got, tt.want)
		}
	}
}

func TestCheckLatest(t *testing.T) {
	t.Run("opt out skips the HTTP transport for a released version", func(t *testing.T) {
		t.Setenv("ENGRAM_NO_UPDATE_CHECK", "1")

		calls := 0
		oldClient := httpClient
		httpClient = &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			calls++
			return nil, fmt.Errorf("update check transport should not run")
		})}
		t.Cleanup(func() { httpClient = oldClient })

		result := CheckLatest("1.10.7")
		if calls != 0 {
			t.Fatalf("HTTP transport calls = %d, want 0", calls)
		}
		if result != (CheckResult{}) {
			t.Fatalf("result = %+v, want empty result", result)
		}
	})

	t.Run("dev and empty versions fail honestly", func(t *testing.T) {
		result := CheckLatest("dev")
		if result.Status != StatusCheckFailed {
			t.Fatalf("status = %q, want %q", result.Status, StatusCheckFailed)
		}
		if !strings.Contains(result.Message, "do not map to a release version") {
			t.Fatalf("message = %q", result.Message)
		}

		result = CheckLatest("")
		if result.Status != StatusCheckFailed {
			t.Fatalf("status = %q, want %q", result.Status, StatusCheckFailed)
		}
		if !strings.Contains(result.Message, "current version is unknown") {
			t.Fatalf("message = %q", result.Message)
		}
	})

	t.Run("update available", func(t *testing.T) {
		withCheckServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"tag_name":"v1.10.8"}`))
		}))

		result := CheckLatest("1.10.7")
		if result.Status != StatusUpdateAvailable {
			t.Fatalf("status = %q, want %q", result.Status, StatusUpdateAvailable)
		}
		if !strings.Contains(result.Message, "Update available: 1.10.7 -> 1.10.8") || !strings.Contains(result.Message, "To update:") {
			t.Fatalf("message = %q", result.Message)
		}
	})

	t.Run("up to date", func(t *testing.T) {
		withCheckServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"tag_name":"v1.10.7"}`))
		}))

		result := CheckLatest("1.10.7")
		if result.Status != StatusUpToDate {
			t.Fatalf("status = %q, want %q", result.Status, StatusUpToDate)
		}
		if result.Message != "" {
			t.Fatalf("message = %q, want empty", result.Message)
		}
	})

	t.Run("non-200 becomes check failed", func(t *testing.T) {
		withCheckServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "rate limited", http.StatusForbidden)
		}))

		result := CheckLatest("1.10.7")
		if result.Status != StatusCheckFailed {
			t.Fatalf("status = %q, want %q", result.Status, StatusCheckFailed)
		}
		if !strings.Contains(result.Message, "403 Forbidden") || !strings.Contains(result.Message, "GH_TOKEN") {
			t.Fatalf("message = %q", result.Message)
		}
	})

	t.Run("decode error becomes check failed", func(t *testing.T) {
		withCheckServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"tag_name":`))
		}))

		result := CheckLatest("1.10.7")
		if result.Status != StatusCheckFailed {
			t.Fatalf("status = %q, want %q", result.Status, StatusCheckFailed)
		}
		if !strings.Contains(result.Message, "could not read the GitHub response") {
			t.Fatalf("message = %q", result.Message)
		}
	})

	t.Run("missing tag becomes check failed", func(t *testing.T) {
		withCheckServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"tag_name":""}`))
		}))

		result := CheckLatest("1.10.7")
		if result.Status != StatusCheckFailed {
			t.Fatalf("status = %q, want %q", result.Status, StatusCheckFailed)
		}
		if !strings.Contains(result.Message, "did not return a release version") {
			t.Fatalf("message = %q", result.Message)
		}
	})

	t.Run("timeout becomes check failed", func(t *testing.T) {
		withCheckTimeout(t, 20*time.Millisecond)
		withCheckServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(50 * time.Millisecond)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"tag_name":"v1.10.8"}`))
		}))

		result := CheckLatest("1.10.7")
		if result.Status != StatusCheckFailed {
			t.Fatalf("status = %q, want %q", result.Status, StatusCheckFailed)
		}
		if !strings.Contains(result.Message, "took too long to respond") {
			t.Fatalf("message = %q", result.Message)
		}
	})
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestCheckLatestUsesGitHubToken(t *testing.T) {
	t.Run("prefers GH_TOKEN", func(t *testing.T) {
		t.Setenv("GH_TOKEN", "gh-token")
		t.Setenv("GITHUB_TOKEN", "github-token")

		withCheckServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if got := r.Header.Get("Authorization"); got != "Bearer gh-token" {
				t.Fatalf("authorization = %q", got)
			}
			if got := r.Header.Get("Accept"); got != "application/vnd.github+json" {
				t.Fatalf("accept = %q", got)
			}
			_, _ = w.Write([]byte(`{"tag_name":"v1.10.7"}`))
		}))

		_ = CheckLatest("1.10.7")
	})

	t.Run("falls back to GITHUB_TOKEN", func(t *testing.T) {
		t.Setenv("GH_TOKEN", "")
		t.Setenv("GITHUB_TOKEN", "github-token")

		withCheckServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if got := r.Header.Get("Authorization"); got != "Bearer github-token" {
				t.Fatalf("authorization = %q", got)
			}
			_, _ = w.Write([]byte(`{"tag_name":"v1.10.7"}`))
		}))

		_ = CheckLatest("1.10.7")
	})

	t.Run("omits authorization header without token", func(t *testing.T) {
		t.Setenv("GH_TOKEN", "")
		t.Setenv("GITHUB_TOKEN", "")

		withCheckServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if got := r.Header.Get("Authorization"); got != "" {
				t.Fatalf("authorization = %q, want empty", got)
			}
			_, _ = w.Write([]byte(`{"tag_name":"v1.10.7"}`))
		}))

		_ = CheckLatest("1.10.7")
	})
}

func TestUpdateInstructions(t *testing.T) {
	msg := updateInstructions()
	if msg == "" {
		t.Fatal("expected non-empty update instructions")
	}
	if runtime.GOOS != "darwin" && !strings.Contains(msg, "github.com/Gentleman-Programming/engram/v2/cmd/engram@latest") {
		t.Fatalf("update instructions = %q, want v2 Go install command", msg)
	}
	if runtime.GOOS != "linux" && !strings.Contains(msg, "https://github.com/Gentleman-Programming/engram/releases/latest") {
		t.Fatalf("update instructions = %q, want canonical GitHub Releases URL", msg)
	}
}

func withCheckServer(t *testing.T, handler http.Handler) {
	t.Helper()

	srv := httptest.NewServer(handler)
	oldURL := githubLatestReleaseURL
	githubLatestReleaseURL = srv.URL
	t.Cleanup(func() {
		githubLatestReleaseURL = oldURL
		srv.Close()
	})
}

func withCheckTimeout(t *testing.T, timeout time.Duration) {
	t.Helper()

	oldTimeout := checkTimeout
	checkTimeout = timeout
	t.Cleanup(func() { checkTimeout = oldTimeout })
}

func TestNonOKStatusMessage(t *testing.T) {
	if got := nonOKStatusMessage(fmt.Sprintf("%d %s", http.StatusUnauthorized, http.StatusText(http.StatusUnauthorized))); !strings.Contains(got, "GH_TOKEN") {
		t.Fatalf("message = %q", got)
	}
}
