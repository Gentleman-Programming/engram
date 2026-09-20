package store

// Tests for the maintainer-approved engram#1287 boundary: LOCAL import surfaces
// (direct JSON Import and the local pulled-chunk apply path) accept and preserve
// blank session directories, while cloud inbound validation stays strict. Each
// test's doc comment names the RED/GREEN matrix item it covers from the issue.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// sessionUpsertPayloadJSON builds a session upsert payload with the directory
// key explicitly present, even when blank, mirroring the wire format cloud and
// local peers exchange.
func sessionUpsertPayloadJSON(id, directory string) string {
	return fmt.Sprintf(`{"id":%q,"project":"engram","directory":%q}`, id, directory)
}

// countSessionDependents returns the number of observations and prompts attached
// to a session id, for dependency-stall assertions.
func countSessionDependents(t *testing.T, s *Store, sessionID string) (observations, prompts int) {
	t.Helper()
	if err := s.DB().QueryRow(`SELECT count(*) FROM observations WHERE session_id = ?`, sessionID).Scan(&observations); err != nil {
		t.Fatalf("count observations for %s: %v", sessionID, err)
	}
	if err := s.DB().QueryRow(`SELECT count(*) FROM user_prompts WHERE session_id = ?`, sessionID).Scan(&prompts); err != nil {
		t.Fatalf("count prompts for %s: %v", sessionID, err)
	}
	return observations, prompts
}

// Matrix item 1 (JSON round trip): a store holding blank-directory sessions
// exports and re-imports into a FRESH store with the blanks preserved exactly —
// "" stays "" and whitespace stays whitespace, never normalized.
// RED on main: Import rejected blank directories outright.
func TestImportRoundTripPreservesBlankDirectory(t *testing.T) {
	s := newTestStore(t)
	result, err := s.Import(&ExportData{
		Version: currentExportVersion,
		Sessions: []Session{
			{ID: "blank-dir-session", Project: "engram", Directory: "", StartedAt: "2026-01-01 00:00:00"},
			{ID: "whitespace-dir-session", Project: "engram", Directory: "  ", StartedAt: "2026-01-01 00:00:00"},
		},
	})
	if err != nil {
		t.Fatalf("Import with blank directories: %v", err)
	}
	if result.SessionsImported != 2 {
		t.Fatalf("SessionsImported = %d, want 2", result.SessionsImported)
	}

	exported, err := s.Export()
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if len(exported.Sessions) != 2 {
		t.Fatalf("exported sessions = %d, want 2", len(exported.Sessions))
	}
	exportedDirectories := map[string]string{}
	for _, sess := range exported.Sessions {
		exportedDirectories[sess.ID] = sess.Directory
	}
	if got := exportedDirectories["blank-dir-session"]; got != "" {
		t.Fatalf("exported blank directory = %q, want exactly \"\"", got)
	}
	if got := exportedDirectories["whitespace-dir-session"]; got != "  " {
		t.Fatalf("exported whitespace directory = %q, want exactly %q", got, "  ")
	}

	dst := newTestStore(t)
	reimported, err := dst.Import(exported)
	if err != nil {
		t.Fatalf("re-import into fresh store: %v", err)
	}
	if reimported.SessionsImported != 2 {
		t.Fatalf("re-imported sessions = %d, want 2 (session count not preserved)", reimported.SessionsImported)
	}
	want := map[string]string{"blank-dir-session": "", "whitespace-dir-session": "  "}
	for id, directory := range want {
		sess, err := dst.GetSession(id)
		if err != nil {
			t.Fatalf("GetSession %s after re-import: %v", id, err)
		}
		if sess.Directory != directory {
			t.Fatalf("stored directory for %s = %q, want exactly %q (blank not preserved)", id, sess.Directory, directory)
		}
	}
}

