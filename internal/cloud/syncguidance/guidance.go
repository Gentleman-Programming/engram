package syncguidance

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/Gentleman-Programming/engram/v2/internal/cloud/constants"
	"github.com/Gentleman-Programming/engram/v2/internal/store"
)

const header = "Known repairable cloud sync failure detected."

type repairableError interface {
	IsRepairable() bool
}

type repairableMigrationError interface {
	IsRepairableMigrationFailure() bool
}

var projectInErrorPattern = regexp.MustCompile(`project "([^"]+)"`)

// IsRepairableCloudSyncError returns true for known cloud sync/upsert/canonicalization
// failures that can be diagnosed by the explicit cloud upgrade doctor/repair flow.
// It never mutates local or remote state.
func IsRepairableCloudSyncError(err error) bool {
	if err == nil {
		return false
	}
	var repairable repairableError
	if errors.As(err, &repairable) && repairable.IsRepairable() {
		return true
	}
	var migration repairableMigrationError
	if errors.As(err, &migration) && migration.IsRepairableMigrationFailure() {
		return true
	}

	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, strings.ToLower(header)) {
		return true
	}
	if strings.Contains(msg, "canonicalize cloud chunk:") || strings.Contains(msg, "canonicalize push chunk:") {
		return true
	}
	if strings.Contains(msg, "directory is required") {
		return true
	}
	if strings.Contains(msg, "legacy mutation") && strings.Contains(msg, "payload") {
		return true
	}
	if strings.Contains(msg, "payload") && containsAny(msg, "cloud", "sync", "mutation", "upsert", "canonicalize") {
		return true
	}
	if strings.Contains(msg, "upsert") && containsAny(msg, "cloud", "sync", "payload", "mutation", "canonicalize") {
		return true
	}
	return false
}

// AppendGuidance preserves the original message and appends deterministic,
// non-mutating recovery instructions for known repairable cloud sync failures.
func AppendGuidance(message, project string, err error) string {
	message = strings.TrimSpace(message)
	if message == "" && err != nil {
		message = err.Error()
	}
	if !IsRepairableCloudSyncError(err) || strings.Contains(message, header) {
		return message
	}
	return message + "\n\n" + Guidance(project)
}

func Guidance(project string) string {
	project = normalizeProject(project)
	return fmt.Sprintf(`%s
Run these commands, then retry sync:
  engram cloud upgrade doctor --project %s
  engram cloud upgrade repair --project %s --dry-run
  engram cloud upgrade repair --project %s --apply
  engram sync --cloud --project %s`, header, project, project, project, project)
}

// ProjectFromTargetKey derives the project suffix from cloud:<project> target keys.
func ProjectFromTargetKey(targetKey string) string {
	trimmed := strings.TrimSpace(targetKey)
	prefix := constants.TargetKeyCloud + ":"
	if strings.HasPrefix(trimmed, prefix) {
		project, _ := store.NormalizeProject(strings.TrimPrefix(trimmed, prefix))
		return strings.TrimSpace(project)
	}
	return ""
}

// ProjectFromError extracts the grouped autosync project from wrapped errors such
// as transport push project "proj-a". It returns an empty string when unknown.
func ProjectFromError(err error) string {
	if err == nil {
		return ""
	}
	matches := projectInErrorPattern.FindStringSubmatch(err.Error())
	if len(matches) != 2 {
		return ""
	}
	project, _ := store.NormalizeProject(matches[1])
	return strings.TrimSpace(project)
}

func normalizeProject(project string) string {
	project, _ = store.NormalizeProject(project)
	project = strings.TrimSpace(project)
	if project == "" {
		return "<project>"
	}
	return project
}

func containsAny(s string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}
