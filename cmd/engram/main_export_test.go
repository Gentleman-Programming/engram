package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func TestCmdExportReturnsOutputPath(t *testing.T) {
	for _, name := range []string{"default output", "explicit output"} {
		t.Run(name, func(t *testing.T) {
			stubRuntimeHooks(t)
			stubExitWithPanic(t)
			withCwd(t, t.TempDir())
			cfg := testConfig(t)
			mustSeedObservation(t, cfg, "export-session", "export-project", "note", "Export title", "Export content", "project")

			wantPath := "engram-export.json"
			args := []string{"engram", "export", "--all"}
			if name == "explicit output" {
				wantPath = filepath.Join(t.TempDir(), "custom.json")
				args = append(args, wantPath)
			}
			withArgs(t, args...)
			var gotPath string
			var gotErr error
			stdout, stderr, recovered := captureOutputAndRecover(t, func() {
				gotPath, gotErr = cmdExport(cfg)
			})
			if recovered != nil || gotErr != nil || stderr != "" {
				t.Fatalf("export: panic=%v err=%v stderr=%q", recovered, gotErr, stderr)
			}
			if gotPath != wantPath {
				t.Fatalf("path=%q, want %q", gotPath, wantPath)
			}
			if !strings.Contains(stdout, "Exported to "+wantPath) {
				t.Fatalf("missing success output: %q", stdout)
			}
			out, err := os.ReadFile(gotPath)
			if err != nil {
				t.Fatalf("read returned path: %v", err)
			}
			var data store.ExportData
			if err := json.Unmarshal(out, &data); err != nil {
				t.Fatalf("decode export: %v", err)
			}
			if len(data.Sessions) != 1 || data.Sessions[0].ID != "export-session" || len(data.Observations) != 1 {
				t.Fatalf("unexpected exported data: %+v", data)
			}
			obs := data.Observations[0]
			if obs.Title != "Export title" || obs.Content != "Export content" {
				t.Fatalf("unexpected exported observation: %+v", obs)
			}
		})
	}
}

func TestCmdExportReturnsErrors(t *testing.T) {
	sentinel := errors.New("export dependency failed")
	for _, tc := range []struct {
		name    string
		args    []string
		setup   func()
		want    error
		context string
		write   bool
	}{
		{name: "missing project value", args: []string{"--project"}, context: "--project requires a non-empty value"},
		{name: "blank project value", args: []string{"--project", " "}, context: "--project requires a non-empty value"},
		{name: "flag as project value", args: []string{"--project", "--all"}, context: "--project requires a non-empty value"},
		{name: "store initialization", setup: func() {
			storeNew = func(store.Config) (*store.Store, error) { return nil, sentinel }
		}, want: sentinel},
		{name: "project scope conflict", args: []string{"--all", "--project", "project"}, context: "--all and --project cannot be used together"},
		{name: "export", setup: func() {
			storeExport = func(*store.Store) (*store.ExportData, error) { return nil, sentinel }
		}, want: sentinel},
		{name: "marshal", setup: func() {
			jsonMarshalIndent = func(any, string, string) ([]byte, error) { return nil, sentinel }
		}, want: sentinel},
		{name: "write", write: true, want: os.ErrNotExist},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubRuntimeHooks(t)
			stubExitWithPanic(t)
			withCwd(t, t.TempDir())
			cfg := testConfig(t)
			args := []string{"engram", "export"}
			if tc.args == nil {
				args = append(args, "--all")
			} else {
				args = append(args, tc.args...)
			}
			var writePath string
			if tc.write {
				writePath = filepath.Join(t.TempDir(), "missing", "export.json")
				args = append(args, writePath)
			}
			withArgs(t, args...)
			if tc.setup != nil {
				tc.setup()
			}
			var gotPath string
			var gotErr error
			stdout, stderr, recovered := captureOutputAndRecover(t, func() {
				gotPath, gotErr = cmdExport(cfg)
			})
			if recovered != nil {
				t.Fatalf("direct export must not terminate or panic: %v", recovered)
			}
			if gotPath != "" || gotErr == nil {
				t.Fatalf("path=%q err=%v, want empty path and error", gotPath, gotErr)
			}
			if stdout != "" || stderr != "" {
				t.Fatalf("error must be returned without output: stdout=%q stderr=%q", stdout, stderr)
			}
			if tc.want != nil && !errors.Is(gotErr, tc.want) {
				t.Fatalf("error=%v, want errors.Is(%v)", gotErr, tc.want)
			}
			if tc.context != "" && !strings.Contains(gotErr.Error(), tc.context) {
				t.Fatalf("error=%v, want context %q", gotErr, tc.context)
			}
			if tc.write {
				var pathErr *os.PathError
				if !errors.As(gotErr, &pathErr) || pathErr.Path != writePath {
					t.Fatalf("error=%v, want PathError for %q", gotErr, writePath)
				}
				if !strings.HasPrefix(gotErr.Error(), "write "+writePath+": ") {
					t.Fatalf("missing write context: %v", gotErr)
				}
			}
		})
	}
}
