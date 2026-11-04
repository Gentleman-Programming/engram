package store

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func contextBudgetFixture(tb testing.TB, count int) *Store {
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
	_, err = s.db.Exec(`INSERT INTO sessions(id,project,directory) VALUES('budget','alpha','/synthetic');
 WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<?)
 INSERT INTO observations(sync_id,session_id,type,title,content,project,scope,normalized_hash,pinned)
 SELECT 'budget-'||x,'budget','decision','synthetic title '||x,printf('%0300d',x),'alpha','project','hash-'||x,1 FROM n`, count)
	if err != nil {
		tb.Fatal(err)
	}
	return s
}

type budgetCountingRows struct {
	rowScanner
	scans *int
}

func (r budgetCountingRows) Scan(dest ...any) error { *r.scans++; return r.rowScanner.Scan(dest...) }

func TestFormatContextByteBudgetBoundsPinnedWork(t *testing.T) {
	for _, count := range []int{100, 1000, 5000} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			s := contextBudgetFixture(t, count)
			original := s.hooks.queryIt
			scans := 0
			s.hooks.queryIt = func(db queryer, query string, args ...any) (rowScanner, error) {
				rows, err := original(db, query, args...)
				if err != nil {
					return nil, err
				}
				if strings.Contains(query, "o.pinned = 1") {
					return budgetCountingRows{rows, &scans}, nil
				}
				return rows, nil
			}
			out, err := s.FormatContextWithOptions("alpha", "project", ContextOptions{MaxBytes: 4096, Sessions: -1, Prompts: -1, Observations: -1})
			if err != nil {
				t.Fatal(err)
			}
			if len(out) != 4096 {
				t.Fatalf("bytes=%d", len(out))
			}
			if scans > 410 {
				t.Fatalf("hydrated=%d rows for4096 bytes; want at most410", scans)
			}
			t.Logf("pinned=%d hydrated=%d output=%d", count, scans, len(out))
		})
	}
}

func BenchmarkContextBudgetPinned(b *testing.B) {
	for _, count := range []int{100, 1000, 5000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			s := contextBudgetFixture(b, count)
			opts := ContextOptions{MaxBytes: 4096, Sessions: -1, Prompts: -1, Observations: -1}
			if _, err := s.FormatContextWithOptions("alpha", "project", opts); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				out, err := s.FormatContextWithOptions("alpha", "project", opts)
				if err != nil || len(out) != 4096 {
					b.Fatalf("bytes=%d error=%v", len(out), err)
				}
			}
		})
	}
}

func TestFormatContextByteBudgetPreservesPrefix(t *testing.T) {
	s := contextBudgetFixture(t, 30)
	// Equal timestamps test the ID tie-breaker; blank legacy fields exercise the
	// smallest possible bullet. Other rows cover UTF-8 and multiline content.
	_, err := s.db.Exec(`UPDATE observations SET created_at='2026-01-01', title=CASE WHEN id%3=0 THEN '' ELSE '界🧠 título' END, type=CASE WHEN id%3=0 THEN '' ELSE type END, content='línea uno
segunda línea'; UPDATE observations SET deleted_at='2026-01-02' WHERE id=2; UPDATE observations SET scope='personal' WHERE id=3; UPDATE observations SET project='other' WHERE id=4`)
	if err != nil {
		t.Fatal(err)
	}
	for _, compact := range []bool{false, true} {
		for _, pinned := range []int{-1, 0, 1, 20, 100} {
			for _, scope := range []string{"", "project", "personal"} {
				opts := ContextOptions{Sessions: -1, Prompts: -1, Observations: -1, Pinned: pinned, Compact: compact}
				full, err := s.FormatContextWithOptions(" ALPHA ", scope, opts)
				if err != nil {
					t.Fatal(err)
				}
				for _, budget := range []int{-1, 0, 1, 12, 13, 14, 32, 64, 128, 4096, len(full) - 1, len(full), len(full) + 1, int(^uint(0) >> 1)} {
					opts.MaxBytes = budget
					got, err := s.FormatContextWithOptions(" ALPHA ", scope, opts)
					if err != nil {
						t.Fatal(err)
					}
					want := limitContextBytes(full, budget)
					if got != want || !utf8.ValidString(got) {
						t.Fatalf("compact=%t pinned=%d scope=%q budget=%d\ngot %q\nwant%q", compact, pinned, scope, budget, got, want)
					}
				}
			}
		}
	}
}

func TestFormatContextByteBudgetKeepsQueryErrors(t *testing.T) {
	s := contextBudgetFixture(t, 5)
	original := s.hooks.queryIt
	s.hooks.queryIt = func(db queryer, query string, args ...any) (rowScanner, error) {
		if strings.Contains(query, "FROM user_prompts") {
			return nil, errors.New("synthetic prompt error")
		}
		return original(db, query, args...)
	}
	_, err := s.FormatContextWithOptions("alpha", "", ContextOptions{MaxBytes: 1, Sessions: -1, Observations: -1})
	if err == nil || !strings.Contains(err.Error(), "synthetic prompt error") {
		t.Fatalf("later section error hidden by byte budget: %v", err)
	}
}

