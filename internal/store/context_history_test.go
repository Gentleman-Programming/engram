package store

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func historyFixture(t *testing.T) *Store {
	t.Helper()
	s := newTestStore(t)
	if _, err := s.db.Exec(`INSERT INTO sessions(id,project,directory) VALUES('history','alpha','')`); err != nil {
		t.Fatal(err)
	}
	for i, date := range []string{"2022-01-01", "2023-01-01", "2026-01-01", "2099-01-01"} {
		if _, err := s.db.Exec(`INSERT INTO observations(session_id,type,title,content,project,created_at) VALUES('history','decision',?,'body','alpha',?)`, "sample", date); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	return s
}

func historyFormat(t *testing.T, s *Store, opts ContextOptions) string {
	t.Helper()
	got, err := s.FormatContextWithOptions(" ALPHA ", "project", opts)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// Coordinate committed writes between the public formatter's reads. No sleeps
// or scheduler races decide which database state the assertions observe.
func TestOlderHistoryConcurrentSnapshot(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("snapshot", "alpha", ""); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	s.contextHistoryNow = func() time.Time { return now }
	var ids []int64
	for i, title := range []string{"recent-before-write", "sample-before-write", "other-sample"} {
		id, err := s.AddObservation(AddObservationParams{SessionID: "snapshot", Project: "alpha", Scope: "project", Type: "decision", Title: title, Content: title})
		if err != nil {
			t.Fatal(err)
		}
		date := "2026-05-19 12:00:00"
		if i == 0 {
			date = "2026-05-20 12:00:00"
		}
		if _, err := s.db.Exec("UPDATE observations SET created_at=? WHERE id=?", date, id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	// A second store opens an independent connection to the same isolated WAL DB.
	writer, err := New(s.cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	work := make(chan string)
	results := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		for stage := range work {
			switch stage {
			case "recent":
				results <- writer.PinObservation(ids[0])
			case "counts":
				title := "sample-after-write"
				_, err := writer.UpdateObservation(ids[1], UpdateObservationParams{Title: &title})
				results <- err
			default:
				results <- fmt.Errorf("unexpected read stage %q", stage)
			}
		}
	}()
	defer func() { close(work); <-finished }()
	var stages []string
	s.contextHistoryAfterRead = func(stage string) {
		stages = append(stages, stage)
		work <- stage
		select {
		case err := <-results:
			if err != nil {
				t.Fatalf("concurrent write after %s: %v", stage, err)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("concurrent writer did not commit after %s", stage)
		}
	}
	opts := ContextOptions{OlderHistory: true, Observations: 1, Sessions: -1, Prompts: -1, Pinned: -1}
	got := historyFormat(t, s, opts)
	if strings.Join(stages, ",") != "recent,counts" {
		t.Fatalf("snapshot barriers not reached: %v", stages)
	}
	wantSamples := fmt.Sprintf("  - Sample #%d: other-sample\n  - Sample #%d: sample-before-write\n", ids[2], ids[1])
	if !strings.Contains(got, "### Recent Observations\n- [decision] **recent-before-write**") ||
		!strings.Contains(got, "2026-05-18–2026-05-24: 2 observations\n"+wantSamples) ||
		strings.Contains(got, "sample-after-write") {
		t.Fatalf("mixed concurrent snapshots: %s", got)
	}
	// Fresh public reads prove both writes committed before the first call ended.
	s.contextHistoryAfterRead = nil
	pinned, err := writer.GetObservation(ids[0])
	if err != nil || !pinned.Pinned {
		t.Fatalf("pin did not commit: observation=%+v err=%v", pinned, err)
	}
	changed, err := writer.GetObservation(ids[1])
	if err != nil || changed.Title != "sample-after-write" {
		t.Fatalf("title did not commit: observation=%+v err=%v", changed, err)
	}
	fresh := historyFormat(t, s, opts)
	if !strings.Contains(fresh, "### Recent Observations\n- [decision] **other-sample**") ||
		!strings.Contains(fresh, fmt.Sprintf("2026-05-18–2026-05-24: 1 observations\n  - Sample #%d: sample-after-write\n", ids[1])) ||
		strings.Contains(fresh, "recent-before-write") || strings.Contains(fresh, "sample-before-write") {
		t.Fatalf("fresh read did not see committed writes: %s", fresh)
	}
}

func TestOlderHistoryExactAllocation(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("exact", "alpha", ""); err != nil {
		t.Fatal(err)
	}
	for _, date := range []string{"2022-01-01", "2023-01-01", "2099-01-01"} {
		if _, err := s.db.Exec(`INSERT INTO observations(session_id,type,title,content,project,created_at) VALUES('exact','decision','sample','body','alpha',?)`, date); err != nil {
			t.Fatal(err)
		}
	}
	opts := ContextOptions{OlderHistory: true, Observations: 1, Sessions: -1, Prompts: -1, Pinned: -1, MaxBytes: 592}
	got := historyFormat(t, s, opts)
	want := "### Older History\nSamples are titles, not summaries; use mem_get_observation with an ID to drill in.\n- 2023: 1 observations\n- 2022: 1 observations\n\n"
	if !strings.HasSuffix(got, want) {
		t.Fatalf("exact allocation missing: %q", got)
	}
	if len(want) != 148 {
		t.Fatalf("fixture bytes=%d", len(want))
	}
	opts.MaxBytes = 588
	if strings.Contains(historyFormat(t, s, opts), "Older History") {
		t.Fatal("undersized section rendered")
	}
}

func TestOlderHistoryActivation(t *testing.T) {
	s := historyFixture(t)
	opts := ContextOptions{Sessions: -1, Prompts: -1, Pinned: -1, Observations: 1}
	legacy := historyFormat(t, s, opts)
	if strings.Contains(legacy, "Older History") {
		t.Fatal(legacy)
	}
	opts.OlderHistory = true
	got := historyFormat(t, s, opts)
	if !strings.Contains(got, "### Older History\n") || !strings.Contains(got, "2022: 1") {
		t.Fatalf("missing overview: %s", got)
	}
	if !strings.Contains(got, legacy) {
		t.Fatalf("legacy changed: %s", got)
	}
	opts.MaxBytes = 40
	got = historyFormat(t, s, opts)
	if len(got) > 40 || !utf8.ValidString(got) || strings.Contains(got, "Older History") {
		t.Fatalf("tiny budget: %q", got)
	}
}

func TestOlderHistoryPublicTotalBudgetAndActualReservation(t *testing.T) {
	s := historyFixture(t)
	// Overflow recent output at both budgets without depending on preview length.
	if _, err := s.db.Exec(`UPDATE observations SET title=? WHERE created_at='2099-01-01'`, strings.Repeat("界", 4000)); err != nil {
		t.Fatal(err)
	}
	opts := ContextOptions{Sessions: -1, Prompts: -1, Pinned: -1, Observations: 1, OlderHistory: true}
	for _, budget := range []int{1024, 8192} {
		t.Run(fmt.Sprint(budget), func(t *testing.T) {
			opts.MaxBytes = budget
			got := historyFormat(t, s, opts)
			index := strings.Index(got, "### Older History\n")
			if index < 0 {
				t.Fatalf("missing older section: %q", got)
			}
			older := got[index:]
			if len(got) > budget || len(older) > min(2048, budget/4) || !utf8.ValidString(got) {
				t.Fatalf("budget=%d total=%d older=%d", budget, len(got), len(older))
			}
			// Compare public disabled rendering with exactly the bytes actually reserved.
			disabled := opts
			disabled.OlderHistory = false
			disabled.MaxBytes = budget - len(older)
			if got[:index] != historyFormat(t, s, disabled) {
				t.Fatal("reserved allocation instead of actual rendered bytes")
			}
			if len(older) >= min(2048, budget/4) {
				t.Fatal("fixture must leave unused older allocation")
			}
			if !strings.HasSuffix(older, "\n\n") || !strings.Contains(older, "2022: 1") {
				t.Fatal(older)
			}
		})
	}
}

func TestOlderHistoryPublicDisabledDefaults(t *testing.T) {
	s := historyFixture(t)
	legacy, err := s.FormatContext("alpha", "project")
	if err != nil {
		t.Fatal(err)
	}
	if legacy != historyFormat(t, s, ContextOptions{}) {
		t.Fatal("zero-value default changed")
	}
	opts := ContextOptions{Sessions: -1, Prompts: -1, Pinned: -1, Observations: 1, Compact: true}
	want := "## Memory from Previous Sessions\n\n### Recent Observations\n- [decision] **sample**\n\n"
	if got := historyFormat(t, s, opts); got != want {
		t.Fatalf("disabled contract: %q", got)
	}
	opts.MaxBytes = 45
	if got := historyFormat(t, s, opts); got != limitContextBytes(want, 45) {
		t.Fatalf("disabled byte contract: %q", got)
	}
}

func TestOlderHistoryPublicNegativeAndFilters(t *testing.T) {
	s := historyFixture(t)
	s.cfg.MaxContextResults = 1
	// All excluded rows are older than the recent window, so a leak changes counts.
	for _, suffix := range []string{`,'beta','project',0,NULL`, `,'alpha','personal',0,NULL`, `,'alpha','project',1,NULL`, `,'alpha','project',0,'2023-02-01'`} {
		if _, err := s.db.Exec(`INSERT INTO observations(session_id,type,title,content,created_at,project,scope,pinned,deleted_at) VALUES('history','decision','excluded','body','2022-01-01'` + suffix + `)`); err != nil {
			t.Fatal(err)
		}
	}
	opts := ContextOptions{OlderHistory: true, Observations: -1, Sessions: -1, Prompts: -1, Pinned: -1}
	got := historyFormat(t, s, opts)
	for _, want := range []string{"2022: 1 observations", "2023: 1 observations"} {
		if !strings.Contains(got, want) {
			t.Fatal(got)
		}
	}
	if strings.Contains(got, "Recent Observations") || strings.Contains(got, "2099") || strings.Contains(got, "excluded") {
		t.Fatal(got)
	}
	opts.Observations = 1
	visible := historyFormat(t, s, opts)
	if visible[strings.Index(visible, "### Older History"):] != got[strings.Index(got, "### Older History"):] {
		t.Fatal("hidden section reclassified recent membership")
	}
}

func TestContextHistoryCalendarAndMembership(t *testing.T) {
	s := historyFixture(t)
	dates := []string{"2026-05-01", "2026-05-03", "2026-05-04", "2026-05-18", "2026-05-18", "2026-05-18", "2026-05-18", "2026-04-01", "2026-03-31"}
	for i, date := range dates {
		_, err := s.db.Exec(`INSERT INTO observations(session_id,type,title,content,project,created_at) VALUES('history','bugfix',?,'secret body','alpha',?)`, fmt.Sprintf("title-%d", i), date)
		if err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	recent, groups, err := s.contextHistory(" ALPHA ", "project", 1, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 1 || recent[0].CreatedAt != "2099-01-01" {
		t.Fatalf("recent: %+v", recent)
	}
	got := renderContextHistory(groups, 2048)
	for _, want := range []string{"2026-05-18–2026-05-24: 4", "2026-05-01–2026-05-03: 2", "2026-04: 1 observations (bugfix: 1)", "2026 Q1: 2", "2023: 1", "Sample #"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, "secret body") || strings.Contains(got, "title-3") || strings.Contains(got, "title-0") {
		t.Fatal(got)
	}
	if strings.Index(got, "title-6") >= strings.Index(got, "title-5") || strings.Index(got, "title-5") >= strings.Index(got, "title-4") {
		t.Fatal(got)
	}
}

func TestOlderHistoryCompleteLinesAndUTF8(t *testing.T) {
	groups := []historyGroup{{label: "2025", count: 12}, {label: "2024", count: 10, samples: []string{"  - Sample #1: " + strings.Repeat("界", 1000) + "\n"}}, {label: "2023", count: 9}}
	for budget := 0; budget <= 2048; budget++ {
		got := renderContextHistory(groups, budget)
		if len(got) > budget || !utf8.ValidString(got) {
			t.Fatalf("budget %d: %q", budget, got)
		}
		if got != "" && (!strings.HasSuffix(got, "\n\n") || !strings.Contains(got, "2025: 12")) {
			t.Fatalf("partial section: %q", got)
		}
	}
	got := renderContextHistory(groups, 250)
	if !strings.Contains(got, "[older groups omitted]") {
		t.Fatal(got)
	}
}

func TestOlderHistoryPublicCalendarSamples(t *testing.T) {
	s := newTestStore(t)
	calls := 0
	s.contextHistoryNow = func() time.Time { calls++; return time.Date(2026, 5, 20, 14, 0, 0, 0, time.FixedZone("offset", 7200)) }
	if err := s.CreateSession("calendar", "alpha", ""); err != nil {
		t.Fatal(err)
	}
	dates := []string{"2025-12-31T23:59:59Z", "2026-01-01T00:00:00Z", "2026-03-31T23:59:59Z", "2026-04-01T00:00:00Z", "2026-04-30T23:59:59Z", "2026-05-01T02:00:00+02:00", "2026-05-03T23:59:59Z", "2026-05-04T00:00:00Z", "2026-05-18T00:00:00Z", "2026-05-18T00:00:00Z", "2026-05-18T00:00:00Z", "2026-05-18T00:00:00Z", "2026-05-20T12:00:00Z", "2026-05-20T12:00:00Z"}
	ids := make([]int64, len(dates))
	for i, date := range dates {
		kind := "decision"
		if i == 4 {
			kind = "bugfix"
		}
		r, err := s.db.Exec(`INSERT INTO observations(session_id,type,title,content,project,created_at) VALUES('calendar',?,?,'hidden body','alpha',?)`, kind, fmt.Sprintf("title-%d", i), date)
		if err != nil {
			t.Fatal(err)
		}
		ids[i], err = r.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
	}
	opts := ContextOptions{OlderHistory: true, Observations: 1, Sessions: -1, Prompts: -1, Pinned: -1}
	got := historyFormat(t, s, opts)
	if calls != 1 {
		t.Fatalf("clock calls=%d", calls)
	}
	history := got[strings.Index(got, "### Older History"):]
	for _, want := range []string{"2026-05-18–2026-05-24: 5 observations", "2026-05-04–2026-05-10: 1 observations", "2026-05-01–2026-05-03: 2 observations", "2026-04: 2 observations (bugfix: 1, decision: 1)", "2026 Q1: 2 observations", "2025: 1 observations"} {
		if !strings.Contains(history, want) {
			t.Fatalf("missing %q: %s", want, history)
		}
	}
	for _, i := range []int{12, 11, 10, 7, 6} {
		want := fmt.Sprintf("  - Sample #%d: title-%d\n", ids[i], i)
		if !strings.Contains(history, want) {
			t.Fatalf("missing %q: %s", want, history)
		}
	}
	if strings.Count(history, "  - Sample #") != 5 {
		t.Fatal(history)
	}
	for _, i := range []int{13, 9, 8, 5} {
		if strings.Contains(history, fmt.Sprintf("Sample #%d:", ids[i])) {
			t.Fatal(history)
		}
	}
	if strings.Index(history, "title-12") > strings.Index(history, "title-11") || strings.Index(history, "title-11") > strings.Index(history, "title-10") {
		t.Fatal(history)
	}
	s.cfg.MaxContextResults = 1
	opts.Observations = -1
	hidden := historyFormat(t, s, opts)
	if strings.Contains(hidden, "Recent Observations") || !strings.HasSuffix(hidden, history) {
		t.Fatalf("negative recent reclassified tied this-year row: %s", hidden)
	}
}

func TestOlderHistoryPublicCalendarMidnight(t *testing.T) {
	for _, tc := range []struct{ now, date, want string }{
		{"2026-01-01T00:00:00Z", "2025-12-31T23:59:59Z", "2025: 1 observations"},
		{"2026-04-01T00:00:00Z", "2026-03-31T23:59:59Z", "2026 Q1: 1 observations"},
		{"2026-05-01T00:00:00Z", "2026-04-30T23:59:59Z", "2026-04: 1 observations (decision: 1)"},
		{"2026-05-04T00:00:00Z", "2026-05-03T23:59:59Z", "2026-05-01–2026-05-03: 1 observations"},
		{"2026-05-01T01:00:00+02:00", "2026-04-30T23:00:00Z", "2026-04-27–2026-04-30: 1 observations"},
	} {
		t.Run(tc.now, func(t *testing.T) {
			s := newTestStore(t)
			now, err := time.Parse(time.RFC3339, tc.now)
			if err != nil {
				t.Fatal(err)
			}
			s.contextHistoryNow = func() time.Time { return now }
			if err := s.CreateSession("midnight", "alpha", ""); err != nil {
				t.Fatal(err)
			}
			for _, date := range []string{tc.date, "2099-01-01"} {
				if _, err := s.db.Exec(`INSERT INTO observations(session_id,type,title,content,project,created_at) VALUES('midnight','decision','title','body','alpha',?)`, date); err != nil {
					t.Fatal(err)
				}
			}
			got := historyFormat(t, s, ContextOptions{OlderHistory: true, Observations: 1, Sessions: -1, Prompts: -1, Pinned: -1})
			if !strings.Contains(got, tc.want) {
				t.Fatal(got)
			}
		})
	}
}

func TestOlderHistoryPublicSaturationAndLines(t *testing.T) {
	s := newTestStore(t)
	s.contextHistoryNow = func() time.Time { return time.Date(2026, 5, 20, 0, 0, 0, 0, time.UTC) }
	if err := s.CreateSession("lines", "alpha", ""); err != nil {
		t.Fatal(err)
	}
	for year := 1900; year <= 2025; year++ {
		if _, err := s.db.Exec(`INSERT INTO observations(session_id,type,title,content,project,created_at) VALUES('lines','decision','title','body','alpha',?)`, fmt.Sprintf("%d-01-01", year)); err != nil {
			t.Fatal(err)
		}
	}
	for _, date := range []string{"2026-05-18", "2099-01-01"} {
		if _, err := s.db.Exec(`INSERT INTO observations(session_id,type,title,content,project,created_at) VALUES('lines','decision',?,'body','alpha',?)`, strings.Repeat("界", 1000), date); err != nil {
			t.Fatal(err)
		}
	}
	opts := ContextOptions{OlderHistory: true, Observations: 1, Sessions: -1, Prompts: -1, Pinned: -1}
	for _, budget := range []int{0, 1024, 8192} {
		opts.MaxBytes = budget
		got := historyFormat(t, s, opts)
		index := strings.Index(got, "### Older History")
		if index < 0 {
			t.Fatal(got)
		}
		section := got[index:]
		cap := 2048
		if budget > 0 {
			cap = min(cap, budget/4)
			if len(got) > budget {
				t.Fatal(len(got))
			}
		}
		if len(section) > cap || !utf8.ValidString(got) || !strings.HasSuffix(section, "\n\n") || !strings.Contains(section, "[older groups omitted]") || strings.Contains(section, "Sample #") {
			t.Fatalf("budget=%d: %q", budget, section)
		}
		if budget == 0 && len(got) <= 2048 {
			t.Fatal("legacy unbounded section was capped")
		}
	}
}

func TestOlderHistoryPublicUnboundedCap(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("cap", "alpha", ""); err != nil {
		t.Fatal(err)
	}
	for year := 1800; year <= 2025; year++ {
		if _, err := s.db.Exec(`INSERT INTO observations(session_id,type,title,content,project,created_at) VALUES('cap','decision','title','body','alpha',?)`, fmt.Sprintf("%d-01-01", year)); err != nil {
			t.Fatal(err)
		}
	}
	opts := ContextOptions{OlderHistory: true, Observations: 1, Sessions: -1, Prompts: -1, Pinned: -1}
	unbounded := historyFormat(t, s, opts)
	opts.MaxBytes = 8192
	bounded := historyFormat(t, s, opts)
	a := unbounded[strings.Index(unbounded, "### Older History"):]
	b := bounded[strings.Index(bounded, "### Older History"):]
	if a != b || len(a) > 2048 || len(a) < 2000 || !strings.Contains(a, "[older groups omitted]") {
		t.Fatalf("cap bytes=%d: %q", len(a), a)
	}
}

func TestContextHistoryQueryPlan(t *testing.T) {
	s := historyFixture(t)
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	base := historyBaseSQL("deleted_at IS NULL AND pinned = 0 AND LOWER(project) = ? AND scope = ?")
	args := []any{"alpha", "project", 20, "alpha", "project"}
	aggregate, aggregateArgs := historyAggregateSQL(base, args, historyPeriods(now), now)
	sampleArgs := append(append([]any{}, args...), "2026-05-18T00:00:00Z", "2026-05-25T00:00:00Z", 3)
	for _, query := range []struct {
		name, sql string
		args      []any
	}{{"aggregate", aggregate, aggregateArgs}, {"sample", historySampleSQL(base), sampleArgs}} {
		t.Run(query.name, func(t *testing.T) {
			t.Logf("production SQL: %s; args: %v", query.sql, query.args)
			rows, err := s.db.Query("EXPLAIN QUERY PLAN "+query.sql, query.args...)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = rows.Close() }()
			for rows.Next() {
				var id, parent, unused int
				var detail string
				if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
					t.Fatal(err)
				}
				t.Logf("%d parent=%d: %s", id, parent, detail)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func BenchmarkOlderHistoryCurrentMonth(b *testing.B) {
	cfg, err := DefaultConfig()
	if err != nil {
		b.Fatal(err)
	}
	cfg.DataDir = b.TempDir()
	s, err := New(cfg)
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	s.contextHistoryNow = func() time.Time { return time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC) }
	_, err = s.db.Exec(`INSERT INTO sessions(id,project,directory) VALUES('bench','alpha',''); INSERT INTO observations(session_id,type,title,content,project,created_at) WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<100000) SELECT 'bench',CASE WHEN x%2=0 THEN 'decision' ELSE 'bugfix' END,'sample',printf('%01000d',x),'alpha',CASE WHEN x<=80000 THEN datetime('2020-01-01',printf('+%d minutes',x)) ELSE datetime('2026-05-01',printf('+%d minutes',((x-80001)*2)%28000)) END FROM n`)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.FormatContextWithOptions("alpha", "", ContextOptions{OlderHistory: true, MaxBytes: 8192, Sessions: -1, Prompts: -1}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkOlderHistoryLarge(b *testing.B) {
	cfg, err := DefaultConfig()
	if err != nil {
		b.Fatal(err)
	}
	cfg.DataDir = b.TempDir()
	s, err := New(cfg)
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	_, err = s.db.Exec(`INSERT INTO sessions(id,project,directory) VALUES('bench','alpha',''); INSERT INTO observations(session_id,type,title,content,project,created_at) WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<100000) SELECT 'bench','decision','sample',printf('%01000d',x),'alpha',datetime('2020-01-01',printf('+%d minutes',x)) FROM n`)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.FormatContextWithOptions("alpha", "", ContextOptions{OlderHistory: true, MaxBytes: 8192, Sessions: -1, Prompts: -1}); err != nil {
			b.Fatal(err)
		}
	}
}
