package main

import (
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func TestContextHistoryCLIRealRendering(t *testing.T) {
	cfg := testConfig(t)
	s, err := store.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession("real-history", "alpha", ""); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 25; i++ {
		project := "alpha"
		if i%2 == 0 {
			project = "beta"
		}
		if _, err := s.DB().Exec(`INSERT INTO observations(session_id,type,title,content,project,scope,created_at,sync_id) VALUES('real-history','decision','past','body',?,'personal','2022-01-01',?)`, project, fmt.Sprintf("history-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	before, err := s.Export()
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := s.FormatContext("", "personal")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	stubExitWithPanic(t)
	for _, enabled := range []bool{false, true} {
		args := []string{"engram", "context", "--all", "--scope", "personal"}
		if enabled {
			args = append(args, "--older-history")
		}
		withArgs(t, args...)
		stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdContext(cfg) })
		if recovered != nil || stderr != "" {
			t.Fatalf("exit=%v stderr=%q", recovered, stderr)
		}
		if enabled {
			if !strings.Contains(stdout, "2022: 5 observations") {
				t.Fatal(stdout)
			}
		} else if stdout != legacy {
			t.Fatalf("legacy stdout changed: %q", stdout)
		}
	}
	s, err = store.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	after, err := s.Export()
	if err != nil {
		t.Fatal(err)
	}
	before.ExportedAt = ""
	after.ExportedAt = ""
	if !reflect.DeepEqual(before, after) {
		t.Fatal("context mutated store")
	}
}

func TestContextHistoryCLI(t *testing.T) {
	cfg := testConfig(t)
	mustSeedObservation(t, cfg, "history", "engram", "note", "fixture", "content", "project")
	stubExitWithPanic(t)
	oldLegacy, oldOptions := storeFormatContext, storeFormatContextWithOptions
	t.Cleanup(func() {
		storeFormatContext = oldLegacy
		storeFormatContextWithOptions = oldOptions
	})
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "default", true: "enabled"}[enabled], func(t *testing.T) {
			legacyCalled, optionsCalled := false, false
			storeFormatContext = func(_ *store.Store, project, scope string) (string, error) {
				legacyCalled = true
				return "legacy", nil
			}
			storeFormatContextWithOptions = func(_ *store.Store, project, scope string, opts store.ContextOptions) (string, error) {
				optionsCalled = true
				if project != "engram" || scope != "personal" || !opts.OlderHistory || opts.MaxBytes != 0 {
					t.Errorf("forwarded %q %q %+v", project, scope, opts)
				}
				return "history", nil
			}
			args := []string{"engram", "context", "--project", "engram", "--scope", "personal"}
			if enabled {
				args = append(args, "--older-history")
			}
			withArgs(t, args...)
			stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdContext(cfg) })
			want := "legacy"
			if enabled {
				want = "history"
			}
			if recovered != nil || stderr != "" || stdout != want || optionsCalled != enabled || legacyCalled == enabled {
				t.Fatalf("stdout=%q stderr=%q panic=%v legacy=%v options=%v", stdout, stderr, recovered, legacyCalled, optionsCalled)
			}
		})
	}
}

func TestContextHistoryHelp(t *testing.T) {
	// Read concurrently: Windows pipes can fill before printUsage returns.
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Stdout = old; _ = r.Close(); _ = w.Close() })
	os.Stdout = w
	output := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(r)
		output <- string(data)
	}()
	printUsage()
	_ = w.Close()
	os.Stdout = old
	if !strings.Contains(<-output, "--older-history") {
		t.Fatal("help omits --older-history")
	}
}
