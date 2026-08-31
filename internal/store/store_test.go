package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	_ "modernc.org/sqlite"
)

func mustDefaultConfig(t *testing.T) Config {
	t.Helper()
	cfg, err := DefaultConfig()
	if err != nil {
		t.Fatalf("DefaultConfig: %v", err)
	}
	return cfg
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	cfg := mustDefaultConfig(t)
	cfg.DataDir = t.TempDir()
	cfg.DedupeWindow = time.Hour

	s, err := New(cfg)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() {
		_ = s.Close()
	})
	return s
}

func TestProjectIdentityAdmissionRejectsEmptyWritesWithoutJournalState(t *testing.T) {
	s := newTestStore(t)
	assertCounts := func(wantSessions, wantObservations, wantPrompts, wantMutations int) {
		t.Helper()
		for table, want := range map[string]int{"sessions": wantSessions, "observations": wantObservations, "user_prompts": wantPrompts, "sync_mutations": wantMutations} {
			var got int
			if err := s.DB().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&got); err != nil {
				t.Fatalf("count %s: %v", table, err)
			}
			if got != want {
				t.Fatalf("%s count = %d, want %d", table, got, want)
			}
		}
	}

	if err := s.CreateSession("missing-project", " ", "/tmp"); !errors.Is(err, ErrProjectRequired) {
		t.Fatalf("CreateSession error = %v, want ErrProjectRequired", err)
	}
	if _, err := s.AddObservation(AddObservationParams{SessionID: "missing-project", Type: "note", Title: "title", Content: "content"}); !errors.Is(err, ErrProjectRequired) {
		t.Fatalf("AddObservation error = %v, want ErrProjectRequired", err)
	}
	if _, err := s.AddPrompt(AddPromptParams{SessionID: "missing-project", Content: "content"}); !errors.Is(err, ErrProjectRequired) {
		t.Fatalf("AddPrompt error = %v, want ErrProjectRequired", err)
	}
	if _, _, err := s.AddPromptIfMissing(AddPromptParams{SessionID: "missing-project", Content: "content"}); !errors.Is(err, ErrProjectRequired) {
		t.Fatalf("AddPromptIfMissing error = %v, want ErrProjectRequired", err)
	}
	assertCounts(0, 0, 0, 0)
}

func newTestStoreWithNullableLegacySessions(t *testing.T, sessions ...struct{ id, project string }) *Store {
	t.Helper()
	cfg := mustDefaultConfig(t)
	cfg.DataDir = t.TempDir()
	raw, err := sql.Open("sqlite", filepath.Join(cfg.DataDir, "engram.db"))
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	if _, err := raw.Exec(`CREATE TABLE sessions (
		id TEXT PRIMARY KEY,
		project TEXT,
		directory TEXT NOT NULL,
		started_at TEXT NOT NULL DEFAULT (datetime('now')),
		ended_at TEXT,
		summary TEXT
	)`); err != nil {
		_ = raw.Close()
		t.Fatalf("create legacy sessions: %v", err)
	}
	for _, session := range sessions {
		var project any = session.project
		if session.project == "<NULL>" {
			project = nil
		}
		if _, err := raw.Exec(`INSERT INTO sessions (id, project, directory) VALUES (?, ?, ?)`, session.id, project, "/tmp"); err != nil {
			_ = raw.Close()
			t.Fatalf("seed legacy session %q: %v", session.id, err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close legacy database: %v", err)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("open migrated legacy database: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestProjectIdentityAdmissionRejectsNullableLegacySessionProjects(t *testing.T) {
	type legacySession struct{ id, project string }
	s := newTestStoreWithNullableLegacySessions(t,
		legacySession{"null-session", "<NULL>"},
		legacySession{"blank-session", " "},
	)

	tests := []struct {
		name string
		run  func(string) error
	}{
		{"observation with NULL project", func(sessionID string) error {
			_, err := s.AddObservation(AddObservationParams{SessionID: sessionID, Type: "note", Title: "title", Content: "content"})
			return err
		}},
		{"prompt with blank project", func(sessionID string) error {
			_, err := s.AddPrompt(AddPromptParams{SessionID: sessionID, Content: "content"})
			return err
		}},
		{"deduplicated prompt with NULL project", func(sessionID string) error {
			_, _, err := s.AddPromptIfMissing(AddPromptParams{SessionID: sessionID, Content: "content"})
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sessionID := "null-session"
			if strings.Contains(tt.name, "blank") {
				sessionID = "blank-session"
			}
			if err := tt.run(sessionID); !errors.Is(err, ErrProjectRequired) {
				t.Fatalf("error = %v, want ErrProjectRequired", err)
			}
		})
	}
}

func TestContentTruncationMeasuresRedactedBytes(t *testing.T) {
	s := newTestStore(t)
	s.cfg.MaxObservationLength = len("ok[REDACTED]")

	metadata := s.ContentTruncation("ok<private>" + strings.Repeat("secret", 10) + "</private>")
	if metadata.OriginalBytes != len("ok[REDACTED]") {
		t.Fatalf("original bytes = %d, want %d", metadata.OriginalBytes, len("ok[REDACTED]"))
	}
	if metadata.LimitBytes != s.cfg.MaxObservationLength {
		t.Fatalf("limit bytes = %d, want %d", metadata.LimitBytes, s.cfg.MaxObservationLength)
	}
	if metadata.Truncated {
		t.Fatal("redacted content at the byte limit must not report truncation")
	}
}

func TestTruncateContentPreservesUTF8BytePrefix(t *testing.T) {
	const marker = "... [truncated]"

	for _, tc := range []struct {
		name    string
		content string
		max     int
		want    string
	}{
		{name: "under limit", content: "hello", max: 6, want: "hello"},
		{name: "exact limit", content: "hello", max: 5, want: "hello"},
		{name: "oversized ASCII", content: "abcdef", max: 4, want: "abcd" + marker},
		{name: "two-byte rune before boundary", content: "a¢z", max: 1, want: "a" + marker},
		{name: "two-byte rune inside boundary", content: "a¢z", max: 2, want: "a" + marker},
		{name: "two-byte rune after boundary", content: "a¢z", max: 3, want: "a¢" + marker},
		{name: "three-byte rune before boundary", content: "a€z", max: 1, want: "a" + marker},
		{name: "three-byte rune inside boundary", content: "a€z", max: 2, want: "a" + marker},
		{name: "three-byte rune after boundary", content: "a€z", max: 4, want: "a€" + marker},
		{name: "four-byte rune before boundary", content: "a😀z", max: 1, want: "a" + marker},
		{name: "four-byte rune inside boundary", content: "a😀z", max: 3, want: "a" + marker},
		{name: "four-byte rune after boundary", content: "a😀z", max: 5, want: "a😀" + marker},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateContent(tc.content, tc.max)
			if got != tc.want {
				t.Fatalf("truncateContent(%q, %d) = %q, want %q", tc.content, tc.max, got, tc.want)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("truncateContent(%q, %d) returned invalid UTF-8: %q", tc.content, tc.max, got)
			}
			if len(tc.content) > tc.max && !strings.HasSuffix(got, marker) {
				t.Fatalf("truncated content does not preserve marker: %q", got)
			}
			if len(tc.content) > tc.max && len(strings.TrimSuffix(got, marker)) > tc.max {
				t.Fatalf("truncated prefix exceeds byte cap %d: %q", tc.max, got)
			}
		})
	}
}

func TestProjectIdentityAdmissionAllowsOwnedWritesAndRejectsReassignment(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("owned-session", "Project", "/tmp"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	id, err := s.AddObservation(AddObservationParams{SessionID: "owned-session", Type: "note", Title: "title", Content: "content", Project: "Project"})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}
	other := "other"
	if _, err := s.UpdateObservation(id, UpdateObservationParams{Project: &other}); !errors.Is(err, ErrObservationProjectImmutable) {
		t.Fatalf("UpdateObservation error = %v, want ErrObservationProjectImmutable", err)
	}
	obs, err := s.GetObservation(id)
	if err != nil {
		t.Fatalf("GetObservation: %v", err)
	}
	if obs.Project == nil || *obs.Project != "project" {
		t.Fatalf("project = %v, want project", obs.Project)
	}
	var mutations int
	if err := s.DB().QueryRow("SELECT COUNT(*) FROM sync_mutations").Scan(&mutations); err != nil {
		t.Fatalf("count sync mutations: %v", err)
	}
	if mutations != 2 {
		t.Fatalf("sync mutations = %d, want 2", mutations)
	}
}

func TestRescueNullProjectOwnershipRequiresExplicitScope(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.RescueNullProjectOwnership(ProjectRescueParams{TargetProject: "target"}); !errors.Is(err, ErrProjectRescueInvalidRequest) {
		t.Fatalf("empty rescue scope error = %v, want ErrProjectRescueInvalidRequest", err)
	}
	if _, err := s.RescueNullProjectOwnership(ProjectRescueParams{ObservationIDs: []int64{1}}); !errors.Is(err, ErrProjectRequired) {
		t.Fatalf("missing target error = %v, want ErrProjectRequired", err)
	}
	if _, err := s.RescueNullProjectOwnership(ProjectRescueParams{TargetProject: "target", PromptIDs: []int64{0}}); !errors.Is(err, ErrProjectRescueInvalidRequest) {
		t.Fatalf("invalid record id error = %v, want ErrProjectRescueInvalidRequest", err)
	}
	if _, err := s.RescueNullProjectOwnership(ProjectRescueParams{TargetProject: "target", SessionIDs: []string{" "}}); !errors.Is(err, ErrProjectRescueInvalidRequest) {
		t.Fatalf("blank session id error = %v, want ErrProjectRescueInvalidRequest", err)
	}
}

func TestRescueNullProjectOwnershipRescuesLegacyNullableSessionAndJournalsOnce(t *testing.T) {
	type legacySession struct{ id, project string }
	s := newTestStoreWithNullableLegacySessions(t, legacySession{"legacy-session", "<NULL>"})

	result, err := s.RescueNullProjectOwnership(ProjectRescueParams{TargetProject: "target", SessionIDs: []string{"legacy-session"}})
	if err != nil {
		t.Fatalf("RescueNullProjectOwnership: %v", err)
	}
	if result.RescuedSessions != 1 || result.Rescued() != 1 || !result.Journaled {
		t.Fatalf("rescue result = %#v, want one rescued journaled session", result)
	}
	var project string
	if err := s.DB().QueryRow(`SELECT project FROM sessions WHERE id = ?`, "legacy-session").Scan(&project); err != nil || project != "target" {
		t.Fatalf("rescued session project = %q, err=%v, want target", project, err)
	}
	var mutations int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM sync_mutations WHERE entity = ? AND entity_key = ? AND project = ? AND acked_at IS NULL`, SyncEntitySession, "legacy-session", "target").Scan(&mutations); err != nil || mutations != 1 {
		t.Fatalf("canonical session mutations = %d, err=%v, want 1", mutations, err)
	}
}

// seedForeignOwnedObservationTx inserts an observation directly, bypassing the
// write paths, so a test can construct the legacy shape where an unowned session
// already parents a record owned by a different project.
func seedForeignOwnedObservation(t *testing.T, s *Store, sessionID, project, title string) int64 {
	t.Helper()
	res, err := s.DB().Exec(
		`INSERT INTO observations (sync_id, session_id, type, title, content, project, scope) VALUES (?, ?, 'note', ?, 'content', ?, 'project')`,
		"obs-seed-"+title, sessionID, title, project,
	)
	if err != nil {
		t.Fatalf("seed foreign-owned observation: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("seed foreign-owned observation id: %v", err)
	}
	return id
}

func sessionProjectOrNull(t *testing.T, s *Store, sessionID string) string {
	t.Helper()
	var project sql.NullString
	if err := s.DB().QueryRow(`SELECT project FROM sessions WHERE id = ?`, sessionID).Scan(&project); err != nil {
		t.Fatalf("read session project: %v", err)
	}
	if !project.Valid {
		return "<NULL>"
	}
	return project.String
}

// A legacy session that carries no ownership must not permanently reject writes.
// The write already knows its own project, so the session adopts it instead of
// the record being rejected or silently split from its parent.
func TestAddObservationAdoptsUnownedLegacySessionProject(t *testing.T) {
	type legacySession struct{ id, project string }
	for _, tc := range []struct{ name, sessionID string }{
		{"null project", "null-session"},
		{"blank project", "blank-session"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStoreWithNullableLegacySessions(t,
				legacySession{"null-session", "<NULL>"},
				legacySession{"blank-session", " "},
			)

			id, err := s.AddObservation(AddObservationParams{SessionID: tc.sessionID, Type: "note", Title: "upgraded", Content: "content", Project: "target"})
			if err != nil {
				t.Fatalf("AddObservation on unowned legacy session = %v, want success", err)
			}
			if id == 0 {
				t.Fatal("AddObservation returned id 0")
			}
			if got := sessionProjectOrNull(t, s, tc.sessionID); got != "target" {
				t.Fatalf("session project = %q, want target (session must adopt the write's project)", got)
			}
			obs, err := s.GetObservation(id)
			if err != nil {
				t.Fatalf("GetObservation: %v", err)
			}
			if obs.Project == nil || *obs.Project != "target" {
				t.Fatalf("observation project = %v, want target", obs.Project)
			}
			// The adopted session must be journaled so the cloud sees the same
			// ownership the local store now holds.
			var mutations int
			if err := s.DB().QueryRow(
				`SELECT COUNT(*) FROM sync_mutations WHERE entity = ? AND entity_key = ? AND project = ? AND acked_at IS NULL`,
				SyncEntitySession, tc.sessionID, "target",
			).Scan(&mutations); err != nil {
				t.Fatalf("count session mutations: %v", err)
			}
			if mutations != 1 {
				t.Fatalf("adopted session mutations = %d, want 1", mutations)
			}
		})
	}
}

func TestAddPromptAdoptsUnownedLegacySessionProject(t *testing.T) {
	type legacySession struct{ id, project string }
	s := newTestStoreWithNullableLegacySessions(t, legacySession{"null-session", "<NULL>"})

	if _, err := s.AddPrompt(AddPromptParams{SessionID: "null-session", Content: "prompt", Project: "target"}); err != nil {
		t.Fatalf("AddPrompt on unowned legacy session = %v, want success", err)
	}
	if got := sessionProjectOrNull(t, s, "null-session"); got != "target" {
		t.Fatalf("session project = %q, want target", got)
	}

	if _, _, err := s.AddPromptIfMissing(AddPromptParams{SessionID: "null-session", Content: "prompt", Project: "target"}); err != nil {
		t.Fatalf("AddPromptIfMissing on adopted session = %v, want success", err)
	}
}

// Adoption must never create the mirror split: an unowned session that already
// parents a record owned by a different project is genuinely ambiguous, so the
// write is refused rather than guessed.
func TestAddObservationRefusesAdoptionWhenSessionParentsForeignOwnedRecord(t *testing.T) {
	type legacySession struct{ id, project string }
	s := newTestStoreWithNullableLegacySessions(t, legacySession{"null-session", "<NULL>"})
	seedForeignOwnedObservation(t, s, "null-session", "other", "foreign")

	_, err := s.AddObservation(AddObservationParams{SessionID: "null-session", Type: "note", Title: "new", Content: "content", Project: "target"})
	if !errors.Is(err, ErrProjectOwnershipAmbiguous) {
		t.Fatalf("AddObservation error = %v, want ErrProjectOwnershipAmbiguous", err)
	}
	if !strings.Contains(err.Error(), "engram projects rescue-ownership") {
		t.Fatalf("error %q must name the concrete repair command", err)
	}
	if got := sessionProjectOrNull(t, s, "null-session"); got != "<NULL>" {
		t.Fatalf("session project = %q, want it left untouched", got)
	}
}

// The residual hard-fail (no project on the request and none derivable from the
// session) must name a repair the operator can actually run.
func TestAddObservationUnownedSessionErrorNamesReachableRepair(t *testing.T) {
	type legacySession struct{ id, project string }
	s := newTestStoreWithNullableLegacySessions(t, legacySession{"null-session", "<NULL>"})

	_, err := s.AddObservation(AddObservationParams{SessionID: "null-session", Type: "note", Title: "t", Content: "c"})
	if !errors.Is(err, ErrProjectRequired) {
		t.Fatalf("error = %v, want ErrProjectRequired", err)
	}
	for _, want := range []string{"null-session", "engram projects rescue-ownership"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q must mention %q", err, want)
		}
	}
}

// A legacy NULL project must be readable; otherwise every caller that inspects
// the session before writing dies on an opaque scan error.
func TestGetSessionReadsLegacyNullProject(t *testing.T) {
	type legacySession struct{ id, project string }
	s := newTestStoreWithNullableLegacySessions(t, legacySession{"null-session", "<NULL>"})

	sess, err := s.GetSession("null-session")
	if err != nil {
		t.Fatalf("GetSession on legacy NULL project = %v, want success", err)
	}
	if sess.Project != "" {
		t.Fatalf("session project = %q, want empty string for NULL ownership", sess.Project)
	}
}

// Finding 3, mirror direction: the session pass must not move a session out from
// under a record that the record pass will classify as conflicting.
func TestRescueNullProjectOwnershipDoesNotSplitSessionFromForeignOwnedRecord(t *testing.T) {
	type legacySession struct{ id, project string }
	s := newTestStoreWithNullableLegacySessions(t, legacySession{"legacy-session", "<NULL>"})
	foreignID := seedForeignOwnedObservation(t, s, "legacy-session", "other", "foreign")

	result, err := s.RescueNullProjectOwnership(ProjectRescueParams{
		TargetProject:  "target",
		SessionIDs:     []string{"legacy-session"},
		ObservationIDs: []int64{foreignID},
	})
	if err != nil {
		t.Fatalf("RescueNullProjectOwnership: %v", err)
	}
	if got := sessionProjectOrNull(t, s, "legacy-session"); got != "<NULL>" {
		t.Fatalf("session project = %q, want <NULL>: the session must not move away from its foreign-owned record", got)
	}
	if result.RescuedSessions != 0 {
		t.Fatalf("rescued sessions = %d, want 0", result.RescuedSessions)
	}
	if result.Complete {
		t.Fatal("result.Complete = true, want false when records are left behind")
	}
	if len(result.Blocked) == 0 {
		t.Fatal("result.Blocked is empty, want the exact records left behind")
	}
	var sawSession, sawObservation bool
	for _, blocked := range result.Blocked {
		if blocked.Kind == "session" && blocked.ID == "legacy-session" {
			sawSession = true
		}
		if blocked.Kind == "observation" && blocked.OwnedBy == "other" {
			sawObservation = true
		}
	}
	if !sawSession || !sawObservation {
		t.Fatalf("blocked = %#v, want both the session and its foreign-owned observation", result.Blocked)
	}
}

// The safe outcome must remain distinguishable from the partial one.
func TestRescueNullProjectOwnershipReportsCompleteWhenNothingIsLeftBehind(t *testing.T) {
	type legacySession struct{ id, project string }
	s := newTestStoreWithNullableLegacySessions(t, legacySession{"legacy-session", "<NULL>"})

	result, err := s.RescueNullProjectOwnership(ProjectRescueParams{TargetProject: "target", SessionIDs: []string{"legacy-session"}})
	if err != nil {
		t.Fatalf("RescueNullProjectOwnership: %v", err)
	}
	if !result.Complete {
		t.Fatalf("result.Complete = false, want true when everything moved: %#v", result)
	}
	if len(result.Blocked) != 0 {
		t.Fatalf("blocked = %#v, want empty", result.Blocked)
	}
}

func TestRescueNullProjectOwnershipRescuesOnlyNullRecordsAndJournalsOnce(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("legacy-session", "legacy", "/tmp"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	observationID, err := s.AddObservation(AddObservationParams{SessionID: "legacy-session", Type: "note", Title: "legacy", Content: "content", Project: "legacy"})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}
	promptID, err := s.AddPrompt(AddPromptParams{SessionID: "legacy-session", Content: "prompt", Project: "legacy"})
	if err != nil {
		t.Fatalf("AddPrompt: %v", err)
	}
	if err := s.CreateSession("owned-session", "other", "/tmp"); err != nil {
		t.Fatalf("CreateSession owned: %v", err)
	}
	ownedID, err := s.AddObservation(AddObservationParams{SessionID: "owned-session", Type: "note", Title: "owned", Content: "content", Project: "other"})
	if err != nil {
		t.Fatalf("AddObservation owned: %v", err)
	}
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`UPDATE sessions SET project = '' WHERE id = ?`, []any{"legacy-session"}},
		{`UPDATE observations SET project = NULL WHERE id = ?`, []any{observationID}},
		{`UPDATE user_prompts SET project = NULL WHERE id = ?`, []any{promptID}},
		{`DELETE FROM sync_mutations WHERE entity_key = (SELECT sync_id FROM observations WHERE id = ?)`, []any{observationID}},
		{`DELETE FROM sync_mutations WHERE entity_key = (SELECT sync_id FROM user_prompts WHERE id = ?)`, []any{promptID}},
	} {
		if _, err := s.DB().Exec(statement.query, statement.args...); err != nil {
			t.Fatalf("seed legacy ownership: %v", err)
		}
	}

	params := ProjectRescueParams{TargetProject: "target", ObservationIDs: []int64{observationID, ownedID}, PromptIDs: []int64{promptID}}
	result, err := s.RescueNullProjectOwnership(params)
	if err != nil {
		t.Fatalf("RescueNullProjectOwnership: %v", err)
	}
	// The unowned session moves with its two records; the owned record stays put
	// because its own session belongs to another project.
	if result.Rescued() != 3 || result.RescuedSessions != 1 || result.ConflictingRecords != 1 || !result.Journaled {
		t.Fatalf("unexpected rescue result: %#v", result)
	}
	for _, query := range []string{
		`SELECT project FROM sessions WHERE id = 'legacy-session'`,
		`SELECT project FROM observations WHERE id = ` + fmt.Sprint(observationID),
		`SELECT project FROM user_prompts WHERE id = ` + fmt.Sprint(promptID),
	} {
		var project string
		if err := s.DB().QueryRow(query).Scan(&project); err != nil || project != "target" {
			t.Fatalf("rescued project query %q = %q, %v", query, project, err)
		}
	}
	var ownedProject string
	if err := s.DB().QueryRow(`SELECT project FROM observations WHERE id = ?`, ownedID).Scan(&ownedProject); err != nil || ownedProject != "other" {
		t.Fatalf("owned record changed to %q, err=%v", ownedProject, err)
	}
	var mutations int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM sync_mutations WHERE project = 'target'`).Scan(&mutations); err != nil || mutations != 3 {
		t.Fatalf("target mutations = %d, err=%v, want 3", mutations, err)
	}

	again, err := s.RescueNullProjectOwnership(params)
	if err != nil {
		t.Fatalf("repeat rescue: %v", err)
	}
	if again.Rescued() != 0 || again.ConflictingRecords != 1 || again.SkippedRecords != 2 || !again.Journaled {
		t.Fatalf("repeat rescue result = %#v", again)
	}
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM sync_mutations WHERE project = 'target'`).Scan(&mutations); err != nil || mutations != 3 {
		t.Fatalf("repeat target mutations = %d, err=%v, want 3", mutations, err)
	}
}

func TestRescueNullProjectOwnershipReplacesStaleAcknowledgedJournal(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("legacy-session", "legacy", "/tmp"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	id, err := s.AddObservation(AddObservationParams{SessionID: "legacy-session", Type: "note", Title: "legacy", Content: "content", Project: "legacy"})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}
	var syncID string
	if err := s.DB().QueryRow(`SELECT sync_id FROM observations WHERE id = ?`, id).Scan(&syncID); err != nil {
		t.Fatalf("read observation sync id: %v", err)
	}
	if _, err := s.DB().Exec(`UPDATE observations SET project = NULL WHERE id = ?`, id); err != nil {
		t.Fatalf("seed NULL ownership: %v", err)
	}
	// The parent session is unowned too, so the rescue is allowed to move both.
	if _, err := s.DB().Exec(`UPDATE sessions SET project = '' WHERE id = ?`, "legacy-session"); err != nil {
		t.Fatalf("seed unowned parent session: %v", err)
	}
	if _, err := s.DB().Exec(`UPDATE sync_mutations SET project = 'stale', payload = '{"project":"stale"}', acked_at = datetime('now') WHERE entity = ? AND entity_key = ?`, SyncEntityObservation, syncID); err != nil {
		t.Fatalf("seed stale journal: %v", err)
	}
	params := ProjectRescueParams{TargetProject: "target", ObservationIDs: []int64{id}}
	result, err := s.RescueNullProjectOwnership(params)
	if err != nil || !result.Journaled {
		t.Fatalf("rescue result = %#v, err=%v", result, err)
	}
	assertCanonical := func() {
		t.Helper()
		var pending int
		if err := s.DB().QueryRow(`SELECT COUNT(*) FROM sync_mutations WHERE entity = ? AND entity_key = ? AND op = ? AND project = 'target' AND acked_at IS NULL AND json_extract(payload, '$.project') = 'target'`, SyncEntityObservation, syncID, SyncOpUpsert).Scan(&pending); err != nil || pending != 1 {
			t.Fatalf("canonical pending mutations = %d, err=%v, want 1", pending, err)
		}
	}
	assertCanonical()
	if _, err := s.RescueNullProjectOwnership(params); err != nil {
		t.Fatalf("repeat rescue: %v", err)
	}
	assertCanonical()
}

func TestRescueNullProjectOwnershipEnqueuesDeleteDespitePendingOppositeOperation(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("legacy-session", "legacy", "/tmp"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	id, err := s.AddObservation(AddObservationParams{SessionID: "legacy-session", Type: "note", Title: "legacy", Content: "content", Project: "legacy"})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}
	var syncID string
	if err := s.DB().QueryRow(`SELECT sync_id FROM observations WHERE id = ?`, id).Scan(&syncID); err != nil {
		t.Fatalf("read observation sync id: %v", err)
	}
	if _, err := s.DB().Exec(`UPDATE observations SET project = NULL, deleted_at = datetime('now') WHERE id = ?`, id); err != nil {
		t.Fatalf("seed soft-deleted NULL ownership: %v", err)
	}
	if _, err := s.DB().Exec(`UPDATE sessions SET project = '' WHERE id = ?`, "legacy-session"); err != nil {
		t.Fatalf("seed unowned parent session: %v", err)
	}
	if _, err := s.DB().Exec(`UPDATE sync_mutations SET project = 'target', payload = json_set(payload, '$.project', 'target') WHERE entity = ? AND entity_key = ? AND op = ?`, SyncEntityObservation, syncID, SyncOpUpsert); err != nil {
		t.Fatalf("seed pending opposite mutation: %v", err)
	}

	params := ProjectRescueParams{TargetProject: "target", ObservationIDs: []int64{id}}
	assertCanonicalDelete := func() {
		t.Helper()
		var deletes int
		if err := s.DB().QueryRow(`SELECT COUNT(*) FROM sync_mutations WHERE entity = ? AND entity_key = ? AND op = ? AND project = 'target' AND acked_at IS NULL AND json_extract(payload, '$.project') = 'target'`, SyncEntityObservation, syncID, SyncOpDelete).Scan(&deletes); err != nil || deletes != 1 {
			t.Fatalf("canonical pending deletes = %d, err=%v, want 1", deletes, err)
		}
	}

	result, err := s.RescueNullProjectOwnership(params)
	if err != nil || !result.Journaled {
		t.Fatalf("rescue result = %#v, err=%v", result, err)
	}
	assertCanonicalDelete()
	result, err = s.RescueNullProjectOwnership(params)
	if err != nil || !result.Journaled {
		t.Fatalf("repeat rescue result = %#v, err=%v", result, err)
	}
	assertCanonicalDelete()
}

func TestRescueNullProjectOwnershipRollsBackWhenJournalEnqueueFails(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("legacy-session", "legacy", "/tmp"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	id, err := s.AddObservation(AddObservationParams{SessionID: "legacy-session", Type: "note", Title: "legacy", Content: "content", Project: "legacy"})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}
	if _, err := s.DB().Exec(`UPDATE observations SET project = NULL WHERE id = ?`, id); err != nil {
		t.Fatalf("seed legacy observation ownership: %v", err)
	}
	if _, err := s.DB().Exec(`UPDATE sessions SET project = '' WHERE id = ?`, "legacy-session"); err != nil {
		t.Fatalf("seed unowned parent session: %v", err)
	}
	if _, err := s.DB().Exec(`DELETE FROM sync_mutations WHERE entity_key = (SELECT sync_id FROM observations WHERE id = ?)`, id); err != nil {
		t.Fatalf("seed legacy observation: %v", err)
	}
	originalExec := s.hooks.exec
	s.hooks.exec = func(db execer, query string, args ...any) (sql.Result, error) {
		if strings.Contains(query, "INSERT INTO sync_mutations") {
			return nil, errors.New("journal unavailable")
		}
		return originalExec(db, query, args...)
	}
	t.Cleanup(func() { s.hooks.exec = originalExec })
	if _, err := s.RescueNullProjectOwnership(ProjectRescueParams{TargetProject: "target", ObservationIDs: []int64{id}}); err == nil {
		t.Fatal("expected journal failure")
	}
	var project sql.NullString
	if err := s.DB().QueryRow(`SELECT project FROM observations WHERE id = ?`, id).Scan(&project); err != nil {
		t.Fatalf("read rolled back observation: %v", err)
	}
	if project.Valid {
		t.Fatalf("ownership persisted after rollback: %q", project.String)
	}
}

// seedUnownedObservationTx inserts an observation whose ownership is NULL under
// an existing session, mirroring rows written before project identity was
// mandatory.
func seedUnownedObservation(t *testing.T, s *Store, sessionID, syncID, title string) int64 {
	t.Helper()
	res, err := s.DB().Exec(
		`INSERT INTO observations (sync_id, session_id, type, title, content, project, scope) VALUES (?, ?, 'note', ?, 'content', NULL, 'project')`,
		syncID, sessionID, title,
	)
	if err != nil {
		t.Fatalf("seed unowned observation: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("read seeded observation id: %v", err)
	}
	return id
}

func assertNoBlankOwnedMutations(t *testing.T, s *Store) {
	t.Helper()
	var blank int
	if err := s.DB().QueryRow(
		`SELECT COUNT(*) FROM sync_mutations WHERE ifnull(project, '') = '' OR ifnull(json_extract(payload, '$.project'), '') = ''`,
	).Scan(&blank); err != nil {
		t.Fatalf("count blank-owned mutations: %v", err)
	}
	if blank != 0 {
		t.Fatalf("blank-owned sync mutations = %d, want 0", blank)
	}
}

func TestRescueNullProjectOwnershipMovesDependentSessionOwnershipAtomically(t *testing.T) {
	type legacySession struct{ id, project string }
	s := newTestStoreWithNullableLegacySessions(t, legacySession{"legacy-session", "<NULL>"})
	observationID := seedUnownedObservation(t, s, "legacy-session", "obs-legacy", "legacy")

	result, err := s.RescueNullProjectOwnership(ProjectRescueParams{TargetProject: "target", ObservationIDs: []int64{observationID}})
	if err != nil {
		t.Fatalf("RescueNullProjectOwnership: %v", err)
	}
	if result.RescuedObservations != 1 || result.RescuedSessions != 1 || !result.Journaled {
		t.Fatalf("rescue result = %#v, want the dependent session rescued alongside the observation", result)
	}
	var sessionProject string
	if err := s.DB().QueryRow(`SELECT ifnull(project, '') FROM sessions WHERE id = ?`, "legacy-session").Scan(&sessionProject); err != nil || sessionProject != "target" {
		t.Fatalf("dependent session project = %q, err=%v, want target", sessionProject, err)
	}
	var sessionMutations int
	if err := s.DB().QueryRow(
		`SELECT COUNT(*) FROM sync_mutations WHERE entity = ? AND entity_key = ? AND project = 'target' AND acked_at IS NULL`,
		SyncEntitySession, "legacy-session",
	).Scan(&sessionMutations); err != nil || sessionMutations != 1 {
		t.Fatalf("dependent session mutations = %d, err=%v, want 1", sessionMutations, err)
	}
	assertNoBlankOwnedMutations(t, s)
}

func TestRescueNullProjectOwnershipMovesBlankOwnedDependentSessionAtomically(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("blank-session", "legacy", "/tmp"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	observationID := seedUnownedObservation(t, s, "blank-session", "obs-blank", "blank")
	if _, err := s.DB().Exec(`UPDATE sessions SET project = '' WHERE id = ?`, "blank-session"); err != nil {
		t.Fatalf("seed blank session ownership: %v", err)
	}
	if _, err := s.DB().Exec(`DELETE FROM sync_mutations`); err != nil {
		t.Fatalf("clear seeded journal: %v", err)
	}

	result, err := s.RescueNullProjectOwnership(ProjectRescueParams{TargetProject: "target", ObservationIDs: []int64{observationID}})
	if err != nil {
		t.Fatalf("RescueNullProjectOwnership: %v", err)
	}
	if result.RescuedObservations != 1 || result.RescuedSessions != 1 {
		t.Fatalf("rescue result = %#v, want the blank-owned session rescued alongside the observation", result)
	}
	var sessionProject string
	if err := s.DB().QueryRow(`SELECT project FROM sessions WHERE id = ?`, "blank-session").Scan(&sessionProject); err != nil || sessionProject != "target" {
		t.Fatalf("blank session project = %q, err=%v, want target", sessionProject, err)
	}
	assertNoBlankOwnedMutations(t, s)
}

func TestRescueNullProjectOwnershipRefusesRecordsOwnedByAnotherSessionProject(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("other-session", "other", "/tmp"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	observationID := seedUnownedObservation(t, s, "other-session", "obs-other", "other")
	promptID, err := s.AddPrompt(AddPromptParams{SessionID: "other-session", Content: "prompt", Project: "other"})
	if err != nil {
		t.Fatalf("AddPrompt: %v", err)
	}
	if _, err := s.DB().Exec(`UPDATE user_prompts SET project = NULL WHERE id = ?`, promptID); err != nil {
		t.Fatalf("seed unowned prompt: %v", err)
	}

	result, err := s.RescueNullProjectOwnership(ProjectRescueParams{
		TargetProject:  "target",
		ObservationIDs: []int64{observationID},
		PromptIDs:      []int64{promptID},
	})
	if err != nil {
		t.Fatalf("RescueNullProjectOwnership: %v", err)
	}
	if result.Rescued() != 0 || result.ConflictingRecords != 2 {
		t.Fatalf("rescue result = %#v, want both records reported as conflicting", result)
	}
	for _, query := range []string{
		`SELECT project FROM observations WHERE id = ?`,
		`SELECT project FROM user_prompts WHERE id = ?`,
	} {
		id := any(observationID)
		if strings.Contains(query, "user_prompts") {
			id = promptID
		}
		var project sql.NullString
		if err := s.DB().QueryRow(query, id).Scan(&project); err != nil {
			t.Fatalf("read record ownership: %v", err)
		}
		if project.Valid {
			t.Fatalf("record moved without its session: %q", project.String)
		}
	}
	var sessionProject string
	if err := s.DB().QueryRow(`SELECT project FROM sessions WHERE id = ?`, "other-session").Scan(&sessionProject); err != nil || sessionProject != "other" {
		t.Fatalf("session project = %q, err=%v, want other", sessionProject, err)
	}
}

func TestEnqueueMissingLocalMutationRefusesBlankOwnedSession(t *testing.T) {
	s := newTestStore(t)
	err := s.withTx(func(tx *sql.Tx) error {
		_, enqueueErr := s.enqueueMissingLocalMutationTx(tx, SyncEntitySession, "blank-session", syncSessionPayload{ID: "blank-session", Directory: "/tmp"})
		return enqueueErr
	})
	if !errors.Is(err, ErrProjectRequired) {
		t.Fatalf("enqueue blank-owned session error = %v, want ErrProjectRequired", err)
	}
	var mutations int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM sync_mutations WHERE entity = ?`, SyncEntitySession).Scan(&mutations); err != nil || mutations != 0 {
		t.Fatalf("journaled blank-owned session mutations = %d, err=%v, want 0", mutations, err)
	}
}

func TestPersistenceRoutesTruncateContentAtUTF8Boundary(t *testing.T) {
	const (
		content = "a😀z"
		marker  = "... [truncated]"
		want    = "a" + marker
	)

	for _, tc := range []struct {
		name  string
		write func(*Store) (string, error)
	}{
		{
			name: "add observation",
			write: func(s *Store) (string, error) {
				id, err := s.AddObservation(AddObservationParams{SessionID: "s1", Type: "bugfix", Title: "title", Content: content, Project: "engram", Scope: "project"})
				if err != nil {
					return "", err
				}
				obs, err := s.GetObservation(id)
				if err != nil {
					return "", err
				}
				return obs.Content, nil
			},
		},
		{
			name: "add prompt",
			write: func(s *Store) (string, error) {
				if _, err := s.AddPrompt(AddPromptParams{SessionID: "s1", Content: content, Project: "engram"}); err != nil {
					return "", err
				}
				prompts, err := s.RecentPrompts("engram", 1)
				if err != nil {
					return "", err
				}
				return prompts[0].Content, nil
			},
		},
		{
			name: "add prompt if missing",
			write: func(s *Store) (string, error) {
				if _, _, err := s.AddPromptIfMissing(AddPromptParams{SessionID: "s1", Content: content, Project: "engram"}); err != nil {
					return "", err
				}
				prompts, err := s.RecentPrompts("engram", 1)
				if err != nil {
					return "", err
				}
				return prompts[0].Content, nil
			},
		},
		{
			name: "update observation",
			write: func(s *Store) (string, error) {
				id, err := s.AddObservation(AddObservationParams{SessionID: "s1", Type: "bugfix", Title: "title", Content: "original", Project: "engram", Scope: "project"})
				if err != nil {
					return "", err
				}
				updatedContent := content
				obs, err := s.UpdateObservation(id, UpdateObservationParams{Content: &updatedContent})
				if err != nil {
					return "", err
				}
				return obs.Content, nil
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			s.cfg.MaxObservationLength = 3
			if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
				t.Fatalf("create session: %v", err)
			}

			got, err := tc.write(s)
			if err != nil {
				t.Fatalf("persist content: %v", err)
			}
			if got != want {
				t.Fatalf("persisted content = %q, want %q", got, want)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("persisted invalid UTF-8: %q", got)
			}
		})
	}
}

type fakeRows struct {
	next     []bool
	scanErr  error
	err      error
	closeErr error
	closed   bool
}

func (f *fakeRows) Next() bool {
	if len(f.next) == 0 {
		return false
	}
	v := f.next[0]
	f.next = f.next[1:]
	return v
}

func (f *fakeRows) Scan(dest ...any) error {
	return f.scanErr
}

func (f *fakeRows) Err() error {
	return f.err
}

func (f *fakeRows) Close() error {
	f.closed = true
	return f.closeErr
}

func TestAddObservationDeduplicatesWithinWindow(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	firstID, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "bugfix",
		Title:     "Fixed tokenizer",
		Content:   "Normalized tokenizer panic on edge case",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add first observation: %v", err)
	}

	secondID, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "bugfix",
		Title:     "Fixed tokenizer",
		Content:   "normalized   tokenizer panic on EDGE case",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add duplicate observation: %v", err)
	}

	if firstID != secondID {
		t.Fatalf("expected duplicate to reuse same id, got %d and %d", firstID, secondID)
	}

	obs, err := s.GetObservation(firstID)
	if err != nil {
		t.Fatalf("get deduped observation: %v", err)
	}
	if obs.DuplicateCount != 2 {
		t.Fatalf("expected duplicate_count=2, got %d", obs.DuplicateCount)
	}
}

func TestObservationWritesStoreProjectAsText(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s-project-storage", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	id, err := s.AddObservation(AddObservationParams{
		SessionID: "s-project-storage", Type: "bugfix", Title: "Store project as text", Content: "Project storage must be text", Project: "engram", Scope: "project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}
	var storageClass string
	if err := s.db.QueryRow(`SELECT typeof(project) FROM observations WHERE id = ?`, id).Scan(&storageClass); err != nil {
		t.Fatalf("read project storage class: %v", err)
	}
	if storageClass != "text" {
		t.Fatalf("project storage class = %q, want text", storageClass)
	}
}

func TestUpdateObservationPreservesProjectAsTextAndProjectVisibility(t *testing.T) {
	s := newTestStore(t)
	project := "engram"
	if err := s.CreateSession("s-update-project-storage", project, "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	id, err := s.AddObservation(AddObservationParams{
		SessionID: "s-update-project-storage", Type: "bugfix", Title: "Store project as text", Content: "Project storage must remain text", Project: project, Scope: "project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}

	updatedTitle := "Updated project storage as text"
	updated, err := s.UpdateObservation(id, UpdateObservationParams{Title: &updatedTitle, Project: &project})
	if err != nil {
		t.Fatalf("update observation: %v", err)
	}
	if updated.Title != updatedTitle {
		t.Fatalf("updated title = %q, want %q", updated.Title, updatedTitle)
	}
	if updated.Project == nil || *updated.Project != project {
		t.Fatalf("updated project = %v, want %q", updated.Project, project)
	}

	var storedProject, storageClass string
	if err := s.db.QueryRow(`SELECT project, typeof(project) FROM observations WHERE id = ?`, id).Scan(&storedProject, &storageClass); err != nil {
		t.Fatalf("read updated project storage: %v", err)
	}
	if storedProject != project {
		t.Fatalf("stored project = %q, want %q", storedProject, project)
	}
	if storageClass != "text" {
		t.Fatalf("project storage class = %q, want text", storageClass)
	}

	observations, err := s.RecentObservations(project, "project", 10)
	if err != nil {
		t.Fatalf("list project observations: %v", err)
	}
	if len(observations) != 1 || observations[0].ID != id {
		t.Fatalf("project observations = %+v, want observation %d", observations, id)
	}
}

func TestAddObservationRejectsBlankTitleBeforePersistenceAndSync(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s-admission", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	seedID, err := s.AddObservation(AddObservationParams{
		SessionID: "s-admission",
		Type:      "decision",
		Title:     "Existing title",
		Content:   "Existing content",
		Project:   "engram",
		Scope:     "project",
		TopicKey:  "architecture/admission",
	})
	if err != nil {
		t.Fatalf("seed observation: %v", err)
	}

	var observationsBefore, mutationsBefore int
	if err := s.db.QueryRow(`SELECT count(*) FROM observations`).Scan(&observationsBefore); err != nil {
		t.Fatalf("count observations before invalid write: %v", err)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM sync_mutations`).Scan(&mutationsBefore); err != nil {
		t.Fatalf("count sync mutations before invalid write: %v", err)
	}

	_, err = s.AddObservation(AddObservationParams{
		SessionID: "s-admission",
		Type:      "decision",
		Title:     " \t\n ",
		Content:   "Replacement content",
		Project:   "engram",
		Scope:     "project",
		TopicKey:  "architecture/admission",
	})
	if !errors.Is(err, ErrObservationTitleRequired) {
		t.Fatalf("expected ErrObservationTitleRequired, got %v", err)
	}

	var observationsAfter, mutationsAfter int
	if err := s.db.QueryRow(`SELECT count(*) FROM observations`).Scan(&observationsAfter); err != nil {
		t.Fatalf("count observations after invalid write: %v", err)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM sync_mutations`).Scan(&mutationsAfter); err != nil {
		t.Fatalf("count sync mutations after invalid write: %v", err)
	}
	if observationsAfter != observationsBefore || mutationsAfter != mutationsBefore {
		t.Fatalf("invalid observation changed persistence: observations %d->%d, mutations %d->%d", observationsBefore, observationsAfter, mutationsBefore, mutationsAfter)
	}

	seed, err := s.GetObservation(seedID)
	if err != nil {
		t.Fatalf("get seeded observation: %v", err)
	}
	if seed.Title != "Existing title" || seed.Content != "Existing content" {
		t.Fatalf("invalid topic-key upsert changed existing observation: %#v", seed)
	}
}

func TestUpdateObservationRejectsBlankTitleBeforePersistenceAndSync(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s-update-admission", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	id, err := s.AddObservation(AddObservationParams{
		SessionID: "s-update-admission",
		Type:      "decision",
		Title:     "Original title",
		Content:   "Original content",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("seed observation: %v", err)
	}

	var mutationsBefore int
	if err := s.db.QueryRow(`SELECT count(*) FROM sync_mutations`).Scan(&mutationsBefore); err != nil {
		t.Fatalf("count sync mutations before invalid update: %v", err)
	}

	blankTitle := " \t\n "
	_, err = s.UpdateObservation(id, UpdateObservationParams{Title: &blankTitle})
	if !errors.Is(err, ErrObservationTitleRequired) {
		t.Fatalf("expected ErrObservationTitleRequired, got %v", err)
	}

	var mutationsAfter int
	if err := s.db.QueryRow(`SELECT count(*) FROM sync_mutations`).Scan(&mutationsAfter); err != nil {
		t.Fatalf("count sync mutations after invalid update: %v", err)
	}
	if mutationsAfter != mutationsBefore {
		t.Fatalf("invalid observation update enqueued a sync mutation: %d->%d", mutationsBefore, mutationsAfter)
	}

	obs, err := s.GetObservation(id)
	if err != nil {
		t.Fatalf("get seeded observation: %v", err)
	}
	if obs.Title != "Original title" {
		t.Fatalf("invalid update changed title to %q", obs.Title)
	}
}

func TestAddObservationRejectsBlankContentBeforePersistenceAndSync(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s-content-admission", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	seedID, err := s.AddObservation(AddObservationParams{
		SessionID: "s-content-admission",
		Type:      "decision",
		Title:     "Existing title",
		Content:   "Existing content",
		Project:   "engram",
		Scope:     "project",
		TopicKey:  "architecture/content-admission",
	})
	if err != nil {
		t.Fatalf("seed observation: %v", err)
	}

	var observationsBefore, mutationsBefore int
	if err := s.db.QueryRow(`SELECT count(*) FROM observations`).Scan(&observationsBefore); err != nil {
		t.Fatalf("count observations before invalid write: %v", err)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM sync_mutations`).Scan(&mutationsBefore); err != nil {
		t.Fatalf("count sync mutations before invalid write: %v", err)
	}

	_, err = s.AddObservation(AddObservationParams{
		SessionID: "s-content-admission",
		Type:      "decision",
		Title:     "Replacement title",
		Content:   " \t\n ",
		Project:   "engram",
		Scope:     "project",
		TopicKey:  "architecture/content-admission",
	})
	if !errors.Is(err, ErrObservationContentRequired) {
		t.Fatalf("expected ErrObservationContentRequired, got %v", err)
	}

	var observationsAfter, mutationsAfter int
	if err := s.db.QueryRow(`SELECT count(*) FROM observations`).Scan(&observationsAfter); err != nil {
		t.Fatalf("count observations after invalid write: %v", err)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM sync_mutations`).Scan(&mutationsAfter); err != nil {
		t.Fatalf("count sync mutations after invalid write: %v", err)
	}
	if observationsAfter != observationsBefore || mutationsAfter != mutationsBefore {
		t.Fatalf("invalid observation changed persistence: observations %d->%d, mutations %d->%d", observationsBefore, observationsAfter, mutationsBefore, mutationsAfter)
	}

	seed, err := s.GetObservation(seedID)
	if err != nil {
		t.Fatalf("get seeded observation: %v", err)
	}
	if seed.Title != "Existing title" || seed.Content != "Existing content" {
		t.Fatalf("invalid topic-key upsert changed existing observation: %#v", seed)
	}
}

func TestUpdateObservationRejectsBlankContentBeforePersistenceAndSync(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s-update-content-admission", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	id, err := s.AddObservation(AddObservationParams{
		SessionID: "s-update-content-admission",
		Type:      "decision",
		Title:     "Original title",
		Content:   "Original content",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("seed observation: %v", err)
	}

	var mutationsBefore int
	if err := s.db.QueryRow(`SELECT count(*) FROM sync_mutations`).Scan(&mutationsBefore); err != nil {
		t.Fatalf("count sync mutations before invalid update: %v", err)
	}

	blankContent := " \t\n "
	_, err = s.UpdateObservation(id, UpdateObservationParams{Content: &blankContent})
	if !errors.Is(err, ErrObservationContentRequired) {
		t.Fatalf("expected ErrObservationContentRequired, got %v", err)
	}

	var mutationsAfter int
	if err := s.db.QueryRow(`SELECT count(*) FROM sync_mutations`).Scan(&mutationsAfter); err != nil {
		t.Fatalf("count sync mutations after invalid update: %v", err)
	}
	if mutationsAfter != mutationsBefore {
		t.Fatalf("invalid observation update enqueued a sync mutation: %d->%d", mutationsBefore, mutationsAfter)
	}

	obs, err := s.GetObservation(id)
	if err != nil {
		t.Fatalf("get seeded observation: %v", err)
	}
	if obs.Content != "Original content" {
		t.Fatalf("invalid update changed content to %q", obs.Content)
	}
}

func TestAddPromptRejectsBlankContentBeforePersistenceAndSync(t *testing.T) {
	type addResult struct {
		err      error
		inserted *bool
	}

	for _, tc := range []struct {
		name string
		add  func(*Store) addResult
	}{
		{
			name: "add prompt",
			add: func(s *Store) addResult {
				_, err := s.AddPrompt(AddPromptParams{SessionID: "s-prompt-admission", Content: " \t\n ", Project: "engram"})
				return addResult{err: err}
			},
		},
		{
			name: "add prompt if missing",
			add: func(s *Store) addResult {
				_, inserted, err := s.AddPromptIfMissing(AddPromptParams{SessionID: "s-prompt-admission", Content: " \t\n ", Project: "engram"})
				return addResult{err: err, inserted: &inserted}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			if err := s.CreateSession("s-prompt-admission", "engram", "/tmp/engram"); err != nil {
				t.Fatalf("create session: %v", err)
			}

			var promptsBefore, mutationsBefore int
			if err := s.db.QueryRow(`SELECT count(*) FROM user_prompts`).Scan(&promptsBefore); err != nil {
				t.Fatalf("count prompts before invalid write: %v", err)
			}
			if err := s.db.QueryRow(`SELECT count(*) FROM sync_mutations`).Scan(&mutationsBefore); err != nil {
				t.Fatalf("count sync mutations before invalid write: %v", err)
			}

			result := tc.add(s)
			if !errors.Is(result.err, ErrPromptContentRequired) {
				t.Fatalf("expected ErrPromptContentRequired, got %v", result.err)
			}
			if result.inserted != nil && *result.inserted {
				t.Fatal("expected invalid AddPromptIfMissing call not to insert")
			}

			var promptsAfter, mutationsAfter int
			if err := s.db.QueryRow(`SELECT count(*) FROM user_prompts`).Scan(&promptsAfter); err != nil {
				t.Fatalf("count prompts after invalid write: %v", err)
			}
			if err := s.db.QueryRow(`SELECT count(*) FROM sync_mutations`).Scan(&mutationsAfter); err != nil {
				t.Fatalf("count sync mutations after invalid write: %v", err)
			}
			if promptsAfter != promptsBefore || mutationsAfter != mutationsBefore {
				t.Fatalf("invalid prompt changed persistence: prompts %d->%d, mutations %d->%d", promptsBefore, promptsAfter, mutationsBefore, mutationsAfter)
			}
		})
	}
}

func TestScopeFiltersSearchAndContext(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	_, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "decision",
		Title:     "Project auth",
		Content:   "Keep auth middleware in project memory",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add project observation: %v", err)
	}

	_, err = s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "decision",
		Title:     "Personal note",
		Content:   "Use this regex trick later",
		Project:   "engram",
		Scope:     "personal",
	})
	if err != nil {
		t.Fatalf("add personal observation: %v", err)
	}

	projectResults, err := s.Search("regex", SearchOptions{Project: "engram", Scope: "project", Limit: 10})
	if err != nil {
		t.Fatalf("search project scope: %v", err)
	}
	if len(projectResults) != 0 {
		t.Fatalf("expected no project-scope regex results, got %d", len(projectResults))
	}

	personalResults, err := s.Search("regex", SearchOptions{Project: "engram", Scope: "personal", Limit: 10})
	if err != nil {
		t.Fatalf("search personal scope: %v", err)
	}
	if len(personalResults) != 1 {
		t.Fatalf("expected 1 personal-scope result, got %d", len(personalResults))
	}

	ctx, err := s.FormatContext("engram", "personal")
	if err != nil {
		t.Fatalf("format context personal: %v", err)
	}
	if !strings.Contains(ctx, "Personal note") {
		t.Fatalf("expected personal context to include personal observation")
	}
	if strings.Contains(ctx, "Project auth") {
		t.Fatalf("expected personal context to exclude project observation")
	}
}

func TestUpdateAndSoftDeleteExcludedFromSearchAndTimeline(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	firstID, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "bugfix",
		Title:     "first",
		Content:   "first event",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add first: %v", err)
	}

	middleID, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "bugfix",
		Title:     "middle",
		Content:   "to be deleted",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add middle: %v", err)
	}

	lastID, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "bugfix",
		Title:     "last",
		Content:   "last event",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add last: %v", err)
	}

	newTitle := "last-updated"
	newContent := "updated content"
	newScope := "personal"
	updated, err := s.UpdateObservation(lastID, UpdateObservationParams{
		Title:   &newTitle,
		Content: &newContent,
		Scope:   &newScope,
	})
	if err != nil {
		t.Fatalf("update observation: %v", err)
	}
	if updated.Title != newTitle || updated.Scope != "personal" {
		t.Fatalf("update did not apply; got title=%q scope=%q", updated.Title, updated.Scope)
	}

	if err := s.DeleteObservation(middleID, false); err != nil {
		t.Fatalf("soft delete: %v", err)
	}

	if _, err := s.GetObservation(middleID); err == nil {
		t.Fatalf("expected deleted observation to be hidden from GetObservation")
	}

	searchResults, err := s.Search("deleted", SearchOptions{Project: "engram", Limit: 10})
	if err != nil {
		t.Fatalf("search after delete: %v", err)
	}
	if len(searchResults) != 0 {
		t.Fatalf("expected deleted observation excluded from search")
	}

	timeline, err := s.Timeline(firstID, 5, 5)
	if err != nil {
		t.Fatalf("timeline: %v", err)
	}
	if len(timeline.After) != 1 || timeline.After[0].ID != lastID {
		t.Fatalf("expected timeline to skip deleted observation")
	}

	if err := s.DeleteObservation(lastID, true); err != nil {
		t.Fatalf("hard delete: %v", err)
	}
	if _, err := s.GetObservation(lastID); err == nil {
		t.Fatalf("expected hard-deleted observation to be missing")
	}
}

func TestPinnedObservationsAndFormatContextPriority(t *testing.T) {
	cfg := mustDefaultConfig(t)
	cfg.DataDir = t.TempDir()
	cfg.DedupeWindow = time.Hour
	cfg.MaxContextResults = 2
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	titles := []string{"pinned architecture", "recent one", "recent two", "recent three"}
	ids := make([]int64, 0, len(titles))
	for i, title := range titles {
		id, err := s.AddObservation(AddObservationParams{
			SessionID: "s1",
			Type:      "decision",
			Title:     title,
			Content:   fmt.Sprintf("content %d", i),
			Project:   "engram",
			Scope:     "project",
		})
		if err != nil {
			t.Fatalf("add observation %q: %v", title, err)
		}
		ids = append(ids, id)
		createdAt := fmt.Sprintf("2026-01-0%d 00:00:00", i+1)
		if _, err := s.db.Exec(`UPDATE observations SET created_at = ?, updated_at = ? WHERE id = ?`, createdAt, createdAt, id); err != nil {
			t.Fatalf("set created_at for %q: %v", title, err)
		}
	}
	exportedBeforePin, err := s.ExportProject("engram")
	if err != nil {
		t.Fatalf("export project before pin: %v", err)
	}
	exportedBeforePinJSON, err := json.Marshal(exportedBeforePin)
	if err != nil {
		t.Fatalf("marshal export before pin: %v", err)
	}
	var updatedAtBeforePin string
	if err := s.db.QueryRow(`SELECT updated_at FROM observations WHERE id = ?`, ids[0]).Scan(&updatedAtBeforePin); err != nil {
		t.Fatalf("get updated_at before pin: %v", err)
	}

	if err := s.PinObservation(ids[0]); err != nil {
		t.Fatalf("pin observation: %v", err)
	}
	var updatedAtAfterPin string
	if err := s.db.QueryRow(`SELECT updated_at FROM observations WHERE id = ?`, ids[0]).Scan(&updatedAtAfterPin); err != nil {
		t.Fatalf("get updated_at after pin: %v", err)
	}
	if updatedAtAfterPin != updatedAtBeforePin {
		t.Fatalf("pin should not change updated_at: before=%q after=%q", updatedAtBeforePin, updatedAtAfterPin)
	}
	pinned, err := s.PinnedObservations("engram", "project")
	if err != nil {
		t.Fatalf("pinned observations: %v", err)
	}
	if len(pinned) != 1 || pinned[0].ID != ids[0] || !pinned[0].Pinned {
		t.Fatalf("expected pinned observation %d, got %#v", ids[0], pinned)
	}

	ctx, err := s.FormatContext("engram", "project")
	if err != nil {
		t.Fatalf("format context: %v", err)
	}
	pinnedIdx := strings.Index(ctx, "### Pinned")
	recentIdx := strings.Index(ctx, "### Recent Observations")
	if pinnedIdx < 0 || recentIdx < 0 || pinnedIdx > recentIdx {
		t.Fatalf("expected pinned section before recent observations, got:\n%s", ctx)
	}
	if !strings.Contains(ctx, "pinned architecture") {
		t.Fatalf("expected pinned observation in context, got:\n%s", ctx)
	}
	if !strings.Contains(ctx, "recent three") || !strings.Contains(ctx, "recent two") {
		t.Fatalf("expected max recent unpinned observations in context, got:\n%s", ctx)
	}
	if strings.Contains(ctx, "recent one") {
		t.Fatalf("expected recent window to stay at MaxContextResults, got:\n%s", ctx)
	}
	exported, err := s.ExportProject("engram")
	if err != nil {
		t.Fatalf("export project: %v", err)
	}
	exportedJSON, err := json.Marshal(exported)
	if err != nil {
		t.Fatalf("marshal export: %v", err)
	}
	if strings.Contains(string(exportedJSON), `"pinned"`) {
		t.Fatalf("pinned state must stay out of sync/export JSON, got %s", exportedJSON)
	}
	if string(exportedJSON) != string(exportedBeforePinJSON) {
		t.Fatalf("pinning must not change export payload:\nbefore: %s\nafter:  %s", exportedBeforePinJSON, exportedJSON)
	}

	if err := s.UnpinObservation(ids[0]); err != nil {
		t.Fatalf("unpin observation: %v", err)
	}
	var updatedAtAfterUnpin string
	if err := s.db.QueryRow(`SELECT updated_at FROM observations WHERE id = ?`, ids[0]).Scan(&updatedAtAfterUnpin); err != nil {
		t.Fatalf("get updated_at after unpin: %v", err)
	}
	if updatedAtAfterUnpin != updatedAtBeforePin {
		t.Fatalf("unpin should not change updated_at: before=%q after=%q", updatedAtBeforePin, updatedAtAfterUnpin)
	}
	pinned, err = s.PinnedObservations("engram", "project")
	if err != nil {
		t.Fatalf("pinned observations after unpin: %v", err)
	}
	if len(pinned) != 0 {
		t.Fatalf("expected no pinned observations after unpin, got %#v", pinned)
	}
}

func TestFormatCompactionContextIsSessionScoped(t *testing.T) {
	s := newTestStore(t)

	for _, sessionID := range []string{"session-a", "session-b"} {
		if err := s.CreateSession(sessionID, "engram", "/tmp/engram"); err != nil {
			t.Fatalf("create %s: %v", sessionID, err)
		}
	}
	if err := s.EndSession("session-a", "summary-a"); err != nil {
		t.Fatalf("end session A: %v", err)
	}
	if err := s.EndSession("session-b", "summary-b"); err != nil {
		t.Fatalf("end session B: %v", err)
	}

	addObservation := func(sessionID, title, content string, pinned bool) {
		t.Helper()
		id, err := s.AddObservation(AddObservationParams{
			SessionID: sessionID,
			Type:      "decision",
			Title:     title,
			Content:   content,
			Project:   "engram",
			Scope:     "project",
		})
		if err != nil {
			t.Fatalf("add %s observation: %v", sessionID, err)
		}
		if pinned {
			if err := s.PinObservation(id); err != nil {
				t.Fatalf("pin %s observation: %v", sessionID, err)
			}
		}
	}
	addObservation("session-a", "pinned-a", "pinned-content-a", true)
	addObservation("session-a", "recent-a", "recent-content-a", false)
	addObservation("session-b", "pinned-b", "pinned-content-b", true)
	addObservation("session-b", "recent-b", "recent-content-b", false)
	if _, err := s.AddObservation(AddObservationParams{
		SessionID: "session-a",
		Type:      "decision",
		Title:     "foreign-project-observation",
		Content:   "must-not-appear",
		Project:   "foreign",
		Scope:     "project",
	}); err != nil {
		t.Fatalf("add foreign-project observation: %v", err)
	}

	for _, prompt := range []struct{ sessionID, content string }{
		{"session-a", "prompt-a"},
		{"session-b", "prompt-b"},
		{"session-a", "foreign-project-prompt"},
	} {
		project := "engram"
		if prompt.content == "foreign-project-prompt" {
			project = "foreign"
		}
		if _, err := s.AddPrompt(AddPromptParams{SessionID: prompt.sessionID, Content: prompt.content, Project: project}); err != nil {
			t.Fatalf("add %s: %v", prompt.content, err)
		}
	}

	for _, tc := range []struct {
		sessionID string
		included  []string
		excluded  []string
	}{
		{"session-a", []string{"session-a", "summary-a", "pinned-a", "recent-a", "prompt-a"}, []string{"session-b", "summary-b", "pinned-b", "recent-b", "prompt-b", "foreign-project-observation", "foreign-project-prompt"}},
		{"session-b", []string{"session-b", "summary-b", "pinned-b", "recent-b", "prompt-b"}, []string{"session-a", "summary-a", "pinned-a", "recent-a", "prompt-a"}},
	} {
		t.Run(tc.sessionID, func(t *testing.T) {
			context, err := s.FormatCompactionContext(tc.sessionID)
			if err != nil {
				t.Fatalf("format compaction context: %v", err)
			}
			for _, value := range tc.included {
				if !strings.Contains(context, value) {
					t.Errorf("context missing %q:\n%s", value, context)
				}
			}
			for _, value := range tc.excluded {
				if strings.Contains(context, value) {
					t.Errorf("context leaked %q:\n%s", value, context)
				}
			}
		})
	}

	if _, err := s.FormatCompactionContext("missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing session error = %v, want sql.ErrNoRows", err)
	}
}

func TestTopicKeyUpsertUpdatesSameTopicWithoutCreatingNewRow(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	firstID, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "architecture",
		Title:     "Auth architecture",
		Content:   "Use middleware for JWT validation.",
		Project:   "engram",
		Scope:     "project",
		TopicKey:  "architecture auth model",
	})
	if err != nil {
		t.Fatalf("add first architecture: %v", err)
	}

	secondID, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "architecture",
		Title:     "Auth architecture",
		Content:   "Move auth to gateway + middleware chain.",
		Project:   "engram",
		Scope:     "project",
		TopicKey:  "ARCHITECTURE   AUTH  MODEL",
	})
	if err != nil {
		t.Fatalf("upsert architecture: %v", err)
	}

	if firstID != secondID {
		t.Fatalf("expected topic upsert to reuse id, got %d and %d", firstID, secondID)
	}

	obs, err := s.GetObservation(firstID)
	if err != nil {
		t.Fatalf("get upserted observation: %v", err)
	}
	if obs.RevisionCount != 2 {
		t.Fatalf("expected revision_count=2, got %d", obs.RevisionCount)
	}
	if obs.TopicKey == nil || *obs.TopicKey != "architecture-auth-model" {
		t.Fatalf("expected normalized topic key, got %v", obs.TopicKey)
	}
	if !strings.Contains(obs.Content, "gateway") {
		t.Fatalf("expected latest content after upsert, got %q", obs.Content)
	}
}

func TestTopicKeyUpsertMovesObservationToLatestSession(t *testing.T) {
	s := newTestStore(t)

	for _, sessionID := range []string{"session-a", "session-b"} {
		if err := s.CreateSession(sessionID, "engram", "/tmp/engram"); err != nil {
			t.Fatalf("create %s: %v", sessionID, err)
		}
	}

	firstID, err := s.AddObservation(AddObservationParams{
		SessionID: "session-a",
		Type:      "architecture",
		Title:     "Auth model",
		Content:   "content-written-by-session-a",
		Project:   "engram",
		Scope:     "project",
		TopicKey:  "architecture/auth-model",
	})
	if err != nil {
		t.Fatalf("add session A observation: %v", err)
	}

	secondID, err := s.AddObservation(AddObservationParams{
		SessionID: "session-b",
		Type:      "architecture",
		Title:     "Auth model",
		Content:   "content-written-by-session-b",
		Project:   "engram",
		Scope:     "project",
		TopicKey:  "architecture/auth-model",
	})
	if err != nil {
		t.Fatalf("upsert session B observation: %v", err)
	}
	if firstID != secondID {
		t.Fatalf("expected cross-session topic upsert to reuse id, got %d and %d", firstID, secondID)
	}

	observation, err := s.GetObservation(firstID)
	if err != nil {
		t.Fatalf("get upserted observation: %v", err)
	}
	if observation.SessionID != "session-b" {
		t.Fatalf("expected latest writer session, got %q", observation.SessionID)
	}
	if observation.RevisionCount != 2 {
		t.Fatalf("expected revision_count=2, got %d", observation.RevisionCount)
	}

	context, err := s.FormatCompactionContext("session-a")
	if err != nil {
		t.Fatalf("format session A compaction context: %v", err)
	}
	if strings.Contains(context, "content-written-by-session-b") {
		t.Fatalf("session A compaction context leaked session B content: %s", context)
	}

	observations, err := s.AllObservations("engram", "project", 10)
	if err != nil {
		t.Fatalf("list observations: %v", err)
	}
	if len(observations) != 1 {
		t.Fatalf("expected one topic observation, got %d", len(observations))
	}
}

func TestDifferentTopicsDoNotReplaceEachOther(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	archID, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "architecture",
		Title:     "Auth architecture",
		Content:   "Architecture decision",
		Project:   "engram",
		Scope:     "project",
		TopicKey:  "architecture/auth",
	})
	if err != nil {
		t.Fatalf("add architecture observation: %v", err)
	}

	bugID, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "bugfix",
		Title:     "Fix auth nil panic",
		Content:   "Bugfix details",
		Project:   "engram",
		Scope:     "project",
		TopicKey:  "bug/auth-nil-panic",
	})
	if err != nil {
		t.Fatalf("add bug observation: %v", err)
	}

	if archID == bugID {
		t.Fatalf("expected different topic keys to create different observations")
	}

	observations, err := s.AllObservations("engram", "project", 10)
	if err != nil {
		t.Fatalf("all observations: %v", err)
	}
	if len(observations) != 2 {
		t.Fatalf("expected 2 observations, got %d", len(observations))
	}
}

func TestNewMigratesLegacyObservationIDSchema(t *testing.T) {
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "engram.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}

	_, err = db.Exec(`
		CREATE TABLE sessions (
			id TEXT PRIMARY KEY,
			project TEXT NOT NULL,
			directory TEXT NOT NULL,
			started_at TEXT NOT NULL DEFAULT (datetime('now')),
			ended_at TEXT,
			summary TEXT
		);
		CREATE TABLE observations (
			id INT,
			session_id TEXT,
			type TEXT,
			title TEXT,
			content TEXT,
			tool_name TEXT,
			project TEXT,
			created_at TEXT
		);
		INSERT INTO sessions (id, project, directory) VALUES ('s1', 'engram', '/tmp/engram');
		INSERT INTO observations (id, session_id, type, title, content, project, created_at)
		VALUES
			(NULL, 's1', 'bugfix', 'legacy null', 'legacy null content', 'engram', datetime('now')),
			(7, 's1', 'bugfix', 'legacy fixed', 'legacy fixed content', 'engram', datetime('now')),
			(7, 's1', 'bugfix', 'legacy duplicate', 'legacy duplicate content', 'engram', datetime('now'));
	`)
	if err != nil {
		_ = db.Close()
		t.Fatalf("seed legacy db: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}

	cfg := mustDefaultConfig(t)
	cfg.DataDir = dataDir

	s, err := New(cfg)
	if err != nil {
		t.Fatalf("new store after legacy schema: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	obs, err := s.AllObservations("engram", "", 20)
	if err != nil {
		t.Fatalf("all observations after migration: %v", err)
	}
	if len(obs) != 3 {
		t.Fatalf("expected 3 migrated observations, got %d", len(obs))
	}

	seen := make(map[int64]bool)
	for _, o := range obs {
		if o.ID <= 0 {
			t.Fatalf("expected migrated observation id > 0, got %d", o.ID)
		}
		if seen[o.ID] {
			t.Fatalf("expected unique migrated ids, duplicate %d", o.ID)
		}
		seen[o.ID] = true
	}

	results, err := s.Search("legacy", SearchOptions{Project: "engram", Limit: 10})
	if err != nil {
		t.Fatalf("search after migration: %v", err)
	}
	if len(results) == 0 {
		t.Fatalf("expected search results after migration")
	}

	newID, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "bugfix",
		Title:     "post migration",
		Content:   "new row should get id",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add observation after migration: %v", err)
	}
	if newID <= 0 {
		t.Fatalf("expected autoincrement id after migration, got %d", newID)
	}
}

func TestNewMigratesLegacyUserPromptsSyncIDSchema(t *testing.T) {
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "engram.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}

	_, err = db.Exec(`
		CREATE TABLE sessions (
			id TEXT PRIMARY KEY,
			project TEXT NOT NULL,
			directory TEXT NOT NULL,
			started_at TEXT NOT NULL DEFAULT (datetime('now')),
			ended_at TEXT,
			summary TEXT
		);
		CREATE TABLE user_prompts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL,
			content TEXT NOT NULL,
			project TEXT,
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			FOREIGN KEY (session_id) REFERENCES sessions(id)
		);
		INSERT INTO sessions (id, project, directory) VALUES ('s1', 'engram', '/tmp/engram');
		INSERT INTO user_prompts (session_id, content, project) VALUES ('s1', 'legacy prompt', 'engram');
	`)
	if err != nil {
		_ = db.Close()
		t.Fatalf("seed legacy db: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}

	cfg := mustDefaultConfig(t)
	cfg.DataDir = dataDir

	s, err := New(cfg)
	if err != nil {
		t.Fatalf("new store after legacy prompt schema: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	var syncID string
	if err := s.db.QueryRow("SELECT sync_id FROM user_prompts WHERE content = ?", "legacy prompt").Scan(&syncID); err != nil {
		t.Fatalf("query migrated prompt sync_id: %v", err)
	}
	if syncID == "" {
		t.Fatalf("expected migrated prompt sync_id to be backfilled")
	}

	var hasSyncIDColumn bool
	rows, err := s.db.Query("PRAGMA table_info(user_prompts)")
	if err != nil {
		t.Fatalf("query prompt columns: %v", err)
	}
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, pk int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			t.Fatalf("scan prompt column: %v", err)
		}
		if name == "sync_id" {
			hasSyncIDColumn = true
			break
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatalf("iterate prompt columns: %v", err)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close prompt columns: %v", err)
	}
	if !hasSyncIDColumn {
		t.Fatalf("expected user_prompts.sync_id column after migration")
	}

	var indexName string
	if err := s.db.QueryRow("SELECT name FROM sqlite_master WHERE type = 'index' AND name = 'idx_prompts_sync_id'").Scan(&indexName); err != nil {
		t.Fatalf("query prompt sync index: %v", err)
	}
	if indexName != "idx_prompts_sync_id" {
		t.Fatalf("expected idx_prompts_sync_id to exist, got %q", indexName)
	}
}

func TestSuggestTopicKeyNormalizesDeterministically(t *testing.T) {
	got := SuggestTopicKey("Architecture", "  Auth Model  ", "ignored")
	if got != "architecture/auth-model" {
		t.Fatalf("expected architecture/auth-model, got %q", got)
	}

	fallback := SuggestTopicKey("bugfix", "", "Fix nil panic in auth middleware on empty token")
	if fallback != "bug/fix-nil-panic-in-auth-middleware-on-empty" {
		t.Fatalf("unexpected fallback topic key: %q", fallback)
	}
}

func TestSuggestTopicKeyInfersFamilyFromTextWhenTypeIsGeneric(t *testing.T) {
	bug := SuggestTopicKey("manual", "", "Fix regression in auth login flow")
	if bug != "bug/fix-regression-in-auth-login-flow" {
		t.Fatalf("expected bug family inference, got %q", bug)
	}

	arch := SuggestTopicKey("", "ADR: Split API gateway boundary", "")
	if arch != "architecture/adr-split-api-gateway-boundary" {
		t.Fatalf("expected architecture family inference, got %q", arch)
	}
}

func TestTopicKeyUpsertIsScopedByProjectAndScope(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	baseID, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "architecture",
		Title:     "Auth model",
		Content:   "Initial architecture",
		Project:   "engram",
		Scope:     "project",
		TopicKey:  "architecture/auth-model",
	})
	if err != nil {
		t.Fatalf("add base observation: %v", err)
	}

	personalID, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "architecture",
		Title:     "Auth model",
		Content:   "Personal take",
		Project:   "engram",
		Scope:     "personal",
		TopicKey:  "architecture/auth-model",
	})
	if err != nil {
		t.Fatalf("add personal scoped observation: %v", err)
	}

	otherProjectID, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "architecture",
		Title:     "Auth model",
		Content:   "Other project",
		Project:   "another-project",
		Scope:     "project",
		TopicKey:  "architecture/auth-model",
	})
	if err != nil {
		t.Fatalf("add other project observation: %v", err)
	}

	if baseID == personalID || baseID == otherProjectID || personalID == otherProjectID {
		t.Fatalf("expected topic upsert boundaries by project+scope, got ids base=%d personal=%d other=%d", baseID, personalID, otherProjectID)
	}
}

func TestPromptProjectNullScan(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Manually insert a prompt with NULL project to simulate legacy data or external changes
	_, err := s.db.Exec(
		"INSERT INTO user_prompts (session_id, content, project) VALUES (?, ?, NULL)",
		"s1", "prompt with null project",
	)
	if err != nil {
		t.Fatalf("manual insert: %v", err)
	}

	// 1. Test RecentPrompts
	prompts, err := s.RecentPrompts("", 10)
	if err != nil {
		t.Fatalf("RecentPrompts failed with null project: %v", err)
	}
	if len(prompts) != 1 || prompts[0].Project != "" {
		t.Errorf("expected empty string for null project, got %q", prompts[0].Project)
	}

	// 2. Test SearchPrompts
	searchResult, err := s.SearchPrompts("null", "", 10)
	if err != nil {
		t.Fatalf("SearchPrompts failed with null project: %v", err)
	}
	if len(searchResult) != 1 || searchResult[0].Project != "" {
		t.Errorf("expected empty string for null project in search, got %q", searchResult[0].Project)
	}

	// 3. Test Export
	data, err := s.Export()
	if err != nil {
		t.Fatalf("Export failed with null project: %v", err)
	}
	found := false
	for _, p := range data.Prompts {
		if p.Content == "prompt with null project" {
			found = true
			if p.Project != "" {
				t.Errorf("expected empty string for null project in export, got %q", p.Project)
			}
		}
	}
	if !found {
		t.Error("exported prompts missing the test prompt")
	}
}

func TestExportProjectScopesRowsWithoutGlobalDumpFiltering(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("sess-a", "proj-a", "/tmp/proj-a"); err != nil {
		t.Fatalf("create session proj-a: %v", err)
	}
	if err := s.CreateSession("sess-b", "proj-b", "/tmp/proj-b"); err != nil {
		t.Fatalf("create session proj-b: %v", err)
	}
	if _, err := s.AddObservation(AddObservationParams{SessionID: "sess-a", Type: "note", Title: "a", Content: "a", Project: "proj-a", Scope: "project"}); err != nil {
		t.Fatalf("add obs proj-a: %v", err)
	}
	if _, err := s.AddObservation(AddObservationParams{SessionID: "sess-b", Type: "note", Title: "b", Content: "b", Project: "proj-b", Scope: "project"}); err != nil {
		t.Fatalf("add obs proj-b: %v", err)
	}
	if _, err := s.AddPrompt(AddPromptParams{SessionID: "sess-a", Content: "prompt-a", Project: "proj-a"}); err != nil {
		t.Fatalf("add prompt proj-a: %v", err)
	}
	if _, err := s.AddPrompt(AddPromptParams{SessionID: "sess-b", Content: "prompt-b", Project: "proj-b"}); err != nil {
		t.Fatalf("add prompt proj-b: %v", err)
	}

	data, err := s.ExportProject("proj-a")
	if err != nil {
		t.Fatalf("ExportProject: %v", err)
	}
	if len(data.Sessions) != 1 || data.Sessions[0].Project != "proj-a" {
		t.Fatalf("expected only proj-a sessions, got %+v", data.Sessions)
	}
	if len(data.Observations) != 1 || data.Observations[0].SessionID != "sess-a" {
		t.Fatalf("expected only proj-a observations, got %+v", data.Observations)
	}
	if len(data.Prompts) != 1 || data.Prompts[0].SessionID != "sess-a" {
		t.Fatalf("expected only proj-a prompts, got %+v", data.Prompts)
	}
}

func TestExportProjectPreservesSessionReferentialClosure(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("sess-owned-by-proj-b", "proj-b", "/tmp/proj-b"); err != nil {
		t.Fatalf("create session proj-b: %v", err)
	}

	if _, err := s.AddObservation(AddObservationParams{
		SessionID: "sess-owned-by-proj-b",
		Type:      "note",
		Title:     "cross-project obs",
		Content:   "observation references proj-b session",
		Project:   "proj-a",
		Scope:     "project",
	}); err != nil {
		t.Fatalf("add cross-project observation: %v", err)
	}

	if _, err := s.AddPrompt(AddPromptParams{
		SessionID: "sess-owned-by-proj-b",
		Content:   "cross-project prompt",
		Project:   "proj-a",
	}); err != nil {
		t.Fatalf("add cross-project prompt: %v", err)
	}

	exported, err := s.ExportProject("proj-a")
	if err != nil {
		t.Fatalf("ExportProject: %v", err)
	}

	if len(exported.Observations) != 1 || len(exported.Prompts) != 1 {
		t.Fatalf("expected one cross-project observation and prompt, got obs=%d prompts=%d", len(exported.Observations), len(exported.Prompts))
	}

	foundReferencedSession := false
	for _, sess := range exported.Sessions {
		if sess.ID == "sess-owned-by-proj-b" {
			foundReferencedSession = true
			break
		}
	}
	if !foundReferencedSession {
		t.Fatalf("expected export to include referenced session sess-owned-by-proj-b for referential closure")
	}

	dstCfg := mustDefaultConfig(t)
	dstCfg.DataDir = t.TempDir()
	dst, err := New(dstCfg)
	if err != nil {
		t.Fatalf("new destination store: %v", err)
	}
	t.Cleanup(func() { _ = dst.Close() })

	if _, err := dst.Import(exported); err != nil {
		t.Fatalf("import exported project data should succeed with referential closure: %v", err)
	}
}

func TestExportProjectDoesNotLeakRowsOwnedByOtherProjectsViaSessionMembership(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("sess-proj-a", "proj-a", "/tmp/proj-a"); err != nil {
		t.Fatalf("create session proj-a: %v", err)
	}

	if _, err := s.AddObservation(AddObservationParams{
		SessionID: "sess-proj-a",
		Type:      "note",
		Title:     "owned-by-proj-b",
		Content:   "should not leak in proj-a export",
		Project:   "proj-b",
		Scope:     "project",
	}); err != nil {
		t.Fatalf("add cross-owned observation: %v", err)
	}

	if _, err := s.AddPrompt(AddPromptParams{
		SessionID: "sess-proj-a",
		Content:   "prompt owned by proj-b",
		Project:   "proj-b",
	}); err != nil {
		t.Fatalf("add cross-owned prompt: %v", err)
	}

	if _, err := s.AddObservation(AddObservationParams{
		SessionID: "sess-proj-a",
		Type:      "note",
		Title:     "projectless observation",
		Content:   "derive ownership from proj-a session",
		Scope:     "project",
	}); err != nil {
		t.Fatalf("add projectless observation: %v", err)
	}

	if _, err := s.AddPrompt(AddPromptParams{
		SessionID: "sess-proj-a",
		Content:   "projectless prompt",
	}); err != nil {
		t.Fatalf("add projectless prompt: %v", err)
	}

	exported, err := s.ExportProject("proj-a")
	if err != nil {
		t.Fatalf("ExportProject: %v", err)
	}

	if len(exported.Observations) != 1 {
		t.Fatalf("expected only project-owned/projectless-derived observations, got %+v", exported.Observations)
	}
	if exported.Observations[0].Title != "projectless observation" {
		t.Fatalf("expected only projectless-derived observation, got %+v", exported.Observations[0])
	}

	if len(exported.Prompts) != 1 {
		t.Fatalf("expected only project-owned/projectless-derived prompts, got %+v", exported.Prompts)
	}
	if exported.Prompts[0].Content != "projectless prompt" {
		t.Fatalf("expected only projectless-derived prompt, got %+v", exported.Prompts[0])
	}
}

// ─── Passive Capture Tests ───────────────────────────────────────────────────

func TestExtractLearningsNumberedList(t *testing.T) {
	text := `Some preamble text here.

## Key Learnings:

1. bcrypt cost=12 is the right balance for our server performance
2. JWT refresh tokens need atomic rotation to prevent race conditions
3. Always validate the audience claim in JWT tokens before trusting them

## Next Steps
- something else
`
	learnings := ExtractLearnings(text)
	if len(learnings) != 3 {
		t.Fatalf("expected 3 learnings, got %d: %v", len(learnings), learnings)
	}
	if !strings.Contains(learnings[0], "bcrypt") {
		t.Fatalf("expected first learning about bcrypt, got %q", learnings[0])
	}
}

func TestExtractLearningsSpanishHeader(t *testing.T) {
	text := `## Aprendizajes Clave:

1. El costo de bcrypt=12 es el balance correcto para nuestro servidor
2. Los refresh tokens de JWT necesitan rotacion atomica
`
	learnings := ExtractLearnings(text)
	if len(learnings) != 2 {
		t.Fatalf("expected 2 learnings, got %d: %v", len(learnings), learnings)
	}
}

func TestExtractLearningsBulletList(t *testing.T) {
	text := `### Learnings:

- bcrypt cost=12 is the right balance for our server performance
- JWT refresh tokens need atomic rotation to prevent race conditions
`
	learnings := ExtractLearnings(text)
	if len(learnings) != 2 {
		t.Fatalf("expected 2 learnings, got %d: %v", len(learnings), learnings)
	}
}

func TestExtractLearningsIgnoresShortItems(t *testing.T) {
	text := `## Key Learnings:

1. too short
2. bcrypt cost=12 is the right balance for our server performance
3. also short
`
	learnings := ExtractLearnings(text)
	if len(learnings) != 1 {
		t.Fatalf("expected 1 learning (short ones filtered), got %d: %v", len(learnings), learnings)
	}
}

func TestExtractLearningsNoSection(t *testing.T) {
	text := `This is just regular text without any learning section headers.
It has multiple lines but no ## Key Learnings or similar.
`
	learnings := ExtractLearnings(text)
	if len(learnings) != 0 {
		t.Fatalf("expected 0 learnings, got %d: %v", len(learnings), learnings)
	}
}

func TestExtractLearningsSectionPresentButNoValidItems(t *testing.T) {
	text := `## Key Learnings:

1. short
2. tiny
`
	learnings := ExtractLearnings(text)
	if len(learnings) != 0 {
		t.Fatalf("expected 0 learnings when section has no valid items, got %d: %v", len(learnings), learnings)
	}
}

func TestExtractLearningsUsesLastSection(t *testing.T) {
	text := `## Key Learnings:

1. This is from the first section and should be ignored

Some other text here.

## Key Learnings:

1. This is from the last section and should be captured as the real one
`
	learnings := ExtractLearnings(text)
	if len(learnings) != 1 {
		t.Fatalf("expected 1 learning from last section, got %d: %v", len(learnings), learnings)
	}
	if !strings.Contains(learnings[0], "last section") {
		t.Fatalf("expected learning from last section, got %q", learnings[0])
	}
}

func TestExtractLearningsFallsBackWhenLastSectionHasNoValidItems(t *testing.T) {
	text := `## Key Learnings:

1. This is long enough and should be captured from the previous section

## Key Learnings:

1. short
2. tiny
`
	learnings := ExtractLearnings(text)
	if len(learnings) != 1 {
		t.Fatalf("expected fallback to previous valid section, got %d: %v", len(learnings), learnings)
	}
	if !strings.Contains(learnings[0], "previous section") {
		t.Fatalf("expected learning from previous section, got %q", learnings[0])
	}
}

func TestExtractLearningsCleansMarkdown(t *testing.T) {
	text := "## Key Learnings:\n\n1. **Use** `context.Context` in *all* handlers to support cancellation correctly\n"
	learnings := ExtractLearnings(text)
	if len(learnings) != 1 {
		t.Fatalf("expected 1 learning, got %d: %v", len(learnings), learnings)
	}
	if strings.Contains(learnings[0], "**") || strings.Contains(learnings[0], "`") || strings.Contains(learnings[0], "*") {
		t.Fatalf("expected markdown to be stripped, got %q", learnings[0])
	}
}

func TestPassiveCaptureStoresLearnings(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	text := `## Key Learnings:

1. bcrypt cost=12 is the right balance for our server performance
2. JWT refresh tokens need atomic rotation to prevent race conditions
`
	result, err := s.PassiveCapture(PassiveCaptureParams{
		SessionID: "s1",
		Content:   text,
		Project:   "engram",
		Source:    "test",
	})
	if err != nil {
		t.Fatalf("passive capture: %v", err)
	}
	if result.Extracted != 2 {
		t.Fatalf("expected 2 extracted, got %d", result.Extracted)
	}
	if result.Saved != 2 {
		t.Fatalf("expected 2 saved, got %d", result.Saved)
	}

	obs, err := s.AllObservations("engram", "", 10)
	if err != nil {
		t.Fatalf("all observations: %v", err)
	}
	if len(obs) != 2 {
		t.Fatalf("expected 2 observations, got %d", len(obs))
	}
	for _, o := range obs {
		if o.Type != "passive" {
			t.Fatalf("expected type=passive, got %q", o.Type)
		}
	}
	if obs[0].ToolName == nil || *obs[0].ToolName != "test" {
		t.Fatalf("expected tool_name source to be stored as 'test', got %+v", obs[0].ToolName)
	}
}

func TestPassiveCaptureEmptyContent(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	result, err := s.PassiveCapture(PassiveCaptureParams{
		SessionID: "s1",
		Content:   "",
		Project:   "engram",
		Source:    "test",
	})
	if err != nil {
		t.Fatalf("passive capture: %v", err)
	}
	if result.Extracted != 0 || result.Saved != 0 {
		t.Fatalf("expected 0 extracted and 0 saved, got %d/%d", result.Extracted, result.Saved)
	}
}

func TestPassiveCaptureDedupesAgainstExistingObservations(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// First: agent saves actively via mem_save
	_, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "decision",
		Title:     "bcrypt cost",
		Content:   "bcrypt cost=12 is the right balance for our server performance",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add active observation: %v", err)
	}

	// Then: passive capture fires with overlapping content
	text := `## Key Learnings:

1. bcrypt cost=12 is the right balance for our server performance
2. JWT refresh tokens need atomic rotation to prevent race conditions
`
	result, err := s.PassiveCapture(PassiveCaptureParams{
		SessionID: "s1",
		Content:   text,
		Project:   "engram",
		Source:    "test",
	})
	if err != nil {
		t.Fatalf("passive capture: %v", err)
	}
	if result.Extracted != 2 {
		t.Fatalf("expected 2 extracted, got %d", result.Extracted)
	}
	if result.Saved != 1 {
		t.Fatalf("expected 1 saved (1 deduped), got %d", result.Saved)
	}
	if result.Duplicates != 1 {
		t.Fatalf("expected 1 duplicate, got %d", result.Duplicates)
	}
}

func TestPassiveCaptureReturnsErrorWhenSessionDoesNotExist(t *testing.T) {
	s := newTestStore(t)

	text := `## Key Learnings:

1. This learning is long enough to attempt insert and fail without session
`
	_, err := s.PassiveCapture(PassiveCaptureParams{
		SessionID: "missing-session",
		Content:   text,
		Project:   "engram",
		Source:    "test",
	})
	if err == nil {
		t.Fatalf("expected error when session does not exist")
	}
}

func TestStatsProjectsOrderedByMostRecentObservation(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session s1: %v", err)
	}
	if err := s.CreateSession("s2", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session s2: %v", err)
	}

	_, err := s.db.Exec(
		`INSERT INTO observations (session_id, type, title, content, project, scope, normalized_hash, revision_count, duplicate_count, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, 1, 1, ?, ?),
		        (?, ?, ?, ?, ?, ?, ?, 1, 1, ?, ?)`,
		"s1", "note", "older", "older alpha", "alpha", "project", hashNormalized("older alpha"), "2026-02-01 10:00:00", "2026-02-01 10:00:00",
		"s2", "note", "newer", "newer beta", "beta", "project", hashNormalized("newer beta"), "2026-02-02 10:00:00", "2026-02-02 10:00:00",
	)
	if err != nil {
		t.Fatalf("insert observations: %v", err)
	}

	stats, err := s.Stats()
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if len(stats.Projects) < 2 {
		t.Fatalf("expected at least 2 projects, got %d", len(stats.Projects))
	}

	if stats.Projects[0] != "beta" || stats.Projects[1] != "alpha" {
		t.Fatalf("expected recency order [beta alpha], got %v", stats.Projects[:2])
	}
}

func TestStatsProjectScopesAllCounts(t *testing.T) {
	s := newTestStore(t)
	for _, project := range []string{"alpha", "beta"} {
		if err := s.CreateSession("session-"+project, project, "/tmp/"+project); err != nil {
			t.Fatalf("create %s session: %v", project, err)
		}
		if _, err := s.AddObservation(AddObservationParams{
			SessionID: "session-" + project,
			Type:      "note",
			Title:     "title " + project,
			Content:   "content " + project,
			Project:   project,
		}); err != nil {
			t.Fatalf("add %s observation: %v", project, err)
		}
		if _, err := s.AddPrompt(AddPromptParams{SessionID: "session-" + project, Content: "prompt " + project, Project: project}); err != nil {
			t.Fatalf("add %s prompt: %v", project, err)
		}
	}

	stats, err := s.StatsProject("ALPHA")
	if err != nil {
		t.Fatalf("stats project: %v", err)
	}
	if stats.TotalSessions != 1 || stats.TotalObservations != 1 || stats.TotalPrompts != 1 || !reflect.DeepEqual(stats.Projects, []string{"alpha"}) {
		t.Fatalf("scoped stats = %#v, want only alpha records", stats)
	}
	if _, err := s.StatsProject(" "); err == nil {
		t.Fatal("blank project stats must fail")
	}
}

func TestSessionsOrderedByMostRecentActivity(t *testing.T) {
	s := newTestStore(t)

	_, err := s.db.Exec(
		`INSERT INTO sessions (id, project, directory, started_at) VALUES
		 (?, ?, ?, ?),
		 (?, ?, ?, ?)`,
		"s-older", "engram", "/tmp/engram", "2026-02-01 09:00:00",
		"s-newer", "engram", "/tmp/engram", "2026-02-02 09:00:00",
	)
	if err != nil {
		t.Fatalf("insert sessions: %v", err)
	}

	_, err = s.db.Exec(
		`INSERT INTO observations (session_id, type, title, content, project, scope, normalized_hash, revision_count, duplicate_count, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, 1, 1, ?, ?)`,
		"s-older", "note", "latest", "session old got new activity", "engram", "project", hashNormalized("session old got new activity"), "2026-02-03 09:00:00", "2026-02-03 09:00:00",
	)
	if err != nil {
		t.Fatalf("insert latest observation: %v", err)
	}

	all, err := s.AllSessions("", 10)
	if err != nil {
		t.Fatalf("all sessions: %v", err)
	}
	if len(all) < 2 {
		t.Fatalf("expected at least 2 sessions, got %d", len(all))
	}
	if all[0].ID != "s-older" {
		t.Fatalf("expected s-older first in all sessions, got %s", all[0].ID)
	}

	recent, err := s.RecentSessions("", 10)
	if err != nil {
		t.Fatalf("recent sessions: %v", err)
	}
	if len(recent) < 2 {
		t.Fatalf("expected at least 2 recent sessions, got %d", len(recent))
	}
	if recent[0].ID != "s-older" {
		t.Fatalf("expected s-older first in recent sessions, got %s", recent[0].ID)
	}
}

func TestSessionObservationsAddPromptImportAndSyncChunks(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	_, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "decision",
		Title:     "Auth",
		Content:   "Use middleware chain",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}

	longPrompt := strings.Repeat("x", s.cfg.MaxObservationLength+25)
	promptID, err := s.AddPrompt(AddPromptParams{SessionID: "s1", Content: longPrompt, Project: "engram"})
	if err != nil {
		t.Fatalf("add prompt: %v", err)
	}
	if promptID <= 0 {
		t.Fatalf("expected valid prompt id, got %d", promptID)
	}

	sessionObs, err := s.SessionObservations("s1", 0)
	if err != nil {
		t.Fatalf("session observations: %v", err)
	}
	if len(sessionObs) != 1 {
		t.Fatalf("expected 1 session observation, got %d", len(sessionObs))
	}

	exported, err := s.Export()
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	cfg := mustDefaultConfig(t)
	cfg.DataDir = t.TempDir()
	dst, err := New(cfg)
	if err != nil {
		t.Fatalf("new destination store: %v", err)
	}
	t.Cleanup(func() { _ = dst.Close() })

	imported, err := dst.Import(exported)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if imported.SessionsImported < 1 || imported.ObservationsImported < 1 || imported.PromptsImported < 1 {
		t.Fatalf("expected non-zero import counts, got %+v", imported)
	}

	if err := dst.RecordSyncedChunk("chunk-1"); err != nil {
		t.Fatalf("record synced chunk: %v", err)
	}
	chunks, err := dst.GetSyncedChunks()
	if err != nil {
		t.Fatalf("get synced chunks: %v", err)
	}
	if !chunks["chunk-1"] {
		t.Fatalf("expected chunk-1 to be marked as synced")
	}

	if err := dst.RecordSyncedChunkForTarget(DefaultSyncTargetKey, "chunk-1"); err != nil {
		t.Fatalf("record cloud-target synced chunk: %v", err)
	}
	localChunks, err := dst.GetSyncedChunksForTarget(LocalChunkTargetKey)
	if err != nil {
		t.Fatalf("get local synced chunks: %v", err)
	}
	cloudChunks, err := dst.GetSyncedChunksForTarget(DefaultSyncTargetKey)
	if err != nil {
		t.Fatalf("get cloud synced chunks: %v", err)
	}
	if !localChunks["chunk-1"] {
		t.Fatal("expected chunk-1 to exist in local chunk target")
	}
	if !cloudChunks["chunk-1"] {
		t.Fatal("expected chunk-1 to exist in cloud chunk target")
	}
}

func TestStoreLocalSyncFoundationEnqueuesCoreMutations(t *testing.T) {
	s := newTestStore(t)

	// Enroll "engram" so mutations are visible via ListPendingSyncMutations.
	if err := s.EnrollProject("engram"); err != nil {
		t.Fatalf("enroll: %v", err)
	}

	if err := s.CreateSession("sync-session", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	obsID, err := s.AddObservation(AddObservationParams{
		SessionID: "sync-session",
		Type:      "decision",
		Title:     "Initial title",
		Content:   "Initial content",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}

	updatedTitle := "Updated title"
	updatedContent := "Updated content"
	if _, err := s.UpdateObservation(obsID, UpdateObservationParams{
		Title:   &updatedTitle,
		Content: &updatedContent,
	}); err != nil {
		t.Fatalf("update observation: %v", err)
	}

	if err := s.DeleteObservation(obsID, false); err != nil {
		t.Fatalf("soft delete observation: %v", err)
	}

	promptID, err := s.AddPrompt(AddPromptParams{
		SessionID: "sync-session",
		Content:   "How do we keep this local-first?",
		Project:   "engram",
	})
	if err != nil {
		t.Fatalf("add prompt: %v", err)
	}

	if err := s.EndSession("sync-session", "done"); err != nil {
		t.Fatalf("end session: %v", err)
	}

	state, err := s.GetSyncState(DefaultSyncTargetKey)
	if err != nil {
		t.Fatalf("get sync state: %v", err)
	}
	if state.TargetKey != DefaultSyncTargetKey {
		t.Fatalf("expected target %q, got %q", DefaultSyncTargetKey, state.TargetKey)
	}
	if state.Lifecycle != SyncLifecyclePending {
		t.Fatalf("expected pending lifecycle after local writes, got %q", state.Lifecycle)
	}
	if state.LastEnqueuedSeq != 6 {
		t.Fatalf("expected 6 enqueued mutations, got %d", state.LastEnqueuedSeq)
	}

	mutations, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 10)
	if err != nil {
		t.Fatalf("list pending sync mutations: %v", err)
	}
	if len(mutations) != 6 {
		t.Fatalf("expected 6 pending mutations, got %d", len(mutations))
	}

	var observationSyncID string
	if err := s.db.QueryRow("SELECT sync_id FROM observations WHERE id = ?", obsID).Scan(&observationSyncID); err != nil {
		t.Fatalf("lookup observation sync id: %v", err)
	}
	if observationSyncID == "" {
		t.Fatalf("expected observation sync id to be persisted")
	}

	var promptSyncID string
	if err := s.db.QueryRow("SELECT sync_id FROM user_prompts WHERE id = ?", promptID).Scan(&promptSyncID); err != nil {
		t.Fatalf("lookup prompt sync id: %v", err)
	}
	if promptSyncID == "" {
		t.Fatalf("expected prompt sync id to be persisted")
	}

	if mutations[0].Entity != SyncEntitySession || mutations[0].EntityKey != "sync-session" || mutations[0].Op != SyncOpUpsert {
		t.Fatalf("unexpected session mutation: %+v", mutations[0])
	}
	if mutations[1].Entity != SyncEntityObservation || mutations[1].EntityKey != observationSyncID || mutations[1].Op != SyncOpUpsert {
		t.Fatalf("unexpected observation insert mutation: %+v", mutations[1])
	}
	if mutations[2].Entity != SyncEntityObservation || mutations[2].EntityKey != observationSyncID || mutations[2].Op != SyncOpUpsert {
		t.Fatalf("unexpected observation update mutation: %+v", mutations[2])
	}
	if mutations[3].Entity != SyncEntityObservation || mutations[3].EntityKey != observationSyncID || mutations[3].Op != SyncOpDelete {
		t.Fatalf("unexpected observation delete mutation: %+v", mutations[3])
	}
	if mutations[4].Entity != SyncEntityPrompt || mutations[4].EntityKey != promptSyncID || mutations[4].Op != SyncOpUpsert {
		t.Fatalf("unexpected prompt mutation: %+v", mutations[4])
	}
	if mutations[5].Entity != SyncEntitySession || mutations[5].EntityKey != "sync-session" || mutations[5].Op != SyncOpUpsert {
		t.Fatalf("unexpected end session mutation: %+v", mutations[5])
	}

	var deletedPayload map[string]any
	if err := json.Unmarshal([]byte(mutations[3].Payload), &deletedPayload); err != nil {
		t.Fatalf("decode delete payload: %v", err)
	}
	if deletedPayload["sync_id"] != observationSyncID {
		t.Fatalf("expected delete payload sync id %q, got %#v", observationSyncID, deletedPayload["sync_id"])
	}
	if deletedPayload["deleted"] != true {
		t.Fatalf("expected delete payload to mark deleted=true, got %#v", deletedPayload["deleted"])
	}

	if err := s.AckSyncMutations(DefaultSyncTargetKey, mutations[3].Seq); err != nil {
		t.Fatalf("ack sync mutations: %v", err)
	}
	remaining, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 10)
	if err != nil {
		t.Fatalf("list remaining sync mutations: %v", err)
	}
	if len(remaining) != 2 || remaining[0].Entity != SyncEntityPrompt || remaining[1].Entity != SyncEntitySession {
		t.Fatalf("expected prompt and end-session mutations to remain pending, got %+v", remaining)
	}
}

func TestStoreLocalSyncFoundationStateHelpers(t *testing.T) {
	s := newTestStore(t)

	state, err := s.GetSyncState(DefaultSyncTargetKey)
	if err != nil {
		t.Fatalf("get initial sync state: %v", err)
	}
	if state.Lifecycle != SyncLifecycleIdle {
		t.Fatalf("expected idle lifecycle, got %q", state.Lifecycle)
	}

	acquired, err := s.AcquireSyncLease(DefaultSyncTargetKey, "worker-a", 2*time.Minute, time.Date(2026, 3, 7, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("acquire first lease: %v", err)
	}
	if !acquired {
		t.Fatalf("expected first lease acquisition to succeed")
	}

	acquired, err = s.AcquireSyncLease(DefaultSyncTargetKey, "worker-b", 2*time.Minute, time.Date(2026, 3, 7, 12, 1, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("acquire conflicting lease: %v", err)
	}
	if acquired {
		t.Fatalf("expected conflicting lease acquisition to fail")
	}

	if err := s.ReleaseSyncLease(DefaultSyncTargetKey, "worker-a"); err != nil {
		t.Fatalf("release lease: %v", err)
	}

	acquired, err = s.AcquireSyncLease(DefaultSyncTargetKey, "worker-b", 2*time.Minute, time.Date(2026, 3, 7, 12, 2, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("acquire released lease: %v", err)
	}
	if !acquired {
		t.Fatalf("expected lease acquisition after release to succeed")
	}

	if err := s.MarkSyncFailure(DefaultSyncTargetKey, "timeout talking to cloud", time.Date(2026, 3, 7, 12, 10, 0, 0, time.UTC)); err != nil {
		t.Fatalf("mark sync failure: %v", err)
	}

	state, err = s.GetSyncState(DefaultSyncTargetKey)
	if err != nil {
		t.Fatalf("get degraded sync state: %v", err)
	}
	if state.Lifecycle != SyncLifecycleDegraded {
		t.Fatalf("expected degraded lifecycle, got %q", state.Lifecycle)
	}
	if state.ConsecutiveFailures != 1 {
		t.Fatalf("expected failure count 1, got %d", state.ConsecutiveFailures)
	}
	if state.LastError == nil || *state.LastError != "timeout talking to cloud" {
		t.Fatalf("expected last error to be stored, got %+v", state.LastError)
	}
	if state.BackoffUntil == nil || *state.BackoffUntil != "2026-03-07T12:10:00Z" {
		t.Fatalf("expected backoff timestamp to be stored, got %+v", state.BackoffUntil)
	}

	if err := s.MarkSyncHealthy(DefaultSyncTargetKey); err != nil {
		t.Fatalf("mark sync healthy: %v", err)
	}

	state, err = s.GetSyncState(DefaultSyncTargetKey)
	if err != nil {
		t.Fatalf("get healthy sync state: %v", err)
	}
	if state.Lifecycle != SyncLifecycleHealthy {
		t.Fatalf("expected healthy lifecycle, got %q", state.Lifecycle)
	}
	if state.ConsecutiveFailures != 0 || state.LastError != nil || state.BackoffUntil != nil {
		t.Fatalf("expected healthy state to clear failure metadata, got %+v", state)
	}
}

func TestAckSyncMutationSeqsRefreshesProjectScopedState(t *testing.T) {
	s := newTestStore(t)
	if err := s.EnrollProject("proj-a"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}
	if err := s.CreateSession("sess-proj", "proj-a", "/tmp/proj-a"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := s.AddObservation(AddObservationParams{
		SessionID: "sess-proj",
		Type:      "note",
		Title:     "proj scoped",
		Content:   "pending",
		Project:   "proj-a",
		Scope:     "project",
	}); err != nil {
		t.Fatalf("add observation: %v", err)
	}

	mutations, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 20)
	if err != nil {
		t.Fatalf("list pending mutations: %v", err)
	}
	if len(mutations) < 2 {
		t.Fatalf("expected at least session + observation mutations, got %+v", mutations)
	}

	projectTarget := syncTargetKeyForProject("proj-a")
	before, err := s.GetSyncState(projectTarget)
	if err != nil {
		t.Fatalf("get project sync state before ack: %v", err)
	}
	if before.Lifecycle != SyncLifecyclePending {
		t.Fatalf("expected project lifecycle pending before ack, got %q", before.Lifecycle)
	}

	seqs := make([]int64, 0, len(mutations))
	for _, mutation := range mutations {
		if mutation.Project == "proj-a" {
			seqs = append(seqs, mutation.Seq)
		}
	}
	if len(seqs) == 0 {
		t.Fatalf("expected project-scoped pending mutations, got %+v", mutations)
	}

	if err := s.AckSyncMutationSeqs(DefaultSyncTargetKey, seqs); err != nil {
		t.Fatalf("ack project mutation seqs: %v", err)
	}

	after, err := s.GetSyncState(projectTarget)
	if err != nil {
		t.Fatalf("get project sync state after ack: %v", err)
	}
	if after.Lifecycle != SyncLifecycleHealthy {
		t.Fatalf("expected project lifecycle healthy after ack, got %q", after.Lifecycle)
	}
	if after.LastAckedSeq < after.LastEnqueuedSeq {
		t.Fatalf("expected project ack counters reconciled, got enqueued=%d acked=%d", after.LastEnqueuedSeq, after.LastAckedSeq)
	}
	if after.ReasonCode != nil || after.ReasonMessage != nil || after.LastError != nil || after.BackoffUntil != nil {
		t.Fatalf("expected ack reconciliation to clear degraded metadata for healthy state, got %+v", after)
	}

	hasPending, err := s.HasPendingSyncMutationsForProject("proj-a")
	if err != nil {
		t.Fatalf("pending project mutations query: %v", err)
	}
	if hasPending {
		t.Fatalf("expected no pending project mutations after ack")
	}
}

func TestAckSyncMutationsRefreshesProjectStateAndClearsDegradedMetadata(t *testing.T) {
	s := newTestStore(t)
	if err := s.EnrollProject("proj-a"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}
	if err := s.CreateSession("sess-proj", "proj-a", "/tmp/proj-a"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := s.AddObservation(AddObservationParams{
		SessionID: "sess-proj",
		Type:      "note",
		Title:     "proj scoped",
		Content:   "pending",
		Project:   "proj-a",
		Scope:     "project",
	}); err != nil {
		t.Fatalf("add observation: %v", err)
	}

	projectTarget := syncTargetKeyForProject("proj-a")
	if err := s.MarkSyncFailure(projectTarget, "seed degraded before ack", time.Now().UTC().Add(-45*time.Second)); err != nil {
		t.Fatalf("seed degraded project state: %v", err)
	}

	allMutations, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 20)
	if err != nil {
		t.Fatalf("list pending mutations: %v", err)
	}
	var maxProjectSeq int64
	for _, mutation := range allMutations {
		if mutation.Project == "proj-a" && mutation.Seq > maxProjectSeq {
			maxProjectSeq = mutation.Seq
		}
	}
	if maxProjectSeq == 0 {
		t.Fatalf("expected pending project mutations, got %+v", allMutations)
	}

	if err := s.AckSyncMutations(DefaultSyncTargetKey, maxProjectSeq); err != nil {
		t.Fatalf("ack sync mutations: %v", err)
	}

	state, err := s.GetSyncState(projectTarget)
	if err != nil {
		t.Fatalf("get project sync state after ack: %v", err)
	}
	if state.Lifecycle != SyncLifecycleHealthy {
		t.Fatalf("expected project lifecycle healthy after full ack, got %q", state.Lifecycle)
	}
	if state.ReasonCode != nil || state.ReasonMessage != nil || state.LastError != nil || state.BackoffUntil != nil {
		t.Fatalf("expected healthy project state to clear degraded metadata, got %+v", state)
	}
}

func TestAckSyncMutationsPreservesActivelyDegradedProjectState(t *testing.T) {
	s := newTestStore(t)
	targetKey := syncTargetKeyForProject("proj-a")
	if err := s.MarkSyncBlocked(targetKey, "blocked_unenrolled", "project is blocked by policy"); err != nil {
		t.Fatalf("seed actively degraded state: %v", err)
	}

	if err := s.AckSyncMutations(DefaultSyncTargetKey, 1); err != nil {
		t.Fatalf("ack sync mutations: %v", err)
	}

	state, err := s.GetSyncState(targetKey)
	if err != nil {
		t.Fatalf("get sync state: %v", err)
	}
	if state.Lifecycle != SyncLifecycleDegraded {
		t.Fatalf("expected actively degraded state to remain degraded, got %q", state.Lifecycle)
	}
	if state.ReasonCode == nil || *state.ReasonCode != "blocked_unenrolled" {
		t.Fatalf("expected blocked_unenrolled reason to be preserved, got %v", state.ReasonCode)
	}
}

func TestSyncStateDeterministicReasonCodes(t *testing.T) {
	s := newTestStore(t)

	tests := []struct {
		name      string
		mark      func() error
		reason    string
		msgSubstr string
	}{
		{
			name: "blocked unenrolled",
			mark: func() error {
				return s.MarkSyncBlocked(DefaultSyncTargetKey, "blocked_unenrolled", "project not enrolled for cloud replication")
			},
			reason:    "blocked_unenrolled",
			msgSubstr: "not enrolled",
		},
		{
			name: "paused",
			mark: func() error {
				return s.MarkSyncPaused(DefaultSyncTargetKey, "cloud sync paused by organization policy")
			},
			reason:    "paused",
			msgSubstr: "paused",
		},
		{
			name: "auth required",
			mark: func() error {
				return s.MarkSyncAuthRequired(DefaultSyncTargetKey, "cloud token is missing")
			},
			reason:    "auth_required",
			msgSubstr: "missing",
		},
		{
			name: "transport failed",
			mark: func() error {
				return s.MarkSyncFailure(DefaultSyncTargetKey, "dial tcp timeout", time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC))
			},
			reason:    "transport_failed",
			msgSubstr: "timeout",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.mark(); err != nil {
				t.Fatalf("mark sync state: %v", err)
			}

			state, err := s.GetSyncState(DefaultSyncTargetKey)
			if err != nil {
				t.Fatalf("get sync state: %v", err)
			}
			if state.ReasonCode == nil || *state.ReasonCode != tc.reason {
				t.Fatalf("expected reason_code=%q, got %+v", tc.reason, state.ReasonCode)
			}
			if state.ReasonMessage == nil || !strings.Contains(*state.ReasonMessage, tc.msgSubstr) {
				t.Fatalf("expected reason_message containing %q, got %+v", tc.msgSubstr, state.ReasonMessage)
			}
		})
	}

	if err := s.MarkSyncHealthy(DefaultSyncTargetKey); err != nil {
		t.Fatalf("mark healthy: %v", err)
	}
	state, err := s.GetSyncState(DefaultSyncTargetKey)
	if err != nil {
		t.Fatalf("get sync state: %v", err)
	}
	if state.ReasonCode != nil || state.ReasonMessage != nil {
		t.Fatalf("expected healthy state to clear reasons, got code=%v message=%v", state.ReasonCode, state.ReasonMessage)
	}
}

func TestUpgradeStateSnapshotLifecycle(t *testing.T) {
	s := newTestStore(t)
	project := "upgrade-proj"

	initial, err := s.GetCloudUpgradeState(project)
	if err != nil {
		t.Fatalf("get initial cloud upgrade state: %v", err)
	}
	if initial != nil {
		t.Fatalf("expected nil initial upgrade state, got %+v", initial)
	}

	snapshot := CloudUpgradeSnapshot{
		Captured:        true,
		ProjectEnrolled: false,
	}
	state := CloudUpgradeState{
		Project:          project,
		Stage:            UpgradeStageBootstrapEnrolled,
		RepairClass:      UpgradeRepairClassRepairable,
		Snapshot:         snapshot,
		LastErrorCode:    "upgrade_blocked_manual",
		LastErrorMessage: "manual fix required",
	}
	if err := s.SaveCloudUpgradeState(state); err != nil {
		t.Fatalf("save upgrade state: %v", err)
	}

	stored, err := s.GetCloudUpgradeState(project)
	if err != nil {
		t.Fatalf("get stored cloud upgrade state: %v", err)
	}
	if stored == nil {
		t.Fatal("expected stored upgrade state")
	}
	if stored.Stage != UpgradeStageBootstrapEnrolled {
		t.Fatalf("expected stage %q, got %q", UpgradeStageBootstrapEnrolled, stored.Stage)
	}
	if !stored.Snapshot.Captured || stored.Snapshot.ProjectEnrolled {
		t.Fatalf("expected snapshot to roundtrip, got %+v", stored.Snapshot)
	}

	allowed, err := s.CanRollbackCloudUpgrade(project)
	if err != nil {
		t.Fatalf("can rollback before verification: %v", err)
	}
	if !allowed {
		t.Fatal("expected rollback allowed before bootstrap verification")
	}

	state.Stage = UpgradeStageBootstrapVerified
	if err := s.SaveCloudUpgradeState(state); err != nil {
		t.Fatalf("save verified stage: %v", err)
	}

	allowed, err = s.CanRollbackCloudUpgrade(project)
	if err != nil {
		t.Fatalf("can rollback after verification: %v", err)
	}
	if allowed {
		t.Fatal("expected rollback blocked after bootstrap verification")
	}

	if err := s.ClearCloudUpgradeState(project); err != nil {
		t.Fatalf("clear upgrade state: %v", err)
	}

	afterClear, err := s.GetCloudUpgradeState(project)
	if err != nil {
		t.Fatalf("get cleared upgrade state: %v", err)
	}
	if afterClear != nil {
		t.Fatalf("expected nil upgrade state after clear, got %+v", afterClear)
	}
}

func TestCloudUpgradeSnapshotMigrationRedactsAndPreservesOnlyTrustedCaptures(t *testing.T) {
	cfg := mustDefaultConfig(t)
	cfg.DataDir = t.TempDir()

	s, err := New(cfg)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close store before legacy seed: %v", err)
	}

	const token = "test-legacy-token-must-not-remain-in-sqlite"
	testCases := []struct {
		project  string
		stage    string
		snapshot string
		want     CloudUpgradeSnapshot
	}{
		{"legacy-captured", UpgradeStageDoctorReady, fmt.Sprintf(`{"cloud_config_present":true,"cloud_config_json":"%s","project_enrolled":true}`, token), CloudUpgradeSnapshot{Captured: true, ProjectEnrolled: true}},
		{"legacy-doctor", UpgradeStageDoctorBlocked, `{"cloud_config_present":false,"project_enrolled":false}`, CloudUpgradeSnapshot{}},
		{"legacy-repair", UpgradeStageRepairApplied, `{"cloud_config_present":false,"project_enrolled":false}`, CloudUpgradeSnapshot{}},
		{"legacy-bootstrap-enrolled", UpgradeStageBootstrapEnrolled, `{"cloud_config_present":true,"project_enrolled":false}`, CloudUpgradeSnapshot{Captured: true}},
		{"legacy-bootstrap-pushed", UpgradeStageBootstrapPushed, `{"cloud_config_present":true,"project_enrolled":false}`, CloudUpgradeSnapshot{Captured: true}},
		{"current-doctor", UpgradeStageDoctorReady, `{"captured":false,"project_enrolled":false}`, CloudUpgradeSnapshot{}},
		{"current-repair", UpgradeStageRepairApplied, `{"captured":false,"project_enrolled":false}`, CloudUpgradeSnapshot{}},
		{"malformed-enrolled", UpgradeStageBootstrapEnrolled, `{"captured":`, CloudUpgradeSnapshot{}},
		{"malformed-pushed", UpgradeStageBootstrapPushed, `{"captured":`, CloudUpgradeSnapshot{}},
	}
	raw, err := sql.Open("sqlite", filepath.Join(cfg.DataDir, "engram.db"))
	if err != nil {
		t.Fatalf("open raw store: %v", err)
	}
	for _, tc := range testCases {
		if _, err := raw.Exec(`INSERT INTO cloud_upgrade_state (project, stage, snapshot_json) VALUES (?, ?, ?)`, tc.project, tc.stage, tc.snapshot); err != nil {
			_ = raw.Close()
			t.Fatalf("seed %s snapshot: %v", tc.project, err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw store: %v", err)
	}

	s, err = New(cfg)
	if err != nil {
		t.Fatalf("reopen migrated store: %v", err)
	}

	for _, tc := range testCases {
		state, err := s.GetCloudUpgradeState(tc.project)
		if err != nil || state == nil || state.Snapshot != tc.want {
			t.Fatalf("migrate %s snapshot: state=%+v err=%v", tc.project, state, err)
		}
		if tc.want.Captured {
			continue
		}
		allowed, err := s.CanRollbackCloudUpgrade(tc.project)
		if err != nil || allowed {
			t.Fatalf("uncaptured %s snapshot must not allow rollback: allowed=%t err=%v", tc.project, allowed, err)
		}
	}
	var snapshotJSON string
	if err := s.DB().QueryRow(`SELECT snapshot_json FROM cloud_upgrade_state WHERE project = ?`, "legacy-captured").Scan(&snapshotJSON); err != nil || strings.Contains(snapshotJSON, token) || strings.Contains(snapshotJSON, `"token"`) || strings.Contains(snapshotJSON, "cloud_config") {
		t.Fatalf("legacy credential material remained in snapshot: %s (%v)", snapshotJSON, err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close migrated store: %v", err)
	}
	s, err = New(cfg)
	if err != nil {
		t.Fatalf("reopen idempotently redacted store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	state, err := s.GetCloudUpgradeState("legacy-captured")
	if err != nil || state == nil || !state.Snapshot.Captured || !state.Snapshot.ProjectEnrolled {
		t.Fatalf("idempotent migration lost captured snapshot: state=%+v err=%v", state, err)
	}
}

func TestUpgradeRepairDryRunAndApply(t *testing.T) {
	t.Run("dry-run is deterministic and non-mutating", func(t *testing.T) {
		s := newTestStore(t)
		if err := s.CreateSession("repair-s1", "repair-proj", "/tmp/repair"); err != nil {
			t.Fatalf("create session: %v", err)
		}
		if _, err := s.AddObservation(AddObservationParams{SessionID: "repair-s1", Type: "decision", Title: "t", Content: "c", Project: "repair-proj", Scope: "project"}); err != nil {
			t.Fatalf("add observation: %v", err)
		}
		if _, err := s.AddPrompt(AddPromptParams{SessionID: "repair-s1", Content: "p", Project: "repair-proj"}); err != nil {
			t.Fatalf("add prompt: %v", err)
		}
		if err := s.EnrollProject("repair-proj"); err != nil {
			t.Fatalf("enroll project: %v", err)
		}

		if _, err := s.execHook(s.db, `
			DELETE FROM sync_mutations
			WHERE seq IN (
				SELECT seq FROM sync_mutations WHERE project = ? AND entity = ? ORDER BY seq ASC LIMIT 1
			)
		`, "repair-proj", SyncEntityObservation); err != nil {
			t.Fatalf("delete mutation for repair setup: %v", err)
		}

		beforeCount := 0
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM sync_mutations WHERE project = ?`, "repair-proj").Scan(&beforeCount); err != nil {
			t.Fatalf("count before dry-run: %v", err)
		}

		report1, err := s.RepairCloudUpgrade("repair-proj", false)
		if err != nil {
			t.Fatalf("dry-run repair: %v", err)
		}
		report2, err := s.RepairCloudUpgrade("repair-proj", false)
		if err != nil {
			t.Fatalf("second dry-run repair: %v", err)
		}
		if report1 != report2 {
			t.Fatalf("expected deterministic dry-run report, got %+v and %+v", report1, report2)
		}
		if report1.Class != UpgradeRepairClassRepairable {
			t.Fatalf("expected repairable class, got %+v", report1)
		}

		afterCount := 0
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM sync_mutations WHERE project = ?`, "repair-proj").Scan(&afterCount); err != nil {
			t.Fatalf("count after dry-run: %v", err)
		}
		if beforeCount != afterCount {
			t.Fatalf("dry-run must not mutate local state, before=%d after=%d", beforeCount, afterCount)
		}
	})

	t.Run("apply backfills safe local fixes", func(t *testing.T) {
		s := newTestStore(t)
		if err := s.CreateSession("repair-s2", "repair-apply", "/tmp/repair-apply"); err != nil {
			t.Fatalf("create session: %v", err)
		}
		if _, err := s.AddObservation(AddObservationParams{SessionID: "repair-s2", Type: "decision", Title: "t", Content: "c", Project: "repair-apply", Scope: "project"}); err != nil {
			t.Fatalf("add observation: %v", err)
		}
		if err := s.EnrollProject("repair-apply"); err != nil {
			t.Fatalf("enroll project: %v", err)
		}
		if _, err := s.execHook(s.db, `
			DELETE FROM sync_mutations
			WHERE seq IN (
				SELECT seq FROM sync_mutations WHERE project = ? AND entity = ? ORDER BY seq ASC LIMIT 1
			)
		`, "repair-apply", SyncEntityObservation); err != nil {
			t.Fatalf("delete mutation for apply setup: %v", err)
		}

		report, err := s.RepairCloudUpgrade("repair-apply", true)
		if err != nil {
			t.Fatalf("apply repair: %v", err)
		}
		if report.Class != UpgradeRepairClassRepairable || !report.Applied {
			t.Fatalf("expected applied repairable result, got %+v", report)
		}

		pending, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 20)
		if err != nil {
			t.Fatalf("list pending mutations: %v", err)
		}
		foundObservation := false
		for _, mutation := range pending {
			if mutation.Project == "repair-apply" && mutation.Entity == SyncEntityObservation {
				foundObservation = true
				break
			}
		}
		if !foundObservation {
			t.Fatal("expected observation mutation to be backfilled by apply")
		}
	})

	t.Run("blocked ambiguity is not auto-mutated", func(t *testing.T) {
		s := newTestStore(t)
		report, err := s.RepairCloudUpgrade("unregistered-proj", true)
		if err != nil {
			t.Fatalf("blocked repair report: %v", err)
		}
		if report.Class != UpgradeRepairClassBlocked || report.Applied {
			t.Fatalf("expected blocked non-applied report, got %+v", report)
		}
	})

	t.Run("auth and policy blockers are manual-action-required", func(t *testing.T) {
		tests := []struct {
			name       string
			reasonCode string
			message    string
			wantClass  string
		}{
			{name: "auth required", reasonCode: "auth_required", message: "token expired", wantClass: UpgradeRepairClassPolicy},
			{name: "policy forbidden", reasonCode: "policy_forbidden", message: "project denied by org policy", wantClass: UpgradeRepairClassPolicy},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				s := newTestStore(t)
				if err := s.MarkSyncBlocked("cloud:repair-policy", tc.reasonCode, tc.message); err != nil {
					t.Fatalf("seed sync blocked state: %v", err)
				}

				report, err := s.RepairCloudUpgrade("repair-policy", true)
				if err != nil {
					t.Fatalf("repair report: %v", err)
				}
				if report.Class != tc.wantClass || report.Applied {
					t.Fatalf("expected class=%s applied=false, got %+v", tc.wantClass, report)
				}
				if !strings.Contains(report.Message, "manual-action-required") {
					t.Fatalf("expected manual-action-required guidance, got %q", report.Message)
				}
			})
		}
	})

	t.Run("legacy mutation required fields are detected and repaired from authoritative local state", func(t *testing.T) {
		s := newTestStore(t)
		if err := s.CreateSession("legacy-s1", "legacy-proj", "/tmp/legacy"); err != nil {
			t.Fatalf("create session: %v", err)
		}
		if _, err := s.AddObservation(AddObservationParams{SessionID: "legacy-s1", Type: "decision", Title: "Authoritative title", Content: "Authoritative content", Project: "legacy-proj", Scope: "project"}); err != nil {
			t.Fatalf("add observation: %v", err)
		}
		if err := s.EnrollProject("legacy-proj"); err != nil {
			t.Fatalf("enroll project: %v", err)
		}

		var syncID string
		if err := s.db.QueryRow(`SELECT sync_id FROM observations WHERE session_id = ? ORDER BY id DESC LIMIT 1`, "legacy-s1").Scan(&syncID); err != nil {
			t.Fatalf("lookup observation sync id: %v", err)
		}

		payload := `{"sync_id":"` + syncID + `","session_id":"legacy-s1","type":"decision","content":"legacy payload missing title","scope":"project"}`
		if _, err := s.execHook(s.db,
			`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			DefaultSyncTargetKey,
			SyncEntityObservation,
			syncID,
			SyncOpUpsert,
			payload,
			SyncSourceLocal,
			"legacy-proj",
		); err != nil {
			t.Fatalf("insert malformed legacy mutation: %v", err)
		}

		diagnosis, err := s.DiagnoseCloudUpgradeLegacyMutations("legacy-proj")
		if err != nil {
			t.Fatalf("diagnose legacy mutations: %v", err)
		}
		if diagnosis.RepairableCount == 0 || diagnosis.BlockedCount != 0 {
			t.Fatalf("expected repairable-only diagnosis, got %+v", diagnosis)
		}
		if len(diagnosis.Findings) == 0 || !diagnosis.Findings[0].Repairable {
			t.Fatalf("expected at least one repairable finding, got %+v", diagnosis.Findings)
		}

		report, err := s.RepairCloudUpgrade("legacy-proj", true)
		if err != nil {
			t.Fatalf("repair legacy payload gaps: %v", err)
		}
		if report.Class != UpgradeRepairClassRepairable || !report.Applied {
			t.Fatalf("expected applied repairable result, got %+v", report)
		}

		var repairedPayload string
		if err := s.db.QueryRow(`
			SELECT payload FROM sync_mutations
			WHERE target_key = ? AND project = ? AND entity = ? AND entity_key = ? AND op = ?
			ORDER BY seq DESC LIMIT 1
		`, DefaultSyncTargetKey, "legacy-proj", SyncEntityObservation, syncID, SyncOpUpsert).Scan(&repairedPayload); err != nil {
			t.Fatalf("load repaired payload: %v", err)
		}
		var repaired syncObservationPayload
		if err := decodeSyncPayload([]byte(repairedPayload), &repaired); err != nil {
			t.Fatalf("decode repaired payload: %v", err)
		}
		if strings.TrimSpace(repaired.Title) == "" {
			t.Fatalf("expected repaired payload title from authoritative local observation, got %+v", repaired)
		}

		after, err := s.DiagnoseCloudUpgradeLegacyMutations("legacy-proj")
		if err != nil {
			t.Fatalf("diagnose after repair: %v", err)
		}
		if after.RepairableCount != 0 || after.BlockedCount != 0 || len(after.Findings) != 0 {
			t.Fatalf("expected no remaining legacy findings after repair, got %+v", after)
		}
	})

	t.Run("legacy relation mutation payload is repaired from authoritative local relation", func(t *testing.T) {
		s := newTestStore(t)
		if err := s.CreateSession("legacy-rel-s1", "legacy-rel-proj", "/tmp/legacy-rel"); err != nil {
			t.Fatalf("create session: %v", err)
		}
		sourceID, err := s.AddObservation(AddObservationParams{SessionID: "legacy-rel-s1", Type: "decision", Title: "Source", Content: "Source content", Project: "legacy-rel-proj", Scope: "project"})
		if err != nil {
			t.Fatalf("add source observation: %v", err)
		}
		targetID, err := s.AddObservation(AddObservationParams{SessionID: "legacy-rel-s1", Type: "decision", Title: "Target", Content: "Target content", Project: "legacy-rel-proj", Scope: "project"})
		if err != nil {
			t.Fatalf("add target observation: %v", err)
		}
		if err := s.EnrollProject("legacy-rel-proj"); err != nil {
			t.Fatalf("enroll project: %v", err)
		}

		var sourceSyncID, targetSyncID string
		if err := s.db.QueryRow(`SELECT sync_id FROM observations WHERE id = ?`, sourceID).Scan(&sourceSyncID); err != nil {
			t.Fatalf("lookup source sync id: %v", err)
		}
		if err := s.db.QueryRow(`SELECT sync_id FROM observations WHERE id = ?`, targetID).Scan(&targetSyncID); err != nil {
			t.Fatalf("lookup target sync id: %v", err)
		}
		rel, err := s.SaveRelation(SaveRelationParams{SyncID: "rel-legacy-repair", SourceID: sourceSyncID, TargetID: targetSyncID})
		if err != nil {
			t.Fatalf("save relation: %v", err)
		}
		reason := "same decision"
		if _, err := s.JudgeRelation(JudgeRelationParams{
			JudgmentID:    rel.SyncID,
			Relation:      RelationCompatible,
			Reason:        &reason,
			MarkedByActor: "engram-test",
			MarkedByKind:  "system",
			SessionID:     "legacy-rel-s1",
		}); err != nil {
			t.Fatalf("judge relation: %v", err)
		}

		legacyPayload := `{"sync_id":"rel-legacy-repair","source_id":"` + sourceSyncID + `","target_id":"` + targetSyncID + `","relation":"compatible"}`
		if _, err := s.execHook(s.db, `
			UPDATE sync_mutations
			SET payload = ?
			WHERE target_key = ? AND project = ? AND entity = ? AND entity_key = ? AND op = ? AND acked_at IS NULL
		`, legacyPayload, DefaultSyncTargetKey, "legacy-rel-proj", SyncEntityRelation, rel.SyncID, SyncOpUpsert); err != nil {
			t.Fatalf("seed legacy relation payload: %v", err)
		}

		diagnosis, err := s.DiagnoseCloudUpgradeLegacyMutations("legacy-rel-proj")
		if err != nil {
			t.Fatalf("diagnose relation legacy mutation: %v", err)
		}
		if diagnosis.RepairableCount == 0 || diagnosis.BlockedCount != 0 {
			t.Fatalf("expected relation payload to be repairable-only, got %+v", diagnosis)
		}

		report, err := s.RepairCloudUpgrade("legacy-rel-proj", true)
		if err != nil {
			t.Fatalf("repair relation legacy payload: %v", err)
		}
		if report.Class != UpgradeRepairClassRepairable || !report.Applied {
			t.Fatalf("expected applied repairable relation result, got %+v", report)
		}

		var repairedPayload string
		if err := s.db.QueryRow(`
			SELECT payload FROM sync_mutations
			WHERE target_key = ? AND project = ? AND entity = ? AND entity_key = ? AND op = ?
			ORDER BY seq DESC LIMIT 1
		`, DefaultSyncTargetKey, "legacy-rel-proj", SyncEntityRelation, rel.SyncID, SyncOpUpsert).Scan(&repairedPayload); err != nil {
			t.Fatalf("load repaired relation payload: %v", err)
		}
		var repaired syncRelationPayload
		if err := decodeSyncPayload([]byte(repairedPayload), &repaired); err != nil {
			t.Fatalf("decode repaired relation payload: %v", err)
		}
		if strings.TrimSpace(repaired.JudgmentStatus) == "" || repaired.MarkedByActor == nil || strings.TrimSpace(*repaired.MarkedByActor) == "" || repaired.MarkedByKind == nil || strings.TrimSpace(*repaired.MarkedByKind) == "" || strings.TrimSpace(repaired.Project) == "" {
			t.Fatalf("expected repaired payload to include required relation fields, got %+v", repaired)
		}
	})

	t.Run("legacy relation mutation stays blocked when provenance cannot be inferred", func(t *testing.T) {
		s := newTestStore(t)
		if err := s.CreateSession("legacy-rel-blocked-s1", "legacy-rel-blocked", "/tmp/legacy-rel-blocked"); err != nil {
			t.Fatalf("create session: %v", err)
		}
		sourceID, err := s.AddObservation(AddObservationParams{SessionID: "legacy-rel-blocked-s1", Type: "decision", Title: "Source", Content: "Source content", Project: "legacy-rel-blocked", Scope: "project"})
		if err != nil {
			t.Fatalf("add source observation: %v", err)
		}
		targetID, err := s.AddObservation(AddObservationParams{SessionID: "legacy-rel-blocked-s1", Type: "decision", Title: "Target", Content: "Target content", Project: "legacy-rel-blocked", Scope: "project"})
		if err != nil {
			t.Fatalf("add target observation: %v", err)
		}
		if err := s.EnrollProject("legacy-rel-blocked"); err != nil {
			t.Fatalf("enroll project: %v", err)
		}

		var sourceSyncID, targetSyncID string
		if err := s.db.QueryRow(`SELECT sync_id FROM observations WHERE id = ?`, sourceID).Scan(&sourceSyncID); err != nil {
			t.Fatalf("lookup source sync id: %v", err)
		}
		if err := s.db.QueryRow(`SELECT sync_id FROM observations WHERE id = ?`, targetID).Scan(&targetSyncID); err != nil {
			t.Fatalf("lookup target sync id: %v", err)
		}
		if _, err := s.SaveRelation(SaveRelationParams{SyncID: "rel-legacy-blocked", SourceID: sourceSyncID, TargetID: targetSyncID}); err != nil {
			t.Fatalf("save relation: %v", err)
		}
		payload := `{"sync_id":"rel-legacy-blocked","source_id":"` + sourceSyncID + `","target_id":"` + targetSyncID + `","relation":"compatible","project":"legacy-rel-blocked"}`
		if _, err := s.execHook(s.db,
			`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			DefaultSyncTargetKey,
			SyncEntityRelation,
			"rel-legacy-blocked",
			SyncOpUpsert,
			payload,
			SyncSourceLocal,
			"legacy-rel-blocked",
		); err != nil {
			t.Fatalf("insert relation mutation: %v", err)
		}

		diagnosis, err := s.DiagnoseCloudUpgradeLegacyMutations("legacy-rel-blocked")
		if err != nil {
			t.Fatalf("diagnose blocked relation legacy mutation: %v", err)
		}
		if diagnosis.BlockedCount != 1 || diagnosis.RepairableCount != 0 || !strings.Contains(diagnosis.Findings[0].Message, "marked_by_actor") || !strings.Contains(diagnosis.Findings[0].Message, "marked_by_kind") {
			t.Fatalf("expected missing provenance to remain blocked, got %+v", diagnosis)
		}
	})

	// Regression test for GitHub issue #446: when both repairable and blocked
	// mutations coexist, RepairCloudUpgrade(apply=true) must apply the
	// repairable subset and return Applied:true with Class=Blocked (and the
	// message must reference the actual blocker, not the low-seq repairable
	// entry that happens to be first in Findings order).
	t.Run("partial apply: repairable mutations applied even when a blocker is queued", func(t *testing.T) {
		s := newTestStore(t)
		if err := s.CreateSession("partial-repair-s1", "partial-repair-proj", "/tmp/partial-repair"); err != nil {
			t.Fatalf("create session: %v", err)
		}

		// Create an observation so we have authoritative local state for the
		// repairable mutation.
		obsID, err := s.AddObservation(AddObservationParams{
			SessionID: "partial-repair-s1",
			Type:      "decision",
			Title:     "Authoritative title",
			Content:   "Authoritative content",
			Project:   "partial-repair-proj",
			Scope:     "project",
		})
		if err != nil {
			t.Fatalf("add observation: %v", err)
		}
		if err := s.EnrollProject("partial-repair-proj"); err != nil {
			t.Fatalf("enroll project: %v", err)
		}

		// Look up the observation's sync_id for payload construction.
		var obsSyncID string
		if err := s.db.QueryRow(`SELECT sync_id FROM observations WHERE id = ?`, obsID).Scan(&obsSyncID); err != nil {
			t.Fatalf("lookup observation sync_id: %v", err)
		}

		// Insert a REPAIRABLE observation mutation (missing title — low seq,
		// will naturally come before the blocker we insert next).
		repairablePayload := `{"sync_id":"` + obsSyncID + `","session_id":"partial-repair-s1","type":"decision","content":"legacy payload missing title","scope":"project"}`
		if _, err := s.execHook(s.db,
			`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			DefaultSyncTargetKey,
			SyncEntityObservation,
			obsSyncID,
			SyncOpUpsert,
			repairablePayload,
			SyncSourceLocal,
			"partial-repair-proj",
		); err != nil {
			t.Fatalf("insert repairable observation mutation: %v", err)
		}

		// Create two observations for source/target of the relation we use as
		// the blocker mutation.
		srcID, err := s.AddObservation(AddObservationParams{SessionID: "partial-repair-s1", Type: "decision", Title: "Src", Content: "src", Project: "partial-repair-proj", Scope: "project"})
		if err != nil {
			t.Fatalf("add source observation: %v", err)
		}
		dstID, err := s.AddObservation(AddObservationParams{SessionID: "partial-repair-s1", Type: "decision", Title: "Dst", Content: "dst", Project: "partial-repair-proj", Scope: "project"})
		if err != nil {
			t.Fatalf("add dest observation: %v", err)
		}
		var srcSyncID, dstSyncID string
		if err := s.db.QueryRow(`SELECT sync_id FROM observations WHERE id = ?`, srcID).Scan(&srcSyncID); err != nil {
			t.Fatalf("lookup src sync_id: %v", err)
		}
		if err := s.db.QueryRow(`SELECT sync_id FROM observations WHERE id = ?`, dstID).Scan(&dstSyncID); err != nil {
			t.Fatalf("lookup dst sync_id: %v", err)
		}

		// Insert a BLOCKED relation mutation (missing provenance — high seq,
		// comes after the repairable entry so Findings[0] would point to the
		// repairable entry without the fix).
		const blockerEntityKey = "rel-partial-blocked-446"
		blockerPayload := `{"sync_id":"` + blockerEntityKey + `","source_id":"` + srcSyncID + `","target_id":"` + dstSyncID + `","relation":"compatible","project":"partial-repair-proj"}`
		if _, err := s.execHook(s.db,
			`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			DefaultSyncTargetKey,
			SyncEntityRelation,
			blockerEntityKey,
			SyncOpUpsert,
			blockerPayload,
			SyncSourceLocal,
			"partial-repair-proj",
		); err != nil {
			t.Fatalf("insert blocked relation mutation: %v", err)
		}

		// Confirm pre-conditions: diagnosis must see exactly one repairable
		// and one blocked entry.
		diag, err := s.DiagnoseCloudUpgradeLegacyMutations("partial-repair-proj")
		if err != nil {
			t.Fatalf("pre-condition diagnose: %v", err)
		}
		if diag.RepairableCount != 1 || diag.BlockedCount != 1 {
			t.Fatalf("expected 1 repairable + 1 blocked pre-condition, got %+v", diag)
		}

		// Find the seq of the blocked finding so we can assert the message
		// references the right entry.
		var blockerSeq int64
		for _, f := range diag.Findings {
			if !f.Repairable {
				blockerSeq = f.Seq
				break
			}
		}
		if blockerSeq == 0 {
			t.Fatal("pre-condition: could not locate blocked finding seq")
		}
		// Also verify that Findings[0] is NOT the blocker (i.e. lowest-seq is
		// the repairable one); this is the exact condition that triggered the
		// wrong-message bug.
		if diag.Findings[0].Seq == blockerSeq {
			t.Fatalf("pre-condition: expected Findings[0] to be repairable (lowest seq), but got blocker seq=%d", blockerSeq)
		}

		// === THE ACTUAL ASSERTION ===
		// With apply=true, repairable mutations must be applied and the
		// report must still surface the blocker clearly.
		report, err := s.RepairCloudUpgrade("partial-repair-proj", true)
		if err != nil {
			t.Fatalf("repair with mixed repairable+blocker: %v", err)
		}

		// Applied MUST be true: at least the repairable mutation was applied.
		if !report.Applied {
			t.Fatalf("expected Applied=true (repairable subset was processed), got %+v", report)
		}
		// Class MUST be Blocked because the non-repairable mutation is still present.
		if report.Class != UpgradeRepairClassBlocked {
			t.Fatalf("expected Class=%q, got %q (full report: %+v)", UpgradeRepairClassBlocked, report.Class, report)
		}
		// The message must name the BLOCKER seq, not the repairable entry.
		blockerSeqStr := fmt.Sprintf("seq=%d", blockerSeq)
		if !strings.Contains(report.Message, blockerSeqStr) {
			t.Fatalf("expected message to reference blocker seq (%s), got %q", blockerSeqStr, report.Message)
		}
		// The message must also include entity_key for debuggability.
		if !strings.Contains(report.Message, blockerEntityKey) {
			t.Fatalf("expected message to include entity_key=%q, got %q", blockerEntityKey, report.Message)
		}
	})
}

func TestRollbackCloudUpgradeSafetyBoundary(t *testing.T) {
	t.Run("rollback before bootstrap verification restores snapshot enrollment", func(t *testing.T) {
		s := newTestStore(t)
		if err := s.CreateSession("rb-s1", "rb-proj", "/tmp/rb"); err != nil {
			t.Fatalf("create session: %v", err)
		}
		if err := s.EnrollProject("rb-proj"); err != nil {
			t.Fatalf("seed enrolled project: %v", err)
		}
		if err := s.SaveCloudUpgradeState(CloudUpgradeState{
			Project:     "rb-proj",
			Stage:       UpgradeStageBootstrapPushed,
			RepairClass: UpgradeRepairClassRepairable,
			Snapshot: CloudUpgradeSnapshot{
				Captured:        true,
				ProjectEnrolled: false,
			},
		}); err != nil {
			t.Fatalf("seed upgrade state: %v", err)
		}

		rolledBack, err := s.RollbackCloudUpgrade("rb-proj")
		if err != nil {
			t.Fatalf("rollback before verification: %v", err)
		}
		if rolledBack.Stage != UpgradeStageRolledBack {
			t.Fatalf("expected rolled_back stage, got %q", rolledBack.Stage)
		}
		enrolled, err := s.IsProjectEnrolled("rb-proj")
		if err != nil {
			t.Fatalf("verify enrollment: %v", err)
		}
		if enrolled {
			t.Fatal("expected rollback to restore unenrolled snapshot state")
		}
	})

	t.Run("rollback after bootstrap verification fails loudly", func(t *testing.T) {
		s := newTestStore(t)
		if err := s.SaveCloudUpgradeState(CloudUpgradeState{
			Project:     "rb-verified",
			Stage:       UpgradeStageBootstrapVerified,
			RepairClass: UpgradeRepairClassReady,
			Snapshot: CloudUpgradeSnapshot{
				Captured:        true,
				ProjectEnrolled: true,
			},
		}); err != nil {
			t.Fatalf("seed verified state: %v", err)
		}

		_, err := s.RollbackCloudUpgrade("rb-verified")
		if err == nil || !strings.Contains(err.Error(), "rollback is unavailable post-bootstrap") {
			t.Fatalf("expected loud post-boundary failure, got %v", err)
		}
	})

	t.Run("rollback rejects uncaptured checkpoints without changing enrollment", func(t *testing.T) {
		s := newTestStore(t)
		if err := s.EnrollProject("rb-uncaptured"); err != nil {
			t.Fatalf("seed enrolled project: %v", err)
		}
		if err := s.SaveCloudUpgradeState(CloudUpgradeState{
			Project:     "rb-uncaptured",
			Stage:       UpgradeStageBootstrapPushed,
			RepairClass: UpgradeRepairClassRepairable,
		}); err != nil {
			t.Fatalf("seed uncaptured checkpoint: %v", err)
		}

		if _, err := s.RollbackCloudUpgrade("rb-uncaptured"); err == nil || !strings.Contains(err.Error(), "rollback requires a captured pre-bootstrap snapshot") {
			t.Fatalf("expected snapshot-specific rollback failure, got %v", err)
		}
		enrolled, err := s.IsProjectEnrolled("rb-uncaptured")
		if err != nil {
			t.Fatalf("verify enrollment after rejected rollback: %v", err)
		}
		if !enrolled {
			t.Fatal("rejected rollback must not unenroll the project")
		}
	})
}

func TestMarkSyncBlockedResetsConsecutiveFailures(t *testing.T) {
	s := newTestStore(t)
	if err := s.MarkSyncFailure(DefaultSyncTargetKey, "transport timeout", time.Now().UTC().Add(30*time.Second)); err != nil {
		t.Fatalf("mark sync failure: %v", err)
	}
	if err := s.MarkSyncBlocked(DefaultSyncTargetKey, "blocked_unenrolled", "project not enrolled"); err != nil {
		t.Fatalf("mark sync blocked: %v", err)
	}

	state, err := s.GetSyncState(DefaultSyncTargetKey)
	if err != nil {
		t.Fatalf("get sync state: %v", err)
	}
	if state.ConsecutiveFailures != 0 {
		t.Fatalf("expected blocked state to reset consecutive failures, got %d", state.ConsecutiveFailures)
	}
}

func TestMarkSyncHealthyCreatesSyncStateWhenMissing(t *testing.T) {
	s := newTestStore(t)
	targetKey := "cloud:proj-a"

	if err := s.MarkSyncHealthy(targetKey); err != nil {
		t.Fatalf("mark healthy on missing row: %v", err)
	}

	state, err := s.GetSyncState(targetKey)
	if err != nil {
		t.Fatalf("get sync state: %v", err)
	}
	if state.Lifecycle != SyncLifecycleHealthy {
		t.Fatalf("expected healthy lifecycle, got %q", state.Lifecycle)
	}
	if state.ReasonCode != nil || state.ReasonMessage != nil || state.LastError != nil {
		t.Fatalf("expected healthy state without degraded reasons/errors, got %+v", state)
	}
}

func TestMarkSyncPendingClearsDegradedMetadata(t *testing.T) {
	s := newTestStore(t)
	targetKey := "cloud:proj-a"

	if err := s.MarkSyncFailure(targetKey, "dial tcp timeout", time.Now().UTC().Add(30*time.Second)); err != nil {
		t.Fatalf("seed degraded state: %v", err)
	}

	if err := s.MarkSyncPending(targetKey); err != nil {
		t.Fatalf("mark pending: %v", err)
	}

	state, err := s.GetSyncState(targetKey)
	if err != nil {
		t.Fatalf("get sync state: %v", err)
	}
	if state.Lifecycle != SyncLifecyclePending {
		t.Fatalf("expected pending lifecycle, got %q", state.Lifecycle)
	}
	if state.ReasonCode != nil || state.ReasonMessage != nil || state.LastError != nil || state.BackoffUntil != nil {
		t.Fatalf("expected pending state to clear degraded metadata, got %+v", state)
	}
}

func TestApplyRemoteMutationIdempotent(t *testing.T) {
	s := newTestStore(t)

	create := SyncMutation{
		Seq:       41,
		TargetKey: DefaultSyncTargetKey,
		Entity:    SyncEntitySession,
		EntityKey: "remote-session",
		Op:        SyncOpUpsert,
		Payload:   `{"id":"remote-session","project":"engram","directory":"/remote"}`,
	}
	if err := s.ApplyPulledMutation(DefaultSyncTargetKey, create); err != nil {
		t.Fatalf("apply session mutation: %v", err)
	}
	if err := s.ApplyPulledMutation(DefaultSyncTargetKey, create); err != nil {
		t.Fatalf("reapply session mutation: %v", err)
	}

	obsMutation := SyncMutation{
		Seq:       42,
		TargetKey: DefaultSyncTargetKey,
		Entity:    SyncEntityObservation,
		EntityKey: "obs-remote-1",
		Op:        SyncOpUpsert,
		Payload:   `{"sync_id":"obs-remote-1","session_id":"remote-session","type":"decision","title":"Remote","content":"Pulled from cloud","project":"engram","scope":"project"}`,
	}
	if err := s.ApplyPulledMutation(DefaultSyncTargetKey, obsMutation); err != nil {
		t.Fatalf("apply observation mutation: %v", err)
	}
	if err := s.ApplyPulledMutation(DefaultSyncTargetKey, obsMutation); err != nil {
		t.Fatalf("reapply observation mutation: %v", err)
	}

	var rowCount int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM observations WHERE sync_id = ?", "obs-remote-1").Scan(&rowCount); err != nil {
		t.Fatalf("count remote observation rows: %v", err)
	}
	if rowCount != 1 {
		t.Fatalf("expected one remote observation row after idempotent upsert, got %d", rowCount)
	}

	deleteMutation := SyncMutation{
		Seq:       43,
		TargetKey: DefaultSyncTargetKey,
		Entity:    SyncEntityObservation,
		EntityKey: "obs-remote-1",
		Op:        SyncOpDelete,
		Payload:   `{"sync_id":"obs-remote-1","deleted":true}`,
	}
	if err := s.ApplyPulledMutation(DefaultSyncTargetKey, deleteMutation); err != nil {
		t.Fatalf("apply delete mutation: %v", err)
	}
	if err := s.ApplyPulledMutation(DefaultSyncTargetKey, deleteMutation); err != nil {
		t.Fatalf("reapply delete mutation: %v", err)
	}

	if _, err := s.GetObservationBySyncID("obs-remote-1"); err == nil {
		t.Fatalf("expected pulled delete to hide observation")
	}

	pending, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 10)
	if err != nil {
		t.Fatalf("list pending after pulled apply: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("expected pulled apply helpers to avoid local re-enqueue, got %+v", pending)
	}

	state, err := s.GetSyncState(DefaultSyncTargetKey)
	if err != nil {
		t.Fatalf("get sync state after pulled apply: %v", err)
	}
	if state.LastPulledSeq != 43 {
		t.Fatalf("expected last pulled seq 43, got %d", state.LastPulledSeq)
	}
}

func TestApplyPulledMutationClearsDegradedReasonFields(t *testing.T) {
	s := newTestStore(t)
	if err := s.MarkSyncBlocked(DefaultSyncTargetKey, "blocked_unenrolled", "project not enrolled"); err != nil {
		t.Fatalf("seed degraded sync state: %v", err)
	}

	mutation := SyncMutation{
		Seq:       1,
		TargetKey: DefaultSyncTargetKey,
		Entity:    SyncEntitySession,
		EntityKey: "remote-session",
		Op:        SyncOpUpsert,
		Payload:   `{"id":"remote-session","project":"engram","directory":"/remote"}`,
	}
	if err := s.ApplyPulledMutation(DefaultSyncTargetKey, mutation); err != nil {
		t.Fatalf("apply pulled mutation: %v", err)
	}

	state, err := s.GetSyncState(DefaultSyncTargetKey)
	if err != nil {
		t.Fatalf("get sync state: %v", err)
	}
	if state.Lifecycle != SyncLifecycleHealthy {
		t.Fatalf("expected lifecycle healthy after pulled apply, got %q", state.Lifecycle)
	}
	if state.ReasonCode != nil || state.ReasonMessage != nil {
		t.Fatalf("expected pulled apply to clear degraded reasons, got reason_code=%v reason_message=%v", state.ReasonCode, state.ReasonMessage)
	}
}

func TestApplyPulledMutationAcceptsStringifiedSessionPayload(t *testing.T) {
	s := newTestStore(t)

	mutation := SyncMutation{
		Seq:       1,
		TargetKey: DefaultSyncTargetKey,
		Entity:    SyncEntitySession,
		EntityKey: "remote-session",
		Op:        SyncOpUpsert,
		Payload:   `"{\"id\":\"remote-session\",\"project\":\"engram\",\"directory\":\"/remote\"}"`,
	}
	if err := s.ApplyPulledMutation(DefaultSyncTargetKey, mutation); err != nil {
		t.Fatalf("apply stringified session mutation: %v", err)
	}

	session, err := s.GetSession("remote-session")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if session.Project != "engram" || session.Directory != "/remote" {
		t.Fatalf("unexpected session after pulled apply: %+v", session)
	}
}

func TestApplyPulledSessionDeleteRemovesSessionAndPrompts(t *testing.T) {
	s := newTestStore(t)

	if _, err := s.db.Exec(`INSERT INTO sessions (id, project, directory) VALUES (?, ?, ?)`, "remote-delete", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if _, err := s.db.Exec(`INSERT INTO user_prompts (sync_id, session_id, content, project) VALUES (?, ?, ?, ?)`, "prompt-remote-delete", "remote-delete", "prompt", "engram"); err != nil {
		t.Fatalf("seed prompt: %v", err)
	}

	mutation := SyncMutation{
		Seq:       2,
		TargetKey: DefaultSyncTargetKey,
		Entity:    SyncEntitySession,
		EntityKey: "remote-delete",
		Op:        SyncOpDelete,
		Payload:   `{"id":"remote-delete","project":"engram","deleted_at":"2026-04-26T10:00:00Z"}`,
	}
	if err := s.ApplyPulledMutation(DefaultSyncTargetKey, mutation); err != nil {
		t.Fatalf("apply pulled session delete: %v", err)
	}

	if _, err := s.GetSession("remote-delete"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected session to be deleted by pulled mutation, got err=%v", err)
	}
	var promptCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM user_prompts WHERE session_id = ?`, "remote-delete").Scan(&promptCount); err != nil {
		t.Fatalf("count prompts: %v", err)
	}
	if promptCount != 0 {
		t.Fatalf("expected pulled session delete to remove prompts, got %d", promptCount)
	}

	pending, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 10)
	if err != nil {
		t.Fatalf("list pending after pulled session delete: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("expected pulled session delete not to enqueue local mutations, got %+v", pending)
	}
}

func TestApplyPulledSessionUpsertTombstoneRemovesSessionAndPrompts(t *testing.T) {
	s := newTestStore(t)

	if _, err := s.db.Exec(`INSERT INTO sessions (id, project, directory) VALUES (?, ?, ?)`, "remote-tombstone", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if _, err := s.db.Exec(`INSERT INTO user_prompts (sync_id, session_id, content, project) VALUES (?, ?, ?, ?)`, "prompt-remote-tombstone", "remote-tombstone", "prompt", "engram"); err != nil {
		t.Fatalf("seed prompt: %v", err)
	}

	mutation := SyncMutation{
		Seq:       3,
		TargetKey: DefaultSyncTargetKey,
		Entity:    SyncEntitySession,
		EntityKey: "remote-tombstone",
		Op:        SyncOpUpsert,
		Payload:   `{"id":"remote-tombstone","project":"engram","deleted_at":"2026-04-26T11:00:00Z"}`,
	}
	if err := s.ApplyPulledMutation(DefaultSyncTargetKey, mutation); err != nil {
		t.Fatalf("apply pulled upsert tombstone: %v", err)
	}

	if _, err := s.GetSession("remote-tombstone"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected session removed by upsert tombstone, got err=%v", err)
	}
	var promptCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM user_prompts WHERE session_id = ?`, "remote-tombstone").Scan(&promptCount); err != nil {
		t.Fatalf("count prompts: %v", err)
	}
	if promptCount != 0 {
		t.Fatalf("expected upsert tombstone to remove prompts, got %d", promptCount)
	}
}

func TestSessionSyncPayloadPreservesStartedAtOnApply(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("local-session", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	var payloadRaw string
	if err := s.db.QueryRow(
		`SELECT payload FROM sync_mutations WHERE entity = ? AND entity_key = ? ORDER BY seq DESC LIMIT 1`,
		SyncEntitySession,
		"local-session",
	).Scan(&payloadRaw); err != nil {
		t.Fatalf("query session mutation payload: %v", err)
	}
	var enqueued syncSessionPayload
	if err := decodeSyncPayload([]byte(payloadRaw), &enqueued); err != nil {
		t.Fatalf("decode enqueued session payload: %v", err)
	}
	if strings.TrimSpace(enqueued.StartedAt) == "" {
		t.Fatal("expected session mutation payload to include started_at")
	}

	mutation := SyncMutation{
		Seq:       2,
		TargetKey: DefaultSyncTargetKey,
		Entity:    SyncEntitySession,
		EntityKey: "remote-started-at",
		Op:        SyncOpUpsert,
		Payload:   `{"id":"remote-started-at","project":"engram","directory":"/remote","started_at":"2024-01-02 03:04:05"}`,
	}
	if err := s.ApplyPulledMutation(DefaultSyncTargetKey, mutation); err != nil {
		t.Fatalf("apply pulled session with started_at: %v", err)
	}

	imported, err := s.GetSession("remote-started-at")
	if err != nil {
		t.Fatalf("get imported session: %v", err)
	}
	if imported.StartedAt != "2024-01-02 03:04:05" {
		t.Fatalf("expected started_at to be preserved from pulled payload, got %q", imported.StartedAt)
	}
}

func TestApplyPulledObservationPreservesChronologyAndRevisionMetadata(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("remote-obs-session", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	mutation := SyncMutation{
		Seq:       10,
		TargetKey: DefaultSyncTargetKey,
		Entity:    SyncEntityObservation,
		EntityKey: "obs-meta-1",
		Op:        SyncOpUpsert,
		Payload:   `{"sync_id":"obs-meta-1","session_id":"remote-obs-session","type":"decision","title":"meta","content":"preserve metadata","project":"engram","scope":"project","created_at":"2024-01-01 00:00:00","updated_at":"2024-01-05 12:30:00","last_seen_at":"2024-01-06 09:15:00","revision_count":7,"duplicate_count":3}`,
	}
	if err := s.ApplyPulledMutation(DefaultSyncTargetKey, mutation); err != nil {
		t.Fatalf("apply pulled observation metadata: %v", err)
	}
	if err := s.ApplyPulledMutation(DefaultSyncTargetKey, mutation); err != nil {
		t.Fatalf("reapply pulled observation metadata: %v", err)
	}

	obs, err := s.GetObservationBySyncID("obs-meta-1")
	if err != nil {
		t.Fatalf("get pulled observation: %v", err)
	}
	if obs.CreatedAt != "2024-01-01 00:00:00" {
		t.Fatalf("expected created_at preserved, got %q", obs.CreatedAt)
	}
	if obs.UpdatedAt != "2024-01-05 12:30:00" {
		t.Fatalf("expected updated_at preserved, got %q", obs.UpdatedAt)
	}
	if obs.LastSeenAt == nil || *obs.LastSeenAt != "2024-01-06 09:15:00" {
		t.Fatalf("expected last_seen_at preserved, got %+v", obs.LastSeenAt)
	}
	if obs.RevisionCount != 7 {
		t.Fatalf("expected revision_count=7, got %d", obs.RevisionCount)
	}
	if obs.DuplicateCount != 3 {
		t.Fatalf("expected duplicate_count=3, got %d", obs.DuplicateCount)
	}
}

func TestApplyPulledChunkIsAtomicAndRetrySafe(t *testing.T) {
	s := newTestStore(t)

	badChunk := []SyncMutation{
		{
			Entity:    SyncEntitySession,
			EntityKey: "chunk-session",
			Op:        SyncOpUpsert,
			Payload:   `{"id":"chunk-session","project":"engram","directory":"/remote"}`,
		},
		{
			Entity:    SyncEntityObservation,
			EntityKey: "chunk-obs-bad",
			Op:        SyncOpUpsert,
			Payload:   `{"sync_id":"chunk-obs-bad","session_id":"missing-session","type":"note","title":"bad","content":"fails fk","project":"engram","scope":"project"}`,
		},
	}

	if err := s.ApplyPulledChunk(DefaultSyncTargetKey, "chunk-retry-safe", badChunk); err == nil {
		t.Fatal("expected chunk apply error for invalid observation payload")
	}
	if _, err := s.GetSession("chunk-session"); err == nil {
		t.Fatal("expected chunk session upsert to roll back after failed chunk apply")
	}
	chunks, err := s.GetSyncedChunksForTarget(DefaultSyncTargetKey)
	if err != nil {
		t.Fatalf("get synced chunks: %v", err)
	}
	if chunks["chunk-retry-safe"] {
		t.Fatal("failed chunk must not be marked as synced")
	}

	goodChunk := []SyncMutation{
		{
			Entity:    SyncEntitySession,
			EntityKey: "chunk-session",
			Op:        SyncOpUpsert,
			Payload:   `{"id":"chunk-session","project":"engram","directory":"/remote"}`,
		},
	}
	if err := s.ApplyPulledChunk(DefaultSyncTargetKey, "chunk-retry-safe", goodChunk); err != nil {
		t.Fatalf("apply valid chunk: %v", err)
	}
	if _, err := s.GetSession("chunk-session"); err != nil {
		t.Fatalf("expected session imported after valid chunk apply: %v", err)
	}
	chunks, err = s.GetSyncedChunksForTarget(DefaultSyncTargetKey)
	if err != nil {
		t.Fatalf("get synced chunks after success: %v", err)
	}
	if !chunks["chunk-retry-safe"] {
		t.Fatal("expected successful chunk to be marked synced")
	}

	if err := s.ApplyPulledChunk(DefaultSyncTargetKey, "chunk-retry-safe", goodChunk); err != nil {
		t.Fatalf("reapplying already synced chunk should be idempotent: %v", err)
	}
	var sessionCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE id = ?`, "chunk-session").Scan(&sessionCount); err != nil {
		t.Fatalf("count imported session: %v", err)
	}
	if sessionCount != 1 {
		t.Fatalf("expected exactly one imported session row, got %d", sessionCount)
	}
}

func TestApplyPulledPromptDeleteCreatesTombstoneAndRemovesPrompt(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s-prompt", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	promptID, err := s.AddPrompt(AddPromptParams{SessionID: "s-prompt", Content: "to-delete", Project: "engram"})
	if err != nil {
		t.Fatalf("add prompt: %v", err)
	}
	var syncID string
	if err := s.db.QueryRow(`SELECT sync_id FROM user_prompts WHERE id = ?`, promptID).Scan(&syncID); err != nil {
		t.Fatalf("lookup prompt sync id: %v", err)
	}

	mutation := SyncMutation{
		Seq:       44,
		TargetKey: DefaultSyncTargetKey,
		Entity:    SyncEntityPrompt,
		EntityKey: syncID,
		Op:        SyncOpDelete,
		Payload:   fmt.Sprintf(`{"sync_id":"%s","session_id":"s-prompt","project":"engram","deleted":true,"hard_delete":true}`, syncID),
	}
	if err := s.ApplyPulledMutation(DefaultSyncTargetKey, mutation); err != nil {
		t.Fatalf("apply pulled prompt delete: %v", err)
	}

	var remaining int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM user_prompts WHERE sync_id = ?`, syncID).Scan(&remaining); err != nil {
		t.Fatalf("count prompts by sync id: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("expected prompt row removed by pulled delete, got %d", remaining)
	}

	var tombstones int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM prompt_tombstones WHERE sync_id = ?`, syncID).Scan(&tombstones); err != nil {
		t.Fatalf("count prompt tombstones: %v", err)
	}
	if tombstones != 1 {
		t.Fatalf("expected prompt tombstone row, got %d", tombstones)
	}
}

func TestApplyPulledPromptUpsertUpdatesCreatedAtOnExistingPrompt(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s-prompt-upsert", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	promptID, err := s.AddPrompt(AddPromptParams{SessionID: "s-prompt-upsert", Content: "local", Project: "engram"})
	if err != nil {
		t.Fatalf("add prompt: %v", err)
	}

	var syncID string
	if err := s.db.QueryRow(`SELECT sync_id FROM user_prompts WHERE id = ?`, promptID).Scan(&syncID); err != nil {
		t.Fatalf("lookup prompt sync id: %v", err)
	}

	mutation := SyncMutation{
		Seq:       45,
		TargetKey: DefaultSyncTargetKey,
		Entity:    SyncEntityPrompt,
		EntityKey: syncID,
		Op:        SyncOpUpsert,
		Payload:   fmt.Sprintf(`{"sync_id":"%s","session_id":"s-prompt-upsert","content":"remote overwrite","project":"engram","created_at":"2024-01-02 03:04:05"}`, syncID),
	}
	if err := s.ApplyPulledMutation(DefaultSyncTargetKey, mutation); err != nil {
		t.Fatalf("apply pulled prompt upsert: %v", err)
	}

	var createdAt string
	var content string
	if err := s.db.QueryRow(`SELECT created_at, content FROM user_prompts WHERE sync_id = ?`, syncID).Scan(&createdAt, &content); err != nil {
		t.Fatalf("query updated prompt: %v", err)
	}
	if createdAt != "2024-01-02 03:04:05" {
		t.Fatalf("expected prompt created_at to be overwritten by incoming payload, got %q", createdAt)
	}
	if content != "remote overwrite" {
		t.Fatalf("expected prompt content updated, got %q", content)
	}
}

func TestDeletePromptEnqueuesDeleteMutationAndTombstone(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s-del-prompt", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	promptID, err := s.AddPrompt(AddPromptParams{SessionID: "s-del-prompt", Content: "bye", Project: "engram"})
	if err != nil {
		t.Fatalf("add prompt: %v", err)
	}
	var syncID string
	if err := s.db.QueryRow(`SELECT sync_id FROM user_prompts WHERE id = ?`, promptID).Scan(&syncID); err != nil {
		t.Fatalf("query prompt sync id: %v", err)
	}

	if err := s.DeletePrompt(promptID); err != nil {
		t.Fatalf("delete prompt: %v", err)
	}

	var tombstones int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM prompt_tombstones WHERE sync_id = ?`, syncID).Scan(&tombstones); err != nil {
		t.Fatalf("count prompt tombstones: %v", err)
	}
	if tombstones != 1 {
		t.Fatalf("expected one prompt tombstone after delete, got %d", tombstones)
	}

	var op string
	if err := s.db.QueryRow(`SELECT op FROM sync_mutations WHERE entity = ? AND entity_key = ? ORDER BY seq DESC LIMIT 1`, SyncEntityPrompt, syncID).Scan(&op); err != nil {
		t.Fatalf("query latest prompt mutation: %v", err)
	}
	if op != SyncOpDelete {
		t.Fatalf("expected latest prompt mutation op=%q, got %q", SyncOpDelete, op)
	}
}

func TestDeleteObservationHardDeleteEnqueuesProjectScopedMutationMetadata(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s-del-obs", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	obsID, err := s.AddObservation(AddObservationParams{
		SessionID: "s-del-obs",
		Type:      "decision",
		Title:     "to-delete",
		Content:   "content",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}

	if err := s.DeleteObservation(obsID, true); err != nil {
		t.Fatalf("hard delete observation: %v", err)
	}

	var project string
	var payloadRaw string
	if err := s.db.QueryRow(
		`SELECT project, payload FROM sync_mutations WHERE entity = ? AND op = ? ORDER BY seq DESC LIMIT 1`,
		SyncEntityObservation,
		SyncOpDelete,
	).Scan(&project, &payloadRaw); err != nil {
		t.Fatalf("query observation delete mutation: %v", err)
	}
	if project != "engram" {
		t.Fatalf("expected project-scoped delete mutation, got project=%q", project)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(payloadRaw), &payload); err != nil {
		t.Fatalf("decode delete mutation payload: %v", err)
	}
	if payload["session_id"] != "s-del-obs" {
		t.Fatalf("expected delete payload session_id metadata, got %#v", payload["session_id"])
	}
	if payload["project"] != "engram" {
		t.Fatalf("expected delete payload project metadata, got %#v", payload["project"])
	}
}

func TestDeleteObservationHardDeleteDerivesProjectFromSessionWhenEntityProjectEmpty(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s-del-obs-empty", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	obsID, err := s.AddObservation(AddObservationParams{
		SessionID: "s-del-obs-empty",
		Type:      "decision",
		Title:     "to-delete",
		Content:   "content",
		Project:   "",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}

	if err := s.DeleteObservation(obsID, true); err != nil {
		t.Fatalf("hard delete observation: %v", err)
	}

	var project string
	if err := s.db.QueryRow(
		`SELECT project FROM sync_mutations WHERE entity = ? AND op = ? ORDER BY seq DESC LIMIT 1`,
		SyncEntityObservation,
		SyncOpDelete,
	).Scan(&project); err != nil {
		t.Fatalf("query observation delete mutation: %v", err)
	}
	if project != "engram" {
		t.Fatalf("expected session-derived project on hard delete mutation, got %q", project)
	}
}

func TestDeleteObservationNotFound(t *testing.T) {
	s := newTestStore(t)
	err := s.DeleteObservation(999999, false)
	if !errors.Is(err, ErrObservationNotFound) {
		t.Fatalf("expected ErrObservationNotFound, got %v", err)
	}
}

func TestUtilityHelpersCoverage(t *testing.T) {
	if got := derefString(nil); got != "" {
		t.Fatalf("expected empty string for nil pointer, got %q", got)
	}
	v := "value"
	if got := derefString(&v); got != "value" {
		t.Fatalf("expected dereferenced value, got %q", got)
	}

	if got := maxInt(10, 5); got != 10 {
		t.Fatalf("expected maxInt(10,5)=10, got %d", got)
	}
	if got := maxInt(3, 7); got != 7 {
		t.Fatalf("expected maxInt(3,7)=7, got %d", got)
	}

	if got := dedupeWindowExpression(0); got != "-15 minutes" {
		t.Fatalf("expected default dedupe window, got %q", got)
	}
	if got := dedupeWindowExpression(20 * time.Second); got != "-1 minutes" {
		t.Fatalf("expected minimum 1 minute window, got %q", got)
	}

	cases := map[string]string{
		"write":   "file_change",
		"patch":   "file_change",
		"bash":    "command",
		"read":    "file_read",
		"glob":    "search",
		"unknown": "tool_use",
	}
	for in, want := range cases {
		if got := ClassifyTool(in); got != want {
			t.Fatalf("ClassifyTool(%q): expected %q, got %q", in, want, got)
		}
	}
}

func TestEndSessionAndTimelineDefaults(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s-end", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	firstID, err := s.AddObservation(AddObservationParams{
		SessionID: "s-end",
		Type:      "note",
		Title:     "first",
		Content:   "first note",
		Project:   "engram",
	})
	if err != nil {
		t.Fatalf("add first observation: %v", err)
	}
	_, err = s.AddObservation(AddObservationParams{
		SessionID: "s-end",
		Type:      "note",
		Title:     "second",
		Content:   "second note",
		Project:   "engram",
	})
	if err != nil {
		t.Fatalf("add second observation: %v", err)
	}

	if err := s.EndSession("s-end", "finished session"); err != nil {
		t.Fatalf("end session: %v", err)
	}

	sess, err := s.GetSession("s-end")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if sess.EndedAt == nil {
		t.Fatalf("expected ended_at to be set")
	}
	if sess.Summary == nil || *sess.Summary != "finished session" {
		t.Fatalf("expected summary to be stored, got %+v", sess.Summary)
	}

	timeline, err := s.Timeline(firstID, 0, -1)
	if err != nil {
		t.Fatalf("timeline with default before/after: %v", err)
	}
	if timeline.SessionInfo == nil {
		t.Fatalf("expected session info in timeline")
	}
	if timeline.TotalInRange != 2 {
		t.Fatalf("expected total_in_range=2, got %d", timeline.TotalInRange)
	}
}

func TestInferTopicFamilyCoverage(t *testing.T) {
	cases := []struct {
		name    string
		typ     string
		title   string
		content string
		want    string
	}{
		{name: "type architecture", typ: "architecture", want: "architecture"},
		{name: "type bugfix", typ: "bugfix", want: "bug"},
		{name: "type decision", typ: "decision", want: "decision"},
		{name: "type pattern", typ: "pattern", want: "pattern"},
		{name: "type config", typ: "config", want: "config"},
		{name: "type discovery", typ: "discovery", want: "discovery"},
		{name: "type learning", typ: "learning", want: "learning"},
		{name: "type session summary", typ: "session_summary", want: "session"},
		{name: "text bug", title: "", content: "this caused a crash regression", want: "bug"},
		{name: "text architecture", title: "", content: "new boundary design", want: "architecture"},
		{name: "text decision", title: "", content: "we chose this tradeoff", want: "decision"},
		{name: "text pattern", title: "", content: "naming convention for handlers", want: "pattern"},
		{name: "text config", title: "", content: "docker env setup", want: "config"},
		{name: "text discovery", title: "", content: "root cause found", want: "discovery"},
		{name: "text learning", title: "", content: "key learning from this issue", want: "learning"},
		{name: "fallback type", typ: "Custom Type", want: "custom-type"},
		{name: "default topic", typ: "manual", title: "", content: "", want: "topic"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := inferTopicFamily(tc.typ, tc.title, tc.content)
			if got != tc.want {
				t.Fatalf("inferTopicFamily(%q,%q,%q): expected %q, got %q", tc.typ, tc.title, tc.content, tc.want, got)
			}
		})
	}
}

func TestStoreAdditionalQueryAndMutationBranches(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s-q", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	longContent := strings.Repeat("x", s.cfg.MaxObservationLength+100)
	obsID, err := s.AddObservation(AddObservationParams{
		SessionID: "s-q",
		Type:      "note",
		Title:     "Private <private>secret</private> title",
		Content:   longContent + " <private>token</private>",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}
	obs, err := s.GetObservation(obsID)
	if err != nil {
		t.Fatalf("get observation: %v", err)
	}
	if !strings.Contains(obs.Title, "[REDACTED]") {
		t.Fatalf("expected private tags redacted in title, got %q", obs.Title)
	}
	if !strings.Contains(obs.Content, "... [truncated]") {
		t.Fatalf("expected truncated content marker, got %q", obs.Content)
	}

	newTopic := ""
	newProject := ""
	if _, err := s.UpdateObservation(obsID, UpdateObservationParams{Project: &newProject}); !errors.Is(err, ErrObservationProjectImmutable) {
		t.Fatalf("empty-project update error = %v, want ErrObservationProjectImmutable", err)
	}
	updated, err := s.UpdateObservation(obsID, UpdateObservationParams{TopicKey: &newTopic})
	if err != nil {
		t.Fatalf("update observation: %v", err)
	}
	if updated.Project == nil || *updated.Project != "engram" {
		t.Fatalf("expected immutable project engram after rejected update, got %v", updated.Project)
	}
	if updated.TopicKey != nil {
		t.Fatalf("expected nil topic key after empty update")
	}

	if _, err := s.AddPrompt(AddPromptParams{SessionID: "s-q", Content: "alpha prompt", Project: "alpha"}); err != nil {
		t.Fatalf("add alpha prompt: %v", err)
	}
	if _, err := s.AddPrompt(AddPromptParams{SessionID: "s-q", Content: "beta prompt", Project: "beta"}); err != nil {
		t.Fatalf("add beta prompt: %v", err)
	}

	recentPrompts, err := s.RecentPrompts("beta", 1)
	if err != nil {
		t.Fatalf("recent prompts with project filter: %v", err)
	}
	if len(recentPrompts) != 1 || recentPrompts[0].Project != "beta" {
		t.Fatalf("expected one beta prompt, got %+v", recentPrompts)
	}

	searchPrompts, err := s.SearchPrompts("prompt", "alpha", 0)
	if err != nil {
		t.Fatalf("search prompts with project filter/default limit: %v", err)
	}
	if len(searchPrompts) != 1 || searchPrompts[0].Project != "alpha" {
		t.Fatalf("expected one alpha prompt search result, got %+v", searchPrompts)
	}

	searchResults, err := s.Search("title", SearchOptions{Scope: "project", Limit: 9999})
	if err != nil {
		t.Fatalf("search with clamped limit: %v", err)
	}
	if len(searchResults) == 0 {
		t.Fatalf("expected search results")
	}

	ctx, err := s.FormatContext("", "project")
	if err != nil {
		t.Fatalf("format context: %v", err)
	}
	if !strings.Contains(ctx, "Recent User Prompts") {
		t.Fatalf("expected prompts section in context output")
	}
}

func TestStoreErrorBranchesWithClosedDatabase(t *testing.T) {
	s := newTestStore(t)

	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	if _, err := s.GetSession("missing"); err == nil {
		t.Fatalf("expected GetSession error when db is closed")
	}
	if _, err := s.AllSessions("", 1); err == nil {
		t.Fatalf("expected AllSessions error when db is closed")
	}
	if _, err := s.RecentSessions("", 1); err == nil {
		t.Fatalf("expected RecentSessions error when db is closed")
	}
	if _, err := s.SearchPrompts("x", "", 1); err == nil {
		t.Fatalf("expected SearchPrompts error when db is closed")
	}
	if _, err := s.Search("x", SearchOptions{}); err == nil {
		t.Fatalf("expected Search error when db is closed")
	}
	if _, err := s.Export(); err == nil {
		t.Fatalf("expected Export error when db is closed")
	}
	if _, err := s.Timeline(1, 1, 1); err == nil {
		t.Fatalf("expected Timeline error when db is closed")
	}
}

func TestEndSessionEdgeCases(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s-edge", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	if err := s.EndSession("missing", "ignored"); err != nil {
		t.Fatalf("end missing session should be no-op: %v", err)
	}

	if err := s.EndSession("s-edge", ""); err != nil {
		t.Fatalf("end session with empty summary: %v", err)
	}

	sess, err := s.GetSession("s-edge")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if sess.EndedAt == nil {
		t.Fatalf("expected ended_at to be set")
	}
	if sess.Summary != nil {
		t.Fatalf("expected empty summary to persist as NULL, got %q", *sess.Summary)
	}
}

func TestTimelineHandlesMissingSessionRecord(t *testing.T) {
	s := newTestStore(t)

	if _, err := s.db.Exec("PRAGMA foreign_keys = OFF"); err != nil {
		t.Fatalf("disable fk: %v", err)
	}
	defer func() {
		_, _ = s.db.Exec("PRAGMA foreign_keys = ON")
	}()

	res, err := s.db.Exec(
		`INSERT INTO observations (session_id, type, title, content, project, scope, normalized_hash, revision_count, duplicate_count, last_seen_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, 1, 1, datetime('now'), datetime('now'))`,
		"manual-save", "manual", "orphan", "orphan content", "engram", "project", hashNormalized("orphan content"),
	)
	if err != nil {
		t.Fatalf("insert orphan observation: %v", err)
	}
	obsID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("last insert id: %v", err)
	}

	timeline, err := s.Timeline(obsID, 1, 1)
	if err != nil {
		t.Fatalf("timeline: %v", err)
	}
	if timeline.SessionInfo != nil {
		t.Fatalf("expected nil session info for missing session, got %+v", timeline.SessionInfo)
	}
	if timeline.TotalInRange != 1 {
		t.Fatalf("expected total in range=1, got %d", timeline.TotalInRange)
	}
}

func TestQueryObservationsScanError(t *testing.T) {
	s := newTestStore(t)

	if _, err := s.queryObservations("SELECT 1"); err == nil {
		t.Fatalf("expected scan error for mismatched projection")
	}
}

func TestMigrationAndHelperEdgeBranches(t *testing.T) {
	t.Run("migrate is idempotent with existing triggers", func(t *testing.T) {
		s := newTestStore(t)
		if err := s.migrate(); err != nil {
			t.Fatalf("second migrate should succeed: %v", err)
		}
	})

	t.Run("legacy migrate skips table without id column", func(t *testing.T) {
		s := newTestStore(t)

		if _, err := s.db.Exec(`
			DROP TRIGGER IF EXISTS obs_fts_insert;
			DROP TRIGGER IF EXISTS obs_fts_update;
			DROP TRIGGER IF EXISTS obs_fts_delete;
			DROP TABLE IF EXISTS observations_fts;
			DROP TABLE observations;
			CREATE TABLE observations (
				session_id TEXT,
				type TEXT,
				title TEXT,
				content TEXT
			);
		`); err != nil {
			t.Fatalf("recreate observations without id: %v", err)
		}

		if err := s.migrateLegacyObservationsTable(); err != nil {
			t.Fatalf("legacy migrate should skip tables without id: %v", err)
		}
	})

	t.Run("topic helpers normalize edge cases", func(t *testing.T) {
		if got := SuggestTopicKey("decision", "decision", ""); got != "decision/general" {
			t.Fatalf("expected decision/general, got %q", got)
		}
		if got := SuggestTopicKey("bugfix", "bug-auth-panic", ""); got != "bug/auth-panic" {
			t.Fatalf("expected bug/auth-panic, got %q", got)
		}
		if got := SuggestTopicKey("manual", "!!!", "..."); got != "topic/general" {
			t.Fatalf("expected topic/general fallback, got %q", got)
		}

		longSegment := normalizeTopicSegment(strings.Repeat("abc", 50))
		if len(longSegment) != 100 {
			t.Fatalf("expected topic segment truncation to 100, got %d", len(longSegment))
		}

		longKey := normalizeTopicKey(strings.Repeat("k", 200))
		if len(longKey) != 120 {
			t.Fatalf("expected topic key truncation to 120, got %d", len(longKey))
		}
	})

	t.Run("format context empty returns empty string", func(t *testing.T) {
		s := newTestStore(t)
		ctx, err := s.FormatContext("", "")
		if err != nil {
			t.Fatalf("format context: %v", err)
		}
		if ctx != "" {
			t.Fatalf("expected empty context when no data, got %q", ctx)
		}
	})
}

func TestImportSkipsObservationWithExistingSyncID(t *testing.T) {
	s := newTestStore(t)
	now := Now()
	project := "engram"
	observations := make([]Observation, 3)
	for i := range observations {
		observations[i] = Observation{
			SyncID:    fmt.Sprintf("obs-import-idempotent-%d", i),
			SessionID: "import-session",
			Type:      "bugfix",
			Title:     fmt.Sprintf("idempotent import %d", i),
			Content:   fmt.Sprintf("import observation %d once", i),
			Project:   &project,
			Scope:     "project",
			CreatedAt: now,
			UpdatedAt: now,
		}
	}
	data := &ExportData{
		Sessions: []Session{{
			ID:        "import-session",
			Project:   "engram",
			Directory: "/tmp/engram",
			StartedAt: now,
		}},
		Observations: observations,
	}

	first, err := s.Import(data)
	if err != nil {
		t.Fatalf("first import: %v", err)
	}
	if first.ObservationsImported != 3 {
		t.Fatalf("first import observations = %d, want 3", first.ObservationsImported)
	}

	for attempt := 2; attempt <= 3; attempt++ {
		result, err := s.Import(data)
		if err != nil {
			t.Fatalf("import attempt %d: %v", attempt, err)
		}
		if result.ObservationsImported != 0 {
			t.Fatalf("import attempt %d observations = %d, want 0", attempt, result.ObservationsImported)
		}
	}

	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM observations WHERE sync_id LIKE 'obs-import-idempotent-%'").Scan(&count); err != nil {
		t.Fatalf("count imported observations: %v", err)
	}
	if count != 3 {
		t.Fatalf("stored observations = %d, want 3", count)
	}
}

func TestImportStoresObservationProjectAsText(t *testing.T) {
	s := newTestStore(t)
	now := Now()
	project := "engram"
	_, err := s.Import(&ExportData{
		Sessions:     []Session{{ID: "import-project-storage", Project: project, Directory: "/tmp/engram", StartedAt: now}},
		Observations: []Observation{{SyncID: "obs-import-project-storage", SessionID: "import-project-storage", Type: "bugfix", Title: "Import project as text", Content: "Import boundary stores text", Project: &project, Scope: "project", CreatedAt: now, UpdatedAt: now}},
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	var storageClass string
	if err := s.db.QueryRow(`SELECT typeof(project) FROM observations WHERE sync_id = ?`, "obs-import-project-storage").Scan(&storageClass); err != nil {
		t.Fatalf("read project storage class: %v", err)
	}
	if storageClass != "text" {
		t.Fatalf("project storage class = %q, want text", storageClass)
	}
}

func TestExportImportEdgeBranches(t *testing.T) {
	t.Run("export fails when observations query fails", func(t *testing.T) {
		s := newTestStore(t)

		if _, err := s.db.Exec(`
			DROP TRIGGER IF EXISTS obs_fts_insert;
			DROP TRIGGER IF EXISTS obs_fts_update;
			DROP TRIGGER IF EXISTS obs_fts_delete;
			DROP TABLE IF EXISTS observations_fts;
			DROP TABLE observations;
		`); err != nil {
			t.Fatalf("drop observations: %v", err)
		}

		_, err := s.Export()
		if err == nil || !strings.Contains(err.Error(), "export observations") {
			t.Fatalf("expected observations export error, got %v", err)
		}
	})

	t.Run("export fails when prompts query fails", func(t *testing.T) {
		s := newTestStore(t)

		if _, err := s.db.Exec(`
			DROP TRIGGER IF EXISTS prompt_fts_insert;
			DROP TRIGGER IF EXISTS prompt_fts_update;
			DROP TRIGGER IF EXISTS prompt_fts_delete;
			DROP TABLE IF EXISTS prompts_fts;
			DROP TABLE user_prompts;
		`); err != nil {
			t.Fatalf("drop prompts: %v", err)
		}

		_, err := s.Export()
		if err == nil || !strings.Contains(err.Error(), "export prompts") {
			t.Fatalf("expected prompts export error, got %v", err)
		}
	})

	t.Run("import begin tx fails on closed db", func(t *testing.T) {
		s := newTestStore(t)
		if err := s.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}

		_, err := s.Import(&ExportData{})
		if err == nil || !strings.Contains(err.Error(), "begin tx") {
			t.Fatalf("expected begin tx import error, got %v", err)
		}
	})

	t.Run("import fails on observation fk error", func(t *testing.T) {
		s := newTestStore(t)
		_, err := s.Import(&ExportData{
			Observations: []Observation{{
				ID:        1,
				SessionID: "missing-session",
				Type:      "bugfix",
				Title:     "x",
				Content:   "y",
				Scope:     "project",
				CreatedAt: Now(),
				UpdatedAt: Now(),
			}},
		})
		if err == nil || !strings.Contains(err.Error(), "import observation") {
			t.Fatalf("expected observation import error, got %v", err)
		}
	})

	t.Run("import fails on prompt fk error", func(t *testing.T) {
		s := newTestStore(t)
		_, err := s.Import(&ExportData{
			Prompts: []Prompt{{
				ID:        1,
				SessionID: "missing-session",
				Content:   "prompt",
				Project:   "engram",
				CreatedAt: Now(),
			}},
		})
		if err == nil || !strings.Contains(err.Error(), "import prompt") {
			t.Fatalf("expected prompt import error, got %v", err)
		}
	})
}

func TestNewErrorBranches(t *testing.T) {
	t.Run("fails when data dir is a file", func(t *testing.T) {
		base := t.TempDir()
		badPath := filepath.Join(base, "not-a-dir")
		if err := os.WriteFile(badPath, []byte("x"), 0600); err != nil {
			t.Fatalf("write file: %v", err)
		}

		cfg := mustDefaultConfig(t)
		cfg.DataDir = badPath

		_, err := New(cfg)
		if err == nil || !strings.Contains(err.Error(), "create data dir") {
			t.Fatalf("expected create data dir error, got %v", err)
		}
	})

	t.Run("fails when db path is a directory", func(t *testing.T) {
		dataDir := t.TempDir()
		dbAsDir := filepath.Join(dataDir, "engram.db")
		if err := os.Mkdir(dbAsDir, 0755); err != nil {
			t.Fatalf("mkdir db path: %v", err)
		}

		cfg := mustDefaultConfig(t)
		cfg.DataDir = dataDir

		_, err := New(cfg)
		if err == nil {
			t.Fatalf("expected New to fail when db path is a directory")
		}
	})

	t.Run("fails when migration encounters conflicting object", func(t *testing.T) {
		dataDir := t.TempDir()
		dbPath := filepath.Join(dataDir, "engram.db")

		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			t.Fatalf("open db: %v", err)
		}
		_, err = db.Exec(`
			CREATE TABLE sessions (
				id TEXT PRIMARY KEY,
				project TEXT NOT NULL,
				directory TEXT NOT NULL,
				started_at TEXT NOT NULL,
				ended_at TEXT,
				summary TEXT
			);
			CREATE TABLE user_prompts (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				session_id TEXT NOT NULL,
				content TEXT NOT NULL,
				created_at TEXT NOT NULL
			);
		`)
		if err != nil {
			_ = db.Close()
			t.Fatalf("create conflicting view: %v", err)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("close db: %v", err)
		}

		cfg := mustDefaultConfig(t)
		cfg.DataDir = dataDir

		_, err = New(cfg)
		if err == nil || !strings.Contains(err.Error(), "migration") {
			t.Fatalf("expected migration error, got %v", err)
		}
	})
}

func TestMigrationInternalErrorAndNoopBranches(t *testing.T) {
	t.Run("addColumnIfNotExists adds then noops", func(t *testing.T) {
		s := newTestStore(t)
		if _, err := s.db.Exec(`CREATE TABLE extra_table (id INTEGER)`); err != nil {
			t.Fatalf("create extra table: %v", err)
		}

		if err := s.addColumnIfNotExists("extra_table", "name", "TEXT"); err != nil {
			t.Fatalf("add column: %v", err)
		}
		if err := s.addColumnIfNotExists("extra_table", "name", "TEXT"); err != nil {
			t.Fatalf("add existing column should noop: %v", err)
		}

		if err := s.addColumnIfNotExists("missing_table", "x", "TEXT"); err == nil {
			t.Fatalf("expected missing table error")
		}
	})

	t.Run("legacy migrate noops when id is primary key", func(t *testing.T) {
		s := newTestStore(t)
		if err := s.migrateLegacyObservationsTable(); err != nil {
			t.Fatalf("expected noop for modern schema: %v", err)
		}
	})

	t.Run("legacy migrate fails if temp table already exists", func(t *testing.T) {
		s := newTestStore(t)
		if _, err := s.db.Exec(`
			DROP TRIGGER IF EXISTS obs_fts_insert;
			DROP TRIGGER IF EXISTS obs_fts_update;
			DROP TRIGGER IF EXISTS obs_fts_delete;
			DROP TABLE IF EXISTS observations_fts;
			DROP TABLE observations;
			CREATE TABLE observations (
				id INT,
				session_id TEXT,
				type TEXT,
				title TEXT,
				content TEXT,
				created_at TEXT
			);
			CREATE TABLE observations_migrated (id INTEGER PRIMARY KEY);
		`); err != nil {
			t.Fatalf("prepare legacy schema: %v", err)
		}

		err := s.migrateLegacyObservationsTable()
		if err == nil || !strings.Contains(err.Error(), "create table") {
			t.Fatalf("expected create table error, got %v", err)
		}
	})

	t.Run("migrate returns deterministic exec hook errors", func(t *testing.T) {
		s := newTestStore(t)

		origExec := s.hooks.exec
		s.hooks.exec = func(db execer, query string, args ...any) (sql.Result, error) {
			if strings.Contains(query, "UPDATE observations SET scope = 'project'") {
				return nil, errors.New("forced migrate update failure")
			}
			return origExec(db, query, args...)
		}

		err := s.migrate()
		if err == nil || !strings.Contains(err.Error(), "forced migrate update failure") {
			t.Fatalf("expected forced migrate failure, got %v", err)
		}
	})

	t.Run("migrate fails when creating missing triggers", func(t *testing.T) {
		s := newTestStore(t)

		if _, err := s.db.Exec(`
			DROP TRIGGER IF EXISTS obs_fts_insert;
			DROP TRIGGER IF EXISTS obs_fts_update;
			DROP TRIGGER IF EXISTS obs_fts_delete;
		`); err != nil {
			t.Fatalf("drop obs triggers: %v", err)
		}

		origExec := s.hooks.exec
		s.hooks.exec = func(db execer, query string, args ...any) (sql.Result, error) {
			if strings.Contains(query, "CREATE TRIGGER obs_fts_insert") {
				return nil, errors.New("forced obs trigger failure")
			}
			return origExec(db, query, args...)
		}

		err := s.migrate()
		if err == nil || !strings.Contains(err.Error(), "forced obs trigger failure") {
			t.Fatalf("expected forced trigger failure, got %v", err)
		}
	})

	t.Run("legacy migrate surfaces begin and commit hook failures", func(t *testing.T) {
		prepareLegacyStore := func(t *testing.T) *Store {
			t.Helper()
			s := newTestStore(t)
			if _, err := s.db.Exec(`
				DROP TRIGGER IF EXISTS obs_fts_insert;
				DROP TRIGGER IF EXISTS obs_fts_update;
				DROP TRIGGER IF EXISTS obs_fts_delete;
				DROP TABLE IF EXISTS observations_fts;
				DROP TABLE observations;
				INSERT OR IGNORE INTO sessions (id, project, directory) VALUES ('s1', 'engram', '/tmp/engram');
				CREATE TABLE observations (
					id INT,
					session_id TEXT,
					type TEXT,
					title TEXT,
					content TEXT,
					tool_name TEXT,
					project TEXT,
					scope TEXT,
					topic_key TEXT,
					normalized_hash TEXT,
					revision_count INTEGER,
					duplicate_count INTEGER,
					last_seen_at TEXT,
					created_at TEXT,
					updated_at TEXT,
					deleted_at TEXT
				);
				INSERT INTO observations (id, session_id, type, title, content, project, created_at, updated_at)
				VALUES (1, 's1', 'bugfix', 'legacy', 'legacy row', 'engram', datetime('now'), datetime('now'));
			`); err != nil {
				t.Fatalf("prepare legacy table: %v", err)
			}
			return s
		}

		t.Run("begin tx", func(t *testing.T) {
			s := prepareLegacyStore(t)
			s.hooks.beginTx = func(_ *sql.DB) (*sql.Tx, error) {
				return nil, errors.New("forced begin failure")
			}

			err := s.migrateLegacyObservationsTable()
			if err == nil || !strings.Contains(err.Error(), "forced begin failure") {
				t.Fatalf("expected begin failure, got %v", err)
			}
		})

		t.Run("commit", func(t *testing.T) {
			s := prepareLegacyStore(t)
			s.hooks.commit = func(_ *sql.Tx) error {
				return errors.New("forced legacy commit failure")
			}

			err := s.migrateLegacyObservationsTable()
			if err == nil || !strings.Contains(err.Error(), "forced legacy commit failure") {
				t.Fatalf("expected commit failure, got %v", err)
			}
		})
	})
}

func TestImportExportSeamErrors(t *testing.T) {
	t.Run("export query hooks", func(t *testing.T) {
		s := newTestStore(t)

		origQueryIt := s.hooks.queryIt
		s.hooks.queryIt = func(db queryer, query string, args ...any) (rowScanner, error) {
			if strings.Contains(query, "FROM sessions") {
				return nil, errors.New("forced sessions export query error")
			}
			return origQueryIt(db, query, args...)
		}
		if _, err := s.Export(); err == nil || !strings.Contains(err.Error(), "export sessions") {
			t.Fatalf("expected sessions export error, got %v", err)
		}

		s.hooks.queryIt = func(db queryer, query string, args ...any) (rowScanner, error) {
			if strings.Contains(query, "FROM observations") {
				return nil, errors.New("forced observations export query error")
			}
			return origQueryIt(db, query, args...)
		}
		if _, err := s.Export(); err == nil || !strings.Contains(err.Error(), "export observations") {
			t.Fatalf("expected observations export error, got %v", err)
		}

		s.hooks.queryIt = func(db queryer, query string, args ...any) (rowScanner, error) {
			if strings.Contains(query, "FROM user_prompts") {
				return nil, errors.New("forced prompts export query error")
			}
			return origQueryIt(db, query, args...)
		}
		if _, err := s.Export(); err == nil || !strings.Contains(err.Error(), "export prompts") {
			t.Fatalf("expected prompts export error, got %v", err)
		}
	})

	t.Run("import tx and exec hooks", func(t *testing.T) {
		s := newTestStore(t)

		s.hooks.beginTx = func(_ *sql.DB) (*sql.Tx, error) {
			return nil, errors.New("forced import begin failure")
		}
		if _, err := s.Import(&ExportData{}); err == nil || !strings.Contains(err.Error(), "begin tx") {
			t.Fatalf("expected begin tx error, got %v", err)
		}

		s.hooks = defaultStoreHooks()
		origExec := s.hooks.exec
		s.hooks.exec = func(db execer, query string, args ...any) (sql.Result, error) {
			if strings.Contains(query, "INSERT OR IGNORE INTO sessions") {
				return nil, errors.New("forced import session insert failure")
			}
			return origExec(db, query, args...)
		}
		if _, err := s.Import(&ExportData{Sessions: []Session{{ID: "s-x", Project: "p", Directory: "/tmp", StartedAt: Now()}}}); err == nil || !strings.Contains(err.Error(), "import session") {
			t.Fatalf("expected session import error, got %v", err)
		}

		s.hooks = defaultStoreHooks()
		s.hooks.commit = func(_ *sql.Tx) error {
			return errors.New("forced import commit failure")
		}
		if _, err := s.Import(&ExportData{}); err == nil || !strings.Contains(err.Error(), "import: commit") {
			t.Fatalf("expected commit error, got %v", err)
		}
	})
}

func TestHookFallbacksAndAdditionalBranches(t *testing.T) {
	t.Run("hook fallbacks call default DB methods", func(t *testing.T) {
		s := newTestStore(t)
		s.hooks = storeHooks{}

		if _, err := s.execHook(s.db, "SELECT 1"); err != nil {
			t.Fatalf("exec hook fallback: %v", err)
		}
		rows, err := s.queryHook(s.db, "SELECT 1")
		if err != nil {
			t.Fatalf("query hook fallback: %v", err)
		}
		_ = rows.Close()

		iter, err := s.queryItHook(s.db, "SELECT 1")
		if err != nil {
			t.Fatalf("query iterator fallback: %v", err)
		}
		_ = iter.Close()

		tx, err := s.beginTxHook()
		if err != nil {
			t.Fatalf("begin tx hook fallback: %v", err)
		}
		if err := s.commitHook(tx); err != nil {
			t.Fatalf("commit hook fallback: %v", err)
		}

		s2 := newTestStore(t)
		rows2, err := s2.queryHook(s2.db, "SELECT 1")
		if err != nil {
			t.Fatalf("query hook default closure: %v", err)
		}
		_ = rows2.Close()

		s.hooks.query = func(db queryer, query string, args ...any) (*sql.Rows, error) {
			return nil, errors.New("forced query hook error")
		}
		s.hooks.queryIt = nil
		if _, err := s.queryItHook(s.db, "SELECT 1"); err == nil {
			t.Fatalf("expected queryItHook error through queryHook fallback")
		}
	})

	t.Run("sessions and observations filters with default limits", func(t *testing.T) {
		s := newTestStore(t)
		if err := s.CreateSession("s-p", "proj-a", "/tmp/proj-a"); err != nil {
			t.Fatalf("create session proj-a: %v", err)
		}
		if err := s.CreateSession("s-q", "proj-b", "/tmp/proj-b"); err != nil {
			t.Fatalf("create session proj-b: %v", err)
		}
		if _, err := s.AddObservation(AddObservationParams{SessionID: "s-p", Type: "note", Title: "a", Content: "a", Project: "proj-a", Scope: "project"}); err != nil {
			t.Fatalf("add observation proj-a: %v", err)
		}
		if _, err := s.AddObservation(AddObservationParams{SessionID: "s-q", Type: "note", Title: "b", Content: "b", Project: "proj-b", Scope: "project"}); err != nil {
			t.Fatalf("add observation proj-b: %v", err)
		}

		recent, err := s.RecentSessions("proj-a", 0)
		if err != nil {
			t.Fatalf("recent sessions filtered: %v", err)
		}
		if len(recent) != 1 || recent[0].Project != "proj-a" {
			t.Fatalf("expected one proj-a recent session, got %+v", recent)
		}

		all, err := s.AllSessions("proj-b", -1)
		if err != nil {
			t.Fatalf("all sessions filtered: %v", err)
		}
		if len(all) != 1 || all[0].Project != "proj-b" {
			t.Fatalf("expected one proj-b session, got %+v", all)
		}

		obs, err := s.AllObservations("proj-a", "project", 0)
		if err != nil {
			t.Fatalf("all observations defaults: %v", err)
		}
		if len(obs) != 1 || obs[0].SessionID != "s-p" {
			t.Fatalf("expected one proj-a observation, got %+v", obs)
		}

		sessionObs, err := s.SessionObservations("s-p", 0)
		if err != nil {
			t.Fatalf("session observations default limit: %v", err)
		}
		if len(sessionObs) != 1 {
			t.Fatalf("expected one session observation, got %d", len(sessionObs))
		}

		recentObs, err := s.RecentObservations("proj-a", "project", 0)
		if err != nil {
			t.Fatalf("recent observations default limit: %v", err)
		}
		if len(recentObs) != 1 {
			t.Fatalf("expected one recent observation, got %d", len(recentObs))
		}

		recentPrompts, err := s.RecentPrompts("", 0)
		if err != nil {
			t.Fatalf("recent prompts default limit: %v", err)
		}
		if len(recentPrompts) != 0 {
			t.Fatalf("expected zero prompts, got %d", len(recentPrompts))
		}
	})

	t.Run("timeline includes before and after in chronological order", func(t *testing.T) {
		s := newTestStore(t)
		if err := s.CreateSession("s-tl", "engram", "/tmp/engram"); err != nil {
			t.Fatalf("create session: %v", err)
		}

		firstID, err := s.AddObservation(AddObservationParams{SessionID: "s-tl", Type: "note", Title: "1", Content: "one", Project: "engram"})
		if err != nil {
			t.Fatalf("add first observation: %v", err)
		}
		middleID, err := s.AddObservation(AddObservationParams{SessionID: "s-tl", Type: "note", Title: "2", Content: "two", Project: "engram"})
		if err != nil {
			t.Fatalf("add middle observation: %v", err)
		}
		lastID, err := s.AddObservation(AddObservationParams{SessionID: "s-tl", Type: "note", Title: "3", Content: "three", Project: "engram"})
		if err != nil {
			t.Fatalf("add last observation: %v", err)
		}

		tl, err := s.Timeline(middleID, 5, 5)
		if err != nil {
			t.Fatalf("timeline middle: %v", err)
		}
		if len(tl.Before) != 1 || tl.Before[0].ID != firstID {
			t.Fatalf("expected first in before list, got %+v", tl.Before)
		}
		if len(tl.After) != 1 || tl.After[0].ID != lastID {
			t.Fatalf("expected last in after list, got %+v", tl.After)
		}
	})

	t.Run("format context returns specific query stage errors", func(t *testing.T) {
		t.Run("recent sessions error", func(t *testing.T) {
			s := newTestStore(t)
			_ = s.Close()
			if _, err := s.FormatContext("", ""); err == nil {
				t.Fatalf("expected format context to fail from recent sessions")
			}
		})

		t.Run("recent observations error", func(t *testing.T) {
			s := newTestStore(t)
			if err := s.CreateSession("s-ctx", "engram", "/tmp/engram"); err != nil {
				t.Fatalf("create session: %v", err)
			}
			if _, err := s.db.Exec("DROP TABLE observations"); err != nil {
				t.Fatalf("drop observations: %v", err)
			}
			if _, err := s.FormatContext("", ""); err == nil {
				t.Fatalf("expected format context to fail from recent observations")
			}
		})

		t.Run("recent prompts error", func(t *testing.T) {
			s := newTestStore(t)
			if err := s.CreateSession("s-ctx2", "engram", "/tmp/engram"); err != nil {
				t.Fatalf("create session: %v", err)
			}
			if _, err := s.db.Exec("DROP TABLE user_prompts"); err != nil {
				t.Fatalf("drop prompts: %v", err)
			}
			if _, err := s.FormatContext("", ""); err == nil {
				t.Fatalf("expected format context to fail from recent prompts")
			}
		})
	})
}

func TestSQLiteWriteRetryRetriesTransientLockErrors(t *testing.T) {
	oldBackoffs := sqliteWriteRetryBackoffs
	sqliteWriteRetryBackoffs = []time.Duration{0, 0, 0}
	t.Cleanup(func() { sqliteWriteRetryBackoffs = oldBackoffs })

	t.Run("begin lock is retried and succeeds", func(t *testing.T) {
		s := newTestStore(t)
		origBegin := s.hooks.beginTx
		attempts := 0
		s.hooks.beginTx = func(db *sql.DB) (*sql.Tx, error) {
			attempts++
			if attempts < 3 {
				return nil, errors.New("database is locked")
			}
			return origBegin(db)
		}

		if err := s.CreateSession("retry-session", "retry-project", "/tmp/retry-project"); err != nil {
			t.Fatalf("expected retry to succeed, got %v", err)
		}
		if attempts != 3 {
			t.Fatalf("expected 3 begin attempts, got %d", attempts)
		}
	})

	t.Run("non lock error is not retried", func(t *testing.T) {
		s := newTestStore(t)
		attempts := 0
		s.hooks.beginTx = func(_ *sql.DB) (*sql.Tx, error) {
			attempts++
			return nil, errors.New("permanent begin failure")
		}

		err := s.CreateSession("no-retry-session", "retry-project", "/tmp/retry-project")
		if err == nil || !strings.Contains(err.Error(), "permanent begin failure") {
			t.Fatalf("expected permanent error, got %v", err)
		}
		if attempts != 1 {
			t.Fatalf("expected one attempt for permanent error, got %d", attempts)
		}
	})

	t.Run("lock errors remain bounded", func(t *testing.T) {
		s := newTestStore(t)
		attempts := 0
		s.hooks.beginTx = func(_ *sql.DB) (*sql.Tx, error) {
			attempts++
			return nil, errors.New("SQLITE_BUSY: database is locked")
		}

		err := s.CreateSession("bounded-session", "retry-project", "/tmp/retry-project")
		if err == nil || !isRetryableSQLiteLockError(err) {
			t.Fatalf("expected retryable lock error after exhaustion, got %v", err)
		}
		if attempts != len(sqliteWriteRetryBackoffs)+1 {
			t.Fatalf("expected bounded attempts=%d, got %d", len(sqliteWriteRetryBackoffs)+1, attempts)
		}
	})
}

func TestStoreUncoveredBranchesPushToHundred(t *testing.T) {
	t.Run("new open database hook error", func(t *testing.T) {
		orig := openDB
		t.Cleanup(func() { openDB = orig })
		openDB = func(driverName, dataSourceName string) (*sql.DB, error) {
			return nil, errors.New("forced open error")
		}

		cfg := mustDefaultConfig(t)
		cfg.DataDir = t.TempDir()
		if _, err := New(cfg); err == nil || !strings.Contains(err.Error(), "open database") {
			t.Fatalf("expected open database error, got %v", err)
		}
	})

	t.Run("migrate forced failures for remaining exec branches", func(t *testing.T) {
		failCases := []string{
			"CREATE INDEX IF NOT EXISTS idx_obs_scope",
			"UPDATE observations SET topic_key = NULL",
			"UPDATE observations SET revision_count = 1",
			"UPDATE observations SET duplicate_count = 1",
			"UPDATE observations SET updated_at = created_at",
			"UPDATE user_prompts SET project = ''",
			"CREATE TRIGGER prompt_fts_insert",
		}
		for _, needle := range failCases {
			t.Run(needle, func(t *testing.T) {
				s := newTestStore(t)
				if strings.Contains(needle, "CREATE TRIGGER prompt_fts_insert") {
					if _, err := s.db.Exec(`
						DROP TRIGGER IF EXISTS prompt_fts_insert;
						DROP TRIGGER IF EXISTS prompt_fts_update;
						DROP TRIGGER IF EXISTS prompt_fts_delete;
					`); err != nil {
						t.Fatalf("drop prompt triggers: %v", err)
					}
				}
				origExec := s.hooks.exec
				s.hooks.exec = func(db execer, query string, args ...any) (sql.Result, error) {
					if strings.Contains(query, needle) {
						return nil, errors.New("forced migrate failure")
					}
					return origExec(db, query, args...)
				}
				if err := s.migrate(); err == nil {
					t.Fatalf("expected migrate error for %q", needle)
				}
			})
		}
	})

	t.Run("migrate addColumn and legacy-call propagation", func(t *testing.T) {
		t.Run("propagates addColumn error", func(t *testing.T) {
			s := newTestStore(t)
			origQueryIt := s.hooks.queryIt
			called := 0
			s.hooks.queryIt = func(db queryer, query string, args ...any) (rowScanner, error) {
				if strings.Contains(query, "PRAGMA table_info(observations)") {
					called++
					if called == 1 {
						return nil, errors.New("forced addColumn failure")
					}
				}
				return origQueryIt(db, query, args...)
			}
			if err := s.migrate(); err == nil {
				t.Fatalf("expected migrate to propagate addColumn failure")
			}
		})

		t.Run("propagates legacy migrate error", func(t *testing.T) {
			s := newTestStore(t)
			origQueryIt := s.hooks.queryIt
			called := 0
			s.hooks.queryIt = func(db queryer, query string, args ...any) (rowScanner, error) {
				if strings.Contains(query, "PRAGMA table_info(observations)") {
					called++
					if called == 9 {
						return nil, errors.New("forced legacy call failure")
					}
				}
				return origQueryIt(db, query, args...)
			}
			if err := s.migrate(); err == nil {
				t.Fatalf("expected migrate to propagate legacy migrate failure")
			}
		})
	})

	t.Run("add observation, prompt, update forced errors", func(t *testing.T) {
		s := newTestStore(t)
		if err := s.CreateSession("s-e", "engram", "/tmp/engram"); err != nil {
			t.Fatalf("create session: %v", err)
		}

		if _, err := s.AddObservation(AddObservationParams{SessionID: "s-e", Type: "note", Title: "top", Content: "x", Project: "engram", TopicKey: "x"}); err != nil {
			t.Fatalf("seed topic observation: %v", err)
		}
		origExec := s.hooks.exec
		s.hooks.exec = func(db execer, query string, args ...any) (sql.Result, error) {
			if strings.Contains(query, "SET session_id = ?") {
				return nil, errors.New("forced topic update error")
			}
			return origExec(db, query, args...)
		}
		if _, err := s.AddObservation(AddObservationParams{SessionID: "s-e", Type: "note", Title: "top", Content: "x", Project: "engram", TopicKey: "x"}); err == nil {
			t.Fatalf("expected topic upsert exec error")
		}

		s.hooks = defaultStoreHooks()
		if _, err := s.AddObservation(AddObservationParams{SessionID: "s-e", Type: "note", Title: "dup", Content: "dup content", Project: "engram"}); err != nil {
			t.Fatalf("seed dedupe observation: %v", err)
		}
		origExec = s.hooks.exec
		s.hooks.exec = func(db execer, query string, args ...any) (sql.Result, error) {
			if strings.Contains(query, "SET duplicate_count = duplicate_count + 1") {
				return nil, errors.New("forced dedupe update error")
			}
			return origExec(db, query, args...)
		}
		if _, err := s.AddObservation(AddObservationParams{SessionID: "s-e", Type: "note", Title: "dup", Content: "dup content", Project: "engram"}); err == nil {
			t.Fatalf("expected dedupe exec error")
		}

		if err := s.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
		if _, err := s.AddObservation(AddObservationParams{SessionID: "s-e", Type: "note", Title: "x", Content: "y", Project: "engram", TopicKey: "t"}); err == nil {
			t.Fatalf("expected topic query error on closed db")
		}
		if _, err := s.AddObservation(AddObservationParams{SessionID: "s-e", Type: "note", Title: "x", Content: "y", Project: "engram"}); err == nil {
			t.Fatalf("expected dedupe query error on closed db")
		}
		if _, err := s.AddPrompt(AddPromptParams{SessionID: "s-e", Content: "x"}); err == nil {
			t.Fatalf("expected add prompt error on closed db")
		}
	})

	t.Run("update observation remaining branches", func(t *testing.T) {
		s := newTestStore(t)
		if err := s.CreateSession("s-u", "engram", "/tmp/engram"); err != nil {
			t.Fatalf("create session: %v", err)
		}
		id, err := s.AddObservation(AddObservationParams{SessionID: "s-u", Type: "old", Title: "t", Content: "c", Project: "engram", TopicKey: "topic/key"})
		if err != nil {
			t.Fatalf("seed observation: %v", err)
		}

		if _, err := s.UpdateObservation(999999, UpdateObservationParams{}); err == nil {
			t.Fatalf("expected update missing observation error")
		}

		newType := "new-type"
		longContent := strings.Repeat("z", s.cfg.MaxObservationLength+50)
		if _, err := s.UpdateObservation(id, UpdateObservationParams{Type: &newType, Content: &longContent}); err != nil {
			t.Fatalf("update with type+truncation: %v", err)
		}

		origExec := s.hooks.exec
		s.hooks.exec = func(db execer, query string, args ...any) (sql.Result, error) {
			if strings.Contains(query, "UPDATE observations") {
				return nil, errors.New("forced update exec error")
			}
			return origExec(db, query, args...)
		}
		if _, err := s.UpdateObservation(id, UpdateObservationParams{}); err == nil {
			t.Fatalf("expected update exec error")
		}
	})

	t.Run("query iterator scan and rows.Err branches", func(t *testing.T) {
		s := newTestStore(t)
		origQueryIt := s.hooks.queryIt
		origQueryItContext := s.hooks.queryItContext

		setScanErr := func(match string) {
			s.hooks.queryIt = func(db queryer, query string, args ...any) (rowScanner, error) {
				if strings.Contains(query, match) {
					return &fakeRows{next: []bool{true, false}, scanErr: errors.New("forced scan error")}, nil
				}
				return origQueryIt(db, query, args...)
			}
			s.hooks.queryItContext = func(ctx context.Context, db *sql.DB, query string, args ...any) (rowScanner, error) {
				if strings.Contains(query, match) {
					return &fakeRows{next: []bool{true, false}, scanErr: errors.New("forced scan error")}, nil
				}
				return origQueryItContext(ctx, db, query, args...)
			}
		}

		setRowsErr := func(match string) {
			s.hooks.queryIt = func(db queryer, query string, args ...any) (rowScanner, error) {
				if strings.Contains(query, match) {
					return &fakeRows{next: []bool{false}, err: errors.New("forced rows err")}, nil
				}
				return origQueryIt(db, query, args...)
			}
			s.hooks.queryItContext = func(ctx context.Context, db *sql.DB, query string, args ...any) (rowScanner, error) {
				if strings.Contains(query, match) {
					return &fakeRows{next: []bool{false}, err: errors.New("forced rows err")}, nil
				}
				return origQueryItContext(ctx, db, query, args...)
			}
		}

		if err := s.CreateSession("s-iter", "engram", "/tmp/engram"); err != nil {
			t.Fatalf("create session: %v", err)
		}
		if _, err := s.AddObservation(AddObservationParams{SessionID: "s-iter", Type: "note", Title: "one", Content: "one", Project: "engram"}); err != nil {
			t.Fatalf("add observation: %v", err)
		}
		if _, err := s.AddPrompt(AddPromptParams{SessionID: "s-iter", Content: "prompt", Project: "engram"}); err != nil {
			t.Fatalf("add prompt: %v", err)
		}

		setScanErr("FROM sessions s")
		if _, err := s.RecentSessions("", 10); err == nil {
			t.Fatalf("expected recent sessions scan error")
		}

		setScanErr("FROM sessions s")
		if _, err := s.AllSessions("", 10); err == nil {
			t.Fatalf("expected all sessions scan error")
		}

		setScanErr("FROM user_prompts")
		if _, err := s.RecentPrompts("", 10); err == nil {
			t.Fatalf("expected recent prompts scan error")
		}

		setScanErr("FROM prompts_fts")
		if _, err := s.SearchPrompts("prompt", "", 10); err == nil {
			t.Fatalf("expected search prompts scan error")
		}

		setScanErr("FROM observations_fts")
		if _, err := s.Search("one", SearchOptions{}); err == nil {
			t.Fatalf("expected search scan error")
		}

		setRowsErr("FROM observations_fts")
		if _, err := s.Search("one", SearchOptions{}); err == nil {
			t.Fatalf("expected search rows err")
		}

		setScanErr("SELECT id, ifnull(project, ''), directory")
		if _, err := s.Export(); err == nil {
			t.Fatalf("expected export sessions scan error")
		}

		setRowsErr("SELECT id, ifnull(project, ''), directory")
		if _, err := s.Export(); err == nil {
			t.Fatalf("expected export sessions rows err")
		}

		setScanErr("FROM observations ORDER BY id")
		if _, err := s.Export(); err == nil {
			t.Fatalf("expected export observations scan error")
		}

		setRowsErr("FROM observations ORDER BY id")
		if _, err := s.Export(); err == nil {
			t.Fatalf("expected export observations rows err")
		}

		setScanErr("FROM user_prompts ORDER BY id")
		if _, err := s.Export(); err == nil {
			t.Fatalf("expected export prompts scan error")
		}

		setRowsErr("FROM user_prompts ORDER BY id")
		if _, err := s.Export(); err == nil {
			t.Fatalf("expected export prompts rows err")
		}

		setScanErr("FROM sync_chunks")
		if _, err := s.GetSyncedChunks(); err == nil {
			t.Fatalf("expected synced chunks scan error")
		}

		setRowsErr("PRAGMA table_info(extra_table)")
		if _, err := s.db.Exec(`CREATE TABLE extra_table (id INTEGER)`); err != nil {
			t.Fatalf("create extra table: %v", err)
		}
		if err := s.addColumnIfNotExists("extra_table", "n", "TEXT"); err == nil {
			t.Fatalf("expected add column rows err")
		}

		setScanErr("PRAGMA table_info(extra_table)")
		if err := s.addColumnIfNotExists("extra_table", "n2", "TEXT"); err == nil {
			t.Fatalf("expected add column scan error")
		}

		setRowsErr("PRAGMA table_info(observations)")
		if err := s.migrateLegacyObservationsTable(); err == nil {
			t.Fatalf("expected legacy migrate pragma rows err")
		}

		setScanErr("PRAGMA table_info(observations)")
		if err := s.migrateLegacyObservationsTable(); err == nil {
			t.Fatalf("expected legacy migrate pragma scan error")
		}

		s.hooks.queryIt = origQueryIt
	})

	t.Run("migration helpers close rows on scan errors", func(t *testing.T) {
		s := newTestStore(t)
		if _, err := s.db.Exec(`CREATE TABLE extra_table (id INTEGER)`); err != nil {
			t.Fatalf("create extra table: %v", err)
		}

		cases := []struct {
			name        string
			queryNeedle string
			run         func() error
		}{
			{
				name:        "add column",
				queryNeedle: "PRAGMA table_info(extra_table)",
				run:         func() error { return s.addColumnIfNotExists("extra_table", "n3", "TEXT") },
			},
			{
				name:        "sync chunks migration",
				queryNeedle: "PRAGMA table_info(sync_chunks)",
				run:         s.migrateSyncChunksTable,
			},
			{
				name:        "legacy observations migration",
				queryNeedle: "PRAGMA table_info(observations)",
				run:         s.migrateLegacyObservationsTable,
			},
		}

		origQueryIt := s.hooks.queryIt
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				forcedRows := &fakeRows{next: []bool{true}, scanErr: errors.New("forced migration scan error")}
				s.hooks.queryIt = func(db queryer, query string, args ...any) (rowScanner, error) {
					if strings.Contains(query, tc.queryNeedle) {
						return forcedRows, nil
					}
					return origQueryIt(db, query, args...)
				}

				if err := tc.run(); err == nil {
					t.Fatalf("expected scan error")
				}
				if !forcedRows.closed {
					t.Fatalf("expected rows to be closed after scan error")
				}
			})
		}
		s.hooks.queryIt = origQueryIt
	})

	t.Run("timeline and search type filter branches", func(t *testing.T) {
		s := newTestStore(t)
		if err := s.CreateSession("s-t2", "engram", "/tmp/engram"); err != nil {
			t.Fatalf("create session: %v", err)
		}
		first, _ := s.AddObservation(AddObservationParams{SessionID: "s-t2", Type: "decision", Title: "a", Content: "a", Project: "engram"})
		_, _ = s.AddObservation(AddObservationParams{SessionID: "s-t2", Type: "decision", Title: "aa", Content: "aa", Project: "engram"})
		focus, _ := s.AddObservation(AddObservationParams{SessionID: "s-t2", Type: "decision", Title: "b", Content: "b", Project: "engram"})
		_, _ = s.AddObservation(AddObservationParams{SessionID: "s-t2", Type: "decision", Title: "c", Content: "c", Project: "engram"})

		if _, err := s.Search("b", SearchOptions{Type: "decision", Project: "engram", Scope: "project", Limit: 5}); err != nil {
			t.Fatalf("search with type filter: %v", err)
		}

		origQueryIt := s.hooks.queryIt
		s.hooks.queryIt = func(db queryer, query string, args ...any) (rowScanner, error) {
			if strings.Contains(query, "id < ?") {
				return nil, errors.New("forced before query error")
			}
			return origQueryIt(db, query, args...)
		}
		if _, err := s.Timeline(focus, 2, 2); err == nil {
			t.Fatalf("expected timeline before query error")
		}

		s.hooks.queryIt = func(db queryer, query string, args ...any) (rowScanner, error) {
			if strings.Contains(query, "id < ?") {
				return &fakeRows{next: []bool{true, false}, scanErr: errors.New("forced before scan error")}, nil
			}
			return origQueryIt(db, query, args...)
		}
		if _, err := s.Timeline(focus, 2, 2); err == nil {
			t.Fatalf("expected timeline before scan error")
		}

		s.hooks.queryIt = func(db queryer, query string, args ...any) (rowScanner, error) {
			if strings.Contains(query, "id < ?") {
				return &fakeRows{next: []bool{false}, err: errors.New("forced before rows err")}, nil
			}
			return origQueryIt(db, query, args...)
		}
		if _, err := s.Timeline(focus, 2, 2); err == nil {
			t.Fatalf("expected timeline before rows err")
		}

		s.hooks.queryIt = func(db queryer, query string, args ...any) (rowScanner, error) {
			if strings.Contains(query, "id > ?") {
				return nil, errors.New("forced after query error")
			}
			return origQueryIt(db, query, args...)
		}
		if _, err := s.Timeline(focus, 2, 2); err == nil {
			t.Fatalf("expected timeline after query error")
		}

		s.hooks.queryIt = func(db queryer, query string, args ...any) (rowScanner, error) {
			if strings.Contains(query, "id > ?") {
				return &fakeRows{next: []bool{true, false}, scanErr: errors.New("forced after scan error")}, nil
			}
			return origQueryIt(db, query, args...)
		}
		if _, err := s.Timeline(focus, 2, 2); err == nil {
			t.Fatalf("expected timeline after scan error")
		}

		s.hooks.queryIt = func(db queryer, query string, args ...any) (rowScanner, error) {
			if strings.Contains(query, "id > ?") {
				return &fakeRows{next: []bool{false}, err: errors.New("forced after rows err")}, nil
			}
			return origQueryIt(db, query, args...)
		}
		if _, err := s.Timeline(focus, 2, 2); err == nil {
			t.Fatalf("expected timeline after rows err")
		}

		s.hooks.queryIt = origQueryIt
		tl, err := s.Timeline(first, 5, 5)
		if err != nil {
			t.Fatalf("timeline reverse branch run: %v", err)
		}
		if len(tl.After) == 0 {
			t.Fatalf("expected timeline after entries")
		}
	})

	t.Run("format context and stats remaining branches", func(t *testing.T) {
		s := newTestStore(t)
		if err := s.CreateSession("s-c", "engram", "/tmp/engram"); err != nil {
			t.Fatalf("create session: %v", err)
		}
		if _, err := s.AddObservation(AddObservationParams{SessionID: "s-c", Type: "note", Title: "n", Content: "n", Project: "engram"}); err != nil {
			t.Fatalf("add obs: %v", err)
		}

		origQueryIt := s.hooks.queryIt
		s.hooks.queryIt = func(db queryer, query string, args ...any) (rowScanner, error) {
			if strings.Contains(query, "FROM observations o") && strings.Contains(query, "WHERE o.deleted_at IS NULL") {
				return nil, errors.New("forced recent observations error")
			}
			return origQueryIt(db, query, args...)
		}
		if _, err := s.FormatContext("engram", "project"); err == nil {
			t.Fatalf("expected format context observations error")
		}

		s.hooks.queryIt = func(db queryer, query string, args ...any) (rowScanner, error) {
			if strings.Contains(query, "GROUP BY project") {
				return nil, errors.New("forced stats query error")
			}
			return origQueryIt(db, query, args...)
		}
		if _, err := s.Stats(); err != nil {
			t.Fatalf("stats should swallow project query errors: %v", err)
		}

		if err := s.EndSession("s-c", "has summary"); err != nil {
			t.Fatalf("end session: %v", err)
		}
		s.hooks.queryIt = origQueryIt
		ctx, err := s.FormatContext("engram", "project")
		if err != nil {
			t.Fatalf("format context with summary: %v", err)
		}
		if !strings.Contains(ctx, "has summary") {
			t.Fatalf("expected session summary included in context")
		}
	})

	t.Run("helper query errors and legacy migration late-stage failures", func(t *testing.T) {
		s := newTestStore(t)
		if err := s.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
		if _, err := s.GetSyncedChunks(); err == nil {
			t.Fatalf("expected synced chunks query error")
		}
		if _, err := s.queryObservations("SELECT id FROM observations"); err == nil {
			t.Fatalf("expected queryObservations query error")
		}
		if err := s.addColumnIfNotExists("observations", "x", "TEXT"); err == nil {
			t.Fatalf("expected addColumn query error")
		}
		if err := s.migrateLegacyObservationsTable(); err == nil {
			t.Fatalf("expected legacy migrate query error")
		}

		s2 := newTestStore(t)
		if _, err := s2.db.Exec(`
			DROP TRIGGER IF EXISTS obs_fts_insert;
			DROP TRIGGER IF EXISTS obs_fts_update;
			DROP TRIGGER IF EXISTS obs_fts_delete;
			DROP TABLE IF EXISTS observations_fts;
			DROP TABLE observations;
			INSERT OR IGNORE INTO sessions (id, project, directory) VALUES ('s1', 'engram', '/tmp/engram');
			CREATE TABLE observations (
				id INT,
				session_id TEXT,
				type TEXT,
				title TEXT,
				content TEXT,
				tool_name TEXT,
				project TEXT,
				scope TEXT,
				topic_key TEXT,
				normalized_hash TEXT,
				revision_count INTEGER,
				duplicate_count INTEGER,
				last_seen_at TEXT,
				created_at TEXT,
				updated_at TEXT,
				deleted_at TEXT
			);
			INSERT INTO observations (id, session_id, type, title, content, project, created_at, updated_at)
			VALUES (1, 's1', 'bugfix', 'legacy', 'legacy row', 'engram', datetime('now'), datetime('now'));
		`); err != nil {
			t.Fatalf("prepare legacy table: %v", err)
		}

		lateFail := []string{"INSERT INTO observations_migrated", "DROP TABLE observations", "RENAME TO observations", "CREATE VIRTUAL TABLE observations_fts"}
		for _, needle := range lateFail {
			t.Run(needle, func(t *testing.T) {
				s3 := newTestStore(t)
				if _, err := s3.db.Exec(`
					DROP TRIGGER IF EXISTS obs_fts_insert;
					DROP TRIGGER IF EXISTS obs_fts_update;
					DROP TRIGGER IF EXISTS obs_fts_delete;
					DROP TABLE IF EXISTS observations_fts;
					DROP TABLE observations;
					INSERT OR IGNORE INTO sessions (id, project, directory) VALUES ('s1', 'engram', '/tmp/engram');
					CREATE TABLE observations (
						id INT,
						session_id TEXT,
						type TEXT,
						title TEXT,
						content TEXT,
						tool_name TEXT,
						project TEXT,
						scope TEXT,
						topic_key TEXT,
						normalized_hash TEXT,
						revision_count INTEGER,
						duplicate_count INTEGER,
						last_seen_at TEXT,
						created_at TEXT,
						updated_at TEXT,
						deleted_at TEXT
					);
					INSERT INTO observations (id, session_id, type, title, content, project, created_at, updated_at)
					VALUES (1, 's1', 'bugfix', 'legacy', 'legacy row', 'engram', datetime('now'), datetime('now'));
				`); err != nil {
					t.Fatalf("prepare legacy schema: %v", err)
				}

				origExec := s3.hooks.exec
				s3.hooks.exec = func(db execer, query string, args ...any) (sql.Result, error) {
					if strings.Contains(query, needle) {
						return nil, errors.New("forced legacy late failure")
					}
					return origExec(db, query, args...)
				}
				if err := s3.migrateLegacyObservationsTable(); err == nil {
					t.Fatalf("expected legacy migrate error for %q", needle)
				}
			})
		}
	})
}

// ─── Issue #25: Session collision regression tests ──────────────────────────

func TestCreateSessionRejectsEmptyProjectWithoutPartialState(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("sess-upsert", "", ""); !errors.Is(err, ErrProjectRequired) {
		t.Fatalf("empty-project create error = %v, want ErrProjectRequired", err)
	}
	if _, err := s.GetSession("sess-upsert"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("empty-project create left session state: %v", err)
	}
	var mutations int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM sync_mutations`).Scan(&mutations); err != nil || mutations != 0 {
		t.Fatalf("empty-project create mutations = %d, err=%v, want 0", mutations, err)
	}

	// A later valid request remains an ordinary normalized create.
	if err := s.CreateSession("sess-upsert", "projectA", "/tmp/a"); err != nil {
		t.Fatalf("valid create session: %v", err)
	}

	sess, err := s.GetSession("sess-upsert")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if sess.Project != "projecta" {
		t.Fatalf("expected project=projecta after upsert (normalized), got %q", sess.Project)
	}
	if sess.Directory != "/tmp/a" {
		t.Fatalf("expected directory=/tmp/a after upsert, got %q", sess.Directory)
	}
}

func TestCreateSessionRejectsBlankIDWithoutPersistence(t *testing.T) {
	for _, id := range []string{"", " \t\n "} {
		t.Run(fmt.Sprintf("%q", id), func(t *testing.T) {
			s := newTestStore(t)
			if err := s.CreateSession(id, "engram", "/tmp/engram"); err == nil || !strings.Contains(err.Error(), "session id is required") {
				t.Fatalf("CreateSession error = %v, want required-id error", err)
			}
			var sessions, mutations int
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&sessions); err != nil {
				t.Fatalf("count sessions: %v", err)
			}
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM sync_mutations`).Scan(&mutations); err != nil {
				t.Fatalf("count mutations: %v", err)
			}
			if sessions != 0 || mutations != 0 {
				t.Fatalf("blank ID persisted sessions=%d mutations=%d", sessions, mutations)
			}
		})
	}
}

func TestSessionIdentityPreservesNonblankWhitespace(t *testing.T) {
	const id = " session with surrounding whitespace "

	t.Run("creation and journal", func(t *testing.T) {
		s := newTestStore(t)
		if err := s.CreateSession(id, "engram", "/tmp/engram"); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		session, err := s.GetSession(id)
		if err != nil || session.ID != id {
			t.Fatalf("GetSession = %+v, %v; want exact ID %q", session, err, id)
		}
		var entityKey, raw string
		if err := s.db.QueryRow(`SELECT entity_key, payload FROM sync_mutations WHERE entity = ?`, SyncEntitySession).Scan(&entityKey, &raw); err != nil {
			t.Fatalf("load session mutation: %v", err)
		}
		var payload syncSessionPayload
		if err := decodeSyncPayload([]byte(raw), &payload); err != nil {
			t.Fatalf("decode mutation: %v", err)
		}
		if entityKey != id || payload.ID != id {
			t.Fatalf("journal identity key=%q payload=%q, want %q", entityKey, payload.ID, id)
		}
	})

	t.Run("import", func(t *testing.T) {
		s := newTestStore(t)
		if _, err := s.Import(&ExportData{Sessions: []Session{{ID: id, Project: "engram", Directory: "/tmp/engram"}}}); err != nil {
			t.Fatalf("Import: %v", err)
		}
		session, err := s.GetSession(id)
		if err != nil || session.ID != id {
			t.Fatalf("GetSession = %+v, %v; want exact ID %q", session, err, id)
		}
	})

	t.Run("pulled mutation", func(t *testing.T) {
		s := newTestStore(t)
		payload := fmt.Sprintf(`{"id":%q,"project":"engram","directory":"/tmp/engram"}`, id)
		if err := s.ApplyPulledMutation(DefaultSyncTargetKey, SyncMutation{Seq: 1, Entity: SyncEntitySession, EntityKey: id, Op: SyncOpUpsert, Payload: payload}); err != nil {
			t.Fatalf("ApplyPulledMutation: %v", err)
		}
		session, err := s.GetSession(id)
		if err != nil || session.ID != id {
			t.Fatalf("GetSession = %+v, %v; want exact ID %q", session, err, id)
		}
	})
}

func TestCreateSessionMutationUsesPersistedCanonicalData(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("canonical-session", "engram", "/canonical"); err != nil {
		t.Fatalf("initial CreateSession: %v", err)
	}
	if err := s.CreateSession("canonical-session", "other", "/stale"); err != nil {
		t.Fatalf("idempotent CreateSession: %v", err)
	}
	var raw string
	if err := s.db.QueryRow(`SELECT payload FROM sync_mutations WHERE entity = ? AND entity_key = ? ORDER BY seq DESC LIMIT 1`, SyncEntitySession, "canonical-session").Scan(&raw); err != nil {
		t.Fatalf("load mutation: %v", err)
	}
	var payload syncSessionPayload
	if err := decodeSyncPayload([]byte(raw), &payload); err != nil {
		t.Fatalf("decode mutation: %v", err)
	}
	if payload.Project != "engram" || payload.Directory != "/canonical" {
		t.Fatalf("mutation payload = %+v, want persisted canonical session", payload)
	}
}

func TestImportRejectsBlankSessionIDAtomically(t *testing.T) {
	s := newTestStore(t)
	data := &ExportData{Sessions: []Session{
		{ID: "valid-import", Project: "engram", Directory: "/tmp/engram"},
		{ID: " \t", Project: "engram", Directory: "/tmp/engram"},
	}}
	if _, err := s.Import(data); err == nil || !strings.Contains(err.Error(), "session id is required") {
		t.Fatalf("Import error = %v, want required-id error", err)
	}
	var sessions int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&sessions); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if sessions != 0 {
		t.Fatalf("import partially persisted %d sessions", sessions)
	}
}

func TestEndSessionRejectsRawBlankIDWithoutMutation(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.DB().Exec(`INSERT INTO sessions (id, project, directory, summary) VALUES ('', 'engram', '/tmp', 'before')`); err != nil {
		t.Fatalf("seed corrupt session: %v", err)
	}
	if err := s.EndSession("", "after"); !errors.Is(err, ErrSessionIDRequired) {
		t.Fatalf("EndSession error = %v, want ErrSessionIDRequired", err)
	}
	var endedAt, summary sql.NullString
	if err := s.DB().QueryRow(`SELECT ended_at, summary FROM sessions WHERE id = ''`).Scan(&endedAt, &summary); err != nil || endedAt.Valid || !summary.Valid || summary.String != "before" {
		t.Fatalf("corrupt session ended_at=%+v summary=%+v err=%v", endedAt, summary, err)
	}
	var mutations int
	if err := s.DB().QueryRow(`SELECT count(*) FROM sync_mutations WHERE entity = ? AND `+sqlSessionIDBlank("entity_key"), SyncEntitySession, sqlWhitespaceTrimSet).Scan(&mutations); err != nil || mutations != 0 {
		t.Fatalf("blank session mutations=%d, err=%v", mutations, err)
	}
}

func TestDeleteSessionRejectsRawBlankIDsWithoutMutation(t *testing.T) {
	for _, tc := range []struct {
		name, id, project string
		enrolled          bool
	}{
		{name: "unenrolled empty", id: "", project: "local"},
		{name: "enrolled whitespace", id: " \t", project: "synced", enrolled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			if _, err := s.DB().Exec(`INSERT INTO sessions (id, project, directory) VALUES (?, ?, '/tmp'); INSERT INTO sync_enrolled_projects (project) SELECT ? WHERE ?`, tc.id, tc.project, tc.project, tc.enrolled); err != nil {
				t.Fatalf("seed corrupt session: %v", err)
			}
			if err := s.DeleteSession(tc.id); !errors.Is(err, ErrSessionIDRequired) {
				t.Fatalf("DeleteSession error = %v, want ErrSessionIDRequired", err)
			}
			var sessions, mutations int
			if err := s.DB().QueryRow(`SELECT count(*) FROM sessions WHERE id = ?`, tc.id).Scan(&sessions); err != nil {
				t.Fatalf("count corrupt session: %v", err)
			}
			if err := s.DB().QueryRow(`SELECT count(*) FROM sync_mutations WHERE entity = ? AND `+sqlSessionIDBlank("entity_key"), SyncEntitySession, sqlWhitespaceTrimSet).Scan(&mutations); err != nil || sessions != 1 || mutations != 0 {
				t.Fatalf("sessions=%d blank mutations=%d err=%v", sessions, mutations, err)
			}
		})
	}
}

func TestEnqueueSessionMutationRejectsBlankKeyAndRollsBack(t *testing.T) {
	s := newTestStore(t)
	err := s.withTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO sessions (id, project, directory) VALUES ('prior-write', 'engram', '/tmp')`); err != nil {
			return err
		}
		return s.enqueueSyncMutationTx(tx, SyncEntitySession, "", SyncOpUpsert, syncSessionPayload{})
	})
	if !errors.Is(err, ErrSessionIDRequired) {
		t.Fatalf("enqueue error = %v, want ErrSessionIDRequired", err)
	}
	var sessions, mutations int
	if err := s.DB().QueryRow(`SELECT count(*) FROM sessions WHERE id = 'prior-write'`).Scan(&sessions); err != nil {
		t.Fatalf("count preceding write: %v", err)
	}
	if err := s.DB().QueryRow(`SELECT count(*) FROM sync_mutations WHERE entity = ? AND `+sqlSessionIDBlank("entity_key"), SyncEntitySession, sqlWhitespaceTrimSet).Scan(&mutations); err != nil || sessions != 0 || mutations != 0 {
		t.Fatalf("sessions=%d blank mutations=%d err=%v", sessions, mutations, err)
	}
}

func TestApplyPulledSessionMutationSkipsInvalidIdentityWithEvidence(t *testing.T) {
	tests := []struct {
		name    string
		entity  string
		payload string
	}{
		{name: "both IDs empty", entity: " ", payload: `{"id":"","project":"engram","directory":"/remote"}`},
		{name: "empty entity key", entity: " ", payload: `{"id":"remote","project":"engram","directory":"/remote"}`},
		{name: "mismatched identity", entity: "other", payload: `{"id":"remote","project":"engram","directory":"/remote"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			mutation := SyncMutation{Seq: 1, Entity: SyncEntitySession, EntityKey: tc.entity, Op: SyncOpUpsert, Payload: tc.payload}
			if err := s.ApplyPulledMutation(DefaultSyncTargetKey, mutation); err != nil {
				t.Fatalf("ApplyPulledMutation: %v", err)
			}
			var sessions int
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&sessions); err != nil {
				t.Fatalf("count sessions: %v", err)
			}
			if sessions != 0 {
				t.Fatalf("invalid mutation persisted %d sessions", sessions)
			}
			rows, err := s.ListDeferred(ListDeferredOptions{Status: "dead"})
			if err != nil || len(rows) != 1 || rows[0].PayloadRaw != tc.payload || rows[0].TargetKey != DefaultSyncTargetKey || rows[0].ReasonCode != SyncSessionIdentityInvalidReasonCode || rows[0].RemoteSeq != 1 || rows[0].EntityKey != tc.entity || rows[0].Op != SyncOpUpsert {
				t.Fatalf("dead evidence=%+v, err=%v", rows, err)
			}
			if row, err := s.GetDeferred(rows[0].SyncID); err != nil || row.PayloadRaw != tc.payload {
				t.Fatalf("GetDeferred = %+v, %v", row, err)
			}
			state, err := s.GetSyncState(DefaultSyncTargetKey)
			if err != nil || state.LastPulledSeq != 1 {
				t.Fatalf("sync state=%+v, err=%v", state, err)
			}
		})
	}
}

func TestApplyPulledSessionInvalidIdentityDoesNotBlockLaterMutations(t *testing.T) {
	s := newTestStore(t)
	invalid := SyncMutation{Seq: 1, Entity: SyncEntitySession, EntityKey: " ", Op: SyncOpUpsert, Payload: `{"id":"","project":"engram","directory":"/bad"}`}
	valid := SyncMutation{Seq: 2, Entity: SyncEntitySession, EntityKey: "opaque id", Op: SyncOpUpsert, Payload: `{"id":"opaque id","project":"engram","directory":"/good"}`}
	for _, mutation := range []SyncMutation{invalid, valid} {
		if err := s.ApplyPulledMutation(DefaultSyncTargetKey, mutation); err != nil {
			t.Fatalf("ApplyPulledMutation seq=%d: %v", mutation.Seq, err)
		}
	}
	if session, err := s.GetSession("opaque id"); err != nil || session.Directory != "/good" {
		t.Fatalf("GetSession = %+v, %v", session, err)
	}
	if replay, err := s.ReplayDeferred(); err != nil || replay.Retried != 0 {
		t.Fatalf("ReplayDeferred = %+v, %v; want no retry", replay, err)
	}
}

// TestPulledSessionDeadLetterKeepsDistinctMutationsWithEqualSequence pins the
// dead-letter row identity to the mutation itself rather than to its position in
// the pull.
//
// The evidence row is the only record that remote data was discarded, so two
// different dropped mutations must never share one sync_id: the insert uses
// ON CONFLICT(sync_id) DO UPDATE, which would silently overwrite the first row
// and turn skip-plus-evidence back into a silent drop. Sequence numbers do not
// distinguish mutations on their own — ApplyPulledChunk replaces the remote
// sequence with the local cursor position, so a mutation carries whatever
// sequence the cursor happened to be at, including zero.
func TestPulledSessionDeadLetterKeepsDistinctMutationsWithEqualSequence(t *testing.T) {
	for _, seq := range []int64{0, 7} {
		t.Run(fmt.Sprintf("seq_%d", seq), func(t *testing.T) {
			s := newTestStore(t)
			mutations := []SyncMutation{
				{Seq: seq, Entity: SyncEntitySession, EntityKey: " ", Op: SyncOpUpsert, Payload: `{"id":"","project":"engram","directory":"/first"}`},
				{Seq: seq, Entity: SyncEntitySession, EntityKey: "\t", Op: SyncOpUpsert, Payload: `{"id":"","project":"engram","directory":"/second"}`},
			}
			if err := s.withTx(func(tx *sql.Tx) error {
				for _, mutation := range mutations {
					if err := s.deadLetterPulledSessionIdentityTx(tx, DefaultSyncTargetKey, mutation); err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				t.Fatalf("dead letter: %v", err)
			}

			rows, err := s.ListDeferred(ListDeferredOptions{Status: "dead", Limit: 50})
			if err != nil {
				t.Fatalf("ListDeferred: %v", err)
			}
			if len(rows) != 2 {
				t.Fatalf("dead rows=%d, want 2; distinct dropped mutations collapsed onto one sync_id: %+v", len(rows), rows)
			}
			payloads := map[string]bool{}
			for _, row := range rows {
				payloads[row.PayloadRaw] = true
			}
			for _, mutation := range mutations {
				if !payloads[mutation.Payload] {
					t.Fatalf("payload %q lost from dead-letter evidence: %+v", mutation.Payload, rows)
				}
			}

			evidence, err := s.ListQuarantinedPulledSessionEvidence("engram")
			if err != nil {
				t.Fatalf("ListQuarantinedPulledSessionEvidence: %v", err)
			}
			if len(evidence) != 2 {
				t.Fatalf("quarantine evidence=%+v, want both dropped mutations reported", evidence)
			}
		})
	}
}

// TestPulledSessionDeadLetterIsIdempotentAcrossRedelivery keeps the evidence
// honest in the other direction: one dropped remote mutation must stay one row.
//
// Chunks are deduplicated by a hash of their contents, so the same invalid
// session redelivered beside different companions arrives under a new chunk id
// and applies again at a later cursor position. Identity derived from that
// position would accumulate a fresh row per redelivery and overstate how much
// remote data was actually discarded.
func TestPulledSessionDeadLetterIsIdempotentAcrossRedelivery(t *testing.T) {
	s := newTestStore(t)
	invalid := SyncMutation{Entity: SyncEntitySession, EntityKey: " ", Op: SyncOpUpsert, Payload: `{"id":"","project":"engram","directory":"/bad"}`}
	first := SyncMutation{Entity: SyncEntitySession, EntityKey: "first", Op: SyncOpUpsert, Payload: `{"id":"first","project":"engram","directory":"/first"}`}
	second := SyncMutation{Entity: SyncEntitySession, EntityKey: "second", Op: SyncOpUpsert, Payload: `{"id":"second","project":"engram","directory":"/second"}`}

	if err := s.ApplyPulledChunk(DefaultSyncTargetKey, "chunk-one", []SyncMutation{invalid, first}); err != nil {
		t.Fatalf("ApplyPulledChunk chunk-one: %v", err)
	}
	if err := s.ApplyPulledChunk(DefaultSyncTargetKey, "chunk-two", []SyncMutation{invalid, second}); err != nil {
		t.Fatalf("ApplyPulledChunk chunk-two: %v", err)
	}

	rows, err := s.ListDeferred(ListDeferredOptions{Status: "dead", Limit: 50})
	if err != nil {
		t.Fatalf("ListDeferred: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("dead rows=%d, want 1; one dropped mutation must not accumulate evidence per redelivery: %+v", len(rows), rows)
	}
	if rows[0].PayloadRaw != invalid.Payload {
		t.Fatalf("dead row payload=%q, want %q", rows[0].PayloadRaw, invalid.Payload)
	}
	for _, id := range []string{"first", "second"} {
		if _, err := s.GetSession(id); err != nil {
			t.Fatalf("valid session %q missing after redelivery: %v", id, err)
		}
	}
}

// TestApplyPulledChunkRetainsEveryInvalidSessionInOneChunk proves the evidence
// survives the public apply path when a single chunk carries more than one
// invalid session, which is the shape no earlier test covered.
func TestApplyPulledChunkRetainsEveryInvalidSessionInOneChunk(t *testing.T) {
	s := newTestStore(t)
	mutations := []SyncMutation{
		{Entity: SyncEntitySession, EntityKey: " ", Op: SyncOpUpsert, Payload: `{"id":"","project":"engram","directory":"/a"}`},
		{Entity: SyncEntitySession, EntityKey: "\t", Op: SyncOpUpsert, Payload: `{"id":"","project":"engram","directory":"/b"}`},
	}
	if err := s.ApplyPulledChunk(DefaultSyncTargetKey, "two-invalid-sessions", mutations); err != nil {
		t.Fatalf("ApplyPulledChunk: %v", err)
	}
	evidence, err := s.ListQuarantinedPulledSessionEvidence("engram")
	if err != nil {
		t.Fatalf("ListQuarantinedPulledSessionEvidence: %v", err)
	}
	if len(evidence) != 2 {
		t.Fatalf("quarantine evidence=%+v, want one row per dropped mutation", evidence)
	}
	if evidence[0].SyncID == evidence[1].SyncID {
		t.Fatalf("dropped mutations share sync_id %q", evidence[0].SyncID)
	}
}

func TestApplyPulledChunkSkipsInvalidSessionButMalformedPayloadFailsClosed(t *testing.T) {
	valid := SyncMutation{Entity: SyncEntitySession, EntityKey: "valid", Op: SyncOpUpsert, Payload: `{"id":"valid","project":"engram","directory":"/good"}`}
	invalid := SyncMutation{Entity: SyncEntitySession, EntityKey: " ", Op: SyncOpUpsert, Payload: `{"id":"","project":"engram","directory":"/bad"}`}
	t.Run("skip and continue atomically", func(t *testing.T) {
		s := newTestStore(t)
		if err := s.ApplyPulledChunk(DefaultSyncTargetKey, "invalid-session", []SyncMutation{invalid, valid}); err != nil {
			t.Fatalf("ApplyPulledChunk: %v", err)
		}
		if _, err := s.GetSession("valid"); err != nil {
			t.Fatalf("valid session missing: %v", err)
		}
		state, _ := s.GetSyncState(DefaultSyncTargetKey)
		if state.LastPulledSeq != 2 {
			t.Fatalf("last pulled sequence=%d, want 2", state.LastPulledSeq)
		}
	})
	t.Run("malformed remains closed", func(t *testing.T) {
		s := newTestStore(t)
		bad := invalid
		bad.Payload = "not json"
		if err := s.ApplyPulledChunk(DefaultSyncTargetKey, "malformed-session", []SyncMutation{bad, valid}); err == nil {
			t.Fatal("ApplyPulledChunk succeeded for malformed session payload")
		}
		if _, err := s.GetSession("valid"); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("valid session applied despite rollback: %v", err)
		}
		rows, err := s.ListDeferred(ListDeferredOptions{})
		if err != nil || len(rows) != 0 {
			t.Fatalf("deferred rows=%+v, err=%v; want none", rows, err)
		}
	})
}

func TestApplyPulledLegacySessionMutationUsesEntityKeyForMissingPayloadID(t *testing.T) {
	s := newTestStore(t)
	const id = " legacy session "
	if err := s.ApplyPulledMutation(DefaultSyncTargetKey, SyncMutation{
		Seq:       1,
		Entity:    SyncEntitySession,
		EntityKey: id,
		Op:        SyncOpUpsert,
		Payload:   `{"project":"engram","directory":"/remote"}`,
	}); err != nil {
		t.Fatalf("ApplyPulledMutation: %v", err)
	}
	session, err := s.GetSession(id)
	if err != nil || session.ID != id {
		t.Fatalf("GetSession = %+v, %v; want exact legacy ID %q", session, err, id)
	}
}

func TestApplyPulledSessionMutationDoesNotNormalizeOpaqueIdentity(t *testing.T) {
	s := newTestStore(t)
	err := s.ApplyPulledMutation(DefaultSyncTargetKey, SyncMutation{
		Seq:       1,
		Entity:    SyncEntitySession,
		EntityKey: " session ",
		Op:        SyncOpUpsert,
		Payload:   `{"id":"session","project":"engram","directory":"/tmp/engram"}`,
	})
	if err != nil {
		t.Fatalf("ApplyPulledMutation: %v", err)
	}
	if _, err := s.GetSession("session"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("mismatched opaque identity was normalized: %v", err)
	}
}

func TestBackfillSkipsInvalidSourceAndBackfillsValidSession(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.db.Exec(`INSERT INTO sessions (id, project, directory) VALUES ('', 'engram', '/tmp/engram')`); err != nil {
		t.Fatalf("seed invalid session: %v", err)
	}
	if _, err := s.db.Exec(`INSERT INTO sessions (id, project, directory) VALUES ('valid-session', 'engram', '/tmp/engram')`); err != nil {
		t.Fatalf("seed valid session: %v", err)
	}
	err := s.withTx(func(tx *sql.Tx) error { return s.backfillSessionSyncMutationsTx(tx, "engram") })
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	var validMutations, invalidMutations int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sync_mutations WHERE entity = ? AND entity_key = ?`, SyncEntitySession, "valid-session").Scan(&validMutations); err != nil {
		t.Fatalf("count valid mutations: %v", err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sync_mutations WHERE entity = ? AND entity_key = ''`, SyncEntitySession).Scan(&invalidMutations); err != nil {
		t.Fatalf("count invalid mutations: %v", err)
	}
	if validMutations != 1 || invalidMutations != 0 {
		t.Fatalf("valid mutations=%d invalid mutations=%d", validMutations, invalidMutations)
	}
}

// blankSessionIDCases enumerates source identities that strings.TrimSpace
// reduces to the empty string. SQLite's bare trim() only strips U+0020, so
// every non-space case here proves the SQL predicates share the Go blank rule
// instead of falling back to the SQLite default.
var blankSessionIDCases = []struct {
	name string
	id   string
}{
	{name: "empty", id: ""},
	{name: "space only", id: " "},
	{name: "tab only", id: "\t"},
	{name: "newline only", id: "\n"},
	{name: "carriage return only", id: "\r"},
	{name: "vertical tab only", id: "\v"},
	{name: "form feed only", id: "\f"},
	{name: "mixed whitespace", id: " \t\r\n\v\f "},
	{name: "unicode space only", id: "  "},
}

// TestSQLSessionIDBlankPredicateMatchesGoRule is the contract test for the one
// blank rule: whatever isBlankSessionID answers in Go, the SQL predicate built
// from sqlWhitespaceTrimSet must answer the same inside SQLite.
func TestSQLSessionIDBlankPredicateMatchesGoRule(t *testing.T) {
	s := newTestStore(t)
	candidates := []string{
		"", " ", "\t", "\n", "\r", "\v", "\f", " \t\r\n\v\f ",
		"", " ", " ", " ", " ", " ", " ", " ", " ", "　",
		"  ", "session", " session ", "\tsession", "session\n", "-", "0",
	}
	for _, id := range candidates {
		var sqlBlank, sqlNotBlank bool
		if err := s.DB().QueryRow(`SELECT `+sqlSessionIDBlank("?")+`, `+sqlSessionIDNotBlank("?"), id, sqlWhitespaceTrimSet, id, sqlWhitespaceTrimSet).Scan(&sqlBlank, &sqlNotBlank); err != nil {
			t.Fatalf("evaluate predicate for %q: %v", id, err)
		}
		goBlank := isBlankSessionID(id)
		if sqlBlank != goBlank || sqlNotBlank == goBlank {
			t.Fatalf("id=%q sql blank=%t not-blank=%t, go blank=%t", id, sqlBlank, sqlNotBlank, goBlank)
		}
	}
}

// TestSQLWhitespaceTrimSetCoversEveryTrimSpaceRune keeps the generated trim set
// exhaustive: strings.TrimSpace strips exactly unicode.IsSpace runes, so every
// one of them must appear in the set handed to SQLite.
func TestSQLWhitespaceTrimSetCoversEveryTrimSpaceRune(t *testing.T) {
	inSet := make(map[rune]bool, len(sqlWhitespaceTrimSet))
	for _, r := range sqlWhitespaceTrimSet {
		if !unicode.IsSpace(r) {
			t.Fatalf("trim set contains non-whitespace rune %U", r)
		}
		inSet[r] = true
	}
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if unicode.IsSpace(r) && !inSet[r] {
			t.Fatalf("trim set is missing whitespace rune %U", r)
		}
	}
}

func TestProjectNeedsBackfillIgnoresBlankSessions(t *testing.T) {
	for _, tc := range blankSessionIDCases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			if _, err := s.DB().Exec(`INSERT INTO sessions (id, project, directory) VALUES (?, 'engram', '/tmp')`, tc.id); err != nil {
				t.Fatalf("seed blank session: %v", err)
			}
			if err := s.EnrollProject("engram"); err != nil {
				t.Fatalf("EnrollProject: %v", err)
			}
			needs, err := s.projectNeedsBackfill("engram")
			if err != nil || needs {
				t.Fatalf("blank-only projectNeedsBackfill = %v, %v", needs, err)
			}
			if _, err := s.DB().Exec(`INSERT INTO sessions (id, project, directory) VALUES ('valid', 'engram', '/tmp')`); err != nil {
				t.Fatalf("seed valid session: %v", err)
			}
			if err := s.repairEnrolledProjectSyncMutations(); err != nil {
				t.Fatalf("backfill mixed sessions: %v", err)
			}
			var mutations int
			if err := s.DB().QueryRow(`SELECT count(*) FROM sync_mutations WHERE entity = ?`, SyncEntitySession).Scan(&mutations); err != nil || mutations != 1 {
				t.Fatalf("session mutations=%d, err=%v; want only valid session", mutations, err)
			}
			var blankMutations int
			if err := s.DB().QueryRow(`SELECT count(*) FROM sync_mutations WHERE entity = ? AND entity_key = ?`, SyncEntitySession, tc.id).Scan(&blankMutations); err != nil || blankMutations != 0 {
				t.Fatalf("blank session mutations=%d, err=%v", blankMutations, err)
			}
		})
	}
}

// TestBackfillSessionSyncMutationsSkipsBlankSourceRows proves the SELECT that
// feeds the backfill uses the same blank rule as enqueueSyncMutationTx. A
// source row the SELECT keeps but the enqueue guard rejects aborts the whole
// transaction and rolls back every valid session backfilled alongside it.
func TestBackfillSessionSyncMutationsSkipsBlankSourceRows(t *testing.T) {
	for _, tc := range blankSessionIDCases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			if _, err := s.DB().Exec(`INSERT INTO sessions (id, project, directory) VALUES (?, 'engram', '/tmp/blank')`, tc.id); err != nil {
				t.Fatalf("seed blank session: %v", err)
			}
			if _, err := s.DB().Exec(`INSERT INTO sessions (id, project, directory) VALUES ('valid-session', 'engram', '/tmp/valid')`); err != nil {
				t.Fatalf("seed valid session: %v", err)
			}
			if err := s.withTx(func(tx *sql.Tx) error { return s.backfillSessionSyncMutationsTx(tx, "engram") }); err != nil {
				t.Fatalf("backfill: %v", err)
			}
			var valid, blank int
			if err := s.DB().QueryRow(`SELECT count(*) FROM sync_mutations WHERE entity = ? AND entity_key = 'valid-session'`, SyncEntitySession).Scan(&valid); err != nil {
				t.Fatalf("count valid mutations: %v", err)
			}
			if err := s.DB().QueryRow(`SELECT count(*) FROM sync_mutations WHERE entity = ? AND entity_key = ?`, SyncEntitySession, tc.id).Scan(&blank); err != nil {
				t.Fatalf("count blank mutations: %v", err)
			}
			if valid != 1 || blank != 0 {
				t.Fatalf("valid mutations=%d blank mutations=%d", valid, blank)
			}
		})
	}
}

// TestListInvalidSessionIdentityEvidenceDetectsEveryBlankSourceRow proves the
// doctor source-row scan cannot be bypassed by a legacy identity built from
// whitespace SQLite's trim() does not strip.
func TestListInvalidSessionIdentityEvidenceDetectsEveryBlankSourceRow(t *testing.T) {
	for _, tc := range blankSessionIDCases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			if _, err := s.DB().Exec(`INSERT INTO sessions (id, project, directory) VALUES (?, 'engram', '/tmp/blank')`, tc.id); err != nil {
				t.Fatalf("seed blank session: %v", err)
			}
			if _, err := s.DB().Exec(`INSERT INTO sessions (id, project, directory) VALUES ('valid-session', 'engram', '/tmp/valid')`); err != nil {
				t.Fatalf("seed valid session: %v", err)
			}
			evidence, err := s.ListInvalidSessionIdentityEvidence("engram")
			if err != nil {
				t.Fatalf("ListInvalidSessionIdentityEvidence: %v", err)
			}
			if len(evidence) != 1 || evidence[0].SessionID != tc.id {
				t.Fatalf("evidence=%+v, want the single blank source row %q", evidence, tc.id)
			}
		})
	}
}

// TestSessionIdentityPreservesWhitespacePaddedIdentities guards the other
// direction: identities that merely contain whitespace are not blank, stay
// byte-exact, and are never reported as corrupt.
func TestSessionIdentityPreservesWhitespacePaddedIdentities(t *testing.T) {
	for _, id := range []string{"\tsession", "session\n", " \r session \v ", " session "} {
		t.Run(fmt.Sprintf("%q", id), func(t *testing.T) {
			s := newTestStore(t)
			if err := s.CreateSession(id, "engram", "/tmp"); err != nil {
				t.Fatalf("CreateSession: %v", err)
			}
			evidence, err := s.ListInvalidSessionIdentityEvidence("engram")
			if err != nil || len(evidence) != 0 {
				t.Fatalf("evidence=%+v, err=%v; want no invalid identity", evidence, err)
			}
			var entityKey string
			if err := s.DB().QueryRow(`SELECT entity_key FROM sync_mutations WHERE entity = ?`, SyncEntitySession).Scan(&entityKey); err != nil {
				t.Fatalf("load session mutation: %v", err)
			}
			if entityKey != id {
				t.Fatalf("entity_key=%q, want byte-exact %q", entityKey, id)
			}
		})
	}
}

func TestNewAllowsEnrolledInvalidSessionIdentityForDiagnostics(t *testing.T) {
	cfg := mustDefaultConfig(t)
	cfg.DataDir = t.TempDir()
	seed, err := New(cfg)
	if err != nil {
		t.Fatalf("initialize store: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close initialized store: %v", err)
	}

	db, err := sql.Open("sqlite", filepath.Join(cfg.DataDir, "engram.db"))
	if err != nil {
		t.Fatalf("open raw database: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO sessions (id, project, directory) VALUES ('', 'engram', '/tmp/engram');
		INSERT INTO sessions (id, project, directory) VALUES ('valid-session', 'engram', '/tmp/engram');
		INSERT INTO sync_enrolled_projects (project) VALUES ('engram');`); err != nil {
		_ = db.Close()
		t.Fatalf("seed enrolled corrupt store: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close raw database: %v", err)
	}

	s, err := New(cfg)
	if err != nil {
		t.Fatalf("Store.New must allow diagnostics for corrupt enrolled store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	evidence, err := s.ListInvalidSessionIdentityEvidence("engram")
	if err != nil || len(evidence) != 1 {
		t.Fatalf("ListInvalidSessionIdentityEvidence = %+v, %v", evidence, err)
	}
	if evidence[0].SessionID != "" {
		t.Fatalf("diagnostic session ID = %q, want preserved invalid identity", evidence[0].SessionID)
	}
	var validMutations, invalidMutations int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sync_mutations WHERE entity = ? AND entity_key = ?`, SyncEntitySession, "valid-session").Scan(&validMutations); err != nil {
		t.Fatalf("count valid session mutations: %v", err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sync_mutations WHERE entity = ? AND entity_key = ''`, SyncEntitySession).Scan(&invalidMutations); err != nil {
		t.Fatalf("count invalid session mutations: %v", err)
	}
	if validMutations != 0 || invalidMutations != 0 {
		t.Fatalf("startup valid mutations=%d invalid mutations=%d", validMutations, invalidMutations)
	}

	if err := s.EnsureEnrolledProjectSyncMutations(context.Background()); err != nil {
		t.Fatalf("ensure enrolled sync journal: %v", err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sync_mutations WHERE entity = ? AND entity_key = ?`, SyncEntitySession, "valid-session").Scan(&validMutations); err != nil {
		t.Fatalf("count valid session mutations after repair: %v", err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sync_mutations WHERE entity = ? AND entity_key = ''`, SyncEntitySession).Scan(&invalidMutations); err != nil {
		t.Fatalf("count invalid session mutations after repair: %v", err)
	}
	if validMutations != 1 || invalidMutations != 0 {
		t.Fatalf("repaired valid mutations=%d invalid mutations=%d", validMutations, invalidMutations)
	}
}

func TestCreateSessionDoesNotOverwriteExistingProject(t *testing.T) {
	s := newTestStore(t)

	// Create session with project A (normalized to "projecta")
	if err := s.CreateSession("sess-preserve", "projectA", "/tmp/a"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Second call with project B should NOT overwrite
	if err := s.CreateSession("sess-preserve", "projectB", "/tmp/b"); err != nil {
		t.Fatalf("upsert session: %v", err)
	}

	sess, err := s.GetSession("sess-preserve")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	// Project names are normalized to lowercase, so "projectA" is stored as "projecta"
	if sess.Project != "projecta" {
		t.Fatalf("expected project=projecta (preserved, normalized), got %q", sess.Project)
	}
	if sess.Directory != "/tmp/a" {
		t.Fatalf("expected directory=/tmp/a (preserved), got %q", sess.Directory)
	}
}

func TestCreateSessionPartialUpsert(t *testing.T) {
	s := newTestStore(t)

	t.Run("fills directory when project already set", func(t *testing.T) {
		if err := s.CreateSession("sess-partial-1", "myproject", ""); err != nil {
			t.Fatalf("create: %v", err)
		}
		// Second call fills directory but project stays
		if err := s.CreateSession("sess-partial-1", "other", "/new/dir"); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		sess, err := s.GetSession("sess-partial-1")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if sess.Project != "myproject" {
			t.Fatalf("project should be preserved, got %q", sess.Project)
		}
		if sess.Directory != "/new/dir" {
			t.Fatalf("directory should be filled, got %q", sess.Directory)
		}
	})

	t.Run("rejects missing project even when directory is present", func(t *testing.T) {
		if err := s.CreateSession("sess-partial-2", "", "/existing/dir"); !errors.Is(err, ErrProjectRequired) {
			t.Fatalf("create: %v, want ErrProjectRequired", err)
		}
		if _, err := s.GetSession("sess-partial-2"); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("missing-project create left a session: %v", err)
		}
	})

	t.Run("rejects fully empty request without a session", func(t *testing.T) {
		if err := s.CreateSession("sess-partial-3", "", ""); !errors.Is(err, ErrProjectRequired) {
			t.Fatalf("create: %v, want ErrProjectRequired", err)
		}
		if _, err := s.GetSession("sess-partial-3"); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("empty create left a session: %v", err)
		}
	})
}

func TestTruncateUTF8(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{name: "short ascii", in: "abc", max: 10, want: "abc"},
		{name: "exact length", in: "hello", max: 5, want: "hello"},
		{name: "long ascii", in: "abcdef", max: 3, want: "abc..."},
		{name: "spanish accents", in: "Decisión de arquitectura", max: 8, want: "Decisión..."},
		{name: "emoji", in: "🐛🔧🚀✨🎉💡", max: 3, want: "🐛🔧🚀..."},
		{name: "mixed ascii and multibyte", in: "café☕latte", max: 5, want: "café☕..."},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := truncate(tc.in, tc.max)
			if got != tc.want {
				t.Fatalf("truncate(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
			}
		})
	}
}

// ─── Project Enrollment CRUD Tests ───────────────────────────────────────────

func TestEnrollProjectBasic(t *testing.T) {
	s := newTestStore(t)

	// Enroll a project.
	if err := s.EnrollProject("engram"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}

	// Verify it shows up in the list.
	projects, err := s.ListEnrolledProjects()
	if err != nil {
		t.Fatalf("list enrolled projects: %v", err)
	}
	if len(projects) != 1 {
		t.Fatalf("expected 1 enrolled project, got %d", len(projects))
	}
	if projects[0].Project != "engram" {
		t.Fatalf("expected project 'engram', got %q", projects[0].Project)
	}
	if projects[0].EnrolledAt == "" {
		t.Fatal("expected enrolled_at to be set")
	}

	// Verify IsProjectEnrolled returns true.
	enrolled, err := s.IsProjectEnrolled("engram")
	if err != nil {
		t.Fatalf("is project enrolled: %v", err)
	}
	if !enrolled {
		t.Fatal("expected project to be enrolled")
	}
}

func TestEnrollProjectIdempotent(t *testing.T) {
	s := newTestStore(t)

	// Enroll twice — should not error.
	if err := s.EnrollProject("engram"); err != nil {
		t.Fatalf("first enroll: %v", err)
	}
	if err := s.EnrollProject("engram"); err != nil {
		t.Fatalf("second enroll (idempotent): %v", err)
	}

	// Should still be exactly one row.
	projects, err := s.ListEnrolledProjects()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(projects) != 1 {
		t.Fatalf("expected 1 enrolled project after double-enroll, got %d", len(projects))
	}
}

func TestEnrollAndLookupProjectNormalization(t *testing.T) {
	s := newTestStore(t)

	if err := s.EnrollProject("  ENGRAM__CORE  "); err != nil {
		t.Fatalf("enroll normalized project: %v", err)
	}

	projects, err := s.ListEnrolledProjects()
	if err != nil {
		t.Fatalf("list enrolled: %v", err)
	}
	if len(projects) != 1 || projects[0].Project != "engram_core" {
		t.Fatalf("expected canonical enrolled project engram_core, got %+v", projects)
	}

	enrolled, err := s.IsProjectEnrolled("engram__core")
	if err != nil {
		t.Fatalf("is enrolled: %v", err)
	}
	if !enrolled {
		t.Fatal("expected normalized enrollment lookup to succeed")
	}

	if err := s.UnenrollProject("ENGRAM__CORE"); err != nil {
		t.Fatalf("unenroll normalized project: %v", err)
	}
	enrolled, err = s.IsProjectEnrolled("engram_core")
	if err != nil {
		t.Fatalf("is enrolled after unenroll: %v", err)
	}
	if enrolled {
		t.Fatal("expected project to be unenrolled after normalized removal")
	}
}

func TestEnrollProjectBackfillsHistoricalMutations(t *testing.T) {
	s := newTestStore(t)

	if _, err := s.db.Exec(
		`INSERT INTO sessions (id, project, directory, ended_at, summary) VALUES (?, ?, ?, datetime('now'), ?)`,
		"legacy-session", "legacy-proj", "/tmp/legacy", "done",
	); err != nil {
		t.Fatalf("insert session: %v", err)
	}

	if _, err := s.db.Exec(
		`INSERT INTO observations (sync_id, session_id, type, title, content, project, scope, normalized_hash, revision_count, duplicate_count, last_seen_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, 1, datetime('now'), datetime('now'))`,
		"obs-legacy", "legacy-session", "decision", "Legacy obs", "Historical content", "legacy-proj", "project", hashNormalized("Historical content"),
	); err != nil {
		t.Fatalf("insert observation: %v", err)
	}

	if _, err := s.db.Exec(
		`INSERT INTO user_prompts (sync_id, session_id, content, project) VALUES (?, ?, ?, ?)`,
		"prompt-legacy", "legacy-session", "What happened before enterprise?", "legacy-proj",
	); err != nil {
		t.Fatalf("insert prompt: %v", err)
	}

	var before int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sync_mutations`).Scan(&before); err != nil {
		t.Fatalf("count mutations before enroll: %v", err)
	}
	if before != 0 {
		t.Fatalf("expected 0 sync mutations before enroll, got %d", before)
	}

	if err := s.EnrollProject("legacy-proj"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}

	mutations, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 10)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(mutations) != 3 {
		t.Fatalf("expected 3 backfilled mutations, got %d", len(mutations))
	}

	expected := map[string]string{
		SyncEntitySession:     "legacy-session",
		SyncEntityObservation: "obs-legacy",
		SyncEntityPrompt:      "prompt-legacy",
	}
	for _, mutation := range mutations {
		entityKey, ok := expected[mutation.Entity]
		if !ok {
			t.Fatalf("unexpected mutation entity %q", mutation.Entity)
		}
		if mutation.EntityKey != entityKey {
			t.Fatalf("expected entity_key %q for %s, got %q", entityKey, mutation.Entity, mutation.EntityKey)
		}
		if mutation.Project != "legacy-proj" {
			t.Fatalf("expected project legacy-proj, got %q", mutation.Project)
		}
	}
	state, err := s.GetSyncState(DefaultSyncTargetKey)
	if err != nil {
		t.Fatalf("get sync state: %v", err)
	}
	if state.LastEnqueuedSeq != 3 {
		t.Fatalf("expected last_enqueued_seq 3 after backfill, got %d", state.LastEnqueuedSeq)
	}
}

func TestEnrollProjectBackfillIsIdempotentAndSkipsExistingMutations(t *testing.T) {
	s := newTestStore(t)

	if _, err := s.db.Exec(
		`INSERT INTO sessions (id, project, directory) VALUES (?, ?, ?)`,
		"legacy-session", "legacy-proj", "/tmp/legacy",
	); err != nil {
		t.Fatalf("insert session: %v", err)
	}

	if _, err := s.db.Exec(
		`INSERT INTO observations (sync_id, session_id, type, title, content, project, scope, normalized_hash, revision_count, duplicate_count, last_seen_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, 1, datetime('now'), datetime('now'))`,
		"obs-legacy", "legacy-session", "decision", "Legacy obs", "Historical content", "legacy-proj", "project", hashNormalized("Historical content"),
	); err != nil {
		t.Fatalf("insert observation: %v", err)
	}

	if _, err := s.db.Exec(
		`INSERT INTO user_prompts (sync_id, session_id, content, project) VALUES (?, ?, ?, ?)`,
		"prompt-legacy", "legacy-session", "Historical prompt", "legacy-proj",
	); err != nil {
		t.Fatalf("insert prompt: %v", err)
	}

	if _, err := s.db.Exec(
		`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		DefaultSyncTargetKey, SyncEntityObservation, "obs-legacy", SyncOpUpsert, `{"sync_id":"obs-legacy","session_id":"legacy-session","project":"legacy-proj"}`, SyncSourceLocal, "legacy-proj",
	); err != nil {
		t.Fatalf("insert existing mutation: %v", err)
	}

	if err := s.EnrollProject("legacy-proj"); err != nil {
		t.Fatalf("first enroll: %v", err)
	}

	var afterFirst int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sync_mutations`).Scan(&afterFirst); err != nil {
		t.Fatalf("count after first enroll: %v", err)
	}
	if afterFirst != 3 {
		t.Fatalf("expected 3 total mutations after first enroll, got %d", afterFirst)
	}

	var observationMutations int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sync_mutations WHERE entity = ? AND entity_key = ?`, SyncEntityObservation, "obs-legacy").Scan(&observationMutations); err != nil {
		t.Fatalf("count observation mutations: %v", err)
	}
	if observationMutations != 1 {
		t.Fatalf("expected existing observation mutation to remain single, got %d rows", observationMutations)
	}

	if err := s.EnrollProject("legacy-proj"); err != nil {
		t.Fatalf("second enroll: %v", err)
	}

	var afterSecond int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sync_mutations`).Scan(&afterSecond); err != nil {
		t.Fatalf("count after second enroll: %v", err)
	}
	if afterSecond != afterFirst {
		t.Fatalf("expected no duplicate backfill on re-enroll, got %d mutations after second enroll vs %d after first", afterSecond, afterFirst)
	}
}

func TestEnrollProjectBackfillsSessionOwnedEntitiesWithEmptyProject(t *testing.T) {
	s := newTestStore(t)

	if _, err := s.db.Exec(
		`INSERT INTO sessions (id, project, directory) VALUES (?, ?, ?)`,
		"legacy-empty-project", "legacy-proj", "/tmp/legacy",
	); err != nil {
		t.Fatalf("insert session: %v", err)
	}

	if _, err := s.db.Exec(
		`INSERT INTO observations (sync_id, session_id, type, title, content, project, scope, normalized_hash, revision_count, duplicate_count, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, NULL, ?, ?, 2, 4, ?, ?)`,
		"obs-empty-project", "legacy-empty-project", "decision", "Legacy empty project obs", "historical empty project observation", "project", hashNormalized("historical empty project observation"), "2024-01-01 10:00:00", "2024-01-02 11:00:00",
	); err != nil {
		t.Fatalf("insert observation with empty project: %v", err)
	}

	if _, err := s.db.Exec(
		`INSERT INTO user_prompts (sync_id, session_id, content, project, created_at) VALUES (?, ?, ?, NULL, ?)`,
		"prompt-empty-project", "legacy-empty-project", "prompt for empty project entity", "2024-01-01 12:00:00",
	); err != nil {
		t.Fatalf("insert prompt with empty project: %v", err)
	}

	if err := s.EnrollProject("legacy-proj"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}

	mutations, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 10)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(mutations) != 3 {
		t.Fatalf("expected session + observation + prompt backfilled, got %d", len(mutations))
	}

	byEntity := map[string]string{}
	for _, mutation := range mutations {
		byEntity[mutation.Entity] = mutation.EntityKey
		if mutation.Project != "legacy-proj" {
			t.Fatalf("expected derived project legacy-proj for backfilled mutation, got %q", mutation.Project)
		}
	}
	if byEntity[SyncEntitySession] != "legacy-empty-project" {
		t.Fatalf("expected session backfill for legacy-empty-project, got %q", byEntity[SyncEntitySession])
	}
	if byEntity[SyncEntityObservation] != "obs-empty-project" {
		t.Fatalf("expected observation backfill for empty-project entity, got %q", byEntity[SyncEntityObservation])
	}
	if byEntity[SyncEntityPrompt] != "prompt-empty-project" {
		t.Fatalf("expected prompt backfill for empty-project entity, got %q", byEntity[SyncEntityPrompt])
	}

	var obsPayloadRaw string
	if err := s.db.QueryRow(
		`SELECT payload FROM sync_mutations WHERE entity = ? AND entity_key = ?`,
		SyncEntityObservation,
		"obs-empty-project",
	).Scan(&obsPayloadRaw); err != nil {
		t.Fatalf("query observation backfill payload: %v", err)
	}
	var obsPayload syncObservationPayload
	if err := decodeSyncPayload([]byte(obsPayloadRaw), &obsPayload); err != nil {
		t.Fatalf("decode observation backfill payload: %v", err)
	}
	if obsPayload.CreatedAt != "2024-01-01 10:00:00" || obsPayload.UpdatedAt != "2024-01-02 11:00:00" {
		t.Fatalf("expected backfill payload to preserve chronology metadata, got created_at=%q updated_at=%q", obsPayload.CreatedAt, obsPayload.UpdatedAt)
	}
	if obsPayload.RevisionCount != 2 || obsPayload.DuplicateCount != 4 {
		t.Fatalf("expected backfill payload to preserve revision metadata, got revision_count=%d duplicate_count=%d", obsPayload.RevisionCount, obsPayload.DuplicateCount)
	}
}

func TestEnrollProjectBackfillsSoftDeletedObservationDeleteMutations(t *testing.T) {
	s := newTestStore(t)

	if _, err := s.db.Exec(
		`INSERT INTO sessions (id, project, directory) VALUES (?, ?, ?)`,
		"legacy-soft-delete-session", "legacy-proj", "/tmp/legacy",
	); err != nil {
		t.Fatalf("insert session: %v", err)
	}

	if _, err := s.db.Exec(
		`INSERT INTO observations (sync_id, session_id, type, title, content, project, scope, normalized_hash, revision_count, duplicate_count, created_at, updated_at, deleted_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, 1, ?, ?, ?)`,
		"obs-soft-deleted", "legacy-soft-delete-session", "decision", "Legacy deleted obs", "historical deleted observation", "legacy-proj", "project", hashNormalized("historical deleted observation"),
		"2024-01-01 10:00:00", "2024-01-02 11:00:00", "2024-01-03 12:00:00",
	); err != nil {
		t.Fatalf("insert soft-deleted observation: %v", err)
	}

	if err := s.EnrollProject("legacy-proj"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}

	mutations, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 10)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}

	var foundDelete bool
	for _, mutation := range mutations {
		if mutation.Entity == SyncEntityObservation && mutation.EntityKey == "obs-soft-deleted" {
			if mutation.Op != SyncOpDelete {
				t.Fatalf("expected observation backfill op=delete for soft-deleted row, got %q", mutation.Op)
			}
			if mutation.Project != "legacy-proj" {
				t.Fatalf("expected derived project legacy-proj for soft-delete mutation, got %q", mutation.Project)
			}
			foundDelete = true
		}
	}
	if !foundDelete {
		t.Fatalf("expected soft-deleted observation delete mutation to be backfilled")
	}
}

func TestEnrollProjectBackfillsPromptDeleteTombstonesWithDerivedProject(t *testing.T) {
	s := newTestStore(t)

	if _, err := s.db.Exec(
		`INSERT INTO sessions (id, project, directory) VALUES (?, ?, ?)`,
		"legacy-prompt-session", "legacy-proj", "/tmp/legacy",
	); err != nil {
		t.Fatalf("insert session: %v", err)
	}

	if _, err := s.db.Exec(
		`INSERT INTO prompt_tombstones (sync_id, session_id, project, deleted_at) VALUES (?, ?, '', ?)`,
		"prompt-soft-delete", "legacy-prompt-session", "2024-01-05 12:34:56",
	); err != nil {
		t.Fatalf("insert prompt tombstone: %v", err)
	}

	if err := s.EnrollProject("legacy-proj"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}

	mutations, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 10)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}

	var foundDelete bool
	for _, mutation := range mutations {
		if mutation.Entity == SyncEntityPrompt && mutation.EntityKey == "prompt-soft-delete" {
			if mutation.Op != SyncOpDelete {
				t.Fatalf("expected prompt tombstone backfill op=delete, got %q", mutation.Op)
			}
			if mutation.Project != "legacy-proj" {
				t.Fatalf("expected project derived from session to be legacy-proj, got %q", mutation.Project)
			}
			foundDelete = true
		}
	}
	if !foundDelete {
		t.Fatalf("expected prompt tombstone delete mutation to be backfilled")
	}
}

func TestEnsureRepairsSoftDeletedObservationDeleteMutationsForEnrolledProjects(t *testing.T) {
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "engram.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}

	obsHash := hashNormalized("Historical deleted content")
	_, err = db.Exec(`
		CREATE TABLE sessions (
			id TEXT PRIMARY KEY,
			project TEXT NOT NULL,
			directory TEXT NOT NULL,
			started_at TEXT NOT NULL DEFAULT (datetime('now')),
			ended_at TEXT,
			summary TEXT
		);
		CREATE TABLE observations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			sync_id TEXT,
			session_id TEXT NOT NULL,
			type TEXT NOT NULL,
			title TEXT NOT NULL,
			content TEXT NOT NULL,
			tool_name TEXT,
			project TEXT,
			scope TEXT NOT NULL DEFAULT 'project',
			topic_key TEXT,
			normalized_hash TEXT,
			revision_count INTEGER NOT NULL DEFAULT 1,
			duplicate_count INTEGER NOT NULL DEFAULT 1,
			last_seen_at TEXT,
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			updated_at TEXT NOT NULL DEFAULT (datetime('now')),
			deleted_at TEXT,
			FOREIGN KEY (session_id) REFERENCES sessions(id)
		);
		CREATE TABLE user_prompts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			sync_id TEXT,
			session_id TEXT NOT NULL,
			content TEXT NOT NULL,
			project TEXT,
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			FOREIGN KEY (session_id) REFERENCES sessions(id)
		);
		CREATE TABLE sync_state (
			target_key TEXT PRIMARY KEY,
			lifecycle TEXT NOT NULL DEFAULT 'idle',
			last_enqueued_seq INTEGER NOT NULL DEFAULT 0,
			last_acked_seq INTEGER NOT NULL DEFAULT 0,
			last_pulled_seq INTEGER NOT NULL DEFAULT 0,
			consecutive_failures INTEGER NOT NULL DEFAULT 0,
			backoff_until TEXT,
			lease_owner TEXT,
			lease_until TEXT,
			last_error TEXT,
			updated_at TEXT NOT NULL DEFAULT (datetime('now'))
		);
		CREATE TABLE sync_mutations (
			seq INTEGER PRIMARY KEY AUTOINCREMENT,
			target_key TEXT NOT NULL,
			entity TEXT NOT NULL,
			entity_key TEXT NOT NULL,
			op TEXT NOT NULL,
			payload TEXT NOT NULL,
			source TEXT NOT NULL DEFAULT 'local',
			occurred_at TEXT NOT NULL DEFAULT (datetime('now')),
			acked_at TEXT,
			project TEXT NOT NULL DEFAULT ''
		);
		CREATE TABLE sync_enrolled_projects (
			project TEXT PRIMARY KEY,
			enrolled_at TEXT NOT NULL DEFAULT (datetime('now'))
		);
		INSERT INTO sessions (id, project, directory) VALUES ('legacy-session', 'legacy-proj', '/tmp/legacy');
		INSERT INTO observations (sync_id, session_id, type, title, content, project, scope, normalized_hash, revision_count, duplicate_count, updated_at, deleted_at)
		VALUES ('obs-soft-deleted', 'legacy-session', 'decision', 'Legacy deleted', 'Historical deleted content', 'legacy-proj', 'project', ?, 1, 1, '2024-01-03 12:00:00', '2024-01-03 12:00:00');
		INSERT INTO sync_state (target_key, lifecycle, updated_at) VALUES (?, 'idle', datetime('now'));
		INSERT INTO sync_enrolled_projects (project) VALUES ('legacy-proj');
	`, obsHash, DefaultSyncTargetKey)
	if err != nil {
		_ = db.Close()
		t.Fatalf("seed legacy db: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}

	cfg := mustDefaultConfig(t)
	cfg.DataDir = dataDir

	s, err := New(cfg)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.EnsureEnrolledProjectSyncMutations(context.Background()); err != nil {
		t.Fatalf("ensure enrolled sync journal: %v", err)
	}

	var op string
	if err := s.db.QueryRow(
		`SELECT op FROM sync_mutations WHERE entity = ? AND entity_key = ?`,
		SyncEntityObservation,
		"obs-soft-deleted",
	).Scan(&op); err != nil {
		t.Fatalf("query repaired soft-delete mutation: %v", err)
	}
	if op != SyncOpDelete {
		t.Fatalf("expected repaired observation mutation op=delete, got %q", op)
	}
}

func TestStoreNewDefersRepairUntilSynchronization(t *testing.T) {
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "engram.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}

	obsHash := hashNormalized("Historical content")
	_, err = db.Exec(`
		CREATE TABLE sessions (
			id TEXT PRIMARY KEY,
			project TEXT NOT NULL,
			directory TEXT NOT NULL,
			started_at TEXT NOT NULL DEFAULT (datetime('now')),
			ended_at TEXT,
			summary TEXT
		);
		CREATE TABLE observations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			sync_id TEXT,
			session_id TEXT NOT NULL,
			type TEXT NOT NULL,
			title TEXT NOT NULL,
			content TEXT NOT NULL,
			tool_name TEXT,
			project TEXT,
			scope TEXT NOT NULL DEFAULT 'project',
			topic_key TEXT,
			normalized_hash TEXT,
			revision_count INTEGER NOT NULL DEFAULT 1,
			duplicate_count INTEGER NOT NULL DEFAULT 1,
			last_seen_at TEXT,
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			updated_at TEXT NOT NULL DEFAULT (datetime('now')),
			deleted_at TEXT,
			FOREIGN KEY (session_id) REFERENCES sessions(id)
		);
		CREATE TABLE user_prompts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			sync_id TEXT,
			session_id TEXT NOT NULL,
			content TEXT NOT NULL,
			project TEXT,
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			FOREIGN KEY (session_id) REFERENCES sessions(id)
		);
		CREATE TABLE sync_state (
			target_key TEXT PRIMARY KEY,
			lifecycle TEXT NOT NULL DEFAULT 'idle',
			last_enqueued_seq INTEGER NOT NULL DEFAULT 0,
			last_acked_seq INTEGER NOT NULL DEFAULT 0,
			last_pulled_seq INTEGER NOT NULL DEFAULT 0,
			consecutive_failures INTEGER NOT NULL DEFAULT 0,
			backoff_until TEXT,
			lease_owner TEXT,
			lease_until TEXT,
			last_error TEXT,
			updated_at TEXT NOT NULL DEFAULT (datetime('now'))
		);
		CREATE TABLE sync_mutations (
			seq INTEGER PRIMARY KEY AUTOINCREMENT,
			target_key TEXT NOT NULL,
			entity TEXT NOT NULL,
			entity_key TEXT NOT NULL,
			op TEXT NOT NULL,
			payload TEXT NOT NULL,
			source TEXT NOT NULL DEFAULT 'local',
			occurred_at TEXT NOT NULL DEFAULT (datetime('now')),
			acked_at TEXT,
			project TEXT NOT NULL DEFAULT '',
			FOREIGN KEY (target_key) REFERENCES sync_state(target_key)
		);
		CREATE TABLE sync_enrolled_projects (
			project TEXT PRIMARY KEY,
			enrolled_at TEXT NOT NULL DEFAULT (datetime('now'))
		);
		INSERT INTO sessions (id, project, directory, summary) VALUES ('legacy-session', 'legacy-proj', '/tmp/legacy', 'done');
		INSERT INTO observations (sync_id, session_id, type, title, content, project, scope, normalized_hash, revision_count, duplicate_count, last_seen_at, updated_at)
		VALUES ('obs-legacy', 'legacy-session', 'decision', 'Legacy obs', 'Historical content', 'legacy-proj', 'project', ?, 1, 1, datetime('now'), datetime('now'));
		INSERT INTO user_prompts (sync_id, session_id, content, project) VALUES ('prompt-legacy', 'legacy-session', 'Historical prompt', 'legacy-proj');
		INSERT INTO sync_state (target_key, lifecycle, updated_at) VALUES (?, 'idle', datetime('now'));
		INSERT INTO sync_enrolled_projects (project) VALUES ('legacy-proj');
	`, obsHash, DefaultSyncTargetKey)
	if err != nil {
		_ = db.Close()
		t.Fatalf("seed legacy db: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}

	cfg := mustDefaultConfig(t)
	cfg.DataDir = dataDir

	s, err := New(cfg)
	if err != nil {
		t.Fatalf("new store after enrolled legacy state: %v", err)
	}

	var beforeRepair int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sync_mutations`).Scan(&beforeRepair); err != nil {
		_ = s.Close()
		t.Fatalf("count mutations before deferred repair: %v", err)
	}
	if beforeRepair != 0 {
		_ = s.Close()
		t.Fatalf("Store.New repaired journal before synchronization: got %d mutations", beforeRepair)
	}
	if err := s.EnsureEnrolledProjectSyncMutations(context.Background()); err != nil {
		_ = s.Close()
		t.Fatalf("ensure enrolled sync journal: %v", err)
	}

	mutations, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 10)
	if err != nil {
		_ = s.Close()
		t.Fatalf("list pending after repair: %v", err)
	}
	if len(mutations) != 3 {
		_ = s.Close()
		t.Fatalf("expected 3 repaired mutations, got %d", len(mutations))
	}

	state, err := s.GetSyncState(DefaultSyncTargetKey)
	if err != nil {
		_ = s.Close()
		t.Fatalf("get sync state after repair: %v", err)
	}
	if state.LastEnqueuedSeq != 3 {
		_ = s.Close()
		t.Fatalf("expected last_enqueued_seq 3 after deferred repair, got %d", state.LastEnqueuedSeq)
	}

	if err := s.Close(); err != nil {
		t.Fatalf("close repaired store: %v", err)
	}

	s, err = New(cfg)
	if err != nil {
		t.Fatalf("reopen repaired store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sync_mutations`).Scan(&count); err != nil {
		t.Fatalf("count repaired mutations after reopen: %v", err)
	}
	if count != 3 {
		t.Fatalf("expected repair to stay idempotent across reopen, got %d sync mutations", count)
	}
}

func TestEnrollProjectEmptyNameReturnsError(t *testing.T) {
	s := newTestStore(t)

	if err := s.EnrollProject(""); err == nil {
		t.Fatal("expected error when enrolling empty project name")
	}
}

func TestUnenrollProjectBasic(t *testing.T) {
	s := newTestStore(t)

	if err := s.EnrollProject("engram"); err != nil {
		t.Fatalf("enroll: %v", err)
	}

	// Unenroll.
	if err := s.UnenrollProject("engram"); err != nil {
		t.Fatalf("unenroll: %v", err)
	}

	// Should be gone.
	enrolled, err := s.IsProjectEnrolled("engram")
	if err != nil {
		t.Fatalf("is enrolled after unenroll: %v", err)
	}
	if enrolled {
		t.Fatal("expected project to be unenrolled")
	}

	projects, err := s.ListEnrolledProjects()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(projects) != 0 {
		t.Fatalf("expected 0 enrolled projects after unenroll, got %d", len(projects))
	}
}

func TestUnenrollProjectIdempotent(t *testing.T) {
	s := newTestStore(t)

	// Unenroll a project that was never enrolled — should not error.
	if err := s.UnenrollProject("nonexistent"); err != nil {
		t.Fatalf("unenroll non-enrolled project should be idempotent: %v", err)
	}
}

func TestUnenrollProjectEmptyNameReturnsError(t *testing.T) {
	s := newTestStore(t)

	if err := s.UnenrollProject(""); err == nil {
		t.Fatal("expected error when unenrolling empty project name")
	}
}

func TestIsProjectEnrolledReturnsFalseForUnknown(t *testing.T) {
	s := newTestStore(t)

	enrolled, err := s.IsProjectEnrolled("unknown-project")
	if err != nil {
		t.Fatalf("is enrolled: %v", err)
	}
	if enrolled {
		t.Fatal("expected false for unknown project")
	}
}

func TestListEnrolledProjectsEmpty(t *testing.T) {
	s := newTestStore(t)

	projects, err := s.ListEnrolledProjects()
	if err != nil {
		t.Fatalf("list enrolled projects: %v", err)
	}
	if projects != nil {
		t.Fatalf("expected nil for empty list, got %v", projects)
	}
}

func TestListEnrolledProjectsAlphabeticalOrder(t *testing.T) {
	s := newTestStore(t)

	// Enroll in non-alphabetical order.
	for _, p := range []string{"zebra", "alpha", "mango"} {
		if err := s.EnrollProject(p); err != nil {
			t.Fatalf("enroll %q: %v", p, err)
		}
	}

	projects, err := s.ListEnrolledProjects()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(projects) != 3 {
		t.Fatalf("expected 3 projects, got %d", len(projects))
	}
	expected := []string{"alpha", "mango", "zebra"}
	for i, ep := range projects {
		if ep.Project != expected[i] {
			t.Fatalf("position %d: expected %q, got %q", i, expected[i], ep.Project)
		}
	}
}

func TestSyncMutationProjectColumnExists(t *testing.T) {
	s := newTestStore(t)

	// Verify the project column exists on sync_mutations by inserting a row.
	_, err := s.db.Exec(
		`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		DefaultSyncTargetKey, "session", "test-key", SyncOpUpsert, `{"project":"myproj"}`, SyncSourceLocal, "myproj",
	)
	if err != nil {
		t.Fatalf("insert sync_mutation with project: %v", err)
	}

	// Read it back and verify project is populated.
	var project string
	if err := s.db.QueryRow(`SELECT project FROM sync_mutations WHERE entity_key = ?`, "test-key").Scan(&project); err != nil {
		t.Fatalf("scan project: %v", err)
	}
	if project != "myproj" {
		t.Fatalf("expected project 'myproj', got %q", project)
	}
}

func TestSyncMutationProjectBackfill(t *testing.T) {
	s := newTestStore(t)

	// Insert a mutation that simulates a pre-migration row (project is empty, but payload has it).
	// The backfill runs during schema init, so we test it by inserting directly then re-running.
	// Since the store already ran migrations, let's verify backfill logic by inserting a new row
	// with empty project and manually running the backfill.
	_, err := s.db.Exec(
		`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project)
		 VALUES (?, ?, ?, ?, ?, ?, '')`,
		DefaultSyncTargetKey, "observation", "backfill-key", SyncOpUpsert, `{"project":"backfilled"}`, SyncSourceLocal,
	)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Run the backfill manually.
	_, err = s.db.Exec(`
		UPDATE sync_mutations
		SET project = COALESCE(json_extract(payload, '$.project'), '')
		WHERE project = '' AND payload != ''
	`)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}

	var project string
	if err := s.db.QueryRow(`SELECT project FROM sync_mutations WHERE entity_key = ?`, "backfill-key").Scan(&project); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if project != "backfilled" {
		t.Fatalf("expected backfilled project 'backfilled', got %q", project)
	}
}

func TestSyncMutationProjectBackfillFromSessionID(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.db.Exec(`INSERT INTO sessions (id, project, directory) VALUES (?, ?, ?)`, "sess-backfill", "derived-proj", "/tmp/derived"); err != nil {
		t.Fatalf("insert session: %v", err)
	}

	_, err := s.db.Exec(
		`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project)
		 VALUES (?, ?, ?, ?, ?, ?, '')`,
		DefaultSyncTargetKey, SyncEntityObservation, "obs-backfill-session", SyncOpDelete, `{"sync_id":"obs-backfill-session","session_id":"sess-backfill","deleted":true,"hard_delete":true}`, SyncSourceLocal,
	)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Same SQL used by migrate() for legacy rows that have empty project but include session metadata.
	_, err = s.db.Exec(`
		UPDATE sync_mutations
		SET project = COALESCE((
			SELECT sessions.project
			FROM sessions
			WHERE sessions.id = json_extract(sync_mutations.payload, '$.session_id')
		), '')
		WHERE project = ''
		  AND payload != ''
		  AND ifnull(json_extract(payload, '$.session_id'), '') != ''
	`)
	if err != nil {
		t.Fatalf("backfill from session_id: %v", err)
	}

	var project string
	if err := s.db.QueryRow(`SELECT project FROM sync_mutations WHERE entity_key = ?`, "obs-backfill-session").Scan(&project); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if project != "derived-proj" {
		t.Fatalf("expected session-derived project 'derived-proj', got %q", project)
	}
}

func TestListPendingSyncMutationsIncludesProject(t *testing.T) {
	s := newTestStore(t)

	// Enroll the project so mutations are visible in ListPendingSyncMutations.
	if err := s.EnrollProject("my-project"); err != nil {
		t.Fatalf("enroll: %v", err)
	}

	if err := s.CreateSession("proj-session", "my-project", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	_, err := s.AddObservation(AddObservationParams{
		SessionID: "proj-session",
		Type:      "decision",
		Title:     "Test obs",
		Content:   "Content",
		Project:   "my-project",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}

	mutations, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 10)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}

	// There should be mutations (session create + observation create at minimum).
	if len(mutations) == 0 {
		t.Fatal("expected at least one pending mutation")
	}

	// Phase 3: Verify the Project field is populated at enqueue time.
	foundProject := false
	for _, m := range mutations {
		if m.Project == "my-project" {
			foundProject = true
			break
		}
	}
	if !foundProject {
		t.Fatal("expected at least one mutation with project='my-project'")
	}
}

func TestCountPendingNonEnrolledSyncMutations(t *testing.T) {
	s := newTestStore(t)

	if err := s.EnrollProject("enrolled-project"); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	for i, project := range []string{"alpha", "alpha", "beta", "enrolled-project"} {
		if _, err := s.db.Exec(
			`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			DefaultSyncTargetKey,
			SyncEntityObservation,
			fmt.Sprintf("key-%s-%d", project, i),
			SyncOpUpsert,
			`{}`,
			SyncSourceLocal,
			project,
		); err != nil {
			t.Fatalf("insert mutation for %s: %v", project, err)
		}
	}
	if _, err := s.db.Exec(
		`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project) VALUES (?, ?, ?, ?, ?, ?, '')`,
		DefaultSyncTargetKey, SyncEntityObservation, "global-key", SyncOpUpsert, `{}`, SyncSourceLocal,
	); err != nil {
		t.Fatalf("insert global mutation: %v", err)
	}
	if _, err := s.db.Exec(
		`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project, acked_at) VALUES (?, ?, ?, ?, ?, ?, ?, datetime('now'))`,
		DefaultSyncTargetKey, SyncEntityObservation, "acked-alpha", SyncOpUpsert, `{}`, SyncSourceLocal, "alpha",
	); err != nil {
		t.Fatalf("insert acked mutation: %v", err)
	}
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO sync_state (target_key, lifecycle, updated_at) VALUES (?, 'idle', datetime('now'))`, "cloud:other"); err != nil {
		t.Fatalf("insert other sync state: %v", err)
	}
	if _, err := s.db.Exec(
		`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"cloud:other", SyncEntityObservation, "other-target-alpha", SyncOpUpsert, `{}`, SyncSourceLocal, "alpha",
	); err != nil {
		t.Fatalf("insert other target mutation: %v", err)
	}

	counts, err := s.CountPendingNonEnrolledSyncMutations(DefaultSyncTargetKey)
	if err != nil {
		t.Fatalf("count pending non-enrolled: %v", err)
	}
	want := []PendingSyncMutationProjectCount{{Project: "alpha", Count: 2}, {Project: "beta", Count: 1}}
	if len(counts) != len(want) {
		t.Fatalf("expected %d counts, got %#v", len(want), counts)
	}
	for i := range want {
		if counts[i] != want[i] {
			t.Fatalf("count[%d]: expected %#v, got %#v", i, want[i], counts[i])
		}
	}
}

// ─── Phase 3: extractProjectFromPayload ──────────────────────────────────────

func TestExtractProjectFromSessionPayload(t *testing.T) {
	p := syncSessionPayload{ID: "s1", Project: "acme"}
	got := extractProjectFromPayload(p)
	if got != "acme" {
		t.Fatalf("expected 'acme', got %q", got)
	}
}

func TestExtractProjectFromObservationPayload(t *testing.T) {
	proj := "obs-project"
	p := syncObservationPayload{SyncID: "obs-1", Project: &proj}
	got := extractProjectFromPayload(p)
	if got != "obs-project" {
		t.Fatalf("expected 'obs-project', got %q", got)
	}
}

func TestExtractProjectFromObservationPayloadNil(t *testing.T) {
	p := syncObservationPayload{SyncID: "obs-1", Project: nil}
	got := extractProjectFromPayload(p)
	if got != "" {
		t.Fatalf("expected empty string, got %q", got)
	}
}

func TestExtractProjectFromPromptPayload(t *testing.T) {
	proj := "prompt-project"
	p := syncPromptPayload{SyncID: "p1", Project: &proj}
	got := extractProjectFromPayload(p)
	if got != "prompt-project" {
		t.Fatalf("expected 'prompt-project', got %q", got)
	}
}

func TestExtractProjectFromPromptPayloadNil(t *testing.T) {
	p := syncPromptPayload{SyncID: "p1", Project: nil}
	got := extractProjectFromPayload(p)
	if got != "" {
		t.Fatalf("expected empty string, got %q", got)
	}
}

func TestExtractProjectFromUnknownPayloadFallback(t *testing.T) {
	// Unknown struct with a project field — uses JSON fallback.
	p := struct {
		Project string `json:"project"`
		Other   string `json:"other"`
	}{Project: "fallback-proj", Other: "x"}
	got := extractProjectFromPayload(p)
	if got != "fallback-proj" {
		t.Fatalf("expected 'fallback-proj', got %q", got)
	}
}

func TestExtractProjectFromPayloadWithoutProjectField(t *testing.T) {
	// Unknown struct without a project field — returns empty.
	p := struct {
		Name string `json:"name"`
	}{Name: "test"}
	got := extractProjectFromPayload(p)
	if got != "" {
		t.Fatalf("expected empty string, got %q", got)
	}
}

// ─── Phase 3: enqueueSyncMutationTx populates project column ────────────────

func TestEnqueueSyncMutationPopulatesProjectFromSessionPayload(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("enq-session", "enqueued-project", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// CreateSession enqueues a sync mutation internally. Check the project column.
	var project string
	err := s.db.QueryRow(
		`SELECT project FROM sync_mutations WHERE entity = ? AND entity_key = ?`,
		SyncEntitySession, "enq-session",
	).Scan(&project)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if project != "enqueued-project" {
		t.Fatalf("expected project='enqueued-project', got %q", project)
	}
}

func TestEnqueueSyncMutationPopulatesProjectFromObservationPayload(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("obs-enq", "obs-proj", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	_, err := s.AddObservation(AddObservationParams{
		SessionID: "obs-enq",
		Type:      "decision",
		Title:     "Test",
		Content:   "Content",
		Project:   "obs-proj",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}

	// Check the observation mutation's project column.
	var project string
	err = s.db.QueryRow(
		`SELECT project FROM sync_mutations WHERE entity = ? ORDER BY seq DESC LIMIT 1`,
		SyncEntityObservation,
	).Scan(&project)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if project != "obs-proj" {
		t.Fatalf("expected project='obs-proj', got %q", project)
	}
}

func TestEnqueueSyncMutationPopulatesProjectFromPromptPayload(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("prompt-enq", "prompt-proj", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	_, err := s.AddPrompt(AddPromptParams{
		SessionID: "prompt-enq",
		Content:   "What did we do?",
		Project:   "prompt-proj",
	})
	if err != nil {
		t.Fatalf("add prompt: %v", err)
	}

	var project string
	err = s.db.QueryRow(
		`SELECT project FROM sync_mutations WHERE entity = ? ORDER BY seq DESC LIMIT 1`,
		SyncEntityPrompt,
	).Scan(&project)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if project != "prompt-proj" {
		t.Fatalf("expected project='prompt-proj', got %q", project)
	}
}

func TestEnqueueSyncMutationUsesProjectScopedTargetKey(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("target-session", "target-proj", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	state, err := s.GetSyncState(syncTargetKeyForProject("target-proj"))
	if err != nil {
		t.Fatalf("get project-scoped sync state: %v", err)
	}
	if state.Lifecycle != SyncLifecyclePending {
		t.Fatalf("expected project-scoped lifecycle pending, got %q", state.Lifecycle)
	}
	if state.LastEnqueuedSeq == 0 {
		t.Fatal("expected project-scoped sync state to track last_enqueued_seq")
	}
}

// ─── Phase 4: ListPendingSyncMutations enrollment filtering ──────────────────

func TestListPendingFiltersNonEnrolledProjects(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s-enrolled", "enrolled-proj", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := s.CreateSession("s-not-enrolled", "other-proj", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Enroll only "enrolled-proj".
	if err := s.EnrollProject("enrolled-proj"); err != nil {
		t.Fatalf("enroll: %v", err)
	}

	mutations, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 100)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}

	// Only enrolled-proj mutations should appear.
	for _, m := range mutations {
		if m.Project == "other-proj" {
			t.Fatalf("non-enrolled project 'other-proj' should not appear in pending mutations")
		}
	}

	foundEnrolled := false
	for _, m := range mutations {
		if m.Project == "enrolled-proj" {
			foundEnrolled = true
			break
		}
	}
	if !foundEnrolled {
		t.Fatal("expected enrolled-proj mutations to appear")
	}
}

func TestListPendingReturnsNoMutationsWhenNoneEnrolled(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s-no-enroll", "some-proj", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	mutations, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 100)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}

	// No projects enrolled → no mutations (all have project != '').
	if len(mutations) != 0 {
		t.Fatalf("expected 0 mutations when no projects enrolled, got %d", len(mutations))
	}
}

// ─── Phase 4: SkipAckNonEnrolledMutations ────────────────────────────────────

func TestSkipAckNonEnrolledMutationsBasic(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("skip-session", "skip-proj", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Do NOT enroll "skip-proj" → mutations should be skip-acked.
	skipped, err := s.SkipAckNonEnrolledMutations(DefaultSyncTargetKey)
	if err != nil {
		t.Fatalf("skip-ack: %v", err)
	}
	if skipped == 0 {
		t.Fatal("expected at least one mutation to be skip-acked")
	}

	// After skip-ack, there should be no pending mutations left.
	mutations, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 100)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(mutations) != 0 {
		t.Fatalf("expected 0 pending mutations after skip-ack, got %d", len(mutations))
	}
}

func TestSkipAckPreservesEnrolledProjectMutations(t *testing.T) {
	s := newTestStore(t)

	if err := s.EnrollProject("enrolled"); err != nil {
		t.Fatalf("enroll: %v", err)
	}

	if err := s.CreateSession("s-enrolled", "enrolled", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := s.CreateSession("s-not-enrolled", "not-enrolled", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Count total pending before skip-ack.
	var totalBefore int
	s.db.QueryRow(`SELECT COUNT(*) FROM sync_mutations WHERE acked_at IS NULL`).Scan(&totalBefore)

	skipped, err := s.SkipAckNonEnrolledMutations(DefaultSyncTargetKey)
	if err != nil {
		t.Fatalf("skip-ack: %v", err)
	}
	if skipped == 0 {
		t.Fatal("expected at least one mutation to be skip-acked for 'not-enrolled'")
	}

	// Remaining pending should be only "enrolled" mutations.
	mutations, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 100)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	for _, m := range mutations {
		if m.Project == "not-enrolled" {
			t.Fatal("skip-acked mutation still appears as pending")
		}
	}
	if len(mutations) == 0 {
		t.Fatal("expected enrolled-project mutations to remain")
	}
}

// ─── Phase 5: Identity-less writes are rejected ──────────────────────────────

func TestIdentitylessSessionCreatesNoMutation(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("global-session", "", "/tmp"); !errors.Is(err, ErrProjectRequired) {
		t.Fatalf("identity-less create error = %v, want ErrProjectRequired", err)
	}
	mutations, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 100)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(mutations) != 0 {
		t.Fatalf("identity-less create left pending mutations: %+v", mutations)
	}
}

func TestSkipAckAfterIdentitylessSessionRejectionHasNoState(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("global-session-2", "", "/tmp"); !errors.Is(err, ErrProjectRequired) {
		t.Fatalf("identity-less create error = %v, want ErrProjectRequired", err)
	}
	skipped, err := s.SkipAckNonEnrolledMutations(DefaultSyncTargetKey)
	if err != nil {
		t.Fatalf("skip-ack: %v", err)
	}
	if skipped != 0 {
		t.Fatalf("expected 0 mutations to be skip-acked after rejection, got %d", skipped)
	}
	mutations, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 100)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(mutations) != 0 {
		t.Fatalf("identity-less create left pending mutations: %+v", mutations)
	}
}

func TestMixedEnrolledAndRejectedIdentitylessSessions(t *testing.T) {
	s := newTestStore(t)

	if err := s.EnrollProject("enrolled-mix"); err != nil {
		t.Fatalf("enroll: %v", err)
	}

	// Create sessions with enrolled, rejected, and unenrolled project states.
	if err := s.CreateSession("mix-enrolled", "enrolled-mix", "/tmp"); err != nil {
		t.Fatalf("create enrolled session: %v", err)
	}
	if err := s.CreateSession("mix-global", "", "/tmp"); !errors.Is(err, ErrProjectRequired) {
		t.Fatalf("create identity-less session: %v, want ErrProjectRequired", err)
	}
	if err := s.CreateSession("mix-unenrolled", "unenrolled-mix", "/tmp"); err != nil {
		t.Fatalf("create unenrolled session: %v", err)
	}

	mutations, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 100)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}

	// Only enrolled identity-bearing mutations may remain pending.
	var hasEnrolled bool
	for _, m := range mutations {
		if m.Project == "unenrolled-mix" {
			t.Fatal("unenrolled project mutations should not appear")
		}
		if m.Project == "enrolled-mix" {
			hasEnrolled = true
		}
		if m.Project == "" {
			t.Fatal("identity-less mutation should never be enqueued")
		}
	}
	if !hasEnrolled {
		t.Fatal("expected enrolled-mix mutations to appear")
	}
}

// ─── MigrateProject ─────────────────────────────────────────────────────────

func TestMigrateProject(t *testing.T) {
	s := newTestStore(t)
	old, new_ := "old-name", "new-name"

	// Seed data under old project name
	s.CreateSession("s1", old, "/tmp/old")
	s.AddObservation(AddObservationParams{
		SessionID: "s1", Type: "decision", Title: "test obs",
		Content: "some content", Project: old, Scope: "project",
	})
	s.AddPrompt(AddPromptParams{SessionID: "s1", Content: "test prompt", Project: old})

	// Run migration
	result, err := s.MigrateProject(old, new_)
	if err != nil {
		t.Fatalf("MigrateProject: %v", err)
	}
	if !result.Migrated {
		t.Fatal("expected migration to happen")
	}
	if result.ObservationsUpdated != 1 {
		t.Fatalf("expected 1 observation migrated, got %d", result.ObservationsUpdated)
	}
	if result.SessionsUpdated != 1 {
		t.Fatalf("expected 1 session migrated, got %d", result.SessionsUpdated)
	}
	if result.PromptsUpdated != 1 {
		t.Fatalf("expected 1 prompt migrated, got %d", result.PromptsUpdated)
	}

	// Verify old project has no records
	obs, _ := s.RecentObservations(old, "", 10)
	if len(obs) != 0 {
		t.Fatalf("expected 0 observations under old name, got %d", len(obs))
	}

	// Verify new project has the records
	obs, _ = s.RecentObservations(new_, "", 10)
	if len(obs) != 1 {
		t.Fatalf("expected 1 observation under new name, got %d", len(obs))
	}

	// Verify FTS search finds it under new project
	results, _ := s.Search("test obs", SearchOptions{Project: new_, Limit: 10})
	if len(results) != 1 {
		t.Fatalf("expected FTS to find 1 result under new project, got %d", len(results))
	}
}

func TestMigrateProjectNoOp(t *testing.T) {
	s := newTestStore(t)

	// No records under "nonexistent" — should be a no-op
	result, err := s.MigrateProject("nonexistent", "anything")
	if err != nil {
		t.Fatalf("MigrateProject: %v", err)
	}
	if result.Migrated {
		t.Fatal("expected no migration for nonexistent project")
	}
}

func TestMigrateProjectIdempotent(t *testing.T) {
	s := newTestStore(t)
	old, new_ := "old-proj", "new-proj"

	s.CreateSession("s1", old, "/tmp")
	s.AddObservation(AddObservationParams{
		SessionID: "s1", Type: "decision", Title: "test",
		Content: "content", Project: old, Scope: "project",
	})

	// First migration
	r1, err := s.MigrateProject(old, new_)
	if err != nil {
		t.Fatalf("first MigrateProject: %v", err)
	}
	if !r1.Migrated {
		t.Fatal("first migration should migrate")
	}

	// Second migration — no records under old name anymore
	r2, err := s.MigrateProject(old, new_)
	if err != nil {
		t.Fatalf("second MigrateProject: %v", err)
	}
	if r2.Migrated {
		t.Fatal("second migration should be a no-op")
	}
}

// ─── Phase 2: project-name-drift — NormalizeProject, ListProjectNames,
//              ListProjectsWithStats, MergeProjects tests ─────────────────────

func TestNormalizeProjectFunction(t *testing.T) {
	tests := []struct {
		input       string
		wantName    string
		wantWarning bool
	}{
		{"engram", "engram", false},
		{"Engram", "engram", true},
		{"ENGRAM", "engram", true},
		{"  engram  ", "engram", true},
		{"Engram-Memory", "engram-memory", true},
		{"engram--memory", "engram-memory", true},
		{"engram__memory", "engram_memory", true},
		{"", "", false},
		{"already-lower", "already-lower", false},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got, warning := NormalizeProject(tc.input)
			if got != tc.wantName {
				t.Errorf("NormalizeProject(%q) name = %q, want %q", tc.input, got, tc.wantName)
			}
			if tc.wantWarning && warning == "" {
				t.Errorf("NormalizeProject(%q) expected a warning, got empty string", tc.input)
			}
			if !tc.wantWarning && warning != "" {
				t.Errorf("NormalizeProject(%q) expected no warning, got %q", tc.input, warning)
			}
		})
	}
}

func TestAddObservationNormalizesProject(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s1", "engram", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Save with mixed-case project name
	id, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "decision",
		Title:     "Normalize test",
		Content:   "This should be stored under lowercase project",
		Project:   "Engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}

	obs, err := s.GetObservation(id)
	if err != nil {
		t.Fatalf("GetObservation: %v", err)
	}

	// Stored project should be normalized to lowercase
	if obs.Project == nil || *obs.Project != "engram" {
		got := "<nil>"
		if obs.Project != nil {
			got = *obs.Project
		}
		t.Errorf("stored project = %q, want \"engram\"", got)
	}
}

func TestSearchNormalizesProjectFilter(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s1", "engram", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Store observation under already-lowercase project
	_, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "decision",
		Title:     "Search normalize test",
		Content:   "content for project filter normalization",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}

	// Search with UPPERCASE project filter — should still find the record
	results, err := s.Search("normalize test", SearchOptions{
		Project: "Engram", // intentionally mixed-case
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if len(results) == 0 {
		t.Fatalf("expected ≥1 result when searching with normalized project filter, got 0")
	}
}

func TestRecentObservationsNormalizesProjectFilter(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s1", "engram", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	_, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "decision",
		Title:     "Recent obs test",
		Content:   "some content",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}

	// Query with uppercase project name
	obs, err := s.RecentObservations("ENGRAM", "", 10)
	if err != nil {
		t.Fatalf("RecentObservations: %v", err)
	}
	if len(obs) == 0 {
		t.Fatalf("expected ≥1 result with normalized project filter, got 0")
	}
}

func TestCreateSessionNormalizesProject(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s-norm", "MyProject", "/tmp"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	sess, err := s.GetSession("s-norm")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if sess.Project != "myproject" {
		t.Errorf("expected project=myproject (normalized), got %q", sess.Project)
	}
}

func TestListProjectNames(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s1", "alpha", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := s.CreateSession("s2", "beta", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	for _, proj := range []string{"alpha", "alpha", "beta", "gamma"} {
		_, err := s.AddObservation(AddObservationParams{
			SessionID: "s1",
			Type:      "decision",
			Title:     "test " + proj,
			Content:   "content for " + proj,
			Project:   proj,
			Scope:     "project",
		})
		if err != nil {
			t.Fatalf("AddObservation: %v", err)
		}
	}

	names, err := s.ListProjectNames()
	if err != nil {
		t.Fatalf("ListProjectNames: %v", err)
	}

	// Should return distinct names: alpha, beta, gamma
	want := map[string]bool{"alpha": true, "beta": true, "gamma": true}
	for _, n := range names {
		if !want[n] {
			t.Errorf("unexpected project name %q in results", n)
		}
		delete(want, n)
	}
	if len(want) > 0 {
		t.Errorf("missing project names: %v", want)
	}
}

func TestListProjectsWithStats(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s1", "proj-a", "/work/a"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := s.CreateSession("s2", "proj-b", "/work/b"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Add 3 observations to proj-a
	for i := 0; i < 3; i++ {
		_, err := s.AddObservation(AddObservationParams{
			SessionID: "s1",
			Type:      "decision",
			Title:     "obs a",
			Content:   strings.Repeat("x", i+1), // unique content per obs
			Project:   "proj-a",
			Scope:     "project",
		})
		if err != nil {
			t.Fatalf("AddObservation proj-a: %v", err)
		}
	}

	// Add 1 observation to proj-b
	_, err := s.AddObservation(AddObservationParams{
		SessionID: "s2",
		Type:      "decision",
		Title:     "obs b",
		Content:   "content for proj-b",
		Project:   "proj-b",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("AddObservation proj-b: %v", err)
	}

	stats, err := s.ListProjectsWithStats()
	if err != nil {
		t.Fatalf("ListProjectsWithStats: %v", err)
	}

	if len(stats) < 2 {
		t.Fatalf("expected ≥2 project stats, got %d", len(stats))
	}

	// Find proj-a and proj-b in results
	statsMap := make(map[string]ProjectStats)
	for _, ps := range stats {
		statsMap[ps.Name] = ps
	}

	if a, ok := statsMap["proj-a"]; !ok {
		t.Error("proj-a not in ListProjectsWithStats results")
	} else {
		if a.ObservationCount != 3 {
			t.Errorf("proj-a: expected 3 observations, got %d", a.ObservationCount)
		}
		if a.SessionCount != 1 {
			t.Errorf("proj-a: expected 1 session, got %d", a.SessionCount)
		}
	}

	if b, ok := statsMap["proj-b"]; !ok {
		t.Error("proj-b not in ListProjectsWithStats results")
	} else {
		if b.ObservationCount != 1 {
			t.Errorf("proj-b: expected 1 observation, got %d", b.ObservationCount)
		}
	}

	// Results should be sorted by observation count descending
	if stats[0].Name != "proj-a" {
		t.Errorf("expected proj-a first (most observations), got %q", stats[0].Name)
	}
}

func TestMergeProjects(t *testing.T) {
	s := newTestStore(t)

	// Set up normalization-equivalent source projects.
	sources := []string{"engram", "Engram"}
	canonical := "engram"

	if err := s.CreateSession("s1", "engram", "/work"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Add observations to each source
	for _, src := range []string{"engram"} {
		for i := 0; i < 2; i++ {
			_, err := s.AddObservation(AddObservationParams{
				SessionID: "s1",
				Type:      "decision",
				Title:     "obs from " + src,
				Content:   strings.Repeat(src, i+1),
				Project:   src,
				Scope:     "project",
			})
			if err != nil {
				t.Fatalf("AddObservation %s: %v", src, err)
			}
		}
	}

	result, err := s.MergeProjects(sources, canonical)
	if err != nil {
		t.Fatalf("MergeProjects: %v", err)
	}

	if result.Canonical != "engram" {
		t.Errorf("canonical = %q, want \"engram\"", result.Canonical)
	}

	// Equivalent sources are allowed, while the exact canonical source is skipped.
	for _, merged := range result.SourcesMerged {
		if merged == "engram" {
			t.Error("canonical 'engram' should not appear in SourcesMerged")
		}
	}

	// All records remain under "engram".
	obs, err := s.RecentObservations("engram", "", 20)
	if err != nil {
		t.Fatalf("RecentObservations: %v", err)
	}
	if len(obs) != 2 {
		t.Errorf("expected 2 observations under 'engram', got %d", len(obs))
	}

	// An unrelated project name is untouched.
	obsMerged, err := s.RecentObservations("engram-memory", "", 10)
	if err != nil {
		t.Fatalf("RecentObservations engram-memory: %v", err)
	}
	if len(obsMerged) != 0 {
		t.Errorf("expected 0 observations under 'engram-memory' after merge, got %d", len(obsMerged))
	}
}

func TestMergeProjectsIdempotent(t *testing.T) {
	s := newTestStore(t)

	// Merge an equivalent nonexistent source — should not error.
	result, err := s.MergeProjects([]string{"Engram"}, "engram")
	if err != nil {
		t.Fatalf("MergeProjects with nonexistent source: %v", err)
	}
	if result.ObservationsUpdated != 0 {
		t.Errorf("expected 0 observations updated for nonexistent source, got %d", result.ObservationsUpdated)
	}
}

func TestMergeProjectsCanonicalInSources(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s1", "engram", "/work"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Put some obs under "engram"
	_, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "decision",
		Title:     "existing",
		Content:   "existing observation",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}

	// Sources include the canonical itself — should be silently skipped
	result, err := s.MergeProjects([]string{"engram", "Engram"}, "engram")
	if err != nil {
		t.Fatalf("MergeProjects: %v", err)
	}

	// Nothing should have been changed (engram and Engram both normalize to "engram" = canonical)
	if result.ObservationsUpdated != 0 {
		t.Errorf("expected 0 observations updated when sources equal canonical, got %d", result.ObservationsUpdated)
	}
	if len(result.SourcesMerged) != 0 {
		t.Errorf("expected empty SourcesMerged when all sources equal canonical, got %v", result.SourcesMerged)
	}
}

func TestMergeProjectsReportsTrimmedLegacySourceWithoutLosingRows(t *testing.T) {
	s := newTestStore(t)

	if _, err := s.db.Exec(`INSERT INTO sessions (id, project, directory) VALUES (?, ?, ?)`, "legacy-session", "Engram Memory", "/work/engram"); err != nil {
		t.Fatalf("seed legacy session: %v", err)
	}
	if _, err := s.db.Exec(`INSERT INTO observations (sync_id, session_id, type, title, content, project, scope, normalized_hash) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, "legacy-obs", "legacy-session", "decision", "legacy", "legacy content", "Engram Memory", "project", "legacy-hash"); err != nil {
		t.Fatalf("seed legacy observation: %v", err)
	}
	if _, err := s.db.Exec(`INSERT INTO user_prompts (sync_id, session_id, content, project) VALUES (?, ?, ?, ?)`, "legacy-prompt", "legacy-session", "legacy prompt", "Engram Memory"); err != nil {
		t.Fatalf("seed legacy prompt: %v", err)
	}

	result, err := s.MergeProjects([]string{" Engram Memory "}, "engram memory")
	if err != nil {
		t.Fatalf("MergeProjects: %v", err)
	}
	if result.ObservationsUpdated != 1 || result.SessionsUpdated != 1 || result.PromptsUpdated != 1 {
		t.Fatalf("unexpected merge result: %+v", result)
	}
	if len(result.SourcesMerged) != 1 || result.SourcesMerged[0] != "Engram Memory" {
		t.Fatalf("SourcesMerged = %v, want [Engram Memory]", result.SourcesMerged)
	}

	for _, table := range []string{"sessions", "observations", "user_prompts"} {
		var count int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE project = ?`, "Engram Memory").Scan(&count); err != nil {
			t.Fatalf("count legacy rows in %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("%s still has %d legacy project rows", table, count)
		}
	}
}

func TestMergeProjectsConsolidatesDeterministicAliasSpellings(t *testing.T) {
	s := newTestStore(t)

	for i, project := range []string{"Engram Memory", "engram memory"} {
		sessionID := fmt.Sprintf("alias-session-%d", i)
		if _, err := s.db.Exec(`INSERT INTO sessions (id, project, directory) VALUES (?, ?, ?)`, sessionID, project, "/work/engram"); err != nil {
			t.Fatalf("seed alias session %q: %v", project, err)
		}
		if _, err := s.db.Exec(`INSERT INTO observations (sync_id, session_id, type, title, content, project, scope, normalized_hash) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, fmt.Sprintf("alias-obs-%d", i), sessionID, "decision", "alias", "alias content", project, "project", fmt.Sprintf("alias-hash-%d", i)); err != nil {
			t.Fatalf("seed alias observation %q: %v", project, err)
		}
		if _, err := s.db.Exec(`INSERT INTO user_prompts (sync_id, session_id, content, project) VALUES (?, ?, ?, ?)`, fmt.Sprintf("alias-prompt-%d", i), sessionID, "alias prompt", project); err != nil {
			t.Fatalf("seed alias prompt %q: %v", project, err)
		}
	}

	result, err := s.MergeProjects([]string{"Engram Memory"}, "engram memory")
	if err != nil {
		t.Fatalf("MergeProjects: %v", err)
	}
	if result.ObservationsUpdated != 1 || result.SessionsUpdated != 1 || result.PromptsUpdated != 1 {
		t.Fatalf("unexpected merge result: %+v", result)
	}
}

func TestMergeProjectsAliasVariantsDoNotRewriteCanonicalProject(t *testing.T) {
	s := newTestStore(t)

	if _, err := s.db.Exec(`INSERT INTO sessions (id, project, directory) VALUES (?, ?, ?)`, "canonical-session", "engram memory", "/work/engram"); err != nil {
		t.Fatalf("seed canonical session: %v", err)
	}
	if _, err := s.db.Exec(`INSERT INTO sessions (id, project, directory) VALUES (?, ?, ?)`, "source-session", "Engram Memory", "/work/engram"); err != nil {
		t.Fatalf("seed source session: %v", err)
	}

	result, err := s.MergeProjects([]string{"Engram Memory"}, "engram memory")
	if err != nil {
		t.Fatalf("MergeProjects: %v", err)
	}
	if result.SessionsUpdated != 1 {
		t.Fatalf("SessionsUpdated = %d, want 1", result.SessionsUpdated)
	}
	var canonicalRows int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE project = ?`, "engram memory").Scan(&canonicalRows); err != nil {
		t.Fatalf("count canonical rows: %v", err)
	}
	if canonicalRows != 2 {
		t.Fatalf("canonical rows = %d, want 2", canonicalRows)
	}
}

func TestMergeProjectsRejectsNonEquivalentSources(t *testing.T) {
	tests := []struct {
		name      string
		source    string
		errorPart string
	}{
		{name: "empty", source: "", errorPart: "must not be empty"},
		{name: "substring", source: "engram-memory", errorPart: "must normalize"},
		{name: "levenshtein", source: "engramm", errorPart: "must normalize"},
		{name: "shared directory", source: "other-project", errorPart: "must normalize"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestStore(t)
			if tt.name == "shared directory" {
				if err := s.CreateSession("shared-directory", tt.source, "/shared"); err != nil {
					t.Fatalf("create source session: %v", err)
				}
			}
			if _, err := s.MergeProjects([]string{tt.source}, "engram"); err == nil || !strings.Contains(err.Error(), tt.errorPart) {
				t.Fatalf("MergeProjects error = %v, want normalization rejection", err)
			}
		})
	}
}

func TestMergeProjectsRejectsSeparatorVariants(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.MergeProjects([]string{"foo-bar"}, "foo_bar"); err == nil || !strings.Contains(err.Error(), "must normalize") {
		t.Fatalf("MergeProjects error = %v, want separator variant rejection", err)
	}
}

func TestMergeProjectsRejectsMixedSourcesWithoutMutation(t *testing.T) {
	s := newTestStore(t)
	for _, statement := range []string{
		`INSERT INTO sessions (id, project, directory) VALUES ('legacy-session', 'Engram', '/work/engram')`,
		`INSERT INTO observations (sync_id, session_id, type, title, content, project, scope, normalized_hash) VALUES ('legacy-obs', 'legacy-session', 'decision', 'legacy', 'content', 'Engram', 'project', 'legacy-hash')`,
		`INSERT INTO user_prompts (sync_id, session_id, content, project) VALUES ('legacy-prompt', 'legacy-session', 'prompt', 'Engram')`,
	} {
		if _, err := s.db.Exec(statement); err != nil {
			t.Fatalf("seed legacy record: %v", err)
		}
	}

	var beforeMutations int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sync_mutations`).Scan(&beforeMutations); err != nil {
		t.Fatalf("count sync mutations before merge: %v", err)
	}
	_, err := s.MergeProjects([]string{"Engram", "engram-memory"}, "engram")
	if err == nil || !strings.Contains(err.Error(), "must normalize") {
		t.Fatalf("MergeProjects error = %v, want normalization rejection", err)
	}

	for _, table := range []string{"sessions", "observations", "user_prompts"} {
		var count int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM ` + table + ` WHERE project = 'Engram'`).Scan(&count); err != nil {
			t.Fatalf("count %s legacy records: %v", table, err)
		}
		if count != 1 {
			t.Fatalf("%s legacy records = %d, want 1", table, count)
		}
	}
	var afterMutations int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sync_mutations`).Scan(&afterMutations); err != nil {
		t.Fatalf("count sync mutations after merge: %v", err)
	}
	if afterMutations != beforeMutations {
		t.Fatalf("sync mutations = %d, want %d", afterMutations, beforeMutations)
	}
}

func TestProjectMergeSourceVariantsStayNormalizationEquivalent(t *testing.T) {
	tests := []struct {
		name             string
		rawSource        string
		normalizedSource string
		canonical        string
		want             []string
	}{
		{name: "legacy case spelling", rawSource: "Engram", normalizedSource: "engram", canonical: "engram", want: []string{"Engram"}},
		{name: "whitespace input uses trimmed SQL target", rawSource: " ENGRAM ", normalizedSource: "engram", canonical: "engram", want: []string{"ENGRAM"}},
		{name: "canonical is excluded", rawSource: "engram", normalizedSource: "engram", canonical: "engram", want: nil},
		{name: "non-equivalent source is excluded", rawSource: "foo-bar", normalizedSource: "foo-bar", canonical: "foo_bar", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := projectMergeSourceVariants(tt.rawSource, tt.normalizedSource, tt.canonical)
			if len(got) != len(tt.want) {
				t.Fatalf("variant count = %d, want %d; variants = %v", len(got), len(tt.want), got)
			}
			for i, want := range tt.want {
				if got[i] != want {
					t.Fatalf("variant[%d] = %q, want %q", i, got[i], want)
				}
			}
		})
	}
}

func seedLegacyMergeRecords(t *testing.T, s *Store, project string) {
	t.Helper()
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO sessions (id, project, directory) VALUES (?, ?, ?)`, []any{"legacy-session", project, "/work/engram"}},
		{`INSERT INTO observations (sync_id, session_id, type, title, content, project, scope, normalized_hash) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, []any{"legacy-obs", "legacy-session", "decision", "legacy", "content", project, "project", "legacy-hash"}},
	} {
		if _, err := s.db.Exec(statement.query, statement.args...); err != nil {
			t.Fatalf("seed legacy record: %v", err)
		}
	}
}

func seedPendingLegacyMutations(t *testing.T, s *Store, project string) {
	t.Helper()
	sessionPayload := fmt.Sprintf(`{"id":"legacy-session","project":%q,"directory":"/work/engram"}`, project)
	obsPayload := fmt.Sprintf(`{"sync_id":"legacy-obs","session_id":"legacy-session","type":"decision","title":"legacy","content":"content","project":%q,"scope":"project"}`, project)
	for _, row := range []struct {
		entity, entityKey, payload string
	}{
		{SyncEntitySession, "legacy-session", sessionPayload},
		{SyncEntityObservation, "legacy-obs", obsPayload},
	} {
		if _, err := s.db.Exec(
			`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			DefaultSyncTargetKey, row.entity, row.entityKey, SyncOpUpsert, row.payload, SyncSourceLocal, project,
		); err != nil {
			t.Fatalf("seed pending legacy mutation: %v", err)
		}
	}
}

func pendingMutationsByEntityKey(t *testing.T, s *Store) map[string]SyncMutation {
	t.Helper()
	mutations, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 100)
	if err != nil {
		t.Fatalf("list pending sync mutations: %v", err)
	}
	byKey := make(map[string]SyncMutation, len(mutations))
	for _, mutation := range mutations {
		byKey[mutation.EntityKey] = mutation
	}
	return byKey
}

func payloadProject(t *testing.T, payload string) string {
	t.Helper()
	var decoded struct {
		Project string `json:"project"`
	}
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		t.Fatalf("decode mutation payload %q: %v", payload, err)
	}
	return decoded.Project
}

func TestMergeProjectsMigratesPendingSyncMutationsToCanonicalIdentity(t *testing.T) {
	s := newTestStore(t)
	seedLegacyMergeRecords(t, s, "Engram")
	seedPendingLegacyMutations(t, s, "Engram")
	if err := s.EnrollProject("engram"); err != nil {
		t.Fatalf("enroll canonical: %v", err)
	}

	if _, err := s.MergeProjects([]string{"Engram"}, "engram"); err != nil {
		t.Fatalf("MergeProjects: %v", err)
	}

	var legacyPending int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sync_mutations WHERE project = 'Engram' AND acked_at IS NULL`).Scan(&legacyPending); err != nil {
		t.Fatalf("count legacy pending mutations: %v", err)
	}
	if legacyPending != 0 {
		t.Fatalf("legacy pending mutations = %d, want 0", legacyPending)
	}

	pending := pendingMutationsByEntityKey(t, s)
	for _, entityKey := range []string{"legacy-session", "legacy-obs"} {
		mutation, ok := pending[entityKey]
		if !ok {
			t.Fatalf("pending mutation for %q not deliverable after merge; got %v", entityKey, pending)
		}
		if mutation.Project != "engram" {
			t.Fatalf("mutation %q project = %q, want %q", entityKey, mutation.Project, "engram")
		}
		if got := payloadProject(t, mutation.Payload); got != "engram" {
			t.Fatalf("mutation %q payload project = %q, want %q", entityKey, got, "engram")
		}
	}

	skipped, err := s.SkipAckNonEnrolledMutations(DefaultSyncTargetKey)
	if err != nil {
		t.Fatalf("SkipAckNonEnrolledMutations: %v", err)
	}
	if skipped != 0 {
		t.Fatalf("skip-acked mutations = %d, want 0 — merged mutations must not vanish from sync", skipped)
	}
}

func TestMergeProjectsDoesNotLetLegacyMutationsSuppressCanonicalBackfill(t *testing.T) {
	s := newTestStore(t)
	seedLegacyMergeRecords(t, s, "Engram")
	seedPendingLegacyMutations(t, s, "Engram")
	// A second legacy record with no journal coverage at all.
	if _, err := s.db.Exec(`INSERT INTO sessions (id, project, directory) VALUES (?, ?, ?)`, "legacy-session-2", "Engram", "/work/engram"); err != nil {
		t.Fatalf("seed uncovered legacy session: %v", err)
	}
	if err := s.EnrollProject("engram"); err != nil {
		t.Fatalf("enroll canonical: %v", err)
	}

	if _, err := s.MergeProjects([]string{"Engram"}, "engram"); err != nil {
		t.Fatalf("MergeProjects: %v", err)
	}

	pending := pendingMutationsByEntityKey(t, s)
	for _, entityKey := range []string{"legacy-session", "legacy-obs", "legacy-session-2"} {
		mutation, ok := pending[entityKey]
		if !ok {
			t.Fatalf("no deliverable canonical mutation for %q after merge", entityKey)
		}
		if mutation.Project != "engram" {
			t.Fatalf("mutation %q project = %q, want %q", entityKey, mutation.Project, "engram")
		}
	}

	needs, err := s.projectNeedsBackfill("engram")
	if err != nil {
		t.Fatalf("projectNeedsBackfill: %v", err)
	}
	if needs {
		t.Fatal("canonical project still needs backfill after merge")
	}
}

func TestMergeProjectsCarriesLegacyEnrollmentToCanonical(t *testing.T) {
	s := newTestStore(t)
	seedLegacyMergeRecords(t, s, "Engram")
	// Legacy enrollment row under the raw spelling, before normalization existed.
	if _, err := s.db.Exec(`INSERT INTO sync_enrolled_projects (project) VALUES ('Engram')`); err != nil {
		t.Fatalf("seed legacy enrollment: %v", err)
	}

	if _, err := s.MergeProjects([]string{"Engram"}, "engram"); err != nil {
		t.Fatalf("MergeProjects: %v", err)
	}

	enrolled, err := s.IsProjectEnrolled("engram")
	if err != nil {
		t.Fatalf("IsProjectEnrolled: %v", err)
	}
	if !enrolled {
		t.Fatal("canonical project not enrolled after merge carried legacy enrollment")
	}
	var legacyEnrollment int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sync_enrolled_projects WHERE project = 'Engram'`).Scan(&legacyEnrollment); err != nil {
		t.Fatalf("count legacy enrollment: %v", err)
	}
	if legacyEnrollment != 0 {
		t.Fatalf("legacy enrollment rows = %d, want 0", legacyEnrollment)
	}
}

func TestMergeProjectsSupersedesLegacyEnrollmentWhenCanonicalEnrolled(t *testing.T) {
	s := newTestStore(t)
	seedLegacyMergeRecords(t, s, "Engram")
	if err := s.EnrollProject("engram"); err != nil {
		t.Fatalf("enroll canonical: %v", err)
	}
	if _, err := s.db.Exec(`INSERT INTO sync_enrolled_projects (project) VALUES ('Engram')`); err != nil {
		t.Fatalf("seed legacy enrollment: %v", err)
	}

	if _, err := s.MergeProjects([]string{"Engram"}, "engram"); err != nil {
		t.Fatalf("MergeProjects: %v", err)
	}

	var rows int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sync_enrolled_projects`).Scan(&rows); err != nil {
		t.Fatalf("count enrollment rows: %v", err)
	}
	if rows != 1 {
		t.Fatalf("enrollment rows = %d, want 1 (canonical only)", rows)
	}
	enrolled, err := s.IsProjectEnrolled("engram")
	if err != nil {
		t.Fatalf("IsProjectEnrolled: %v", err)
	}
	if !enrolled {
		t.Fatal("canonical project must stay enrolled after merge")
	}
}

func TestMergeProjectsLeavesUnrelatedEnrollmentAndMutationsUntouched(t *testing.T) {
	s := newTestStore(t)
	seedLegacyMergeRecords(t, s, "Engram")
	if err := s.EnrollProject("other-project"); err != nil {
		t.Fatalf("enroll unrelated project: %v", err)
	}
	if _, err := s.db.Exec(
		`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		DefaultSyncTargetKey, SyncEntitySession, "other-session", SyncOpUpsert, `{"id":"other-session","project":"other-project"}`, SyncSourceLocal, "other-project",
	); err != nil {
		t.Fatalf("seed unrelated mutation: %v", err)
	}

	if _, err := s.MergeProjects([]string{"Engram"}, "engram"); err != nil {
		t.Fatalf("MergeProjects: %v", err)
	}

	var project, payload string
	if err := s.db.QueryRow(`SELECT project, payload FROM sync_mutations WHERE entity_key = 'other-session'`).Scan(&project, &payload); err != nil {
		t.Fatalf("read unrelated mutation: %v", err)
	}
	if project != "other-project" || payloadProject(t, payload) != "other-project" {
		t.Fatalf("unrelated mutation rewritten: project=%q payload=%q", project, payload)
	}
	enrolled, err := s.IsProjectEnrolled("other-project")
	if err != nil {
		t.Fatalf("IsProjectEnrolled: %v", err)
	}
	if !enrolled {
		t.Fatal("unrelated enrollment must survive the merge")
	}
}

func TestMigrateProjectMigratesSyncIdentityForRename(t *testing.T) {
	s := newTestStore(t)
	if err := s.EnrollProject("engram"); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	if err := s.CreateSession("rename-session", "engram", "/work/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := s.AddObservation(AddObservationParams{
		SessionID: "rename-session",
		Type:      "decision",
		Title:     "Renamed identity",
		Content:   "Rename must keep sync identity.",
		Project:   "engram",
		Scope:     "project",
	}); err != nil {
		t.Fatalf("add observation: %v", err)
	}

	result, err := s.MigrateProject("engram", "engram-v2")
	if err != nil {
		t.Fatalf("MigrateProject: %v", err)
	}
	if !result.Migrated {
		t.Fatal("expected migration to happen")
	}

	var stalePending int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sync_mutations WHERE project = 'engram' AND acked_at IS NULL`).Scan(&stalePending); err != nil {
		t.Fatalf("count stale pending mutations: %v", err)
	}
	if stalePending != 0 {
		t.Fatalf("stale pending mutations under old name = %d, want 0", stalePending)
	}

	pending := pendingMutationsByEntityKey(t, s)
	mutation, ok := pending["rename-session"]
	if !ok {
		t.Fatalf("session mutation not deliverable after rename; got %v", pending)
	}
	if mutation.Project != "engram-v2" {
		t.Fatalf("session mutation project = %q, want %q", mutation.Project, "engram-v2")
	}
	if got := payloadProject(t, mutation.Payload); got != "engram-v2" {
		t.Fatalf("session mutation payload project = %q, want %q", got, "engram-v2")
	}

	oldEnrolled, err := s.IsProjectEnrolled("engram")
	if err != nil {
		t.Fatalf("IsProjectEnrolled old: %v", err)
	}
	newEnrolled, err := s.IsProjectEnrolled("engram-v2")
	if err != nil {
		t.Fatalf("IsProjectEnrolled new: %v", err)
	}
	if oldEnrolled || !newEnrolled {
		t.Fatalf("enrollment after rename: old=%v new=%v, want old=false new=true", oldEnrolled, newEnrolled)
	}

	skipped, err := s.SkipAckNonEnrolledMutations(DefaultSyncTargetKey)
	if err != nil {
		t.Fatalf("SkipAckNonEnrolledMutations: %v", err)
	}
	if skipped != 0 {
		t.Fatalf("skip-acked mutations = %d, want 0 after rename", skipped)
	}
}

func TestMigrateProjectLeavesCoexistingNormalizedProjectUntouched(t *testing.T) {
	s := newTestStore(t)
	// A legacy row can carry a non-normalized spelling that no current write
	// path produces. Renaming it moves only its own records, so it must not
	// seize the sync identity of the live project stored under the normalized
	// spelling either.
	if err := s.CreateSession("live-session", "engram", "/work/live"); err != nil {
		t.Fatalf("create live session: %v", err)
	}
	if err := s.EnrollProject("engram"); err != nil {
		t.Fatalf("enroll live: %v", err)
	}
	if _, err := s.db.Exec(
		`INSERT INTO sessions (id, project, directory, started_at) VALUES (?, ?, ?, datetime('now'))`,
		"legacy-session", "Engram", "/work/legacy",
	); err != nil {
		t.Fatalf("seed legacy session: %v", err)
	}
	if _, err := s.db.Exec(
		`INSERT INTO sync_enrolled_projects (project) VALUES (?)`, "Engram",
	); err != nil {
		t.Fatalf("seed legacy enrollment: %v", err)
	}

	if _, err := s.MigrateProject("Engram", "engram-v2"); err != nil {
		t.Fatalf("MigrateProject: %v", err)
	}

	pending := pendingMutationsByEntityKey(t, s)
	live, ok := pending["live-session"]
	if !ok {
		t.Fatalf("coexisting project mutation missing after rename; got %v", pending)
	}
	if live.Project != "engram" {
		t.Fatalf("coexisting mutation project = %q, want %q", live.Project, "engram")
	}
	if got := payloadProject(t, live.Payload); got != "engram" {
		t.Fatalf("coexisting mutation payload project = %q, want %q", got, "engram")
	}

	stillEnrolled, err := s.IsProjectEnrolled("engram")
	if err != nil {
		t.Fatalf("IsProjectEnrolled coexisting: %v", err)
	}
	if !stillEnrolled {
		t.Fatal("coexisting project lost its enrollment to the rename")
	}

	skipped, err := s.SkipAckNonEnrolledMutations(DefaultSyncTargetKey)
	if err != nil {
		t.Fatalf("SkipAckNonEnrolledMutations: %v", err)
	}
	if skipped != 0 {
		t.Fatalf("skip-acked mutations = %d, want 0", skipped)
	}
}

func TestMigrateProjectNormalizesNewName(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s1", "old-name", "/tmp/old"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	result, err := s.MigrateProject("old-name", " New--Name ")
	if err != nil {
		t.Fatalf("MigrateProject: %v", err)
	}
	if !result.Migrated || result.SessionsUpdated != 1 {
		t.Fatalf("unexpected migrate result: %+v", result)
	}

	var project string
	if err := s.db.QueryRow(`SELECT project FROM sessions WHERE id = 's1'`).Scan(&project); err != nil {
		t.Fatalf("read migrated session: %v", err)
	}
	if project != "new-name" {
		t.Fatalf("migrated project = %q, want normalized %q", project, "new-name")
	}
}

func TestMergeThenRenameKeepsSyncLifecycle(t *testing.T) {
	s := newTestStore(t)
	seedLegacyMergeRecords(t, s, "Engram")
	seedPendingLegacyMutations(t, s, "Engram")
	if err := s.EnrollProject("engram"); err != nil {
		t.Fatalf("enroll canonical: %v", err)
	}

	if _, err := s.MergeProjects([]string{"Engram"}, "engram"); err != nil {
		t.Fatalf("MergeProjects: %v", err)
	}
	if _, err := s.MigrateProject("engram", "engram-core"); err != nil {
		t.Fatalf("MigrateProject after merge: %v", err)
	}

	pending := pendingMutationsByEntityKey(t, s)
	for _, entityKey := range []string{"legacy-session", "legacy-obs"} {
		mutation, ok := pending[entityKey]
		if !ok {
			t.Fatalf("no deliverable mutation for %q after merge+rename; got %v", entityKey, pending)
		}
		if mutation.Project != "engram-core" {
			t.Fatalf("mutation %q project = %q, want %q", entityKey, mutation.Project, "engram-core")
		}
		if got := payloadProject(t, mutation.Payload); got != "engram-core" {
			t.Fatalf("mutation %q payload project = %q, want %q", entityKey, got, "engram-core")
		}
	}

	enrolled, err := s.IsProjectEnrolled("engram-core")
	if err != nil {
		t.Fatalf("IsProjectEnrolled: %v", err)
	}
	if !enrolled {
		t.Fatal("renamed project must be enrolled after merge+rename")
	}
	skipped, err := s.SkipAckNonEnrolledMutations(DefaultSyncTargetKey)
	if err != nil {
		t.Fatalf("SkipAckNonEnrolledMutations: %v", err)
	}
	if skipped != 0 {
		t.Fatalf("skip-acked mutations = %d, want 0 after merge+rename", skipped)
	}
}

func TestNewLimitsSQLiteConnectionPoolToSingleOpenConnection(t *testing.T) {
	s := newTestStore(t)
	stats := s.db.Stats()
	if stats.MaxOpenConnections != 1 {
		t.Fatalf("MaxOpenConnections = %d, want 1", stats.MaxOpenConnections)
	}
}

func TestCountObservationsForProject(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s1", "alpha", "/work/alpha"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// No observations yet — count should be 0
	count, err := s.CountObservationsForProject("alpha")
	if err != nil {
		t.Fatalf("CountObservationsForProject: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0, got %d", count)
	}

	// Add two observations
	for i := 0; i < 2; i++ {
		if _, err := s.AddObservation(AddObservationParams{
			SessionID: "s1",
			Type:      "decision",
			Title:     "obs " + string(rune('A'+i)),
			Content:   "unique content that is definitely unique " + string(rune('A'+i)),
			Project:   "alpha",
			Scope:     "project",
		}); err != nil {
			t.Fatalf("AddObservation: %v", err)
		}
	}

	count, err = s.CountObservationsForProject("alpha")
	if err != nil {
		t.Fatalf("CountObservationsForProject: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2, got %d", count)
	}

	// Different project should return 0
	count, err = s.CountObservationsForProject("beta")
	if err != nil {
		t.Fatalf("CountObservationsForProject for beta: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 for beta, got %d", count)
	}
}

func TestPruneProjectPreservesSoftDeletedObservationSession(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("referenced", "empty-project", "/work"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	observationID, err := s.AddObservation(AddObservationParams{SessionID: "referenced", Type: "note", Title: "deleted", Content: "deleted content", Project: "empty-project", Scope: "project"})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}
	if err := s.DeleteObservation(observationID, false); err != nil {
		t.Fatalf("DeleteObservation: %v", err)
	}
	if _, err := s.AddPrompt(AddPromptParams{SessionID: "referenced", Content: "remove me", Project: "empty-project"}); err != nil {
		t.Fatalf("AddPrompt: %v", err)
	}

	result, err := s.PruneProject("empty-project")
	if err != nil {
		t.Fatalf("PruneProject: %v", err)
	}
	if result.PromptsDeleted != 1 || result.SessionsDeleted != 0 {
		t.Fatalf("PruneResult = %+v, want one prompt and no sessions", result)
	}
	var sessions, observations, prompts int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM sessions WHERE id = 'referenced'`).Scan(&sessions); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM observations WHERE id = ? AND deleted_at IS NOT NULL`, observationID).Scan(&observations); err != nil {
		t.Fatalf("count observations: %v", err)
	}
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM user_prompts WHERE project = 'empty-project'`).Scan(&prompts); err != nil {
		t.Fatalf("count prompts: %v", err)
	}
	if sessions != 1 || observations != 1 || prompts != 0 {
		t.Fatalf("rows after prune: sessions=%d observations=%d prompts=%d", sessions, observations, prompts)
	}
}

func TestPruneProjectDeletesOnlyUnreferencedSessions(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("referenced", "empty-project", "/work"); err != nil {
		t.Fatalf("CreateSession referenced: %v", err)
	}
	if err := s.CreateSession("unreferenced", "empty-project", "/work"); err != nil {
		t.Fatalf("CreateSession unreferenced: %v", err)
	}
	id, err := s.AddObservation(AddObservationParams{SessionID: "referenced", Type: "note", Title: "deleted", Content: "deleted content", Project: "empty-project", Scope: "project"})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}
	if err := s.DeleteObservation(id, false); err != nil {
		t.Fatalf("DeleteObservation: %v", err)
	}

	result, err := s.PruneProject("empty-project")
	if err != nil {
		t.Fatalf("PruneProject: %v", err)
	}
	if result.SessionsDeleted != 1 || result.PromptsDeleted != 0 {
		t.Fatalf("PruneResult = %+v, want one session and no prompts", result)
	}
	var referenced, unreferenced int
	_ = s.DB().QueryRow(`SELECT COUNT(*) FROM sessions WHERE id = 'referenced'`).Scan(&referenced)
	_ = s.DB().QueryRow(`SELECT COUNT(*) FROM sessions WHERE id = 'unreferenced'`).Scan(&unreferenced)
	if referenced != 1 || unreferenced != 0 {
		t.Fatalf("sessions after prune: referenced=%d unreferenced=%d", referenced, unreferenced)
	}
}

// ─── DeleteSession tests ─────────────────────────────────────────────────────

func TestRecentObservationsOrderByCreatedAtBeforeID(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s-recent-created", "proj", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	rows := []struct {
		id        int64
		title     string
		createdAt string
	}{
		{id: 100, title: "older-high-id", createdAt: "2025-01-01 00:00:00"},
		{id: 50, title: "newer-low-id", createdAt: "2025-01-02 00:00:00"},
	}
	for _, row := range rows {
		if _, err := s.db.Exec(`INSERT INTO observations (id, sync_id, session_id, type, title, content, project, scope, normalized_hash, revision_count, duplicate_count, created_at, updated_at)
			VALUES (?, ?, 's-recent-created', 'note', ?, ?, 'proj', 'project', ?, 1, 1, ?, ?)`, row.id, fmt.Sprintf("obs-%d", row.id), row.title, row.title, row.title, row.createdAt, row.createdAt); err != nil {
			t.Fatalf("insert observation %d: %v", row.id, err)
		}
	}

	obs, err := s.RecentObservations("proj", "project", 10)
	if err != nil {
		t.Fatalf("RecentObservations: %v", err)
	}
	if len(obs) < 2 || obs[0].Title != "newer-low-id" || obs[1].Title != "older-high-id" {
		t.Fatalf("expected created_at desc before id desc, got %+v", obs)
	}
}

func TestRecentObservationsSameTimestampTiesByIDDesc(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s-recent-tie", "proj", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	for _, id := range []int64{10, 20} {
		if _, err := s.db.Exec(`INSERT INTO observations (id, sync_id, session_id, type, title, content, project, scope, normalized_hash, revision_count, duplicate_count, created_at, updated_at)
			VALUES (?, ?, 's-recent-tie', 'note', ?, ?, 'proj', 'project', ?, 1, 1, '2025-01-01 00:00:00', '2025-01-01 00:00:00')`, id, fmt.Sprintf("obs-tie-%d", id), fmt.Sprintf("tie-%d", id), fmt.Sprintf("tie-%d", id), fmt.Sprintf("hash-%d", id)); err != nil {
			t.Fatalf("insert observation %d: %v", id, err)
		}
	}

	obs, err := s.RecentObservations("proj", "project", 10)
	if err != nil {
		t.Fatalf("RecentObservations: %v", err)
	}
	if len(obs) < 2 || obs[0].ID != 20 || obs[1].ID != 10 {
		t.Fatalf("expected id desc tie-breaker, got %+v", obs)
	}
}

func TestRecentSessionsOrderByLatestCreatedAtDeterministically(t *testing.T) {
	s := newTestStore(t)
	for _, sess := range []struct {
		id        string
		startedAt string
	}{
		{id: "sess-a", startedAt: "2025-01-01 00:00:00"},
		{id: "sess-b", startedAt: "2025-01-01 00:00:00"},
		{id: "sess-c", startedAt: "2025-01-01 00:00:00"},
	} {
		if err := s.CreateSession(sess.id, "proj", "/tmp"); err != nil {
			t.Fatalf("create session %s: %v", sess.id, err)
		}
		if _, err := s.db.Exec(`UPDATE sessions SET started_at = ? WHERE id = ?`, sess.startedAt, sess.id); err != nil {
			t.Fatalf("update session %s: %v", sess.id, err)
		}
	}
	for _, row := range []struct {
		id        int64
		sessionID string
		createdAt string
	}{
		{id: 1, sessionID: "sess-a", createdAt: "2025-01-03 00:00:00"},
		{id: 2, sessionID: "sess-b", createdAt: "2025-01-02 00:00:00"},
		{id: 3, sessionID: "sess-c", createdAt: "2025-01-03 00:00:00"},
	} {
		if _, err := s.db.Exec(`INSERT INTO observations (id, sync_id, session_id, type, title, content, project, scope, normalized_hash, revision_count, duplicate_count, created_at, updated_at)
			VALUES (?, ?, ?, 'note', ?, ?, 'proj', 'project', ?, 1, 1, ?, ?)`, row.id, fmt.Sprintf("obs-session-%d", row.id), row.sessionID, row.sessionID, row.sessionID, fmt.Sprintf("hash-session-%d", row.id), row.createdAt, row.createdAt); err != nil {
			t.Fatalf("insert observation %d: %v", row.id, err)
		}
	}

	sessions, err := s.RecentSessions("proj", 10)
	if err != nil {
		t.Fatalf("RecentSessions: %v", err)
	}
	if len(sessions) < 3 || sessions[0].ID != "sess-c" || sessions[1].ID != "sess-a" || sessions[2].ID != "sess-b" {
		t.Fatalf("expected latest created_at desc with session id desc tie-breaker, got %+v", sessions)
	}
}

func TestDeleteSession_EmptySession(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("sess-empty", "proj", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	if err := s.DeleteSession("sess-empty"); err != nil {
		t.Fatalf("expected no error deleting empty session, got: %v", err)
	}

	// Session should be gone.
	sessions, err := s.RecentSessions("proj", 10)
	if err != nil {
		t.Fatalf("recent sessions: %v", err)
	}
	for _, ss := range sessions {
		if ss.ID == "sess-empty" {
			t.Fatal("expected session to be deleted but it still exists")
		}
	}
}

func TestDeleteSession_EnrolledProjectEnqueuesSyncDeleteMutation(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("sess-enrolled", "proj", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := s.AddPrompt(AddPromptParams{
		SessionID: "sess-enrolled",
		Content:   "prompt should remain",
		Project:   "proj",
	}); err != nil {
		t.Fatalf("add prompt: %v", err)
	}
	if err := s.EnrollProject("proj"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}

	if err := s.DeleteSession("sess-enrolled"); err != nil {
		t.Fatalf("delete session: %v", err)
	}

	sessions, err := s.RecentSessions("proj", 10)
	if err != nil {
		t.Fatalf("recent sessions: %v", err)
	}
	found := false
	for _, ss := range sessions {
		if ss.ID == "sess-enrolled" {
			found = true
			break
		}
	}
	if found {
		t.Fatal("expected enrolled session to be deleted")
	}

	prompts, err := s.RecentPrompts("proj", 10)
	if err != nil {
		t.Fatalf("recent prompts: %v", err)
	}
	if len(prompts) != 0 {
		t.Fatalf("expected prompt rows to be removed with enrolled delete, got %d", len(prompts))
	}

	mutations, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 10)
	if err != nil {
		t.Fatalf("list pending mutations: %v", err)
	}
	if len(mutations) == 0 {
		t.Fatal("expected session delete mutation to be enqueued")
	}
	last := mutations[len(mutations)-1]
	if last.Entity != SyncEntitySession || last.EntityKey != "sess-enrolled" || last.Op != SyncOpDelete {
		t.Fatalf("expected final mutation session/delete for sess-enrolled, got %+v", last)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(last.Payload), &payload); err != nil {
		t.Fatalf("decode session delete payload: %v", err)
	}
	if payload["id"] != "sess-enrolled" {
		t.Fatalf("expected delete payload id sess-enrolled, got %#v", payload["id"])
	}
	if payload["project"] != "proj" {
		t.Fatalf("expected delete payload project proj, got %#v", payload["project"])
	}
	if _, ok := payload["deleted_at"]; !ok {
		t.Fatalf("expected delete payload to include deleted_at, got %#v", payload)
	}
}

func TestQuarantineIrreparableSyncMutationsPreservesJournalAndUnblocksTransport(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("repairable", "project", "/tmp/repairable"); err != nil {
		t.Fatalf("create repairable session: %v", err)
	}
	for _, mutation := range []struct {
		entity, key, op, payload, project string
	}{
		{SyncEntitySession, "poison", SyncOpUpsert, `{"id":"poison"}`, ""},
		{SyncEntitySession, "later", SyncOpDelete, `{"id":"later"}`, ""},
		{SyncEntitySession, "repairable", SyncOpUpsert, `{"id":"repairable"}`, "project"},
	} {
		if _, err := s.db.Exec(`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project) VALUES (?, ?, ?, ?, ?, ?, ?)`, DefaultSyncTargetKey, mutation.entity, mutation.key, mutation.op, mutation.payload, SyncSourceLocal, mutation.project); err != nil {
			t.Fatalf("seed mutation %s: %v", mutation.key, err)
		}
	}
	var laterSeq int64
	if err := s.db.QueryRow(`SELECT seq FROM sync_mutations WHERE entity_key = 'later'`).Scan(&laterSeq); err != nil {
		t.Fatalf("read later sequence: %v", err)
	}

	dryRun, err := s.QuarantineIrreparableSyncMutations("", false)
	if err != nil || len(dryRun.Actions) != 1 {
		t.Fatalf("dry-run report=%+v err=%v", dryRun, err)
	}
	var disposition string
	if err := s.db.QueryRow(`SELECT disposition FROM sync_mutations WHERE entity_key = 'poison'`).Scan(&disposition); err != nil || disposition != SyncMutationDispositionPending {
		t.Fatalf("dry-run disposition=%q err=%v", disposition, err)
	}

	report, err := s.QuarantineIrreparableSyncMutations("", true)
	if err != nil || len(report.Actions) != 1 {
		t.Fatalf("apply report=%+v err=%v", report, err)
	}
	var payload, reason, evidence string
	var ackedAt, dispositionAt sql.NullString
	if err := s.db.QueryRow(`SELECT payload, disposition, disposition_reason, disposition_evidence, disposition_at, acked_at FROM sync_mutations WHERE entity_key = 'poison'`).Scan(&payload, &disposition, &reason, &evidence, &dispositionAt, &ackedAt); err != nil {
		t.Fatalf("read quarantined mutation: %v", err)
	}
	if payload != `{"id":"poison"}` || disposition != SyncMutationDispositionQuarantined || reason == "" || evidence == "" || !dispositionAt.Valid || ackedAt.Valid {
		t.Fatalf("quarantine did not preserve audit state: payload=%q disposition=%q reason=%q evidence=%q at=%v acked=%v", payload, disposition, reason, evidence, dispositionAt, ackedAt)
	}
	pending, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 10)
	if err != nil || len(pending) != 1 || pending[0].EntityKey != "later" || pending[0].Seq != laterSeq {
		t.Fatalf("transport pending=%+v err=%v", pending, err)
	}
	state, err := s.GetSyncState(DefaultSyncTargetKey)
	if err != nil || state.LastAckedSeq != 0 {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	again, err := s.QuarantineIrreparableSyncMutations("", true)
	if err != nil || len(again.Actions) != 0 {
		t.Fatalf("repeat report=%+v err=%v", again, err)
	}
	var repeatedEvidence string
	if err := s.db.QueryRow(`SELECT disposition_evidence FROM sync_mutations WHERE entity_key = 'poison'`).Scan(&repeatedEvidence); err != nil || repeatedEvidence != evidence {
		t.Fatalf("repeat changed evidence=%q err=%v", repeatedEvidence, err)
	}
}

func TestQuarantineIrreparableSyncMutationsRefreshesAffectedLifecycles(t *testing.T) {
	t.Run("clears stale default and project lifecycle", func(t *testing.T) {
		s := newTestStore(t)
		const payload = `{"id":"poison"}`
		if _, err := s.db.Exec(`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project) VALUES ('cloud', 'session', 'poison', 'upsert', ?, 'local', 'project-a')`, payload); err != nil {
			t.Fatalf("seed poison mutation: %v", err)
		}
		var seq int64
		if err := s.db.QueryRow(`SELECT seq FROM sync_mutations WHERE entity_key = 'poison'`).Scan(&seq); err != nil {
			t.Fatalf("read poison sequence: %v", err)
		}
		if err := s.MarkSyncPending(DefaultSyncTargetKey); err != nil {
			t.Fatalf("mark default pending: %v", err)
		}
		if err := s.MarkSyncPending(syncTargetKeyForProject("project-a")); err != nil {
			t.Fatalf("mark project pending: %v", err)
		}

		report, err := s.QuarantineIrreparableSyncMutations("project-a", true)
		if err != nil || len(report.Actions) != 1 {
			t.Fatalf("apply report=%+v err=%v", report, err)
		}
		var gotSeq int64
		var gotPayload, evidence string
		var ackedAt sql.NullString
		if err := s.db.QueryRow(`SELECT seq, payload, disposition_evidence, acked_at FROM sync_mutations WHERE entity_key = 'poison'`).Scan(&gotSeq, &gotPayload, &evidence, &ackedAt); err != nil {
			t.Fatalf("read quarantined mutation: %v", err)
		}
		if gotSeq != seq || gotPayload != payload || evidence == "" || ackedAt.Valid {
			t.Fatalf("quarantine changed mutation audit data: seq=%d payload=%q evidence=%q acked=%v", gotSeq, gotPayload, evidence, ackedAt)
		}
		for _, targetKey := range []string{DefaultSyncTargetKey, syncTargetKeyForProject("project-a")} {
			state, err := s.GetSyncState(targetKey)
			if err != nil || state.Lifecycle != SyncLifecycleHealthy || state.LastAckedSeq != 0 {
				t.Fatalf("state for %q = %+v, err=%v", targetKey, state, err)
			}
		}

		again, err := s.QuarantineIrreparableSyncMutations("project-a", true)
		if err != nil || len(again.Actions) != 0 {
			t.Fatalf("repeat report=%+v err=%v", again, err)
		}
		var repeatedEvidence string
		if err := s.db.QueryRow(`SELECT disposition_evidence FROM sync_mutations WHERE entity_key = 'poison'`).Scan(&repeatedEvidence); err != nil || repeatedEvidence != evidence {
			t.Fatalf("repeat changed evidence=%q err=%v", repeatedEvidence, err)
		}
	})

	t.Run("preserves pending lifecycle and refreshes only quarantined project", func(t *testing.T) {
		s := newTestStore(t)
		for _, mutation := range []struct{ key, project, payload string }{
			{key: "poison", project: "project-a", payload: `{"id":"poison"}`},
			{key: "pending", project: "project-b", payload: `{"id":"pending","project":"project-b"}`},
		} {
			if _, err := s.db.Exec(`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project) VALUES ('cloud', 'session', ?, 'upsert', ?, 'local', ?)`, mutation.key, mutation.payload, mutation.project); err != nil {
				t.Fatalf("seed %s mutation: %v", mutation.key, err)
			}
		}
		for _, targetKey := range []string{DefaultSyncTargetKey, syncTargetKeyForProject("project-a"), syncTargetKeyForProject("project-b")} {
			if err := s.MarkSyncPending(targetKey); err != nil {
				t.Fatalf("mark %q pending: %v", targetKey, err)
			}
		}

		if _, err := s.QuarantineIrreparableSyncMutations("project-a", true); err != nil {
			t.Fatalf("quarantine project-a: %v", err)
		}
		for _, targetKey := range []string{DefaultSyncTargetKey, syncTargetKeyForProject("project-b")} {
			state, err := s.GetSyncState(targetKey)
			if err != nil || state.Lifecycle != SyncLifecyclePending {
				t.Fatalf("state for %q = %+v, err=%v", targetKey, state, err)
			}
		}
		state, err := s.GetSyncState(syncTargetKeyForProject("project-a"))
		if err != nil || state.Lifecycle != SyncLifecycleHealthy {
			t.Fatalf("affected project state=%+v err=%v", state, err)
		}
	})
}

func TestQuarantineIrreparableSyncMutationsKeepsProjectPendingWhenWorkRemains(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("keep", "project-a", "/work/project-a"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	for _, mutation := range []struct{ key, payload string }{
		{key: "poison", payload: `{"id":"poison"}`},
		{key: "keep", payload: `{"id":"keep","directory":"/work/project-a"}`},
	} {
		if _, err := s.db.Exec(`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project) VALUES (?, 'session', ?, 'upsert', ?, 'local', 'project-a')`, DefaultSyncTargetKey, mutation.key, mutation.payload); err != nil {
			t.Fatalf("seed %s mutation: %v", mutation.key, err)
		}
	}
	for _, targetKey := range []string{DefaultSyncTargetKey, syncTargetKeyForProject("project-a")} {
		if err := s.MarkSyncPending(targetKey); err != nil {
			t.Fatalf("mark %q pending: %v", targetKey, err)
		}
	}

	report, err := s.QuarantineIrreparableSyncMutations("project-a", true)
	if err != nil || len(report.Actions) != 1 || report.Actions[0].EntityKey != "poison" {
		t.Fatalf("apply report=%+v err=%v", report, err)
	}

	// The local journal writes every row under the default `cloud` target key and
	// carries the project in its own column, so the per-project lifecycle refresh
	// must count that key instead of the `cloud:<project>` bookkeeping key.
	for _, targetKey := range []string{DefaultSyncTargetKey, syncTargetKeyForProject("project-a")} {
		state, err := s.GetSyncState(targetKey)
		if err != nil {
			t.Fatalf("state for %q: %v", targetKey, err)
		}
		if state.Lifecycle != SyncLifecyclePending {
			t.Fatalf("quarantine masked pending work for %q: lifecycle=%q", targetKey, state.Lifecycle)
		}
	}
	pendingForProject, err := s.HasPendingSyncMutationsForProject("project-a")
	if err != nil || !pendingForProject {
		t.Fatalf("HasPendingSyncMutationsForProject=%v err=%v", pendingForProject, err)
	}

	// Once the transportable work is acked, quarantining a newly poisoned row must
	// clear the project lifecycle through that same key.
	if _, err := s.db.Exec(`UPDATE sync_mutations SET acked_at = datetime('now') WHERE entity_key = 'keep'`); err != nil {
		t.Fatalf("ack keep mutation: %v", err)
	}
	if _, err := s.db.Exec(`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project) VALUES (?, 'session', 'poison-2', 'upsert', '{"id":"poison-2"}', 'local', 'project-a')`, DefaultSyncTargetKey); err != nil {
		t.Fatalf("seed second poison mutation: %v", err)
	}
	second, err := s.QuarantineIrreparableSyncMutations("project-a", true)
	if err != nil || len(second.Actions) != 1 || second.Actions[0].EntityKey != "poison-2" {
		t.Fatalf("second quarantine report=%+v err=%v", second, err)
	}
	state, err := s.GetSyncState(syncTargetKeyForProject("project-a"))
	if err != nil || state.Lifecycle != SyncLifecycleHealthy {
		t.Fatalf("project lifecycle should clear once no transportable work remains: %+v err=%v", state, err)
	}
}

func TestQuarantineIrreparableSyncMutationsClearsCloudUpgradeBlockers(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.db.Exec(`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project) VALUES (?, 'session', 'poison', 'upsert', '{"id":"poison"}', 'local', 'project-a')`, DefaultSyncTargetKey); err != nil {
		t.Fatalf("seed poison mutation: %v", err)
	}

	before, err := s.DiagnoseCloudUpgradeLegacyMutations("project-a")
	if err != nil || before.BlockedCount != 1 {
		t.Fatalf("legacy report before quarantine=%+v err=%v", before, err)
	}

	if _, err := s.QuarantineIrreparableSyncMutations("project-a", true); err != nil {
		t.Fatalf("quarantine: %v", err)
	}

	after, err := s.DiagnoseCloudUpgradeLegacyMutations("project-a")
	if err != nil {
		t.Fatalf("legacy report after quarantine: %v", err)
	}
	if after.BlockedCount != 0 || after.RepairableCount != 0 || len(after.Findings) != 0 {
		t.Fatalf("quarantined mutation still blocks the cloud upgrade: %+v", after)
	}

	// A genuinely irreparable row enqueued afterwards must still block.
	if _, err := s.db.Exec(`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project) VALUES (?, 'session', 'poison-2', 'upsert', '{"id":"poison-2"}', 'local', 'project-a')`, DefaultSyncTargetKey); err != nil {
		t.Fatalf("seed second poison mutation: %v", err)
	}
	residual, err := s.DiagnoseCloudUpgradeLegacyMutations("project-a")
	if err != nil || residual.BlockedCount != 1 || len(residual.Findings) != 1 || residual.Findings[0].EntityKey != "poison-2" {
		t.Fatalf("new irreparable work must still block: %+v err=%v", residual, err)
	}
}

func TestQuarantineIrreparableSyncMutationsFailsClosed(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.db.Exec(`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project) VALUES ('cloud', 'session', 'poison', 'upsert', '{"id":"poison"}', 'local', '')`); err != nil {
		t.Fatalf("seed mutation: %v", err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER reject_quarantine BEFORE UPDATE OF disposition ON sync_mutations BEGIN SELECT RAISE(ABORT, 'quarantine blocked'); END`); err != nil {
		t.Fatalf("create reject trigger: %v", err)
	}
	if _, err := s.QuarantineIrreparableSyncMutations("", true); err == nil {
		t.Fatal("expected quarantine persistence error")
	}
	var disposition string
	if err := s.db.QueryRow(`SELECT disposition FROM sync_mutations WHERE entity_key = 'poison'`).Scan(&disposition); err != nil || disposition != SyncMutationDispositionPending {
		t.Fatalf("failed quarantine disposition=%q err=%v", disposition, err)
	}
	pending, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 10)
	if err != nil || len(pending) != 1 || pending[0].EntityKey != "poison" {
		t.Fatalf("failed quarantine transport pending=%+v err=%v", pending, err)
	}
}

func TestQuarantineIrreparableSyncMutationsRollsBackWhenLifecycleRefreshFails(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.db.Exec(`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project) VALUES ('cloud', 'session', 'poison', 'upsert', '{"id":"poison"}', 'local', 'project-a')`); err != nil {
		t.Fatalf("seed mutation: %v", err)
	}
	if err := s.MarkSyncPending(DefaultSyncTargetKey); err != nil {
		t.Fatalf("mark default pending: %v", err)
	}
	if err := s.MarkSyncPending(syncTargetKeyForProject("project-a")); err != nil {
		t.Fatalf("mark project pending: %v", err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER reject_lifecycle_refresh BEFORE UPDATE OF lifecycle ON sync_state BEGIN SELECT RAISE(ABORT, 'lifecycle refresh blocked'); END`); err != nil {
		t.Fatalf("create lifecycle refresh trigger: %v", err)
	}

	if _, err := s.QuarantineIrreparableSyncMutations("project-a", true); err == nil {
		t.Fatal("expected lifecycle refresh error")
	}
	var disposition string
	var evidence sql.NullString
	if err := s.db.QueryRow(`SELECT disposition, disposition_evidence FROM sync_mutations WHERE entity_key = 'poison'`).Scan(&disposition, &evidence); err != nil {
		t.Fatalf("read mutation after rollback: %v", err)
	}
	if disposition != SyncMutationDispositionPending || evidence.Valid {
		t.Fatalf("refresh failure did not roll back quarantine: disposition=%q evidence=%v", disposition, evidence)
	}
}

func TestRepairObservationMutationTitles(t *testing.T) {
	seed := func(t *testing.T, content string, mutate func(map[string]json.RawMessage)) (*Store, Observation, SyncMutation, string) {
		t.Helper()
		s := newTestStore(t)
		if err := s.CreateSession("title-repair", "project-a", "/work/project-a"); err != nil {
			t.Fatalf("create session: %v", err)
		}
		id, err := s.AddObservation(AddObservationParams{SessionID: "title-repair", Type: "bugfix", Title: "original", Content: "original", Project: "project-a", Scope: "project"})
		if err != nil {
			t.Fatalf("add observation: %v", err)
		}
		obs, err := s.GetObservation(id)
		if err != nil {
			t.Fatalf("get observation: %v", err)
		}
		if _, err := s.db.Exec(`UPDATE observations SET title = '', content = ? WHERE id = ?`, content, id); err != nil {
			t.Fatalf("seed titleless source: %v", err)
		}
		var mutation SyncMutation
		if err := s.db.QueryRow(`SELECT seq, target_key, entity, entity_key, op, payload, source, project, occurred_at, acked_at FROM sync_mutations WHERE entity = ? AND entity_key = ?`, SyncEntityObservation, obs.SyncID).Scan(&mutation.Seq, &mutation.TargetKey, &mutation.Entity, &mutation.EntityKey, &mutation.Op, &mutation.Payload, &mutation.Source, &mutation.Project, &mutation.OccurredAt, &mutation.AckedAt); err != nil {
			t.Fatalf("read mutation: %v", err)
		}
		var payload map[string]json.RawMessage
		if err := json.Unmarshal([]byte(mutation.Payload), &payload); err != nil {
			t.Fatalf("decode mutation: %v", err)
		}
		payload["title"] = json.RawMessage(`"  "`)
		payload["unknown"] = json.RawMessage(`{"kept":true}`)
		mutate(payload)
		body, _ := json.Marshal(payload)
		if _, err := s.db.Exec(`UPDATE sync_mutations SET payload = ? WHERE seq = ?`, string(body), mutation.Seq); err != nil {
			t.Fatalf("seed frozen payload: %v", err)
		}
		mutation.Payload = string(body)
		return s, *obs, mutation, string(body)
	}

	t.Run("plans and applies an in-place repair", func(t *testing.T) {
		s, obs, mutation, originalPayload := seed(t, "<private>secret</private> First sentence. Second sentence.", func(map[string]json.RawMessage) {})
		var mutationCountBefore int
		if err := s.db.QueryRow(`SELECT count(*) FROM sync_mutations`).Scan(&mutationCountBefore); err != nil {
			t.Fatalf("count mutations before repair: %v", err)
		}
		plan, err := s.RepairObservationMutationTitles("project-a", false)
		if err != nil || len(plan.Actions) != 1 || plan.Actions[0].Title != "[REDACTED] First sentence." {
			t.Fatalf("plan=%+v err=%v", plan, err)
		}
		var title, payload, disposition string
		var seq int64
		var ackedAt sql.NullString
		if err := s.db.QueryRow(`SELECT title FROM observations WHERE id = ?`, obs.ID).Scan(&title); err != nil || title != "" {
			t.Fatalf("plan changed source title=%q err=%v", title, err)
		}
		if err := s.db.QueryRow(`SELECT seq, payload, acked_at, disposition FROM sync_mutations WHERE seq = ?`, mutation.Seq).Scan(&seq, &payload, &ackedAt, &disposition); err != nil || seq != mutation.Seq || payload != originalPayload || ackedAt.Valid || disposition != SyncMutationDispositionPending {
			t.Fatalf("plan changed mutation seq=%d payload=%q acked=%v disposition=%q err=%v", seq, payload, ackedAt, disposition, err)
		}

		applied, err := s.RepairObservationMutationTitles("project-a", true)
		if err != nil || len(applied.Actions) != 1 {
			t.Fatalf("apply=%+v err=%v", applied, err)
		}
		if err := s.db.QueryRow(`SELECT title FROM observations WHERE id = ?`, obs.ID).Scan(&title); err != nil || title != applied.Actions[0].Title {
			t.Fatalf("source title=%q err=%v", title, err)
		}
		if err := s.db.QueryRow(`SELECT payload, acked_at, disposition FROM sync_mutations WHERE seq = ?`, mutation.Seq).Scan(&payload, &ackedAt, &disposition); err != nil || ackedAt.Valid || disposition != SyncMutationDispositionPending {
			t.Fatalf("mutation state payload=%q acked=%v disposition=%q err=%v", payload, ackedAt, disposition, err)
		}
		var mutationCountAfter int
		if err := s.db.QueryRow(`SELECT count(*) FROM sync_mutations`).Scan(&mutationCountAfter); err != nil || mutationCountAfter != mutationCountBefore {
			t.Fatalf("mutation count after repair=%d before=%d err=%v", mutationCountAfter, mutationCountBefore, err)
		}
		var repaired map[string]json.RawMessage
		_ = json.Unmarshal([]byte(payload), &repaired)
		if string(repaired["unknown"]) != `{"kept":true}` || ValidateSyncMutationPayload(mutation.Entity, mutation.Op, payload, mutation.EntityKey).ReasonCode != "" {
			t.Fatalf("repaired payload=%s", payload)
		}
		var ftsCount int
		if err := s.db.QueryRow(`SELECT count(*) FROM observations_fts WHERE observations_fts MATCH 'REDACTED'`).Scan(&ftsCount); err != nil || ftsCount != 1 {
			t.Fatalf("fts count=%d err=%v", ftsCount, err)
		}
		if again, err := s.RepairObservationMutationTitles("project-a", true); err != nil || len(again.Actions) != 0 {
			t.Fatalf("repeat=%+v err=%v", again, err)
		}
	})

	for _, tc := range []struct {
		name, content string
		mutate        func(map[string]json.RawMessage)
		want          int
	}{
		{"additional missing field", "content", func(p map[string]json.RawMessage) { p["scope"] = json.RawMessage(`""`) }, 0},
		{"empty content", "", func(map[string]json.RawMessage) {}, 0},
		{"unicode sentence", "  日本語の文章です。 Second sentence.", func(map[string]json.RawMessage) {}, 1},
		{"rune safe truncation", strings.Repeat("界", 301), func(map[string]json.RawMessage) {}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _, _ := seed(t, tc.content, tc.mutate)
			report, err := s.RepairObservationMutationTitles("project-a", false)
			if err != nil || len(report.Actions) != tc.want {
				t.Fatalf("report=%+v err=%v", report, err)
			}
			if tc.name == "unicode sentence" && report.Actions[0].Title != "日本語の文章です。" {
				t.Fatalf("unicode title=%q", report.Actions[0].Title)
			}
			if tc.name == "rune safe truncation" && utf8.RuneCountInString(report.Actions[0].Title) != 303 {
				t.Fatalf("truncated title=%q", report.Actions[0].Title)
			}
		})
	}

	t.Run("skips ineligible observations without mutation", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			mutate func(t *testing.T, s *Store, obs Observation, mutation SyncMutation)
		}{
			{
				name: "missing payload sync ID",
				mutate: func(t *testing.T, s *Store, _ Observation, mutation SyncMutation) {
					t.Helper()
					var payload map[string]json.RawMessage
					if err := json.Unmarshal([]byte(mutation.Payload), &payload); err != nil {
						t.Fatalf("decode payload: %v", err)
					}
					delete(payload, "sync_id")
					body, err := json.Marshal(payload)
					if err != nil {
						t.Fatalf("encode payload: %v", err)
					}
					if _, err := s.db.Exec(`UPDATE sync_mutations SET payload = ? WHERE seq = ?`, string(body), mutation.Seq); err != nil {
						t.Fatalf("remove payload sync ID: %v", err)
					}
				},
			},
			{
				name: "mismatched payload sync ID",
				mutate: func(t *testing.T, s *Store, _ Observation, mutation SyncMutation) {
					t.Helper()
					if _, err := s.db.Exec(`UPDATE sync_mutations SET payload = json_set(payload, '$.sync_id', 'other-observation') WHERE seq = ?`, mutation.Seq); err != nil {
						t.Fatalf("mismatch payload sync ID: %v", err)
					}
				},
			},
			{
				name: "payload project mismatch",
				mutate: func(t *testing.T, s *Store, _ Observation, mutation SyncMutation) {
					t.Helper()
					if _, err := s.db.Exec(`UPDATE sync_mutations SET payload = json_set(payload, '$.project', 'project-b') WHERE seq = ?`, mutation.Seq); err != nil {
						t.Fatalf("mismatch payload project: %v", err)
					}
				},
			},
			{
				name: "mutation project mismatch",
				mutate: func(t *testing.T, s *Store, _ Observation, mutation SyncMutation) {
					t.Helper()
					if _, err := s.db.Exec(`UPDATE sync_mutations SET project = 'project-b' WHERE seq = ?`, mutation.Seq); err != nil {
						t.Fatalf("mismatch mutation project: %v", err)
					}
				},
			},
			{
				name: "soft-deleted observation",
				mutate: func(t *testing.T, s *Store, obs Observation, _ SyncMutation) {
					t.Helper()
					if _, err := s.db.Exec(`UPDATE observations SET deleted_at = datetime('now') WHERE id = ?`, obs.ID); err != nil {
						t.Fatalf("soft-delete observation: %v", err)
					}
				},
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				s, obs, mutation, _ := seed(t, "Recovered title. More detail.", func(map[string]json.RawMessage) {})
				tc.mutate(t, s, obs, mutation)

				var title, payload string
				var deletedAt sql.NullString
				if err := s.db.QueryRow(`SELECT title, deleted_at FROM observations WHERE id = ?`, obs.ID).Scan(&title, &deletedAt); err != nil {
					t.Fatalf("read source before repair: %v", err)
				}
				if err := s.db.QueryRow(`SELECT payload FROM sync_mutations WHERE seq = ?`, mutation.Seq).Scan(&payload); err != nil {
					t.Fatalf("read mutation before repair: %v", err)
				}

				report, err := s.RepairObservationMutationTitles("project-a", true)
				if err != nil || len(report.Actions) != 0 {
					t.Fatalf("report=%+v err=%v", report, err)
				}

				var repairedTitle, repairedPayload string
				var repairedDeletedAt sql.NullString
				if err := s.db.QueryRow(`SELECT title, deleted_at FROM observations WHERE id = ?`, obs.ID).Scan(&repairedTitle, &repairedDeletedAt); err != nil {
					t.Fatalf("read source after repair: %v", err)
				}
				if err := s.db.QueryRow(`SELECT payload FROM sync_mutations WHERE seq = ?`, mutation.Seq).Scan(&repairedPayload); err != nil {
					t.Fatalf("read mutation after repair: %v", err)
				}
				if repairedTitle != title || repairedPayload != payload || repairedDeletedAt != deletedAt {
					t.Fatalf("ineligible repair mutated source title=%q payload=%q deleted_at=%v; want title=%q payload=%q deleted_at=%v", repairedTitle, repairedPayload, repairedDeletedAt, title, payload, deletedAt)
				}
			})
		}
	})

	t.Run("reports only actions from the successful retry attempt", func(t *testing.T) {
		s, _, _, _ := seed(t, "Recovered title. More detail.", func(map[string]json.RawMessage) {})
		oldBackoffs := sqliteWriteRetryBackoffs
		sqliteWriteRetryBackoffs = []time.Duration{0}
		t.Cleanup(func() { sqliteWriteRetryBackoffs = oldBackoffs })

		originalCommit := s.hooks.commit
		commitAttempts := 0
		s.hooks.commit = func(tx *sql.Tx) error {
			commitAttempts++
			if commitAttempts == 1 {
				return errors.New("database is locked")
			}
			return originalCommit(tx)
		}
		t.Cleanup(func() { s.hooks.commit = originalCommit })

		report, err := s.RepairObservationMutationTitles("project-a", false)
		if err != nil {
			t.Fatalf("repair after retry: %v", err)
		}
		if commitAttempts != 2 || len(report.Actions) != 1 {
			t.Fatalf("commit attempts=%d report=%+v", commitAttempts, report)
		}
	})

	t.Run("applies a Unicode blank source title", func(t *testing.T) {
		s, obs, _, _ := seed(t, "Recovered title. More detail.", func(map[string]json.RawMessage) {})
		if _, err := s.db.Exec(`UPDATE observations SET title = ? WHERE id = ?`, "\u2003", obs.ID); err != nil {
			t.Fatalf("seed Unicode blank title: %v", err)
		}
		report, err := s.RepairObservationMutationTitles("project-a", true)
		if err != nil || len(report.Actions) != 1 || report.Actions[0].Title != "Recovered title." {
			t.Fatalf("report=%+v err=%v", report, err)
		}
	})

	t.Run("repairs every pending upsert for one observation", func(t *testing.T) {
		s, obs, mutation, _ := seed(t, "Recovered title. More detail.", func(map[string]json.RawMessage) {})
		if err := s.EnrollProject("project-a"); err != nil {
			t.Fatalf("enroll project: %v", err)
		}
		result, err := s.db.Exec(`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project) VALUES (?, ?, ?, ?, ?, ?, ?)`, mutation.TargetKey, mutation.Entity, mutation.EntityKey, mutation.Op, mutation.Payload, mutation.Source, mutation.Project)
		if err != nil {
			t.Fatalf("insert second frozen payload: %v", err)
		}
		secondSeq, err := result.LastInsertId()
		if err != nil {
			t.Fatalf("read second sequence: %v", err)
		}
		plan, err := s.RepairObservationMutationTitles("project-a", false)
		if err != nil || len(plan.Actions) != 2 || plan.Actions[0].Seq != mutation.Seq || plan.Actions[1].Seq != secondSeq {
			t.Fatalf("plan=%+v err=%v", plan, err)
		}
		applied, err := s.RepairObservationMutationTitles("project-a", true)
		if err != nil || !reflect.DeepEqual(applied.Actions, plan.Actions) {
			t.Fatalf("apply=%+v plan=%+v err=%v", applied, plan, err)
		}
		pending, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 10)
		if err != nil {
			t.Fatalf("list pending mutations: %v", err)
		}
		var repaired []SyncMutation
		for _, pendingMutation := range pending {
			if pendingMutation.EntityKey == obs.SyncID {
				repaired = append(repaired, pendingMutation)
			}
		}
		if len(repaired) != 2 || repaired[0].Seq != mutation.Seq || repaired[1].Seq != secondSeq || ValidateSyncMutationPayload(repaired[0].Entity, repaired[0].Op, repaired[0].Payload, repaired[0].EntityKey).ReasonCode != "" || ValidateSyncMutationPayload(repaired[1].Entity, repaired[1].Op, repaired[1].Payload, repaired[1].EntityKey).ReasonCode != "" {
			t.Fatalf("repaired pending mutations=%+v", repaired)
		}
	})

	t.Run("rolls back both writes", func(t *testing.T) {
		s, obs, mutation, _ := seed(t, "Repair me.", func(map[string]json.RawMessage) {})
		if _, err := s.db.Exec(`CREATE TRIGGER reject_title_repair BEFORE UPDATE OF payload ON sync_mutations BEGIN SELECT RAISE(ABORT, 'payload blocked'); END`); err != nil {
			t.Fatalf("create trigger: %v", err)
		}
		if _, err := s.RepairObservationMutationTitles("project-a", true); err == nil {
			t.Fatal("expected repair failure")
		}
		var title, payload string
		if err := s.db.QueryRow(`SELECT title FROM observations WHERE id = ?`, obs.ID).Scan(&title); err != nil || title != "" {
			t.Fatalf("source rollback title=%q err=%v", title, err)
		}
		if err := s.db.QueryRow(`SELECT payload FROM sync_mutations WHERE seq = ?`, mutation.Seq).Scan(&payload); err != nil || !strings.Contains(payload, `"title":"  "`) {
			t.Fatalf("mutation rollback payload=%q err=%v", payload, err)
		}
	})
}

func TestDeleteSession_NotFound(t *testing.T) {
	s := newTestStore(t)

	err := s.DeleteSession("does-not-exist")
	if !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("expected ErrSessionNotFound, got: %v", err)
	}
}

func TestDeleteSession_HasActiveObservations(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("sess-has-obs", "proj", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := s.AddObservation(AddObservationParams{
		SessionID: "sess-has-obs",
		Type:      "decision",
		Title:     "some decision",
		Content:   "content",
		Project:   "proj",
		Scope:     "project",
	}); err != nil {
		t.Fatalf("add observation: %v", err)
	}

	err := s.DeleteSession("sess-has-obs")
	if !errors.Is(err, ErrSessionHasObservations) {
		t.Fatalf("expected ErrSessionHasObservations, got: %v", err)
	}
}

func TestDeleteSession_HasSoftDeletedObservations(t *testing.T) {
	// Even soft-deleted observations must block the session delete
	// to avoid FK constraint violations.
	s := newTestStore(t)

	if err := s.CreateSession("sess-soft", "proj", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	obsID, err := s.AddObservation(AddObservationParams{
		SessionID: "sess-soft",
		Type:      "decision",
		Title:     "soft deleted obs",
		Content:   "content",
		Project:   "proj",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}
	if err := s.DeleteObservation(obsID, false); err != nil {
		t.Fatalf("soft delete observation: %v", err)
	}

	err = s.DeleteSession("sess-soft")
	if !errors.Is(err, ErrSessionHasObservations) {
		t.Fatalf("expected ErrSessionHasObservations for soft-deleted obs, got: %v", err)
	}
}

func TestDeleteSession_DeletesPromptsAlso(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("sess-with-prompts", "proj", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := s.AddPrompt(AddPromptParams{
		SessionID: "sess-with-prompts",
		Content:   "a prompt",
		Project:   "proj",
	}); err != nil {
		t.Fatalf("add prompt: %v", err)
	}

	if err := s.DeleteSession("sess-with-prompts"); err != nil {
		t.Fatalf("delete session: %v", err)
	}

	prompts, err := s.RecentPrompts("proj", 10)
	if err != nil {
		t.Fatalf("recent prompts: %v", err)
	}
	if len(prompts) != 0 {
		t.Fatalf("expected prompts to be deleted with session, got %d", len(prompts))
	}
}

func TestDeleteSession_FKConstraintFallback(t *testing.T) {
	// Verify that a SQLite FK constraint error on the DELETE FROM sessions
	// statement is translated into ErrSessionHasObservations.
	//
	// SQLite is a single-writer database, so it is not possible to inject an
	// observation from a concurrent connection while the transaction already
	// holds the write lock. Instead we simulate the race by:
	//   1. Pre-inserting an observation directly (bypassing store logic).
	//   2. Mocking the queryIt hook so the COUNT query returns 0 (as if the
	//      observation arrived after the count).
	//   3. Letting DeleteSession proceed; the DELETE FROM sessions then fails
	//      with a real SQLite FK constraint error (SQLITE_CONSTRAINT_FOREIGNKEY).
	s := newTestStore(t)

	if err := s.CreateSession("sess-race", "proj", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Insert the observation directly, bypassing the store COUNT guard.
	if _, err := s.db.Exec(`
		INSERT INTO observations
			(session_id, type, title, content, project, scope, created_at, updated_at, sync_id, duplicate_count, revision_count)
		VALUES
			('sess-race', 'decision', 'race obs', 'content', 'proj', 'project',
			 datetime('now'), datetime('now'), 'sync-race-1', 1, 1)`); err != nil {
		t.Fatalf("pre-insert observation: %v", err)
	}

	// Mock queryIt so the COUNT returns 0, simulating the race window where the
	// observation did not exist when the count ran.
	origQueryIt := s.hooks.queryIt
	faked := false
	s.hooks.queryIt = func(db queryer, query string, args ...any) (rowScanner, error) {
		if !faked && strings.Contains(query, "COUNT(*)") && strings.Contains(query, "observations WHERE session_id") {
			faked = true
			// Return a scanner that always yields count = 0.
			return &fakeCountScanner{}, nil
		}
		return origQueryIt(db, query, args...)
	}
	defer func() { s.hooks = defaultStoreHooks() }()

	err := s.DeleteSession("sess-race")
	if !errors.Is(err, ErrSessionHasObservations) {
		t.Fatalf("expected ErrSessionHasObservations from FK constraint, got: %v", err)
	}
}

// fakeCountScanner is a rowScanner that yields a single row with value 0,
// used to simulate a COUNT(*) result of zero.
type fakeCountScanner struct {
	done bool
}

func (f *fakeCountScanner) Next() bool {
	if f.done {
		return false
	}
	f.done = true
	return true
}
func (f *fakeCountScanner) Scan(dest ...any) error {
	if len(dest) > 0 {
		if p, ok := dest[0].(*int); ok {
			*p = 0
		}
	}
	return nil
}
func (f *fakeCountScanner) Err() error   { return nil }
func (f *fakeCountScanner) Close() error { return nil }

// ─── DeletePrompt tests ──────────────────────────────────────────────────────

func TestDeletePrompt_Success(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("sess-p", "proj", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	id, err := s.AddPrompt(AddPromptParams{
		SessionID: "sess-p",
		Content:   "delete me",
		Project:   "proj",
	})
	if err != nil {
		t.Fatalf("add prompt: %v", err)
	}

	if err := s.DeletePrompt(id); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	prompts, err := s.RecentPrompts("proj", 10)
	if err != nil {
		t.Fatalf("recent prompts: %v", err)
	}
	if len(prompts) != 0 {
		t.Fatalf("expected prompt to be deleted, got %d", len(prompts))
	}
}

func TestDeletePrompt_NotFound(t *testing.T) {
	s := newTestStore(t)

	err := s.DeletePrompt(999999)
	if !errors.Is(err, ErrPromptNotFound) {
		t.Fatalf("expected ErrPromptNotFound, got: %v", err)
	}
}

// ─── ProjectExists tests (Batch 2 — REQ-315) ─────────────────────────────────

func TestProjectExists_EmptyStore(t *testing.T) {
	s := newTestStore(t)

	exists, err := s.ProjectExists("any-project")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exists {
		t.Error("expected false on empty store")
	}
}

func TestProjectExists_Known(t *testing.T) {
	s := newTestStore(t)

	// Insert an observation for the target project.
	if err := s.CreateSession("sess-1", "my-project", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	_, err := s.AddObservation(AddObservationParams{
		SessionID: "sess-1",
		Type:      "manual",
		Title:     "test",
		Content:   "test content",
		Project:   "my-project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}

	exists, err := s.ProjectExists("my-project")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !exists {
		t.Error("expected true for known project with observation")
	}
}

func TestProjectExists_KnownViaSession(t *testing.T) {
	s := newTestStore(t)

	// Only a session, no observations.
	if err := s.CreateSession("sess-only", "session-only-project", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	exists, err := s.ProjectExists("session-only-project")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !exists {
		t.Error("expected true for project with a session only")
	}
}

func TestProjectExists_KnownViaPrompt(t *testing.T) {
	s := newTestStore(t)

	// Only a prompt, no session or observation.
	if err := s.CreateSession("sess-prompt", "prompt-only-project", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	_, err := s.AddPrompt(AddPromptParams{
		SessionID: "sess-prompt",
		Content:   "what is this?",
		Project:   "prompt-only-project",
	})
	if err != nil {
		t.Fatalf("add prompt: %v", err)
	}

	exists, err := s.ProjectExists("prompt-only-project")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !exists {
		t.Error("expected true for project with a prompt only")
	}
}

func TestProjectExists_Unknown(t *testing.T) {
	s := newTestStore(t)

	// Populate with a different project.
	if err := s.CreateSession("sess-other", "other-project", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	_, err := s.AddObservation(AddObservationParams{
		SessionID: "sess-other",
		Type:      "manual",
		Title:     "other",
		Content:   "other content",
		Project:   "other-project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}

	exists, err := s.ProjectExists("does-not-exist")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exists {
		t.Error("expected false for unknown project in populated store")
	}
}

// TestProjectExists_KnownViaEnrollmentOnly: a project enrolled via EnrollProject()
// with no observations/sessions/prompts must still be found by ProjectExists (JC1).
func TestProjectExists_KnownViaEnrollmentOnly(t *testing.T) {
	s := newTestStore(t)

	// Enroll a project — no observations, sessions, or prompts.
	if err := s.EnrollProject("enrolled-only-project"); err != nil {
		t.Fatalf("EnrollProject: %v", err)
	}

	exists, err := s.ProjectExists("enrolled-only-project")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !exists {
		t.Error("enrolled-only-project must be found via sync_enrolled_projects UNION ALL branch")
	}
}

// ─── Doctor diagnostic helpers ───────────────────────────────────────────────

func TestListDiagnosticSessionsScopesByProject(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("manual-save-engram", "engram", "/work/engram"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := s.CreateSession("manual-save-other", "other", "/work/other"); err != nil {
		t.Fatalf("CreateSession other: %v", err)
	}
	sessions, err := s.ListDiagnosticSessions("engram")
	if err != nil {
		t.Fatalf("ListDiagnosticSessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0].ID != "manual-save-engram" || sessions[0].Name != "manual-save-engram" {
		t.Fatalf("sessions=%+v", sessions)
	}
}

func TestListPendingProjectMutationsAndPayloadValidation(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.db.Exec(`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project) VALUES (?, ?, ?, ?, ?, ?, ?)`, DefaultSyncTargetKey, SyncEntityObservation, "obs-1", SyncOpUpsert, `{"sync_id":"obs-1"}`, SyncSourceLocal, "engram"); err != nil {
		t.Fatalf("insert pending mutation: %v", err)
	}
	mutations, err := s.ListPendingProjectMutations("engram")
	if err != nil {
		t.Fatalf("ListPendingProjectMutations: %v", err)
	}
	if len(mutations) != 1 {
		t.Fatalf("mutations=%+v", mutations)
	}
	validation := ValidateSyncMutationPayload(mutations[0].Entity, mutations[0].Op, mutations[0].Payload, mutations[0].EntityKey)
	if validation.ReasonCode != "sync_mutation_payload_missing_required_fields" {
		t.Fatalf("validation=%+v", validation)
	}
	if strings.Join(validation.MissingFields, ",") != "session_id,type,title,content,scope" {
		t.Fatalf("missing fields=%v", validation.MissingFields)
	}
}

func TestValidateSyncMutationPayloadRelationRequiresServerFields(t *testing.T) {
	payload := `{"sync_id":"rel-1","source_id":"obs-a","target_id":"obs-b","relation":"conflicts_with","judgment_status":"judged","project":"engram"}`
	validation := ValidateSyncMutationPayload(SyncEntityRelation, SyncOpUpsert, payload, "rel-1")
	if validation.ReasonCode != "sync_mutation_payload_missing_required_fields" {
		t.Fatalf("validation=%+v", validation)
	}
	if strings.Join(validation.MissingFields, ",") != "marked_by_actor,marked_by_kind" {
		t.Fatalf("missing fields=%v", validation.MissingFields)
	}
}

func TestValidateSyncMutationPayloadSessionIdentity(t *testing.T) {
	tests := []struct {
		name        string
		payload     string
		entityKey   string
		wantReason  string
		wantMissing string
	}{
		{name: "blank payload ID", payload: `{"id":" \t","directory":"/tmp"}`, entityKey: "session", wantReason: "sync_mutation_payload_missing_required_fields", wantMissing: "id"},
		{name: "blank entity key", payload: `{"id":"session","directory":"/tmp"}`, entityKey: " \t", wantReason: "sync_mutation_payload_missing_required_fields", wantMissing: "entity_key"},
		{name: "opaque mismatch", payload: `{"id":" session ","directory":"/tmp"}`, entityKey: "session", wantReason: "sync_session_mutation_identity_mismatch"},
		{name: "opaque exact match", payload: `{"id":" session ","directory":"/tmp"}`, entityKey: " session "},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			validation := ValidateSyncMutationPayload(SyncEntitySession, SyncOpUpsert, tc.payload, tc.entityKey)
			if validation.ReasonCode != tc.wantReason || strings.Join(validation.MissingFields, ",") != tc.wantMissing {
				t.Fatalf("validation=%+v want reason=%q missing=%q", validation, tc.wantReason, tc.wantMissing)
			}
		})
	}
}

func TestValidateSyncMutationPayloadTreatsWhitespaceEntityKeysAsMissing(t *testing.T) {
	for _, entity := range []string{SyncEntityObservation, SyncEntityPrompt} {
		t.Run(entity, func(t *testing.T) {
			validation := ValidateSyncMutationPayload(entity, SyncOpUpsert, `{"sync_id":"","session_id":"s","type":"decision","title":"title","content":"content","scope":"project"}`, " \t")
			if strings.Join(validation.MissingFields, ",") != "sync_id" || validation.EntityKey != " \t" {
				t.Fatalf("validation=%+v", validation)
			}
			valid := ValidateSyncMutationPayload(entity, SyncOpUpsert, `{"sync_id":"","session_id":"s","type":"decision","title":"title","content":"content","scope":"project"}`, " opaque ")
			if len(valid.MissingFields) != 0 {
				t.Fatalf("opaque key validation=%+v", valid)
			}
		})
	}
}

func TestReadSQLiteLockSnapshotDoesNotMutateApplicationRows(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s1", "engram", "/work/engram"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	var before int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&before); err != nil {
		t.Fatalf("count before: %v", err)
	}
	snapshot, err := s.ReadSQLiteLockSnapshot(context.Background())
	if err != nil {
		t.Fatalf("ReadSQLiteLockSnapshot: %v", err)
	}
	var after int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&after); err != nil {
		t.Fatalf("count after: %v", err)
	}
	if before != after {
		t.Fatalf("session count changed: before=%d after=%d", before, after)
	}
	if snapshot.JournalMode == "" || snapshot.BusyTimeoutMS <= 0 {
		t.Fatalf("snapshot=%+v", snapshot)
	}
}

// newTestStoreRaw creates a store that runs migrations but skips the startup repair,
// allowing tests to seed data and call repairEnrolledProjectSyncMutations themselves.
func newTestStoreRaw(t *testing.T) *Store {
	t.Helper()
	cfg := mustDefaultConfig(t)
	cfg.DataDir = t.TempDir()
	cfg.DedupeWindow = time.Hour

	s, err := newWithoutRepair(cfg)
	if err != nil {
		t.Fatalf("new raw store: %v", err)
	}
	t.Cleanup(func() {
		_ = s.Close()
	})
	return s
}

func TestEnsureEnrolledProjectSyncMutationsRetriesAfterFailure(t *testing.T) {
	s := newTestStore(t)

	calls := 0
	s.repairOperation = func() error {
		calls++
		if calls == 1 {
			return errors.New("repair failed")
		}
		return nil
	}

	if err := s.EnsureEnrolledProjectSyncMutations(context.Background()); err == nil || !strings.Contains(err.Error(), "repair failed") {
		t.Fatalf("first ensure error = %v, want repair failure", err)
	}
	if err := s.EnsureEnrolledProjectSyncMutations(context.Background()); err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if err := s.EnsureEnrolledProjectSyncMutations(context.Background()); err != nil {
		t.Fatalf("memoized ensure: %v", err)
	}
	if calls != 2 {
		t.Fatalf("repair calls = %d, want 2 (failed attempt plus successful retry)", calls)
	}
}

func TestEnsureEnrolledProjectSyncMutationsCanceledLeaderSkipsRepair(t *testing.T) {
	s := newTestStore(t)

	calls := 0
	s.repairOperation = func() error {
		calls++
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := s.EnsureEnrolledProjectSyncMutations(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled leader error = %v, want context.Canceled", err)
	}
	if calls != 0 {
		t.Fatalf("repair calls = %d, want 0", calls)
	}
}

func TestEnsureEnrolledProjectSyncMutationsSharesConcurrentFirstRepair(t *testing.T) {
	s := newTestStore(t)

	started := make(chan struct{})
	release := make(chan struct{})
	calls := 0
	s.repairOperation = func() error {
		calls++
		close(started)
		<-release
		return nil
	}

	const callers = 8
	start := make(chan struct{})
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- s.EnsureEnrolledProjectSyncMutations(context.Background())
		}()
	}
	close(start)
	<-started
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent ensure: %v", err)
		}
	}
	if calls != 1 {
		t.Fatalf("repair calls = %d, want exactly one", calls)
	}
}

func TestEnsureEnrolledProjectSyncMutationsWaiterReceivesSuccessfulInFlightResult(t *testing.T) {
	s := newTestStore(t)
	inFlight := &enrolledProjectRepair{done: make(chan struct{})}

	s.repairMu.Lock()
	s.repairInFlight = inFlight
	s.repairMu.Unlock()

	result := make(chan error, 1)
	go func() { result <- s.EnsureEnrolledProjectSyncMutations(context.Background()) }()

	s.repairMu.Lock()
	close(inFlight.done)
	s.repairMu.Unlock()
	if err := <-result; err != nil {
		t.Fatalf("successful waiter error = %v, want nil", err)
	}
}

func TestEnsureEnrolledProjectSyncMutationsWaiterCanCancel(t *testing.T) {
	s := newTestStore(t)

	started := make(chan struct{})
	release := make(chan struct{})
	s.repairOperation = func() error {
		close(started)
		<-release
		return nil
	}
	leaderDone := make(chan error, 1)
	go func() { leaderDone <- s.EnsureEnrolledProjectSyncMutations(context.Background()) }()
	<-started

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.EnsureEnrolledProjectSyncMutations(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled waiter error = %v, want context.Canceled", err)
	}
	close(release)
	if err := <-leaderDone; err != nil {
		t.Fatalf("leader ensure: %v", err)
	}
}

// countSyncMutations returns the total number of rows in sync_mutations for a project.
func countSyncMutations(t *testing.T, s *Store, project string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM sync_mutations WHERE project = ? AND source = ?`,
		project, SyncSourceLocal,
	).Scan(&n); err != nil {
		t.Fatalf("count sync_mutations: %v", err)
	}
	return n
}

// TestRepairIsIdempotentOnAlreadyRepairedStore verifies that calling
// repairEnrolledProjectSyncMutations on a store where all sessions/observations/prompts
// already have corresponding sync_mutations is a no-op (no new mutations added).
func TestRepairIsIdempotentOnAlreadyRepairedStore(t *testing.T) {
	s := newTestStoreRaw(t)

	// Enroll so repair considers this project.
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO sync_enrolled_projects (project) VALUES (?)`, "engram"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}

	// Create a session and observation via the normal API (which also enqueues mutations).
	if err := s.CreateSession("s-repair-1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := s.AddObservation(AddObservationParams{
		SessionID: "s-repair-1",
		Type:      "decision",
		Title:     "Title",
		Content:   "Content",
		Project:   "engram",
		Scope:     "project",
	}); err != nil {
		t.Fatalf("add observation: %v", err)
	}
	if _, err := s.AddPrompt(AddPromptParams{
		SessionID: "s-repair-1",
		Content:   "a prompt",
		Project:   "engram",
	}); err != nil {
		t.Fatalf("add prompt: %v", err)
	}

	// At this point mutations exist for session + observation + prompt.
	before := countSyncMutations(t, s, "engram")
	if before == 0 {
		t.Fatalf("expected mutations to exist before repair; got 0")
	}

	// Run repair — must be idempotent.
	start := time.Now()
	if err := s.repairEnrolledProjectSyncMutations(); err != nil {
		t.Fatalf("repair: %v", err)
	}
	elapsed := time.Since(start)

	after := countSyncMutations(t, s, "engram")
	if after != before {
		t.Fatalf("repair added mutations on an already-repaired store: before=%d after=%d", before, after)
	}
	// Must complete quickly — no busy loop.
	if elapsed > 2*time.Second {
		t.Fatalf("repair took too long (%v); possible busy loop", elapsed)
	}
}

// TestRepairBackfillsMissingMutations verifies that sessions/observations/prompts
// that exist without corresponding sync_mutations are backfilled by repair.
func TestRepairBackfillsMissingMutations(t *testing.T) {
	s := newTestStoreRaw(t)

	// Enroll project.
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO sync_enrolled_projects (project) VALUES (?)`, "engram"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}

	// Insert a session directly (bypasses mutation enqueue).
	if _, err := s.db.Exec(
		`INSERT INTO sessions (id, project, directory, started_at) VALUES (?, ?, ?, datetime('now'))`,
		"s-missing", "engram", "/tmp/engram",
	); err != nil {
		t.Fatalf("insert session: %v", err)
	}

	// Insert an observation directly (bypasses mutation enqueue).
	obsSync := "obs-sync-uuid-001"
	if _, err := s.db.Exec(`
		INSERT INTO observations (session_id, type, title, content, project, scope, normalized_hash,
		                          revision_count, duplicate_count, last_seen_at, updated_at, sync_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, 1, 1, datetime('now'), datetime('now'), ?)`,
		"s-missing", "decision", "T", "C", "engram", "project", hashNormalized("C"), obsSync,
	); err != nil {
		t.Fatalf("insert observation: %v", err)
	}

	// Insert a prompt directly (bypasses mutation enqueue).
	promptSync := "prompt-sync-uuid-001"
	if _, err := s.db.Exec(`
		INSERT INTO user_prompts (session_id, content, project, created_at, sync_id)
		VALUES (?, ?, ?, datetime('now'), ?)`,
		"s-missing", "hello", "engram", promptSync,
	); err != nil {
		t.Fatalf("insert prompt: %v", err)
	}

	// Confirm no mutations exist yet.
	before := countSyncMutations(t, s, "engram")
	if before != 0 {
		t.Fatalf("expected 0 mutations before repair, got %d", before)
	}

	// Run repair.
	if err := s.repairEnrolledProjectSyncMutations(); err != nil {
		t.Fatalf("repair: %v", err)
	}

	// Mutations must now exist for session, observation, and prompt.
	after := countSyncMutations(t, s, "engram")
	if after == 0 {
		t.Fatalf("repair did not backfill any mutations; expected >=3, got 0")
	}

	// Session mutation must exist.
	var sessionMutCount int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM sync_mutations WHERE entity = ? AND entity_key = ? AND source = ?`,
		SyncEntitySession, "s-missing", SyncSourceLocal,
	).Scan(&sessionMutCount); err != nil {
		t.Fatalf("count session mutations: %v", err)
	}
	if sessionMutCount == 0 {
		t.Fatalf("expected session mutation to be backfilled, got 0")
	}

	// Observation mutation must exist.
	var obsMutCount int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM sync_mutations WHERE entity = ? AND entity_key = ? AND source = ?`,
		SyncEntityObservation, obsSync, SyncSourceLocal,
	).Scan(&obsMutCount); err != nil {
		t.Fatalf("count observation mutations: %v", err)
	}
	if obsMutCount == 0 {
		t.Fatalf("expected observation mutation to be backfilled, got 0")
	}

	// Prompt mutation must exist.
	var promptMutCount int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM sync_mutations WHERE entity = ? AND entity_key = ? AND source = ?`,
		SyncEntityPrompt, promptSync, SyncSourceLocal,
	).Scan(&promptMutCount); err != nil {
		t.Fatalf("count prompt mutations: %v", err)
	}
	if promptMutCount == 0 {
		t.Fatalf("expected prompt mutation to be backfilled, got 0")
	}
}

// TestRepairDoesNotDeadlockWithCursorAndInsert verifies that repair handles
// 100 sessions without mutations correctly — no deadlock, cursor-insert interference,
// or busy loop.
func TestRepairDoesNotDeadlockWithCursorAndInsert(t *testing.T) {
	s := newTestStoreRaw(t)

	// Enroll project.
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO sync_enrolled_projects (project) VALUES (?)`, "engram"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}

	// Insert 100 sessions directly (no mutations).
	for i := 0; i < 100; i++ {
		id := fmt.Sprintf("session-%03d", i)
		if _, err := s.db.Exec(
			`INSERT INTO sessions (id, project, directory, started_at) VALUES (?, ?, ?, datetime('now'))`,
			id, "engram", "/tmp/engram",
		); err != nil {
			t.Fatalf("insert session %s: %v", id, err)
		}
	}

	before := countSyncMutations(t, s, "engram")
	if before != 0 {
		t.Fatalf("expected 0 mutations before repair, got %d", before)
	}

	start := time.Now()
	if err := s.repairEnrolledProjectSyncMutations(); err != nil {
		t.Fatalf("repair: %v", err)
	}
	elapsed := time.Since(start)

	after := countSyncMutations(t, s, "engram")
	if after != 100 {
		t.Fatalf("expected 100 session mutations after repair, got %d", after)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("repair took too long (%v) for 100 sessions; possible deadlock or busy loop", elapsed)
	}
}

// TestRepairHandlesMixedState verifies that repair only backfills what is missing:
// project A is fully repaired, project B is partially repaired, project C has nothing.
func TestRepairHandlesMixedState(t *testing.T) {
	s := newTestStoreRaw(t)

	// Enroll 3 projects.
	for _, p := range []string{"proj-a", "proj-b", "proj-c"} {
		if _, err := s.db.Exec(`INSERT OR IGNORE INTO sync_enrolled_projects (project) VALUES (?)`, p); err != nil {
			t.Fatalf("enroll %s: %v", p, err)
		}
	}

	// proj-a: 2 sessions inserted directly, then manually enqueue mutations (fully repaired).
	for i := 0; i < 2; i++ {
		id := fmt.Sprintf("a-sess-%d", i)
		if _, err := s.db.Exec(
			`INSERT INTO sessions (id, project, directory, started_at) VALUES (?, ?, ?, datetime('now'))`,
			id, "proj-a", "/tmp/a",
		); err != nil {
			t.Fatalf("insert proj-a session: %v", err)
		}
	}
	// For proj-a: simulate already-repaired by creating sync_state + mutations directly.
	if _, err := s.db.Exec(
		`INSERT OR IGNORE INTO sync_state (target_key, lifecycle, updated_at) VALUES (?, ?, datetime('now'))`,
		DefaultSyncTargetKey, SyncLifecycleIdle,
	); err != nil {
		t.Fatalf("insert sync_state: %v", err)
	}
	for i := 0; i < 2; i++ {
		id := fmt.Sprintf("a-sess-%d", i)
		if _, err := s.db.Exec(
			`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			DefaultSyncTargetKey, SyncEntitySession, id, SyncOpUpsert, `{}`, SyncSourceLocal, "proj-a",
		); err != nil {
			t.Fatalf("insert proj-a mutation: %v", err)
		}
	}

	// proj-b: 3 sessions, only 1 has a mutation (partial).
	for i := 0; i < 3; i++ {
		id := fmt.Sprintf("b-sess-%d", i)
		if _, err := s.db.Exec(
			`INSERT INTO sessions (id, project, directory, started_at) VALUES (?, ?, ?, datetime('now'))`,
			id, "proj-b", "/tmp/b",
		); err != nil {
			t.Fatalf("insert proj-b session: %v", err)
		}
	}
	// Only b-sess-0 has a mutation.
	if _, err := s.db.Exec(
		`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		DefaultSyncTargetKey, SyncEntitySession, "b-sess-0", SyncOpUpsert, `{}`, SyncSourceLocal, "proj-b",
	); err != nil {
		t.Fatalf("insert proj-b partial mutation: %v", err)
	}

	// proj-c: 2 sessions, no mutations.
	for i := 0; i < 2; i++ {
		id := fmt.Sprintf("c-sess-%d", i)
		if _, err := s.db.Exec(
			`INSERT INTO sessions (id, project, directory, started_at) VALUES (?, ?, ?, datetime('now'))`,
			id, "proj-c", "/tmp/c",
		); err != nil {
			t.Fatalf("insert proj-c session: %v", err)
		}
	}

	// Snapshot counts before repair.
	beforeA := countSyncMutations(t, s, "proj-a")
	beforeB := countSyncMutations(t, s, "proj-b")
	beforeC := countSyncMutations(t, s, "proj-c")

	if beforeA != 2 {
		t.Fatalf("proj-a: expected 2 mutations before repair, got %d", beforeA)
	}
	if beforeB != 1 {
		t.Fatalf("proj-b: expected 1 mutation before repair, got %d", beforeB)
	}
	if beforeC != 0 {
		t.Fatalf("proj-c: expected 0 mutations before repair, got %d", beforeC)
	}

	// Run repair.
	if err := s.repairEnrolledProjectSyncMutations(); err != nil {
		t.Fatalf("repair: %v", err)
	}

	afterA := countSyncMutations(t, s, "proj-a")
	afterB := countSyncMutations(t, s, "proj-b")
	afterC := countSyncMutations(t, s, "proj-c")

	// proj-a: fully repaired — count must not change.
	if afterA != beforeA {
		t.Fatalf("proj-a: repair must not add mutations to fully repaired project: before=%d after=%d", beforeA, afterA)
	}
	// proj-b: was missing 2 sessions — must now have 3 total.
	if afterB != 3 {
		t.Fatalf("proj-b: expected 3 mutations after repair (1 existing + 2 backfilled), got %d", afterB)
	}
	// proj-c: was missing all 2 sessions — must now have 2.
	if afterC != 2 {
		t.Fatalf("proj-c: expected 2 mutations after repair, got %d", afterC)
	}
}

// ---------------------------------------------------------------------------
// Phase F — Decay defaults wiring (REQ-006)
// ---------------------------------------------------------------------------

// queryReviewAfter returns the review_after and expires_at for a given observation ID.
// Returns ("", false) for review_after if NULL, ("", false) for expires_at if NULL.
func queryDecayFields(t *testing.T, s *Store, obsID int64) (reviewAfter string, reviewAfterNull bool, expiresAt string, expiresAtNull bool) {
	t.Helper()
	var ra, ea sql.NullString
	if err := s.db.QueryRow(
		`SELECT review_after, expires_at FROM observations WHERE id = ?`, obsID,
	).Scan(&ra, &ea); err != nil {
		t.Fatalf("queryDecayFields: %v", err)
	}
	return ra.String, !ra.Valid, ea.String, !ea.Valid
}

// withinDays asserts that parsed is within ±days of expected.
func withinDays(t *testing.T, label, value string, expected time.Time, days int) {
	t.Helper()
	parsed, err := time.Parse("2006-01-02 15:04:05", value)
	if err != nil {
		// try RFC3339 as fallback
		parsed, err = time.Parse(time.RFC3339, value)
		if err != nil {
			t.Fatalf("withinDays: %s: cannot parse %q: %v", label, value, err)
		}
	}
	delta := parsed.Sub(expected)
	if delta < 0 {
		delta = -delta
	}
	maxDelta := time.Duration(days) * 24 * time.Hour
	if delta > maxDelta {
		t.Fatalf("withinDays: %s: got %s, want ~%s (±%dd), delta=%s",
			label, parsed.Format(time.RFC3339), expected.Format(time.RFC3339), days, delta)
	}
}

// TestAddObservation_DecayDefaults verifies that AddObservation populates
// review_after for known types (decision, policy, preference) and leaves it
// NULL for unknown/unlisted types. expires_at is NULL for all types Phase 1.
func TestAddObservation_DecayDefaults(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("decay-sess", "decay-proj", "/tmp/decay"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	now := time.Now().UTC()

	cases := []struct {
		obsType          string
		wantReviewNull   bool
		wantMonthsOffset int
	}{
		{"decision", false, 6},
		{"policy", false, 12},
		{"preference", false, 3},
		{"observation", true, 0},
		{"manual", true, 0},
		{"bugfix", true, 0},
		{"architecture", true, 0},
		{"", true, 0},
	}

	for _, tc := range cases {
		tc := tc
		t.Run("type="+tc.obsType, func(t *testing.T) {
			obsType := tc.obsType
			if obsType == "" {
				obsType = "manual" // AddObservation requires non-empty type; test blank separately
			}
			title := "Decay test — " + tc.obsType + " — " + time.Now().Format(time.RFC3339Nano)
			id, err := s.AddObservation(AddObservationParams{
				SessionID: "decay-sess",
				Type:      obsType,
				Title:     title,
				Content:   "decay defaults test content",
				Project:   "decay-proj",
				Scope:     "project",
			})
			if err != nil {
				t.Fatalf("AddObservation: %v", err)
			}

			ra, raNull, _, eaNull := queryDecayFields(t, s, id)

			// expires_at MUST always be NULL (Phase 1)
			if !eaNull {
				t.Errorf("type=%s: expected expires_at=NULL, got non-NULL", tc.obsType)
			}

			if tc.wantReviewNull {
				if !raNull {
					t.Errorf("type=%s: expected review_after=NULL, got %q", tc.obsType, ra)
				}
				return
			}

			// For types with a decay offset, review_after must be ~N months from now.
			if raNull {
				t.Fatalf("type=%s: expected review_after to be set, got NULL", tc.obsType)
			}
			expected := now.AddDate(0, tc.wantMonthsOffset, 0)
			withinDays(t, "review_after type="+tc.obsType, ra, expected, 2)
		})
	}
}

// TestAddObservation_DecayNotAppliedToExistingRows verifies that topic_key
// revisions and deduplication do NOT overwrite review_after on existing rows.
func TestAddObservation_DecayNotAppliedToExistingRows(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("decay-rev-sess", "decay-rev-proj", "/tmp/decay-rev"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Insert a decision observation via topic_key so revision path is exercised.
	firstID, err := s.AddObservation(AddObservationParams{
		SessionID: "decay-rev-sess",
		Type:      "decision",
		Title:     "Architecture: use SQLite",
		Content:   "We chose SQLite as the primary store.",
		Project:   "decay-rev-proj",
		Scope:     "project",
		TopicKey:  "arch/db-choice",
	})
	if err != nil {
		t.Fatalf("AddObservation first: %v", err)
	}

	ra1, ra1Null, _, _ := queryDecayFields(t, s, firstID)
	if ra1Null {
		t.Fatalf("first insert: expected review_after to be populated for 'decision', got NULL")
	}

	// Revise via same topic_key — this hits the UPDATE path, NOT a new insert.
	secondID, err := s.AddObservation(AddObservationParams{
		SessionID: "decay-rev-sess",
		Type:      "decision",
		Title:     "Architecture: use SQLite (revised)",
		Content:   "Confirmed: SQLite is the right choice.",
		Project:   "decay-rev-proj",
		Scope:     "project",
		TopicKey:  "arch/db-choice",
	})
	if err != nil {
		t.Fatalf("AddObservation revision: %v", err)
	}

	// Topic_key revision should return same row ID.
	if firstID != secondID {
		t.Fatalf("expected topic_key revision to return same ID, got %d vs %d", firstID, secondID)
	}

	ra2, _, _, _ := queryDecayFields(t, s, firstID)

	// review_after MUST NOT have been updated by the revision (original value preserved).
	if ra1 != ra2 {
		t.Errorf("revision must not overwrite review_after: was %q, now %q", ra1, ra2)
	}
}

func TestObservationState(t *testing.T) {
	future := time.Now().UTC().Add(time.Hour).Format("2006-01-02 15:04:05")
	past := time.Now().UTC().Add(-time.Hour).Format("2006-01-02 15:04:05")

	if got := (Observation{}).State(); got != ObservationStateActive {
		t.Fatalf("nil review_after state = %q, want active", got)
	}
	if got := (Observation{ReviewAfter: &future}).State(); got != ObservationStateActive {
		t.Fatalf("future review_after state = %q, want active", got)
	}
	if got := (Observation{ReviewAfter: &past}).State(); got != ObservationStateNeedsReview {
		t.Fatalf("past review_after state = %q, want needs_review", got)
	}
}

func TestObservationsNeedingReview(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("review-sess", "review-proj", "/tmp/review"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	staleID, err := s.AddObservation(AddObservationParams{SessionID: "review-sess", Type: "decision", Title: "stale", Content: "stale content", Project: "review-proj"})
	if err != nil {
		t.Fatalf("add stale: %v", err)
	}
	futureID, err := s.AddObservation(AddObservationParams{SessionID: "review-sess", Type: "decision", Title: "future", Content: "future content", Project: "review-proj"})
	if err != nil {
		t.Fatalf("add future: %v", err)
	}
	otherID, err := s.AddObservation(AddObservationParams{SessionID: "review-sess", Type: "decision", Title: "other", Content: "other content", Project: "other-proj"})
	if err != nil {
		t.Fatalf("add other: %v", err)
	}

	past := time.Now().UTC().Add(-24 * time.Hour).Format("2006-01-02 15:04:05")
	future := time.Now().UTC().Add(24 * time.Hour).Format("2006-01-02 15:04:05")
	if _, err := s.db.Exec(`UPDATE observations SET review_after = ? WHERE id IN (?, ?)`, past, staleID, otherID); err != nil {
		t.Fatalf("backdate review_after: %v", err)
	}
	if _, err := s.db.Exec(`UPDATE observations SET review_after = ? WHERE id = ?`, future, futureID); err != nil {
		t.Fatalf("future review_after: %v", err)
	}

	got, err := s.ObservationsNeedingReview("review-proj", 10)
	if err != nil {
		t.Fatalf("ObservationsNeedingReview(project): %v", err)
	}
	if len(got) != 1 || got[0].ID != staleID || got[0].State() != ObservationStateNeedsReview {
		t.Fatalf("project review list = %#v, want only staleID", got)
	}

	all, err := s.ObservationsNeedingReview("", 10)
	if err != nil {
		t.Fatalf("ObservationsNeedingReview(all): %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("all review list len = %d, want 2: %#v", len(all), all)
	}
}

func TestObservationsNeedingReviewExcludesDeletedObservations(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("review-deleted-sess", "review-deleted-proj", "/tmp/review"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	activeID, err := s.AddObservation(AddObservationParams{SessionID: "review-deleted-sess", Type: "decision", Title: "active", Content: "active content", Project: "review-deleted-proj"})
	if err != nil {
		t.Fatalf("add active: %v", err)
	}
	deletedID, err := s.AddObservation(AddObservationParams{SessionID: "review-deleted-sess", Type: "decision", Title: "deleted", Content: "deleted content", Project: "review-deleted-proj"})
	if err != nil {
		t.Fatalf("add deleted: %v", err)
	}
	past := time.Now().UTC().Add(-24 * time.Hour).Format("2006-01-02 15:04:05")
	if _, err := s.db.Exec(`UPDATE observations SET review_after = ? WHERE id IN (?, ?)`, past, activeID, deletedID); err != nil {
		t.Fatalf("backdate review_after: %v", err)
	}
	if err := s.DeleteObservation(deletedID, false); err != nil {
		t.Fatalf("delete observation: %v", err)
	}

	got, err := s.ObservationsNeedingReview("review-deleted-proj", 10)
	if err != nil {
		t.Fatalf("ObservationsNeedingReview(project): %v", err)
	}
	if len(got) != 1 || got[0].ID != activeID {
		t.Fatalf("review list = %#v, want only activeID", got)
	}
}

func TestMarkReviewedResetsReviewAfter(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("mark-reviewed-sess", "mark-reviewed-proj", "/tmp/review"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	decisionID, err := s.AddObservation(AddObservationParams{SessionID: "mark-reviewed-sess", Type: "decision", Title: "decision", Content: "decision content", Project: "mark-reviewed-proj"})
	if err != nil {
		t.Fatalf("add decision: %v", err)
	}
	manualID, err := s.AddObservation(AddObservationParams{SessionID: "mark-reviewed-sess", Type: "manual", Title: "manual", Content: "manual content", Project: "mark-reviewed-proj"})
	if err != nil {
		t.Fatalf("add manual: %v", err)
	}
	past := time.Now().UTC().Add(-24 * time.Hour).Format("2006-01-02 15:04:05")
	if _, err := s.db.Exec(`UPDATE observations SET review_after = ? WHERE id IN (?, ?)`, past, decisionID, manualID); err != nil {
		t.Fatalf("backdate review_after: %v", err)
	}

	start := time.Now().UTC()
	if err := s.MarkReviewed(decisionID); err != nil {
		t.Fatalf("MarkReviewed decision: %v", err)
	}
	reviewAfter, reviewNull, _, _ := queryDecayFields(t, s, decisionID)
	if reviewNull {
		t.Fatal("decision review_after should be reset, got NULL")
	}
	withinDays(t, "mark reviewed decision", reviewAfter, start.AddDate(0, decayDecisionMonths, 0), 2)
	obs, err := s.GetObservation(decisionID)
	if err != nil {
		t.Fatalf("GetObservation decision: %v", err)
	}
	if obs.State() != ObservationStateActive {
		t.Fatalf("reviewed decision state = %q, want active", obs.State())
	}

	if err := s.MarkReviewed(manualID); err != nil {
		t.Fatalf("MarkReviewed manual: %v", err)
	}
	_, manualReviewNull, _, _ := queryDecayFields(t, s, manualID)
	if !manualReviewNull {
		t.Fatal("manual review_after should be NULL after mark reviewed")
	}
}

func TestMarkReviewedForProjectEnforcesCanonicalOwnership(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("review-scope-alpha", "alpha", "/tmp/alpha"); err != nil {
		t.Fatalf("CreateSession alpha: %v", err)
	}
	id, err := s.AddObservation(AddObservationParams{SessionID: "review-scope-alpha", Type: "decision", Title: "scoped", Content: "scoped content", Project: "alpha"})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}
	past := time.Now().UTC().Add(-24 * time.Hour).Format("2006-01-02 15:04:05")
	if _, err := s.DB().Exec(`UPDATE observations SET review_after = ? WHERE id = ?`, past, id); err != nil {
		t.Fatalf("backdate review_after: %v", err)
	}

	if err := s.MarkReviewedForProject(id, "beta"); !errors.Is(err, ErrObservationNotFound) {
		t.Fatalf("MarkReviewedForProject mismatched project error = %v, want ErrObservationNotFound", err)
	}
	unchanged, err := s.GetObservation(id)
	if err != nil {
		t.Fatalf("GetObservation after mismatch: %v", err)
	}
	if unchanged.State() != ObservationStateNeedsReview {
		t.Fatalf("mismatched mutation changed review state to %q", unchanged.State())
	}

	if err := s.MarkReviewedForProject(id, " ALPHA "); err != nil {
		t.Fatalf("MarkReviewedForProject matching canonical project: %v", err)
	}
	updated, err := s.GetObservation(id)
	if err != nil {
		t.Fatalf("GetObservation after matching mutation: %v", err)
	}
	if updated.State() != ObservationStateActive {
		t.Fatalf("matching mutation review state = %q, want active", updated.State())
	}
}

func TestMarkReviewedForProjectMatchesLegacyMixedCaseProject(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("legacy-review-sess", "legacy-project", "/tmp/legacy-review"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	id, err := s.AddObservation(AddObservationParams{SessionID: "legacy-review-sess", Type: "decision", Title: "legacy", Content: "legacy content", Project: "legacy-project"})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}
	past := time.Now().UTC().Add(-24 * time.Hour).Format("2006-01-02 15:04:05")
	if _, err := s.DB().Exec(`UPDATE observations SET project = ?, review_after = ? WHERE id = ?`, "Legacy-Project", past, id); err != nil {
		t.Fatalf("store legacy project: %v", err)
	}

	observations, err := s.ObservationsNeedingReview("legacy-project", 10)
	if err != nil {
		t.Fatalf("ObservationsNeedingReview: %v", err)
	}
	if len(observations) != 1 || observations[0].ID != id {
		t.Fatalf("review list = %#v, want legacy observation %d", observations, id)
	}

	if err := s.MarkReviewedForProject(id, "legacy-project"); err != nil {
		t.Fatalf("MarkReviewedForProject: %v", err)
	}
	updated, err := s.GetObservation(id)
	if err != nil {
		t.Fatalf("GetObservation: %v", err)
	}
	if updated.State() != ObservationStateActive {
		t.Fatalf("reviewed legacy observation state = %q, want active", updated.State())
	}
}

func TestMarkReviewedDoesNotEnqueueSyncMutation(t *testing.T) {
	s := newTestStore(t)
	if err := s.EnrollProject("mark-reviewed-sync-proj"); err != nil {
		t.Fatalf("EnrollProject: %v", err)
	}
	if err := s.CreateSession("mark-reviewed-sync-sess", "mark-reviewed-sync-proj", "/tmp/review-sync"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	obsID, err := s.AddObservation(AddObservationParams{SessionID: "mark-reviewed-sync-sess", Type: "decision", Title: "decision", Content: "decision content", Project: "mark-reviewed-sync-proj"})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}
	past := time.Now().UTC().Add(-24 * time.Hour).Format("2006-01-02 15:04:05")
	if _, err := s.db.Exec(`UPDATE observations SET review_after = ? WHERE id = ?`, past, obsID); err != nil {
		t.Fatalf("backdate review_after: %v", err)
	}

	before, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 10)
	if err != nil {
		t.Fatalf("ListPendingSyncMutations before: %v", err)
	}
	if err := s.MarkReviewed(obsID); err != nil {
		t.Fatalf("MarkReviewed: %v", err)
	}
	after, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 10)
	if err != nil {
		t.Fatalf("ListPendingSyncMutations after: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("MarkReviewed enqueued sync mutation: before=%d after=%d", len(before), len(after))
	}
}

// ─── C.2 [RED] — ListDeferred / GetDeferred ──────────────────────────────────

// seedDeferredRow is a test helper that inserts a row into sync_apply_deferred.
// Uses a different name from the existing insertDeferredRow in sync_apply_test.go
// which has parameter order (syncID, entity, payload, retryCount, applyStatus).
func seedDeferredRow(t *testing.T, s *Store, syncID, entity, payload string, retryCount int, applyStatus string) {
	t.Helper()
	if _, err := s.db.Exec(`
		INSERT INTO sync_apply_deferred
			(sync_id, entity, payload, apply_status, retry_count, first_seen_at)
		VALUES (?, ?, ?, ?, ?, datetime('now'))
	`, syncID, entity, payload, applyStatus, retryCount); err != nil {
		t.Fatalf("seedDeferredRow %q: %v", syncID, err)
	}
}

// TestListDeferred_HappyPath verifies pagination and status filter.
func TestListDeferred_HappyPath(t *testing.T) {
	s := newTestStore(t)

	validPayload := `{"relation_type":"conflicts_with","source_id":"obs-aaa","target_id":"obs-bbb"}`
	seedDeferredRow(t, s, "def-001", "relation", validPayload, 0, "deferred")
	seedDeferredRow(t, s, "def-002", "relation", validPayload, 1, "deferred")
	seedDeferredRow(t, s, "def-003", "relation", validPayload, 5, "dead")

	// List all.
	all, err := s.ListDeferred(ListDeferredOptions{Limit: 50})
	if err != nil {
		t.Fatalf("ListDeferred all: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("expected 3 rows; got %d", len(all))
	}

	// List only deferred status.
	deferred, err := s.ListDeferred(ListDeferredOptions{Status: "deferred", Limit: 50})
	if err != nil {
		t.Fatalf("ListDeferred deferred: %v", err)
	}
	if len(deferred) != 2 {
		t.Errorf("expected 2 deferred rows; got %d", len(deferred))
	}

	// Pagination: limit=1.
	page, err := s.ListDeferred(ListDeferredOptions{Limit: 1})
	if err != nil {
		t.Fatalf("ListDeferred limit=1: %v", err)
	}
	if len(page) != 1 {
		t.Errorf("expected 1 row with limit=1; got %d", len(page))
	}
}

// TestListDeferred_DecodedPayload verifies that DeferredRow.Payload is decoded
// and PayloadValid=true for well-formed JSON.
func TestListDeferred_DecodedPayload(t *testing.T) {
	s := newTestStore(t)

	validPayload := `{"relation_type":"conflicts_with","source_id":"obs-src","target_id":"obs-tgt","extra":42}`
	seedDeferredRow(t, s, "def-valid", "relation", validPayload, 0, "deferred")

	rows, err := s.ListDeferred(ListDeferredOptions{Limit: 10})
	if err != nil {
		t.Fatalf("ListDeferred: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row; got %d", len(rows))
	}
	row := rows[0]
	if !row.PayloadValid {
		t.Errorf("expected PayloadValid=true for well-formed JSON; got false. PayloadRaw=%q", row.PayloadRaw)
	}
	if row.Payload == nil {
		t.Fatal("expected decoded Payload map; got nil")
	}
	if row.Payload["relation_type"] != "conflicts_with" {
		t.Errorf("decoded Payload[relation_type]: want conflicts_with; got %v", row.Payload["relation_type"])
	}
}

// TestListDeferred_MalformedPayload verifies that a malformed JSON payload sets
// PayloadValid=false and preserves PayloadRaw.
func TestListDeferred_MalformedPayload(t *testing.T) {
	s := newTestStore(t)

	seedDeferredRow(t, s, "def-bad", "relation", "not valid json", 5, "dead")

	rows, err := s.ListDeferred(ListDeferredOptions{Limit: 10})
	if err != nil {
		t.Fatalf("ListDeferred malformed: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row; got %d", len(rows))
	}
	row := rows[0]
	if row.PayloadValid {
		t.Errorf("expected PayloadValid=false for malformed JSON; got true")
	}
	if row.PayloadRaw != "not valid json" {
		t.Errorf("expected PayloadRaw preserved; got %q", row.PayloadRaw)
	}
}

// TestGetDeferred_HappyPath verifies GetDeferred returns the correct row.
func TestGetDeferred_HappyPath(t *testing.T) {
	s := newTestStore(t)

	validPayload := `{"relation_type":"related","source_id":"obs-xyz","target_id":"obs-abc"}`
	seedDeferredRow(t, s, "def-xyz", "relation", validPayload, 2, "deferred")

	row, err := s.GetDeferred("def-xyz")
	if err != nil {
		t.Fatalf("GetDeferred: %v", err)
	}
	if row.SyncID != "def-xyz" {
		t.Errorf("expected SyncID=def-xyz; got %q", row.SyncID)
	}
	if row.ApplyStatus != "deferred" {
		t.Errorf("expected ApplyStatus=deferred; got %q", row.ApplyStatus)
	}
	if row.RetryCount != 2 {
		t.Errorf("expected RetryCount=2; got %d", row.RetryCount)
	}
	if !row.PayloadValid {
		t.Errorf("expected PayloadValid=true for valid JSON; got false")
	}
	if row.Payload["relation_type"] != "related" {
		t.Errorf("decoded Payload[relation_type]: want related; got %v", row.Payload["relation_type"])
	}
}

// TestGetDeferred_NotFound verifies GetDeferred returns an error wrapping "not found".
func TestGetDeferred_NotFound(t *testing.T) {
	s := newTestStore(t)

	_, err := s.GetDeferred("def-missing")
	if err == nil {
		t.Fatal("expected error for missing sync_id; got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected error to contain 'not found'; got %q", err.Error())
	}
}

func TestNormalizeScopeHandlesGlobal(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"global", "global"},
		{"Global", "global"},
		{"GLOBAL", "global"},
		{"  global  ", "global"},
		{"personal", "personal"},
		{"Personal", "personal"},
		{"project", "project"},
		{"Project", "project"},
		{"", "project"},
		{"unknown", "project"},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := normalizeScope(tc.input)
			if got != tc.want {
				t.Errorf("normalizeScope(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// TestSearchLegacyMixedCaseProject reproduces issue #146:
// observations stored with a mixed-case project name (legacy data pre-normalization)
// must be found when searched with a normalized (lowercase) project name.
//
// Previously, Search and ProjectExists used case-sensitive "project = ?" which
// caused all MCP tool calls to return empty results for such projects.
func TestSearchLegacyMixedCaseProject(t *testing.T) {
	s := newTestStore(t)

	// Insert a session and observation directly with mixed-case project name,
	// bypassing AddObservation normalization to simulate legacy data.
	legacyProject := "Ebook2Audio"
	_, err := s.db.Exec(
		`INSERT INTO sessions (id, project, directory) VALUES (?, ?, ?)`,
		"legacy-mixed-sess", legacyProject, "/tmp/ebook",
	)
	if err != nil {
		t.Fatalf("insert legacy session: %v", err)
	}

	_, err = s.db.Exec(`
		INSERT INTO observations (session_id, type, title, content, project, scope)
		VALUES (?, ?, ?, ?, ?, ?)`,
		"legacy-mixed-sess", "bugfix",
		"Fixed log routing in DisplayManager",
		"Corrected log routing so debug output goes to stderr not stdout",
		legacyProject, "project",
	)
	if err != nil {
		t.Fatalf("insert legacy observation: %v", err)
	}

	// Re-build FTS index so the new row is searchable.
	if _, err := s.db.Exec(`INSERT INTO observations_fts(observations_fts) VALUES('rebuild')`); err != nil {
		t.Fatalf("rebuild FTS: %v", err)
	}

	normalizedProject := "ebook2audio"

	// ProjectExists must find the legacy project via case-insensitive match.
	exists, err := s.ProjectExists(normalizedProject)
	if err != nil {
		t.Fatalf("ProjectExists error: %v", err)
	}
	if !exists {
		t.Error("ProjectExists returned false for mixed-case legacy project; want true")
	}

	// Search must return the observation when filtering by normalized project name.
	results, err := s.Search("log routing", SearchOptions{
		Project: normalizedProject,
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("Search error: %v", err)
	}
	if len(results) == 0 {
		t.Error("Search returned 0 results for legacy mixed-case project; want >=1")
	}

	// RecentObservations (used by mem_context) must also find the data.
	obs, err := s.RecentObservations(normalizedProject, "", 10)
	if err != nil {
		t.Fatalf("RecentObservations error: %v", err)
	}
	if len(obs) == 0 {
		t.Error("RecentObservations returned 0 results for legacy mixed-case project; want >=1")
	}

	// RecentSessions (used by mem_context) must also find the session.
	sessions, err := s.RecentSessions(normalizedProject, 5)
	if err != nil {
		t.Fatalf("RecentSessions error: %v", err)
	}
	if len(sessions) == 0 {
		t.Error("RecentSessions returned 0 results for legacy mixed-case project; want >=1")
	}
}

// ─── DeleteProject tests ──────────────────────────────────────────────────────

func TestDeleteProjectCascadesAllEntities(t *testing.T) {
	s := newTestStore(t)

	// Seed: one session with two observations and one prompt.
	if err := s.CreateSession("s-del-proj-1", "alpha", "/tmp/alpha"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	obsID1, err := s.AddObservation(AddObservationParams{
		SessionID: "s-del-proj-1",
		Type:      "decision",
		Title:     "obs-one",
		Content:   "content one",
		Project:   "alpha",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add obs 1: %v", err)
	}
	_, err = s.AddObservation(AddObservationParams{
		SessionID: "s-del-proj-1",
		Type:      "bugfix",
		Title:     "obs-two",
		Content:   "content two",
		Project:   "alpha",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add obs 2: %v", err)
	}
	_, err = s.AddPrompt(AddPromptParams{SessionID: "s-del-proj-1", Content: "prompt one", Project: "alpha"})
	if err != nil {
		t.Fatalf("add prompt: %v", err)
	}

	result, err := s.DeleteProject("alpha", true)
	if err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}

	if result.Project != "alpha" {
		t.Errorf("result.Project = %q, want %q", result.Project, "alpha")
	}
	if result.ObservationsDeleted != 2 {
		t.Errorf("ObservationsDeleted = %d, want 2", result.ObservationsDeleted)
	}
	if result.PromptsDeleted != 1 {
		t.Errorf("PromptsDeleted = %d, want 1", result.PromptsDeleted)
	}
	if result.SessionsDeleted != 1 {
		t.Errorf("SessionsDeleted = %d, want 1", result.SessionsDeleted)
	}

	// Verify rows are gone.
	var obsCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM observations WHERE session_id = ?`, "s-del-proj-1").Scan(&obsCount); err != nil {
		t.Fatalf("count observations: %v", err)
	}
	if obsCount != 0 {
		t.Errorf("expected 0 observations after hard delete, got %d", obsCount)
	}

	var sessionCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE project = ?`, "alpha").Scan(&sessionCount); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if sessionCount != 0 {
		t.Errorf("expected 0 sessions after DeleteProject, got %d", sessionCount)
	}

	var promptCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM user_prompts WHERE project = ?`, "alpha").Scan(&promptCount); err != nil {
		t.Fatalf("count prompts: %v", err)
	}
	if promptCount != 0 {
		t.Errorf("expected 0 prompts after DeleteProject, got %d", promptCount)
	}

	// Hard-deleted obs must also be gone from the table itself.
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM observations WHERE id = ?`, obsID1).Scan(&obsCount); err != nil {
		t.Fatalf("count obs by id: %v", err)
	}
	if obsCount != 0 {
		t.Errorf("expected obs #%d to be hard-deleted, got count %d", obsID1, obsCount)
	}
}

func TestDeleteProjectSoftDeleteObservations(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s-del-proj-soft", "beta", "/tmp/beta"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	_, err := s.AddPrompt(AddPromptParams{SessionID: "s-del-proj-soft", Content: "p", Project: "beta"})
	if err != nil {
		t.Fatalf("add prompt: %v", err)
	}
	obsID, err := s.AddObservation(AddObservationParams{
		SessionID: "s-del-proj-soft",
		Type:      "decision",
		Title:     "soft-obs",
		Content:   "content",
		Project:   "beta",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add obs: %v", err)
	}

	result, err := s.DeleteProject("beta", false)
	if err != nil {
		t.Fatalf("DeleteProject soft: %v", err)
	}
	if result.ObservationsDeleted != 1 {
		t.Errorf("ObservationsDeleted = %d, want 1", result.ObservationsDeleted)
	}
	if result.PromptsDeleted != 1 {
		t.Errorf("PromptsDeleted = %d, want 1", result.PromptsDeleted)
	}

	// Observation row must still exist but have deleted_at set.
	var deletedAt *string
	if err := s.db.QueryRow(`SELECT deleted_at FROM observations WHERE id = ?`, obsID).Scan(&deletedAt); err != nil {
		t.Fatalf("scan deleted_at: %v", err)
	}
	if deletedAt == nil {
		t.Errorf("expected deleted_at to be set after soft-delete, got nil")
	}

	// Prompts must be removed.
	var promptCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM user_prompts WHERE project = ?`, "beta").Scan(&promptCount); err != nil {
		t.Fatalf("count prompts: %v", err)
	}
	if promptCount != 0 {
		t.Errorf("expected 0 prompts after soft DeleteProject, got %d", promptCount)
	}

	// Sessions must NOT be removed in soft-delete mode (FK constraint from soft-deleted obs).
	var sessionCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE project = ?`, "beta").Scan(&sessionCount); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if sessionCount != 1 {
		t.Errorf("expected sessions to remain intact after soft DeleteProject, got count %d", sessionCount)
	}
	// SessionsDeleted must be 0 in soft mode.
	if result.SessionsDeleted != 0 {
		t.Errorf("expected SessionsDeleted = 0 in soft mode, got %d", result.SessionsDeleted)
	}
}

func TestDeleteProjectUnknownProjectReturnsError(t *testing.T) {
	s := newTestStore(t)
	_, err := s.DeleteProject("nonexistent-project-xyz", true)
	if err == nil {
		t.Fatal("expected error for unknown project, got nil")
	}
	if !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("expected ErrProjectNotFound, got %v", err)
	}
}

func TestDeleteProjectEmptyNameReturnsError(t *testing.T) {
	s := newTestStore(t)
	_, err := s.DeleteProject("", true)
	if err == nil {
		t.Fatal("expected error for empty project name, got nil")
	}
}

func TestDeleteProjectOrphansMemoryRelations(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s-del-proj-rel", "gamma", "/tmp/gamma"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	obsID, err := s.AddObservation(AddObservationParams{
		SessionID: "s-del-proj-rel",
		Type:      "decision",
		Title:     "rel-obs",
		Content:   "content",
		Project:   "gamma",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add obs: %v", err)
	}

	// Get sync_id of the observation for the relation.
	var syncID string
	if err := s.db.QueryRow(`SELECT sync_id FROM observations WHERE id = ?`, obsID).Scan(&syncID); err != nil {
		t.Fatalf("get sync_id: %v", err)
	}

	// Insert a fake relation that references this observation.
	relSyncID := "rel-" + syncID
	if _, err := s.db.Exec(`
		INSERT INTO memory_relations (sync_id, source_id, target_id, relation, judgment_status, created_at, updated_at)
		VALUES (?, ?, 'other-obs', 'related', 'pending', datetime('now'), datetime('now'))
	`, relSyncID, syncID); err != nil {
		t.Fatalf("insert relation: %v", err)
	}

	if _, err := s.DeleteProject("gamma", true); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}

	// The relation must be orphaned, not deleted.
	var judgmentStatus string
	if err := s.db.QueryRow(`SELECT judgment_status FROM memory_relations WHERE sync_id = ?`, relSyncID).Scan(&judgmentStatus); err != nil {
		t.Fatalf("scan relation judgment_status: %v", err)
	}
	if judgmentStatus != "orphaned" {
		t.Errorf("expected relation judgment_status = orphaned after hard delete, got %q", judgmentStatus)
	}
}

func TestMostRecentActiveSessionReturnsUnEndedSession(t *testing.T) {
	s := newTestStore(t)

	// A hook-registered UUID session, never ended.
	if err := s.CreateSession("uuid-active-1", "engram", "/work/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	id, ok, err := s.MostRecentActiveSession("engram")
	if err != nil {
		t.Fatalf("MostRecentActiveSession: %v", err)
	}
	if !ok || id != "uuid-active-1" {
		t.Fatalf("expected active session uuid-active-1, got id=%q ok=%v", id, ok)
	}
}

func TestMostRecentActiveSessionSkipsEndedSessions(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("uuid-ended-1", "engram", "/work/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := s.EndSession("uuid-ended-1", "done"); err != nil {
		t.Fatalf("end session: %v", err)
	}

	_, ok, err := s.MostRecentActiveSession("engram")
	if err != nil {
		t.Fatalf("MostRecentActiveSession: %v", err)
	}
	if ok {
		t.Fatalf("expected no active session when the only session is ended, got ok=%v", ok)
	}
}

func TestMostRecentActiveSessionNoSessionsReturnsFalse(t *testing.T) {
	s := newTestStore(t)

	_, ok, err := s.MostRecentActiveSession("engram")
	if err != nil {
		t.Fatalf("MostRecentActiveSession: %v", err)
	}
	if ok {
		t.Fatalf("expected ok=false for a project with no sessions, got ok=%v", ok)
	}
}

func TestMostRecentActiveSessionPicksMostRecentWhenMultipleActive(t *testing.T) {
	s := newTestStore(t)

	// Two un-ended UUID sessions for the same project; the newer started_at wins.
	if err := s.CreateSession("uuid-old", "engram", "/work/engram"); err != nil {
		t.Fatalf("create old session: %v", err)
	}
	if _, err := s.db.Exec(`UPDATE sessions SET started_at = ? WHERE id = ?`, "2025-01-01 00:00:00", "uuid-old"); err != nil {
		t.Fatalf("backdate old session: %v", err)
	}
	if err := s.CreateSession("uuid-new", "engram", "/work/engram"); err != nil {
		t.Fatalf("create new session: %v", err)
	}
	if _, err := s.db.Exec(`UPDATE sessions SET started_at = ? WHERE id = ?`, "2025-06-01 00:00:00", "uuid-new"); err != nil {
		t.Fatalf("set new session started_at: %v", err)
	}

	id, ok, err := s.MostRecentActiveSession("engram")
	if err != nil {
		t.Fatalf("MostRecentActiveSession: %v", err)
	}
	if !ok || id != "uuid-new" {
		t.Fatalf("expected most recent active session uuid-new, got id=%q ok=%v", id, ok)
	}
}

func TestMostRecentActiveSessionIgnoresManualSaveSessions(t *testing.T) {
	s := newTestStore(t)

	// The manual-save fallback session is also un-ended, but it must NOT be
	// resolved as "the active session" — otherwise resolution becomes circular.
	if err := s.CreateSession("manual-save-engram", "engram", "/work/engram"); err != nil {
		t.Fatalf("create manual-save session: %v", err)
	}

	_, ok, err := s.MostRecentActiveSession("engram")
	if err != nil {
		t.Fatalf("MostRecentActiveSession: %v", err)
	}
	if ok {
		t.Fatalf("expected manual-save session to be ignored, got ok=%v", ok)
	}
}

func TestMostRecentActiveSessionScopedByProject(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("uuid-other-proj", "other", "/work/other"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	_, ok, err := s.MostRecentActiveSession("engram")
	if err != nil {
		t.Fatalf("MostRecentActiveSession: %v", err)
	}
	if ok {
		t.Fatalf("expected no active session for engram when only 'other' has one, got ok=%v", ok)
	}
}

// ─── match_mode tests (issue #352) ──────────────────────────────────────────

// seedMatchModeFixture creates a session and three observations with partial
// token overlap — no single observation contains all three query tokens.
//
//	obs1: title "Auth session middleware"       content "request routing layer"
//	obs2: title "Compliance audit notes"        content "session policy"
//	obs3: title "OAuth tokens"                  content "auth and compliance"
func seedMatchModeFixture(t *testing.T, s *Store) {
	t.Helper()
	if err := s.CreateSession("s-matchmode", "engram", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	obs := []AddObservationParams{
		{SessionID: "s-matchmode", Type: "decision", Title: "Auth session middleware", Content: "request routing layer", Project: "engram", Scope: "project"},
		{SessionID: "s-matchmode", Type: "decision", Title: "Compliance audit notes", Content: "session policy", Project: "engram", Scope: "project"},
		{SessionID: "s-matchmode", Type: "decision", Title: "OAuth tokens", Content: "auth and compliance", Project: "engram", Scope: "project"},
	}
	for _, p := range obs {
		if _, err := s.AddObservation(p); err != nil {
			t.Fatalf("seed observation %q: %v", p.Title, err)
		}
	}
}

// TestSearchMatchMode_DefaultIsAND verifies that the default (AND) behaviour
// returns 0 results when no single observation contains all query tokens.
func TestSearchMatchMode_DefaultIsAND(t *testing.T) {
	s := newTestStore(t)
	seedMatchModeFixture(t, s)

	results, err := s.Search("auth compliance session", SearchOptions{Project: "engram", Limit: 10})
	if err != nil {
		t.Fatalf("Search error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 results for AND query, got %d", len(results))
	}
}

// TestSearchMatchMode_AllExplicit verifies that MatchMode "all" behaves
// identically to the default AND mode.
func TestSearchMatchMode_AllExplicit(t *testing.T) {
	s := newTestStore(t)
	seedMatchModeFixture(t, s)

	results, err := s.Search("auth compliance session", SearchOptions{Project: "engram", Limit: 10, MatchMode: "all"})
	if err != nil {
		t.Fatalf("Search error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 results for explicit match_mode=all, got %d", len(results))
	}
}

// TestSearchMatchMode_Any verifies that MatchMode "any" returns all three
// observations because each contains at least one of the query tokens.
func TestSearchMatchMode_Any(t *testing.T) {
	s := newTestStore(t)
	seedMatchModeFixture(t, s)

	results, err := s.Search("auth compliance session", SearchOptions{Project: "engram", Limit: 10, MatchMode: "any"})
	if err != nil {
		t.Fatalf("Search error: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results for match_mode=any, got %d", len(results))
	}
}

func TestSearchMatchMode_AnyEscapesInteriorQuotes(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s-matchmode-quotes", "engram", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := s.AddObservation(AddObservationParams{
		SessionID: "s-matchmode-quotes",
		Type:      "decision",
		Title:     `hello"world`,
		Content:   "quoted search target",
		Project:   "engram",
		Scope:     "project",
	}); err != nil {
		t.Fatalf("add observation: %v", err)
	}

	for _, query := range []string{`hello"world`, `"hello""world"`} {
		t.Run(query, func(t *testing.T) {
			results, err := s.SearchContext(context.Background(), query, SearchOptions{Project: "engram", Limit: 10, MatchMode: "any"})
			if err != nil {
				t.Fatalf("SearchContext(%q): %v", query, err)
			}
			if len(results) != 1 {
				t.Fatalf("expected 1 result for %q, got %d", query, len(results))
			}
		})
	}
}

// TestSearchMatchMode_InvalidReturnsError verifies that an unrecognised
// match_mode value returns an explicit error regardless of query shape.
func TestSearchMatchMode_InvalidReturnsError(t *testing.T) {
	s := newTestStore(t)
	seedMatchModeFixture(t, s)

	_, err := s.Search("auth compliance session", SearchOptions{Project: "engram", Limit: 10, MatchMode: "or"})
	if err == nil {
		t.Fatalf("expected error for invalid match_mode, got nil")
	}
	if !strings.Contains(err.Error(), "invalid match_mode") {
		t.Fatalf("expected error to contain \"invalid match_mode\", got: %v", err)
	}
}

// TestSearchMatchMode_SingleToken verifies that a single-token query returns
// the same result regardless of match_mode (both modes are equivalent for one
// token because AND and OR over a single term are identical).
func TestSearchMatchMode_SingleToken(t *testing.T) {
	s := newTestStore(t)
	seedMatchModeFixture(t, s)

	defaultRes, err := s.Search("auth", SearchOptions{Project: "engram", Limit: 10})
	if err != nil {
		t.Fatalf("default Search error: %v", err)
	}
	anyRes, err := s.Search("auth", SearchOptions{Project: "engram", Limit: 10, MatchMode: "any"})
	if err != nil {
		t.Fatalf("any Search error: %v", err)
	}
	if len(defaultRes) != len(anyRes) {
		t.Fatalf("single-token results differ: default=%d any=%d", len(defaultRes), len(anyRes))
	}
}

// TestSearchMatchMode_EmptyQueryAnyReturnsError pins that Search("", …{MatchMode:"any"})
// returns an error — the FTS5 engine rejects an empty match expression, and this
// behaviour is the same as the default AND mode with an empty query.
func TestSearchMatchMode_EmptyQueryAnyReturnsError(t *testing.T) {
	s := newTestStore(t)
	seedMatchModeFixture(t, s)

	_, err := s.Search("", SearchOptions{Project: "engram", Limit: 10, MatchMode: "any"})
	if err == nil {
		t.Fatal("expected error for empty query with match_mode=any, got nil")
	}
}

func TestSearch_WeightedBM25Ranking(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s-bm25", "engram", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Observation A: query term in title (weight 5.0)
	idA, err := s.AddObservation(AddObservationParams{
		SessionID: "s-bm25",
		Type:      "decision",
		Title:     "banana apple grape",
		Content:   "nothing here",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("AddObservation A: %v", err)
	}

	// Observation B: query term in content (weight 1.0)
	idB, err := s.AddObservation(AddObservationParams{
		SessionID: "s-bm25",
		Type:      "decision",
		Title:     "nothing here",
		Content:   "banana apple grape",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("AddObservation B: %v", err)
	}

	results, err := s.Search("banana", SearchOptions{Project: "engram", Limit: 10})
	if err != nil {
		t.Fatalf("Search error: %v", err)
	}

	if len(results) < 2 {
		t.Fatalf("expected at least 2 results, got %d", len(results))
	}

	// Since we order by rank, and rank is BM25, the title match (A) should be first (more relevant).
	if results[0].ID != idA {
		t.Errorf("expected observation A (title match) to rank higher than B (content match); got first: %d (title: %q, rank: %v), second: %d (title: %q, rank: %v)",
			results[0].ID, results[0].Title, results[0].Rank, results[1].ID, results[1].Title, results[1].Rank)
	}
	if results[1].ID != idB {
		t.Errorf("expected observation B (content match) to rank second; got second: %d (title: %q, rank: %v)",
			results[1].ID, results[1].Title, results[1].Rank)
	}
}

func TestSearchContext_AlreadyCanceled(t *testing.T) {
	s := newTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	results, err := s.SearchContext(ctx, "unreachable", SearchOptions{Project: "engram", Limit: 10})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got results=%v err=%v", results, err)
	}
	if results != nil {
		t.Fatalf("expected no results from canceled search, got %v", results)
	}
}

func TestFTSQueriesUseFTSFirstCrossJoin(t *testing.T) {
	searchQuery, _ := buildSearchFTSQuery(`"memory"`, SearchOptions{}, 10)
	for name, query := range map[string]string{
		"search":          searchQuery,
		"find candidates": findCandidatesFTSQuery,
	} {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(query, "CROSS JOIN observations o ON o.id = fts.rowid") {
				t.Fatalf("expected FTS-first CROSS JOIN, got query:\n%s", query)
			}
		})
	}
}

// TestSanitizeFTS verifies sanitizeFTS escapes interior double-quotes per FTS5
// string-literal rules, preventing "unterminated string" crashes on inputs like
// `hello"world`. See issue #574.
func TestSanitizeFTS(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"plain word", "foo", `"foo"`},
		{"multiple words", "fix auth bug", `"fix" "auth" "bug"`},
		{"interior double-quote (the bug)", `foo"bar`, `"foo""bar"`},
		{"already quoted", `"hello"`, `"hello"`},
		{"multiple interior quotes", `a"b"c`, `"a""b""c"`},
		{"just quotes", `""`, `""`},
		{"mixed quote and plain", `hello"world test`, `"hello""world" "test"`},
		{"empty input", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeFTS(tc.input)
			if got != tc.want {
				t.Errorf("sanitizeFTS(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// TestListQuarantinedPulledSessionEvidenceScopesByProject proves the doctor
// surface for skipped pull mutations is project-scoped and reports the remote
// coordinates an operator needs to locate the dropped data.
func TestListQuarantinedPulledSessionEvidenceScopesByProject(t *testing.T) {
	s := newTestStore(t)
	mutations := []SyncMutation{
		{Seq: 1, Entity: SyncEntitySession, EntityKey: "\t", Op: SyncOpUpsert, Payload: `{"id":"","project":"engram","directory":"/a"}`},
		{Seq: 2, Entity: SyncEntitySession, EntityKey: "\n", Op: SyncOpUpsert, Payload: `{"id":"","project":"other","directory":"/b"}`},
	}
	for _, mutation := range mutations {
		if err := s.ApplyPulledMutation(DefaultSyncTargetKey, mutation); err != nil {
			t.Fatalf("ApplyPulledMutation seq=%d: %v", mutation.Seq, err)
		}
	}

	scoped, err := s.ListQuarantinedPulledSessionEvidence("engram")
	if err != nil {
		t.Fatalf("ListQuarantinedPulledSessionEvidence: %v", err)
	}
	if len(scoped) != 1 || scoped[0].RemoteSeq != 1 || scoped[0].EntityKey != "\t" || scoped[0].Project != "engram" {
		t.Fatalf("scoped evidence=%+v", scoped)
	}
	if scoped[0].ReasonCode != SyncSessionIdentityInvalidReasonCode || scoped[0].TargetKey != DefaultSyncTargetKey || scoped[0].Op != SyncOpUpsert {
		t.Fatalf("scoped evidence=%+v", scoped)
	}

	all, err := s.ListQuarantinedPulledSessionEvidence("")
	if err != nil || len(all) != 2 {
		t.Fatalf("unscoped evidence=%+v, err=%v", all, err)
	}
	if all[0].RemoteSeq != 1 || all[1].RemoteSeq != 2 {
		t.Fatalf("unscoped evidence is not ordered by remote seq: %+v", all)
	}

	state, err := s.GetSyncState(DefaultSyncTargetKey)
	if err != nil || state.LastPulledSeq != 2 {
		t.Fatalf("sync state=%+v, err=%v; cursor must advance past quarantined mutations", state, err)
	}
}

// TestAddObservationRejectsEmptyTitle pins the write-time guard for #459: an
// observation without a usable title must never reach the observations table,
// because it also enqueues a cloud upsert the sync validators reject, which
// blocks every later mutation for the project.
func TestAddObservationRejectsEmptyTitle(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s-title-guard", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	countMutations := func() int {
		t.Helper()
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM sync_mutations WHERE entity = ?`, SyncEntityObservation).Scan(&n); err != nil {
			t.Fatalf("count sync mutations: %v", err)
		}
		return n
	}
	countObservations := func() int {
		t.Helper()
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM observations`).Scan(&n); err != nil {
			t.Fatalf("count observations: %v", err)
		}
		return n
	}

	cases := []struct {
		name  string
		title string
	}{
		{"empty title", ""},
		{"whitespace only title", "   "},
		// stripPrivateTags trims the value, so a title made only of blank
		// characters is still empty after stripping. The guard runs on the
		// post-strip title precisely so redaction cannot smuggle one through.
		{"title empty after stripping private tags", " \t\n "},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mutationsBefore := countMutations()
			observationsBefore := countObservations()

			id, err := s.AddObservation(AddObservationParams{
				SessionID: "s-title-guard",
				Type:      "note",
				Title:     tc.title,
				Content:   "content that is perfectly valid",
				Project:   "engram",
				Scope:     "project",
			})
			if !errors.Is(err, ErrObservationTitleRequired) {
				t.Fatalf("expected ErrObservationTitleRequired, got id=%d err=%v", id, err)
			}
			if id != 0 {
				t.Fatalf("expected no observation id, got %d", id)
			}
			if got := countObservations(); got != observationsBefore {
				t.Fatalf("expected no observation persisted, count went %d → %d", observationsBefore, got)
			}
			if got := countMutations(); got != mutationsBefore {
				t.Fatalf("expected no sync mutation enqueued, count went %d → %d", mutationsBefore, got)
			}
		})
	}
}

// TestAddObservationAcceptsValidTitle pins that the #459 guard does not change
// behaviour for observations that already carry a usable title, including a
// title whose private tags collapse into the redaction marker.
func TestAddObservationAcceptsValidTitle(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s-title-ok", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	cases := []struct {
		name      string
		title     string
		wantTitle string
	}{
		{"plain title", "  Reject empty titles  ", "Reject empty titles"},
		{"title reduced to redaction marker", "<private>secret</private>", "[REDACTED]"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, err := s.AddObservation(AddObservationParams{
				SessionID: "s-title-ok",
				Type:      "note",
				Title:     tc.title,
				Content:   "content for " + tc.name,
				Project:   "engram",
				Scope:     "project",
			})
			if err != nil {
				t.Fatalf("add observation: %v", err)
			}
			obs, err := s.GetObservation(id)
			if err != nil {
				t.Fatalf("get observation: %v", err)
			}
			if obs.Title != tc.wantTitle {
				t.Fatalf("expected title %q, got %q", tc.wantTitle, obs.Title)
			}

			var mutations int
			if err := s.db.QueryRow(
				`SELECT COUNT(*) FROM sync_mutations WHERE entity = ? AND entity_key = ?`,
				SyncEntityObservation, obs.SyncID,
			).Scan(&mutations); err != nil {
				t.Fatalf("count sync mutations: %v", err)
			}
			if mutations != 1 {
				t.Fatalf("expected 1 sync mutation for %s, got %d", obs.SyncID, mutations)
			}
		})
	}
}

// TestValidateObservationTitleMatchesSyncPayloadRule pins that the write-time
// guard and the sync payload validator agree on what a missing title is, so the
// rule keeps living in exactly one place.
func TestValidateObservationTitleMatchesSyncPayloadRule(t *testing.T) {
	cases := []struct {
		name  string
		title string
		valid bool
	}{
		{"empty", "", false},
		{"whitespace", "   ", false},
		{"present", "Real title", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateObservationTitle(tc.title)
			if tc.valid && err != nil {
				t.Fatalf("expected title %q to be accepted, got %v", tc.title, err)
			}
			if !tc.valid && !errors.Is(err, ErrObservationTitleRequired) {
				t.Fatalf("expected ErrObservationTitleRequired for %q, got %v", tc.title, err)
			}

			payload, marshalErr := json.Marshal(map[string]string{
				"sync_id":    "obs-1",
				"session_id": "s-1",
				"type":       "note",
				"title":      tc.title,
				"content":    "content",
				"scope":      "project",
			})
			if marshalErr != nil {
				t.Fatalf("marshal payload: %v", marshalErr)
			}
			result := ValidateSyncMutationPayload(SyncEntityObservation, SyncOpUpsert, string(payload), "obs-1")
			missingTitle := false
			for _, field := range result.MissingFields {
				if field == "title" {
					missingTitle = true
				}
			}
			if missingTitle == tc.valid {
				t.Fatalf("validator disagrees with write guard for %q: missing_fields=%v", tc.title, result.MissingFields)
			}
		})
	}
}

func TestUpdateObservationRejectsBlankTitleWithoutSideEffects(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s-update-title-guard", "engram", t.TempDir()); err != nil {
		t.Fatalf("create session: %v", err)
	}
	id, err := s.AddObservation(AddObservationParams{
		SessionID: "s-update-title-guard",
		Type:      "note",
		Title:     "Original title",
		Content:   "Original content",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}
	before, err := s.GetObservation(id)
	if err != nil {
		t.Fatalf("get original observation: %v", err)
	}
	countMutations := func() int {
		t.Helper()
		var count int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM sync_mutations WHERE entity = ? AND entity_key = ?`, SyncEntityObservation, before.SyncID).Scan(&count); err != nil {
			t.Fatalf("count observation mutations: %v", err)
		}
		return count
	}
	mutationsBefore := countMutations()

	for _, title := range []string{"", " \t\n "} {
		title := title
		t.Run(fmt.Sprintf("title %q", title), func(t *testing.T) {
			_, err := s.UpdateObservation(id, UpdateObservationParams{Title: &title})
			if !errors.Is(err, ErrObservationTitleRequired) {
				t.Fatalf("expected ErrObservationTitleRequired, got %v", err)
			}
			after, err := s.GetObservation(id)
			if err != nil {
				t.Fatalf("get observation after rejected update: %v", err)
			}
			if after.Title != before.Title || after.Content != before.Content || after.RevisionCount != before.RevisionCount {
				t.Fatalf("rejected update changed observation: before=%#v after=%#v", before, after)
			}
			if got := countMutations(); got != mutationsBefore {
				t.Fatalf("rejected update enqueued a mutation: got %d, want %d", got, mutationsBefore)
			}
		})
	}
}

func TestUpdateObservationAcceptsPrivateTagOnlyTitle(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s-update-redaction", "engram", t.TempDir()); err != nil {
		t.Fatalf("create session: %v", err)
	}
	id, err := s.AddObservation(AddObservationParams{
		SessionID: "s-update-redaction",
		Type:      "note",
		Title:     "Original title",
		Content:   "Original content",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}
	title := "<private>secret</private>"
	updated, err := s.UpdateObservation(id, UpdateObservationParams{Title: &title})
	if err != nil {
		t.Fatalf("update observation: %v", err)
	}
	if updated.Title != "[REDACTED]" {
		t.Fatalf("expected redacted title, got %q", updated.Title)
	}

	var mutationCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sync_mutations WHERE entity = ? AND entity_key = ?`, SyncEntityObservation, updated.SyncID).Scan(&mutationCount); err != nil {
		t.Fatalf("count observation mutations: %v", err)
	}
	if mutationCount != 2 {
		t.Fatalf("expected exactly two observation mutations (create and update), got %d", mutationCount)
	}
	var mutation SyncMutation
	if err := s.db.QueryRow(`SELECT op, payload FROM sync_mutations WHERE entity = ? AND entity_key = ? ORDER BY seq DESC LIMIT 1`, SyncEntityObservation, updated.SyncID).Scan(&mutation.Op, &mutation.Payload); err != nil {
		t.Fatalf("load update mutation: %v", err)
	}
	if mutation.Op != SyncOpUpsert {
		t.Fatalf("expected update mutation op %q, got %q", SyncOpUpsert, mutation.Op)
	}
	if validation := ValidateSyncMutationPayload(SyncEntityObservation, mutation.Op, mutation.Payload, updated.SyncID); len(validation.MissingFields) != 0 || validation.ReasonCode != "" {
		t.Fatalf("expected valid update mutation, got %#v", validation)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(mutation.Payload), &payload); err != nil {
		t.Fatalf("decode update mutation payload: %v", err)
	}
	if payload["title"] != "[REDACTED]" {
		t.Fatalf("expected redacted title in update payload, got %#v", payload["title"])
	}
}
