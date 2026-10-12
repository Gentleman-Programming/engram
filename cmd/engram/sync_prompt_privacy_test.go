package main

import (
	"bytes"
	versioncheck "github.com/Gentleman-Programming/engram/v3/internal/version"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func TestCmdSyncPromptPrivacyMainEntrypoint(t *testing.T) {
	for _, tc := range []struct {
		name  string
		args  []string
		cloud string
	}{
		{"import", []string{"--import"}, "false"},
		{"status", []string{"--status"}, "false"},
		{"cloud", []string{"--cloud"}, "false"},
		{"environment", nil, "true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			withCwd(t, dir)
			t.Setenv("ENGRAM_DATA_DIR", dir)
			t.Setenv("ENGRAM_CLOUD_SYNC", tc.cloud)
			files := []string{"engram.db", "engram.db-wal", "engram.db-shm", "manifest.json"}
			for _, name := range files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte("unchanged "+name), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var calls []string
			oldUpdate, oldConfig, oldMigration, oldStore := checkForUpdates, storeDefaultConfig, migrateOrphanedDatabase, storeNew
			checkForUpdates = func(string) versioncheck.CheckResult {
				calls = append(calls, "update")
				return versioncheck.CheckResult{}
			}
			storeDefaultConfig = func() (store.Config, error) { calls = append(calls, "config"); return store.Config{DataDir: dir}, nil }
			migrateOrphanedDatabase = func(string) { calls = append(calls, "migration") }
			storeNew = func(store.Config) (*store.Store, error) { calls = append(calls, "store"); panic("prohibited store") }
			t.Cleanup(func() {
				checkForUpdates, storeDefaultConfig, migrateOrphanedDatabase, storeNew = oldUpdate, oldConfig, oldMigration, oldStore
			})
			stubExitWithPanic(t)
			withArgs(t, append([]string{"engram", "sync", "--include-prompts"}, tc.args...)...)
			stdout, stderr, recovered := captureOutputAndRecover(t, main)
			if recovered != exitCode(1) || stdout != "" || stderr != promptPrivacyError || len(calls) != 0 {
				t.Errorf("exit=%v stdout=%q stderr=%q prohibited hooks=%v", recovered, stdout, stderr, calls)
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != len(files) {
				t.Errorf("artifacts changed: %v", entries)
			}
			for _, name := range files {
				got, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil || !bytes.Equal(got, []byte("unchanged "+name)) {
					t.Errorf("%s changed: %q err=%v", name, got, err)
				}
			}
		})
	}
}

func TestCmdSyncPromptPrivacyMainValidPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		help bool
	}{
		{"default", nil, false},
		{"opt-in", []string{"--include-prompts", "--project", "privacy"}, false},
		{"project named help", []string{"--include-prompts", "--project", "help"}, false},
		{"project equals mode", []string{"--include-prompts", "--project=--status"}, false},
		{"mode is invalid project value", []string{"--include-prompts", "--project", "--status"}, false},
		{"help wins", []string{"--include-prompts", "--status", "--help"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			withCwd(t, dir)
			t.Setenv("ENGRAM_DATA_DIR", dir)
			t.Setenv("ENGRAM_CLOUD_SYNC", "false")
			oldConfig, oldUpdate := storeDefaultConfig, checkForUpdates
			checkForUpdates = func(string) versioncheck.CheckResult { return versioncheck.CheckResult{} }
			storeDefaultConfig = func() (store.Config, error) { panic("configuration reached") }
			t.Cleanup(func() { storeDefaultConfig, checkForUpdates = oldConfig, oldUpdate })
			withArgs(t, append([]string{"engram", "sync"}, tc.args...)...)
			stdout, stderr, recovered := captureOutputAndRecover(t, main)
			if stderr != "" {
				t.Fatalf("unexpected rejection: %q", stderr)
			}
			// Existing main startup resolves config even for sync help. Preflight
			// must preserve that route rather than reject the conflicting flags.
			if recovered != "configuration reached" || stdout != "" {
				t.Fatalf("valid startup stdout=%q panic=%v", stdout, recovered)
			}
			if tc.help {
				stdout, stderr, recovered = captureOutputAndRecover(t, func() { cmdSync(store.Config{DataDir: dir}) })
				if recovered != nil || stderr != "" || !strings.Contains(stdout, "usage: engram sync") {
					t.Fatalf("help stdout=%q stderr=%q panic=%v", stdout, stderr, recovered)
				}
			}
		})
	}
}