// Matrix item 2 (local chunk round trip): a pulled chunk whose session upsert
// payload carries "directory": "" applies fully through the local apply path —
// no stall, no dead-letter — and stores the blank unchanged, with the pull
// cursor advanced past it.
// RED on main: the shared validator rejected present-but-blank directories.
func TestPulledChunkBlankDirectoryAppliesLocally(t *testing.T) {
	s := newTestStore(t)
	const sessionID = "chunk-blank-dir-session"
	mutations := []SyncMutation{{
		Entity:    SyncEntitySession,
		EntityKey: sessionID,
		Op:        SyncOpUpsert,
		Payload:   sessionUpsertPayloadJSON(sessionID, ""),
	}}
	if err := s.ApplyPulledChunk(LocalChunkTargetKey, "chunk-blank-dir-1", mutations); err != nil {
		t.Fatalf("ApplyPulledChunk with blank directory: %v", err)
	}
	sess, err := s.GetSession(sessionID)
	if err != nil {
		t.Fatalf("GetSession %s: %v", sessionID, err)
	}
	if sess.Directory != "" {
		t.Fatalf("stored directory = %q, want exactly \"\" (unchanged from payload)", sess.Directory)
	}
	deferred, err := s.ListDeferred(ListDeferredOptions{})
	if err != nil {
		t.Fatalf("ListDeferred: %v", err)
	}
	if len(deferred) != 0 {
		t.Fatalf("deferred rows = %d, want 0 (blank directory must not dead-letter)", len(deferred))
	}
	state, err := s.GetSyncState(LocalChunkTargetKey)
	if err != nil {
		t.Fatalf("GetSyncState: %v", err)
	}
	if state.LastPulledSeq != 1 {
		t.Fatalf("LastPulledSeq = %d, want 1 (chunk must not stall the cursor)", state.LastPulledSeq)
	}
}

// Matrix item 3 (dependent observations/prompts): an observation and a prompt
// referencing a blank-directory session import and apply without dependency
// stalls and stay attached to the blank session.
// RED on main: the blank session upsert was rejected, so its dependents could
// not attach.
func TestBlankDirectorySessionKeepsDependentRecordsAttached(t *testing.T) {
	const sessionID = "blank-dir-parent"

	t.Run("json import", func(t *testing.T) {
		s := newTestStore(t)
		data := &ExportData{
			Version:      currentExportVersion,
			Sessions:     []Session{{ID: sessionID, Project: "engram", Directory: "", StartedAt: "2026-01-01 00:00:00"}},
			Observations: []Observation{{SyncID: "obs-blank-parent", SessionID: sessionID, Type: "manual", Title: "title", Content: "content", Scope: "project", CreatedAt: "2026-01-01 00:01:00", UpdatedAt: "2026-01-01 00:01:00"}},
			Prompts:      []Prompt{{SyncID: "prompt-blank-parent", SessionID: sessionID, Content: "hello", CreatedAt: "2026-01-01 00:01:00"}},
		}
		if _, err := s.Import(data); err != nil {
			t.Fatalf("Import blank session with dependents: %v", err)
		}
		observations, prompts := countSessionDependents(t, s, sessionID)
		if observations != 1 || prompts != 1 {
			t.Fatalf("dependents attached = %d observations, %d prompts, want 1 and 1", observations, prompts)
		}
		sess, err := s.GetSession(sessionID)
		if err != nil || sess.Directory != "" {
			t.Fatalf("GetSession = %+v, %v; want directory exactly \"\"", sess, err)
		}
	})

	t.Run("pulled chunk", func(t *testing.T) {
		s := newTestStore(t)
		obsPayload, err := json.Marshal(syncObservationPayload{
			SyncID: "obs-chunk-blank-parent", SessionID: sessionID, Type: "manual",
			Title: "title", Content: "content", Scope: "project",
			CreatedAt: "2026-01-01 00:01:00", UpdatedAt: "2026-01-01 00:01:00",
		})
		if err != nil {
			t.Fatalf("marshal observation payload: %v", err)
		}
		promptPayload, err := json.Marshal(syncPromptPayload{
			SyncID: "prompt-chunk-blank-parent", SessionID: sessionID,
			Content: "hello", CreatedAt: "2026-01-01 00:01:00",
		})
		if err != nil {
			t.Fatalf("marshal prompt payload: %v", err)
		}
		mutations := []SyncMutation{
			{Entity: SyncEntitySession, EntityKey: sessionID, Op: SyncOpUpsert, Payload: sessionUpsertPayloadJSON(sessionID, "")},
			{Entity: SyncEntityObservation, EntityKey: "obs-chunk-blank-parent", Op: SyncOpUpsert, Payload: string(obsPayload)},
			{Entity: SyncEntityPrompt, EntityKey: "prompt-chunk-blank-parent", Op: SyncOpUpsert, Payload: string(promptPayload)},
		}
		if err := s.ApplyPulledChunk(LocalChunkTargetKey, "chunk-blank-parent-1", mutations); err != nil {
			t.Fatalf("ApplyPulledChunk blank session with dependents: %v", err)
		}
		deferred, err := s.ListDeferred(ListDeferredOptions{})
		if err != nil {
			t.Fatalf("ListDeferred: %v", err)
		}
		if len(deferred) != 0 {
			t.Fatalf("deferred rows = %d, want 0 (dependents must not stall)", len(deferred))
		}
		observations, prompts := countSessionDependents(t, s, sessionID)
		if observations != 1 || prompts != 1 {
			t.Fatalf("dependents attached = %d observations, %d prompts, want 1 and 1", observations, prompts)
		}
		state, err := s.GetSyncState(LocalChunkTargetKey)
		if err != nil || state.LastPulledSeq != 3 {
			t.Fatalf("LastPulledSeq = %d, %v; want 3 (all mutations applied)", state.LastPulledSeq, err)
		}
	})
}

