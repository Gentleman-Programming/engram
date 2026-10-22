package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestSQLiteTempHTTPDeletedInheritedDirectory exercises the released runtime
// boundary: a fresh binary, native SQLite, and only public HTTP writes/reads.
func TestSQLiteTempHTTPDeletedInheritedDirectory(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("native Windows SQLite temporary routing")
	}
	if testing.Short() {
		t.Skip("builds CLI and seeds a large public HTTP corpus")
	}
	binary := buildCompatibilityBinary(t)
	root := t.TempDir()
	cwd := filepath.Join(root, "worktree")
	inherited := filepath.Join(root, "inherited-temp")
	home := filepath.Join(root, "home")
	for _, dir := range []string{cwd, inherited, home} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	token := strings.Repeat("c", 64)
	cmd := exec.Command(binary, "serve", "--session-owned")
	cmd.Dir = cwd
	// Exclude inherited runtime configuration, credentials and test-child modes.
	for _, entry := range os.Environ() {
		key := strings.ToUpper(strings.SplitN(entry, "=", 2)[0])
		if strings.HasPrefix(key, "ENGRAM_") || strings.HasPrefix(key, "GENTLE_") {
			continue
		}
		switch key {
		case "TEMP", "TMP", "HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA":
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	cmd.Env = append(cmd.Env, "ENGRAM_DATA_DIR="+filepath.Join(root, "data"), "ENGRAM_TOKEN="+token,
		"TEMP="+inherited, "TMP="+inherited, "HOME="+home, "USERPROFILE="+home, "APPDATA="+home, "LOCALAPPDATA="+home)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	// A file avoids concurrent reads of a buffer while the server logs requests.
	logFile, err := os.Create(filepath.Join(root, "server.log"))
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = logFile.Close()
		if t.Failed() {
			if data, err := os.ReadFile(logFile.Name()); err == nil {
				t.Logf("server stderr:\n%s", data)
			}
		}
	})
	lines := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		if scanner.Scan() {
			lines <- scanner.Text()
		} else {
			lines <- ""
		}
		_, _ = io.Copy(io.Discard, stdout)
	}()
	var ready struct {
		Endpoint string `json:"endpoint"`
	}
	select {
	case line := <-lines:
		if err := json.Unmarshal([]byte(line), &ready); err != nil || ready.Endpoint == "" {
			t.Fatalf("invalid readiness %q: %v", line, err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("server readiness timed out")
	}
	client := &http.Client{Timeout: 15 * time.Second}
	defer client.CloseIdleConnections()
	call := func(method, path string, body any, status int) map[string]any {
		t.Helper()
		var payload []byte
		if body != nil {
			var err error
			payload, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
		}
		req, err := http.NewRequest(method, ready.Endpoint+path, bytes.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer func() {
			if err := resp.Body.Close(); err != nil {
				t.Errorf("close HTTP response body: %v", err)
			}
		}()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != status {
			t.Fatalf("%s %s status=%d want=%d body=%s", method, path, resp.StatusCode, status, data)
		}
		var result map[string]any
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatalf("%s %s response: %v (%s)", method, path, err, data)
		}
		return result
	}
	const session = "sqlite-temp-http-session"
	const project = "sqlite-temp-http-project"
	registered := call("POST", "/sessions", map[string]any{"id": session, "project": project, "directory": cwd}, 201)
	if registered["id"] != session {
		t.Fatalf("registered identity: %v", registered)
	}
	save := func(index int, kind, content string) float64 {
		t.Helper()
		result := call("POST", "/observations", map[string]any{"session_id": session, "project": project, "type": kind,
			"title": fmt.Sprintf("Synthetic SQLite temporary row %04d", index), "content": content, "capture_prompt": false}, 201)
		want := float64(index + 1)
		if result["id"] != want {
			t.Fatalf("save %d identity=%v want=%v", index, result["id"], want)
		}
		return want
	}
	// Distinct large contents prevent deduplication while reproducing the retained
	// 1,024 x approximately 48 KiB disk-backed FTS/write workload.
	for i := 0; i < 1024; i++ {
		save(i, "discovery", fmt.Sprintf("Synthetic corpus row %04d\n", i)+strings.Repeat("synthetic corpus payload ", 2048))
	}
	assertCount := func(want float64) {
		t.Helper()
		stats := call("GET", "/stats?project="+project, nil, 200)
		if stats["total_observations"] != want {
			t.Fatalf("observation count=%v want=%v", stats["total_observations"], want)
		}
	}
	assertCount(1024)
	// Only the empty, inherited synthetic directory is removed. The stable shared
	// directory and every SQLite-owned temporary file remain untouched.
	if err := os.Remove(inherited); err != nil {
		t.Fatalf("remove inherited temporary directory: %v", err)
	}
	pattern := save(1024, "pattern", "Synthetic pattern after inherited temporary directory deletion")
	summary := save(1025, "session_summary", "Synthetic summary after inherited temporary directory deletion")
	assertCount(1026)
	for _, id := range []float64{pattern, summary} {
		observation := call("GET", fmt.Sprintf("/observations/%.0f", id), nil, 200)
		if observation["id"] != id || observation["session_id"] != session || observation["project"] != project {
			t.Fatalf("saved identity changed: %v", observation)
		}
	}
	finalBody := map[string]any{"project": project, "summary": "Synthetic finalization after inherited temporary deletion"}
	finalized := call("POST", "/sessions/"+session+"/finalize", finalBody, 200)
	if finalized["session_id"] != session || finalized["summary_id"] != float64(1027) || finalized["already_finalized"] != false {
		t.Fatalf("finalization: %v", finalized)
	}
	syncID, ok := finalized["summary_sync_id"].(string)
	if !ok || syncID == "" {
		t.Fatalf("missing summary identity: %v", finalized)
	}
	replay := call("POST", "/sessions/"+session+"/finalize", finalBody, 200)
	if replay["summary_id"] != finalized["summary_id"] || replay["summary_sync_id"] != syncID || replay["already_finalized"] != true {
		t.Fatalf("unstable finalization identity: first=%v replay=%v", finalized, replay)
	}
	ended := call("GET", "/sessions/"+session, nil, 200)
	if ended["id"] != session || ended["project"] != project || ended["directory"] != strings.ToLower(cwd) || ended["ended_at"] == nil {
		t.Fatalf("session identity/completion changed: %v", ended)
	}
	assertCount(1027)
}
