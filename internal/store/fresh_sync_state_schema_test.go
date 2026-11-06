package store

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
)

type syncStateSchemaRows struct {
	rowScanner
	afterClose func()
	closed     bool
}

func (r *syncStateSchemaRows) Close() error {
	err := r.rowScanner.Close()
	if !r.closed && err == nil {
		r.closed = true
		r.afterClose()
	}
	return err
}

type syncStateSchemaColumn struct {
	ID      int
	Name    string
	Type    string
	NotNull int
	Default sql.NullString
	Primary int
}

func syncStateSchemaMetadata(t *testing.T, db *sql.DB) []syncStateSchemaColumn {
	t.Helper()
	rows, err := db.Query("PRAGMA table_info(sync_state)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var result []syncStateSchemaColumn
	for rows.Next() {
		var c syncStateSchemaColumn
		if err := rows.Scan(&c.ID, &c.Name, &c.Type, &c.NotNull, &c.Default, &c.Primary); err != nil {
			t.Fatal(err)
		}
		result = append(result, c)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestSyncStateSchemaInitialColumnBatch(t *testing.T) {
	for _, route := range []string{"fresh", "legacy", "partial"} {
		t.Run(route, func(t *testing.T) {
			cfg := FallbackConfig(t.TempDir())
			db, err := sql.Open("sqlite", storeDSN(filepath.Join(cfg.DataDir, "engram.db")))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			db.SetMaxOpenConns(1)
			const legacyState = `
CREATE TABLE sync_state (
 target_key TEXT PRIMARY KEY, lifecycle TEXT NOT NULL DEFAULT 'idle',
 last_enqueued_seq INTEGER NOT NULL DEFAULT 0, last_acked_seq INTEGER NOT NULL DEFAULT 0,
 last_pulled_seq INTEGER NOT NULL DEFAULT 0, consecutive_failures INTEGER NOT NULL DEFAULT 0,
 backoff_until TEXT, lease_owner TEXT, lease_until TEXT, last_error TEXT,
 updated_at TEXT NOT NULL DEFAULT (datetime('now'))
)`
			if route != "fresh" {
				if _, err := db.Exec(legacyState); err != nil {
					t.Fatal(err)
				}
			}
			if route == "partial" {
				if _, err := db.Exec("ALTER TABLE sync_state ADD COLUMN reason_code TEXT; ALTER TABLE sync_state ADD COLUMN last_success_at TEXT"); err != nil {
					t.Fatal(err)
				}
			}
			seed := func() {
				t.Helper()
				if _, err := db.Exec(`INSERT INTO sync_state(target_key,lifecycle,last_enqueued_seq,last_acked_seq,last_pulled_seq,consecutive_failures,backoff_until,lease_owner,lease_until,last_error,updated_at)
 VALUES('cloud:fixture','degraded',7,3,2,5,'2031-01-01','fixture-owner','2032-01-01','kept error','2026-01-01')`); err != nil {
					t.Fatal(err)
				}
				if route == "partial" {
					if _, err := db.Exec("UPDATE sync_state SET reason_code='kept reason',last_success_at='2025-12-01' WHERE target_key='cloud:fixture'"); err != nil {
						t.Fatal(err)
					}
				}
			}
			if route != "fresh" {
				seed()
			}
			s := &Store{db: db, cfg: cfg, hooks: defaultStoreHooks()}
			version := func() int {
				t.Helper()
				var n int
				if err := db.QueryRow("PRAGMA schema_version").Scan(&n); err != nil {
					t.Fatal(err)
				}
				return n
			}
			lastInspection := -1
			var additions []int
			query := s.hooks.queryIt
			s.hooks.queryIt = func(db queryer, q string, args ...any) (rowScanner, error) {
				// This is the first operation after the sync_state column batch.
				if q == "PRAGMA table_info(sync_chunks)" {
					if lastInspection < 0 {
						t.Fatal("sync_state batch was not inspected")
					}
					additions = append(additions, version()-lastInspection)
				}
				rows, err := query(db, q, args...)
				if err == nil && q == "PRAGMA table_info(sync_state)" {
					return &syncStateSchemaRows{rowScanner: rows, afterClose: func() { lastInspection = version() }}, nil
				}
				return rows, err
			}
			if err := s.migrate(); err != nil {
				t.Fatal(err)
			}
			want := map[string]int{"fresh": 0, "legacy": 3, "partial": 1}[route]
			if !reflect.DeepEqual(additions, []int{want}) {
				t.Errorf("column ALTER count=%v, want [%d]", additions, want)
			}
			if route == "fresh" {
				seed()
			}
			backoff, owner, lease, success := "2031-01-01", "fixture-owner", "2032-01-01", "2025-12-01"
			lastError, reason := "kept error", "kept reason"
			expected := &SyncState{
				TargetKey: "cloud:fixture", Lifecycle: SyncLifecycleDegraded,
				LastEnqueuedSeq: 7, LastAckedSeq: 3, LastPulledSeq: 2, ConsecutiveFailures: 5,
				BackoffUntil: &backoff, LeaseOwner: &owner, LeaseUntil: &lease,
				LastError: &lastError, UpdatedAt: "2026-01-01",
			}
			if route == "partial" {
				expected.ReasonCode, expected.LastSuccessAt = &reason, &success
			}
			checkState := func(store *Store) {
				t.Helper()
				got, err := store.GetSyncState("cloud:fixture")
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, expected) {
					t.Fatalf("state changed: got %+v want %+v", got, expected)
				}
				inbox, err := store.GetSyncState(SyncInboxTargetKey)
				if err != nil || inbox == nil || inbox.Lifecycle != SyncLifecycleInbox {
					t.Fatalf("reserved inbox=%+v err=%v", inbox, err)
				}
			}
			checkState(s)
			before := syncStateSchemaMetadata(t, db)
			found := map[string]syncStateSchemaColumn{}
			for _, c := range before {
				found[c.Name] = c
			}
			for i, name := range []string{"reason_code", "reason_message"} {
				c, ok := found[name]
				if !ok || c.Type != "TEXT" || c.NotNull != 0 || c.Default.Valid || c.Primary != 0 {
					t.Errorf("unexpected reason column %s: %+v", name, c)
				}
				if route == "fresh" && c.ID != found["updated_at"].ID+i+1 {
					t.Errorf("fresh append order for %s: %+v", name, c)
				}
			}
			lastInspection = -1
			if err := s.migrate(); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(additions, []int{want, 0}) {
				t.Errorf("repeat column ALTER count=%v", additions)
			}
			if after := syncStateSchemaMetadata(t, db); !reflect.DeepEqual(before, after) {
				t.Fatal("repeat migration changed metadata")
			}
			checkState(s)
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = reopened.Close() })
			if after := syncStateSchemaMetadata(t, reopened.db); !reflect.DeepEqual(before, after) {
				t.Fatal("reopening changed metadata")
			}
			checkState(reopened)
		})
	}
}
