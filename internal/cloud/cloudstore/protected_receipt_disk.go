package cloudstore

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

const protectedReceiptVersion = 3
const protectedReceiptBytes = 16 << 10
const protectedReceiptName = "startup-receipt-v3.json"
const protectedDeltaTimeout = 2 * time.Second

type protectedDiskReceipt struct {
	Version  int      `json:"protected_receipt_version"`
	Projects []string `json:"projects"`
	Snapshot string   `json:"snapshot"`
	Identity string   `json:"identity"`
	Witness  string   `json:"witness"`
}

// NewProtectedStartupReceiptVerifier checks for a prepared receipt in a
// private, operator-created directory. Startup uses ordinary repair whenever
// the existing receipt and audit feed cannot prove coverage.
func NewProtectedStartupReceiptVerifier(directory string) *StartupReceiptVerifier {
	if directory == "" {
		return nil
	}
	return &StartupReceiptVerifier{verify: func(ctx context.Context, db *sql.DB, projects []string) (bool, error) {
		root, err := openPrivateReceiptDirectory(directory)
		if err != nil {
			fmt.Fprintln(os.Stderr, "engram cloud startup: protected receipt directory unavailable")
			return false, err
		}
		defer func() { _ = root.Close() }()
		deltaCtx, cancel := context.WithTimeout(ctx, protectedDeltaTimeout)
		reused := reuseProtectedDiskReceipt(deltaCtx, db, projects, root)
		deltaErr := deltaCtx.Err()
		cancel()
		if reused {
			return true, nil
		}
		if deltaErr != nil {
			if deltaErr == context.DeadlineExceeded {
				fmt.Fprintln(os.Stderr, "engram cloud startup: protected receipt delta timed out")
			}
			return false, deltaErr
		}
		return false, nil
	}}
}

// PrepareProtectedStartupReceipt checks the existing PostgreSQL snapshot and
// stores the verified result. It does not initialize or repair PostgreSQL.
func PrepareProtectedStartupReceipt(ctx context.Context, cfg cloud.Config, projects []string) error {
	if len(protectedProjects(projects)) == 0 {
		return fmt.Errorf("cloud startup receipt requires an explicit allowed-project list")
	}
	root, err := openPrivateReceiptDirectory(cfg.StartupReceiptDir)
	if err != nil {
		return fmt.Errorf("cloud startup receipt requires an existing private Linux directory")
	}
	defer func() { _ = root.Close() }()
	cs, err := openCloudStore(ctx, cfg)
	if err != nil {
		return fmt.Errorf("cloud startup receipt could not open PostgreSQL")
	}
	defer func() { _ = cs.Close() }()
	receipt, err := captureProtectedReceipt(ctx, cs.db, projects)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("cloud startup receipt could not prove coverage for the configured projects")
	}
	if err := writeProtectedDiskReceipt(root, protectedReceiptName, receipt); err != nil {
		return fmt.Errorf("cloud startup receipt could not store the verified receipt in the configured directory")
	}
	fmt.Fprintln(os.Stderr, "engram cloud startup: stored protected no-op receipt")
	return nil
}

func protectedProjects(projects []string) []string {
	// Runtime provides normalized project identities. Sort a private copy for
	// the receipt without changing the order used by ordinary fallback.
	for _, project := range projects {
		normalized, _ := store.NormalizeProject(project)
		if project == "" || project == "*" || project != normalized {
			return nil
		}
	}
	ordered := slices.Clone(projects)
	slices.Sort(ordered)
	return slices.Compact(ordered)
}

func reuseProtectedDiskReceipt(ctx context.Context, db *sql.DB, projects []string, root *os.Root) bool {
	projects = protectedProjects(projects)
	if len(projects) == 0 {
		return false
	}
	prior, err := readProtectedDiskReceipt(root, protectedReceiptName)
	if err != nil {
		return false
	}
	if !slices.Equal(prior.projects, projects) {
		fmt.Fprintln(os.Stderr, "engram cloud startup: protected receipt scope changed")
		return false
	}
	current, events, err := tryProtectedDelta(ctx, db, prior)
	if err != nil {
		fmt.Fprintf(os.Stderr, "engram cloud startup: protected delta refused (events=%d)\n", len(events))
		return false
	}
	if current.witness != prior.witness {
		if err := writeProtectedDiskReceipt(root, protectedReceiptName, current); err != nil {
			return false
		}
	}
	fmt.Fprintf(os.Stderr, "engram cloud startup: protected receipt reused (events=%d)\n", len(events))
	return true
}

func readProtectedDiskReceipt(root *os.Root, name string) (protectedReceipt, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return protectedReceipt{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > protectedReceiptBytes {
		return protectedReceipt{}, errReceiptAppendUnproven
	}
	file, err := root.Open(name)
	if err != nil {
		return protectedReceipt{}, err
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil {
		return protectedReceipt{}, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return protectedReceipt{}, errReceiptAppendUnproven
	}
	data, err := io.ReadAll(io.LimitReader(file, protectedReceiptBytes+1))
	if err != nil {
		return protectedReceipt{}, err
	}
	var disk protectedDiskReceipt
	if len(data) > protectedReceiptBytes || json.Unmarshal(data, &disk) != nil || disk.Version != protectedReceiptVersion || len(disk.Projects) == 0 || disk.Snapshot == "" || disk.Identity == "" || disk.Witness == "" {
		return protectedReceipt{}, errReceiptAppendUnproven
	}
	if !slices.Equal(protectedProjects(disk.Projects), disk.Projects) {
		return protectedReceipt{}, errReceiptAppendUnproven
	}
	return protectedReceipt{disk.Projects, disk.Snapshot, disk.Identity, disk.Witness}, nil
}

func writeProtectedDiskReceipt(root *os.Root, name string, receipt protectedReceipt) error {
	disk := protectedDiskReceipt{protectedReceiptVersion, receipt.projects, receipt.snapshot, receipt.identity, receipt.witness}
	data, err := json.Marshal(disk)
	if err != nil {
		return err
	}
	if len(data) > protectedReceiptBytes {
		return errReceiptAppendUnproven
	}
	info, err := root.Lstat(name)
	if err == nil && !info.Mode().IsRegular() {
		return errReceiptAppendUnproven
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	temporary := ".protected-receipt-" + rand.Text()
	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		_ = file.Close()
		_ = root.Remove(temporary)
	}()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := root.Rename(temporary, name); err != nil {
		return err
	}
	return syncReceiptDirectory(root)
}
