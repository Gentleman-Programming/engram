package project

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigReadFailureDiagnostic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".engram", "config.json")
	cause := &os.PathError{Op: "open", Path: path, Err: os.ErrPermission}
	res, found := readConfigAtWithReader(dir, func(got string) ([]byte, error) {
		if got != path {
			t.Fatalf("read path = %q, want %q", got, path)
		}
		return nil, cause
	})
	if !found || res.Project != "" || res.Source != SourceConfig || res.Path != dir || res.Error == nil {
		t.Fatalf("read failure must remain authoritative and fail closed: found=%v result=%+v", found, res)
	}
	if !strings.HasPrefix(res.Error.Error(), "cannot read .engram/config.json: ") || !strings.Contains(res.Error.Error(), cause.Error()) {
		t.Errorf("read-specific diagnostic must retain cause: %v", res.Error)
	}
	if !errors.Is(res.Error, ErrInvalidConfig) || !errors.Is(res.Error, os.ErrPermission) {
		t.Errorf("error must retain config identity and permission cause: %v", res.Error)
	}
	var pathErr *os.PathError
	if !errors.As(res.Error, &pathErr) || pathErr != cause {
		t.Errorf("original PathError not preserved: %v", res.Error)
	}
}

func TestConfigReadDiagnosticControls(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		missing bool
		invalid bool
	}{
		{name: "missing", missing: true},
		{name: "malformed", content: "{", invalid: true},
		{name: "invalid name", content: `{"project_name":"bad/name"}`, invalid: true},
		{name: "valid", content: `{"project_name":"synthetic-project"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if !tc.missing {
				configDir := filepath.Join(dir, ".engram")
				if err := os.Mkdir(configDir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(tc.content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			res, found := readConfigAt(dir)
			switch {
			case tc.missing:
				if found || res.Error != nil || res.Project != "" {
					t.Fatalf("missing config must permit existing fallback: found=%v result=%+v", found, res)
				}
			case tc.invalid:
				if !found || res.Project != "" || !errors.Is(res.Error, ErrInvalidConfig) || !strings.HasPrefix(res.Error.Error(), "invalid .engram/config.json") {
					t.Fatalf("invalid content classification changed: found=%v result=%+v", found, res)
				}
			default:
				if !found || res.Error != nil || res.Project != "synthetic-project" || res.Source != SourceConfig {
					t.Fatalf("valid config changed: found=%v result=%+v", found, res)
				}
			}
		})
	}
}
