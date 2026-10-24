package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

func stagedDigest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func stagedIntegrity(b []byte) string {
	h := sha512.Sum512(b)
	return "sha512-" + base64.StdEncoding.EncodeToString(h[:])
}

// This authored, never-executed program supplies synthetic Go metadata only.
// It is not a production workflow, source authenticity or runtime fixture.
func stagedMetadataFixture(t *testing.T) ([]byte, string, string) {
	return stagedProgramFixture(t, "package main\nfunc main() {}\n")
}

func stagedProgramFixture(t *testing.T, program string) ([]byte, string, string) {
	t.Helper()
	root := t.TempDir()
	write := func(name, s string) {
		t.Helper()
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module github.com/Gentleman-Programming/engram/v3\n\ngo 1.23\n")
	write("cmd/engram/main.go", program)
	command := func(name string, args ...string) string {
		t.Helper()
		c := exec.Command(name, args...)
		c.Dir = root
		c.Env = append(os.Environ(), "CGO_ENABLED=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
		b, e := c.CombinedOutput()
		if e != nil {
			t.Fatalf("synthetic fixture %s: %v: %s", name, e, b)
		}
		return strings.TrimSpace(string(b))
	}
	command("git", "init", "--quiet")
	command("git", "add", "go.mod", "cmd/engram/main.go")
	command("git", "-c", "user.name=Synthetic Fixture", "-c", "user.email=fixture@example.invalid", "-c", "core.hooksPath="+filepath.Join(root, "no-hooks"), "commit", "--quiet", "-m", "synthetic metadata fixture")
	revision := command("git", "rev-parse", "HEAD")
	output := filepath.Join(t.TempDir(), "core")
	command("go", "build", "-buildvcs=true", "-o", output, "./cmd/engram")
	b, e := os.ReadFile(output)
	if e != nil {
		t.Fatal(e)
	}
	return b, revision, root
}

func TestStagedArtifacts(t *testing.T) {
	core, revision, source := stagedMetadataFixture(t)
	host := "engram_" + runtime.GOOS + "_" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		host += ".exe"
	}
	pi := []byte("opaque Pi fixture: validated elsewhere, never executed")
	want := stagingExpectation{WorkflowSHA: strings.Repeat("a", 40), CandidateSHA: revision, RunID: "123", RunAttempt: "1", CoreVersion: "1.2.3", PiVersion: "4.5.6"}
	base := map[string][]byte{"gentle-engram-4.5.6.tgz": pi}
	for _, system := range []string{"linux", "darwin", "windows"} {
		for _, arch := range []string{"amd64", "arm64"} {
			name := "engram_" + system + "_" + arch
			if system == "windows" {
				name += ".exe"
			}
			base[name] = []byte("nonhost placeholder")
			base[name+".buildinfo.txt"] = []byte("synthetic inventory only")
		}
	}
	base[host] = core
	pack := []map[string]any{{"name": "gentle-engram", "version": want.PiVersion, "filename": "gentle-engram-4.5.6.tgz", "integrity": stagedIntegrity(pi)}}
	encode := func(v any) []byte {
		b, e := json.Marshal(v)
		if e != nil {
			t.Fatal(e)
		}
		return b
	}
	base["npm-pack.json"] = encode(pack)
	type mutation struct {
		name      string
		expected  func(*stagingExpectation)
		members   func(map[string][]byte)
		manifest  func(map[string]any)
		mode      os.FileMode
		extra     string
		omit      string
		duplicate bool
		crc       bool
		missing   bool
		cancelled bool
	}
	cases := []mutation{{name: "matching"},
		{name: "ZIP SHA", expected: func(w *stagingExpectation) { w.ZIPHash = strings.Repeat("0", 64) }},
		{name: "workflow", expected: func(w *stagingExpectation) { w.WorkflowSHA = "wrong" }},
		{name: "candidate source", expected: func(w *stagingExpectation) { w.CandidateSHA = "wrong" }},
		{name: "run", expected: func(w *stagingExpectation) { w.RunID = "wrong" }},
		{name: "attempt", expected: func(w *stagingExpectation) { w.RunAttempt = "wrong" }},
		{name: "Core version", expected: func(w *stagingExpectation) { w.CoreVersion = "wrong" }},
		{name: "Pi version", expected: func(w *stagingExpectation) { w.PiVersion = "wrong" }},
		{name: "missing context", expected: func(w *stagingExpectation) { w.WorkflowSHA = "" }},
		{name: "package name", manifest: func(m map[string]any) { m["package_name"] = "wrong" }},
		{name: "member SHA", manifest: func(m map[string]any) { m["files"].([]map[string]any)[0]["sha256"] = "wrong" }},
		{name: "member SRI", manifest: func(m map[string]any) { m["files"].([]map[string]any)[0]["sri"] = "wrong" }},
		{name: "member size", manifest: func(m map[string]any) { m["files"].([]map[string]any)[0]["size"] = 999 }},
		{name: "manifest duplicate", manifest: func(m map[string]any) { f := m["files"].([]map[string]any); m["files"] = append(f, f[0]) }},
		{name: "manifest incomplete", manifest: func(m map[string]any) { m["files"] = m["files"].([]map[string]any)[1:] }},
		{name: "npm source", members: func(m map[string][]byte) { m["npm-pack.json"] = []byte(`[{"name":"wrong"}]`) }},
		{name: "missing inventory", omit: "npm-pack.json"},
		{name: "extra inventory", extra: "extra"},
		{name: "path inventory", extra: "../escape"},
		{name: "duplicate inventory", duplicate: true},
		{name: "symlink member", mode: os.ModeSymlink | 0777},
		{name: "directory member", mode: os.ModeDir | 0700},
		{name: "CRC", crc: true},
		{name: "unavailable", missing: true},
		{name: "cancelled", cancelled: true},
		{name: "invalid Core", members: func(m map[string][]byte) { m[host] = []byte("not Go buildinfo") }},
	}
	// Change actual embedded build metadata, then recompute all enclosing hashes.
	// Equal-length substitutions preserve the executable container framing.
	for _, setting := range []struct{ name, old, new string }{
		{"module", "github.com/Gentleman-Programming/engram/v3", "github.com/Gentleman-Programming/engram/v9"},
		{"VCS", revision, strings.Repeat("b", 40)},
		{"dirty", "vcs.modified=false", "vcs.modified=true "},
		{"host OS", "GOOS=" + runtime.GOOS, "GOOS=" + strings.Repeat("x", len(runtime.GOOS))},
		{"host arch", "GOARCH=" + runtime.GOARCH, "GOARCH=" + strings.Repeat("x", len(runtime.GOARCH))},
		{"CGO", "CGO_ENABLED=0", "CGO_ENABLED=1"},
	} {
		s := setting
		cases = append(cases, mutation{name: "actual metadata " + s.name, members: func(m map[string][]byte) {
			if !bytes.Contains(core, []byte(s.old)) {
				t.Fatalf("metadata fixture lacks %q", s.old)
			}
			m[host] = bytes.ReplaceAll(core, []byte(s.old), []byte(s.new))
		}})
	}
	snapshot := func(root string) map[string]string {
		t.Helper()
		out := map[string]string{}
		e := filepath.WalkDir(root, func(p string, d os.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if !d.IsDir() {
				b, e := os.ReadFile(p)
				if e != nil {
					return e
				}
				out[p] = stagedDigest(b)
			}
			return nil
		})
		if e != nil {
			t.Fatal(e)
		}
		return out
	}
	sourceBefore := encode(snapshot(source))
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			members := map[string][]byte{}
			for n, b := range base {
				members[n] = bytes.Clone(b)
			}
			if tc.members != nil {
				tc.members(members)
			}
			files := []map[string]any{}
			names := []string{}
			for n := range members {
				names = append(names, n)
			}
			sort.Strings(names)
			for _, n := range names {
				b := members[n]
				files = append(files, map[string]any{"name": n, "size": len(b), "sha256": stagedDigest(b), "sri": stagedIntegrity(b)})
			}
			manifest := map[string]any{"workflow_sha": want.WorkflowSHA, "candidate_sha": want.CandidateSHA, "run_id": want.RunID, "run_attempt": want.RunAttempt, "core_version": want.CoreVersion, "package_name": "gentle-engram", "package_version": want.PiVersion, "files": files}
			if tc.manifest != nil {
				tc.manifest(manifest)
			}
			members["manifest.json"] = encode(manifest)
			if tc.extra != "" {
				members[tc.extra] = []byte("extra")
			}
			var buffer bytes.Buffer
			zw := zip.NewWriter(&buffer)
			put := func(n string, b []byte) {
				h := &zip.FileHeader{Name: n, Method: zip.Store}
				h.SetMode(0600)
				if n == host && tc.mode != 0 {
					h.SetMode(tc.mode)
				}
				w, e := zw.CreateHeader(h)
				if e != nil {
					t.Fatal(e)
				}
				if _, e = w.Write(b); e != nil {
					t.Fatal(e)
				}
			}
			for n, b := range members {
				if n != tc.omit {
					put(n, b)
				}
			}
			if tc.duplicate {
				put("npm-pack.json", members["npm-pack.json"])
			}
			if e := zw.Close(); e != nil {
				t.Fatal(e)
			}
			archive := bytes.Clone(buffer.Bytes())
			if tc.crc {
				i := bytes.Index(archive, pi)
				if i < 0 {
					t.Fatal("missing CRC fixture")
				}
				archive[i] ^= 1
			}
			filename := filepath.Join(t.TempDir(), "stage.zip")
			if e := os.WriteFile(filename, archive, 0600); e != nil {
				t.Fatal(e)
			}
			w := want
			w.ZIPHash = stagedDigest(archive)
			if tc.expected != nil {
				tc.expected(&w)
			}
			ctx := context.Background()
			if tc.cancelled {
				c, cancel := context.WithCancel(ctx)
				cancel()
				ctx = c
			}
			path := filename
			if tc.missing {
				path += ".absent"
			}
			got, err := readStagedArtifacts(ctx, path, w)
			if tc.name == "matching" {
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(got.Core, core) || !bytes.Equal(got.Pi, pi) || got.CoreSHA256 != stagedDigest(core) || got.PiSHA256 != stagedDigest(pi) || got.ZIPHash != w.ZIPHash {
					t.Fatal("returned bytes/digests differ")
				}
			} else {
				if err == nil {
					t.Fatal("accepted inconsistent input")
				}
				if len(got.Core) != 0 || len(got.Pi) != 0 || got.CoreSHA256 != "" || got.PiSHA256 != "" || got.ZIPHash != "" {
					t.Fatal("rejected result leaked bytes/digests")
				}
			}
			after, e := os.ReadFile(filename)
			if e != nil || !bytes.Equal(after, archive) {
				t.Fatal("original ZIP changed", e)
			}
			if !bytes.Equal(sourceBefore, encode(snapshot(source))) {
				t.Fatal("source fixture data or file inventory changed")
			}
		})
	}
	t.Run("source symlink rejected before target open", func(t *testing.T) {
		link := filepath.Join(t.TempDir(), "link.zip")
		if e := os.Symlink(filepath.Join(t.TempDir(), "missing-target"), link); e != nil {
			t.Fatalf("required symlink prerequisite unavailable: %v", e)
		}
		w := want
		w.ZIPHash = strings.Repeat("a", 64)
		got, e := readStagedArtifacts(context.Background(), link, w)
		if e == nil || !strings.HasPrefix(e.Error(), "staging: source") || len(got.Core) != 0 || got.ZIPHash != "" {
			t.Fatalf("must reject source path before opening target: %v", e)
		}
	})
}
