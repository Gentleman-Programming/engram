package cloudstore

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v2/internal/cloud"
)

// openTestCloudStore opens a CloudStore using CLOUDSTORE_TEST_DSN env var.
// If the env var is not set, the test is skipped.
func openTestCloudStore(t *testing.T) *CloudStore {
	t.Helper()
	dsn := os.Getenv("CLOUDSTORE_TEST_DSN")
	if dsn == "" {
		t.Skip("CLOUDSTORE_TEST_DSN not set — skipping integration test (requires Postgres)")
	}
	cs, err := New(cloud.Config{DSN: dsn})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

// TestProjectSyncControlPersists round-trips SetProjectSyncEnabled and
// verifies GetProjectSyncControl returns the updated state. Satisfies REQ-104.
func TestProjectSyncControlPersists(t *testing.T) {
	cs := openTestCloudStore(t)

	const project = "test-project-controls"

	// Default: enabled (no row).
	enabled, err := cs.IsProjectSyncEnabled(project)
	if err != nil {
		t.Fatalf("IsProjectSyncEnabled: %v", err)
	}
	if !enabled {
		t.Errorf("expected default sync enabled=true for unknown project, got false")
	}

	// Disable sync.
	if err := cs.SetProjectSyncEnabled(project, false, "operator", "maintenance"); err != nil {
		t.Fatalf("SetProjectSyncEnabled(false): %v", err)
	}

	enabled, err = cs.IsProjectSyncEnabled(project)
	if err != nil {
		t.Fatalf("IsProjectSyncEnabled after disable: %v", err)
	}
	if enabled {
		t.Errorf("expected sync enabled=false after SetProjectSyncEnabled(false), got true")
	}

	ctrl, err := cs.GetProjectSyncControl(project)
	if err != nil {
		t.Fatalf("GetProjectSyncControl: %v", err)
	}
	if ctrl == nil {
		t.Fatalf("expected non-nil ProjectSyncControl after SetProjectSyncEnabled")
	}
	if ctrl.SyncEnabled {
		t.Errorf("expected SyncEnabled=false, got true")
	}
	if ctrl.PausedReason == nil || *ctrl.PausedReason != "maintenance" {
		t.Errorf("expected PausedReason=%q, got %v", "maintenance", ctrl.PausedReason)
	}

	// Re-enable.
	if err := cs.SetProjectSyncEnabled(project, true, "operator", ""); err != nil {
		t.Fatalf("SetProjectSyncEnabled(true): %v", err)
	}
	enabled, err = cs.IsProjectSyncEnabled(project)
	if err != nil {
		t.Fatalf("IsProjectSyncEnabled after re-enable: %v", err)
	}
	if !enabled {
		t.Errorf("expected sync enabled=true after re-enable, got false")
	}
}

// TestProjectSyncControlUnknownProjectDefaultsEnabled asserts that IsProjectSyncEnabled
// for a project with no control record returns true (safe default). Satisfies REQ-104.
func TestProjectSyncControlUnknownProjectDefaultsEnabled(t *testing.T) {
	cs := openTestCloudStore(t)

	enabled, err := cs.IsProjectSyncEnabled("no-such-project-xyz-" + t.Name())
	if err != nil {
		t.Fatalf("IsProjectSyncEnabled: %v", err)
	}
	if !enabled {
		t.Errorf("expected default enabled=true for unknown project, got false")
	}
}

// ─── BW8: IsProjectSyncEnabled fail-closed on DB error ────────────────────────

// errorDriver is a database/sql driver that fails every query.
// Used to test fail-closed behavior without a real Postgres instance.
type errorDriver struct{}

func (d errorDriver) Open(_ string) (driver.Conn, error) { return &errorConn{}, nil }

type errorConn struct{}

func (c *errorConn) Prepare(query string) (driver.Stmt, error) { return &errorStmt{}, nil }
func (c *errorConn) Close() error                              { return nil }
func (c *errorConn) Begin() (driver.Tx, error)                 { return nil, errors.New("tx not supported") }

type errorStmt struct{}

func (s *errorStmt) Close() error  { return nil }
func (s *errorStmt) NumInput() int { return -1 }
func (s *errorStmt) Exec(_ []driver.Value) (driver.Result, error) {
	return nil, errors.New("driver: intentional failure")
}
func (s *errorStmt) Query(_ []driver.Value) (driver.Rows, error) {
	return nil, errors.New("driver: intentional failure")
}

func init() {
	sql.Register("cloudstore-error-driver", errorDriver{})
}

// TestIsProjectSyncEnabledReturnsFalseOnDBError verifies BW8:
// IsProjectSyncEnabled must return (false, error) on DB errors,
// not (true, error) — fail-open on DB errors is a security bug.
func TestIsProjectSyncEnabledReturnsFalseOnDBError(t *testing.T) {
	db, err := sql.Open("cloudstore-error-driver", "dsn")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	cs := &CloudStore{db: db}

	enabled, err := cs.IsProjectSyncEnabled("some-project")
	if err == nil {
		t.Fatal("expected error from DB failure, got nil")
	}
	if enabled {
		t.Fatalf("expected IsProjectSyncEnabled to return false on DB error (fail-closed), got true")
	}
}

// ─── BW3: InsertMutationBatch atomicity ──────────────────────────────────────

// partialFailDriver fails INSERT statements after the first one succeeds.
//
// BR2-2: The driver is registered once in init() with a stable name. Each test
// resets state via resetPartialFailDriver() so -count=2 is safe.
//
// BR2-6: Failure is triggered by inspecting the SQL text (fails any second
// INSERT), not by a fragile call counter. This decouples the test from internal
// query ordering so that adding auxiliary queries (e.g., RETURNING, SAVEPOINT)
// does not break the assertion.
type partialFailDriver struct {
	insertCount int // incremented for every INSERT statement prepared
	failAfter   int // fail when insertCount > failAfter (0 = fail on 2nd INSERT)
	lastTx      *partialFailTx
}

// partialFailDriverSingleton is the global instance registered with database/sql.
// Tests MUST reset its state before use via resetPartialFailDriver().
var partialFailDriverSingleton = &partialFailDriver{}

func init() {
	sql.Register("cloudstore-partial-fail-driver", partialFailDriverSingleton)
}

// resetPartialFailDriver resets the singleton so it allows the first INSERT
// and fails on every subsequent INSERT. failAfter=0 means: succeed 1 INSERT,
// fail on the 2nd.
func resetPartialFailDriver(failAfter int) {
	partialFailDriverSingleton.insertCount = 0
	partialFailDriverSingleton.failAfter = failAfter
	partialFailDriverSingleton.lastTx = nil
}

func (d *partialFailDriver) Open(_ string) (driver.Conn, error) { return &partialFailConn{d: d}, nil }

type partialFailConn struct{ d *partialFailDriver }

// Prepare inspects the SQL text. For INSERT statements, it increments insertCount.
// BR2-6: Trigger is based on SQL content, not call order, so it is stable even
// if InsertMutationBatch's internals change (e.g. adding RETURNING clauses).
func (c *partialFailConn) Prepare(query string) (driver.Stmt, error) {
	isInsert := strings.HasPrefix(strings.ToUpper(strings.TrimSpace(query)), "INSERT")
	shouldFail := false
	if isInsert {
		c.d.insertCount++
		shouldFail = c.d.insertCount > c.d.failAfter
	}
	return &partialFailStmt{d: c.d, shouldFail: shouldFail}, nil
}
func (c *partialFailConn) Close() error              { return nil }
func (c *partialFailConn) Begin() (driver.Tx, error) { return &partialFailTx{d: c.d}, nil }

type partialFailTx struct {
	d          *partialFailDriver
	committed  bool
	rolledBack bool
}

func (t *partialFailTx) Commit() error {
	t.committed = true
	t.d.lastTx = t
	return nil
}
func (t *partialFailTx) Rollback() error {
	t.rolledBack = true
	t.d.lastTx = t
	return nil
}

type partialFailStmt struct {
	d          *partialFailDriver
	shouldFail bool
}

func (s *partialFailStmt) Close() error  { return nil }
func (s *partialFailStmt) NumInput() int { return -1 }
func (s *partialFailStmt) Exec(_ []driver.Value) (driver.Result, error) {
	if s.shouldFail {
		return nil, errors.New("driver: intentional INSERT failure")
	}
	return driver.RowsAffected(1), nil
}
func (s *partialFailStmt) Query(_ []driver.Value) (driver.Rows, error) {
	if s.shouldFail {
		return nil, errors.New("driver: intentional INSERT failure")
	}
	return &seqRows{seq: s.d.insertCount}, nil
}

type seqRows struct {
	seq  int
	done bool
}

func (r *seqRows) Columns() []string { return []string{"seq"} }
func (r *seqRows) Close() error      { return nil }
func (r *seqRows) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	dest[0] = int64(r.seq)
	return nil
}

