package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/diagnostic"
	"github.com/Gentleman-Programming/engram/v3/internal/hostbind"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func TestDoctorHostSessionLifecycle(t *testing.T) {
	cfg := testConfig(t)
	s, err := store.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})

	const root = "doctor-fixture-root"
	const project = "doctor-fixture-project"
	dir := t.TempDir()
	if err := s.CreateSession(root, project, dir); err != nil {
		t.Fatal(err)
	}
	pid := os.Getpid()
	if err := hostbind.Publish(s.DataDir(), pid, root); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENGRAM_DATA_DIR", cfg.DataDir)
	t.Setenv("ENGRAM_NO_UPDATE_CHECK", "1")
	withArgs(t, "engram", "doctor", "host-session", "--pid", strconv.Itoa(pid), "--json")
	previousExit := exitFunc
	t.Cleanup(func() { exitFunc = previousExit })
	var exitCode int
	exitFunc = func(code int) { exitCode = code }
	check := func(status, reason string) {
		t.Helper()
		raw, stderr := captureOutput(t, main)
		if exitCode != 0 || stderr != "" || strings.Contains(raw, root) || strings.Contains(raw, project) {
			t.Fatalf("unexpected output %q stderr=%q", raw, stderr)
		}
		var report diagnostic.Report
		if err := json.Unmarshal([]byte(raw), &report); err != nil {
			t.Fatal(err)
		}
		if report.Status != status || len(report.Checks) != 1 || report.Checks[0].ReasonCode != reason {
			t.Fatalf("unexpected report: %s", raw)
		}
	}

	check("ok", "live_host_session_active")
	if err := s.EndSession(root, ""); err != nil {
		t.Fatal(err)
	}
	check("warning", "live_host_session_ended")
	effective, err := s.ResumeSessionWithOwnershipMode(root, project, dir, store.SessionOwnershipShared)
	if err != nil {
		t.Fatal(err)
	}
	check("ok", "live_host_session_continuation_active")
	if err := s.EndSession(effective, ""); err != nil {
		t.Fatal(err)
	}
	check("warning", "live_host_session_ended")

	bindingPath := filepath.Join(s.DataDir(), "host-sessions", strconv.Itoa(pid)+".json")
	stale := hostbind.Binding{PID: pid, Start: "obsolete", SessionID: root}
	content, err := json.Marshal(stale)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bindingPath, content, 0600); err != nil {
		t.Fatal(err)
	}
	check("warning", "live_host_session_binding_unverified")
	post, err := os.ReadFile(bindingPath)
	if err != nil || string(post) != string(content) {
		t.Fatalf("diagnostic modified host binding: %v", err)
	}
	session, err := s.GetSession(effective)
	if err != nil || session.EndedAt == nil {
		t.Fatalf("diagnostic changed the continuation: %v", err)
	}
}

func TestDoctorHostSessionLeavesUninitializedDirectoryUntouched(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "uninitialized")
	t.Setenv("ENGRAM_DATA_DIR", dataDir)
	t.Setenv("ENGRAM_NO_UPDATE_CHECK", "1")
	pid := os.Getpid()
	withArgs(t, "engram", "doctor", "host-session", "--pid", strconv.Itoa(pid), "--json")

	check := func(reason string) {
		t.Helper()
		raw, stderr := captureOutput(t, main)
		if stderr != "" {
			t.Fatal(stderr)
		}
		var report diagnostic.Report
		if err := json.Unmarshal([]byte(raw), &report); err != nil {
			t.Fatal(err)
		}
		if len(report.Checks) != 1 || report.Checks[0].ReasonCode != reason {
			t.Fatalf("unexpected report: %s", raw)
		}
	}

	check("live_host_session_binding_unverified")
	if _, err := os.Stat(dataDir); !os.IsNotExist(err) {
		t.Fatalf("diagnostic initialized an untouched directory: %v", err)
	}
	if err := hostbind.Publish(dataDir, pid, "doctor-fixture-uninitialized"); err != nil {
		t.Fatal(err)
	}
	check("live_host_session_state_unavailable")
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "host-sessions" {
		t.Fatalf("diagnostic initialized store files: %v", entries)
	}
}
