//go:build windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
	"golang.org/x/sys/windows"
)

// Supply a freshly built binary explicitly. The job is only synthetic cleanup,
// not another Job-escape model: both concurrent public hooks recover normally.
func TestHookManagedLocalPublicRecovery(t *testing.T) {
	if os.Getenv("ENGRAM_RECOVERY_PUBLIC_HELPER") == "1" {
		if _, err := io.CopyN(io.Discard, os.Stdin, 1); err != nil {
			t.Fatal(err)
		}
		command := exec.Command(os.Getenv("ENGRAM_RECOVERY_PUBLIC_BINARY"), "hook", "claude-pre-tool-use")
		command.Stdin = strings.NewReader(os.Getenv("ENGRAM_RECOVERY_PUBLIC_INPUT"))
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		if err := command.Run(); err != nil {
			t.Fatal(err)
		}
		fmt.Println()
		return
	}
	binary := os.Getenv("ENGRAM_RECOVERY_PUBLIC_BINARY")
	if binary == "" {
		t.Skip("set ENGRAM_RECOVERY_PUBLIC_BINARY to freshly built engram.exe")
	}
	root := t.TempDir()
	data := filepath.Join(root, "data")
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(data, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "engram.db"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	expected, err := store.EnsureInstanceID(data)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	input, _ := json.Marshal(map[string]any{"session_id": "public-recovery-host", "cwd": home, "tool_name": "mcp__engram__mem_save", "tool_input": map[string]string{"session_id": "untrusted", "title": "not executed", "content": "not executed"}})
	// Public subprocesses receive no provider credentials or user configuration.
	// Only Windows runtime locations survive from the invoking environment.
	var env []string
	for _, key := range []string{"SystemRoot", "WINDIR", "SystemDrive"} {
		if value := os.Getenv(key); value != "" {
			env = append(env, key+"="+value)
		}
	}
	for _, key := range []string{"APPDATA", "LOCALAPPDATA", "TEMP", "TMP"} {
		dir := filepath.Join(home, key)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		env = append(env, key+"="+dir)
	}
	env = append(env, "HOME="+home, "USERPROFILE="+home, "XDG_CONFIG_HOME="+home, "PI_CODING_AGENT=", "ENGRAM_DATA_DIR="+data, "ENGRAM_PORT="+strconv.Itoa(port), "ENGRAM_HOOK_SESSION_CONFIRMATION_TIMEOUT=8s", "ENGRAM_RECOVERY_PUBLIC_HELPER=1", "ENGRAM_RECOVERY_PUBLIC_BINARY="+binary, "ENGRAM_RECOVERY_PUBLIC_INPUT="+string(input))
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = windows.TerminateJobObject(job, 0)
		defer func() {
			if err := windows.CloseHandle(job); err != nil {
				t.Errorf("close synthetic job handle: %v", err)
			}
		}()
		var accounting struct {
			Times                             [4]int64
			Faults, Total, Active, Terminated uint32
		}
		deadline := time.Now().Add(3 * time.Second)
		for {
			err := windows.QueryInformationJobObject(job, windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&accounting)), uint32(unsafe.Sizeof(accounting)), nil)
			if err != nil {
				t.Errorf("query synthetic cleanup: %v", err)
				return
			}
			if accounting.Active == 0 {
				return
			}
			if time.Now().After(deadline) {
				t.Error("synthetic job cleanup did not finish")
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	})
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		t.Fatal(err)
	}
	type hook struct {
		command     *exec.Cmd
		gate        io.WriteCloser
		out, stderr bytes.Buffer
		waited      bool
	}
	hooks := make([]*hook, 2)
	for i := range hooks {
		h := &hook{}
		hooks[i] = h
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		h.command = exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHookManagedLocalPublicRecovery$")
		h.command.Env = env
		h.command.Stdout = &h.out
		h.command.Stderr = &h.stderr
		h.command.WaitDelay = time.Second
		h.gate, err = h.command.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err = h.command.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = h.gate.Close()
			if !h.waited {
				_ = h.command.Process.Kill()
				_ = h.command.Wait()
				h.waited = true
			}
		})
		process, openErr := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(h.command.Process.Pid))
		if openErr != nil {
			t.Fatal(openErr)
		}
		assignErr := windows.AssignProcessToJobObject(job, process)
		_ = windows.CloseHandle(process)
		if assignErr != nil {
			t.Fatal(assignErr)
		}
	}
	// Gate both only after job assignment; no unowned server can be created.
	for _, h := range hooks {
		_, _ = h.gate.Write([]byte("g"))
		_ = h.gate.Close()
	}
	for _, h := range hooks {
		err = h.command.Wait()
		h.waited = true
		if err != nil {
			t.Fatalf("hook: %v stderr=%s stdout=%s", err, h.stderr.String(), h.out.String())
		}
		// The Go test helper appends PASS after the public CLI's JSON line.
		line, _, _ := strings.Cut(h.out.String(), "\n")
		var result struct {
			Output struct {
				Input    map[string]any `json:"updatedInput"`
				Decision string         `json:"permissionDecision"`
			} `json:"hookSpecificOutput"`
		}
		if err = json.Unmarshal([]byte(line), &result); err != nil {
			t.Fatalf("public output %q: %v", line, err)
		}
		if result.Output.Input["session_id"] != "public-recovery-host" || result.Output.Decision != "" {
			t.Fatalf("not authoritative binding: %s", line)
		}
	}
	client := &http.Client{Timeout: time.Second}
	response, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/health", port))
	if err != nil {
		t.Fatal(err)
	}
	var health struct {
		Instance string `json:"instance_id"`
	}
	err = json.NewDecoder(response.Body).Decode(&health)
	_ = response.Body.Close()
	if err != nil || response.StatusCode != 200 || health.Instance != expected {
		t.Fatalf("health=%+v err=%v", health, err)
	}
	// Two wrappers + two public hook processes + at most one serve child
	// per hook. A losing listener contender must exit, leaving one server.
	var accounting struct {
		User, Kernel, PeriodUser, PeriodKernel int64
		Faults, Total, Active, Terminated      uint32
	}
	settle := time.Now().Add(3 * time.Second)
	for {
		err = windows.QueryInformationJobObject(job, windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&accounting)), uint32(unsafe.Sizeof(accounting)), nil)
		if err != nil {
			t.Fatal(err)
		}
		if accounting.Active == 1 {
			break
		}
		if time.Now().After(settle) {
			t.Fatalf("contenders did not exit: %+v", accounting)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if accounting.Total < 5 || accounting.Total > 6 {
		t.Fatalf("unexpected launch count: %+v", accounting)
	}
	// Both hook processes have exited, but their adopted shared server survives.
	// Closing our job below is the only termination authority used for descendants.
	if err = windows.TerminateJobObject(job, 0); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		response, err = client.Get(fmt.Sprintf("http://127.0.0.1:%d/health", port))
		if err != nil {
			return
		}
		_ = response.Body.Close()
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("synthetic server did not stop after job termination")
}
