package dashboard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestLayoutRendersVersionInFooterWhenMountConfigVersionSet is the T2 RED test
// for #1657. When dashboard.MountConfig.Version is set, the Layout component's
// footer must surface that build identifier next to the existing "ENGRAM CLOUD
// / SHARED MEMORY INDEX / LIVE SYNC READY" marker so operators can identify
// the running build from the browser chrome alone.
func TestLayoutRendersVersionInFooterWhenMountConfigVersionSet(t *testing.T) {
	mux := http.NewServeMux()
	Mount(mux, MountConfig{
		RequireSession: func(r *http.Request) error {
			if r.URL.Query().Get("auth") == "ok" {
				return nil
			}
			return errUnauthorized
		},
		IsAdmin:      func(_ *http.Request) bool { return false },
		Store:        parityStoreStub{},
		GetDisplayName: func(_ *http.Request) string { return "operator" },
		Version:      "1.2.3-test",
	})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/?auth=ok", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `v1.2.3-test`) {
		t.Errorf("expected Layout footer to contain %q, body=%q", `v1.2.3-test`, body)
	}
	// The version marker must sit inside the same <small> as the canonical
	// footer copy so the chrome stays one cohesive row instead of a new
	// free-floating label.
	if !strings.Contains(body, "ENGRAM CLOUD / SHARED MEMORY INDEX / LIVE SYNC READY · v1.2.3-test</small>") {
		t.Errorf("expected canonical footer copy + version marker inside one <small>, body=%q", body)
	}
}

// TestLayoutOmitsVersionWhenMountConfigVersionEmpty asserts that the zero
// value of MountConfig.Version keeps the footer byte-identical to the
// pre-#1657 render — empty version = omit rendering. This protects operators
// running an un-versioned binary (e.g. local `go run`) from gaining an empty
// " · v" marker.
func TestLayoutOmitsVersionWhenMountConfigVersionEmpty(t *testing.T) {
	mux := http.NewServeMux()
	Mount(mux, MountConfig{
		RequireSession: func(r *http.Request) error {
			if r.URL.Query().Get("auth") == "ok" {
				return nil
			}
			return errUnauthorized
		},
		IsAdmin:        func(_ *http.Request) bool { return false },
		Store:          parityStoreStub{},
		GetDisplayName: func(_ *http.Request) string { return "operator" },
		// Version intentionally zero — must NOT render a `v` marker.
	})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/?auth=ok", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, " · v") || strings.Contains(body, "·v") {
		t.Errorf("expected empty MountConfig.Version to omit the ` · v` marker, body=%q", body)
	}
	// Existing marker must still be present (this is the regression guard —
	// the version addition must not remove the existing chrome copy).
	if !strings.Contains(body, "ENGRAM CLOUD / SHARED MEMORY INDEX / LIVE SYNC READY") {
		t.Errorf("expected baseline footer copy in body, body=%q", body)
	}
}

// TestLoginPageRendersVersionWhenMountConfigVersionSet is the T3 RED test for
// #1657. Unauthenticated users hitting the login form must also see the
// running build identifier so operators can confirm the build before
// submitting a token.
func TestLoginPageRendersVersionWhenMountConfigVersionSet(t *testing.T) {
	mux := http.NewServeMux()
	Mount(mux, MountConfig{
		RequireSession: func(r *http.Request) error { return errUnauthorized },
		Store:          parityStoreStub{},
		Version:        "1.2.3-test",
	})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/login", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `v1.2.3-test`) {
		t.Errorf("expected LoginPage to contain %q, body=%q", `v1.2.3-test`, body)
	}
}

// TestLoginPageOmitsVersionWhenMountConfigVersionEmpty is the T3 negative
// case. Empty Version must not produce an empty ` · v` marker.
func TestLoginPageOmitsVersionWhenMountConfigVersionEmpty(t *testing.T) {
	mux := http.NewServeMux()
	Mount(mux, MountConfig{
		RequireSession: func(r *http.Request) error { return errUnauthorized },
		Store:          parityStoreStub{},
		// Version intentionally zero.
	})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/login", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, " · v") || strings.Contains(body, "·v") {
		t.Errorf("expected empty MountConfig.Version to omit the ` · v` marker, body=%q", body)
	}
}