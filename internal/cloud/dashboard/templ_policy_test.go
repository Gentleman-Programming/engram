package dashboard

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func TestTemplGenerationIsDashboardScoped(t *testing.T) {
	if testing.Short() {
		t.Skip("invokes the pinned templ tool")
	}
	root := t.TempDir()
	for name, source := range map[string]string{
		"internal/cloud/dashboard/probe.templ":        "package dashboard\n\ntempl probe(value string) {\n<div>{ value }</div>\n}\n",
		"internal/cloud/dashboard/nested/probe.templ": "package dashboard\n\ntempl nestedProbe(value string) {\n<div>{ value }</div>\n}\n",
		"outside/other.templ":                         "package outside\n\ntempl other() {\n<div>outside</div>\n}\n",
	} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(source), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/isolated\n\ngo 1.25\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// Resolve the pinned tool from this repository, but generate only in the
	// isolated fixture module. Replace the documented dashboard path with it.
	args := strings.Fields(templRuntimePolicy().GenerateCommand)
	if len(args) != 6 || strings.Join(args[:4], " ") != "go tool templ generate" || args[4] != "-path" {
		t.Fatalf("expected scoped generator command, got %q", templRuntimePolicy().GenerateCommand)
	}
	args[5] = filepath.Join(root, "internal", "cloud", "dashboard")
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = "../../.."
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("generate isolated fixture: %v\n%s", err, output)
	}
	if _, err := os.Stat(filepath.Join(root, "internal/cloud/dashboard/probe_templ.go")); err != nil {
		t.Fatalf("dashboard source was not generated: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "outside/other_templ.go")); !os.IsNotExist(err) {
		t.Fatalf("out-of-dashboard source generated: %v", err)
	}
	generated, err := os.ReadFile(filepath.Join(root, "internal/cloud/dashboard/probe_templ.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(generated), "FileName: `probe.templ`") {
		t.Fatal("generated diagnostics must use the dashboard source basename")
	}
	if strings.Contains(string(generated), "FileName: `internal/cloud/dashboard/probe.templ`") {
		t.Fatal("generated diagnostics must not retain repository-relative paths")
	}
	nestedGenerated, err := os.ReadFile(filepath.Join(root, "internal/cloud/dashboard/nested/probe_templ.go"))
	if err != nil {
		t.Fatalf("nested dashboard source was not generated: %v", err)
	}
	if !strings.Contains(string(nestedGenerated), "FileName: `nested/probe.templ`") {
		t.Fatal("nested diagnostics must be relative to the dashboard path")
	}
}

func TestTemplRuntimePolicyIsDeterministic(t *testing.T) {
	policy := templRuntimePolicy()

	if policy.Mode != "checked-in-generated" {
		t.Fatalf("expected deterministic templ mode checked-in-generated, got %q", policy.Mode)
	}
	if policy.RuntimeGenerationAllowed {
		t.Fatal("expected runtime generation to be disabled")
	}
	if policy.GenerateCommand != "go tool templ generate -path ./internal/cloud/dashboard" {
		t.Fatalf("expected module-pinned root generate command, got %q", policy.GenerateCommand)
	}
	mod, err := os.ReadFile("../../../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`(?m)^tool github\.com/a-h/templ/cmd/templ(?:\r)?$`).Match(mod) {
		t.Fatal("go.mod must register the templ tool")
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var directives []string
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		content, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, directive := range regexp.MustCompile(`(?m)^//go:generate .+`).FindAllString(string(content), -1) {
			directives = append(directives, strings.TrimSpace(directive))
		}
	}
	if len(directives) != 1 || directives[0] != "//go:generate go -C ../../.. tool templ generate -path ./internal/cloud/dashboard" {
		t.Fatalf("expected one root-relative module tool generation directive, got %q", directives)
	}
}

// TestStaticAssetByteFloors asserts that the embedded StaticFS contains real
// (non-stub) static assets. Satisfies REQ-100.
func TestStaticAssetByteFloors(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		minBytes int64
	}{
		{
			name:     "htmx.min.js",
			path:     "static/htmx.min.js",
			minBytes: 40_000,
		},
		{
			name:     "pico.min.css",
			path:     "static/pico.min.css",
			minBytes: 60_000,
		},
		{
			name:     "styles.css",
			path:     "static/styles.css",
			minBytes: 20_000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, err := fs.Stat(StaticFS, tt.path)
			if err != nil {
				t.Fatalf("fs.Stat(%q) failed: %v\nPossible cause: static asset not embedded or path is wrong.", tt.path, err)
			}
			if info.Size() < tt.minBytes {
				t.Fatalf(
					"%s size = %d bytes; expected >= %d bytes.\nPossible cause: the file is still a stub. Copy the real asset from engram-cloud/internal/cloud/dashboard/static/.",
					tt.path, info.Size(), tt.minBytes,
				)
			}
		})
	}
}

// TestTemplGeneratedFilesAreCheckedIn asserts that the templ-generated *_templ.go
// files are committed alongside the .templ sources. Satisfies REQ-101.
func TestTemplGeneratedFilesAreCheckedIn(t *testing.T) {
	_, callerFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	pkgDir := filepath.Dir(callerFile)

	headerRe := regexp.MustCompile(`(?m)^// Code generated by templ`)

	tests := []struct {
		name     string
		file     string
		minBytes int64
	}{
		{
			name:     "components_templ.go",
			file:     "components_templ.go",
			minBytes: 100_000,
		},
		{
			name:     "layout_templ.go",
			file:     "layout_templ.go",
			minBytes: 1,
		},
		{
			name:     "login_templ.go",
			file:     "login_templ.go",
			minBytes: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fullPath := filepath.Join(pkgDir, tt.file)
			info, err := os.Stat(fullPath)
			if err != nil {
				t.Fatalf(
					"%s not found at %s: %v\nPossible cause: the *.templ sources changed but the generated *_templ.go files were not committed. Run `go tool templ generate -path ./internal/cloud/dashboard` (or `make templ`) and commit the regenerated files.",
					tt.file, fullPath, err,
				)
			}
			if info.Size() < tt.minBytes {
				t.Fatalf(
					"%s size = %d bytes; expected >= %d bytes.\nPossible cause: the *.templ sources changed but the generated *_templ.go files were not committed. Run `go tool templ generate -path ./internal/cloud/dashboard` (or `make templ`) and commit the regenerated files.",
					tt.file, info.Size(), tt.minBytes,
				)
			}
			content, err := os.ReadFile(fullPath)
			if err != nil {
				t.Fatalf("os.ReadFile(%s) failed: %v", fullPath, err)
			}
			preview := content
			if len(preview) > 1024 {
				preview = preview[:1024]
			}
			if strings.Contains(string(content), "FileName: `internal/cloud/dashboard/") {
				t.Fatalf("%s contains stale repository-relative diagnostic paths; run `go tool templ generate -path ./internal/cloud/dashboard`", tt.file)
			}
			if !headerRe.Match(preview) {
				t.Fatalf(
					"%s does not start with '// Code generated by templ' header.\nFirst 1024 bytes: %q\nPossible cause: the file is hand-written, not generated. Run `go tool templ generate -path ./internal/cloud/dashboard` (or `make templ`) and commit.",
					tt.file, string(preview),
				)
			}
		})
	}
}