// Matrix item 4 (repeated import): importing the same JSON payload twice into
// the same store succeeds idempotently and does not corrupt the blank row.
// RED on main: the first import was rejected outright.
func TestImportBlankDirectorySessionIsIdempotent(t *testing.T) {
	s := newTestStore(t)
	data := &ExportData{
		Version:  currentExportVersion,
		Sessions: []Session{{ID: "idempotent-blank", Project: "engram", Directory: "", StartedAt: "2026-01-01 00:00:00"}},
	}
	first, err := s.Import(data)
	if err != nil {
		t.Fatalf("first Import: %v", err)
	}
	if first.SessionsImported != 1 {
		t.Fatalf("first SessionsImported = %d, want 1", first.SessionsImported)
	}
	second, err := s.Import(data)
	if err != nil {
		t.Fatalf("second Import: %v", err)
	}
	if second.SessionsImported != 0 {
		t.Fatalf("second SessionsImported = %d, want 0 (duplicate skipped, not duplicated)", second.SessionsImported)
	}
	sess, err := s.GetSession("idempotent-blank")
	if err != nil {
		t.Fatalf("GetSession after repeat import: %v", err)
	}
	if sess.Directory != "" {
		t.Fatalf("stored directory after repeat import = %q, want exactly \"\"", sess.Directory)
	}
}

// Matrix item 5 (later completion): a blank-directory session adopts a concrete
// directory when the same id arrives again with one, pinning the existing
// createSessionTx/startSessionTx upsert CASE completion contract.
// GREEN on main too: the first subtest seeds the blank via the local
// partial-session create path (main rejected blank imports), so it documents a
// contract that already exists; the second subtest composes it with the new
// import acceptance and is therefore RED on main.
func TestBlankDirectorySessionCompletesOnLaterConcreteDirectory(t *testing.T) {
	t.Run("partial local create then completion", func(t *testing.T) {
		s := newTestStore(t)
		if err := s.CreateSession("completing-session", "engram", " \t "); err != nil {
			t.Fatalf("CreateSession blank: %v", err)
		}
		partial, err := s.GetSession("completing-session")
		if err != nil || strings.TrimSpace(partial.Directory) != "" {
			t.Fatalf("partial session = %+v, %v; want blank directory", partial, err)
		}
		if err := s.CreateSession("completing-session", "engram", "/concrete/dir"); err != nil {
			t.Fatalf("CreateSession concrete: %v", err)
		}
		sess, err := s.GetSession("completing-session")
		if err != nil {
			t.Fatalf("GetSession after completion: %v", err)
		}
		if sess.Directory != "/concrete/dir" {
			t.Fatalf("stored directory = %q, want /concrete/dir (blank must adopt later non-blank)", sess.Directory)
		}
	})

	t.Run("imported blank then completion", func(t *testing.T) {
		s := newTestStore(t)
		if _, err := s.Import(&ExportData{
			Version:  currentExportVersion,
			Sessions: []Session{{ID: "imported-completing", Project: "engram", Directory: "", StartedAt: "2026-01-01 00:00:00"}},
		}); err != nil {
			t.Fatalf("Import blank: %v", err)
		}
		if err := s.CreateSession("imported-completing", "engram", "/imported/dir"); err != nil {
			t.Fatalf("CreateSession concrete over imported blank: %v", err)
		}
		sess, err := s.GetSession("imported-completing")
		if err != nil {
			t.Fatalf("GetSession after completion: %v", err)
		}
		if sess.Directory != "/imported/dir" {
			t.Fatalf("stored directory = %q, want /imported/dir (imported blank must adopt later non-blank)", sess.Directory)
		}
	})
}

