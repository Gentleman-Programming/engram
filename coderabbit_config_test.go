package engram_test

import (
	"crypto/sha256"
	"fmt"
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestCodeRabbitBalancedReview(t *testing.T) {
	_, config := codeRabbitConfig(t)
	if config.Reviews.Profile != "chill" {
		t.Fatalf("review profile = %q, want chill", config.Reviews.Profile)
	}
}

func TestCodeRabbitDocstringPercentageDisabled(t *testing.T) {
	_, config := codeRabbitConfig(t)
	if config.Reviews.PreMergeChecks.Docstrings.Mode != "off" {
		t.Fatalf("docstring check = %q, want off", config.Reviews.PreMergeChecks.Docstrings.Mode)
	}
}

func TestCodeRabbitPreservesExportedSettings(t *testing.T) {
	content, _ := codeRabbitConfig(t)
	// Source: PR #1728, CodeRabbit reply #6084792433. Restore the two
	// intentional changes, then compare every remaining exported setting.
	content = strings.Replace(content, "  profile: chill\n", "  profile: assertive\n", 1)
	content = strings.Replace(content, "    docstrings:\n      mode: off\n", "    docstrings:\n      mode: warning\n", 1)
	var lines []string
	for _, line := range strings.Split(content, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "#") {
			lines = append(lines, line)
		}
	}
	got := fmt.Sprintf("%x", sha256.Sum256([]byte(strings.TrimSpace(strings.Join(lines, "\n")))))
	const want = "25258494aec855af73fcc94a1a7880a74456042416e9191500845d1792193434"
	if got != want {
		t.Fatalf("exported settings changed beyond the two noise adjustments: hash %s, want %s", got, want)
	}
}

type codeRabbitPolicy struct {
	Reviews struct {
		Profile        string `yaml:"profile"`
		PreMergeChecks struct {
			Docstrings struct {
				Mode string `yaml:"mode"`
			} `yaml:"docstrings"`
		} `yaml:"pre_merge_checks"`
	} `yaml:"reviews"`
}

func codeRabbitConfig(t *testing.T) (string, codeRabbitPolicy) {
	t.Helper()
	content, err := os.ReadFile(".coderabbit.yaml")
	if err != nil {
		t.Fatalf("read CodeRabbit configuration: %v", err)
	}
	var config codeRabbitPolicy
	if err := yaml.Unmarshal(content, &config); err != nil {
		t.Fatalf("parse CodeRabbit YAML: %v", err)
	}
	return strings.ReplaceAll(string(content), "\r\n", "\n"), config
}
