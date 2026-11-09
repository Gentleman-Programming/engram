package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func TestSQLiteTempCommandScope(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows process routing")
	}
	type commandCase struct {
		name  string
		args  []string
		local bool
	}
	cases := []commandCase{
		{"protocol mode", []string{"protocol-mode"}, false},
		{"setup", []string{"setup"}, false},
		{"cloud serve", []string{"cloud", "serve"}, false},
		{"cloud config", []string{"cloud", "config"}, false},
		{"cloud repair", []string{"cloud", "repair"}, false},
		{"cloud bootstrap", []string{"cloud", "bootstrap"}, false},
		{"cloud unknown", []string{"cloud", "unknown"}, false},
		{"cloud missing", []string{"cloud"}, false},
		{"unknown", []string{"unknown"}, false},
		{"case sensitive dispatch", []string{"STATS"}, false},
		{"doctor", []string{"doctor"}, true},
		{"serve", []string{"serve"}, true},
		{"mcp", []string{"mcp"}, true},
		{"cloud status", []string{"cloud", "status"}, true},
		{"cloud enroll", []string{"cloud", "enroll"}, true},
		{"cloud unenroll", []string{"cloud", "unenroll"}, true},
		{"cloud attest", []string{"cloud", "attest-prompt-source"}, true},
	}
	for _, command := range []string{"tui", "search", "save", "delete", "timeline", "conflicts", "context", "stats", "export", "import", "sync", "obsidian-export", "projects"} {
		cases = append(cases, commandCase{command, []string{command}, true})
	}
	for _, args := range [][]string{nil, {}, {""}, {"cloud", "UPGRADE", "doctor"}, {"cloud", "upgrade"}} {
		if usesLocalSQLite(args) {
			t.Errorf("non-dispatchable args classified as local SQLite: %q", args)
		}
	}
	for _, sub := range []string{"doctor", "repair", "bootstrap", "remirror", "status", "rollback", " DOCTOR "} {
		cases = append(cases, commandCase{"cloud upgrade " + sub, []string{"cloud", "upgrade", sub}, true})
	}
	for _, sub := range []string{"help", "unknown", ""} {
		cases = append(cases, commandCase{"cloud upgrade excluded " + sub, []string{"cloud", "upgrade", sub}, false})
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			inherited := filepath.Join(root, "inherited")
			if err := os.Mkdir(inherited, 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("TEMP", inherited)
			t.Setenv("TMP", inherited)
			data := filepath.Join(root, "data")
			t.Setenv("ENGRAM_DATA_DIR", data)
			stubRuntimeHooks(t)
			withArgs(t, append([]string{"engram"}, tt.args...)...)
			oldConfig, oldMigrate, oldExit := storeDefaultConfig, migrateOrphanedDatabase, exitFunc
			t.Cleanup(func() { storeDefaultConfig, migrateOrphanedDatabase, exitFunc = oldConfig, oldMigrate, oldExit })
			storeDefaultConfig = func() (store.Config, error) { return store.FallbackConfig(data), nil }
			stop := new(int)
			exitFunc = func(code int) { t.Errorf("unexpected exit %d", code); panic(stop) }
			reached := false
			// Isolate dispatch and old migration effects: this checks only the new
			// bootstrap at the unchanged migration boundary, not command execution.
			migrateOrphanedDatabase = func(string) { reached = true; panic(stop) }
			func() {
				defer func() {
					if v := recover(); v != nil && v != stop {
						panic(v)
					}
				}()
				main()
			}()
			if !reached {
				t.Fatal("existing migration boundary not reached")
			}
			want := inherited
			if tt.local {
				want = filepath.Join(data, "tmp", "sqlite")
			}
			if os.Getenv("TEMP") != want || os.Getenv("TMP") != want {
				t.Errorf("routing: TEMP=%q TMP=%q want %q", os.Getenv("TEMP"), os.Getenv("TMP"), want)
			}
			_, err := os.Stat(filepath.Join(data, "tmp", "sqlite"))
			if tt.local && err != nil {
				t.Errorf("local SQLite directory: %v", err)
			}
			if !tt.local && !os.IsNotExist(err) {
				t.Errorf("non-SQLite command acquired directory: %v", err)
			}
		})
	}
}
