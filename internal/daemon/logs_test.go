package daemon

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLogPathAndAppendSink(t *testing.T) {
	dir := t.TempDir()
	path := LogPath(dir)
	if filepath.Base(path) != LogName {
		t.Fatalf("log base = %q, want %q", filepath.Base(path), LogName)
	}
	file, err := OpenLog(path)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	if _, err := file.WriteString("first line\n"); err != nil {
		t.Fatalf("write log: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	file, err = OpenLog(path)
	if err != nil {
		t.Fatalf("reopen log: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "first line\n" {
		t.Fatalf("log content = %q, want appended first line", data)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("log mode = %v, want 0600", info.Mode().Perm())
		}
	}
}
