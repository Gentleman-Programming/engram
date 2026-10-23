package main

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func assessmentErrorFixture(t *testing.T) (string, func()) {
	t.Helper()
	root := t.TempDir()
	for name, content := range map[string]string{
		"plugin/pi/package.json":  `{"name":"gentle-engram","version":"0.5.0"}`,
		"internal/setup/setup.go": "package setup\nconst piGentleEngramPackage = \"npm:gentle-engram@0.5.0\"\n",
		"context.txt":             "unrelated candidate context\n",
	} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := func() map[string]string {
		files := map[string]string{}
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				files[path] = "directory"
				return nil
			}
			data, err := os.ReadFile(path)
			files[path] = string(data)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return files
	}
	before := snapshot()
	return root, func() {
		t.Helper()
		if !reflect.DeepEqual(before, snapshot()) {
			t.Fatal("assessment changed candidate metadata/context or fixture tree")
		}
	}
}

func TestAssessmentContractUnavailable(t *testing.T) {
	root, unchanged := assessmentErrorFixture(t)
	defer unchanged()
	for attempt := 0; attempt < 2; attempt++ {
		var output bytes.Buffer
		calls := 0
		err := assessCandidateWithContract(root, "3.0.0", "0.5.0", &output, func() ([]byte, error) {
			calls++
			return nil, errors.New("export failure details must not leak")
		})
		var result *assessmentExit
		// This is main's silent-result branch: assessmentExit bypasses stderr.
		if !errors.As(err, &result) || result.code != 2 {
			t.Fatalf("want silent blocked exit 2, got %v", err)
		}
		want := `{"schemaVersion":1,"version":"3.0.0","piVersion":"0.5.0","outcome":"blocked","reason":{"code":"contract-unavailable","message":"cannot export live Go agent contract"}}` + "\n"
		if output.String() != want || calls != 1 {
			t.Fatalf("calls=%d output=%q; want one export and %q", calls, output.String(), want)
		}
	}
}

type assessmentFailingWriter struct {
	err   error
	calls int
}

func (w *assessmentFailingWriter) Write([]byte) (int, error) {
	w.calls++
	return 0, w.err
}

func TestAssessmentRunWriterError(t *testing.T) {
	root, unchanged := assessmentErrorFixture(t)
	defer unchanged()
	want := errors.New("report sink unavailable")
	output := &assessmentFailingWriter{err: want}
	err := run([]string{"-assess", "-root", root, "-core-ref", "refs/tags/v3.0.0", "-pi-ref", "refs/tags/pi-v0.5.0"}, output)
	var result *assessmentExit
	if err != want || errors.As(err, &result) || output.calls != 1 {
		t.Fatalf("want exact writer error, no assessment result, one write; got error=%v calls=%d", err, output.calls)
	}
}
