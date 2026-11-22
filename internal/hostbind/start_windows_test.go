//go:build windows

package hostbind

import (
	"testing"

	"golang.org/x/sys/windows"
)

func TestProcessStartRejectsTerminatedProcessWithOpenHandle(t *testing.T) {
	cmd, input := startHostbindExitFixture(t)
	pid, dir := cmd.Process.Pid, t.TempDir()
	// Keep the process object open after exec.Cmd releases its own handle.
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = windows.CloseHandle(handle) })
	if err := Publish(dir, pid, "fixture"); err != nil {
		t.Fatalf("publish live binding: %v", err)
	}
	if err := input.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	assertExitedHostBinding(t, dir, pid)
}
