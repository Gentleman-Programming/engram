package store

import (
	"context"
	"database/sql/driver"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

const storeLeaseProbeEnv = "ENGRAM_STORE_LEASE_EXCLUSIVE_PROBE"

func TestStoreLeaseExclusiveProbe(t *testing.T) {
	if os.Getenv(storeLeaseProbeEnv) != "1" {
		return
	}

	file, err := os.OpenFile(filepath.Join(os.Getenv("ENGRAM_STORE_LEASE_DIR"), ".engram.store.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		os.Exit(2)
	}
	defer file.Close()
	if err := lockStoreLease(file, true, true); err != nil {
		os.Exit(1)
	}
	if err := unlockStoreLease(file); err != nil {
		os.Exit(2)
	}
}

func exclusiveStoreLeaseAvailable(t *testing.T, dataDir string) bool {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestStoreLeaseExclusiveProbe$")
	cmd.Env = append(os.Environ(), storeLeaseProbeEnv+"=1", "ENGRAM_STORE_LEASE_DIR="+dataDir)
	return cmd.Run() == nil
}

func TestStoreLifetimesCoordinateLeases(t *testing.T) {
	dir := t.TempDir()
	first, err := New(FallbackConfig(dir))
	if err != nil {
		t.Fatalf("open first store: %v", err)
	}
	second, err := New(FallbackConfig(dir))
	if err != nil {
		_ = first.Close()
		t.Fatalf("open second store: %v", err)
	}
	if exclusiveStoreLeaseAvailable(t, dir) {
		_ = second.Close()
		_ = first.Close()
		t.Fatal("exclusive lease acquired while two stores were live")
	}
	if err := first.Close(); err != nil {
		_ = second.Close()
		t.Fatalf("close first store: %v", err)
	}
	if exclusiveStoreLeaseAvailable(t, dir) {
		_ = second.Close()
		t.Fatal("exclusive lease acquired while second store was live")
	}
	if err := second.Close(); err != nil {
		t.Fatalf("close second store: %v", err)
	}
	if !exclusiveStoreLeaseAvailable(t, dir) {
		t.Fatal("exclusive lease remained blocked after both stores closed")
	}
}

func TestBorrowedConnectionRetainsLeaseAfterStoreClose(t *testing.T) {
	dir := t.TempDir()
	s, err := New(FallbackConfig(dir))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	conn, err := s.DB().Conn(context.Background())
	if err != nil {
		_ = s.Close()
		t.Fatalf("borrow connection: %v", err)
	}
	if err := s.Close(); err != nil {
		_ = conn.Close()
		t.Fatalf("close store: %v", err)
	}
	if exclusiveStoreLeaseAvailable(t, dir) {
		_ = conn.Close()
		t.Fatal("exclusive lease acquired while borrowed connection was live")
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("close borrowed connection: %v", err)
	}
	if !exclusiveStoreLeaseAvailable(t, dir) {
		t.Fatal("exclusive lease remained blocked after borrowed connection closed")
	}
}

type failingGenerationDriver struct{ err error }

func (d failingGenerationDriver) Open(string) (driver.Conn, error) {
	return nil, d.err
}

func TestGenerationConnectionOpenFailureReleasesLease(t *testing.T) {
	dir := t.TempDir()
	openErr := errors.New("open failed")
	connector := generationConnector{
		dsn:        filepath.Join(dir, "engram.db"),
		driver:     failingGenerationDriver{err: openErr},
		generation: &databaseGeneration{},
	}
	_, err := connector.Connect(context.Background())
	if !errors.Is(err, openErr) {
		t.Fatalf("connect error = %v, want wrapped open error", err)
	}
	if !exclusiveStoreLeaseAvailable(t, dir) {
		t.Fatal("exclusive lease remained blocked after connection open failure")
	}
}

type leaseCloseTestConn struct{ closeErr error }

func (c leaseCloseTestConn) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (c leaseCloseTestConn) Close() error                        { return c.closeErr }
func (leaseCloseTestConn) Begin() (driver.Tx, error)             { return nil, driver.ErrSkip }

func TestGenerationConnectionCloseReleasesLeaseAfterCloseError(t *testing.T) {
	dir := t.TempDir()
	lease, err := acquireStoreLease(dir, false)
	if err != nil {
		t.Fatalf("acquire connection lease: %v", err)
	}
	closeErr := errors.New("close failed")
	err = (generationConn{Conn: leaseCloseTestConn{closeErr: closeErr}, lease: lease}).Close()
	if !errors.Is(err, closeErr) {
		t.Fatalf("close error = %v, want wrapped close error", err)
	}
	if !exclusiveStoreLeaseAvailable(t, dir) {
		t.Fatal("exclusive lease remained blocked after connection close error")
	}
}

func TestStoreLeasePathIsInsideCanonicalDataDirectory(t *testing.T) {
	dataDir := t.TempDir()
	canonicalDir, err := canonicalStoreLeaseDir(dataDir)
	if err != nil {
		t.Fatalf("resolve data directory: %v", err)
	}
	lockPath, err := storeLeasePath(dataDir)
	if err != nil {
		t.Fatalf("derive lease path: %v", err)
	}
	if want := filepath.Join(canonicalDir, ".engram.store.lock"); lockPath != want {
		t.Fatalf("lease path = %q, want %q", lockPath, want)
	}
}
