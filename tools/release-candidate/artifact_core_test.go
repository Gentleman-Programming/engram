package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Authored process fixture only: it is never production artifact evidence.
const artifactCoreFixture = `package main
import (
 "encoding/hex"
 "encoding/json"
 "io"
 "net"
 "net/http"
 "os"
 "strings"
)
func main() {
 cwd, err := os.Getwd()
 if err != nil || len(os.Args)!=3 || os.Args[1]!="serve" || os.Args[2]!="--session-owned" { os.Exit(20) }
 token:=os.Getenv("ENGRAM_TOKEN")
 decoded, err:=hex.DecodeString(token)
 if err!=nil || len(decoded)!=32 { os.Exit(21) }
 for _,key:=range []string{"ENGRAM_URL","ENGRAM_PORT","ENGRAM_SOCKET","ENGRAM_HTTP_TOKEN","ENGRAM_CLOUD_AUTOSYNC","ENGRAM_CLOUD_TOKEN","ENGRAM_CLOUD_SERVER","OPENAI_API_KEY","HTTP_PROXY","NODE_OPTIONS"} {
  if _,found:=os.LookupEnv(key); found { os.Exit(22) }
 }
 for _,key:=range []string{"ENGRAM_DATA_DIR","HOME","USERPROFILE","APPDATA","LOCALAPPDATA","XDG_CONFIG_HOME","XDG_DATA_HOME","XDG_CACHE_HOME","TMP","TEMP","TMPDIR"} {
  if !strings.HasPrefix(os.Getenv(key),cwd+string(os.PathSeparator)) { os.Exit(23) }
 }
 listener, err:=net.Listen("tcp","127.0.0.1:0")
 if err!=nil { os.Exit(24) }
 instance:="synthetic-owned-instance"
 handler:=http.HandlerFunc(func(w http.ResponseWriter,r *http.Request) {
  if r.Header.Get("Authorization")!="Bearer "+token && !strings.Contains(cwd,"-128-") { w.WriteHeader(401); return }
  identity:=instance
  if strings.Contains(cwd,"-124-") { identity="different-instance" }
  _=json.NewEncoder(w).Encode(map[string]any{"status":"ok","service":"engram","version":"fixture-observed","instance_id":identity})
 })
 go func(){ _=http.Serve(listener,handler) }()
 protocol, endpoint, pid:="engram-session-owned-v1","http://"+listener.Addr().String(),os.Getpid()
 if strings.Contains(cwd,"-125-") { protocol="wrong-protocol" }
 if strings.Contains(cwd,"-129-") { pid++ }
 if strings.Contains(cwd,"-130-") { endpoint="http://example.invalid:80" }
 if !strings.Contains(cwd,"-127-") {
  _=json.NewEncoder(os.Stdout).Encode(map[string]any{"protocol":protocol,"endpoint":endpoint,"pid":pid,"instance_id":instance})
 }
 _,_=io.Copy(io.Discard,os.Stdin)
 if strings.Contains(cwd,"-126-") { select{} }
 _=listener.Close()
}
`

// Change only the synthetic manifest run label. Runtime cwd carries that label,
// so the authored child can expose deterministic public protocol failures.
func runtimeFixtureZIP(t *testing.T, original []byte, run string) []byte {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(original), int64(len(original)))
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	for _, file := range reader.File {
		r, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(r)
		closeErr := r.Close()
		if err != nil || closeErr != nil {
			t.Fatalf("fixture member read: %v/%v", err, closeErr)
		}
		if file.Name == "manifest.json" {
			var manifest map[string]any
			if err := json.Unmarshal(data, &manifest); err != nil {
				t.Fatal(err)
			}
			manifest["run_id"] = run
			data, err = json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
		}
		w, err := writer.Create(file.Name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func TestArtifactCoreFailedKill(t *testing.T) {
	core, _, _ := stagedProgramFixture(t, artifactCoreFixture)
	marker := filepath.Join(t.TempDir(), "owned-child-exited.json")
	encoded, err := json.Marshal(marker)
	if err != nil {
		t.Fatal(err)
	}
	// The error cannot safely be forced via OS permission changes. Instrument
	// only this test's builtin spawn, keeping the real creation handle/kill.
	hook := `
import cp from "node:child_process";
import { syncBuiltinESMExports } from "node:module";
import { writeFileSync } from "node:fs";
const marker = ` + string(encoded) + `;
const spawn = cp.spawn, exit = process.exit.bind(process);
let child, terminate, stopping = false, faultAlive = false;
function finish(code) {
  if (stopping) return;
  stopping = true;
  if (!child) return exit(70);
  child.once("exit", () => {
    clearTimeout(watchdog);
    writeFileSync(marker, JSON.stringify({ root: process.cwd(), faultAlive, exitObserved: true }));
    exit(code);
  });
  terminate(); // Saved real handle, never a discovered/recycled PID.
}
const watchdog = setTimeout(() => finish(70), 12000);
cp.spawn = (...args) => {
  child = spawn(...args);
  terminate = child.kill.bind(child);
  child.kill = () => {
    faultAlive = !!child.pid && child.exitCode === null && child.signalCode === null;
    child.emit("error", new Error("synthetic owned kill failure"));
    return false;
  };
  return child;
};
syncBuiltinESMExports();
process.exit = finish;
`
	original := artifactCoreOwner
	preload := "import \"data:text/javascript;base64," + base64.StdEncoding.EncodeToString([]byte(hook)) + "\";\n"
	artifactCoreOwner = append([]byte(preload), original...)
	t.Cleanup(func() { artifactCoreOwner = original })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	result := runArtifactCore(ctx, core, stagedDigest(core), "126")
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("fixture has no observed own-child exit: %v", err)
	}
	var proof struct {
		Root                     string
		FaultAlive, ExitObserved bool
	}
	if err := json.Unmarshal(data, &proof); err != nil {
		t.Fatal(err)
	}
	if !proof.FaultAlive || !proof.ExitObserved || !strings.HasPrefix(filepath.Base(proof.Root), "engram-core-126-") || filepath.Dir(proof.Root) != filepath.Clean(os.TempDir()) {
		t.Fatal("no safe direct-handle termination proof")
	}
	// The instrumented owner observed the real exit AFTER emitting the result.
	// This test now has independent cleanup authority; the product did not.
	t.Cleanup(func() {
		if err := os.RemoveAll(proof.Root); err != nil {
			t.Error(err)
		}
	})
	if result.State != "unavailable" || result.Joined || result.Cleanup != "unknown" || !result.Retained {
		t.Fatalf("operation error falsely established termination: %+v", result)
	}
	if info, err := os.Stat(proof.Root); err != nil || !info.IsDir() {
		t.Fatal("unknown termination did not retain runtime")
	}
}
