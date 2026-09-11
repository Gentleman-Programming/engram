package scripts

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

var (
	fullSHAPattern        = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)
	versionCommentPattern = regexp.MustCompile(`^v\d+(?:[.\w-]*)?$`)
)

func TestWorkflowActionParsing(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef01234567"
	tests := []struct {
		name              string
		workflow          string
		wantErrors        []string
		wantErrorContains string
	}{
		{name: "pinned step action", workflow: workflowWithStep("actions/checkout@" + sha + " # v6")},
		{name: "double quoted step action", workflow: workflowWithStep("\"actions/checkout@" + sha + "\" # v6")},
		{name: "single quoted step action", workflow: workflowWithStep("'actions/checkout@" + sha + "' # v6")},
		{name: "pinned reusable workflow", workflow: "jobs:\n  call:\n    uses: owner/repo/.github/workflows/reusable.yml@" + sha + " # v1\n"},
		{name: "mutable reusable workflow", workflow: "jobs:\n  call:\n    uses: owner/repo/.github/workflows/reusable.yml@main # v1\n", wantErrors: []string{"line 3: external action \"owner/repo/.github/workflows/reusable.yml\" must use a full 40-hex commit SHA"}},
		{name: "local action", workflow: workflowWithStep("./local-action")},
		{name: "Docker action", workflow: workflowWithStep("docker://alpine@sha256:abc")},
		{name: "wrong length SHA", workflow: workflowWithStep("actions/checkout@" + sha[:39] + " # v6"), wantErrors: []string{"line 4: external action \"actions/checkout\" must use a full 40-hex commit SHA"}},
		{name: "non-hex SHA", workflow: workflowWithStep("actions/checkout@" + strings.Repeat("g", 40) + " # v6"), wantErrors: []string{"line 4: external action \"actions/checkout\" must use a full 40-hex commit SHA"}},
		{name: "missing version comment", workflow: workflowWithStep("actions/checkout@" + sha), wantErrors: []string{"line 4: external action \"actions/checkout\" must have an adjacent version comment"}},
		{name: "malformed version comment", workflow: workflowWithStep("actions/checkout@" + sha + " # release-6"), wantErrors: []string{"line 4: external action \"actions/checkout\" must have an adjacent version comment"}},
		{name: "scalar anchor", workflow: workflowWithStep("&checkout actions/checkout@" + sha + " # v6")},
		{name: "scalar alias", workflow: "action: &checkout actions/checkout@" + sha + "\njobs:\n  test:\n    steps:\n      - uses: *checkout # v6\n"},
		{name: "scalar alias requires an occurrence comment", workflow: "action: &checkout actions/checkout@" + sha + " # v6\njobs:\n  test:\n    steps:\n      - uses: *checkout\n", wantErrors: []string{"line 5: external action \"actions/checkout\" must have an adjacent version comment"}},
		{name: "mutable scalar alias", workflow: "action: &checkout actions/checkout@main\njobs:\n  test:\n    steps:\n      - uses: *checkout # v6\n", wantErrors: []string{"line 5: external action \"actions/checkout\" must use a full 40-hex commit SHA"}},
		{name: "non-scalar uses value", workflow: workflowWithStep("{action: actions/checkout@" + sha + "}"), wantErrors: []string{"line 4: uses value must resolve to a scalar, got mapping"}},
		{name: "unrecognized uses value", workflow: workflowWithStep("checkout # v6"), wantErrors: []string{"line 4: uses value \"checkout\" is not a supported local, Docker, or GitHub action reference"}},
		{name: "unresolved alias", workflow: workflowWithStep("*missing"), wantErrorContains: "parse workflow YAML:"},
		{name: "malformed YAML", workflow: "jobs:\n  test: [\n", wantErrorContains: "parse workflow YAML:"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := errorStrings(validateWorkflowActions([]byte(tt.workflow)))
			if tt.wantErrorContains != "" {
				if len(got) != 1 || !strings.Contains(got[0], tt.wantErrorContains) {
					t.Fatalf("errors = %v, want one error containing %q", got, tt.wantErrorContains)
				}
				return
			}
			if diff := compareStringSlices(got, tt.wantErrors); diff != "" {
				t.Fatal(diff)
			}
		})
	}
}

func TestWorkflowExternalActionsArePinned(t *testing.T) {
	workflowDir := workflowDirectory(t)
	entries, err := os.ReadDir(workflowDir)
	if err != nil {
		t.Fatalf("read workflow directory: %v", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || (filepath.Ext(entry.Name()) != ".yml" && filepath.Ext(entry.Name()) != ".yaml") {
			continue
		}

		path := filepath.Join(workflowDir, entry.Name())
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}

		for _, err := range validateWorkflowActions(content) {
			t.Errorf("%s: %v", path, err)
		}
	}
}

