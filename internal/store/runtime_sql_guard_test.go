package store

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRuntimeSQLBoundaryGuard prevents the generation-regression entry points
// from quietly returning to direct runtime database access. Startup, health,
// close, hook, and DB test-access methods are intentionally outside this guard.
func TestRuntimeSQLBoundaryGuard(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		file    string
		methods map[string]string
	}{
		{"store.go", map[string]string{"GetSession": "withRead", "setObservationPinned": "withTx", "UnenrollProject": "withTx"}},
		{"relations.go", map[string]string{"FindCandidates": "withRead"}},
	} {
		source, err := os.ReadFile(filepath.Join(".", tc.file))
		if err != nil {
			t.Fatalf("read %s: %v", tc.file, err)
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, tc.file, source, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", tc.file, err)
		}
		for _, declaration := range file.Decls {
			fn, ok := declaration.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || fn.Name == nil {
				continue
			}
			boundary, guarded := tc.methods[fn.Name.Name]
			if !guarded {
				continue
			}
			start := fset.Position(fn.Pos()).Offset
			end := fset.Position(fn.End()).Offset
			body := string(source[start:end])
			if !strings.Contains(body, "s."+boundary+"(") {
				t.Fatalf("%s must use the %s runtime boundary", fn.Name.Name, boundary)
			}
		}
	}
}
