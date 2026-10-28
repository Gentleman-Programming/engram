package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func migrateCopyTemp(t *testing.T) string {
	t.Helper()
	d, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	return d
}

type migrateCopyTreeEntry struct {
	info   os.FileInfo
	digest [32]byte
}

// Include directories as well as file bytes: an unsuccessful copy must not
// create entries or change source identities, modes, sizes or modification times.
func migrateCopyTree(t *testing.T, root string) map[string]migrateCopyTreeEntry {
	t.Helper()
	tree := map[string]migrateCopyTreeEntry{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		value := migrateCopyTreeEntry{info: info}
		if !info.IsDir() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			value.digest = sha256.Sum256(data)
		}
		tree[relative] = value
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func migrateCopyTreesEqual(before, after map[string]migrateCopyTreeEntry) bool {
	if len(before) != len(after) {
		return false
	}
	for path, a := range before {
		b, ok := after[path]
		if !ok || a.digest != b.digest || a.info.Mode() != b.info.Mode() || a.info.Size() != b.info.Size() || !a.info.ModTime().Equal(b.info.ModTime()) || !os.SameFile(a.info, b.info) {
			return false
		}
	}
	return true
}

func TestMigrateCopyPhysicalOverlapProcess(t *testing.T) {
	root := migrateCopyTemp(t)
	source := filepath.Join(root, "case-source")
	s, err := store.New(store.Config{DataDir: source})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(source, "nested")
	if err = os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(nested, "cursor.json"), []byte(`{"cursor":17}`), 0600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "CASE-SOURCE")
	sourceInfo, err := os.Lstat(source)
	if err != nil {
		t.Fatal(err)
	}
	aliasInfo, err := os.Lstat(alias)
	if os.IsNotExist(err) {
		t.Skip("fixture filesystem does not expose a case alias")
	}
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(sourceInfo, aliasInfo) {
		t.Skip("case spelling identifies a distinct directory")
	}
	before := migrateCopyTree(t, source)
	output := filepath.Join(alias, "nested-output")
	command := exec.Command(os.Args[0], "-test.run=^TestMigrateCopyProcess$")
	command.Env = []string{"ENGRAM_COPY_TEST_CHILD=1", "ENGRAM_COPY_TEST_ARGS=" + strings.Join([]string{"migrate-copy", "--data-dir", source, "--output-dir", output, "--confirm-quiescent", "--json"}, "\n"), "HOME=" + migrateCopyTemp(t)}
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err = command.Run()
	exit, ok := err.(*exec.ExitError)
	t.Logf("public dispatch stdout=%q stderr=%q error=%v", stdout.String(), stderr.String(), err)
	after := migrateCopyTree(t, source)
	if !migrateCopyTreesEqual(before, after) {
		t.Error("source tree or metadata changed on physical-overlap rejection")
	}
	if _, err = os.Lstat(output); !os.IsNotExist(err) {
		t.Errorf("output created inside source: %v", err)
	}
	if !ok || exit.ExitCode() != 1 || stdout.String() != "" || stderr.String() != "engram migrate-copy: source and output must be disjoint directories\n" {
		t.Fatalf("physical overlap: stdout=%q stderr=%q exit=%v", stdout.String(), stderr.String(), exit)
	}
}