func TestPRValidationAndTransientArtifactWorkflowContracts(t *testing.T) {
	prCheckPath := filepath.Join(workflowDirectory(t), "pr-check.yml")
	prCheckContent, err := os.ReadFile(prCheckPath)
	if err != nil {
		t.Fatalf("read %s: %v", prCheckPath, err)
	}

	prCheck := strings.ReplaceAll(string(prCheckContent), "\r\n", "\n")
	for _, required := range []string{
		"pull_request:",
		"types: [opened, edited, labeled, unlabeled, synchronize]",
		"check-issue-reference:",
		"name: Check Issue Reference",
		"check-issue-approved:",
		"name: Check Issue Has status:approved",
	} {
		if !strings.Contains(prCheck, required) {
			t.Errorf("%s does not contain %q", prCheckPath, required)
		}
	}
	for _, forbidden := range []string{"pull_request_target:", "check-transient-artifacts:"} {
		if strings.Contains(prCheck, forbidden) {
			t.Errorf("%s must not contain %q", prCheckPath, forbidden)
		}
	}
	labelCheckPath := filepath.Join(workflowDirectory(t), "pr-label-check.yml")
	labelCheckContent, err := os.ReadFile(labelCheckPath)
	if err != nil {
		t.Fatalf("read %s: %v", labelCheckPath, err)
	}
	labelCheck := strings.ReplaceAll(string(labelCheckContent), "\r\n", "\n")
	for _, required := range []string{"pull_request_target:", "check-label-policy:", "name: Check PR Label Policy", "ref: ${{ github.event.pull_request.base.sha }}", "persist-credentials: false"} {
		if !strings.Contains(labelCheck, required) {
			t.Errorf("%s does not contain %q", labelCheckPath, required)
		}
	}

	artifactWorkflowPath := filepath.Join(workflowDirectory(t), "transient-artifacts.yml")
	artifactWorkflowContent, err := os.ReadFile(artifactWorkflowPath)
	if err != nil {
		t.Fatalf("read %s: %v", artifactWorkflowPath, err)
	}

	artifactWorkflow := strings.ReplaceAll(string(artifactWorkflowContent), "\r\n", "\n")
	for _, required := range []string{
		"pull_request_target:",
		"types: [opened, edited, labeled, unlabeled, synchronize, reopened]",
		"permissions:\n  contents: read\n  pull-requests: read",
		"check-transient-artifacts:",
		"name: Check PR Has No Transient Artifacts",
		"actions/checkout@d23441a48e516b6c34aea4fa41551a30e30af803 # v6",
		"ref: ${{ github.event.pull_request.base.sha }}",
		"persist-credentials: false",
		"actions/github-script@ed597411d8f924073f98dfc5c65a23a2325f34cd # v8",
		".github/scripts/transient-artifacts.mjs",
		"({ findTransientArtifacts, listPullRequestFiles } = await import(",
		"await listPullRequestFiles(github, {",
		"owner: context.repo.owner",
		"repo: context.repo.repo",
		"pullNumber: prNumber",
		"findTransientArtifacts(files)",
		"core.setFailed('❌ Could not load the trusted transient artifact policy: ' + err.message);",
		"core.setFailed('❌ Could not enumerate PR files: ' + err.message);",
	} {
		if !strings.Contains(artifactWorkflow, required) {
			t.Errorf("%s does not contain %q", artifactWorkflowPath, required)
		}
	}
	for _, forbidden := range []string{"github.event.pull_request.head", "write", "gh pr diff", "status: 'modified'"} {
		if strings.Contains(artifactWorkflow, forbidden) {
			t.Errorf("%s must not contain %q", artifactWorkflowPath, forbidden)
		}
	}

	helperPath := filepath.Join(filepath.Dir(filepath.Dir(workflowDirectory(t))), ".github", "scripts", "transient-artifacts.mjs")
	helper, err := os.ReadFile(helperPath)
	if err != nil {
		t.Fatalf("read %s: %v", helperPath, err)
	}
	if !strings.Contains(string(helper), "const files = await github.paginate(github.rest.pulls.listFiles, {") {
		t.Errorf("%s does not use GitHub pagination for PR file enumeration", helperPath)
	}
}

func workflowDirectory(t *testing.T) string {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate workflow directory: runtime.Caller did not return the test source path")
	}

	workflowDir := filepath.Join(filepath.Dir(filepath.Dir(sourceFile)), ".github", "workflows")
	if info, err := os.Stat(workflowDir); err != nil {
		t.Fatalf("locate workflow directory from test source %q: %v", sourceFile, err)
	} else if !info.IsDir() {
		t.Fatalf("locate workflow directory from test source %q: %q is not a directory", sourceFile, workflowDir)
	}
	return workflowDir
}