// Matrix item 6 (cloud rejection unchanged): the strict validator variant used
// by the cloud callers (ApplyPulledMutation / autosync, via the cloud domain)
// still rejects present-but-blank and missing-key payloads with today's
// byte-identical error strings.
func TestValidatePulledSessionDirectoryStrictRejectsBlankAndMissing(t *testing.T) {
	t.Run("present but blank", func(t *testing.T) {
		for _, directory := range []string{`""`, `" \t "`, `null`} {
			payload := fmt.Sprintf(`{"id":"strict-session","project":"engram","directory":%s}`, directory)
			err := validatePulledSessionDirectory([]byte(payload))
			if !errors.Is(err, ErrPulledSessionDirectoryInvalid) {
				t.Fatalf("directory %s: error = %v, want ErrPulledSessionDirectoryInvalid", directory, err)
			}
			if got, want := err.Error(), "pulled session directory is invalid: directory must be non-blank"; got != want {
				t.Fatalf("directory %s: error = %q, want byte-identical %q", directory, got, want)
			}
		}
	})
	t.Run("missing key", func(t *testing.T) {
		err := validatePulledSessionDirectory([]byte(`{"id":"strict-session","project":"engram"}`))
		if !errors.Is(err, ErrPulledSessionDirectoryInvalid) {
			t.Fatalf("error = %v, want ErrPulledSessionDirectoryInvalid", err)
		}
		if got, want := err.Error(), "pulled session directory is invalid: directory is required"; got != want {
			t.Fatalf("error = %q, want byte-identical %q", got, want)
		}
	})
	t.Run("non-string value stays rejected", func(t *testing.T) {
		err := validatePulledSessionDirectory([]byte(`{"id":"strict-session","project":"engram","directory":123}`))
		if !errors.Is(err, ErrPulledSessionDirectoryInvalid) {
			t.Fatalf("error = %v, want ErrPulledSessionDirectoryInvalid", err)
		}
		if got, want := err.Error(), "pulled session directory is invalid: directory must be non-blank"; got != want {
			t.Fatalf("error = %q, want byte-identical %q", got, want)
		}
	})
	t.Run("concrete directory still admitted", func(t *testing.T) {
		if err := validatePulledSessionDirectory([]byte(`{"id":"strict-session","project":"engram","directory":"/real/dir"}`)); err != nil {
			t.Fatalf("concrete directory rejected: %v", err)
		}
	})
}

// Matrix item 7 (validation retained): JSON import with an invalid session ID
// and with an invalid ownership mode still fails exactly as today.
func TestImportValidationRetainsIdentityAndOwnershipChecks(t *testing.T) {
	s := newTestStore(t)

	t.Run("invalid session id", func(t *testing.T) {
		_, err := s.Import(&ExportData{
			Version:  currentExportVersion,
			Sessions: []Session{{ID: " \t", Project: "engram", Directory: "/tmp/x"}},
		})
		if !errors.Is(err, ErrSessionIDRequired) {
			t.Fatalf("Import blank id error = %v, want ErrSessionIDRequired", err)
		}
	})

	t.Run("invalid ownership mode", func(t *testing.T) {
		_, err := s.Import(&ExportData{
			Version:  currentExportVersion,
			Sessions: []Session{{ID: "mode-invalid-session", Project: "engram", Directory: "/tmp/x", OwnershipMode: "exclusive"}},
		})
		if !errors.Is(err, ErrInvalidSessionOwnershipMode) {
			t.Fatalf("Import invalid ownership mode error = %v, want ErrInvalidSessionOwnershipMode", err)
		}
	})
}

