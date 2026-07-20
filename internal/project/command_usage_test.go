package project

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"testing"
)

func TestProjectDetectionGitCommandsUsePlatformHelper(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test source")
	}

	file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(filepath.Dir(testFile), "detect.go"), nil, 0)
	if err != nil {
		t.Fatalf("parse detect.go: %v", err)
	}

	tests := []struct {
		name string
	}{
		{name: "detectGitRootDir"},
		{name: "detectFromGitRemote"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var function *ast.FuncDecl
			for _, declaration := range file.Decls {
				candidate, ok := declaration.(*ast.FuncDecl)
				if ok && candidate.Name.Name == tt.name {
					function = candidate
					break
				}
			}
			if function == nil {
				t.Fatalf("function %s not found", tt.name)
			}

			helperCalls := 0
			ast.Inspect(function.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				identifier, ok := call.Fun.(*ast.Ident)
				if ok && identifier.Name == "newProjectCommandContext" {
					helperCalls++
				}
				return true
			})
			if helperCalls != 1 {
				t.Fatalf("%s uses newProjectCommandContext %d times; want 1", tt.name, helperCalls)
			}
		})
	}
}
