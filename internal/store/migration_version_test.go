package store

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// migrationVersionFingerprints maps each schema generation to the formatted
// source of its migration functions. When a migration step changes, bump
// schemaVersion and record the new fingerprint so existing databases run it.
var migrationVersionFingerprints = map[int]string{
	2: "8a8e7471b6f3ae6be31463efae14ff8484be6e847f517bd4dd4d0b65e21166b9",
}

func TestSchemaVersionMatchesMigrationFingerprint(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate migration version test source")
	}
	dir := filepath.Dir(thisFile)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read store source directory: %v", err)
	}

	fset := token.NewFileSet()
	var functions []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || !strings.HasPrefix(fn.Name.Name, "migrate") {
				continue
			}
			var formatted bytes.Buffer
			if err := format.Node(&formatted, fset, fn); err != nil {
				t.Fatalf("format migration %s: %v", fn.Name.Name, err)
			}
			functions = append(functions, formatted.String())
		}
	}
	sort.Strings(functions)
	actual := fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(functions, "\n"))))
	expected, ok := migrationVersionFingerprints[schemaVersion]
	if !ok || expected != actual {
		t.Fatalf("migration source fingerprint = %s for schemaVersion %d; bump schemaVersion and update migrationVersionFingerprints", actual, schemaVersion)
	}
}
