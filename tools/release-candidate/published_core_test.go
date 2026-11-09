package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestPublishedCoreAdmission(t *testing.T) {
	core, revision, _ := stagedMetadataFixture(t)
	root := t.TempDir()
	archive, sums, pinsFile := filepath.Join(root, "core.zip"), filepath.Join(root, "checksums.txt"), filepath.Join(root, "context.json")
	makeZIP := func(extra string) []byte {
		var b bytes.Buffer
		z := zip.NewWriter(&b)
		for _, name := range []string{"engram.exe", "tools/cloud-sync-projects.sh", "tools/cloud-sync-projects.ps1", extra} {
			if name == "" {
				continue
			}
			w, err := z.Create(name)
			if err != nil {
				t.Fatal(err)
			}
			data := []byte("synthetic")
			if name == "engram.exe" {
				data = core
			}
			if _, err := w.Write(data); err != nil {
				t.Fatal(err)
			}
		}
		if err := z.Close(); err != nil {
			t.Fatal(err)
		}
		return b.Bytes()
	}
	write := func(path string, b []byte) {
		t.Helper()
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []string{"valid", "digest", "source", "checksums", "duplicate", "unexpected", "traversal", "missing-line", "duplicate-line", "wrong-line-hash", "bad-version", "bad-source", "bad-archive-hash", "bad-checksums-hash"} {
		t.Run(tc, func(t *testing.T) {
			extra := ""
			if tc == "duplicate" {
				extra = "engram.exe"
			}
			if tc == "unexpected" {
				extra = "other.exe"
			}
			if tc == "traversal" {
				extra = "../engram.exe"
			}
			payload := makeZIP(extra)
			write(archive, payload)
			pins := publishedCoreContext{Version: "1.2.3", Source: revision, ArchiveSHA256: stagedDigest(payload)}
			checksum := []byte(pins.ArchiveSHA256 + "  engram_1.2.3_windows_amd64.zip\n")

			if tc == "missing-line" {
				checksum = []byte(pins.ArchiveSHA256 + "  other.zip\n")
			}
			if tc == "duplicate-line" {
				checksum = append(checksum, checksum...)
			}
			if tc == "wrong-line-hash" {
				checksum = []byte(strings.Repeat("0", 64) + "  engram_1.2.3_windows_amd64.zip\n")
			}
			write(sums, checksum)
			pins.ChecksumsSHA256 = stagedDigest(checksum)
			if tc == "digest" {
				pins.ArchiveSHA256 = strings.Repeat("0", 64)
			}
			if tc == "source" {
				pins.Source = strings.Repeat("0", 40)
			}
			if tc == "checksums" {
				write(sums, []byte("changed"))
			}

			switch tc {
			case "bad-version":
				pins.Version = "../1.2.3"
			case "bad-source":
				pins.Source = strings.Repeat("A", 40)
			case "bad-archive-hash":
				pins.ArchiveSHA256 = "short"
			case "bad-checksums-hash":
				pins.ChecksumsSHA256 = strings.Repeat("A", 64)
			}
			got, err := admitPublishedCore(context.Background(), archive, sums, pins)
			if tc != "valid" {
				if err == nil {
					t.Fatal("invalid input admitted")
				}
				return
			}
			if err != nil || !bytes.Equal(got, core) {
				t.Fatalf("valid admission: %v", err)
			}

			// Byte admission is portable; only command execution is host-gated.
			if runtime.GOOS != "windows" || runtime.GOARCH != "amd64" {
				return
			}
			p, _ := json.Marshal(pins)
			write(pinsFile, p)
			var out bytes.Buffer
			err = run([]string{"-observe-published-core", "-archive", archive, "-checksums", sums, "-context", pinsFile}, &out)
			var exit *assessmentExit
			if !errors.As(err, &exit) {
				t.Fatalf("not a nonapproving observation: %v", err)
			}
			var result map[string]any
			if json.Unmarshal(out.Bytes(), &result) != nil || result["admission"] != "passed" || result["runtime"] != nil || result["outcome"] != "blocked" {
				t.Fatalf("admission-only executed or approved: %s", out.Bytes())
			}
			write(pinsFile, []byte(`{"version":"1.2.3","unknown":true}`))
			out.Reset()
			_ = runPublishedCore([]string{"-archive", archive, "-checksums", sums, "-context", pinsFile, "-execute"}, &out)
			if strings.Contains(out.String(), `"runtime"`) || !strings.Contains(out.String(), `"admission":"unavailable"`) {
				t.Fatalf("malformed context reached execution: %s", out.Bytes())
			}
		})
	}
}

// Authored protocol fixture, never evidence about a distributed binary.
func publishedPersistenceProgram() string {
	source := strings.Replace(artifactCoreFixture, "handler:=http.HandlerFunc", `
 var saved map[string]any
 file:=os.Getenv("ENGRAM_DATA_DIR")+string(os.PathSeparator)+"record.json"
 if !strings.Contains(cwd,"-lost-") { b,_:=os.ReadFile(file); _=json.Unmarshal(b,&saved) }
 handler:=http.HandlerFunc`, 1)
	source = strings.Replace(source, "identity:=instance", `
  if r.URL.Path=="/sessions" { w.WriteHeader(201); _,_=w.Write([]byte("{}")); return }
  if r.URL.Path=="/observations" && r.Method=="POST" {
   if strings.Contains(cwd,"-writefail-") { w.WriteHeader(500); _,_=w.Write([]byte("{}")); return }
   _=json.NewDecoder(r.Body).Decode(&saved); saved["id"]=1
   b,_:=json.Marshal(saved); _=os.WriteFile(file,b,0600)
   w.WriteHeader(201); _,_=w.Write([]byte("{\"id\":1}")); return
  }
  if r.URL.Path=="/observations/1" {
   if saved==nil { w.WriteHeader(404); return }; _=json.NewEncoder(w).Encode(saved); return
  }
  identity:=instance
  if strings.Contains(cwd,"-changed-") && saved!=nil { identity="changed-after-restart" }
`, 1)
	return source
}

func TestPublishedCorePersistence(t *testing.T) {
	requireArtifactNode(t)
	root := t.TempDir()
	source := publishedPersistenceProgram()
	name := filepath.Join(root, "fixture.go")
	if err := os.WriteFile(name, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "fixture.exe")
	cmd := exec.Command("go", "build", "-o", binary, name)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v %s", err, out)
	}
	core, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, version, want string }{
		{"persisted", "fixture-observed", "passed"},
		{"lost", "fixture-observed", "unavailable"},
		{"wrong-version", "different", "unavailable"},
		{"writefail", "fixture-observed", "unavailable"},
		{"changed", "fixture-observed", "unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			got := runArtifactCoreObservation(ctx, core, stagedDigest(core), tc.name, tc.version)
			if got.State != tc.want || !got.Joined || got.Cleanup != "passed" || got.Retained {
				t.Fatalf("unexpected observation: %+v", got)
			}
			if (got.Persistence == "passed") != (tc.want == "passed") {
				t.Fatalf("persistence: %+v", got)
			}
		})
	}
}

