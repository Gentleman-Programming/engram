package engram_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestReleaseChecksModuleMetadataWithoutMutatingTaggedSources(t *testing.T) {
	root := releaseConfigRepoRoot(t)
	goreleaser := releaseConfigFile(t, filepath.Join(root, ".goreleaser.yaml"))
	if strings.Contains(goreleaser, "go mod tidy") {
		t.Fatal(".goreleaser.yaml must not run go mod tidy during a release")
	}

	workflow := releaseConfigFile(t, filepath.Join(root, ".github", "workflows", "release.yml"))
	if !releaseWorkflowChecksModuleMetadataInGoreleaserJob(workflow) {
		t.Fatal("release workflow must set up Go, check tidy module metadata, and run GoReleaser in order in the goreleaser job")
	}
}

func TestReleaseWorkflowChecksModuleMetadataInGoreleaserJob(t *testing.T) {
	tests := []struct {
		name     string
		workflow string
		want     bool
	}{
		{
			name: "valid same-job order",
			workflow: `jobs:
  goreleaser:
    steps:
      - name: Set up Go
      - name: Verify module metadata is tidy
        run: go mod tidy -diff
      - name: Run GoReleaser
`,
			want: true,
		},
		{
			name: "missing required step",
			workflow: `jobs:
  goreleaser:
    steps:
      - name: Set up Go
      - name: Run GoReleaser
`,
		},
		{
			name: "incorrect order",
			workflow: `jobs:
  goreleaser:
    steps:
      - name: Verify module metadata is tidy
        run: go mod tidy -diff
      - name: Set up Go
      - name: Run GoReleaser
`,
		},
		{
			name: "tidy check in a different job",
			workflow: `jobs:
  checks:
    steps:
      - name: Set up Go
      - name: Verify module metadata is tidy
        run: go mod tidy -diff
  goreleaser:
    steps:
      - name: Set up Go
      - name: Run GoReleaser
`,
		},
		{
			name: "tidy command appears only in a comment",
			workflow: `jobs:
  goreleaser:
    steps:
      - name: Set up Go
      - name: Verify module metadata is tidy
        # run: go mod tidy -diff
      - name: Run GoReleaser
`,
		},
		{
			name: "tidy command appears in a quoted non-run value",
			workflow: `jobs:
  goreleaser:
    steps:
      - name: Set up Go
      - name: Verify module metadata is tidy
        uses: "run: go mod tidy -diff"
      - name: Run GoReleaser
`,
		},
		{
			name: "tidy command belongs to a separate step",
			workflow: `jobs:
  goreleaser:
    steps:
      - name: Set up Go
      - name: Verify module metadata is tidy
      - name: Run a different command
        run: go mod tidy -diff
      - name: Run GoReleaser
`,
		},
		{
			name: "tidy command appears only in a nested script",
			workflow: `jobs:
  goreleaser:
    steps:
      - name: Set up Go
      - name: Verify module metadata is tidy
        run: |
          echo "run: go mod tidy -diff"
      - name: Run GoReleaser
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := releaseWorkflowChecksModuleMetadataInGoreleaserJob(tt.workflow); got != tt.want {
				t.Errorf("releaseWorkflowChecksModuleMetadataInGoreleaserJob() = %t, want %t", got, tt.want)
			}
		})
	}
}

func releaseWorkflowChecksModuleMetadataInGoreleaserJob(workflow string) bool {
	job := releaseWorkflowGoreleaserJob(workflow)
	steps, ok := releaseWorkflowGoreleaserSteps(job)
	if !ok {
		return false
	}

	setupGo, tidyCheck, goreleaserStep := -1, -1, -1
	for index, step := range steps {
		switch step.name {
		case "Set up Go":
			if setupGo >= 0 {
				return false
			}
			setupGo = index
		case "Verify module metadata is tidy":
			if tidyCheck >= 0 || step.run != "go mod tidy -diff" {
				return false
			}
			tidyCheck = index
		case "Run GoReleaser":
			if goreleaserStep >= 0 {
				return false
			}
			goreleaserStep = index
		}
	}

	return setupGo >= 0 && tidyCheck >= 0 && goreleaserStep >= 0 && setupGo < tidyCheck && tidyCheck < goreleaserStep
}

type releaseWorkflowStep struct {
	name string
	run  string
}

// releaseWorkflowGoreleaserSteps intentionally recognizes only this workflow's
// six-space step entries and eight-space direct fields.
func releaseWorkflowGoreleaserSteps(job string) ([]releaseWorkflowStep, bool) {
	const (
		stepsLine   = "    steps:"
		stepPrefix  = "      - "
		fieldPrefix = "        "
	)

	var steps []releaseWorkflowStep
	stepsStarted := false
	for _, rawLine := range strings.Split(job, "\n") {
		line := strings.TrimSuffix(rawLine, "\r")
		if !stepsStarted {
			if line == stepsLine {
				stepsStarted = true
			}
			continue
		}

		if line != "" && !strings.HasPrefix(line, "      ") {
			break
		}
		if strings.HasPrefix(line, stepPrefix) {
			steps = append(steps, releaseWorkflowStep{})
			if !releaseWorkflowStepField(&steps[len(steps)-1], line[len(stepPrefix):]) {
				return nil, false
			}
			continue
		}
		if len(steps) > 0 && strings.HasPrefix(line, fieldPrefix) && !strings.HasPrefix(line, fieldPrefix+" ") {
			if !releaseWorkflowStepField(&steps[len(steps)-1], line[len(fieldPrefix):]) {
				return nil, false
			}
		}
	}

	return steps, len(steps) > 0
}

func releaseWorkflowStepField(step *releaseWorkflowStep, field string) bool {
	key, value, found := strings.Cut(field, ":")
	if !found {
		return true
	}

	switch key {
	case "name":
		if step.name != "" {
			return false
		}
		step.name = strings.TrimSpace(value)
	case "run":
		if step.run != "" {
			return false
		}
		step.run = strings.TrimSpace(value)
	}
	return true
}

func releaseWorkflowGoreleaserJob(workflow string) string {
	lines := strings.Split(workflow, "\n")
	for start, line := range lines {
		if strings.TrimSuffix(line, "\r") != "  goreleaser:" {
			continue
		}
		end := start + 1
		for ; end < len(lines); end++ {
			line := strings.TrimSuffix(lines[end], "\r")
			if line != "" && !strings.HasPrefix(line, " ") {
				break
			}
			if strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "    ") {
				break
			}
		}
		return strings.Join(lines[start:end], "\n")
	}
	return ""
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
