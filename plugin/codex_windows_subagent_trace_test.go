package plugin_test

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every invocation owns its script and trace. A late timed-out process cannot
// write another case's trace, and Cleanup retains partial evidence after Fatal.
func newTracedCodexSubagentCopy(t *testing.T, source []byte, expectCapture bool) (string, string) {
	t.Helper()
	pluginRoot := filepath.Join(t.TempDir(), "plugin root with spaces")
	adapterPath := filepath.Join(pluginRoot, "scripts", "subagent-stop.ps1")
	tracePath := filepath.Join(pluginRoot, "subagent-stages.log")
	if err := os.MkdirAll(filepath.Dir(adapterPath), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		trace, err := os.ReadFile(tracePath)
		if err != nil {
			t.Logf("SubagentStop phases unavailable (script entry or trace write not observed): %v", err)
			if !t.Failed() {
				t.Error("successful adapter must retain its phase trace")
			}
			return
		}
		t.Logf("test-owned SubagentStop phases:\n%s", trace)
		if t.Failed() {
			return
		}
		stages := []string{"entry", "stdin-consumed", "finalization", "completed"}
		if expectCapture {
			stages = []string{"entry", "stdin-consumed", "json-parse-start", "json-parse-end", "payload-inspection", "port-read", "port-validated", "project-call", "project-function-entry", "uri-encode-start", "uri-encode-end", "project-request", "project-response", "capture-request", "capture-response", "finalization", "completed"}
		}
		previous := -1
		for _, stage := range stages {
			marker := []byte(" " + stage + " elapsed_ms=")
			position := bytes.Index(trace, marker)
			if position <= previous || bytes.Count(trace, marker) != 1 {
				t.Errorf("adapter trace must contain %s exactly once in order", stage)
			}
			previous = position
		}
	})
	// Windows PowerShell may parse a BOM-less script as ANSI. Inject ASCII only
	// and decode the UTF-8 trace path, keeping payload/session data out of logs.
	prelude := "$engramTestClock = [Diagnostics.Stopwatch]::StartNew()\n" +
		"$engramTestTracePath = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('" + base64.StdEncoding.EncodeToString([]byte(tracePath)) + "'))\n" +
		"function Write-EngramTestSubagentStage($stage) { try { [IO.File]::AppendAllText($engramTestTracePath, " +
		"([DateTime]::UtcNow.ToString('o') + ' ' + $stage + ' elapsed_ms=' + $engramTestClock.Elapsed.TotalMilliseconds + [Environment]::NewLine)) } catch {} }\n" +
		"Write-EngramTestSubagentStage 'entry'\n"
	text := string(source)
	for _, point := range []struct {
		anchor, stage string
		before        bool
	}{
		{"$rawInput = [Console]::In.ReadToEnd()", "stdin-consumed", false},
		{"$payload = $rawInput | ConvertFrom-Json -ErrorAction Stop", "json-parse-start", true},
		{"$payload = $rawInput | ConvertFrom-Json -ErrorAction Stop", "json-parse-end", false},
		{"$output = [string]$payload.last_assistant_message", "payload-inspection", true},
		{"$portText = [Environment]::GetEnvironmentVariable('ENGRAM_PORT')", "port-read", true},
		{`$engramUrl = "http://127.0.0.1:$port"`, "port-validated", true},
		{"$project = Resolve-EngramProject", "project-call", true},
		{"if ([string]::IsNullOrWhiteSpace($Cwd))", "project-function-entry", true},
		{"$encodedCwd = [System.Uri]::EscapeDataString($Cwd)", "uri-encode-start", true},
		{"$encodedCwd = [System.Uri]::EscapeDataString($Cwd)", "uri-encode-end", false},
		{"$resolution = Invoke-RestMethod", "project-request", true},
		{"$projectProperty =", "project-response", true},
		{`Invoke-WebRequest -Uri "$engramUrl/observations/passive"`, "capture-request", true},
		{`Invoke-WebRequest -Uri "$engramUrl/observations/passive" -Method Post -ContentType 'application/json' -Body $body -UseBasicParsing -TimeoutSec $captureTimeoutSec -MaximumRedirection 0 -ErrorAction Stop *> $null`, "capture-response", false},
		{"    Write-EmptyHookResponse", "finalization", true},
		{"    Write-EmptyHookResponse", "completed", false},
	} {
		if strings.Count(text, point.anchor) != 1 {
			t.Fatalf("SubagentStop trace anchor %q must occur exactly once", point.anchor)
		}
		marker := "Write-EngramTestSubagentStage '" + point.stage + "'"
		replacement := point.anchor + "\n" + marker
		if point.before {
			replacement = marker + "\n" + point.anchor
		}
		text = strings.Replace(text, point.anchor, replacement, 1)
	}
	if err := os.WriteFile(adapterPath, []byte(prelude+text), 0o644); err != nil {
		t.Fatal(err)
	}
	return pluginRoot, adapterPath
}