func TestMigrateCopyProcess(t *testing.T) {
	if os.Getenv("ENGRAM_COPY_TEST_CHILD") == "1" {
		os.Args = append([]string{"engram"}, strings.Split(os.Getenv("ENGRAM_COPY_TEST_ARGS"), "\n")...)
		main()
		os.Exit(0)
		return
	}
	run := func(args ...string) (string, string, int) {
		t.Helper()
		c := exec.Command(os.Args[0], "-test.run=^TestMigrateCopyProcess$")
		// Deliberately do not inherit credentials, cloud/autosync flags, project or data directory.
		home := migrateCopyTemp(t)
		c.Env = []string{"ENGRAM_COPY_TEST_CHILD=1", "ENGRAM_COPY_TEST_ARGS=" + strings.Join(args, "\n"), "HOME=" + home}
		var out, err bytes.Buffer
		c.Stdout = &out
		c.Stderr = &err
		e := c.Run()
		code := 0
		if e != nil {
			if x, ok := e.(*exec.ExitError); ok {
				code = x.ExitCode()
			} else {
				t.Fatal(e)
			}
		}
		if _, e := os.Stat(filepath.Join(home, ".engram")); !os.IsNotExist(e) {
			t.Fatalf("early command touched default data directory: %v", e)
		}
		return out.String(), err.String(), code
	}
	src := filepath.Join(migrateCopyTemp(t), "source")
	s, e := store.New(store.Config{DataDir: src})
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	cases := []struct {
		args    []string
		message string
	}{
		{[]string{}, "required flag --data-dir"},
		{[]string{"--data-dir"}, "missing value for --data-dir"},
		{[]string{"--data-dir", src, "--output-dir", "relative"}, "--output-dir must be absolute"},
		{[]string{"--data-dir", src, "--output-dir", filepath.Join(migrateCopyTemp(t), "copy"), "--json"}, "required flag --confirm-quiescent"},
		{[]string{"--data-dir", src, "--data-dir", src}, "duplicate flag --data-dir"},
		{[]string{"--data-dir", src, "--output-dir", filepath.Join(migrateCopyTemp(t), "copy"), "--confirm-quiescent", "--json", "--wat"}, "unknown flag --wat"},
		{[]string{"--json", "--json"}, "duplicate flag --json"},
		{[]string{"--confirm-quiescent", "--confirm-quiescent"}, "duplicate flag --confirm-quiescent"},
		{[]string{"--output-dir", src, "--output-dir", src}, "duplicate flag --output-dir"},
		{[]string{"--data-dir", src, "--output-dir", filepath.Join(migrateCopyTemp(t), "copy"), "--confirm-quiescent"}, "required flag --json"},
	}
	dbBefore, e := os.ReadFile(filepath.Join(src, "engram.db"))
	if e != nil {
		t.Fatal(e)
	}
	for _, tc := range cases {
		out, err, code := run(append([]string{"migrate-copy"}, tc.args...)...)
		if code != 2 || out != "" || err != "engram migrate-copy: "+tc.message+"\n" {
			t.Fatalf("rejection stdout=%q stderr=%q exit=%d", out, err, code)
		}
		for i, a := range tc.args {
			if a == "--output-dir" && i+1 < len(tc.args) && tc.args[i+1] != src && filepath.IsAbs(tc.args[i+1]) {
				if _, e = os.Lstat(tc.args[i+1]); !os.IsNotExist(e) {
					t.Fatalf("rejected output exists: %v", e)
				}
			}
		}
	}
	dbAfter, e := os.ReadFile(filepath.Join(src, "engram.db"))
	if e != nil || !bytes.Equal(dbBefore, dbAfter) {
		t.Fatal("source modified on parse rejection")
	}
	dst := filepath.Join(migrateCopyTemp(t), "copy")
	out, err, code := run("migrate-copy", "--data-dir", src, "--output-dir", dst, "--confirm-quiescent", "--json")
	if code != 0 || err != "" {
		t.Fatalf("stdout=%q stderr=%q exit=%d", out, err, code)
	}
	var r store.MigrateCopyResult
	if e = json.Unmarshal([]byte(out), &r); e != nil || !r.MigrationCompleted || r.SourceDir != src || r.OutputDir != dst || r.InstanceID == "" {
		t.Fatalf("evidence: %q %v", out, e)
	}
	expected, e := json.Marshal(r)
	if e != nil || out != string(expected)+"\n" {
		t.Fatalf("stdout must be exactly one JSON result: %q", out)
	}
	out, err, code = run("migrate-copy", "--help")
	if out != migrateCopyUsage || err != "" || code != 0 {
		t.Fatalf("help: stdout=%q stderr=%q exit=%d", out, err, code)
	}
	out, err, code = run("compatibility", "--data-dir", migrateCopyTemp(t), "--json")
	want := "{\"revision\":1,\"source\":\"offline\",\"status\":\"fresh\",\"offline_automatic_opening_authorized\":false,\"active_reuse_authorized\":false}\n"
	if code != 0 || err != "" || out != want {
		t.Fatalf("prior dispatch: stdout=%q stderr=%q exit=%d", out, err, code)
	}
	out, err, code = run("compatibility", "--data-dir", src, "--json")
	if code != 2 || out != "" || err != "compatibility: unsafe or invalid fixture metadata\n" {
		t.Fatalf("WAL compatibility regression: stdout=%q stderr=%q exit=%d", out, err, code)
	}
	// Operational failure must not emit successful JSON or overwrite caller data.
	out, err, code = run("migrate-copy", "--data-dir", src, "--output-dir", dst, "--confirm-quiescent", "--json")
	if code != 1 || out != "" || err != "engram migrate-copy: output directory must not exist\n" {
		t.Fatalf("existing output: stdout=%q stderr=%q exit=%d", out, err, code)
	}
}