func isExternalGitHubAction(action string) bool {
	return !strings.HasPrefix(action, "./") && !strings.HasPrefix(action, "docker://") && strings.Contains(action, "/")
}

func workflowWithStep(uses string) string {
	return "jobs:\n  test:\n    steps:\n      - uses: " + uses + "\n"
}

func validateWorkflowActions(content []byte) []error {
	var document yaml.Node
	if err := yaml.Unmarshal(content, &document); err != nil {
		return []error{fmt.Errorf("parse workflow YAML: %w", err)}
	}
	if len(document.Content) != 1 {
		return nil
	}

	var violations []error
	for _, jobs := range mappingValues(document.Content[0], "jobs") {
		jobs, err := resolveYAMLNode(jobs)
		if err != nil || jobs.Kind != yaml.MappingNode {
			continue
		}
		for index := 1; index < len(jobs.Content); index += 2 {
			job := jobs.Content[index]
			job, err = resolveYAMLNode(job)
			if err != nil || job.Kind != yaml.MappingNode {
				continue
			}

			for _, uses := range mappingValues(job, "uses") {
				violations = append(violations, validateWorkflowUses(uses)...)
			}
			for _, steps := range mappingValues(job, "steps") {
				steps, err = resolveYAMLNode(steps)
				if err != nil || steps.Kind != yaml.SequenceNode {
					continue
				}
				for _, step := range steps.Content {
					step, err = resolveYAMLNode(step)
					if err != nil || step.Kind != yaml.MappingNode {
						continue
					}
					for _, uses := range mappingValues(step, "uses") {
						violations = append(violations, validateWorkflowUses(uses)...)
					}
				}
			}
		}
	}
	return violations
}

func mappingValues(node *yaml.Node, key string) []*yaml.Node {
	if node.Kind != yaml.MappingNode {
		return nil
	}

	var values []*yaml.Node
	for index := 0; index+1 < len(node.Content); index += 2 {
		if node.Content[index].Value == key {
			values = append(values, node.Content[index+1])
		}
	}
	return values
}

func resolveYAMLNode(node *yaml.Node) (*yaml.Node, error) {
	visited := make(map[*yaml.Node]struct{})
	for node != nil && node.Kind == yaml.AliasNode {
		if _, ok := visited[node]; ok {
			return nil, fmt.Errorf("alias cycle")
		}
		visited[node] = struct{}{}
		node = node.Alias
	}
	if node == nil {
		return nil, fmt.Errorf("unresolved alias")
	}
	return node, nil
}

func validateWorkflowUses(uses *yaml.Node) []error {
	resolved, err := resolveYAMLNode(uses)
	if err != nil {
		return []error{fmt.Errorf("line %d: uses value must resolve to a scalar: %v", uses.Line, err)}
	}
	if resolved.Kind != yaml.ScalarNode {
		return []error{fmt.Errorf("line %d: uses value must resolve to a scalar, got %s", uses.Line, yamlNodeKind(resolved.Kind))}
	}

	value := resolved.Value
	if strings.HasPrefix(value, "./") || strings.HasPrefix(value, "docker://") {
		return nil
	}
	if !isExternalGitHubAction(value) {
		return []error{fmt.Errorf("line %d: uses value %q is not a supported local, Docker, or GitHub action reference", uses.Line, value)}
	}

	action, ref, ok := strings.Cut(value, "@")
	if !ok || action == "" || ref == "" {
		return []error{fmt.Errorf("line %d: external action %q must use the form owner/action@ref", uses.Line, value)}
	}

	var violations []error
	if !fullSHAPattern.MatchString(ref) {
		violations = append(violations, fmt.Errorf("line %d: external action %q must use a full 40-hex commit SHA", uses.Line, action))
	}
	if !versionCommentPattern.MatchString(strings.TrimSpace(strings.TrimPrefix(uses.LineComment, "#"))) {
		violations = append(violations, fmt.Errorf("line %d: external action %q must have an adjacent version comment", uses.Line, action))
	}
	return violations
}

func yamlNodeKind(kind yaml.Kind) string {
	switch kind {
	case yaml.DocumentNode:
		return "document"
	case yaml.SequenceNode:
		return "sequence"
	case yaml.MappingNode:
		return "mapping"
	case yaml.ScalarNode:
		return "scalar"
	case yaml.AliasNode:
		return "alias"
	default:
		return "unknown"
	}
}

func errorStrings(errs []error) []string {
	values := make([]string, len(errs))
	for index, err := range errs {
		values[index] = err.Error()
	}
	return values
}

func compareStringSlices(got, want []string) string {
	if len(got) != len(want) {
		return fmt.Sprintf("errors = %v, want %v", got, want)
	}
	for index := range got {
		if got[index] != want[index] {
			return fmt.Sprintf("errors = %v, want %v", got, want)
		}
	}
	return ""
}
