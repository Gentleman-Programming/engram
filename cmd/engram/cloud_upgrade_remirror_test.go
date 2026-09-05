package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v2/internal/cloudconfig"
	"github.com/Gentleman-Programming/engram/v2/internal/store"
	engramsync "github.com/Gentleman-Programming/engram/v2/internal/sync"
)

func TestCmdCloudUpgradeRemirrorSuccessPrintsExportCounts(t *testing.T) {
	cfg := testConfig(t)
	if err := saveCloudConfig(cfg, &cloudConfig{ServerURL: "https://cloud.example.test"}); err != nil {
		t.Fatalf("save cloud config: %v", err)
	}

	oldRun := runUpgradeRemirror
	runUpgradeRemirror = func(_ *store.Store, project string, cc *cloudconfig.Config) (*engramsync.SyncResult, error) {
		if project != "project-a" || cc.ServerURL != "https://cloud.example.test" {
			t.Fatalf("runUpgradeRemirror inputs = project %q, config %+v", project, cc)
		}
		return &engramsync.SyncResult{ChunksExported: 2, MutationsExported: 3}, nil
	}
	t.Cleanup(func() { runUpgradeRemirror = oldRun })

	withArgs(t, "engram", "cloud", "upgrade", "remirror", "--project", "project-a")
	stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdCloudUpgradeRemirror(cfg) })
	if recovered != nil {
		t.Fatalf("cmdCloudUpgradeRemirror unexpectedly exited: %v; stderr=%q", recovered, stderr)
	}
	for _, want := range []string{"project: project-a", "chunks_exported: 2", "mutations_exported: 3"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want %q", stdout, want)
		}
	}
}

func TestCmdCloudUpgradeRemirrorFailures(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		configure func(t *testing.T, cfg store.Config)
		stub      func(t *testing.T)
		want      string
	}{
		{
			name: "requires project",
			args: []string{"engram", "cloud", "upgrade", "remirror"},
			want: "--project is required",
		},
		{
			name: "store open failure",
			args: []string{"engram", "cloud", "upgrade", "remirror", "--project", "project-a"},
			stub: func(t *testing.T) {
				old := storeNew
				storeNew = func(store.Config) (*store.Store, error) { return nil, errors.New("open failed") }
				t.Cleanup(func() { storeNew = old })
			},
			want: "open failed",
		},
		{
			name: "requires configured cloud server",
			args: []string{"engram", "cloud", "upgrade", "remirror", "--project", "project-a"},
			configure: func(t *testing.T, cfg store.Config) {
				t.Helper()
				t.Setenv("ENGRAM_CLOUD_SERVER", "")
				if err := saveCloudConfig(cfg, &cloudConfig{ServerURL: " \t "}); err != nil {
					t.Fatalf("save cloud config: %v", err)
				}
			},
			want: "cloud upgrade remirror requires configured cloud server",
		},
		{
			name: "configuration failure",
			args: []string{"engram", "cloud", "upgrade", "remirror", "--project", "project-a"},
			configure: func(t *testing.T, cfg store.Config) {
				t.Helper()
				if err := saveCloudConfig(cfg, &cloudConfig{ServerURL: "ftp://cloud.example.test"}); err != nil {
					t.Fatalf("save cloud config: %v", err)
				}
			},
			want: "invalid cloud runtime server URL",
		},
		{
			name:      "remirror failure",
			args:      []string{"engram", "cloud", "upgrade", "remirror", "--project", "project-a"},
			configure: configureRemirrorTestCloud,
			stub: func(t *testing.T) {
				old := runUpgradeRemirror
				runUpgradeRemirror = func(*store.Store, string, *cloudconfig.Config) (*engramsync.SyncResult, error) {
					return nil, errors.New("remirror failed")
				}
				t.Cleanup(func() { runUpgradeRemirror = old })
			},
			want: "cloud remirror: remirror failed",
		},
		{
			name:      "export failure",
			args:      []string{"engram", "cloud", "upgrade", "remirror", "--project", "project-a"},
			configure: configureRemirrorTestCloud,
			stub: func(t *testing.T) {
				old := runUpgradeRemirror
				runUpgradeRemirror = func(*store.Store, string, *cloudconfig.Config) (*engramsync.SyncResult, error) {
					return nil, errors.New("export failed")
				}
				t.Cleanup(func() { runUpgradeRemirror = old })
			},
			want: "cloud remirror: export failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stubExitWithPanic(t)
			cfg := testConfig(t)
			if tt.configure != nil {
				tt.configure(t, cfg)
			}
			if tt.stub != nil {
				tt.stub(t)
			}
			withArgs(t, tt.args...)
			_, stderr, recovered := captureOutputAndRecover(t, func() { cmdCloudUpgradeRemirror(cfg) })
			code, ok := recovered.(exitCode)
			if !ok || code != 1 {
				t.Fatalf("exit = %v, want 1; stderr=%q", recovered, stderr)
			}
			if !strings.Contains(stderr, tt.want) {
				t.Fatalf("stderr = %q, want %q", stderr, tt.want)
			}
		})
	}
}