// TestInsertMutationBatchIsAtomicOnFailure verifies BW3:
// If one insert in the batch fails, no entries from that batch should be committed.
// The function must wrap the batch in a transaction and rollback on failure.
func TestInsertMutationBatchIsAtomicOnFailure(t *testing.T) {
	// BR2-2: Use the singleton registered in init(). Reset state before each run
	// so -count=2 and parallel tests are safe.
	// BR2-6: failAfter=0 → succeed 1st INSERT, fail 2nd INSERT (SQL-text based).
	driverName := "cloudstore-partial-fail-driver"
	resetPartialFailDriver(0) // succeed 1 INSERT, fail on 2nd INSERT
	d := partialFailDriverSingleton

	db, err := sql.Open(driverName, "dsn")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	cs := &CloudStore{db: db}

	batch := []MutationEntry{
		{Project: "proj-a", Entity: "obs", EntityKey: "k1", Op: "upsert", Payload: json.RawMessage(`{}`)},
		{Project: "proj-a", Entity: "obs", EntityKey: "k2", Op: "upsert", Payload: json.RawMessage(`{}`)},
	}

	// BW3: If the batch fails mid-way, InsertMutationBatch must return an error.
	seqs, err := cs.InsertMutationBatch(context.Background(), batch)
	if err == nil {
		t.Fatalf("expected error on partial batch failure, got nil (seqs=%v)", seqs)
	}

	// BW3 ATOMICITY: The transaction must have been rolled back, NOT committed.
	// This is the core property: entries 1..N-1 must not be persisted on partial failure.
	if d.lastTx == nil {
		t.Fatal("expected a transaction to have been started (Begin was never called)")
	}
	if !d.lastTx.rolledBack {
		t.Fatal("expected transaction to be rolled back on batch failure (atomicity violated: entry 1 was committed without entry 2)")
	}
	if d.lastTx.committed {
		t.Fatal("expected no commit on batch failure")
	}
}

