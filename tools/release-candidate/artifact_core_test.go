package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Authored process fixture only: it is never production artifact evidence.
const artifactCoreFixture = `package main
import (
 "fmt"
 "encoding/hex"
 "encoding/json"
 "io"
 "net"
 "net/http"
 "os"
 "strings"
 "sync"
 "sync/atomic"
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
 var written atomic.Bool
 var mutex sync.Mutex
 observations:=[]map[string]any{}
 relations:=[]map[string]any{}
 registered,finalized:=false,false
 summaryID:=0
 nextID:=0
 handler:=http.HandlerFunc(func(w http.ResponseWriter,r *http.Request) {
  if r.Header.Get("Authorization")!="Bearer "+token && !strings.Contains(cwd,"-128-") { w.WriteHeader(401); _=json.NewEncoder(w).Encode(map[string]any{"error":"unauthorized"}); return }
  mutex.Lock(); defer mutex.Unlock()
  finalizationFixture:=false
  for _,run:=range []string{"153","154","155","156","157","158","159"} { finalizationFixture=finalizationFixture || strings.Contains(cwd,"engram-core-"+run+"-") }
  judgmentFixture:=finalizationFixture || strings.Contains(cwd,"engram-core-150-") || strings.Contains(cwd,"engram-core-151-") || strings.Contains(cwd,"engram-core-152-")
  consolidationFixture:=judgmentFixture || strings.Contains(cwd,"engram-core-148-") || strings.Contains(cwd,"engram-core-149-")
  topicFixture:=false
  for _,run:=range []string{"140","141","142","143","144","145","146","147","148","149","150","151","152","153","154","155","156","157","158","159"} { topicFixture=topicFixture || strings.Contains(cwd,"engram-core-"+run+"-") }
  if judgmentFixture && strings.HasPrefix(r.URL.Path,"/conflicts") {
   switch r.URL.Path {
   case "/conflicts/scan":
    relations=[]map[string]any{{"id":1,"sync_id":"fixture-first","relation":"related","judgment_status":"pending"},{"id":2,"sync_id":"fixture-second","relation":"related","judgment_status":"pending"}}
    _=json.NewEncoder(w).Encode(map[string]any{"inserted":2,"dry_run":false,"semantic_judged":0,"semantic_skipped":0,"semantic_errors":0}); return
   case "/conflicts":
    _=json.NewEncoder(w).Encode(map[string]any{"total":len(relations),"relations":relations}); return
   case "/conflicts/stats":
    statuses,verbs:=map[string]int{},map[string]int{}
    for _,value:=range relations { statuses[value["judgment_status"].(string)]++; verbs[value["relation"].(string)]++ }
    _=json.NewEncoder(w).Encode(map[string]any{"by_judgment_status":statuses,"by_relation":verbs}); return
   case "/conflicts/judge-many":
    var params struct { Judgments []map[string]any }
    _=json.NewDecoder(r.Body).Decode(&params)
    if len(params.Judgments)==0 { w.WriteHeader(400); _=json.NewEncoder(w).Encode(map[string]any{"error":"judgments must be an array containing 1..32 items"}); return }
    outcomes:=[]map[string]any{}
    marker:=""
    for i,item:=range params.Judgments {
     id,_:=item["judgment_id"].(string); verb,_:=item["relation"].(string)
     errorText:=""
     if confidence,found:=item["confidence"]; found { if number,ok:=confidence.(float64); !ok || number<0 || number>1 { errorText="confidence must be a number between 0.0 and 1.0" } }
     if errorText=="" { switch verb { case "related","compatible","scoped","conflicts_with","supersedes","not_conflict": default: errorText=fmt.Sprintf("JudgeRelation: invalid relation verb %q — must be one of: related, compatible, scoped, conflicts_with, supersedes, not_conflict",verb) } }
     var found map[string]any
     if errorText=="" { for _,value:=range relations { if value["sync_id"]==id { found=value } }; if found==nil { errorText=fmt.Sprintf("JudgeRelation: relation %q not found",id) } }
     outcome:=map[string]any{"index":i,"judgment_id":id,"status":"error","error":errorText}
     if errorText=="" {
      found["relation"],found["judgment_status"]=verb,"judged"
      if confidence,ok:=item["confidence"]; ok { found["confidence"]=confidence }
      if strings.Contains(cwd,"engram-core-152-") && verb=="conflicts_with" { found["relation"]="related"; marker="persisted judgment changed" }
      outcome=map[string]any{"index":i,"judgment_id":id,"status":"success","judgment_status":"judged","relation":found}
     }
     outcomes=append(outcomes,outcome)
    }
    if strings.Contains(cwd,"engram-core-151-") && len(relations)==0 && len(outcomes)==2 { outcomes[0],outcomes[1]=outcomes[1],outcomes[0]; marker="batch item order" }
    _=json.NewEncoder(w).Encode(map[string]any{"outcomes":outcomes,"fixture_mutated":marker}); return
   default:
    for _,value:=range relations { if r.URL.Path==fmt.Sprintf("/conflicts/%v",value["id"]) { _=json.NewEncoder(w).Encode(value); return } }
   }
  }
  if topicFixture {
   switch r.URL.Path {
   case "/topic-keys/suggest":
    var params map[string]any
    _=json.NewDecoder(r.Body).Decode(&params)
    if params["title"]==nil && params["content"]==nil { w.WriteHeader(400); _=json.NewEncoder(w).Encode(map[string]any{"error":"provide title or content to suggest a topic_key"}); return }
    _=json.NewEncoder(w).Encode(map[string]any{"topic_key":"architecture/auth-model"}); return
   case "/observations/consolidate":
    if consolidationFixture {
     var params map[string]any
     _=json.NewDecoder(r.Body).Decode(&params)
     if params["confirm"]!=true {
      mutated:=strings.Contains(cwd,"engram-core-149-")
      if mutated && len(observations)>0 { observations[0]["content"]="mutated preview" }
      _=json.NewEncoder(w).Encode(map[string]any{"status":"preview","plan":map[string]any{"fingerprint":"fixture-plan"},"fixture_mutated":mutated}); return
     }
     if params["fingerprint"]!="fixture-plan" { w.WriteHeader(400); return }
     nextID++
     observations=[]map[string]any{{"id":nextID,"title":params["new_title"],"content":params["new_content"]}}
     _=json.NewEncoder(w).Encode(map[string]any{"status":"consolidated","soft_deleted_ids":params["ids_to_delete"]}); return
    }
   case "/sessions/real-host/finalize":
    if finalizationFixture {
     if !registered { w.WriteHeader(404); _=json.NewEncoder(w).Encode(map[string]any{"error":"session not registered"}); return }
     var params map[string]any; _=json.NewDecoder(r.Body).Decode(&params)
     state,marker:="already_finalized",""
     if !finalized {
      state="finalized"; nextID++; summaryID=nextID; finalized=true
      observations=append(observations,map[string]any{"id":summaryID,"type":"session_summary","session_id":"real-host","content":params["summary"]})
     } else if strings.Contains(cwd,"engram-core-154-") { summaryID++; marker="finalization receipt changed" }
     _=json.NewEncoder(w).Encode(map[string]any{"state":state,"session_id":"real-host","summary_id":summaryID,"summary_sync_id":"fixture-summary","fixture_mutated":marker}); return
    }
   case "/observations":
    if consolidationFixture {
     if r.Method=="POST" {
      if finalizationFixture && finalized { w.WriteHeader(409); _=json.NewEncoder(w).Encode(map[string]any{"error":"session finalized"}); return }
      if finalizationFixture { registered=true }
      var value map[string]any
      _=json.NewDecoder(r.Body).Decode(&value)
      nextID++; value["id"]=nextID; observations=append(observations,value)
      _=json.NewEncoder(w).Encode(map[string]any{"id":nextID}); return
     }
     _=json.NewEncoder(w).Encode(observations); return
    }
    if r.Method=="POST" { written.Store(true); _=json.NewEncoder(w).Encode(map[string]any{"id":1}); return }
    if written.Load() { _=json.NewEncoder(w).Encode([]any{map[string]any{"id":1}}) } else { _=json.NewEncoder(w).Encode([]any{}) }; return
   case "/sessions/real-host":
    if finalizationFixture && registered {
     ended:=""; if finalized { ended="fixture-ended" }
     _=json.NewEncoder(w).Encode(map[string]any{"id":"real-host","ended_at":ended}); return
    }
    w.WriteHeader(404); _=json.NewEncoder(w).Encode(map[string]any{"error":"not found"}); return
   }
  }
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

func artifactNodeMajor(t *testing.T) int {
	t.Helper()
	out, err := exec.Command("node", "--version").Output()
	if err != nil {
		t.Skipf("Core lifecycle tests require Node.js >=24 in PATH: %v", err)
	}
	version := strings.TrimSpace(string(out))
	major, err := strconv.Atoi(strings.Split(strings.TrimPrefix(version, "v"), ".")[0])
	if err != nil {
		t.Fatalf("cannot parse Node.js version %q: %v", version, err)
	}
	return major
}

func requireArtifactNode(t *testing.T) {
	t.Helper()
	if major := artifactNodeMajor(t); major < 24 {
		t.Skipf("Core lifecycle tests require Node.js >=24 in PATH; found Node.js %d", major)
	}
}

func TestArtifactCoreUnsupportedNode(t *testing.T) {
	if major := artifactNodeMajor(t); major >= 24 {
		t.Skip("unsupported-runtime regression requires Node.js <24 in PATH")
	}
	marker := filepath.Join(t.TempDir(), "core-started")
	encoded, err := json.Marshal(marker)
	if err != nil {
		t.Fatal(err)
	}
	program := strings.Replace(artifactCoreFixture, "func main() {", "func main() {\n if os.WriteFile("+string(encoded)+", []byte(\"started\"), 0600) != nil { os.Exit(25) }", 1)
	core, _, _ := stagedProgramFixture(t, program)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	result := runArtifactCoreWithPi(ctx, core, stagedDigest(core), "123", nil, nil)
	if result.State != "unavailable" || result.Cleanup != "not-started" || !result.Joined || result.Retained || !strings.HasPrefix(result.NodeVersion, strconv.Itoa(artifactNodeMajor(t))+".") {
		t.Fatalf("unsupported Node.js did not fail closed before spawn: %+v", result)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("unsupported Node.js started Core or marker check failed: %v", err)
	}
}

func TestArtifactCoreFailedKill(t *testing.T) {
	requireArtifactNode(t)
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
	result := runArtifactCoreWithPi(ctx, core, stagedDigest(core), "126", nil, nil)
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

func TestArtifactPiLifetime(t *testing.T) {
	requireArtifactNode(t)
	core, _, _ := stagedProgramFixture(t, artifactCoreFixture)
	for _, mode := range []string{"parent EOF", "early clean Core exit"} {
		t.Run(mode, func(t *testing.T) {
			root, err := os.MkdirTemp("", "engram-lifetime-")
			if err != nil {
				t.Fatal(err)
			}
			// Unknown joins retain this root; never clean a possibly live runtime.
			marker := filepath.Join(root, "core-stop-order.json")
			encoded, _ := json.Marshal(marker)
			eof := mode == "parent EOF"
			hook := `
