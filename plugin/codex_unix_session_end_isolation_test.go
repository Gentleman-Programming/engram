package plugin_test

import (
	"context"
	"errors"
	"flag"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

var codexSessionEndIsolationChild = flag.Bool("codex-session-end-isolation-child", false, "test-only: fail the slow SessionEnd case before consuming its request")

const codexSessionEndIsolationMarker = "CONTROLLED: early exit before consuming queued slow request"

// queueCodexSessionEndIsolationRequest bypasses only the child slow adapter call.
// Synchronize with the existing HTTP handler without consuming its queued request.
func queueCodexSessionEndIsolationRequest(t *testing.T, port string, queued, handlerDone <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	payload := `{"id":"slow","project":"end-project","directory":"isolation-fixture"}`
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"http://127.0.0.1:"+port+"/runtime-sessions/end", strings.NewReader(payload))
	if err != nil {
		t.Fatalf("isolation setup: create request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	transport := &http.Transport{Proxy: nil}
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	result := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		response, err := client.Do(request)
		if response != nil {
			_ = response.Body.Close()
		}
		result <- err
	}()
	defer func() {
		cancel()
		<-finished
		transport.CloseIdleConnections()
	}()
	select {
	case <-queued:
	case <-ctx.Done():
		t.Fatalf("isolation setup: request was not queued: %v", ctx.Err())
	case err := <-result:
		t.Fatalf("isolation setup: request ended before cancellation: %v", err)
	}
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("isolation setup: canceled request returned %v", err)
	}
	select {
	case <-handlerDone:
	case <-time.After(3 * time.Second):
		t.Fatal("isolation setup: canceled HTTP handler did not finish")
	}
}

func TestCodexUnixSessionEndCaseIsolation(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// This proves HTTP fixture isolation, not adapter timeout behavior: the
	// child queues a synthetic HTTP request in the slow case, then runs the real
	// empty-input adapter case. The selector excludes this parent (no recursion).
	child := exec.Command(binary,
		"-test.run=^TestCodexUnixSessionEndAdapter$/^(bounds_an_unresponsive_HTTP_request|empty_input_makes_no_request)$",
		"-test.v", "-test.count=1", "-test.timeout=60s",
		"-codex-session-end-isolation-child")
	output, err := child.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("isolation child exit = %v, want exit 1 from controlled failure:\n%s", err, output)
	}
	text := string(output)
	for _, want := range []string{
		codexSessionEndIsolationMarker,
		"--- FAIL: TestCodexUnixSessionEndAdapter/bounds_an_unresponsive_HTTP_request (",
		"--- PASS: TestCodexUnixSessionEndAdapter/empty_input_makes_no_request (",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("isolation child missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "--- FAIL: TestCodexUnixSessionEndAdapter/empty_input_makes_no_request (") {
		t.Errorf("slow-case failure contaminated the empty-input case:\n%s", text)
	}
}