func TestFormatContextByteBudgetMinimalBullets(t *testing.T) {
	s := contextBudgetFixture(t, 500)
	if _, err := s.db.Exec(`UPDATE observations SET title='',type='',content=''`); err != nil {
		t.Fatal(err)
	}
	opts := ContextOptions{Sessions: -1, Prompts: -1, Observations: -1, Compact: true}
	full, err := s.FormatContextWithOptions("alpha", "", opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, budget := range []int{1, 10, 11, 45, 46, 47, 4095, 4096, 4097, len(full) - 1, len(full), len(full) + 1} {
		opts.MaxBytes = budget
		got, err := s.FormatContextWithOptions("alpha", "", opts)
		if err != nil {
			t.Fatal(err)
		}
		if want := limitContextBytes(full, budget); got != want {
			t.Fatalf("budget%d: got%q want%q", budget, got, want)
		}
	}
}

// Every section is populated: a budget can stop in any loop, while older
// history reserves its actual rendered bytes before clipping the main block.
func TestFormatContextByteBudgetMixedSections(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	s.contextHistoryNow = func() time.Time { return now }
	for i := 0; i < 3; i++ {
		session := fmt.Sprintf("mixed-%d", i)
		date := fmt.Sprintf("2026-05-20 10:0%d:00", i)
		_, err := s.db.Exec(`INSERT INTO sessions(id,project,directory,started_at,summary) VALUES(?,'alpha','/synthetic',?,?)`, session, date, strings.Repeat("session 界 summary ", 15))
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.db.Exec(`INSERT INTO user_prompts(session_id,project,content,created_at) VALUES(?,'alpha',?,?)`, session, strings.Repeat("prompt 🧠 content ", 20), date)
		if err != nil {
			t.Fatal(err)
		}
		for _, pinned := range []int{0, 1} {
			_, err = s.db.Exec(`INSERT INTO observations(session_id,project,scope,type,title,content,pinned,created_at) VALUES(?,'alpha','project','decision',?,?,?,?)`, session, fmt.Sprintf("memory-%d-%d 界", pinned, i), strings.Repeat("body 🧠 line\n", 40), pinned, date)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, date := range []string{"2022-01-01", "2023-01-01", "2024-01-01"} {
		if _, err := s.db.Exec(`INSERT INTO observations(session_id,project,scope,type,title,content,created_at) VALUES('mixed-0','alpha','project','decision','historical 界','old body',?)`, date); err != nil {
			t.Fatal(err)
		}
	}
	_, groups, err := s.contextHistory("alpha", "project", 3, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) == 0 {
		t.Fatal("fixture must include older history")
	}
	for _, compact := range []bool{false, true} {
		for _, olderHistory := range []bool{false, true} {
			t.Run(fmt.Sprintf("compact=%t/older=%t", compact, olderHistory), func(t *testing.T) {
				opts := ContextOptions{Sessions: 3, Prompts: 3, Observations: 3, Compact: compact, OlderHistory: olderHistory}
				full, err := s.FormatContextWithOptions("alpha", "project", opts)
				if err != nil {
					t.Fatal(err)
				}
				main := full
				if olderHistory {
					index := strings.Index(full, "### Older History\n")
					if index < 0 {
						t.Fatal("fixture must render older history")
					}
					main = full[:index]
				}
				cases := map[string]int{"tiny": 1, "marker": len(contextTruncationMarker), "exact-total": len(full), "above-total": len(full) + 1, "unbounded-zero": 0, "unbounded-negative": -1}
				headings := []string{"### Recent Sessions\n", "### Recent User Prompts\n", "### Pinned\n", "### Recent Observations\n"}
				for i, heading := range headings {
					start := strings.Index(main, heading)
					if start < 0 {
						t.Fatalf("missing fixture section%q", heading)
					}
					end := len(main)
					if i+1 < len(headings) {
						end = strings.Index(main, headings[i+1])
					}
					cases[fmt.Sprintf("section%d-inside", i)] = start + len(heading) + 1
					cases[fmt.Sprintf("section%d-end", i)] = end
					cases[fmt.Sprintf("section%d-before-end", i)] = end - 1
					cases[fmt.Sprintf("section%d-after-end", i)] = end + 1
				}
				if olderHistory {
					// Pick total budgets whose main-block remainder lands exactly
					// inside/on section boundaries after actual history reservation.
					for i, heading := range headings {
						start := strings.Index(main, heading) + len(heading) + 1
						end := len(main)
						if i+1 < len(headings) {
							end = strings.Index(main, headings[i+1])
						}
						for name, cut := range map[string]int{"inside": start, "end": end} {
							found := false
							for extra := 0; extra <= 2048; extra++ {
								budget := cut + extra
								tail := renderContextHistory(groups, min(2048, budget/4))
								if budget-len(tail) == cut {
									cases[fmt.Sprintf("section%d-%s-after-reservation", i, name)] = budget
									found = true
									break
								}
							}
							if !found {
								t.Fatalf("no budget for main cut %d", cut)
							}
						}
					}
				}
				for name, budget := range cases {
					t.Run(name, func(t *testing.T) {
						opts.MaxBytes = budget
						got, err := s.FormatContextWithOptions("alpha", "project", opts)
						if err != nil {
							t.Fatal(err)
						}
						want := full
						if budget > 0 {
							tail := ""
							if olderHistory {
								tail = renderContextHistory(groups, min(2048, budget/4))
							}
							want = limitContextBytes(main, positiveContextRemainder(budget, len(tail))) + tail
						}
						if got != want || !utf8.ValidString(got) {
							t.Fatalf("budget%d\ngot%q\nwant%q", budget, got, want)
						}
					})
				}
			})
		}
	}
}