// A successful synthetic runtime must not hide mutation of the reviewed context.
func TestPublishedCoreContextMutation(t *testing.T) {
	if runtime.GOOS != "windows" || runtime.GOARCH != "amd64" {
		t.Skip("published observation supports Windows amd64 only")
	}
	requireArtifactNode(t)

	for _, changedInput := range []string{"control", "context", "archive", "checksums"} {
		t.Run(changedInput, func(t *testing.T) {
			root := t.TempDir()
			pinsFile := filepath.Join(root, "context.json")
			source := strings.ReplaceAll(publishedPersistenceProgram(), "fixture-observed", "1.2.3")

			archive, sums := filepath.Join(root, "core.zip"), filepath.Join(root, "checksums.txt")
			target := map[string]string{"context": pinsFile, "archive": archive, "checksums": sums}[changedInput]
			if target != "" {
				source = strings.Replace(source, `token:=os.Getenv("ENGRAM_TOKEN")`, fmt.Sprintf(`
 b,_:=os.ReadFile(%q); _=os.WriteFile(%q,append(b,' '),0600)
 token:=os.Getenv("ENGRAM_TOKEN")`, target, target), 1)
			}
			core, revision, _ := stagedProgramFixture(t, source)
			var payload bytes.Buffer
			z := zip.NewWriter(&payload)
			for _, name := range []string{"engram.exe", "tools/cloud-sync-projects.sh", "tools/cloud-sync-projects.ps1"} {
				w, e := z.Create(name)
				if e != nil {
					t.Fatal(e)
				}
				data := []byte("synthetic")
				if name == "engram.exe" {
					data = core
				}
				if _, e = w.Write(data); e != nil {
					t.Fatal(e)
				}
			}
			if e := z.Close(); e != nil {
				t.Fatal(e)
			}
			checksum := []byte(stagedDigest(payload.Bytes()) + "  engram_1.2.3_windows_amd64.zip\n")
			pins := publishedCoreContext{Version: "1.2.3", Source: revision, ArchiveSHA256: stagedDigest(payload.Bytes()), ChecksumsSHA256: stagedDigest(checksum)}
			original, e := json.Marshal(pins)
			if e != nil {
				t.Fatal(e)
			}
			for name, data := range map[string][]byte{archive: payload.Bytes(), sums: checksum, pinsFile: original} {
				if e := os.WriteFile(name, data, 0600); e != nil {
					t.Fatal(e)
				}
			}
			var out bytes.Buffer
			err := runPublishedCore([]string{"-archive", archive, "-checksums", sums, "-context", pinsFile, "-execute"}, &out)
			var exit *assessmentExit
			if !errors.As(err, &exit) {
				t.Fatalf("observation: %v", err)
			}
			var result struct {
				Admission string            `json:"admission"`
				Outcome   string            `json:"outcome"`
				Runtime   artifactCoreCheck `json:"runtime"`
			}
			if e := json.Unmarshal(out.Bytes(), &result); e != nil {
				t.Fatal(e)
			}

			want := "unavailable"
			if changedInput == "control" {
				want = "passed"
			}
			if target != "" {
				changed, e := os.ReadFile(target)
				before := map[string][]byte{pinsFile: original, archive: payload.Bytes(), sums: checksum}[target]
				if e != nil || bytes.Equal(changed, before) {
					t.Fatal("mutation not observed")
				}
			}
			if result.Outcome != "blocked" || result.Admission != want || result.Runtime.State != want || result.Runtime.Persistence != "passed" || !result.Runtime.Joined || result.Runtime.Cleanup != "passed" || result.Runtime.Retained {
				t.Fatalf("mutated input improperly accepted or runtime incomplete: %+v", result)
			}
		})
	}
}

func TestPublishedCorePinnedFile(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "input")
	if e := os.WriteFile(file, []byte("123456789"), 0600); e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		name, path, hash string
		limit            int64
		valid            bool
	}{
		{"valid", file, stagedDigest([]byte("123456789")), 9, true},
		{"oversize", file, stagedDigest([]byte("123456789")), 8, false},
		{"directory", root, strings.Repeat("0", 64), 10, false},
		{"missing", filepath.Join(root, "absent"), strings.Repeat("0", 64), 10, false},
		{"digest", file, strings.Repeat("0", 64), 9, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := readPinnedArtifact(tc.path, tc.hash, tc.limit)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%t error=%v", tc.valid, err)
			}
		})
	}
}
