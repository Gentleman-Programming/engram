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
	"strconv"
	"strings"
	"testing"
)

func TestArtifactInputCLI(t *testing.T) { testArtifactInputCLI(t, false, false, false, false) }

func TestArtifactPiBootstrap(t *testing.T) { testArtifactInputCLI(t, true, false, false, false) }

func TestArtifactTopics(t *testing.T) { testArtifactInputCLI(t, true, true, false, false) }

func TestArtifactConsolidation(t *testing.T) { testArtifactInputCLI(t, true, true, true, false) }

func TestArtifactJudgments(t *testing.T) { testArtifactInputCLI(t, true, true, true, true) }

func testArtifactInputCLI(t *testing.T, bootstrap, topics, consolidation, judgments bool) {
	started := filepath.Join(t.TempDir(), "core-started")
	marker, err := json.Marshal(started)
	if err != nil {
		t.Fatal(err)
	}
	program := strings.Replace(artifactCoreFixture, "func main() {", "func main() {\n if os.WriteFile("+string(marker)+", []byte(\"started\"), 0600) != nil { os.Exit(25) }", 1)
	core, revision, repo := stagedProgramFixture(t, program) // Synthetic child; never production proof.
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
	topicMarker := filepath.Join(repo, "topic-mutation-observed")
	var packed bytes.Buffer
	gz := gzip.NewWriter(&packed)
	tw := tar.NewWriter(gz)
	for _, name := range packageRuntimeFiles {
		body := []byte("throw new Error('archive must not execute');\n")
		if bootstrap && name == "index.ts" {
			body = []byte(artifactPiFixture)
			origin := filepath.Join(repo, "plugin", "pi", "node_modules", "typebox", "index.js")
			body = append(body, []byte("\nif (cwd.includes('-139-')) writeFileSync("+string(encode(origin))+", '// mutated original peer');\n")...)
		}
		if topics && name == "index.ts" {
			fixture := artifactTopicFixture
			if consolidation {
				extra := artifactConsolidationFixture
				if judgments {
					extra += artifactJudgmentFixture
				}
				fixture = strings.Replace(fixture, "  host.registerTool(", extra+"\n  host.registerTool(", 1)
			}
			body = []byte(strings.ReplaceAll(fixture, `"$TOPIC_MARKER$"`, string(encode(topicMarker))))
		}
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
		cmd.Env = append(os.Environ(), "ENGRAM_TOKEN=inherited-not-private", "ENGRAM_URL=http://example.invalid", "ENGRAM_PORT=7", "ENGRAM_SOCKET=forbidden", "ENGRAM_HTTP_TOKEN=inherited", "ENGRAM_CLOUD_AUTOSYNC=1", "ENGRAM_CLOUD_TOKEN=inherited", "ENGRAM_CLOUD_SERVER=http://example.invalid", "OPENAI_API_KEY=inherited", "HTTP_PROXY=http://example.invalid", "NODE_OPTIONS=forbidden-inherited-option")
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
	runtimeModes := map[string]string{"core healthy": "123", "core identity": "124", "core protocol": "125", "core shutdown": "126", "core startup": "127", "core auth": "128", "core PID": "129", "core nonloopback": "130"}
	cases := []struct{ name, state string }{
		{"matching", "passed"}, {"missing context", "unavailable"}, {"malformed context", "unavailable"},
		{"context mismatch", "unavailable"}, {"digest mismatch", "unavailable"}, {"Pi source mismatch", "not-established"},
		{"context boundary", "passed"}, {"context oversized", "unavailable"},
		{"core healthy", "passed"}, {"core identity", "passed"}, {"core protocol", "passed"}, {"core shutdown", "passed"},
		{"core startup", "passed"}, {"core auth", "passed"}, {"core PID", "passed"}, {"core nonloopback", "passed"},
	}
	if bootstrap {
		cases = []struct{ name, state string }{{"pi bootstrap healthy", "passed"}, {"pi bootstrap import failure", "passed"}, {"pi bootstrap mutation", "unavailable"}, {"pi bootstrap stalled", "passed"}, {"pi bootstrap ancestor dependency", "passed"}, {"pi bootstrap peer healthy", "passed"}, {"pi bootstrap peer version", "passed"}, {"pi bootstrap peer mutation", "unavailable"}, {"pi bootstrap peer origin mutation", "unavailable"}}
		for i, tc := range cases {
			runtimeModes[tc.name] = strconv.Itoa(131 + i)
		}
	}
	if topics {
		cases = []struct{ name, state string }{{"topic vectors", "passed"}, {"topic rejects mutation", "passed"}}
		runtimeModes["topic vectors"], runtimeModes["topic rejects mutation"] = "140", "141"
		for i, helper := range []string{"workflow", "Pi", "Core"} {
			for j, operation := range []string{"mutation", "removal"} {
				name := helper + " helper " + operation
				cases = append(cases, struct{ name, state string }{name, "unavailable"})
				runtimeModes[name] = strconv.Itoa(142 + i*2 + j)
			}
		}
	}
	if consolidation {
		cases = []struct{ name, state string }{{"consolidation plan", "passed"}, {"consolidation rejects preview mutation", "passed"}}
		runtimeModes["consolidation plan"], runtimeModes["consolidation rejects preview mutation"] = "148", "149"
	}
	if judgments {
		cases = []struct{ name, state string }{{"judgment plans", "passed"}, {"batch rejects item order", "passed"}, {"judgment rejects lost persistence", "passed"}}
		for i, tc := range cases {
			runtimeModes[tc.name] = strconv.Itoa(150 + i)
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			helperMutation := topics && strings.Contains(tc.name, " helper ")
			var peerOrigin string
			var peerOriginal []byte
			if strings.HasPrefix(tc.name, "pi bootstrap peer") {
				version := "1.3.34"
				if tc.name == "pi bootstrap peer version" {
					version = "2.0.0"
				}
				peer := filepath.Join(repo, "plugin", "pi", "node_modules", "typebox")
				write(filepath.Join(peer, "package.json"), encode(map[string]string{"name": "typebox", "version": version, "type": "module", "exports": "./index.js"}))
				// Synthetic cache only; never genuine TypeBox qualification.
				write(filepath.Join(peer, "index.js"), []byte(`import { writeFileSync } from 'node:fs';
if (process.cwd().includes('-138-')) writeFileSync(new URL('./index.js', import.meta.url), '// mutated peer');
export const syntheticPeer = true;
`))
				if tc.name == "pi bootstrap peer origin mutation" {
					peerOrigin = filepath.Join(peer, "index.js")
					var err error
					peerOriginal, err = os.ReadFile(peerOrigin)
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			var ancestorMarker string
			var ancestorJoined bool
			if tc.name == "pi bootstrap ancestor dependency" {
				ancestorMarker = artifactPiAncestorFixture(t, &ancestorJoined)
			}
			expected, pi := want, piRevision
			payload, mode := archive.Bytes(), "-observe-artifact-inputs"
			if run, active := runtimeModes[tc.name]; active {
				expected.RunID, mode = run, "-observe-artifact-workflows"
				payload = runtimeFixtureZIP(t, payload, run)
				expected.ZIPHash = stagedDigest(payload)
			}
			write(bundle, payload)
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
			args := []string{"-root", repo, "-core-ref", "refs/tags/v1.2.3", "-pi-ref", "refs/tags/pi-v4.5.6", "-core-source", revision, "-pi-source", pi, mode, "-staging-bundle", bundle, "-staging-context", contextPath}
			out, diagnostic, code := invoke(args)
			if code != 2 || diagnostic != "" {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, diagnostic)
			}
			var result map[string]any
			if err := json.Unmarshal([]byte(out), &result); err != nil || strings.Count(out, "\n") != 1 {
				t.Fatalf("not one JSON line: %q", out)
			}
			if peerOrigin != "" {
				if result["coreRuntime"].(map[string]any)["joined"] != true || result["piBootstrap"].(map[string]any)["joined"] != true {
					t.Fatalf("unknown joins; retain original-cache mutation fixture: %s", out)
				}
				changed, err := os.ReadFile(peerOrigin)
				if err != nil || string(changed) != "// mutated original peer" {
					t.Fatalf("original-cache mutation did not execute: %q %v", changed, err)
				}
				// Restore only this known test-owned input after observing mutation and joins.
				if err := os.WriteFile(peerOrigin, peerOriginal, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if topics {
				inventory, ok := result["workflows"].(map[string]any)["cases"].([]any)
				if !ok || len(inventory) != 5 {
					t.Fatalf("missing original workflow inventory: %s", out)
				}
			}
			if helperMutation {
				first := result["workflows"].(map[string]any)["cases"].([]any)[0].(map[string]any)
				for _, key := range []string{"coreRuntime", "piRuntime"} {
					child := first[key].(map[string]any)
					if child["joined"] != true || child["exitCode"] != float64(0) || child["cleanup"] != "passed" {
						t.Fatalf("helper mutation prerequisite did not complete cleanly: %s", out)
					}
				}
				marker, err := os.ReadFile(topicMarker)
				if err != nil || string(marker) != tc.name {
					t.Fatal("actual owned helper mutation not observed")
				}
				if err := os.Remove(topicMarker); err != nil {
					t.Fatal(err)
				}
				t.Log("confirmed owned helper change/removal and both exit0 joins:", tc.name)
			}
			if result["outcome"] != "blocked" || result["schemaVersion"] != float64(1) || result["check"].(map[string]any)["state"] != tc.state || result["workflows"].(map[string]any)["state"] != "unavailable" {
				t.Fatalf("unexpected report: %s", out)
			}
			if bootstrap {
				observed, ok := result["piBootstrap"].(map[string]any)
				state, cleanup := "unavailable", "passed"
				if topics || tc.name == "pi bootstrap healthy" || tc.name == "pi bootstrap peer healthy" {
					state = "passed"
				}
				if helperMutation {
					state = "unavailable"
					if result["piPackage"].(map[string]any)["state"] != "unavailable" {
						t.Fatal("helper mutation left package evidence passing")
					}
				}
				if tc.name == "pi bootstrap peer version" {
					cleanup = "not-started"
				}
				if tc.name == "pi bootstrap stalled" {
					cleanup = "killed"
				}
				if tc.name == "pi bootstrap import failure" || ancestorMarker != "" {
					cleanup = "failed"
				}
				if ancestorMarker != "" && ok {
					ancestorJoined = observed["joined"] == true && result["coreRuntime"].(map[string]any)["joined"] == true
					if _, err := os.Stat(ancestorMarker); !os.IsNotExist(err) {
						t.Errorf("undeclared ancestor package executed: %v", err)
					}
				}
				if !ok || observed["name"] != "pi-module-bootstrap-only" || observed["state"] != state || observed["cleanup"] != cleanup || observed["joined"] != true {
					t.Fatalf("missing or incorrect bootstrap-only observation: %s", out)
				}
				if tc.name == "pi bootstrap peer healthy" && (observed["typeBoxVersion"] != "1.3.34" || len(observed["typeBoxTreeSha256"].(string)) != 64 || !reflect.DeepEqual(observed["substitutions"], []any{"@earendil-works/pi-tui.Text"})) {
					t.Fatalf("missing measured fixed-peer/substitution evidence: %s", out)
				}
				if cleanup == "failed" && observed["exitCode"] != float64(1) {
					t.Fatalf("missing actual failed child exit: %s", out)
				}
				if strings.Contains(out, "synthetic-private-diagnostic") || strings.Contains(out, "inherited") {
					t.Fatal("child output leaked")
				}
			}
			if topics {
				cases, ok := result["workflows"].(map[string]any)["cases"].([]any)
				if !ok || len(cases) != 5 {
					t.Fatalf("missing original workflow inventory: %s", out)
				}
				first := cases[0].(map[string]any)
				state := "passed"
				if helperMutation || tc.name == "topic rejects mutation" {
					state = "unavailable"
				}
				if first["name"] != "real Pi topic vectors match Go without registering sessions or creating observations" || first["state"] != state || first["coreRuntime"].(map[string]any)["joined"] != true || first["piRuntime"].(map[string]any)["joined"] != true {
					t.Fatalf("incorrect topic workflow observation: %s", out)
				}
				core, pi := first["coreRuntime"].(map[string]any), first["piRuntime"].(map[string]any)
				if helperMutation && (core["state"] != "unavailable" || pi["state"] != "unavailable") {
					t.Fatalf("helper mutation left runtime evidence passing: %s", out)
				}
				piExit, piCleanup := float64(0), "passed"
				if tc.name == "topic rejects mutation" {
					piExit, piCleanup = 1, "failed"
					marker, err := os.ReadFile(topicMarker)
					if err != nil || string(marker) != "mutation accepted" {
						t.Fatal("semantic mutation prerequisite not observed")
					}
					if err := os.Remove(topicMarker); err != nil {
						t.Fatal(err)
					}
				} else if _, err := os.Stat(topicMarker); !os.IsNotExist(err) && !consolidation {
					t.Fatal("positive topic case mutated the fixture")
				}
				if core["exitCode"] != float64(0) || core["cleanup"] != "passed" || pi["exitCode"] != piExit || pi["cleanup"] != piCleanup {
					t.Fatalf("wrong case termination: %s", out)
				}
				pendingCases := cases[1:]
				if consolidation {
					second := cases[1].(map[string]any)
					state, exit, cleanup := "passed", float64(0), "passed"
					if tc.name == "consolidation rejects preview mutation" {
						state, exit, cleanup = "unavailable", 1, "failed"
					}
					if second["name"] != "real Pi consolidation previews without mutation and confirms the exact Go plan" || second["state"] != state || second["coreRuntime"] == nil || second["piRuntime"] == nil {
						t.Fatalf("missing consolidation execution: %s", out)
					}
					core, pi := second["coreRuntime"].(map[string]any), second["piRuntime"].(map[string]any)
					if core["joined"] != true || core["exitCode"] != float64(0) || core["cleanup"] != "passed" || pi["joined"] != true || pi["exitCode"] != exit || pi["cleanup"] != cleanup {
						t.Fatalf("incorrect consolidation termination: %s", out)
					}
					if exit == 1 {
						marker, err := os.ReadFile(topicMarker)
						if err != nil || string(marker) != "consolidation preview mutation" {
							t.Fatal("preview mutation prerequisite not observed")
						}
						if err := os.Remove(topicMarker); err != nil {
							t.Fatal(err)
						}
					} else if _, err := os.Stat(topicMarker); !os.IsNotExist(err) && !judgments {
						t.Fatal("positive consolidation mutated preview")
					}
					pendingCases = cases[2:]
				}
				if judgments {
					for i, phase := range []string{"batch-errors", "judgments"} {
						index := i + 2
						observed := cases[index].(map[string]any)
						state, exit, cleanup, markerText := "passed", float64(0), "passed", ""
						if i == 0 && tc.name == "batch rejects item order" {
							state, exit, cleanup, markerText = "unavailable", 1, "failed", "batch item order"
						}
						if i == 1 && tc.name == "judgment rejects lost persistence" {
							state, exit, cleanup, markerText = "unavailable", 1, "failed", "persisted judgment changed"
						}
						if observed["state"] != state || observed["coreRuntime"] == nil || observed["piRuntime"] == nil {
							t.Fatalf("missing %s execution: %s", phase, out)
						}
						core, pi := observed["coreRuntime"].(map[string]any), observed["piRuntime"].(map[string]any)
						if core["state"] != "passed" || core["joined"] != true || core["exitCode"] != float64(0) || core["cleanup"] != "passed" || pi["state"] != state || pi["joined"] != true || pi["exitCode"] != exit || pi["cleanup"] != cleanup {
							t.Fatalf("wrong %s termination: %s", phase, out)
						}
						pendingCases = cases[index+1:]
						if exit == 1 {
							marker, err := os.ReadFile(topicMarker)
							if err != nil || string(marker) != markerText {
								t.Fatalf("actual %s prerequisite not observed", phase)
							}
							if err := os.Remove(topicMarker); err != nil {
								t.Fatal(err)
							}
							t.Log("confirmed owned semantic marker and observed Core0/Pi1 joins:", markerText)
							break
						}
					}
					if _, err := os.Stat(topicMarker); !os.IsNotExist(err) {
						t.Fatal("unexpected judgment mutation marker")
					}
					for _, pending := range pendingCases {
						if value := pending.(map[string]any); value["coreRuntime"] != nil || value["piRuntime"] != nil {
							t.Fatal("continued after unavailable phase or executed finalization")
						}
					}
				}
				for _, pending := range pendingCases {
					if pending.(map[string]any)["state"] != "unavailable" {
						t.Fatal("unimplemented case reported passing")
					}
				}
			}
			if tc.state == "passed" {
				measured := result["artifactInputs"].(map[string]any)
				pkg := result["piPackage"].(map[string]any)
				if measured["zipSha256"] != expected.ZIPHash || measured["coreSha256"] != stagedDigest(core) || measured["piSha256"] != stagedDigest(packed.Bytes()) || measured["platform"] != runtime.GOOS+"/"+runtime.GOARCH || pkg["files"] != float64(len(packageRuntimeFiles)) || pkg["state"] != "passed" || result["core"].(map[string]any)["commit"] != revision || result["pi"].(map[string]any)["commit"] != piRevision {
					t.Fatalf("wrong measured evidence: %s", out)
				}
			}
			if tc.name == "matching" {
				x, y, z := invoke(append(args, "-observe-artifact-workflows=false"))
				if x != out || y != diagnostic || z != code {
					t.Fatal("false execution flag changed admission")
				}
			}
			if _, active := runtimeModes[tc.name]; !active && result["coreRuntime"] != nil {
				t.Fatal("read-only mode executed Core")
			}
			if _, active := runtimeModes[tc.name]; active {
				observed := result["coreRuntime"].(map[string]any)
				state, cleanup := "unavailable", "passed"
				if tc.name == "core healthy" || bootstrap {
					state = "passed"
				}
				if tc.name == "pi bootstrap peer version" {
					state, cleanup = "unavailable", "not-started"
				}
				if tc.name == "core shutdown" {
					cleanup = "killed"
				}
				if observed["state"] != state || observed["cleanup"] != cleanup || observed["joined"] != true {
					t.Fatalf("wrong runtime: %s", out)
				}
				if state == "passed" && (observed["reportedVersion"] != "fixture-observed" || observed["instanceId"] != "synthetic-owned-instance") {
					t.Fatalf("invented runtime identity/version: %s", out)
				}
				if _, leaked := observed["token"]; leaked || strings.Contains(out, "inherited-not-private") {
					t.Fatal("private transport leaked")
				}
			}
			after, err := sourceTreeDigest(repo)
			b, readErr := os.ReadFile(bundle)
			c, ctxErr := os.ReadFile(contextFile)
			if err != nil || before != after || readErr != nil || !bytes.Equal(b, payload) || ctxErr != nil || !bytes.Equal(c, data) {
				t.Fatal("changed source/Git/input or left owned workspace")
			}
		})
	}
	if bootstrap {
		return
	}
	for _, target := range []string{"bundle", "context"} {
		t.Run("changed "+target+" before execution", func(t *testing.T) {
			write(bundle, archive.Bytes())
			write(contextFile, encode(want))
			if err := os.Remove(started); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			filename := bundle
			if target == "context" {
				filename = contextFile
			}
			realGit, err := exec.LookPath("git")
			if err != nil {
				t.Fatal(err)
			}
			wrapper, mutated := artifactMutationGit(t)
			t.Setenv("PATH", wrapper+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("ARTIFACT_TEST_REAL_GIT", realGit)
			t.Setenv("ARTIFACT_TEST_MUTATE", filename)
			t.Setenv("ARTIFACT_TEST_MUTATED", mutated)
			before, err := sourceTreeDigest(repo)
			if err != nil {
				t.Fatal(err)
			}
			out, diagnostic, code := invoke([]string{"-root", repo, "-core-ref", "refs/tags/v1.2.3", "-pi-ref", "refs/tags/pi-v4.5.6", "-core-source", revision, "-pi-source", piRevision, "-observe-artifact-workflows", "-staging-bundle", bundle, "-staging-context", contextFile})
			var result artifactInputObservation
			if err := json.Unmarshal([]byte(out), &result); err != nil || code != 2 || diagnostic != "" || strings.Count(out, "\n") != 1 {
				t.Fatalf("invalid public report: code=%d stdout=%q stderr=%q err=%v", code, out, diagnostic, err)
			}
			if proof, err := os.ReadFile(mutated); err != nil || string(proof) != "mutated" {
				t.Fatalf("mutation did not occur after admission: %q %v", proof, err)
			}
			if _, err := os.Stat(started); !os.IsNotExist(err) {
				t.Fatalf("Core executed after original %s changed: %v", target, err)
			}
			if result.Outcome != "blocked" || result.Check.State != "unavailable" || result.PiPackage == nil || result.PiPackage.State != "unavailable" || result.PiPackage.Files != 0 || result.Workflows.State != "unavailable" || result.CoreRuntime == nil || result.CoreRuntime.State != "unavailable" || result.CoreRuntime.Cleanup != "not-started" || !result.CoreRuntime.Joined {
				t.Fatalf("changed input must prevent execution: %s", out)
			}
			after, digestErr := sourceTreeDigest(repo)
			current, readErr := os.ReadFile(filename)
			other, expected := contextFile, encode(want)
			if target == "context" {
				other, expected = bundle, archive.Bytes()
			}
			unchanged, otherErr := os.ReadFile(other)
			if digestErr != nil || before != after || readErr != nil || string(current) != "tampered" || otherErr != nil || !bytes.Equal(unchanged, expected) {
				t.Fatal("collector changed source/Git or rewrote original inputs")
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
			x, y, z := invoke(append(command, "-observe-artifact-inputs=false", "-observe-artifact-workflows=false"))
			if c != mode.code || !reflect.DeepEqual([]any{a, b, c}, []any{x, y, z}) {
				t.Fatalf("legacy %v changed: exits %d/%d, stdout equal %v, stderr equal %v", mode.flags, c, z, a == x, b == y)
			}
		}
	})
}
