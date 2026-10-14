package main_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/mcp"
)

// These tests intentionally invoke the public command: an absent command is a
// behavior-level RED, not an undefined production symbol in the test package.
func candidateFixture(t *testing.T, version string) string {
	t.Helper()
	root := t.TempDir()
	candidateWrite(t, root, "plugin/pi/package.json", `{"name":"gentle-engram","version":"`+version+`"}`)
	candidateWrite(t, root, "internal/setup/setup.go", "package setup\nconst piGentleEngramPackage = \"npm:gentle-engram@"+version+"\"\n")
	return root
}

func candidateWrite(t *testing.T, root, name, text string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0644); err != nil {
		t.Fatal(err)
	}
}

func candidateSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[path] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func candidateRun(t *testing.T, root string, args ...string) (string, string, int) {
	t.Helper()
	before := candidateSnapshot(t, root)
	// The working directory is the package directory, not the fixture. Only
	// metadata comes from -root; the contract must come from the live Go owner.
	cmd := exec.Command("go", append([]string{"run", ".", "-root", root}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			code = exit.ExitCode()
		} else {
			t.Fatalf("launch public CLI: %v", err)
		}
	}
	after := candidateSnapshot(t, root)
	if len(before) != len(after) {
		t.Fatal("command changed candidate file set")
	}
	for path, text := range before {
		if after[path] != text {
			t.Fatalf("command modified candidate file %s", path)
		}
	}
	return stdout.String(), stderr.String(), code
}

func candidateRefs(version string) []string {
	return []string{"-core-ref", "refs/tags/v" + version, "-pi-ref", "refs/tags/pi-v" + version}
}

func TestReleaseCandidateAcceptsCanonicalVersionsAndExportsLiveContract(t *testing.T) {
	live, err := mcp.AgentContractJSON()
	if err != nil {
		t.Fatal(err)
	}
	var expected any
	if err := json.Unmarshal(live, &expected); err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"0.0.0", "1.2.3", "12.34.56", "1.2.3-0", "1.2.3-alpha.0", "1.2.3-01a", "1.2.3-rc.1+build.001", "1.2.3+001.sha-abc", "999999999999999999999999.2.3-999999999999999999999999"} {
		t.Run(version, func(t *testing.T) {
			root := candidateFixture(t, version)
			out, diagnostic, code := candidateRun(t, root, candidateRefs(version)...)
			if code != 0 || diagnostic != "" {
				t.Fatalf("exit=%d stderr=%q stdout=%q", code, diagnostic, out)
			}
			var result map[string]json.RawMessage
			if err := json.Unmarshal([]byte(out), &result); err != nil {
				t.Fatalf("single JSON object: %v: %q", err, out)
			}
			if len(result) != 2 {
				t.Fatalf("unexpected envelope fields: %s", out)
			}
			var gotVersion string
			if err := json.Unmarshal(result["version"], &gotVersion); err != nil || gotVersion != version {
				t.Fatalf("version: %s", out)
			}
			var contract any
			if err := json.Unmarshal(result["contract"], &contract); err != nil {
				t.Fatal(err)
			}
			got, err := json.Marshal(contract)
			if err != nil {
				t.Fatal(err)
			}
			want, err := json.Marshal(expected)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Fatal("export differs from live Go AgentContractJSON")
			}
		})
	}
}

func TestReleaseCandidateHelpAndArguments(t *testing.T) {
	root := candidateFixture(t, "1.2.3")
	for _, flag := range []string{"-h", "--help"} {
		out, diagnostic, code := candidateRun(t, root, flag)
		if code != 0 || diagnostic != "" {
			t.Fatalf("help exit=%d stderr=%q", code, diagnostic)
		}
		for _, text := range []string{"-core-ref", "-pi-ref", "-root", "metadata directory", "not Pi parity or release approval"} {
			if !strings.Contains(out, text) {
				t.Fatalf("help missing %q: %s", text, out)
			}
		}
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"-skip"}, "flag provided but not defined: -skip"},
		{[]string{"-publish"}, "flag provided but not defined: -publish"},
		{[]string{"-core-ref"}, "flag needs an argument: -core-ref"},
		{append(candidateRefs("1.2.3"), "extra"), "positional arguments are not supported"},
	} {
		out, diagnostic, code := candidateRun(t, root, tc.args...)
		first, _, _ := strings.Cut(diagnostic, "\n")
		if code == 0 || out != "" || first != tc.want {
			t.Fatalf("args=%q exit=%d stdout=%q stderr=%q", tc.args, code, out, diagnostic)
		}
	}
}