func TestRunUpgradeRemirror(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(t *testing.T, s *store.Store)
		status    int
		wantError string
		wantCount bool
	}{
		{
			name:      "requires enrolled project",
			wantError: "requires enrolled project",
		},
		{
			name: "export failure",
			setup: func(t *testing.T, s *store.Store) {
				t.Helper()
				seedRemirrorProject(t, s)
			},
			status:    http.StatusInternalServerError,
			wantError: "push chunk",
		},
		{
			name: "success returns export counts",
			setup: func(t *testing.T, s *store.Store) {
				t.Helper()
				seedRemirrorProject(t, s)
			},
			status:    http.StatusOK,
			wantCount: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig(t)
			s, err := store.New(cfg)
			if err != nil {
				t.Fatalf("open store: %v", err)
			}
			defer s.Close() //nolint:errcheck
			if tt.setup != nil {
				tt.setup(t, s)
			}

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/sync/pull":
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"version":1,"chunks":[]}`))
				case r.Method == http.MethodPost && r.URL.Path == "/sync/push":
					w.WriteHeader(tt.status)
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()

			result, err := runUpgradeRemirror(s, "project-a", &cloudconfig.Config{ServerURL: srv.URL})
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("runUpgradeRemirror error = %v, want %q", err, tt.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("runUpgradeRemirror: %v", err)
			}
			if !tt.wantCount || result.ChunksExported < 1 || result.MutationsExported < 1 {
				t.Fatalf("runUpgradeRemirror result = %+v, want exported chunk and mutation counts", result)
			}
		})
	}
}

func TestRunUpgradeRemirrorRejectsAuthenticatedHTTPBeforeLocalReplay(t *testing.T) {
	cfg := testConfig(t)
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close() //nolint:errcheck
	seedRemirrorProject(t, s)

	beforeMutations, err := s.ListPendingSyncMutations(store.DefaultSyncTargetKey, 100)
	if err != nil {
		t.Fatalf("list pending mutations before remirror: %v", err)
	}
	beforeState, err := s.GetSyncState(store.DefaultSyncTargetKey)
	if err != nil {
		t.Fatalf("get sync state before remirror: %v", err)
	}

	_, err = runUpgradeRemirror(s, "project-a", &cloudconfig.Config{
		ServerURL: "http://cloud.example.test",
		Token:     "bearer-token",
	})
	if err == nil || !strings.Contains(err.Error(), "bearer token requires an HTTPS remote URL") {
		t.Fatalf("runUpgradeRemirror error = %v, want bearer HTTPS validation error", err)
	}

	afterMutations, err := s.ListPendingSyncMutations(store.DefaultSyncTargetKey, 100)
	if err != nil {
		t.Fatalf("list pending mutations after remirror: %v", err)
	}
	afterState, err := s.GetSyncState(store.DefaultSyncTargetKey)
	if err != nil {
		t.Fatalf("get sync state after remirror: %v", err)
	}
	if !reflect.DeepEqual(afterMutations, beforeMutations) {
		t.Fatalf("invalid transport queued remirror mutations: before=%+v after=%+v", beforeMutations, afterMutations)
	}
	if !reflect.DeepEqual(afterState, beforeState) {
		t.Fatalf("invalid transport changed sync state: before=%+v after=%+v", beforeState, afterState)
	}
}

func configureRemirrorTestCloud(t *testing.T, cfg store.Config) {
	t.Helper()
	if err := saveCloudConfig(cfg, &cloudConfig{ServerURL: "https://cloud.example.test"}); err != nil {
		t.Fatalf("save cloud config: %v", err)
	}
}

func seedRemirrorProject(t *testing.T, s *store.Store) {
	t.Helper()
	if err := s.CreateSession("session-a", "project-a", "/tmp/project-a"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := s.EnrollProject("project-a"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}
}
