package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func compactProjectionFixture(tb testing.TB, size int) *Store {
	tb.Helper()
	cfg, err := DefaultConfig()
	if err != nil {
		tb.Fatal(err)
	}
	cfg.DataDir = tb.TempDir()
	s, err := New(cfg)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = s.Close() })
	_, err = s.db.Exec(`INSERT INTO sessions(id,project,directory) VALUES('compact','alpha','/synthetic');
 WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<40)
 INSERT INTO observations(session_id,project,scope,type,title,content,pinned,created_at)
 SELECT 'compact','alpha','project','decision','title-'||x,?,CASE WHEN x<=20 THEN 1 ELSE 0 END,'2026-05-20' FROM n`, strings.Repeat("x", size))
	if err != nil {
		tb.Fatal(err)
	}
	return s
}

type compactProbeRows struct {
	rowScanner
	bodyBytes *int
	scans     *int
	fields    *int
}

func (r compactProbeRows) Scan(dest ...any) error {
	err := r.rowScanner.Scan(dest...)
	if err == nil {
		*r.scans++
		*r.fields += len(dest)
		if len(dest) == 19 {
			*r.bodyBytes += len(*dest[5].(*string))
		}
	}
	return err
}

func TestCompactContextDoesNotReadBodies(t *testing.T) {
	for _, older := range []bool{false, true} {
		t.Run(fmt.Sprintf("older=%t", older), func(t *testing.T) {
			s := compactProjectionFixture(t, 64*1024)
			s.contextHistoryNow = func() time.Time { return time.Date(2026, 5, 21, 0, 0, 0, 0, time.UTC) }
			if _, err := s.db.Exec(`INSERT INTO observations(session_id,project,scope,type,title,content,created_at) VALUES('compact','alpha','project','decision','old sample','old body','2022-01-01')`); err != nil {
				t.Fatal(err)
			}
			original := s.hooks.queryIt
			scans, bodyBytes, fields := 0, 0, 0
			s.hooks.queryIt = func(db queryer, q string, args ...any) (rowScanner, error) {
				rows, err := original(db, q, args...)
				if err != nil {
					return nil, err
				}
				if strings.Contains(q, "pinned =") {
					return compactProbeRows{rows, &bodyBytes, &scans, &fields}, nil
				}
				return rows, nil
			}
			out, err := s.FormatContextWithOptions("alpha", "project", ContextOptions{Sessions: -1, Prompts: -1, Pinned: 20, Observations: 20, Compact: true, OlderHistory: older, MaxBytes: 16384})
			if err != nil {
				t.Fatal(err)
			}
			if len(out) == 0 || scans != 40 || fields != 80 || bodyBytes != 0 {
				t.Fatalf("output%d rows%d fields%d body_bytes%d; want40 rows80 fields0 body bytes", len(out), scans, fields, bodyBytes)
			}
			if older && !strings.Contains(out, "### Older History") {
				t.Fatal("fixture must retain older history")
			}
		})
	}
}

func TestCompactProjectionKeepsFullPinnedAPI(t *testing.T) {
	s := compactProjectionFixture(t, 64*1024)
	rows, err := s.PinnedObservations("alpha", "project")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 20 {
		t.Fatalf("rows%d", len(rows))
	}
	for _, obs := range rows {
		if obs.ID == 0 || !obs.Pinned || (obs.Project == nil || *obs.Project != "alpha") || obs.SessionID != "compact" || len(obs.Content) != 64*1024 {
			t.Fatalf("public pinned API lost fields: ID%d content%d", obs.ID, len(obs.Content))
		}
	}
}

