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
	5: "5c7ceb07936a1efa43df120119cb44c4d653514fd0090db591d1a9588b65c6ee",
	6: "eec183e125f76d214d420d15c55ac52e60b21007c31da90155868aecbea9983d",
	7: "1425d98f1bbd80d7d4518558abf6e3c1db964505735b402a5134f9e1d71ae6e7",
	8: "c557b7ce6ed6fa249785583ebb071d22b9b02796630d3ce3bdec55ca7db5bb10",
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
		t.Fatalf("migration source fingerprint = %s for schemaVersion %d; go/format output can vary by Go toolchain, so first rerun with the repository toolchain before changing schemaVersion. If the migration source changed under that toolchain, bump schemaVersion and update migrationVersionFingerprints", actual, schemaVersion)
	}
}
