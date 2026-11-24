package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestServeOwnedInheritedRejectsNonListenerBeforeStore(t *testing.T) {
	cmd, data := ownedCommand(t)
	bad, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = bad.Close() }()
	cmd.ExtraFiles = []*os.File{bad}
	cmd.Env = append(cmd.Env, "ENGRAM_OWNED_LISTENER_FD=3")
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "descriptor 3 is not a listening socket") {
		t.Fatalf("wrong startup result: %s (%v)", output, err)
	}
	if _, err := os.Stat(data); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid inherited socket opened the store: %v", err)
	}
}

func TestServeOwnedInheritedLeaseHandsOffAfterDrain(t *testing.T) {
	data := filepath.Join(t.TempDir(), "store")
	if err := os.Mkdir(data, 0700); err != nil {
		t.Fatal(err)
	}
	first := startInheritedCore(t, data)
	firstReady := first.readReady(t)
	canceled := startInheritedCore(t, data)
	canceled.waitForLeaseFile(t, data)
	if err := canceled.input.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case line := <-canceled.ready:
		if line != "" {
			t.Fatalf("canceled replacement announced readiness: %q", line)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("canceled replacement did not close its readiness stream")
	}
	canceled.waitExit(t)
	// Sending an incomplete body after 100 Continue holds a real request
	// inside the HTTP handler until the fixture releases it.
	conn, err := net.DialTimeout("tcp", first.address, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(7 * time.Second)); err != nil {
		t.Fatal(err)
	}
	payload := `{"id":"lease-handoff","project":"project"}`
	if _, err := fmt.Fprintf(conn, "POST /sessions HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\nExpect: 100-continue\r\nConnection: close\r\n\r\n",
		first.address, strings.Repeat("a", 64), len(payload)); err != nil {
		t.Fatal(err)
	}
	response := bufio.NewReader(conn)
	initial, err := http.ReadResponse(response, nil)
	if err != nil || initial.StatusCode != http.StatusContinue {
		t.Fatalf("request did not enter body read: %v %v", initial, err)
	}
	_ = initial.Body.Close()

	second := startInheritedCore(t, data)
	second.waitForLeaseFile(t, data)
	if err := first.input.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case line := <-second.ready:
		t.Fatalf("replacement opened the store while the predecessor drained: %q", line)
	case <-time.After(250 * time.Millisecond):
	}
	if _, err := io.WriteString(conn, payload); err != nil {
		t.Fatal(err)
	}
	written, err := http.ReadResponse(response, nil)
	if err != nil || written.StatusCode != http.StatusCreated {
		t.Fatalf("predecessor did not finish its request: %v %v", written, err)
	}
	_ = written.Body.Close()
	first.waitExit(t)
	secondReady := second.readReady(t)
	if firstReady.InstanceID != secondReady.InstanceID {
		t.Fatalf("replacement changed store identity: %q != %q", firstReady.InstanceID, secondReady.InstanceID)
	}
	request, err := http.NewRequest(http.MethodGet, secondReady.Endpoint+"/sessions/lease-handoff", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 64))
	client := http.Client{Timeout: 2 * time.Second}
	got, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = got.Body.Close() }()
	if got.StatusCode != http.StatusOK {
		t.Fatalf("replacement cannot read predecessor's completed request: HTTP %d", got.StatusCode)
	}
}

type inheritedCoreFixture struct {
	address string
	cmd     *exec.Cmd
	input   io.WriteCloser
	ready   <-chan string
}

func startInheritedCore(t *testing.T, data string) inheritedCoreFixture {
	t.Helper()
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	fd, err := listener.File()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fd.Close() }()
	cmd, _ := ownedCommand(t)
	cmd.Env = append(cmd.Env, "ENGRAM_DATA_DIR="+data, "ENGRAM_OWNED_LISTENER_FD=3")
	cmd.ExtraFiles = []*os.File{fd}
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line := make(chan string, 1)
	go func() {
		defer close(line)
		scanner := bufio.NewScanner(stdout)
		if scanner.Scan() {
			line <- scanner.Text()
		}
	}()
	t.Cleanup(func() {
		_ = stdin.Close()
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	return inheritedCoreFixture{listener.Addr().String(), cmd, stdin, line}
}

func (c inheritedCoreFixture) readReady(t *testing.T) ownedReadiness {
	t.Helper()
	select {
	case line := <-c.ready:
		var ready ownedReadiness
		if err := json.Unmarshal([]byte(line), &ready); err != nil || ready.Protocol != sessionOwnedProtocol ||
			ready.Endpoint != "http://"+c.address || ready.PID != c.cmd.Process.Pid || len(ready.InstanceID) != 32 || !ready.ExclusiveStore {
			t.Fatalf("wrong inherited readiness: %q (%v)", line, err)
		}
		return ready
	case <-time.After(10 * time.Second):
		t.Fatal("no inherited readiness")
		return ownedReadiness{}
	}
}

func (c inheritedCoreFixture) waitForLeaseFile(t *testing.T, data string) {
	t.Helper()
	fds := fmt.Sprintf("/proc/%d/fd", c.cmd.Process.Pid)
	lock := filepath.Join(data, ".owned-core.lock")
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for {
		entries, _ := os.ReadDir(fds)
		for _, entry := range entries {
			target, err := os.Readlink(filepath.Join(fds, entry.Name()))
			if err == nil && target == lock {
				return
			}
		}
		select {
		case line := <-c.ready:
			t.Fatalf("replacement became ready before opening the lease: %q", line)
		case <-deadline.C:
			t.Fatal("replacement did not reach the lease")
		case <-poll.C:
		}
	}
}

func (c inheritedCoreFixture) waitExit(t *testing.T) {
	t.Helper()
	exit := make(chan error, 1)
	go func() { exit <- c.cmd.Wait() }()
	select {
	case err := <-exit:
		if err != nil {
			t.Fatalf("Core exit: %v", err)
		}
	case <-time.After(8 * time.Second):
		_ = c.cmd.Process.Kill()
		<-exit
		t.Fatal("Core did not exit")
	}
}
