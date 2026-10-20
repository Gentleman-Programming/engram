package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
	versioncheck "github.com/Gentleman-Programming/engram/v3/internal/version"
)

func TestSQLiteTempIsolation(t *testing.T) {
	runSQLiteTempIsolation(t, "")
}

func runSQLiteTempIsolation(t *testing.T, failure string) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows in-process startup isolation")
	}
	root := t.TempDir()
	// A synthetic home stands in for the real default: inspect only this fixture.
	t.Setenv("USERPROFILE", root)
	t.Setenv("HOME", root)
	t.Setenv("ENGRAM_DATA_DIR", "")
	beforeTemp, tempPresent := os.LookupEnv("TEMP")
	beforeTMP, tmpPresent := os.LookupEnv("TMP")
	for _, name := range []string{"first main", "next main"} {
		t.Run(name, func(t *testing.T) {
			stubRuntimeHooks(t)
			withArgs(t, "engram", "stats")
			if failure == "preparation" {
				t.Setenv("ENGRAM_DATA_DIR", "relative-data")
			}
			oldMigrate := migrateOrphanedDatabase
			migrateOrphanedDatabase = func(string) {}
			t.Cleanup(func() { migrateOrphanedDatabase = oldMigrate })
			reached := false
			stop := new(int)
			oldExit := exitFunc
			exitFunc = func(code int) { t.Errorf("unexpected exit %d", code); panic(stop) }
			t.Cleanup(func() { exitFunc = oldExit })
			storeNew = func(cfg store.Config) (*store.Store, error) {
				reached = true
				if failure == "assertion" {
					t.Fatal("forced store assertion")
				}
				if cfg.DataDir == filepath.Join(root, ".engram") {
					t.Fatal("main used the home default instead of a synthetic data directory")
				}
				if os.Getenv("TEMP") != filepath.Join(cfg.DataDir, "tmp", "sqlite") {
					t.Fatal("production routing was bypassed")
				}
				panic(stop)
			}
			func() {
				defer func() {
					if v := recover(); v != nil && v != stop {
						panic(v)
					}
				}()
				main()
			}()
			if !reached {
				t.Fatal("main did not open store")
			}
		})
		if got, present := os.LookupEnv("TEMP"); got != beforeTemp || present != tempPresent {
			t.Fatalf("TEMP was not restored: %q, present=%v", got, present)
		}
		if got, present := os.LookupEnv("TMP"); got != beforeTMP || present != tmpPresent {
			t.Fatalf("TMP was not restored: %q, present=%v", got, present)
		}
		t.Run("next temporary root remains usable", func(t *testing.T) { _ = t.TempDir() })
	}
	if _, err := os.Stat(filepath.Join(root, ".engram")); !os.IsNotExist(err) {
		t.Fatalf("home default was touched: %v", err)
	}
}

func TestSQLiteTempStartup(t *testing.T) {
	runSQLiteTempStartup(t, "")
}

func runSQLiteTempStartup(t *testing.T, failure string) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows process bootstrap")
	}
	root := t.TempDir()
	data := filepath.Join(root, "data")
	if failure != "" {
		t.Cleanup(func() {
			for _, name := range []string{"engram.db", "engram.db-wal", "engram.db-shm"} {
				if _, err := os.Stat(filepath.Join(data, name)); !os.IsNotExist(err) {
					t.Errorf("rejected startup created %s: %v", name, err)
				}
			}
		})
	}
	inherited := filepath.Join(root, "inherited")
	if err := os.Mkdir(inherited, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEMP", inherited)
	t.Setenv("TMP", inherited)
	t.Setenv("ENGRAM_DATA_DIR", data)
	if failure == "preparation" {
		t.Setenv("ENGRAM_DATA_DIR", "relative-data")
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	oldArgs, oldConfig, oldNew, oldMigrate := os.Args, storeDefaultConfig, storeNew, migrateOrphanedDatabase
	oldCheck := checkForUpdates
	t.Cleanup(func() {
		os.Args = oldArgs
		storeDefaultConfig = oldConfig
		storeNew = oldNew
		migrateOrphanedDatabase = oldMigrate
		checkForUpdates = oldCheck
	})
	storeDefaultConfig = func() (store.Config, error) {
		return store.FallbackConfig(data), nil
	}
	checkForUpdates = func(string) versioncheck.CheckResult { return versioncheck.CheckResult{} }
	migrateOrphanedDatabase = func(string) {}
	reached := false
	stop := new(int)
	oldExit := exitFunc
	exitFunc = func(code int) { t.Errorf("unexpected exit %d", code); panic(stop) }
	t.Cleanup(func() { exitFunc = oldExit })
	storeNew = func(cfg store.Config) (*store.Store, error) {
		reached = true
		if failure == "assertion" {
			t.Fatal("forced store assertion")
		}
		want := filepath.Join(data, "tmp", "sqlite")
		if os.Getenv("TEMP") != want || os.Getenv("TMP") != want {
			t.Errorf("SQLite opened before temp routing: TEMP=%q TMP=%q", os.Getenv("TEMP"), os.Getenv("TMP"))
		}
		info, err := os.Stat(want)
		if err != nil || !info.IsDir() {
			t.Errorf("stable directory not prepared: %v", err)
		}
		if got, _ := os.Getwd(); got != cwd {
			t.Errorf("cwd changed: %q", got)
		}
		if cfg.DataDir != data {
			t.Errorf("DataDir changed: %q", cfg.DataDir)
		}
		panic(stop)
	}
	os.Args = []string{"engram", "stats"}
	func() {
		defer func() {
			if v := recover(); v != nil && v != stop {
				panic(v)
			}
		}()
		main()
	}()
	if !reached {
		t.Fatal("store not reached")
	}
}
