package engram_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
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
			name: "valid quoted tidy preflight",
			workflow: `jobs:
  goreleaser:
    steps:
      - name: Set up Go
      - name: Verify module metadata is tidy
        run: "go mod tidy -diff"
      - name: Run GoReleaser
`,
			want: true,
		},
		{
			name: "duplicate Set up Go steps",
			workflow: `jobs:
  goreleaser:
    steps:
      - name: Set up Go
      - name: Set up Go
      - name: Verify module metadata is tidy
        run: go mod tidy -diff
      - name: Run GoReleaser
`,
			want: false,
		},
		{
			name: "duplicate Verify module metadata is tidy steps",
			workflow: `jobs:
  goreleaser:
    steps:
      - name: Set up Go
      - name: Verify module metadata is tidy
        run: go mod tidy -diff
      - name: Verify module metadata is tidy
        run: go mod tidy -diff
      - name: Run GoReleaser
`,
			want: false,
		},
		{
			name: "duplicate Run GoReleaser steps",
			workflow: `jobs:
  goreleaser:
    steps:
      - name: Set up Go
      - name: Verify module metadata is tidy
        run: go mod tidy -diff
      - name: Run GoReleaser
      - name: Run GoReleaser
`,
			want: false,
		},
		{
			name: "duplicate name field after empty value",
			workflow: `jobs:
  goreleaser:
    steps:
      - name:
        name: Set up Go
      - name: Verify module metadata is tidy
        run: go mod tidy -diff
      - name: Run GoReleaser
`,
			want: false,
		},
		{
			name: "duplicate run field after empty value",
			workflow: `jobs:
  goreleaser:
    steps:
      - name: Set up Go
      - name: Verify module metadata is tidy
        run:
        run: go mod tidy -diff
      - name: Run GoReleaser
`,
			want: false,
		},
		{
			name: "tidy check is skipped",
			workflow: `jobs:
  goreleaser:
    steps:
      - name: Set up Go
      - name: Verify module metadata is tidy
        if: false
        run: go mod tidy -diff
      - name: Run GoReleaser
`,
			want: false,
		},
		{
			name: "tidy check is non-blocking",
			workflow: `jobs:
  goreleaser:
    steps:
      - name: Set up Go
      - name: Verify module metadata is tidy
        continue-on-error: true
        run: go mod tidy -diff
      - name: Run GoReleaser
`,
			want: false,
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
			name: "mutating tidy command in an unrelated direct step",
			workflow: `jobs:
  goreleaser:
    steps:
      - name: Set up Go
      - name: Verify module metadata is tidy
        run: go mod tidy -diff
      - name: Mutate module metadata
        run: go mod tidy
      - name: Run GoReleaser
`,
		},
		{
			name: "mutating tidy command after an environment assignment",
			workflow: `jobs:
  goreleaser:
    steps:
      - name: Set up Go
      - name: Verify module metadata is tidy
        run: go mod tidy -diff
      - name: Mutate module metadata
        run: CI=1 go mod tidy
      - name: Run GoReleaser
`,
		},
		{
			name: "mutating tidy command through env",
			workflow: `jobs:
  goreleaser:
    steps:
      - name: Set up Go
      - name: Verify module metadata is tidy
        run: go mod tidy -diff
      - name: Mutate module metadata
        run: env CI=1 go mod tidy
      - name: Run GoReleaser
`,
		},
		{
			name: "mutating tidy command through sudo",
			workflow: `jobs:
  goreleaser:
    steps:
      - name: Set up Go
      - name: Verify module metadata is tidy
        run: go mod tidy -diff
      - name: Mutate module metadata
        run: sudo go mod tidy
      - name: Run GoReleaser
`,
		},
		{
			name: "mutating tidy command in a subshell",
			workflow: `jobs:
  goreleaser:
    steps:
      - name: Set up Go
      - name: Verify module metadata is tidy
        run: go mod tidy -diff
      - name: Mutate module metadata
        run: (go mod tidy)
      - name: Run GoReleaser
`,
		},
		{
			name: "mutating tidy command in a double-quoted direct step",
			workflow: `jobs:
  goreleaser:
    steps:
      - name: Set up Go
      - name: Verify module metadata is tidy
        run: go mod tidy -diff
      - name: Mutate module metadata
        run: "go mod tidy"
      - name: Run GoReleaser
`,
		},
		{
			name: "mutating tidy command in a single-quoted direct step",
			workflow: `jobs:
  goreleaser:
    steps:
      - name: Set up Go
      - name: Verify module metadata is tidy
        run: go mod tidy -diff
      - name: Mutate module metadata
        run: 'go mod tidy'
      - name: Run GoReleaser
`,
		},
		{
			name: "mutating tidy command with an inline YAML comment",
			workflow: `jobs:
  goreleaser:
    steps:
      - name: Set up Go
      - name: Verify module metadata is tidy
        run: go mod tidy -diff
      - name: Mutate module metadata
        run: 'go mod tidy' # mutates tagged sources
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
		{
			name: "mutating tidy command in a multiline script",
			workflow: `jobs:
  goreleaser:
    steps:
      - name: Set up Go
      - name: Verify module metadata is tidy
        run: go mod tidy -diff
      - name: Mutate module metadata
        run: |
          echo "checking module metadata"
          go mod tidy
      - name: Run GoReleaser
`,
		},
		{
			name: "mutating tidy command with diff in a shell comment",
			workflow: `jobs:
  goreleaser:
    steps:
      - name: Set up Go
      - name: Verify module metadata is tidy
        run: go mod tidy -diff
      - name: Mutate module metadata
        run: |
          go mod tidy # -diff
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
		if step.hasMutatingTidy {
			return false
		}
		switch step.name {
		case "Set up Go":
			if setupGo >= 0 {
				return false
			}
			setupGo = index
		case "Verify module metadata is tidy":
			if tidyCheck >= 0 || step.run != "go mod tidy -diff" || step.hasIf || step.hasContinueOnError {
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
	name               string
	run                string
	hasName            bool
	hasRun             bool
	hasIf              bool
	hasContinueOnError bool
	hasMutatingTidy    bool
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
	runBlockStep := -1
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
			runBlockStep = -1
			if !releaseWorkflowStepField(&steps[len(steps)-1], line[len(stepPrefix):]) {
				return nil, false
			}
			if releaseWorkflowRunBlock(steps[len(steps)-1].run) {
				runBlockStep = len(steps) - 1
			}
			continue
		}
		if len(steps) > 0 && strings.HasPrefix(line, fieldPrefix) && !strings.HasPrefix(line, fieldPrefix+" ") {
			runBlockStep = -1
			if !releaseWorkflowStepField(&steps[len(steps)-1], line[len(fieldPrefix):]) {
				return nil, false
			}
			if releaseWorkflowRunBlock(steps[len(steps)-1].run) {
				runBlockStep = len(steps) - 1
			}
			continue
		}
		if runBlockStep >= 0 && strings.HasPrefix(line, fieldPrefix+" ") {
			steps[runBlockStep].hasMutatingTidy = steps[runBlockStep].hasMutatingTidy || releaseWorkflowRunHasMutatingTidy(strings.TrimSpace(line))
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
		if step.hasName {
			return false
		}
		step.hasName = true
		step.name = strings.TrimSpace(value)
	case "run":
		if step.hasRun {
			return false
		}
		step.hasRun = true
		step.run = releaseWorkflowDirectScalar(value)
		step.hasMutatingTidy = releaseWorkflowRunHasMutatingTidy(step.run)
	case "if":
		if step.hasIf {
			return false
		}
		step.hasIf = true
	case "continue-on-error":
		if step.hasContinueOnError {
			return false
		}
		step.hasContinueOnError = true
	}
	return true
}

func releaseWorkflowDirectScalar(value string) string {
	value = strings.TrimSpace(releaseWorkflowWithoutInlineComment(value))
	if len(value) < 2 || value[0] != value[len(value)-1] {
		return value
	}

	switch value[0] {
	case '"':
		decoded, err := strconv.Unquote(value)
		if err == nil {
			return decoded
		}
	case '\'':
		return strings.ReplaceAll(value[1:len(value)-1], "''", "'")
	}
	return value
}

func releaseWorkflowWithoutInlineComment(value string) string {
	var quote byte
	for index := 0; index < len(value); index++ {
		switch quote {
		case '"':
			switch value[index] {
			case '\\':
				index++
			case '"':
				quote = 0
			}
		case '\'':
			if value[index] == '\'' {
				if index+1 < len(value) && value[index+1] == '\'' {
					index++
					continue
				}
				quote = 0
			}
		default:
			switch value[index] {
			case '"', '\'':
				quote = value[index]
			case '#':
				return value[:index]
			}
		}
	}
	return value
}

func releaseWorkflowRunBlock(run string) bool {
	run = strings.TrimSpace(run)
	return strings.HasPrefix(run, "|") || strings.HasPrefix(run, ">")
}

func releaseWorkflowRunHasMutatingTidy(run string) bool {
	for _, line := range strings.Split(run, "\n") {
		line = releaseWorkflowWithoutInlineComment(line)
		for _, command := range strings.FieldsFunc(line, func(r rune) bool {
			return r == ';' || r == '&' || r == '|'
		}) {
			fields := strings.Fields(command)
			for index := range fields {
				fields[index] = strings.Trim(fields[index], "()")
			}
			for len(fields) > 0 {
				if fields[0] == "" || fields[0] == "env" || fields[0] == "sudo" || strings.Contains(fields[0], "=") {
					fields = fields[1:]
					continue
				}
				break
			}
			if len(fields) < 3 || fields[0] != "go" || fields[1] != "mod" || fields[2] != "tidy" {
				continue
			}
			for _, argument := range fields[3:] {
				if argument == "-diff" {
					goto nextCommand
				}
			}
			return true

		nextCommand:
		}
	}
	return false
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
