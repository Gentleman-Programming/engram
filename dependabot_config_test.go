package engram_test

import (
	"os"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

// Capture the pre-existing policy independently of the new npm entry.
const dependabotExistingPolicy = `version: 2

updates:
  # Go module dependencies
  - package-ecosystem: "gomod"
    directory: "/"
    schedule:
      interval: "weekly"
      day: "monday"
      time: "09:00"
      timezone: "UTC"
    open-pull-requests-limit: 5
    labels:
      - "type:chore"
    commit-message:
      prefix: "chore(deps)"
    reviewers:
      - "Gentleman-Programming"

  # GitHub Actions
  - package-ecosystem: "github-actions"
    directory: "/"
    schedule:
      interval: "weekly"
      day: "monday"
      time: "09:00"
      timezone: "UTC"
    open-pull-requests-limit: 3
    labels:
      - "type:chore"
    commit-message:
      prefix: "chore(ci)"
    reviewers:
      - "Gentleman-Programming"

  # Docker image dependencies
  - package-ecosystem: "docker"
    directory: "/docker/cloud"
    schedule:
      interval: "weekly"
      day: "monday"
      time: "09:00"
      timezone: "UTC"
    open-pull-requests-limit: 5
    labels:
      - "type:chore"
    commit-message:
      prefix: "chore(deps)"
    reviewers:
      - "Gentleman-Programming"
`

const dependabotObsidianPolicy = `updates:
  # Obsidian npm dependencies
  - package-ecosystem: "npm"
    directory: "/plugin/obsidian"
    schedule:
      interval: "weekly"
      day: "monday"
      time: "09:00"
      timezone: "UTC"
    open-pull-requests-limit: 3
    cooldown:
      default-days: 3
    labels:
      - "type:chore"
    commit-message:
      prefix: "chore(deps)"
`

func readDependabotPolicy(t *testing.T, content []byte) map[string]any {
	t.Helper()
	var policy map[string]any
	if err := yaml.Unmarshal(content, &policy); err != nil {
		t.Fatalf("parse Dependabot policy: %v", err)
	}
	return policy
}

func currentDependabotPolicy(t *testing.T) map[string]any {
	t.Helper()
	content, err := os.ReadFile(".github/dependabot.yml")
	if err != nil {
		t.Fatal(err)
	}
	return readDependabotPolicy(t, content)
}

func TestDependabotObsidianNpmPolicy(t *testing.T) {
	policy := currentDependabotPolicy(t)
	updates, ok := policy["updates"].([]any)
	if !ok {
		t.Fatal("Dependabot updates must be a list")
	}
	var npmUpdates []any
	for _, update := range updates {
		entry, ok := update.(map[string]any)
		if !ok {
			t.Fatal("each Dependabot update must be a mapping")
		}
		if entry["package-ecosystem"] == "npm" {
			npmUpdates = append(npmUpdates, update)
		}
	}
	expected := readDependabotPolicy(t, []byte(dependabotObsidianPolicy))["updates"]
	if !reflect.DeepEqual(npmUpdates, expected) {
		t.Fatalf("npm policy must cover only Obsidian with bounded scheduling, canonical labels and a three-day version-update cooldown\nwant: %#v\ngot: %#v", expected, npmUpdates)
	}
	for _, path := range []string{"plugin/obsidian/package.json", "plugin/obsidian/package-lock.json"} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("configured npm input %s: %v", path, err)
		}
	}
}

func TestDependabotPreservesExistingPolicy(t *testing.T) {
	policy := currentDependabotPolicy(t)
	updates, ok := policy["updates"].([]any)
	if !ok {
		t.Fatal("Dependabot updates must be a list")
	}
	var existing []any
	for _, update := range updates {
		entry, ok := update.(map[string]any)
		if !ok {
			t.Fatal("each Dependabot update must be a mapping")
		}
		if entry["package-ecosystem"] != "npm" {
			existing = append(existing, update)
		}
	}
	policy["updates"] = existing
	expected := readDependabotPolicy(t, []byte(dependabotExistingPolicy))
	if !reflect.DeepEqual(policy, expected) {
		t.Fatalf("existing Go, Actions, Docker and top-level policy must remain unchanged\nwant: %#v\ngot: %#v", expected, policy)
	}
}
