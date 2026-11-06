package server

import (
	"errors"
	"fmt"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
	"net/http/httptest"
	"testing"
)

func TestHealthDoesNotCalculateStatistics(t *testing.T) {
	st := newServerTestStore(t)
	original := loadServerStats
	t.Cleanup(func() { loadServerStats = original })
	calls := 0
	loadServerStats = func(*store.Store) (*store.Stats, error) {
		calls++
		return nil, errors.New("statistics must not run for health")
	}
	rec := httptest.NewRecorder()
	New(st, 0).Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/health", nil))
	if rec.Code != 200 || calls != 0 {
		t.Fatalf("health status=%d statistics calls=%d body=%s", rec.Code, calls, rec.Body.String())
	}
}

// The baseline uses the old Stats probe through the same real handler. Each
// variant keeps the response encoding, middleware and generation fence intact.
func BenchmarkHealthReadiness(b *testing.B) {
	for _, projects := range []int{10, 500} {
		b.Run(fmt.Sprint(projects), func(b *testing.B) {
			cfg, err := store.DefaultConfig()
			if err != nil {
				b.Fatal(err)
			}
			cfg.DataDir = b.TempDir()
			st, err := store.New(cfg)
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { _ = st.Close() })
			_, err = st.DB().Exec(`INSERT INTO sessions(id,project,directory) VALUES('health','audit','/synthetic');
 WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<5000)
 INSERT INTO observations(sync_id,session_id,type,title,content,project,scope,normalized_hash)
 SELECT 'health-'||x,'health','decision','synthetic '||x,'content',printf('project-%d',x%?),'project','hash-'||x FROM n`, projects)
			if err != nil {
				b.Fatal(err)
			}
			srv := New(st, 0)
			req := httptest.NewRequest("GET", "/health", nil)
			original := checkServerReadiness
			b.Cleanup(func() { checkServerReadiness = original })
			for _, tc := range []struct {
				name  string
				probe func(*store.Store) error
			}{
				{"stats-baseline", func(st *store.Store) error { _, err := st.Stats(); return err }},
				{"bounded", (*store.Store).CheckReadiness},
			} {
				b.Run(tc.name, func(b *testing.B) {
					checkServerReadiness = tc.probe
					run := func() {
						rec := httptest.NewRecorder()
						srv.Handler().ServeHTTP(rec, req)
						if rec.Code != 200 {
							b.Fatalf("health%d: %s", rec.Code, rec.Body.String())
						}
					}
					run()
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						run()
					}
				})
			}
		})
	}
}
