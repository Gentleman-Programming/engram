package engram_test

import (
	"os"
	"strings"
	"testing"
)

// These contracts inspect trusted workflow text; they do not exercise Actions,
// registry permissions, candidate code, or the hosted toolchain.
func TestReleaseStaging(t *testing.T) {
	raw, err := os.ReadFile(".github/workflows/stage-release-candidate.yml")
	if err != nil {
		t.Fatalf("manual staging workflow required: %v", err)
	}
	w := strings.ReplaceAll(string(raw), "\r\n", "\n")
	t.Run("manual-read-only", func(t *testing.T) {
		for _, want := range []string{"on:\n  workflow_dispatch:", "permissions:\n  contents: read", "persist-credentials: false", "workflow_sha: ${{ github.workflow_sha }}"} {
			if !strings.Contains(w, want) {
				t.Errorf("missing %q", want)
			}
		}
		for _, forbidden := range []string{"  push:", "  pull_request", "  schedule:", "id-token:", "secrets.", "contents: write", "npm publish", "dist-tag", "goreleaser", "codesign", "npm install", "npm ci"} {
			if strings.Contains(w, forbidden) {
				t.Errorf("forbidden staging capability %q", forbidden)
			}
		}
	})
	t.Run("validate-before-candidate", func(t *testing.T) {
		positions := []string{"- name: Validate inputs", "- name: Checkout candidate", "- name: Build unsigned binaries", "- name: Pack Pi locally", "- name: Manifest", "- name: Upload"}
		previous := -1
		for _, marker := range positions {
			at := strings.Index(w, marker)
			if at <= previous {
				t.Errorf("missing or out of order %q", marker)
			}
			previous = at
		}
		for _, want := range []string{"SOURCE_SHA: ${{ inputs.source_sha }}", "CORE_VERSION: ${{ inputs.core_version }}", "^[0-9a-f]{40}$", "^(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)$", "path: candidate", "ref: ${{ inputs.source_sha }}", "path: policy"} {
			if !strings.Contains(w, want) {
				t.Errorf("missing input/isolation guard %q", want)
			}
		}
		// Expressions belong in env/with, never interpolated into a shell script.
		inRun := false
		for _, line := range strings.Split(w, "\n") {
			if strings.HasPrefix(line, "        run: |") {
				inRun = true
				continue
			}
			if inRun && len(strings.TrimSpace(line)) > 0 && !strings.HasPrefix(line, "          ") {
				inRun = false
			}
			if inRun && strings.Contains(line, "${{") {
				t.Errorf("expression in script: %s", line)
			}
		}
	})
	t.Run("job-env-output-context", func(t *testing.T) {
		const marker = "\n    env:\n"
		if strings.Count(w, marker) != 1 {
			t.Fatal("exactly one job-level env block required")
		}
		_, env, _ := strings.Cut(w, marker)
		end := strings.Index(env, "\n    steps:")
		if end < 0 {
			t.Fatal("job-level env must end before steps")
		}
		env = env[:end]
		if strings.Contains(env, "runner.") {
			t.Error("runner context is unavailable in job-level env")
		}
		if !strings.Contains(env, "      OUT: ${{ github.workspace }}/staging") {
			t.Error("job output must use github.workspace staging outside candidate checkout")
		}
	})
	t.Run("build-and-package-evidence", func(t *testing.T) {
		for _, want := range []string{"go-version: \"1.26.10\"", "GOTOOLCHAIN: local", "CGO_ENABLED: '0'", "linux darwin windows", "amd64 arm64", "-mod=readonly", "-trimpath", "-buildvcs=true", "-X main.version=", "go version -m", "vcs.revision=", "vcs.modified=false", "git status --porcelain", "--untracked-files=all", "npm pack --ignore-scripts --json --pack-destination", "sha256", "sha512", "workflow_sha", "candidate_sha", "run_attempt"} {
			if !strings.Contains(w, want) {
				t.Errorf("missing build/package evidence %q", want)
			}
		}
		if strings.Count(w, "git status --porcelain") < 2 {
			t.Error("clean source required before and after packaging")
		}
	})
	t.Run("scoped-success-artifact", func(t *testing.T) {
		const marker = "      - name: Upload\n"
		if strings.Count(w, marker) != 1 {
			t.Fatal("exactly one named Upload step required")
		}
		_, upload, _ := strings.Cut(w, marker)
		if next := strings.Index(upload, "\n      - "); next >= 0 {
			upload = upload[:next]
		}
		for _, want := range []string{"actions/upload-artifact@cf430e030ddbb5b0abf93d22962f4752f3646cd9 # v7", "if: success()", "if-no-files-found: error", "overwrite: false", "retention-days: 7", "candidate-${{ github.run_id }}-${{ github.run_attempt }}", "${{ github.workspace }}/staging/"} {
			if !strings.Contains(upload, want) {
				t.Errorf("missing upload boundary %q", want)
			}
		}
		for _, forbidden := range []string{"path: .\n", "path: candidate\n", "include-hidden-files: true", "if: always()"} {
			if strings.Contains(upload, forbidden) {
				t.Errorf("unsafe upload %q", forbidden)
			}
		}
	})
}
