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
	"sort"
	"strings"
	"testing"
)

func TestSourceEvidence(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "candidate.exe")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v: %s", err, out)
	}
	repo, marker := t.TempDir(), filepath.Join(t.TempDir(), "executed")
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_OPTIONAL_LOCKS=0")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("fixture git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "--quiet")
	git("config", "user.name", "Evidence Fixture")
	git("config", "user.email", "fixture@example.invalid")
	git("config", "commit.gpgsign", "false")
	// Fixture commits must not leave a detached maintenance worker mutating
	// .git while the public collector's preservation assertion takes snapshots.
	git("config", "maintenance.auto", "false")
	git("config", "gc.auto", "0")
	t.Run("fixture disables automatic maintenance", func(t *testing.T) {
		for key, want := range map[string]string{"maintenance.auto": "false", "gc.auto": "0"} {
			out, err := exec.Command("git", "-C", repo, "config", "--local", "--get", key).CombinedOutput()
			if err != nil || strings.TrimSpace(string(out)) != want {
				t.Fatalf("fixture must disable %s before commits: %v, output=%q", key, err, out)
			}
		}
	})
	candidateWrite(t, repo, ".gitattributes", "tools/agent-contract/main.go export-ignore\n")
	candidateWrite(t, repo, "docs/contracts/agent-contract-v1.json", "{\"status\":\"mcp-foundation\"}\n")
	candidateWrite(t, repo, "go.mod", "module example.invalid/evidencefixture\n\ngo 1.25.0\n")
	candidateWrite(t, repo, "plugin/pi/package.json", `{"name":"gentle-engram","version":"0.4.0"}`)
	candidateWrite(t, repo, "internal/setup/setup.go", "package setup\nconst piGentleEngramPackage = \"npm:gentle-engram@0.5.0\"\n")
	candidateWrite(t, repo, "tools/agent-contract/main.go", "package main\nimport \"os\"\nfunc main(){if os.WriteFile(os.Getenv(\"EVIDENCE_FIXTURE_MARKER\"),[]byte(\"selected-core\"),0600)!=nil{os.Exit(8)}}\n")
	git("add", ".")
	git("commit", "--quiet", "-m", "fixture core")
	core := git("rev-parse", "HEAD")
	candidateWrite(t, repo, "plugin/pi/package.json", `{"name":"gentle-engram","version":"0.5.0"}`)
	candidateWrite(t, repo, "tools/agent-contract/main.go", "package main\nimport \"os\"\nfunc main(){os.Exit(7)}\n")
	git("add", ".")
	git("commit", "--quiet", "-m", "fixture pi")
	pi := git("rev-parse", "HEAD")
	run := func(t *testing.T, coreSource string, extra ...string) (map[string]any, string, int) {
		t.Helper()
		before := candidateSnapshot(t, repo)
		args := []string{"-root", repo, "-observe-contract", "-core-ref", "refs/tags/v3.0.0", "-pi-ref", "refs/tags/pi-v0.5.0", "-core-source", coreSource, "-pi-source", pi}
		cmd := exec.Command(bin, append(args, extra...)...)
		cmd.Env = append(os.Environ(), "EVIDENCE_FIXTURE_MARKER="+marker)
		var out, diagnostic bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &diagnostic
		err := cmd.Run()
		code := 0
		if err != nil {
			if e, ok := err.(*exec.ExitError); ok {
				code = e.ExitCode()
			} else {
				t.Fatal(err)
			}
		}
		after := candidateSnapshot(t, repo)
		if !reflect.DeepEqual(before, after) {
			paths := make(map[string]bool)
			for path := range before {
				paths[path] = true
			}
			for path := range after {
				paths[path] = true
			}
			var changed []string
			for path := range paths {
				old, existed := before[path]
				current, exists := after[path]
				if existed != exists || old != current {
					changed = append(changed, path)
				}
			}
			sort.Strings(changed)
			var delta strings.Builder
			for _, path := range changed {
				rel, err := filepath.Rel(repo, path)
				if err != nil {
					t.Fatal("collector changed original Git state or sources: relative path unavailable")
				}
				fingerprint := func(snapshot map[string]string) string {
					if data, exists := snapshot[path]; exists {
						return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(data)))
					}
					return "absent"
				}
				fmt.Fprintf(&delta, "\n%s before=%s after=%s", filepath.ToSlash(rel), fingerprint(before), fingerprint(after))
			}
			t.Fatalf("collector changed original Git state or sources%s", delta.String())
		}
		var report map[string]any
		if out.Len() != 0 {
			if err := json.Unmarshal(out.Bytes(), &report); err != nil {
				t.Fatalf("not one JSON object: %q", out.String())
			}
		}
		return report, diagnostic.String(), code
	}
	t.Run("independent identities and real selected source execution", func(t *testing.T) {
		r, stderr, code := run(t, core)
		if code != 2 || stderr != "" || r["outcome"] != "blocked" || r["schemaVersion"] != float64(1) {
			t.Fatalf("want non-approving JSON/exit2/empty stderr; got %v %q %d", r, stderr, code)
		}
		if r["core"].(map[string]any)["commit"] != core || r["pi"].(map[string]any)["commit"] != pi {
			t.Fatalf("wrong verified identities: %v", r)
		}
		check := r["check"].(map[string]any)
		if check["state"] != "passed" || check["exitCode"] != float64(0) || check["sourceCommit"] != core {
			t.Fatalf("freshness observation not bound to selected core: %v", check)
		}
		if data, err := os.ReadFile(marker); err != nil || string(data) != "selected-core" {
			t.Fatalf("selected source was not executed: %q %v", data, err)
		}
	})
	t.Run("nonzero checker does not establish freshness", func(t *testing.T) {
		r, stderr, code := run(t, pi)
		if code != 2 || stderr != "" || r["check"].(map[string]any)["state"] != "not-established" || r["check"].(map[string]any)["exitCode"] != float64(1) {
			t.Fatalf("unestablished check: %v %q %d", r, stderr, code)
		}
	})
	t.Run("missing commit is not a verified identity", func(t *testing.T) {
		r, stderr, code := run(t, strings.Repeat("0", 40))
		if code != 2 || stderr != "" || r["core"].(map[string]any)["commit"] != nil || r["check"].(map[string]any)["state"] != "unavailable" {
			t.Fatalf("invented source proof: %v %q %d", r, stderr, code)
		}
	})
	for _, tc := range []struct {
		name, source, diagnostic string
		extra                    []string
	}{
		{"malformed source", "HEAD", "core-source and pi-source must be full lowercase commit IDs\n", nil},
		{"source flags without mode", core, "source commit flags require -observe-contract\n", []string{"-observe-contract=false"}},
		{"assessment conflict", core, "-observe-contract cannot be combined with -assess or -pi-tarball\n", []string{"-assess"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, stderr, code := run(t, tc.source, tc.extra...)
			if code != 1 || stderr != tc.diagnostic || r != nil {
				t.Fatalf("invocation: %v %q %d", r, stderr, code)
			}
		})
	}
	t.Run("local tag mismatch prevents execution", func(t *testing.T) {
		git("tag", "v3.0.0", pi)
		r, stderr, code := run(t, core)
		git("tag", "-d", "v3.0.0")
		if code != 2 || stderr != "" || r["check"].(map[string]any)["state"] != "unavailable" {
			t.Fatalf("mismatched ref produced evidence: %v %q %d", r, stderr, code)
		}
	})
	t.Run("replacements cannot redirect source", func(t *testing.T) {
		for _, pair := range []struct{ name, from, to string }{
			{"core commit", core, pi},
			{"core blob", git("rev-parse", core+":tools/agent-contract/main.go"), git("rev-parse", pi+":tools/agent-contract/main.go")},
			{"pi commit", pi, core},
		} {
			t.Run(pair.name, func(t *testing.T) {
				git("replace", pair.from, pair.to)
				defer git("replace", "-d", pair.from)
				r, stderr, code := run(t, core)
				if code != 2 || stderr != "" || r["core"].(map[string]any)["commit"] != core || r["pi"].(map[string]any)["commit"] != pi || r["check"].(map[string]any)["state"] != "passed" || r["check"].(map[string]any)["exitCode"] != float64(0) {
					t.Fatalf("replacement redirected immutable source: %v %q %d", r, stderr, code)
				}
			})
		}
	})
	t.Run("source mutation invalidates passing process", func(t *testing.T) {
		candidateWrite(t, repo, "tools/agent-contract/main.go", "package main\nimport \"os\"\nfunc main(){if os.WriteFile(\"changed\",nil,0600)!=nil{os.Exit(9)}}\n")
		git("add", ".")
		git("commit", "--quiet", "-m", "fixture mutation")
		r, stderr, code := run(t, git("rev-parse", "HEAD"))
		if code != 2 || stderr != "" || r["check"].(map[string]any)["state"] != "unavailable" {
			t.Fatalf("mutated source passed: %v %q %d", r, stderr, code)
		}
	})
	t.Run("link cannot execute source", func(t *testing.T) {
		candidateWrite(t, repo, "link-data", "../outside")
		blob := git("hash-object", "-w", "link-data")
		git("update-index", "--add", "--cacheinfo", "120000", blob, "unsafe-link")
		candidateWrite(t, repo, "tools/agent-contract/main.go", "package main\nimport \"os\"\nfunc main(){_ = os.WriteFile(os.Getenv(\"EVIDENCE_FIXTURE_MARKER\"),[]byte(\"unsafe\"),0600)}\n")
		git("add", "tools/agent-contract/main.go")
		git("commit", "--quiet", "-m", "fixture link")
		r, stderr, code := run(t, git("rev-parse", "HEAD"))
		data, err := os.ReadFile(marker)
		if code != 2 || stderr != "" || r["check"].(map[string]any)["state"] != "unavailable" || err != nil || string(data) != "selected-core" {
			t.Fatalf("unsafe source executed: %v %q %d %q %v", r, stderr, code, data, err)
		}
	})
	t.Run("conflict rejected before repository access", func(t *testing.T) {
		r, stderr, code := run(t, core, "-pi-tarball=", "-root", filepath.Join(repo, "absent"))
		if code != 1 || stderr != "-observe-contract cannot be combined with -assess or -pi-tarball\n" || r != nil {
			t.Fatalf("conflict result: %v %q %d", r, stderr, code)
		}
	})
}
