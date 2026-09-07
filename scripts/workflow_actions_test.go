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
	workflowActionPattern = regexp.MustCompile(`^\s*(?:-\s+)?uses:\s*(["']?)([^@\s"']+)@([^\s#"']+)(["']?)(?:\s+#\s*(.+?))?\s*$`)
	fullSHAPattern        = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)
	versionCommentPattern = regexp.MustCompile(`^v\d+(?:[.\w-]*)?$`)
)

func parseWorkflowAction(line string) (action, ref, comment string, ok bool) {
	matches := workflowActionPattern.FindStringSubmatch(line)
	if matches == nil || matches[1] != matches[4] {
		return "", "", "", false
	}
	return matches[2], matches[3], matches[5], true
}

func TestWorkflowActionParsing(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef01234567"
	tests := []struct {
		name, line, action, ref, comment, classify string
		matched, external, fullSHA, versionComment bool
	}{
		{name: "pinned", line: "uses: actions/checkout@" + sha + " # v6", action: "actions/checkout", ref: sha, comment: "v6", classify: "actions/checkout", matched: true, external: true, fullSHA: true, versionComment: true},
		{name: "tag", line: "uses: actions/checkout@v6 # v6", action: "actions/checkout", ref: "v6", comment: "v6", classify: "actions/checkout", matched: true, external: true, versionComment: true},
		{name: "branch", line: "uses: owner/action@main # v1", action: "owner/action", ref: "main", comment: "v1", classify: "owner/action", matched: true, external: true, versionComment: true},
		{name: "wrong length SHA", line: "uses: actions/checkout@" + sha[:39] + " # v6", action: "actions/checkout", ref: sha[:39], comment: "v6", classify: "actions/checkout", matched: true, external: true, versionComment: true},
		{name: "non-hex SHA", line: "uses: actions/checkout@" + strings.Repeat("g", 40) + " # v6", action: "actions/checkout", ref: strings.Repeat("g", 40), comment: "v6", classify: "actions/checkout", matched: true, external: true, versionComment: true},
		{name: "missing comment", line: "uses: actions/checkout@" + sha, action: "actions/checkout", ref: sha, classify: "actions/checkout", matched: true, external: true, fullSHA: true},
		{name: "malformed version comment", line: "uses: actions/checkout@" + sha + " # release-6", action: "actions/checkout", ref: sha, comment: "release-6", classify: "actions/checkout", matched: true, external: true, fullSHA: true},
		{name: "list item", line: "- uses: actions/checkout@" + sha + " # v6", action: "actions/checkout", ref: sha, comment: "v6", classify: "actions/checkout", matched: true, external: true, fullSHA: true, versionComment: true},
		{name: "double quoted", line: "uses: \"actions/checkout@" + sha + "\" # v6", action: "actions/checkout", ref: sha, comment: "v6", classify: "actions/checkout", matched: true, external: true, fullSHA: true, versionComment: true},
		{name: "single quoted", line: "uses: 'actions/checkout@" + sha + "' # v6", action: "actions/checkout", ref: sha, comment: "v6", classify: "actions/checkout", matched: true, external: true, fullSHA: true, versionComment: true},
		{name: "local", line: "uses: ./local-action", classify: "./local-action"},
		{name: "Docker", line: "uses: docker://alpine@sha256:abc", action: "docker://alpine", ref: "sha256:abc", classify: "docker://alpine", matched: true},
		{name: "malformed", line: "uses actions/checkout@v6"},
		{name: "missing closing quote", line: "uses: \"actions/checkout@" + sha + " # v6"},
		{name: "mismatched quotes", line: "uses: \"actions/checkout@" + sha + "' # v6"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			action, ref, comment, matched := parseWorkflowAction(tt.line)
			if matched != tt.matched {
				t.Fatalf("matched = %v, want %v", matched, tt.matched)
			}
			if matched && (action != tt.action || ref != tt.ref || comment != tt.comment) {
				t.Errorf("parsed = [%q %q %q], want [%q %q %q]", action, ref, comment, tt.action, tt.ref, tt.comment)
			}
			if matched && tt.external {
				if got := fullSHAPattern.MatchString(ref); got != tt.fullSHA {
					t.Errorf("fullSHAPattern.MatchString(%q) = %v, want %v", ref, got, tt.fullSHA)
				}
				if got := versionCommentPattern.MatchString(comment); got != tt.versionComment {
					t.Errorf("versionCommentPattern.MatchString(%q) = %v, want %v", comment, got, tt.versionComment)
				}
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
			action, ref, comment, ok := parseWorkflowAction(line)
			if !ok || !isExternalGitHubAction(action) {
				continue
			}
			if !fullSHAPattern.MatchString(ref) {
				t.Errorf("%s:%d: external action %q must use a full 40-hex commit SHA", path, lineNumber+1, action)
			}
			if !versionCommentPattern.MatchString(comment) {
				t.Errorf("%s:%d: external action %q must have an adjacent version comment", path, lineNumber+1, action)
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