func TestCompactProjectionOutputCompatibility(t *testing.T) {
	s := compactProjectionFixture(t, 1024)
	now := time.Date(2026, 5, 21, 0, 0, 0, 0, time.UTC)
	s.contextHistoryNow = func() time.Time { return now }
	_, err := s.db.Exec(`UPDATE sessions SET started_at='2026-05-20',summary='session summary 界'; INSERT INTO user_prompts(session_id,project,content,created_at) VALUES('compact','alpha','prompt 🧠','2026-05-20'); UPDATE observations SET title='title: ** 界' WHERE id=1; UPDATE observations SET project='other' WHERE id=2; UPDATE observations SET scope='personal' WHERE id=3; UPDATE observations SET deleted_at='2026-05-20' WHERE id=4; INSERT INTO observations(session_id,project,scope,type,title,content,created_at) VALUES('compact','alpha','project','decision','old sample','old body','2022-01-01')`)
	if err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"", "project", "personal"} {
		for _, older := range []bool{false, true} {
			for _, cap := range []int{-1, 0, 1, 20} {
				opts := ContextOptions{Pinned: cap, Observations: 20, OlderHistory: older}
				full, err := s.FormatContextWithOptions(" ALPHA ", scope, opts)
				if err != nil {
					t.Fatal(err)
				}
				main := strings.SplitN(full, "### Older History\n", 2)[0]
				lines := strings.Split(main, "\n")
				// The reference fetches full observations via Compact=false, then drops
				// only the known fixture previews before applying the existing byte policy.
				for i, line := range lines {
					if strings.HasPrefix(line, "- [") {
						line = strings.TrimSuffix(line, ": "+truncate(strings.Repeat("x", 1024), 300))
						line = strings.TrimSuffix(line, ": old body")
						lines[i] = line
					}
				}
				main = strings.Join(lines, "\n")
				var groups []historyGroup
				if older {
					_, groups, err = s.contextHistory(" ALPHA ", scope, 20, now, false)
					if err != nil {
						t.Fatal(err)
					}
				}
				for _, budget := range []int{-1, 0, 1, 13, 128, 592, 4096, len(main), len(main) + 1} {
					opts.Compact = true
					opts.MaxBytes = budget
					got, err := s.FormatContextWithOptions(" ALPHA ", scope, opts)
					if err != nil {
						t.Fatal(err)
					}
					tail := ""
					if older {
						historyBudget := 2048
						if budget > 0 {
							historyBudget = min(historyBudget, budget/4)
						}
						tail = renderContextHistory(groups, historyBudget)
					}
					want := limitContextBytes(main, positiveContextRemainder(budget, len(tail))) + tail
					if got != want || !utf8.ValidString(got) {
						t.Fatalf("scope%q older%t pinned%d budget%d\ngot%q\nwant%q", scope, older, cap, budget, got, want)
					}
				}
			}
		}
	}
}

func TestCompactProjectionQueryAndScanErrors(t *testing.T) {
	for _, older := range []bool{false, true} {
		for _, kind := range []string{"query", "scan", "rows", "closed"} {
			t.Run(fmt.Sprintf("older%t/%s", older, kind), func(t *testing.T) {
				s := compactProjectionFixture(t, 1024)
				opts := ContextOptions{Sessions: -1, Prompts: -1, Pinned: -1, Observations: 20, Compact: true, OlderHistory: older}
				if kind == "closed" {
					if err := s.Close(); err != nil {
						t.Fatal(err)
					}
				} else {
					original := s.hooks.queryIt
					s.hooks.queryIt = func(db queryer, q string, args ...any) (rowScanner, error) {
						if kind == "query" {
							return nil, errors.New("synthetic context query")
						}
						rows, err := original(db, q, args...)
						if err != nil {
							return nil, err
						}
						return compactFaultRows{rowScanner: rows, kind: kind}, nil
					}
				}
				if _, err := s.FormatContextWithOptions("alpha", "project", opts); err == nil {
					t.Fatal("context read failure hidden")
				}
			})
		}
	}
}

type compactFaultRows struct {
	rowScanner
	kind string
}

func (r compactFaultRows) Scan(dest ...any) error {
	if r.kind == "scan" {
		return errors.New("synthetic context scan")
	}
	return r.rowScanner.Scan(dest...)
}
func (r compactFaultRows) Err() error {
	if r.kind == "rows" {
		return errors.New("synthetic context rows")
	}
	return r.rowScanner.Err()
}

func BenchmarkCompactProjection(b *testing.B) {
	for _, size := range []int{1024, 64 * 1024} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			s := compactProjectionFixture(b, size)
			opts := ContextOptions{Sessions: -1, Prompts: -1, Pinned: 20, Observations: 20, Compact: true, MaxBytes: 16384}
			if _, err := s.FormatContextWithOptions("alpha", "project", opts); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := s.FormatContextWithOptions("alpha", "project", opts); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestCompactContextGenerationInvalidation(t *testing.T) {
	for _, older := range []bool{false, true} {
		for _, pins := range []int{-1, 20} {
			t.Run(fmt.Sprintf("older%t/pinned%d", older, pins), func(t *testing.T) {
				s := compactProjectionFixture(t, 1024)
				original := statFile
				t.Cleanup(func() { statFile = original })
				replacement := filepath.Join(t.TempDir(), "different-identity")
				if err := os.WriteFile(replacement, []byte("owned fixture"), 0600); err != nil {
					t.Fatal(err)
				}
				info, err := os.Stat(replacement)
				if err != nil {
					t.Fatal(err)
				}
				dbPath := filepath.Join(s.DataDir(), "engram.db")
				statFile = func(path string) (os.FileInfo, error) {
					if path == dbPath {
						return info, nil
					}
					return original(path)
				}
				_, err = s.FormatContextWithOptions("alpha", "project", ContextOptions{Sessions: -1, Prompts: -1, Pinned: pins, Observations: 20, Compact: true, OlderHistory: older})
				assertGenerationChanged(t, err)
			})
		}
	}
}