const promptPrivacyError = "engram: --include-prompts is only supported for local Git sync export; cannot combine with --import, --status, or cloud sync\n"

func TestCmdSyncPromptPrivacyDefaultNotice(t *testing.T) {
	for _, args := range [][]string{{"engram", "sync"}, {"engram", "sync", "--all"}, {"engram", "sync", "--project", "privacy"}} {
		t.Run(strings.Join(args[2:], " "), func(t *testing.T) {
			withCwd(t, t.TempDir())
			t.Setenv("ENGRAM_CLOUD_SYNC", "false")
			t.Setenv("ENGRAM_PROJECT", "privacy")
			withArgs(t, args...)
			stdout, stderr := captureOutput(t, func() { cmdSync(testConfig(t)) })
			if stderr != "" || !strings.Contains(stdout, "Prompts excluded") || !strings.Contains(stdout, "Nothing new to sync") {
				t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
			}
		})
	}
}

func TestCmdSyncPromptPrivacyRejectBeforeStore(t *testing.T) {
	for _, tc := range []struct {
		name  string
		args  []string
		cloud string
	}{
		{"import", []string{"--import"}, "false"},
		{"status", []string{"--status"}, "false"},
		{"cloud", []string{"--cloud", "--project", "privacy"}, "false"},
		{"environment cloud", []string{"--project", "privacy"}, "true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			withCwd(t, dir)
			cfg := testConfig(t)
			t.Setenv("ENGRAM_CLOUD_SYNC", tc.cloud)
			withArgs(t, append([]string{"engram", "sync", "--include-prompts"}, tc.args...)...)
			old := storeNew
			storeNew = func(store.Config) (*store.Store, error) { t.Fatal("rejection opened store"); return nil, nil }
			t.Cleanup(func() { storeNew = old })
			stubExitWithPanic(t)
			stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdSync(cfg) })
			if recovered != exitCode(1) || stdout != "" || stderr != promptPrivacyError {
				t.Fatalf("exit=%v stdout=%q stderr=%q", recovered, stdout, stderr)
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("rejection created artifacts: %v", entries)
			}
		})
	}
}

func TestCmdSyncPromptPrivacyHelp(t *testing.T) {
	withArgs(t, "engram", "sync", "--help")
	stdout, stderr := captureOutput(t, printSyncUsage)
	for _, want := range []string{"--include-prompts", "excluded", "cloud"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("help missing %q: %s", want, stdout)
		}
	}
	if stderr != "" {
		t.Fatal(stderr)
	}
}

func TestCmdSyncPromptPrivacyOptInAndWarning(t *testing.T) {
	withCwd(t, t.TempDir())
	t.Setenv("ENGRAM_CLOUD_SYNC", "false")
	cfg := testConfig(t)
	mustSeedObservation(t, cfg, "privacy-session", "privacy", "note", "sensitive title", "sensitive observation", "project")
	mustSeedPrompt(t, cfg, "privacy-session", "privacy")
	withArgs(t, "engram", "sync", "--project", "privacy")
	stdout, stderr := captureOutput(t, func() { cmdSync(cfg) })
	if stderr != "" || !strings.Contains(stdout, "  Prompts:      0") {
		t.Fatalf("default stdout=%q stderr=%q", stdout, stderr)
	}
	warning, instruction := strings.Index(stdout, "Warning:"), strings.Index(stdout, "Add to git:")
	if warning < 0 || instruction < warning || !strings.Contains(stdout, "permanently in Git history") {
		t.Fatalf("missing warning before instruction: %q", stdout)
	}
	withArgs(t, "engram", "sync", "--project", "privacy", "--include-prompts")
	stdout, stderr = captureOutput(t, func() { cmdSync(cfg) })
	if stderr != "" || strings.Contains(stdout, "Prompts excluded") || !strings.Contains(stdout, "  Prompts:      1") {
		t.Fatalf("opt-in stdout=%q stderr=%q", stdout, stderr)
	}
	withArgs(t, "engram", "sync", "--project", "privacy", "--include-prompts")
	stdout, stderr = captureOutput(t, func() { cmdSync(cfg) })
	if stderr != "" || !strings.Contains(stdout, "Nothing new to sync") {
		t.Fatalf("repeated opt-in stdout=%q stderr=%q", stdout, stderr)
	}
}
