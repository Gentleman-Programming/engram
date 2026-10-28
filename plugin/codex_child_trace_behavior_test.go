package plugin_test

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCodexChildTraceScope(t *testing.T) {
	for _, tc := range []struct {
		os, name, dir string
		want          bool
	}{
		{"windows", "empty confirmation", "", false},
		{"windows", "missing confirmation", "capture", false},
		{"linux", "empty confirmation", "capture", false},
		{"windows", "empty confirmation", "capture", true},
	} {
		if got := codexChildTraceEnabled(tc.os, tc.name, tc.dir); got != tc.want {
			t.Errorf("scope(%q,%q,%q)=%v, want %v", tc.os, tc.name, tc.dir, got, tc.want)
		}
	}
}

func TestCodexChildTraceRollsBackPartialWrite(t *testing.T) {
	root := t.TempDir()
	patches := []codexTracePatch{{filepath.Join(root, "gate"), []byte("guard\n"), []byte("guard\ntrace\n"), 0600}, {filepath.Join(root, "shim"), []byte("#!/bin/bash -p\noriginal\n"), []byte("changed\n"), 0700}}
	for _, p := range patches {
		if err := os.WriteFile(p.path, p.before, p.mode); err != nil {
			t.Fatal(err)
		}
	}
	failed := false
	err := applyCodexTracePatches(patches, func(p string, b []byte, m os.FileMode) error {
		if p == patches[1].path && !failed {
			failed = true
			if err := os.WriteFile(p, []byte("partial"), m); err != nil {
				return err
			}
			return errors.New("injected write failure")
		}
		return os.WriteFile(p, b, m)
	})
	if err == nil {
		t.Fatal("missing preparation failure")
	}
	for _, p := range patches {
		got, err := os.ReadFile(p.path)
		if err != nil || !bytes.Equal(got, p.before) {
			t.Fatalf("original fixture not restored: %q %v", got, err)
		}
	}
}

func TestCodexChildTraceRelativeDestinationLeavesOracleUntouched(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("requires native Windows paths")
	}
	t.Chdir(t.TempDir())
	if err := os.Mkdir("relative", 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(codexChildTraceEnv, "relative")
	stdout, stderr := bytes.NewBufferString("{}\n"), bytes.NewBuffer(nil)
	finish := startCodexChildTrace(t, "empty confirmation", t.TempDir(), &exec.Cmd{}, stdout, stderr)
	finish()
	// Stand in for the original rejection oracle after observer completion.
	if stdout.String() != "{}\n" || stderr.Len() != 0 {
		t.Fatal("observer changed original streams")
	}
	entries, err := os.ReadDir("relative")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("rejected observer destination received %d files", len(entries))
	}
}
