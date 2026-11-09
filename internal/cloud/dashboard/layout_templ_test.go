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
		IsAdmin:        func(_ *http.Request) bool { return false },
		Store:          parityStoreStub{},
		GetDisplayName: func(_ *http.Request) string { return "operator" },
		Version:        "v1.2.3-test",
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
	// MountConfig.Version is already display-ready (see the Version field doc),
	// so the chrome must render it verbatim instead of prefixing it a second
	// time. Regression guard for the "vv1.2.3-test" double-prefix bug.
	if got := strings.Count(body, `v1.2.3-test`); got != 1 {
		t.Errorf("expected exactly one %q marker, got %d, body=%q", `v1.2.3-test`, got, body)
	}
	// The version marker must sit inside the same <small> as the canonical
	// footer copy so the chrome stays one cohesive row instead of a new
	// free-floating label.
	if !strings.Contains(body, "ENGRAM CLOUD / SHARED MEMORY INDEX / LIVE SYNC READY · v1.2.3-test</small>") {
		t.Errorf("expected canonical footer copy + version marker inside one <small>, body=%q", body)
	}
}

// An unset build identifier must be explicit in the dashboard footer.
func TestLayoutShowsUnknownWhenMountConfigVersionEmpty(t *testing.T) {
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
		// Version intentionally zero.
	})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/?auth=ok", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "<small>ENGRAM CLOUD / SHARED MEMORY INDEX / LIVE SYNC READY · version unknown</small>") {
		t.Errorf("expected full footer with unknown version, body=%q", body)
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
		Version:        "v1.2.3-test",
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
	if got := strings.Count(body, `v1.2.3-test`); got != 1 {
		t.Errorf("expected exactly one %q marker, got %d, body=%q", `v1.2.3-test`, got, body)
	}
}

// TestChromeRendersNonSemverVersionAsIs is the regression guard for the
// CodeRabbit finding on #1658. Both chrome surfaces used to prepend "v" to
// every non-empty identifier, so a legitimate local build reported itself as
// "vdev". Issue #1657 requires "dev" to render as-is.
func TestChromeRendersNonSemverVersionAsIs(t *testing.T) {
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
		Version:        "dev",
	})

	tests := []struct {
		name string
		path string
	}{
		{name: "layout footer", path: "/dashboard/?auth=ok"},
		{name: "login footer", path: "/dashboard/login"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
			}
			body := rec.Body.String()
			if !strings.Contains(body, "LIVE SYNC READY · dev</small>") {
				t.Errorf("expected footer to render the version as-is, body=%q", body)
			}
			if strings.Contains(body, "vdev") {
				t.Errorf("expected no duplicated v prefix on a non-semver identifier, body=%q", body)
			}
		})
	}
}

func TestChromeErrorPathsPreserveVersion(t *testing.T) {
	for _, version := range []string{"v1.2.3-rc.1+build.7", ""} {
		t.Run("version="+version, func(t *testing.T) {
			for _, login := range []bool{true, false} {
				name := "session not found"
				if login {
					name = "invalid login token"
				}
				t.Run(name, func(t *testing.T) {
					validated, created := 0, 0
					mux := http.NewServeMux()
					Mount(mux, MountConfig{
						RequireSession: func(r *http.Request) error {
							if r.URL.Query().Get("auth") == "ok" {
								return nil
							}
							return errUnauthorized
						},
						IsAdmin: func(_ *http.Request) bool { return false },
						Store:   parityStoreStub{},
						Version: version,
						ValidateLoginToken: func(token string) error {
							validated++
							if token != "wrong" {
								t.Errorf("validated token = %q, want wrong", token)
							}
							return errUnauthorized
						},
						CreateSessionCookie: func(http.ResponseWriter, *http.Request, string) error {
							created++
							return nil
						},
					})
					req := httptest.NewRequest(http.MethodGet, "/dashboard/browser/sessions/%20?auth=ok", nil)
					status, content := http.StatusNotFound, "Session Not Found"
					if login {
						req = httptest.NewRequest(http.MethodPost, "/dashboard/login", strings.NewReader("token=wrong"))
						req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
						status, content = http.StatusOK, "invalid token"
					}
					rec := httptest.NewRecorder()
					mux.ServeHTTP(rec, req)
					body := rec.Body.String()
					if rec.Code != status || !strings.Contains(body, content) {
						t.Fatalf("status=%d, want %d with %q; body=%q", rec.Code, status, content, body)
					}
					label := version
					if label == "" {
						label = "version unknown"
					}
					footer := "<small>ENGRAM CLOUD / SHARED MEMORY INDEX / LIVE SYNC READY · " + label + "</small>"
					if strings.Count(body, footer) != 1 || strings.Count(body, " · "+label+"</small>") != 1 {
						t.Errorf("expected exactly one canonical footer %q; body=%q", footer, body)
					}
					if created != 0 || len(rec.Header().Values("Set-Cookie")) != 0 || rec.Header().Get("Location") != "" {
						t.Errorf("unexpected session creation or redirect: created=%d headers=%v", created, rec.Header())
					}
					if login && validated != 1 {
						t.Errorf("token validation calls=%d, want 1", validated)
					}
				})
			}
		})
	}
}

// The login footer must remain visible even without a build identifier.
func TestLoginPageShowsUnknownWhenMountConfigVersionEmpty(t *testing.T) {
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
	if !strings.Contains(body, "<small>ENGRAM CLOUD / SHARED MEMORY INDEX / LIVE SYNC READY · version unknown</small>") {
		t.Errorf("expected full footer with unknown version, body=%q", body)
	}
}
