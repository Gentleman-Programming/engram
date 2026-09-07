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
