package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestPublishedPairRejectsBeforeExecution(t *testing.T) {
	root := t.TempDir()
	pins := filepath.Join(root, "context.json")
	for _, body := range []string{`{}`, `{"core":{},"pi":{},"unknown":true}`, `{} {}`} {
		if err := os.WriteFile(pins, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		err := run([]string{"-observe-published-pair", "-archive", "missing.zip", "-checksums", "missing.txt", "-pi-tarball", "missing.tgz", "-context", pins, "-execute"}, &out)
		var exit *assessmentExit
		if !errors.As(err, &exit) || exit.code != 2 {
			t.Fatalf("expected nonapproving observation, got %v", err)
		}
		var result map[string]any
		if json.Unmarshal(out.Bytes(), &result) != nil || result["outcome"] != "blocked" || result["admission"] != "unavailable" || result["runtime"] != nil {
			t.Fatalf("invalid pins reached execution: %s", out.Bytes())
		}
	}
}

// Authored adapter fixture: it forwards tools to the authored Core protocol
// fixture, and is never evidence that distributed Pi bytes work.
const publishedPairPiFixture = `export default function(host) {
  for (const name of ['mem_save', 'mem_get_observation']) host.registerTool({name,
    async execute(_id, params) {
      const save = name === 'mem_save';
      const response = await fetch(new URL(save ? '/observations' : '/observations/' + params.id, process.env.ENGRAM_URL), {
        method: save ? 'POST' : 'GET', headers: {Authorization: 'Bearer ' + process.env.ENGRAM_TOKEN, 'Content-Type': 'application/json'},
        body: save ? JSON.stringify(params) : undefined });
      return {isError: !response.ok, details: {data: await response.json()}};
    }
  });
}
`

func TestPublishedPairPersistence(t *testing.T) {
	requireArtifactNode(t)
	program := strings.Replace(publishedPersistenceProgram(), `w.WriteHeader(201); _,_=w.Write([]byte("{\"id\":1}")); return`, `
   w.Header().Set("Content-Length", "8")
   w.WriteHeader(201); _,_=w.Write([]byte("{\"id\":1}"))
   if strings.Contains(cwd,"-coreexit-") { w.(http.Flusher).Flush(); os.Exit(0) }
   return`, 1)
	core, _, _ := stagedProgramFixture(t, program)
	for _, mode := range []string{"persisted", "lost", "changed", "writefail", "coreexit", "pi-exit", "pi-mutated", "pi-drain"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			materialize := func(dest string) bool {
				if os.MkdirAll(dest, 0700) != nil {
					return false
				}
				body := publishedPairPiFixture
				if mode == "pi-exit" {
					body += "\nprocess.exit(0);\n"
				}
				if mode == "pi-mutated" {
					body += "\nimport {writeFileSync} from 'node:fs'; writeFileSync(new URL(import.meta.url), '// changed');\n"
				}
				if mode == "pi-drain" {
					body += "\nsetInterval(() => {}, 1000);\n"
				}
				return os.WriteFile(filepath.Join(dest, "index.ts"), []byte(body), 0600) == nil
			}
			pi := &artifactPiCheck{State: "unavailable", Joined: true, Cleanup: "not-started"}
			got := runArtifactObservation(ctx, core, stagedDigest(core), mode, materialize, pi, "", "fixture-observed")
			if !got.Joined || got.Cleanup != "passed" || got.Retained || !pi.Joined || pi.Retained {
				t.Fatalf("cleanup not established: core=%+v pi=%+v", got, pi)
			}
			if mode == "persisted" {
				if got.State != "passed" || got.Persistence != "passed" || pi.State != "passed" || len(pi.Stages) != 2 {
					t.Fatalf("round trip failed: core=%+v pi=%+v", got, pi)
				}
				for _, stage := range pi.Stages {
					if stage.State != "passed" || stage.Cleanup != "passed" || !stage.Joined {
						t.Fatalf("stage: %+v", stage)
					}
				}
			} else if mode == "coreexit" {
				if got.State != "unavailable" || pi.State != "unavailable" || pi.Cleanup != "passed" || len(pi.Stages) != 1 {
					t.Fatalf("Core exit was not reported: core=%+v pi=%+v", got, pi)
				}
				stage := pi.Stages[0]
				if stage.Name != "persistence-save" || stage.State != "unavailable" || stage.Cleanup != "passed" || !stage.Joined {
					t.Fatalf("Core exit stage: %+v", stage)
				}
			} else if pi.State == "passed" || (mode != "pi-mutated" && got.State == "passed") {
				t.Fatalf("failure became passing: core=%+v pi=%+v", got, pi)
			}
		})
	}
}