// Matrix item 2b (JSON null directory): a pulled session upsert whose directory
// value is JSON null must be rejected on the local domain. The rejection goes
// through the real pulled-chunk apply path so the atomicity contract is proven
// end to end: the session is not persisted and the chunk is not recorded as
// imported, leaving the chunk free to be redelivered after a fix.
// RED before the fix: json.Unmarshal folds JSON null into a Go string no-op, so
// the null slipped through as if it were a blank directory.
func TestPulledChunkNullDirectoryRejectedLocallyWithoutSideEffects(t *testing.T) {
	s := newTestStore(t)
	const sessionID = "chunk-null-dir-session"
	mutations := []SyncMutation{{
		Entity:    SyncEntitySession,
		EntityKey: sessionID,
		Op:        SyncOpUpsert,
		Payload:   `{"id":"chunk-null-dir-session","project":"engram","directory":null}`,
	}}
	err := s.ApplyPulledChunk(LocalChunkTargetKey, "chunk-null-dir-1", mutations)
	if !errors.Is(err, ErrPulledSessionDirectoryInvalid) {
		t.Fatalf("ApplyPulledChunk with null directory: err = %v, want ErrPulledSessionDirectoryInvalid", err)
	}
	if _, getErr := s.GetSession(sessionID); !errors.Is(getErr, sql.ErrNoRows) {
		t.Fatalf("session persisted despite null-directory rejection: %v", getErr)
	}
	synced, err := s.GetSyncedChunksForTarget(LocalChunkTargetKey)
	if err != nil {
		t.Fatalf("GetSyncedChunksForTarget: %v", err)
	}
	if synced["chunk-null-dir-1"] {
		t.Fatal("chunk recorded as imported despite null-directory rejection")
	}
	deferred, err := s.ListDeferred(ListDeferredOptions{})
	if err != nil {
		t.Fatalf("ListDeferred: %v", err)
	}
	if len(deferred) != 0 {
		t.Fatalf("deferred rows = %d, want 0 (null directory must fail closed, not quarantine)", len(deferred))
	}
}

// The missing directory key stays admitted on the local domain (distinct from
// JSON null): the local partial-session state has no directory yet, and the
// payload bytes are applied exactly as carried.
func TestPulledChunkMissingDirectoryKeyAppliesLocally(t *testing.T) {
	s := newTestStore(t)
	const sessionID = "chunk-missing-dir-key-session"
	mutations := []SyncMutation{{
		Entity:    SyncEntitySession,
		EntityKey: sessionID,
		Op:        SyncOpUpsert,
		Payload:   `{"id":"chunk-missing-dir-key-session","project":"engram"}`,
	}}
	if err := s.ApplyPulledChunk(LocalChunkTargetKey, "chunk-missing-dir-key-1", mutations); err != nil {
		t.Fatalf("ApplyPulledChunk with missing directory key: %v", err)
	}
	sess, err := s.GetSession(sessionID)
	if err != nil {
		t.Fatalf("GetSession %s: %v", sessionID, err)
	}
	if sess.Directory != "" {
		t.Fatalf("stored directory = %q, want exactly \"\" (missing key decodes to blank)", sess.Directory)
	}
	synced, err := s.GetSyncedChunksForTarget(LocalChunkTargetKey)
	if err != nil {
		t.Fatalf("GetSyncedChunksForTarget: %v", err)
	}
	if !synced["chunk-missing-dir-key-1"] {
		t.Fatal("chunk with missing directory key must be recorded as imported")
	}
}

// Deterministic validator table for the local pull-path admission rule: ONLY a
// missing directory key or a JSON string value (blank included) is admitted.
// JSON null and every non-string value are rejected with
// ErrPulledSessionDirectoryInvalid.
func TestValidatePulledSessionDirectoryLocalRejectsNullAndNonString(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		wantErr bool
	}{
		{name: "null rejected", payload: `{"id":"s","project":"engram","directory":null}`, wantErr: true},
		{name: "number rejected", payload: `{"id":"s","project":"engram","directory":42}`, wantErr: true},
		{name: "array rejected", payload: `{"id":"s","project":"engram","directory":["/a"]}`, wantErr: true},
		{name: "object rejected", payload: `{"id":"s","project":"engram","directory":{"path":"/a"}}`, wantErr: true},
		{name: "missing key admitted", payload: `{"id":"s","project":"engram"}`},
		{name: "blank string admitted", payload: `{"id":"s","project":"engram","directory":""}`},
		{name: "whitespace string admitted", payload: `{"id":"s","project":"engram","directory":" \t "}`},
		{name: "concrete string admitted", payload: `{"id":"s","project":"engram","directory":"/real/dir"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validatePulledSessionDirectoryLocal([]byte(tt.payload))
			if tt.wantErr && !errors.Is(err, ErrPulledSessionDirectoryInvalid) {
				t.Fatalf("error = %v, want ErrPulledSessionDirectoryInvalid", err)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("error = %v, want nil", err)
			}
		})
	}
}
