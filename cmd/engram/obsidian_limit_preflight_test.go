package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
	versioncheck "github.com/Gentleman-Programming/engram/v3/internal/version"
)

func TestObsidianLimitPreflightBeforeStartup(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags []string
	}{
		{"standalone", []string{"--limit", "2"}},
		{"equals", []string{"--limit=2"}},
		{"missing", []string{"--limit"}},
		{"watch", []string{"--watch", "--interval", "30s", "--limit", "2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			orphan, data := filepath.Join(root, "orphan"), filepath.Join(root, "data")
			if err := os.Mkdir(orphan, 0700); err != nil {
				t.Fatal(err)
			}
			for _, suffix := range []string{"", "-wal", "-shm"} {
				if err := os.WriteFile(filepath.Join(orphan, "engram.db"+suffix), []byte("synthetic"+suffix), 0600); err != nil {
					t.Fatal(err)
				}
			}
			before := compatibilityTreeHash(t, root)
			t.Setenv("ENGRAM_DATA_DIR", data)
			var calls []string
			oldUpdate, oldConfig, oldMigration, oldStore, oldExit := checkForUpdates, storeDefaultConfig, migrateOrphanedDatabase, storeNew, exitFunc
			checkForUpdates = func(string) versioncheck.CheckResult {
				calls = append(calls, "update")
				return versioncheck.CheckResult{}
			}
			storeDefaultConfig = func() (store.Config, error) { calls = append(calls, "config"); return store.Config{DataDir: data}, nil }
			// Never call the real fixed-root migrator, even in the RED run.
			migrateOrphanedDatabase = func(string) {
				calls = append(calls, "migration")
				if err := os.MkdirAll(data, 0700); err != nil {
					t.Fatal(err)
				}
				for _, suffix := range []string{"", "-wal", "-shm"} {
					if err := os.Rename(filepath.Join(orphan, "engram.db"+suffix), filepath.Join(data, "engram.db"+suffix)); err != nil {
						t.Fatal(err)
					}
				}
			}
			storeNew = func(store.Config) (*store.Store, error) {
				calls = append(calls, "store")
				return nil, errors.New("unexpected store")
			}
			exitFunc = func(code int) { calls = append(calls, fmt.Sprintf("exit:%d", code)) }
			t.Cleanup(func() {
				checkForUpdates, storeDefaultConfig, migrateOrphanedDatabase, storeNew, exitFunc = oldUpdate, oldConfig, oldMigration, oldStore, oldExit
			})
			withArgs(t, append([]string{"engram", "obsidian-export", "--vault", filepath.Join(root, "vault")}, tc.flags...)...)
			stdout, stderr := captureOutput(t, main)
			if stdout != "" || stderr != "error: --limit is not supported by obsidian-export; omit it to export all matching observations\n" {
				t.Errorf("stdout=%q stderr=%q", stdout, stderr)
			}
			if fmt.Sprint(calls) != "[exit:1]" {
				t.Errorf("startup calls = %v, want [exit:1]", calls)
			}
			if compatibilityTreeHash(t, root) != before {
				t.Error("rejection changed synthetic orphan/data/temp/vault tree")
			}
		})
	}
}

func TestObsidianLimitPreflightArgumentConsumption(t *testing.T) {
	for _, flag := range []string{"--vault", "--project", "--since", "--graph-config", "--interval"} {
		for _, value := range []string{"--limit", "--limit=2"} {
			t.Run(flag+"/"+value, func(t *testing.T) {
				if err := validateObsidianExportLimit([]string{flag, value}); err != nil {
					t.Fatalf("consumed value rejected: %v", err)
				}
				if err := validateObsidianExportLimit([]string{flag, value, "--limit"}); err == nil {
					t.Fatal("independent limit not rejected")
				}
			})
		}
	}
	for _, args := range [][]string{nil, {"--vault", "notes", "--all"}, {"--unknown", "--limit"}, {"positional", "--limit=2"}} {
		if err := validateObsidianExportLimit(args); err != nil {
			t.Errorf("%v: preflight reordered parser error: %v", args, err)
		}
	}
}
