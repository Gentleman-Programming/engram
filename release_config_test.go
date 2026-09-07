package engram_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const expectedGoreleaserJob = `  goreleaser:
    runs-on: ubuntu-latest
    steps:
      - name: Checkout
        uses: actions/checkout@d23441a48e516b6c34aea4fa41551a30e30af803 # v6
        with:
          fetch-depth: 0

      - name: Set up Go
        uses: actions/setup-go@924ae3a1cded613372ab5595356fb5720e22ba16 # v6
        with:
          go-version: "1.25.10"

      - name: Verify module metadata is tidy
        run: go mod tidy -diff

      - name: Run GoReleaser
        uses: goreleaser/goreleaser-action@f06c13b6b1a9625abc9e6e439d9c05a8f2190e94 # v7
        with:
          version: "~> v2"
          args: release --clean
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
          HOMEBREW_TAP_TOKEN: ${{ secrets.HOMEBREW_TAP_TOKEN }}
`

func TestReleaseChecksModuleMetadataWithoutMutatingTaggedSources(t *testing.T) {
	root := releaseConfigRepoRoot(t)
	goreleaser := releaseConfigFile(t, filepath.Join(root, ".goreleaser.yaml"))
	if strings.Contains(goreleaser, "go mod tidy") {
		t.Fatal(".goreleaser.yaml must not run go mod tidy during a release")
	}

	workflow := releaseConfigFile(t, filepath.Join(root, ".github", "workflows", "release.yml"))
	job, ok := releaseWorkflowGoreleaserJob(workflow)
	if !ok {
		t.Fatal("release workflow must contain exactly one top-level goreleaser job")
	}
	if job != expectedGoreleaserJob {
		t.Errorf("unexpected goreleaser job; it must set up Go, run blocking `go mod tidy -diff`, then run GoReleaser without additional release steps\nwant:\n%s\ngot:\n%s", expectedGoreleaserJob, job)
	}
}

// releaseWorkflowGoreleaserJob extracts this repository's one top-level job.
// It deliberately is not a YAML parser: the exact job text is the contract.
func releaseWorkflowGoreleaserJob(workflow string) (string, bool) {
	const jobMarker = "\n  goreleaser:\n"
	workflow = strings.ReplaceAll(workflow, "\r\n", "\n")
	if strings.Count(workflow, jobMarker) != 1 {
		return "", false
	}

	start := strings.Index(workflow, jobMarker) + 1
	job := workflow[start:]
	for offset := len(jobMarker) - 1; ; {
		nextJob := strings.Index(job[offset:], "\n  ")
		if nextJob < 0 {
			break
		}
		nextJob += offset
		if nextJob+3 == len(job) || job[nextJob+3] != ' ' {
			job = job[:nextJob]
			break
		}
		offset = nextJob + 3
	}
	return job, true
}

func releaseConfigRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Dir(file)
}

func releaseConfigFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(content)
}
