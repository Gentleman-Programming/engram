//go:build linux && amd64

package store

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"modernc.org/libc"
	libctime "modernc.org/libc/time"
)

const sqliteSleepChildEnv = "ENGRAM_SQLITE_SLEEP_CHILD"

// S3: exercise the driver's public sleep owner, not a Store mock or a retry
// wrapper. Only Linux/amd64 is included: its libc Timespec and syscall ABI were
// inspected. Each scenario has its own process so SIGURG cannot affect peers.
func TestSQLiteLibcSleepEINTR(t *testing.T) {
	if testing.Short() {
		t.Skip("isolated signal-delivery subprocess regression")
	}
	for _, mode := range []string{"relative-null-remainder", "absolute-deadline"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSQLiteLibcSleepEINTRChild$", "-test.count=1", "-test.timeout=4s")
			cmd.Env = append(os.Environ(), sqliteSleepChildEnv+"="+mode)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			if err != nil || ctx.Err() != nil {
				t.Fatalf("child exit=%v timeout=%v\nstdout:\n%s\nstderr:\n%s", err, ctx.Err(), stdout.String(), stderr.String())
			}
			if stderr.Len() != 0 {
				t.Fatalf("unexpected child stderr: %s", stderr.String())
			}
			if !strings.Contains(stdout.String(), "sleep-contract-ok "+mode+"\n") || !strings.Contains(stdout.String(), "PASS\n") {
				t.Fatalf("missing child assertions/PASS: %s", stdout.String())
			}
		})
	}
}

func TestSQLiteLibcSleepEINTRChild(t *testing.T) {
	mode := os.Getenv(sqliteSleepChildEnv)
	if mode == "" {
		t.Skip("helper requires parent subprocess guard")
	}
	if mode != "relative-null-remainder" && mode != "absolute-deadline" {
		t.Fatalf("invalid child mode %q", mode)
	}
	// Lock the goroutine making the libc call, not its parent or a t.Run caller.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	tid := syscall.Gettid()
	tls := libc.NewTLS()
	defer tls.Close()

	const duration = 300 * time.Millisecond
	req := libctime.Timespec{Ftv_nsec: int64(duration)}
	if mode == "absolute-deadline" {
		if rc := libc.Xclock_gettime(tls, libctime.CLOCK_MONOTONIC, uintptr(unsafe.Pointer(&req))); rc != 0 {
			t.Fatalf("clock_gettime rc=%d errno=%d", rc, *(*int32)(unsafe.Pointer(libc.X__errno_location(tls))))
		}
		req.Ftv_nsec += int64(duration)
		if req.Ftv_nsec >= int64(time.Second) {
			req.Ftv_sec++
			req.Ftv_nsec -= int64(time.Second)
		}
	}

	// Notify confirms the signal was handled, while tgkill targets precisely the
	// locked sleeper. /proc confirms it was in a sleep syscall before delivery;
	// successful tgkill alone would not prove interruption of the intended call.
	signals := make(chan os.Signal, 32)
	signal.Notify(signals, syscall.SIGURG)
	defer signal.Stop(signals)
	type injection struct {
		sent int
		err  error
	}
	injected := make(chan injection, 1)
	stop := make(chan struct{})
	go func() {
		result := injection{}
		defer func() { injected <- result }()
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		timeout := time.NewTimer(time.Second)
		defer timeout.Stop()
		for result.sent < 12 {
			select {
			case <-stop:
				return
			case <-timeout.C:
				result.err = fmt.Errorf("bounded injector timed out after %d deliveries", result.sent)
				return
			case <-ticker.C:
			}
			data, err := os.ReadFile(fmt.Sprintf("/proc/self/task/%d/syscall", tid))
			if err != nil {
				result.err = fmt.Errorf("observe sleeper syscall: %w", err)
				return
			}
			fields := strings.Fields(string(data))
			if len(fields) == 0 {
				result.err = fmt.Errorf("empty sleeper syscall observation")
				return
			}
			number, err := strconv.Atoi(fields[0])
			if err != nil || (number != syscall.SYS_NANOSLEEP && number != syscall.SYS_CLOCK_NANOSLEEP) {
				continue // The runtime may briefly run between interrupted calls.
			}
			if err := syscall.Tgkill(os.Getpid(), tid, syscall.SIGURG); err != nil {
				result.err = fmt.Errorf("tgkill sleeper: %w", err)
				return
			}
			result.sent++
		}
	}()

	start := time.Now() // Carries Go's monotonic reading, unlike wall-clock Unix time.
	var rc int32
	if mode == "relative-null-remainder" {
		rc = libc.Xnanosleep(tls, uintptr(unsafe.Pointer(&req)), 0)
	} else {
		rc = libc.Xclock_nanosleep(tls, libctime.CLOCK_MONOTONIC, libctime.TIMER_ABSTIME, uintptr(unsafe.Pointer(&req)), 0)
	}
	elapsed := time.Since(start)
	errno := *(*int32)(unsafe.Pointer(libc.X__errno_location(tls)))
	runtime.KeepAlive(&req)
	close(stop)
	result := <-injected
	if result.err != nil {
		t.Fatal(result.err)
	}
	// Even on baseline failure, require at least one successful targeted signal
	// and an observed handler delivery before reporting the sleep result.
	if result.sent == 0 {
		t.Fatalf("no signals delivered during sleep; rc=%d errno=%d elapsed=%s", rc, errno, elapsed)
	}
	select {
	case <-signals:
	case <-time.After(time.Second):
		t.Fatal("successful tgkill had no observed SIGURG delivery")
	}
	if rc != 0 {
		t.Fatalf("sleep rc=%d errno=%d elapsed=%s targeted-signals=%d", rc, errno, elapsed, result.sent)
	}
	if result.sent != 12 {
		t.Fatalf("sleep returned before repeated injection completed: sent=%d elapsed=%s", result.sent, elapsed)
	}
	// Absolute setup precedes start slightly; allow 10ms for that setup. The
	// upper bound also rejects restarting a full duration after the last signal
	// (~240ms), rather than retaining the original absolute deadline (~300ms).
	if elapsed < duration-10*time.Millisecond || elapsed > duration+200*time.Millisecond {
		t.Fatalf("deadline not preserved: elapsed=%s want=[%s,%s] signals=%d", elapsed, duration-10*time.Millisecond, duration+200*time.Millisecond, result.sent)
	}
	fmt.Printf("sleep-contract-ok %s\n", mode)
}