func TestReleaseCandidateRejectsInvalidInputs(t *testing.T) {
	type rejection struct {
		name                      string
		args                      []string
		file, content, diagnostic string
	}
	cases := []rejection{
		{name: "missing refs", diagnostic: "core-ref and pi-ref are required"},
		{name: "missing core", args: []string{"-pi-ref", "refs/tags/pi-v1.2.3"}, diagnostic: "core-ref and pi-ref are required"},
		{name: "missing pi", args: []string{"-core-ref", "refs/tags/v1.2.3"}, diagnostic: "core-ref and pi-ref are required"},
		{name: "different versions", args: []string{"-core-ref", "refs/tags/v1.2.3", "-pi-ref", "refs/tags/pi-v1.2.4"}, diagnostic: "core and Pi versions must match exactly"},
		{name: "different build metadata", args: []string{"-core-ref", "refs/tags/v1.2.3+a", "-pi-ref", "refs/tags/pi-v1.2.3+b"}, diagnostic: "core and Pi versions must match exactly"},
		{name: "package name", file: "plugin/pi/package.json", content: `{"name":"other","version":"1.2.3"}`, diagnostic: "package name must be gentle-engram"},
		{name: "package version", file: "plugin/pi/package.json", content: `{"name":"gentle-engram","version":"1.2.4"}`, diagnostic: "package version must match candidate version"},
		{name: "malformed package", file: "plugin/pi/package.json", content: `{`, diagnostic: "invalid plugin/pi/package.json"},
		{name: "missing package", file: "missing-package", diagnostic: "cannot read plugin/pi/package.json"},
		{name: "missing installer", file: "missing-installer", diagnostic: "cannot read internal/setup/setup.go"},
		{name: "wrong pin", file: "internal/setup/setup.go", content: "package setup\nconst piGentleEngramPackage = \"npm:gentle-engram@1.2.4\"\n", diagnostic: "installer pin must match candidate version exactly"},
		{name: "range pin", file: "internal/setup/setup.go", content: "package setup\nconst piGentleEngramPackage = \"npm:gentle-engram@^1.2.3\"\n", diagnostic: "installer pin must match candidate version exactly"},
		{name: "missing pin", file: "internal/setup/setup.go", content: "package setup\n", diagnostic: "expected exactly one piGentleEngramPackage pin"},
		{name: "duplicate pin", file: "internal/setup/setup.go", content: "package setup\nconst piGentleEngramPackage = \"npm:gentle-engram@1.2.3\"\nconst piGentleEngramPackage = \"npm:gentle-engram@1.2.3\"\n", diagnostic: "expected exactly one piGentleEngramPackage pin"},
	}
	for _, version := range []string{"", "1", "1.2", "01.2.3", "1.02.3", "1.2.03", "1.2.3-01", "1.2.3-alpha.01", "1.2.3-", "1.2.3+", "1.2.3-a..b", "1.2.3+a..b", "1.2.3-a_b", "1.2.3\n", " 1.2.3", "1.2.3.4"} {
		cases = append(cases, rejection{name: "invalid semver/" + version, args: candidateRefs(version), diagnostic: "invalid core-ref: expected refs/tags/v<strict-semver>"})
	}
	for _, ref := range []string{"v1.2.3", "1.2.3", "refs/heads/v1.2.3", "refs/tags/pi-v1.2.3"} {
		cases = append(cases, rejection{name: "core boundary/" + ref, args: []string{"-core-ref", ref, "-pi-ref", "refs/tags/pi-v1.2.3"}, diagnostic: "invalid core-ref: expected refs/tags/v<strict-semver>"})
	}
	for _, ref := range []string{"pi-v1.2.3", "refs/tags/v1.2.3", "refs/heads/pi-v1.2.3", "refs/tags/pi-v1.2.3-01"} {
		cases = append(cases, rejection{name: "pi boundary/" + ref, args: []string{"-core-ref", "refs/tags/v1.2.3", "-pi-ref", ref}, diagnostic: "invalid pi-ref: expected refs/tags/pi-v<strict-semver>"})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if tc.file != "missing-package" {
				candidateWrite(t, root, "plugin/pi/package.json", `{"name":"gentle-engram","version":"1.2.3"}`)
			}
			if tc.file != "missing-installer" {
				candidateWrite(t, root, "internal/setup/setup.go", "package setup\nconst piGentleEngramPackage = \"npm:gentle-engram@1.2.3\"\n")
			}
			if tc.content != "" {
				candidateWrite(t, root, tc.file, tc.content)
			}
			args := tc.args
			if args == nil && tc.name != "missing refs" {
				args = candidateRefs("1.2.3")
			}
			out, diagnostic, code := candidateRun(t, root, args...)
			if code == 0 {
				t.Fatalf("accepted rejected input: %q", out)
			}
			if out != "" {
				t.Fatalf("rejection leaked stdout: %q", out)
			}
			// go run appends its own exit-status line; the application diagnostic
			// itself is exact and must be the first line, never toolchain noise.
			first, _, _ := strings.Cut(diagnostic, "\n")
			if first != tc.diagnostic {
				t.Fatalf("diagnostic=%q want=%q (exit %d)", diagnostic, tc.diagnostic, code)
			}
		})
	}
}
