package main_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPiPackageSourceEvidence(t *testing.T) {
	if testing.Short() {
		t.Skip("source observation runs the local Go toolchain")
	}
	bin := filepath.Join(t.TempDir(), "candidate.exe")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v: %s", err, out)
	}
	repo, entries := packageFixture(t)
	candidateWrite(t, repo, "go.mod", "module example.invalid/packagefixture\n\ngo 1.25.0\n")
	candidateWrite(t, repo, "docs/contracts/agent-contract-v1.json", "{\"status\":\"mcp-foundation\"}\n")
	entries[1].body = "throw new Error('archive entrypoint must never execute');\n"
	candidateWrite(t, repo, "plugin/pi/cli.js", entries[1].body)
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_OPTIONAL_LOCKS=0")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "--quiet")
	git("config", "user.name", "Package Fixture")
	git("config", "user.email", "fixture@example.invalid")
	git("config", "commit.gpgsign", "false")
	commit := func(mutation string) string {
		t.Helper()
		program := "package main\nimport \"os\"\nfunc main(){if len(os.Args)!=2||os.Args[1]!=\"-check\"{os.Exit(8)};" + mutation + "}\n"
		candidateWrite(t, repo, "tools/agent-contract/main.go", program)
		git("add", ".")
		git("commit", "--quiet", "-m", "fixture source")
		return git("rev-parse", "HEAD")
	}
	core := commit("")
	pi := commit("os.Exit(9)") // Pi's Go program cannot substitute for selected Core.
	candidateWrite(t, repo, "plugin/pi/index.ts", "different selected source")
	otherPi := commit("os.Exit(9)")
	candidateWrite(t, repo, "plugin/pi/index.ts", entries[2].body)
	mutatedArchive := commit("if os.WriteFile(\"../pi-package.tgz\",[]byte(\"changed\"),0600)!=nil{os.Exit(7)}")
	mutatedSource := commit("if os.WriteFile(\"../pi-package-source/plugin/pi/index.ts\",[]byte(\"changed\"),0600)!=nil{os.Exit(7)}")
	candidateWrite(t, repo, "plugin/pi/package.json", "not the selected working tree")
	archive := filepath.Join(t.TempDir(), "candidate.tgz")
	invoke := func(coreID, piID string, extra ...string) (map[string]any, string, string, int) {
		t.Helper()
		before := candidateSnapshot(t, repo)
		input, inputErr := os.ReadFile(archive)
		args := []string{"-root", repo, "-observe-contract", "-core-ref", "refs/tags/v3.0.0", "-pi-ref", "refs/tags/pi-v1.2.3", "-core-source", coreID, "-pi-source", piID}
		cmd := exec.Command(bin, append(args, extra...)...)
		var out, diagnostic bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &diagnostic
		code := 0
		if err := cmd.Run(); err != nil {
			if exit, ok := err.(*exec.ExitError); ok {
				code = exit.ExitCode()
			} else {
				t.Fatal(err)
			}
		}
		after, afterErr := os.ReadFile(archive)
		if !reflect.DeepEqual(before, candidateSnapshot(t, repo)) || !bytes.Equal(input, after) || (inputErr == nil) != (afterErr == nil) {
			t.Fatal("changed original source, Git, archive or left an owned workspace")
		}
		var report map[string]any
		if out.Len() != 0 {
			if err := json.Unmarshal(out.Bytes(), &report); err != nil || strings.Count(out.String(), "\n") != 1 {
				t.Fatalf("not one JSON line: %q", out.String())
			}
		}
		return report, out.String(), diagnostic.String(), code
	}
	for _, tc := range []struct{ name, core, pi, state string }{
		{"selected source not working tree", core, pi, "passed"},
		{"different selected source", core, otherPi, "not-established"},
		{"wrong archive version", core, pi, "not-established"},
		{"wrong archive content", core, pi, "not-established"},
		{"malformed gzip", core, pi, "not-established"},
		{"owned tarball mutation", mutatedArchive, pi, "unavailable"},
		{"owned source mutation", mutatedSource, pi, "unavailable"},
		{"missing archive", core, pi, "unavailable"},
		{"oversized archive", core, pi, "unavailable"},
		{"directory archive", core, pi, "unavailable"},
		{"Pi input observation coexistence", core, pi, "passed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			packed := append([]packedEntry(nil), entries...)
			if tc.name == "wrong archive version" {
				packed[0].body = strings.ReplaceAll(packed[0].body, "1.2.3", "1.2.4")
			}
			if tc.name == "wrong archive content" {
				packed[1].body = "changed"
			}
			raw := packageArchive(t, packed)
			if tc.name == "malformed gzip" {
				raw = []byte("not gzip")
			}
			if err := os.WriteFile(archive, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if tc.name == "missing archive" {
				if err := os.Remove(archive); err != nil {
					t.Fatal(err)
				}
			}
			if tc.name == "oversized archive" {
				if err := os.Truncate(archive, (64<<20)+1); err != nil {
					t.Fatal(err)
				}
			}
			input := archive
			if tc.name == "directory archive" {
				input = filepath.Dir(archive)
			}
			args := []string{"-observe-pi-package", "-pi-tarball", input}
			if tc.name == "Pi input observation coexistence" {
				args = append(args, "-observe-pi-inputs")
			}
			r, stdout, stderr, code := invoke(tc.core, tc.pi, args...)
			if code != 2 || stderr != "" || r["outcome"] != "blocked" || r["schemaVersion"] != float64(1) || r["piPackage"] == nil {
				t.Fatalf("non-approving observation: %s %q %d", stdout, stderr, code)
			}
			if tc.name == "Pi input observation coexistence" {
				inputs, ok := r["piInputs"].(map[string]any)
				if !ok || inputs["name"] != "pi-input-projection" || inputs["state"] != "unavailable" {
					t.Fatalf("lost independent Pi input view without prerequisites: %v", r)
				}
			}
			check := r["check"].(map[string]any)
			p := r["piPackage"].(map[string]any)
			if check["state"] != "passed" || check["exitCode"] != float64(0) || p["state"] != tc.state || p["sourceCommit"] != tc.pi || p["exitCode"] != nil || p["verifierCommit"] != nil {
				t.Fatalf("wrong identity/state or invented process result: %v", r)
			}
			unreadable := tc.name == "missing archive" || tc.name == "oversized archive" || tc.name == "directory archive"
			if unreadable && p["tarballSha256"] != nil {
				t.Fatalf("invented complete archive digest: %v", p)
			}
			if !unreadable && p["tarballSha256"] != fmt.Sprintf("%x", sha256.Sum256(raw)) {
				t.Fatalf("digest is not the observed compressed archive: %v", p)
			}
			if tc.state == "passed" && (p["version"] != "1.2.3" || p["files"] != float64(10)) {
				t.Fatalf("wrong measured version/inventory: %v", p)
			}
			if tc.state != "passed" && p["files"] != nil {
				t.Fatalf("invented valid inventory: %v", p)
			}
		})
	}
	t.Run("false preserves legacy observation and conflict", func(t *testing.T) {
		a, ao, ae, ac := invoke(core, pi)
		b, bo, be, bc := invoke(core, pi, "-observe-pi-package=false")
		if ac != 2 || bc != 2 || ae != "" || be != "" || ao != bo || !reflect.DeepEqual(a, b) || a["piPackage"] != nil || len(a) != 5 {
			t.Fatalf("legacy changed: %q %q %q %q %d %d", ao, bo, ae, be, ac, bc)
		}
		r, out, diagnostic, code := invoke(core, pi, "-observe-pi-package=false", "-pi-tarball=", "-root", filepath.Join(repo, "absent"))
		if code != 1 || r != nil || out != "" || diagnostic != "-observe-contract cannot be combined with -assess or -pi-tarball\n" {
			t.Fatalf("legacy conflict changed: %q %q %d", out, diagnostic, code)
		}
	})
	for _, tc := range []struct {
		name, message string
		args          []string
	}{
		{"requires source mode", "-observe-pi-package requires -observe-contract\n", []string{"-observe-contract=false", "-observe-pi-package"}},
		{"requires archive", "-observe-pi-package requires a non-empty -pi-tarball\n", []string{"-observe-pi-package"}},
		{"rejects empty archive", "-observe-pi-package requires a non-empty -pi-tarball\n", []string{"-observe-pi-package", "-pi-tarball="}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, out, diagnostic, code := invoke(core, pi, append(tc.args, "-root", filepath.Join(repo, "absent"))...)
			if code != 1 || r != nil || out != "" || diagnostic != tc.message {
				t.Fatalf("invocation before IO: %q %q %d", out, diagnostic, code)
			}
		})
	}
}
