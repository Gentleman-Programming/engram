package scripts

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

var (
	workflowActionPattern = regexp.MustCompile(`^\s*(?:-\s+)?uses:\s*([^@\s]+)@([^\s#]+)(?:\s+#\s*(.+?))?\s*$`)
	fullSHAPattern        = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)
	versionCommentPattern = regexp.MustCompile(`^v\d+(?:[.\w-]*)?$`)
)

func TestWorkflowActionParsing(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef01234567"
	tests := []struct {
		name, line, action, ref, comment, classify string
		matched, external                          bool
	}{
		{name: "pinned", line: "uses: actions/checkout@" + sha + " # v6", action: "actions/checkout", ref: sha, comment: "v6", classify: "actions/checkout", matched: true, external: true},
		{name: "tag", line: "uses: actions/checkout@v6 # v6", action: "actions/checkout", ref: "v6", comment: "v6", classify: "actions/checkout", matched: true, external: true},
		{name: "branch", line: "uses: owner/action@main # v1", action: "owner/action", ref: "main", comment: "v1", classify: "owner/action", matched: true, external: true},
		{name: "missing comment", line: "uses: actions/checkout@" + sha, action: "actions/checkout", ref: sha, classify: "actions/checkout", matched: true, external: true},
		{name: "list item", line: "- uses: actions/checkout@" + sha + " # v6", action: "actions/checkout", ref: sha, comment: "v6", classify: "actions/checkout", matched: true, external: true},
		{name: "local", line: "uses: ./local-action", classify: "./local-action"},
		{name: "Docker", line: "uses: docker://alpine@sha256:abc", action: "docker://alpine", ref: "sha256:abc", classify: "docker://alpine", matched: true},
		{name: "malformed", line: "uses actions/checkout@v6"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matches := workflowActionPattern.FindStringSubmatch(tt.line)
			if (matches != nil) != tt.matched {
				t.Fatalf("matched = %v, want %v", matches != nil, tt.matched)
			}
			if matches != nil && (matches[1] != tt.action || matches[2] != tt.ref || matches[3] != tt.comment) {
				t.Errorf("groups = %q, want [%q %q %q]", matches[1:], tt.action, tt.ref, tt.comment)
			}
			if tt.classify != "" {
				if got := isExternalGitHubAction(tt.classify); got != tt.external {
					t.Errorf("isExternalGitHubAction(%q) = %v, want %v", tt.classify, got, tt.external)
				}
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

		for lineNumber, line := range strings.Split(string(content), "\n") {
			matches := workflowActionPattern.FindStringSubmatch(line)
			if matches == nil || !isExternalGitHubAction(matches[1]) {
				continue
			}
			if !fullSHAPattern.MatchString(matches[2]) {
				t.Errorf("%s:%d: external action %q must use a full 40-hex commit SHA", path, lineNumber+1, matches[1])
			}
			if !versionCommentPattern.MatchString(matches[3]) {
				t.Errorf("%s:%d: external action %q must have an adjacent version comment", path, lineNumber+1, matches[1])
			}
		}
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