// TestProjectSyncControlListIncludesKnownChunkProjects asserts that
// ListProjectSyncControls returns projects that appear in cloud_chunks
// even if they have no explicit control row. Satisfies REQ-104.
func TestProjectSyncControlListIncludesKnownChunkProjects(t *testing.T) {
	cs := openTestCloudStore(t)

	// Insert a minimal chunk directly to plant a known project.
	const chunkProject = "proj-controls-integration-x"
	_, err := cs.db.Exec(`
		INSERT INTO cloud_chunks (project_name, chunk_id, created_by, payload, sessions_count, observations_count, prompts_count)
		VALUES ($1, $2, $3, $4::jsonb, 0, 0, 0)
		ON CONFLICT DO NOTHING
	`, chunkProject, "test-chunk-controls-"+t.Name(), "test-user", `{"sessions":[],"observations":[],"prompts":[]}`)
	if err != nil {
		t.Fatalf("seed chunk for project %q: %v", chunkProject, err)
	}
	t.Cleanup(func() {
		cs.db.Exec(`DELETE FROM cloud_chunks WHERE project_name = $1`, chunkProject)
	})

	controls, err := cs.ListProjectSyncControls()
	if err != nil {
		t.Fatalf("ListProjectSyncControls: %v", err)
	}

	found := false
	for _, c := range controls {
		if c.Project == chunkProject {
			found = true
			if !c.SyncEnabled {
				t.Errorf("expected default SyncEnabled=true for project with no control row, got false")
			}
			break
		}
	}
	if !found {
		t.Errorf("expected project %q in ListProjectSyncControls (via UNION with cloud_chunks), not found in %d controls", chunkProject, len(controls))
	}
}
