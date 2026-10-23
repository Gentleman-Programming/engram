package main_test

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/mcp"
)

// Unlike go run, the executable preserves exit 2 without adding stderr text.
func assessmentBinary(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "release-candidate.exe")
	if output, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build public CLI: %v: %s", err, output)
	}
	return binary
}

func assessmentRun(t *testing.T, binary, root string, args ...string) (string, string, int) {
	t.Helper()
	before := candidateSnapshot(t, root)
	cmd := exec.Command(binary, append([]string{"-root", root}, args...)...)
	var out, diagnostic bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &diagnostic
	code := 0
	if err := cmd.Run(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			code = exit.ExitCode()
		} else {
			t.Fatalf("launch public CLI: %v", err)
		}
	}
	if !reflect.DeepEqual(before, candidateSnapshot(t, root)) {
		t.Fatal("assessment changed candidate files")
	}
	return out.String(), diagnostic.String(), code
}

func TestReleaseCandidateAssessment(t *testing.T) {
	binary := assessmentBinary(t)
	// Pin the live authority's limitation, not a fixture-authored passed report.
	live, err := mcp.AgentContractJSON()
	if err != nil {
		t.Fatal(err)
	}
	var authority struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(live, &authority); err != nil || authority.Status != "mcp-foundation" {
		t.Fatalf("revisit assessment boundary: live contract=%s error=%v", live, err)
	}
	for _, tc := range []struct {
		name, file, content, outcome, reason, message string
		code                                          int
	}{
		{"valid independent pair", "", "", "blocked", "positive-proof-unavailable", "live Go contract does not establish Pi parity or release approval", 2},
		{"observed invalid manifest", "plugin/pi/package.json", `{`, "rejected", "invalid-metadata", "invalid plugin/pi/package.json", 1},
		{"observed wrong pin", "internal/setup/setup.go", "package setup\nconst piGentleEngramPackage = \"npm:gentle-engram@3.0.0\"\n", "rejected", "invalid-metadata", "installer pin must match candidate version exactly", 1},
		{"missing manifest", "missing-manifest", "", "blocked", "metadata-unavailable", "cannot read plugin/pi/package.json", 2},
		{"missing installer", "missing-installer", "", "blocked", "metadata-unavailable", "cannot read internal/setup/setup.go", 2},
		// A directory at a file path deterministically fails ReadFile even as root
		// or on Windows, unlike permission-bit fixtures.
		{"unreadable manifest", "plugin/pi/package.json/child", "not a manifest", "blocked", "metadata-unavailable", "cannot read plugin/pi/package.json", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if tc.file != "missing-manifest" && tc.name != "unreadable manifest" {
				candidateWrite(t, root, "plugin/pi/package.json", `{"name":"gentle-engram","version":"0.5.0"}`)
			}
			if tc.file != "missing-installer" {
				candidateWrite(t, root, "internal/setup/setup.go", "package setup\nconst piGentleEngramPackage = \"npm:gentle-engram@0.5.0\"\n")
			}
			if tc.content != "" {
				candidateWrite(t, root, tc.file, tc.content)
			}
			want := `{"schemaVersion":1,"version":"3.0.0","piVersion":"0.5.0","outcome":"` + tc.outcome + `","reason":{"code":"` + tc.reason + `","message":"` + tc.message + `"}}` + "\n"
			args := []string{"-assess", "-core-ref", "refs/tags/v3.0.0", "-pi-ref", "refs/tags/pi-v0.5.0"}
			for attempt := 0; attempt < 2; attempt++ {
				out, diagnostic, code := assessmentRun(t, binary, root, args...)
				if out != want || diagnostic != "" || code != tc.code {
					t.Fatalf("exit=%d stdout=%q stderr=%q; want exit=%d stdout=%q empty stderr", code, out, diagnostic, tc.code, want)
				}
				var report struct {
					SchemaVersion int                            `json:"schemaVersion"`
					Version       string                         `json:"version"`
					PiVersion     string                         `json:"piVersion"`
					Outcome       string                         `json:"outcome"`
					Reason        struct{ Code, Message string } `json:"reason"`
				}
				if err := json.Unmarshal([]byte(out), &report); err != nil || report.SchemaVersion != 1 || report.Version != "3.0.0" || report.PiVersion != "0.5.0" || report.Outcome != tc.outcome || report.Reason.Code != tc.reason || report.Reason.Message != tc.message {
					t.Fatalf("invalid assessment fields: %s (%v)", out, err)
				}
			}
		})
	}
}

func TestReleaseCandidateAssessmentFlagBoundaries(t *testing.T) {
	binary := assessmentBinary(t)
	root := candidateFixture(t, "1.2.3")
	for _, flag := range []string{"-passed", "-evidence", "-status", "-contract"} {
		t.Run(flag, func(t *testing.T) {
			args := append([]string{"-assess"}, candidateRefs("1.2.3")...)
			args = append(args, flag, `{"status":"passed"}`)
			out, diagnostic, code := assessmentRun(t, binary, root, args...)
			if out != "" || diagnostic != "flag provided but not defined: "+flag+"\n" || code != 1 {
				t.Fatalf("caller evidence override: exit=%d stdout=%q stderr=%q", code, out, diagnostic)
			}
		})
	}
	// Presence matters, including an explicitly empty tarball flag. Standalone
	// tarball validation and default exporter already have dedicated tests.
	args := append([]string{"-assess"}, candidateRefs("1.2.3")...)
	args = append(args, "-pi-tarball", "")
	out, diagnostic, code := assessmentRun(t, binary, root, args...)
	if out != "" || diagnostic != "-assess and -pi-tarball cannot be combined\n" || code != 1 {
		t.Fatalf("mixed modes: exit=%d stdout=%q stderr=%q", code, out, diagnostic)
	}
	// Explicit opt-out must be byte-for-byte identical to the legacy export.
	want, wantDiagnostic, wantCode := assessmentRun(t, binary, root, candidateRefs("1.2.3")...)
	args = append([]string{"-assess=false"}, candidateRefs("1.2.3")...)
	out, diagnostic, code = assessmentRun(t, binary, root, args...)
	if code != 0 || code != wantCode || diagnostic != "" || diagnostic != wantDiagnostic || out != want {
		t.Fatalf("explicit opt-out changed legacy output: exit=%d stdout=%q stderr=%q", code, out, diagnostic)
	}
}