import cp from "node:child_process";
import { syncBuiltinESMExports } from "node:module";
import { writeFileSync } from "node:fs";
const spawn = cp.spawn;
let core, requested = false, piExited = false, coreClosedEarly = false;
cp.spawn = (...args) => {
  const child = spawn(...args);
  if (!core) { core = child; return child; }
  child.once("exit", () => { piExited = true; });
  const end = child.stdin.end.bind(child.stdin);
  if (` + strconv.FormatBool(eof) + `) {
    const coreEnd = core.stdin.end.bind(core.stdin);
    core.stdin.end = (...args) => {
      coreClosedEarly ||= !piExited;
      writeFileSync(` + string(encoded) + `, JSON.stringify({ piExited: !coreClosedEarly }));
      return coreEnd(...args);
    };
    child.stdin.end = (...args) => {
      if (!requested) {
        requested = true;
        process.stdout.write("synthetic-pi-joining\n");
        return child.stdin;
      }
      return end(...args);
    };
    process.stdin.once("end", () => end());
  } else {
    child.stdin.end = (...args) => {
      if (!requested) {
        requested = true;
        core.once("exit", () => end(...args));
        core.stdin.end(); // Real creation handle requests a synthetic clean exit.
      }
      return child.stdin;
    };
  }
  return child;
};
syncBuiltinESMExports();
`
			write := func(name string, body []byte) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(root, name), body, 0700); err != nil {
					t.Fatal(err)
				}
			}
			write("core.exe", core)
			preload := "import \"data:text/javascript;base64," + base64.StdEncoding.EncodeToString([]byte(hook)) + "\";\n"
			write("owner.mjs", append([]byte(preload), artifactCoreOwner...))
			write("pi-owner.mjs", artifactPiOwner)
			write("pi-workflows.mjs", artifactPiWorkflows)
			write("index.ts", []byte("export const synthetic = true;\n"))
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "node", filepath.Join(root, "owner.mjs"), filepath.Join(root, "core.exe"), filepath.Join(root, "index.ts"))
			cmd.Dir, cmd.Env = root, []string{}
			for _, key := range []string{"HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "TMP", "TEMP", "TMPDIR", "ENGRAM_DATA_DIR"} {
				dir := filepath.Join(root, key)
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
				cmd.Env = append(cmd.Env, key+"="+dir)
			}
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = stdin.Close() }()
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			var diagnostic bytes.Buffer
			cmd.Stderr = &diagnostic
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			reader := bufio.NewReader(stdout)
			if eof {
				line, err := reader.ReadString('\n')
				if err != nil || line != "synthetic-pi-joining\n" {
					t.Fatalf("missing join synchronization: %q %v", line, err)
				}
				if err := stdin.Close(); err != nil {
					t.Fatal(err)
				}
			}
			output, readErr := io.ReadAll(reader)
			err = cmd.Wait()
			if err != nil || readErr != nil || diagnostic.Len() != 0 {
				t.Fatalf("owner exit=%v read=%v stdout=%q stderr=%q retained=%s", err, readErr, output, diagnostic.String(), root)
			}
			var report struct {
				artifactCoreCheck
				PiBootstrap artifactPiCheck `json:"piBootstrap"`
			}
			if json.Unmarshal(output, &report) != nil || bytes.Count(output, []byte("\n")) != 1 || !report.Joined || !report.PiBootstrap.Joined {
				t.Fatalf("unknown joins: %s retained=%s", output, root)
			}
			t.Cleanup(func() {
				if err := os.RemoveAll(root); err != nil {
					t.Error(err)
				}
			})
			if report.State != "unavailable" || report.PiBootstrap.State != "unavailable" || report.Cleanup != "passed" || report.PiBootstrap.Cleanup != "passed" || report.ExitCode == nil || *report.ExitCode != 0 || report.PiBootstrap.ExitCode == nil || *report.PiBootstrap.ExitCode != 0 {
				t.Errorf("lifetime violation falsely passed: %s", output)
			}
			if eof {
				proof, err := os.ReadFile(marker)
				if err != nil || string(proof) != `{"piExited":true}` {
					t.Errorf("Core stdin closed before actual Pi exit: %s %v", proof, err)
				}
			}
		})
	}
}
