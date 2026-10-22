package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSourceCheckCancellationDoesNotWaitForCheckerPipes(t *testing.T) {
	repo, workspace := t.TempDir(), t.TempDir()
	t.Setenv("GOTMPDIR", t.TempDir())
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	// The reviewed fixture signals readiness, then keeps inherited descriptors
	// open until the parent releases it. No process-tree termination is assumed.
	checker := fmt.Sprintf(`package main
import("net";"os")
func main(){c,e:=net.Dial("tcp4",%q);if e!=nil{os.Exit(9)};defer c.Close();if _,e=c.Write([]byte{1});e!=nil{return};_,_=c.Read(make([]byte,1))}
`, listener.Addr().String())
	for name, data := range map[string]string{
		"go.mod":                                "module example.invalid/cancellation\n\ngo 1.25.0\n",
		"tools/agent-contract/main.go":          checker,
		"docs/contracts/agent-contract-v1.json": "{}\n",
		"plugin/pi/package.json":                `{"name":"gentle-engram","version":"0.5.0"}`,
		"internal/setup/setup.go":               "package setup\nconst piGentleEngramPackage = \"npm:gentle-engram@0.5.0\"\n",
	} {
		path := filepath.Join(repo, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("fixture git: %v: %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "--quiet")
	git("config", "user.name", "Cancellation Fixture")
	git("config", "user.email", "fixture@example.invalid")
	git("config", "commit.gpgsign", "false")
	git("add", ".")
	git("commit", "--quiet", "-m", "fixture")
	commit := git("rev-parse", "HEAD")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan sourceCheck, 1)
	go func() { result <- runSourceCheck(ctx, repo, workspace, commit, commit, "0.5.0") }()
	type accepted struct {
		conn net.Conn
		err  error
	}
	ready := make(chan accepted, 1)
	go func() { c, e := listener.Accept(); ready <- accepted{c, e} }()
	var conn net.Conn
	select {
	case a := <-ready:
		if a.err != nil {
			t.Fatal(a.err)
		}
		conn = a.conn
	case r := <-result:
		t.Fatalf("checker never reached readiness: %+v", r)
	case <-time.After(15 * time.Second):
		t.Fatal("checker readiness deadline exceeded")
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(conn, make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	cancel()
	var observation sourceCheck
	returned := false
	select {
	case observation = <-result:
		returned = true
	case <-time.After(3 * time.Second):
		t.Error("canceled launcher still waits for live checker's inherited output pipes")
	}
	// Always release the controlled child, including on the pre-fix RED path.
	if err := conn.Close(); err != nil {
		t.Error(err)
	}
	if !returned {
		select {
		case observation = <-result:
		case <-time.After(5 * time.Second):
			t.Fatal("checker did not return after fixture release")
		}
	}
	if observation.State != "unavailable" || observation.ExitCode != nil || observation.SourceCommit != commit {
		t.Errorf("cancellation must not be a checker outcome: %+v", observation)
	}
	if git("rev-parse", "HEAD") != commit || git("status", "--porcelain") != "" {
		t.Error("checker changed original fixture repository")
	}
}
