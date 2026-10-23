package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestArtifactInputCLI(t *testing.T) {
	core, revision, repo := stagedMetadataFixture(t) // Compiled metadata only; never executed.
	write := func(name string, data []byte) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	encode := func(v any) []byte {
		t.Helper()
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_OPTIONAL_LOCKS=0")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git: %v: %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	var packed bytes.Buffer
	gz := gzip.NewWriter(&packed)
	tw := tar.NewWriter(gz)
	for _, name := range packageRuntimeFiles {
		body := []byte("throw new Error('archive must not execute');\n")
		if name == "package.json" {
			body = encode(map[string]any{"name": "gentle-engram", "version": "4.5.6", "files": packageRuntimeFiles})
		}
		write(filepath.Join(repo, "plugin", "pi", name), body)
		if err := tw.WriteHeader(&tar.Header{Name: "package/" + name, Mode: 0644, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	git("add", "plugin")
	git("-c", "user.name=Artifact Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "-c", "core.hooksPath="+filepath.Join(repo, "no-hooks"), "commit", "--quiet", "-m", "Pi source")
	piRevision := git("rev-parse", "HEAD") // Independently selected Pi source, not working-tree bytes.
	host := "engram_" + runtime.GOOS + "_" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		host += ".exe"
	}
	members := map[string][]byte{"gentle-engram-4.5.6.tgz": packed.Bytes()}
	for _, system := range []string{"linux", "darwin", "windows"} {
		for _, arch := range []string{"amd64", "arm64"} {
			name := "engram_" + system + "_" + arch
			if system == "windows" {
				name += ".exe"
			}
			members[name], members[name+".buildinfo.txt"] = []byte("synthetic nonhost"), []byte("synthetic metadata inventory")
		}
	}
	members[host] = core
	members["npm-pack.json"] = encode([]map[string]any{{"name": "gentle-engram", "version": "4.5.6", "filename": "gentle-engram-4.5.6.tgz", "integrity": stagedIntegrity(packed.Bytes())}})
	want := stagingExpectation{WorkflowSHA: strings.Repeat("a", 40), CandidateSHA: revision, RunID: "123", RunAttempt: "1", CoreVersion: "1.2.3", PiVersion: "4.5.6"}
	files := []map[string]any{}
	for name, b := range members {
		files = append(files, map[string]any{"name": name, "size": len(b), "sha256": stagedDigest(b), "sri": stagedIntegrity(b)})
	}
	members["manifest.json"] = encode(map[string]any{"workflow_sha": want.WorkflowSHA, "candidate_sha": revision, "run_id": want.RunID, "run_attempt": want.RunAttempt, "core_version": want.CoreVersion, "package_name": "gentle-engram", "package_version": want.PiVersion, "files": files})
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	for name, b := range members {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(b); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	want.ZIPHash = stagedDigest(archive.Bytes())
	input := t.TempDir()
	bundle, contextFile, bin := filepath.Join(input, "bundle.zip"), filepath.Join(input, "context.json"), filepath.Join(input, "candidate.exe")
	write(bundle, archive.Bytes())
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v: %s", err, out)
	}
	invoke := func(args []string) (string, string, int) {
		t.Helper()
		cmd := exec.Command(bin, args...)
		var out, diagnostic bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &diagnostic
		code := 0
		if err := cmd.Run(); err != nil {
			if e, ok := err.(*exec.ExitError); ok {
				code = e.ExitCode()
			} else {
				t.Fatal(err)
			}
		}
		return out.String(), diagnostic.String(), code
	}
	for _, tc := range []struct{ name, state string }{
		{"matching", "passed"}, {"missing context", "unavailable"}, {"malformed context", "unavailable"},
		{"context mismatch", "unavailable"}, {"digest mismatch", "unavailable"}, {"Pi source mismatch", "not-established"},
		{"context boundary", "passed"}, {"context oversized", "unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expected, pi := want, piRevision
			if tc.name == "context mismatch" {
				expected.RunID = "124"
			}
			if tc.name == "digest mismatch" {
				expected.ZIPHash = strings.Repeat("0", 64)
			}
			if tc.name == "Pi source mismatch" {
				pi = revision
			}
			data := encode(expected)
			if tc.name == "malformed context" {
				data = []byte("not JSON")
			}
			if tc.name == "context boundary" || tc.name == "context oversized" {
				limit := 4096
				if tc.name == "context oversized" {
					limit++
				}
				data = append(data, bytes.Repeat([]byte(" "), limit-len(data))...)
			}
			write(contextFile, data)
			contextPath := contextFile
			if tc.name == "missing context" {
				contextPath = filepath.Join(input, "missing")
			}
			before, err := sourceTreeDigest(repo)
			if err != nil {
				t.Fatal(err)
			}
			args := []string{"-root", repo, "-core-ref", "refs/tags/v1.2.3", "-pi-ref", "refs/tags/pi-v4.5.6", "-core-source", revision, "-pi-source", pi, "-observe-artifact-inputs", "-staging-bundle", bundle, "-staging-context", contextPath}
			out, diagnostic, code := invoke(args)
			if code != 2 || diagnostic != "" {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, diagnostic)
			}
			var result map[string]any
			if err := json.Unmarshal([]byte(out), &result); err != nil || strings.Count(out, "\n") != 1 {
				t.Fatalf("not one JSON line: %q", out)
			}
			if result["outcome"] != "blocked" || result["schemaVersion"] != float64(1) || result["check"].(map[string]any)["state"] != tc.state || result["workflows"].(map[string]any)["state"] != "unavailable" {
				t.Fatalf("unexpected report: %s", out)
			}
			if tc.state == "passed" {
				measured := result["artifactInputs"].(map[string]any)
				pkg := result["piPackage"].(map[string]any)
				if measured["zipSha256"] != want.ZIPHash || measured["coreSha256"] != stagedDigest(core) || measured["piSha256"] != stagedDigest(packed.Bytes()) || measured["platform"] != runtime.GOOS+"/"+runtime.GOARCH || pkg["files"] != float64(len(packageRuntimeFiles)) || pkg["state"] != "passed" || result["core"].(map[string]any)["commit"] != revision || result["pi"].(map[string]any)["commit"] != piRevision {
					t.Fatalf("wrong measured evidence: %s", out)
				}
			}
			after, err := sourceTreeDigest(repo)
			b, readErr := os.ReadFile(bundle)
			c, ctxErr := os.ReadFile(contextFile)
			if err != nil || before != after || readErr != nil || !bytes.Equal(b, archive.Bytes()) || ctxErr != nil || !bytes.Equal(c, data) {
				t.Fatal("changed source/Git/input or left owned workspace")
			}
		})
	}
	t.Run("false preserves legacy", func(t *testing.T) {
		root, err := filepath.Abs("../..")
		if err != nil {
			t.Fatal(err)
		}
		manifest, err := os.ReadFile(filepath.Join(root, "plugin/pi/package.json"))
		if err != nil {
			t.Fatal(err)
		}
		var pkg struct{ Version string }
		if err := json.Unmarshal(manifest, &pkg); err != nil {
			t.Fatal(err)
		}
		args := []string{"-root", root, "-core-ref", "refs/tags/v1.2.3", "-pi-ref", "refs/tags/pi-v" + pkg.Version}
		for _, mode := range []struct {
			flags []string
			code  int
		}{
			{nil, 0}, {[]string{"-assess"}, 2}, {[]string{"-pi-tarball", bundle}, 1},
			{[]string{"-observe-contract", "-core-source", revision, "-pi-source", piRevision}, 2},
			{[]string{"-observe-contract", "-core-source", revision, "-pi-source", piRevision, "-observe-pi-inputs"}, 2},
			{[]string{"-observe-contract", "-core-source", revision, "-pi-source", piRevision, "-observe-pi-package", "-pi-tarball", bundle}, 2},
		} {
			command := append(append([]string{}, args...), mode.flags...)
			a, b, c := invoke(command)
			x, y, z := invoke(append(command, "-observe-artifact-inputs=false"))
			if c != mode.code || !reflect.DeepEqual([]any{a, b, c}, []any{x, y, z}) {
				t.Fatalf("legacy %v changed: exits %d/%d, stdout equal %v, stderr equal %v", mode.flags, c, z, a == x, b == y)
			}
		}
	})
}
