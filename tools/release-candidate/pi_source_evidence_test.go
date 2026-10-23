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

func TestPiSourceEvidence(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "candidate.exe")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v: %s", err, out)
	}
	repo, trace := t.TempDir(), filepath.Join(t.TempDir(), "capture.json")
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
	commit := func() string {
		t.Helper()
		git("add", ".")
		git("commit", "--quiet", "-m", "fixture source")
		return git("rev-parse", "HEAD")
	}
	git("init", "--quiet")
	git("config", "user.name", "Pi Fixture")
	git("config", "user.email", "fixture@example.invalid")
	git("config", "commit.gpgsign", "false")
	candidateWrite(t, repo, ".gitignore", "plugin/pi/node_modules/\n")
	candidateWrite(t, repo, "go.mod", "module example.invalid/pievidence\n\ngo 1.25.0\n")
	candidateWrite(t, repo, "docs/contracts/agent-contract-v1.json", "{\"status\":\"mcp-foundation\"}\n")
	candidateWrite(t, repo, "internal/setup/setup.go", "package setup\nconst piGentleEngramPackage = \"npm:gentle-engram@0.5.0\"\n")
	owner := `package main
import("encoding/json";"os")
func main(){
 if len(os.Args)==2 && os.Args[1]=="-check" { return }
 if len(os.Args)!=3 || os.Args[1]!="-verify-pi" { os.Exit(9) }
 raw,e:=os.ReadFile(os.Args[2]);if e!=nil{os.Exit(8)}
 if os.WriteFile(os.Getenv("PI_FIXTURE_CAPTURE"),raw,0600)!=nil{os.Exit(7)}
 var tools []struct{Name string;Parameters struct{Type string;Properties map[string]struct{Type string}}}
 if json.Unmarshal(raw,&tools)!=nil || len(tools)!=1 || tools[0].Name!="mem_fixture" || tools[0].Parameters.Type!="object" || tools[0].Parameters.Properties["query"].Type!="string"{os.Exit(6)}
 MUTATION
}`
	candidateWrite(t, repo, "tools/agent-contract/main.go", strings.ReplaceAll(owner, "MUTATION", ""))
	helper, err := os.ReadFile("../../plugin/pi/test/plugin-sandbox.mjs")
	if err != nil {
		t.Fatal(err)
	}
	candidateWrite(t, repo, "plugin/pi/test/plugin-sandbox.mjs", string(helper))
	for _, name := range []string{"compaction-recovery.js", "memory-tool-chrome.js", "private-redaction.js", "runtime-contract.js", "runtime-contract.json"} {
		candidateWrite(t, repo, "plugin/pi/"+name, "{}")
	}
	index := `import {Type} from 'typebox'; export default function(pi){pi.registerTool({name:'mem_fixture',parameters:Type.Object({query:Type.KIND()})});}`
	candidateWrite(t, repo, "plugin/pi/package.json", `{"name":"gentle-engram","version":"0.4.0"}`)
	candidateWrite(t, repo, "plugin/pi/index.ts", strings.ReplaceAll(index, "KIND", "Number"))
	core := commit()
	candidateWrite(t, repo, "plugin/pi/package.json", `{"name":"gentle-engram","version":"0.5.0"}`)
	candidateWrite(t, repo, "plugin/pi/index.ts", strings.ReplaceAll(index, "KIND", "String"))
	// A verifier from the selected Pi/current checkout must NOT substitute for Core.
	candidateWrite(t, repo, "tools/agent-contract/main.go", "package main\nimport \"os\"\nfunc main(){os.Exit(9)}\n")
	pi := commit()
	candidateWrite(t, repo, "plugin/pi/index.ts", strings.ReplaceAll(index, "KIND", "Number"))
	mismatch := commit()
	candidateWrite(t, repo, "plugin/pi/index.ts", strings.ReplaceAll(index, "KIND", "String"))
	candidateWrite(t, repo, "plugin/pi/test/plugin-sandbox.mjs", string(helper)+"\nimport {writeFileSync} from 'node:fs'; writeFileSync(new URL('../runtime-contract.json',import.meta.url),'changed');\n")
	mutatedPi := commit()
	candidateWrite(t, repo, "tools/agent-contract/main.go", strings.ReplaceAll(owner, "MUTATION", `if os.WriteFile("changed",nil,0600)!=nil{os.Exit(5)}`))
	mutatedCore := commit()
	candidateWrite(t, repo, "tools/agent-contract/main.go", strings.ReplaceAll(owner, "MUTATION", `if os.WriteFile(os.Args[2],[]byte("changed"),0600)!=nil{os.Exit(5)}`))
	mutatedCapture := commit()
	invoke := func(coreID, piID string, extra ...string) (map[string]any, string, int) {
		t.Helper()
		before := candidateSnapshot(t, repo)
		args := []string{"-root", repo, "-observe-contract", "-core-ref", "refs/tags/v3.0.0", "-pi-ref", "refs/tags/pi-v0.5.0", "-core-source", coreID, "-pi-source", piID}
		cmd := exec.Command(bin, append(args, extra...)...)
		cmd.Env = append(os.Environ(), "PI_FIXTURE_CAPTURE="+trace)
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
		after := candidateSnapshot(t, repo)
		if !reflect.DeepEqual(before, after) {
			// Report paths and hashes, never contents; retain the immutable snapshot guard.
			for path, data := range before {
				value, exists := after[path]
				if !exists || value != data {
					rel, _ := filepath.Rel(repo, path)
					t.Logf("SNAPSHOT_CHANGED path=%q before_sha256=%x after_sha256=%x present_after=%t before_bytes=%d after_bytes=%d", rel, sha256.Sum256([]byte(data)), sha256.Sum256([]byte(value)), exists, len(data), len(value))
				}
			}
			for path, data := range after {
				if _, exists := before[path]; !exists {
					rel, _ := filepath.Rel(repo, path)
					t.Logf("SNAPSHOT_ADDED path=%q sha256=%x bytes=%d", rel, sha256.Sum256([]byte(data)), len(data))
				}
			}
			t.Fatal("changed original sources, dependency cache or Git state")
		}
		var report map[string]any
		if out.Len() != 0 {
			if err := json.Unmarshal(out.Bytes(), &report); err != nil {
				t.Fatalf("not one JSON: %q", out.String())
			}
		}
		return report, diagnostic.String(), code
	}
	t.Run("old source mode stays opt-in free", func(t *testing.T) {
		r, stderr, code := invoke(core, pi)
		if code != 2 || stderr != "" || r["piInputs"] != nil || r["check"].(map[string]any)["state"] != "passed" || len(r) != 5 {
			t.Fatalf("legacy observation changed: %v %q %d", r, stderr, code)
		}
	})
	t.Run("missing fixed dependency cannot establish Pi proof", func(t *testing.T) {
		r, stderr, code := invoke(core, pi, "-observe-pi-inputs")
		if code != 2 || stderr != "" || r["outcome"] != "blocked" || r["piInputs"] == nil {
			t.Fatalf("missing dependency: %v %q %d", r, stderr, code)
		}
		p := r["piInputs"].(map[string]any)
		if p["state"] != "unavailable" || p["exitCode"] != nil || p["captureSha256"] != nil {
			t.Fatalf("invented prerequisite proof: %v", p)
		}
	})
	t.Run("new flag rejected before IO without source mode", func(t *testing.T) {
		r, stderr, code := invoke(core, pi, "-observe-pi-inputs", "-observe-contract=false", "-root", filepath.Join(repo, "absent"))
		if code != 1 || stderr != "-observe-pi-inputs requires -observe-contract\n" || r != nil {
			t.Fatalf("invocation: %v %q %d", r, stderr, code)
		}
	})
	t.Run("real selected Pi capture and Core verification", func(t *testing.T) {
		dependency := filepath.Clean("../../plugin/pi/node_modules/typebox")
		_, depErr := os.Stat(filepath.Join(dependency, "package.json"))
		_, nodeErr := exec.LookPath("node")
		if depErr != nil || nodeErr != nil {
			if os.Getenv("ENGRAM_PI_SCHEMA_REQUIRE_REAL") == "1" {
				t.Fatalf("mandatory real capture prerequisites absent: TypeBox=%v Node=%v", depErr, nodeErr)
			}
			t.Skip("real capture requires fixed local TypeBox1.3.34 and Node24; ENGRAM_PI_SCHEMA_REQUIRE_REAL=1 makes this mandatory")
		}
		if err := os.CopyFS(filepath.Join(repo, "plugin/pi/node_modules/typebox"), os.DirFS(dependency)); err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			name, core, pi, state string
			exit                  float64
		}{
			{"independent selected sources", core, pi, "passed", 0},
			{"selected Pi schema mismatch", core, mismatch, "not-established", 1},
			{"Pi source mutation", core, mutatedPi, "unavailable", 0},
			{"Core verifier mutation", mutatedCore, pi, "unavailable", 0},
			{"capture mutation", mutatedCapture, pi, "unavailable", 0},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Setenv("NODE_OPTIONS", "--not-a-real-node-option") // Ambient hooks must not replace selected-source execution.
				r, stderr, code := invoke(tc.core, tc.pi, "-observe-pi-inputs")
				if code != 2 || stderr != "" || r["outcome"] != "blocked" || r["schemaVersion"] != float64(1) || r["piInputs"] == nil {
					t.Fatalf("nonapproving observation: %v %q %d", r, stderr, code)
				}
				p := r["piInputs"].(map[string]any)
				raw, err := os.ReadFile(trace)
				if err != nil {
					t.Fatal(err)
				}
				if p["state"] != tc.state || p["sourceCommit"] != tc.pi || p["verifierCommit"] != tc.core || p["exitCode"] != tc.exit || p["captureExitCode"] != float64(0) || p["captureSha256"] != fmt.Sprintf("%x", sha256.Sum256(raw)) || p["typeBoxVersion"] != "1.3.34" || p["contractSha256"] != r["check"].(map[string]any)["artifactSha256"] {
					t.Fatalf("wrong selected-source evidence: %v", r)
				}
			})
		}
		t.Run("explicit false preserves old observation", func(t *testing.T) {
			a, se, code := invoke(core, pi)
			b, be, bc := invoke(core, pi, "-observe-pi-inputs=false")
			if code != 2 || bc != 2 || se != "" || be != "" || !reflect.DeepEqual(a, b) {
				t.Fatalf("false changed legacy: %v %v %q %q %d %d", a, b, se, be, code, bc)
			}
		})
	})
}
