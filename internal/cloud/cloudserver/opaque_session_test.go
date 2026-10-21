package cloudserver_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud"
	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

type opaqueAuth struct{}

func (opaqueAuth) Authorize(*http.Request) error { return nil }
func (opaqueAuth) AuthorizeProject(string) error { return nil }
func (opaqueAuth) EnrolledProjects() []string    { return []string{"opaque"} }

func opaqueCloud(t *testing.T) (*cloudstore.CloudStore, cloud.Config) {
	t.Helper()
	dsn := os.Getenv("CLOUDSTORE_TEST_DSN")
	if testing.Short() || dsn == "" {
		t.Skip("requires disposable PostgreSQL via CLOUDSTORE_TEST_DSN")
	}
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		t.Fatal("requires URL-style PostgreSQL DSN")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("opaque_%d", time.Now().UnixNano())
	if _, err = db.Exec("CREATE SCHEMA " + schema); err != nil {
		db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, err := db.Exec("DROP SCHEMA " + schema + " CASCADE")
		db.Close()
		if err != nil {
			t.Error(err)
		}
	})
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	cfg := cloud.Config{DSN: u.String(), MaxPool: 3}
	cs, err := cloudstore.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs, cfg
}
func opaqueJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func opaqueRequest(t *testing.T, h http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var b []byte
	if body != nil {
		b = opaqueJSON(t, body)
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func opaqueLocal(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.New(store.FallbackConfig(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
