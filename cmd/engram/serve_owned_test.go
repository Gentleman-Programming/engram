package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestServeOwnedChild(t *testing.T) {
	if os.Getenv("ENGRAM_OWNED_TEST_CHILD") == "parent" {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = w.Close() }()
		child := exec.Command(os.Args[0], "-test.run=^TestServeOwnedChild$", "--", "--session-owned")
		for _, e := range os.Environ() {
			if !strings.HasPrefix(e, "ENGRAM_OWNED_TEST_CHILD=") {
				child.Env = append(child.Env, e)
			}
		}
		child.Env = append(child.Env, "ENGRAM_OWNED_TEST_CHILD=1")
		child.Stdin = r
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		_ = r.Close()
		_ = child.Wait()
		os.Exit(0)
	}
	if os.Getenv("ENGRAM_OWNED_TEST_CHILD") != "1" {
		return
	}
	os.Args = append([]string{"engram", "serve"}, os.Args[3:]...)
	main()
	os.Exit(0)
}

func ownedCommand(t *testing.T, args ...string) (*exec.Cmd, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "store")
	cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestServeOwnedChild$", "--", "--session-owned"}, args...)...)
	// Never inherit user stores, tokens, cloud settings or CLI test-child switches.
	for _, e := range os.Environ() {
		k := strings.SplitN(e, "=", 2)[0]
		if !strings.HasPrefix(k, "ENGRAM_") && !strings.HasPrefix(k, "GENTLE_") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	cmd.Env = append(cmd.Env, "ENGRAM_OWNED_TEST_CHILD=1", "ENGRAM_DATA_DIR="+dir, "ENGRAM_TOKEN="+strings.Repeat("a", 64), "HOME="+t.TempDir())
	return cmd, dir
}

func TestServeOwnedRejectsBeforeStore(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		env  string
	}{
		{name: "missing token", env: "ENGRAM_TOKEN="},
		{name: "short token", env: "ENGRAM_TOKEN=short"},
		{name: "port", args: []string{"7437"}},
		{name: "socket", args: []string{"--socket=x"}},
		{name: "env port", env: "ENGRAM_PORT=7437"},
		{name: "blank env port", env: "ENGRAM_PORT="},
		{name: "env socket", env: "ENGRAM_SOCKET=x"},
		{name: "URL", env: "ENGRAM_URL=http://127.0.0.1:7437"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd, dir := ownedCommand(t, tc.args...)
			if tc.env != "" {
				k := strings.SplitN(tc.env, "=", 2)[0] + "="
				filtered := cmd.Env[:0]
				for _, e := range cmd.Env {
					if !strings.HasPrefix(e, k) {
						filtered = append(filtered, e)
					}
				}
				cmd.Env = append(filtered, tc.env)
			}
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("expected rejection: %s", out)
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatalf("rejection touched store: %v", err)
			}
		})
	}
}

func TestServeOwnedStartupFailure(t *testing.T) {
	cmd, dir := ownedCommand(t)
	// Keep the parent alive: EOF requests successful shutdown and must not
	// compete with the invalid-store startup failure asserted by this test.
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdin.Close() }()
	if err := os.WriteFile(dir, []byte("not a store"), 0600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err == nil {
		t.Fatal("expected startup failure")
	}
	if stdout.Len() != 0 {
		t.Fatalf("failure emitted readiness: %s", stdout.String())
	}
	if strings.Contains(stderr.String(), strings.Repeat("a", 64)) {
		t.Fatal("token leaked on failure")
	}
	contents, err := os.ReadFile(dir)
	if err != nil || string(contents) != "not a store" {
		t.Fatalf("failed startup changed fixture: %q %v", contents, err)
	}
}

func TestServeOwnedParentDeath(t *testing.T) {
	cmd, _ := ownedCommand(t)
	for i, e := range cmd.Env {
		if strings.HasPrefix(e, "ENGRAM_OWNED_TEST_CHILD=") {
			cmd.Env[i] = "ENGRAM_OWNED_TEST_CHILD=parent"
		}
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	line := make(chan string, 1)
	go func() {
		s := bufio.NewScanner(stdout)
		if s.Scan() {
			line <- s.Text()
		} else {
			line <- ""
		}
	}()
	var ready struct {
		Endpoint string `json:"endpoint"`
	}
	select {
	case v := <-line:
		if err := json.Unmarshal([]byte(v), &ready); err != nil {
			t.Fatalf("readiness: %q %s", v, stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("parent child not ready")
	}
	_ = cmd.Process.Kill()
	client := http.Client{Timeout: time.Second}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		res, err := client.Get(ready.Endpoint + "/health")
		if err != nil {
			_ = cmd.Wait()
			return
		}
		_ = res.Body.Close()
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("server survived owning parent death")
}

func TestServeOwnedReadinessAuthAndEOF(t *testing.T) {
	cmd, _ := ownedCommand(t)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = stdin.Close()
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	lines := make(chan string, 1)
	go func() {
		s := bufio.NewScanner(stdout)
		if s.Scan() {
			lines <- s.Text()
		} else {
			lines <- ""
		}
	}()
	var line string
	select {
	case line = <-lines:
	case <-time.After(10 * time.Second):
		t.Fatal("no readiness")
	}
	var ready struct {
		Protocol   string `json:"protocol"`
		Endpoint   string `json:"endpoint"`
		PID        int    `json:"pid"`
		InstanceID string `json:"instance_id"`
	}
	if err := json.Unmarshal([]byte(line), &ready); err != nil {
		t.Fatalf("readiness %q: %v; stderr %s", line, err, stderr.String())
	}
	if ready.Protocol != "engram-session-owned-v1" || ready.PID != cmd.Process.Pid || len(ready.InstanceID) != 32 || !strings.HasPrefix(ready.Endpoint, "http://127.0.0.1:") || len(line) > 1024 || strings.Contains(line, strings.Repeat("a", 64)) {
		t.Fatalf("unsafe readiness: %s", line)
	}
	client := http.Client{Timeout: 2 * time.Second}
	for _, path := range []string{"/health", "/stats", "/sessions", "/unknown"} {
		for _, token := range []string{"", "wrong", strings.Repeat("a", 64)} {
			method := "GET"
			if path == "/sessions" {
				method = "POST"
			}
			req, _ := http.NewRequest(method, ready.Endpoint+path, strings.NewReader(`{}`))
			if token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			res, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = res.Body.Close()
			if token != strings.Repeat("a", 64) && res.StatusCode != 401 {
				t.Fatalf("%s without valid token: %d", path, res.StatusCode)
			}
			if token == strings.Repeat("a", 64) && res.StatusCode == 401 {
				t.Fatalf("%s valid token rejected", path)
			}
		}
	}
	_ = stdin.Close()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("EOF exit: %v %s", err, stderr.String())
		}
	case <-time.After(8 * time.Second):
		t.Fatal("EOF left child alive")
	}
}
