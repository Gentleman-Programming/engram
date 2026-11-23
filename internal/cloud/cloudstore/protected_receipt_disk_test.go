package cloudstore

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestProtectedReceiptDoesNotDependOnWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	root := openReceiptTestRoot(t, dir)
	other := t.TempDir()
	t.Chdir(other)
	const name = "receipt.json"
	value := protectedReceipt{projects: []string{"fixture"}, snapshot: "1:2:", identity: "fixture-db", witness: "fixture-witness"}
	if err := writeProtectedDiskReceipt(root, name, value); err != nil {
		t.Fatal(err)
	}
	updated := value
	updated.snapshot = "2:3:"
	got, err := readProtectedDiskReceipt(root, name)
	if err != nil || !slices.Equal(got.projects, value.projects) || got.snapshot != value.snapshot || got.identity != value.identity {
		t.Fatalf("original receipt changed: %+v, %v", got, err)
	}
	if err := writeProtectedDiskReceipt(root, name, updated); err != nil {
		t.Fatal(err)
	}
	got, err = readProtectedDiskReceipt(root, name)
	if err != nil || got.snapshot != updated.snapshot {
		t.Fatalf("anchored replacement: %+v, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(other, name)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("receipt in working directory: %v", err)
	}
}

func openReceiptTestRoot(t *testing.T, dir string) *os.Root {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root
}

func TestProtectedReceiptRejectsNonRegularTarget(t *testing.T) {
	dir := t.TempDir()
	root := openReceiptTestRoot(t, dir)
	const name = "receipt.json"
	receiptDir := filepath.Join(dir, name)
	if err := os.Mkdir(receiptDir, 0o700); err != nil {
		t.Fatal(err)
	}
	value := protectedReceipt{projects: []string{"fixture"}, snapshot: "1:2:", identity: "fixture-db", witness: "fixture-witness"}
	if _, err := readProtectedDiskReceipt(root, name); !errors.Is(err, errReceiptAppendUnproven) {
		t.Fatalf("directory read: %v", err)
	}
	if err := writeProtectedDiskReceipt(root, name, value); !errors.Is(err, errReceiptAppendUnproven) {
		t.Fatalf("directory replacement: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || !entries[0].IsDir() {
		t.Fatalf("receipt parent changed: %v, %v", entries, err)
	}
}
