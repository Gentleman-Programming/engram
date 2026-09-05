package command

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSubprocessesUseNewContext(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test source")
	}
	internalDir := filepath.Dir(filepath.Dir(testFile))

	tests := []struct {
		name      string
		path      string
		wantCalls int
	}{
		{name: "project detection", path: filepath.Join(internalDir, "project", "detect.go"), wantCalls: 3},
		{name: "Claude CLI", path: filepath.Join(internalDir, "llm", "claude.go"), wantCalls: 1},
		{name: "setup commands", path: filepath.Join(internalDir, "setup", "setup.go"), wantCalls: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file, err := parser.ParseFile(token.NewFileSet(), tt.path, nil, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", tt.path, err)
			}

			calls := 0
			ast.Inspect(file, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || selector.Sel.Name != "NewContext" {
					return true
				}
				pkg, ok := selector.X.(*ast.Ident)
				if ok && pkg.Name == "command" {
					calls++
				}
				return true
			})
			if calls != tt.wantCalls {
				t.Fatalf("command.NewContext calls = %d; want %d", calls, tt.wantCalls)
			}
		})
	}
}