func TestPublishedPairAdmission(t *testing.T) {
	control := filepath.Join(t.TempDir(), "mutation-target")
	program := strings.ReplaceAll(publishedPersistenceProgram(), "fixture-observed", "1.2.3")
	encoded, _ := json.Marshal(control)
	program = strings.Replace(program, `token:=os.Getenv("ENGRAM_TOKEN")`, `
 if b,e:=os.ReadFile(`+string(encoded)+`); e==nil {
  target:=string(b)
  if strings.HasPrefix(target,"owned:") { target=cwd+string(os.PathSeparator)+strings.TrimPrefix(target,"owned:") }
  original,_:=os.ReadFile(target); _=os.WriteFile(target,append(original,' '),0600)
 }
 token:=os.Getenv("ENGRAM_TOKEN")`, 1)
	core, revision, repo := stagedProgramFixture(t, program)
	write := func(path string, body []byte) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	var packed bytes.Buffer
	gz := gzip.NewWriter(&packed)
	tw := tar.NewWriter(gz)
	for _, name := range packageRuntimeFiles {
		body := []byte("// synthetic unused member\n")
		if name == "index.ts" {
			body = []byte(publishedPairPiFixture)
		}
		if name == "package.json" {
			body, _ = json.Marshal(map[string]any{"name": "gentle-engram", "version": "4.5.6", "files": packageRuntimeFiles})
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
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("add", "plugin")
	git("-c", "user.name=Pair Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "-c", "core.hooksPath="+filepath.Join(repo, "no-hooks"), "commit", "--quiet", "-m", "Pi fixture")
	piRevision := git("rev-parse", "HEAD")
	var payload bytes.Buffer
	z := zip.NewWriter(&payload)
	for name, data := range map[string][]byte{"engram.exe": core, "tools/cloud-sync-projects.sh": []byte("synthetic"), "tools/cloud-sync-projects.ps1": []byte("synthetic")} {
		w, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	archive, sums, tarball, pinsFile := filepath.Join(root, "core.zip"), filepath.Join(root, "checksums.txt"), filepath.Join(root, "pi.tgz"), filepath.Join(root, "context.json")
	checksum := []byte(stagedDigest(payload.Bytes()) + "  engram_1.2.3_windows_amd64.zip\n")
	write(archive, payload.Bytes())
	write(sums, checksum)
	write(tarball, packed.Bytes())
	for _, mode := range []string{"valid", "pi-hash", "pi-source", "pi-version", "core-hash", "missing-peer"} {
		t.Run(mode, func(t *testing.T) {
			pins := publishedPairContext{Core: publishedCoreContext{Version: "1.2.3", Source: revision, ArchiveSHA256: stagedDigest(payload.Bytes()), ChecksumsSHA256: stagedDigest(checksum)}}
			pins.Pi.Version = "4.5.6"
			pins.Pi.Source = piRevision
			pins.Pi.TarballSHA256 = stagedDigest(packed.Bytes())
			switch mode {
			case "pi-hash":
				pins.Pi.TarballSHA256 = strings.Repeat("0", 64)
			case "pi-source":
				pins.Pi.Source = revision
			case "pi-version":
				pins.Pi.Version = "4.5.7"
			case "core-hash":
				pins.Core.ArchiveSHA256 = strings.Repeat("0", 64)
			}
			raw, _ := json.Marshal(pins)
			write(pinsFile, raw)
			args := []string{"-archive", archive, "-checksums", sums, "-pi-tarball", tarball, "-context", pinsFile, "-root", repo}
			if mode == "missing-peer" {
				args = append(args, "-execute")
			}
			var out bytes.Buffer
			if runtime.GOOS != "windows" || runtime.GOARCH != "amd64" {
				// Admission is portable; the public command is intentionally host-gated.
				result := publishedPairObservation{Identity: pins, Admission: "unavailable"}
				observePublishedPair(context.Background(), repo, archive, sums, tarball, pinsFile, raw, mode == "missing-peer", &result)
				if (result.Admission == "passed") != (mode == "valid") || result.Runtime != nil {
					t.Fatalf("portable admission: %+v", result)
				}
				return
			}
			err := runPublishedPair(args, &out)
			var exit *assessmentExit
			if !errors.As(err, &exit) || exit.code != 2 {
				t.Fatalf("exit: %v", err)
			}
			var result publishedPairObservation
			if json.Unmarshal(out.Bytes(), &result) != nil {
				t.Fatal(out.String())
			}
			want := "unavailable"
			if mode == "valid" {
				want = "passed"
			}
			if result.Admission != want || result.Runtime != nil || result.PiRuntime != nil || result.Outcome != "blocked" {
				t.Fatal(out.String())
			}
		})
	}
	if runtime.GOOS != "windows" || runtime.GOARCH != "amd64" {
		return
	}
	requireArtifactNode(t)
	peer := filepath.Join(repo, "plugin", "pi", "node_modules", "typebox")
	for _, mode := range []string{"control", "context", "archive", "checksums", "tarball", "peer", "pi-copy", "helper-copy"} {
		t.Run("runtime-"+mode, func(t *testing.T) {
			write(archive, payload.Bytes())
			write(sums, checksum)
			write(tarball, packed.Bytes())
			write(filepath.Join(peer, "package.json"), []byte(`{"name":"typebox","version":"1.3.34","type":"module","exports":"./index.js"}`))
			write(filepath.Join(peer, "index.js"), []byte("export const syntheticPeer = true;\n"))
			pins := publishedPairContext{Core: publishedCoreContext{Version: "1.2.3", Source: revision, ArchiveSHA256: stagedDigest(payload.Bytes()), ChecksumsSHA256: stagedDigest(checksum)}}
			pins.Pi.Version = "4.5.6"
			pins.Pi.Source = piRevision
			pins.Pi.TarballSHA256 = stagedDigest(packed.Bytes())
			raw, _ := json.Marshal(pins)
			write(pinsFile, raw)
			target := map[string]string{"context": pinsFile, "archive": archive, "checksums": sums, "tarball": tarball, "peer": filepath.Join(peer, "index.js"), "pi-copy": "owned:materialized-pi/index.ts", "helper-copy": "owned:pi-workflows.mjs"}[mode]
			if target != "" {
				write(control, []byte(target))
			} else if err := os.Remove(control); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			var out bytes.Buffer
			err := runPublishedPair([]string{"-archive", archive, "-checksums", sums, "-pi-tarball", tarball, "-context", pinsFile, "-root", repo, "-execute"}, &out)
			var exit *assessmentExit
			if !errors.As(err, &exit) || exit.code != 2 {
				t.Fatalf("exit: %v", err)
			}
			var result publishedPairObservation
			if json.Unmarshal(out.Bytes(), &result) != nil {
				t.Fatal(out.String())
			}
			if result.Runtime == nil || result.PiRuntime == nil || !result.Runtime.Joined || result.Runtime.Cleanup != "passed" || !result.PiRuntime.Joined || result.Outcome != "blocked" {
				t.Fatal(out.String())
			}
			if mode == "control" {
				if result.Admission != "passed" || result.Runtime.State != "passed" || result.PiRuntime.State != "passed" {
					t.Fatal(out.String())
				}
			} else if result.Admission != "unavailable" || result.Package.State != "unavailable" || result.Runtime.State != "unavailable" || result.PiRuntime.State != "unavailable" {
				t.Fatal(out.String())
			}
			if mode != "control" {
				if result.Runtime.Persistence == "passed" {
					t.Fatal("invalidated persistence still passes")
				}
				for _, stage := range result.PiRuntime.Stages {
					if stage.State == "passed" {
						t.Fatalf("invalidated stage still passes: %+v", stage)
					}
				}
			}
		})
	}
}
