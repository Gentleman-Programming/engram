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
	2: "db0bd6c4dcb6520a2165ba63c6ce228c8dd5baeaf66a8be0d53b658e81222138",
	3: "20de316386769239e96f3b977f33195d05aecebe795834296086f1713f92ee11",
	4: "e4ed27435b9e8d4fda41add3f1c797901fb46ead095409dd97574b3cc6fd8b00",
}

// migrationFingerprintHelperFunctions names migration-relevant helpers whose
// names do not begin with "migrate". Keep this list explicit so unrelated
// helpers do not make schema-version bumps noisy.
var migrationFingerprintHelperFunctions = map[string]bool{
	"addColumnIfNotExists": true,
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
			if !ok || (!strings.HasPrefix(fn.Name.Name, "migrate") && !migrationFingerprintHelperFunctions[fn.Name.Name]) {
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
