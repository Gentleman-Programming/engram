package engram_test

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path"
	"reflect"
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
	// Source: PR #1728, CodeRabbit reply #6084792433. Remove only the five
	// exact JS/TS blocks (#1742), two workflow blocks (#1743), six sync blocks
	// and two conditional persistence additions (#1744), then restore the
	// two noise adjustments and compare every exported setting.
	// The original hash stays unchanged; unrelated settings still fail.
	for _, glob := range codeRabbitJSTestPaths {
		block := "    - path: '" + glob + "'\n      instructions: >-\n"
		for _, line := range strings.Split(codeRabbitJSTestInstructions, "\n") {
			block += "        " + line + "\n"
		}
		content = strings.Replace(content, block, "", 1)
	}
	for _, glob := range codeRabbitWorkflowPaths {
		block := "    - path: '" + glob + "'\n      instructions: >-\n"
		for _, line := range strings.Split(codeRabbitWorkflowInstructions, "\n") {
			block += "        " + line + "\n"
		}
		content = strings.Replace(content, block, "", 1)
	}
	for _, glob := range codeRabbitSyncPaths {
		content = strings.Replace(content, codeRabbitSyncBlock(glob, codeRabbitSyncInstructions), "", 1)
	}
	for _, rule := range codeRabbitSyncPersistenceRules {
		block := codeRabbitSyncBlock(rule.path, rule.original+" For sync/import changes only:\n"+codeRabbitSyncInstructions)
		original := "    - path: " + rule.path + "\n      instructions: " + rule.original + "\n"
		content = strings.Replace(content, block, original, 1)
	}
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
		t.Fatalf("exported settings changed beyond the noise adjustments and exact JS/TS/workflow/sync instructions: hash %s, want %s", got, want)
	}
}

var codeRabbitJSTestPaths = []string{
	".github/scripts/*.test.mjs",
	"plugin/obsidian/test/*.test.mjs",
	"plugin/opencode/*.test.mjs",
	"plugin/opencode/*.test.mts",
	"plugin/pi/test/**/*.test.mjs",
}

const codeRabbitJSTestInstructions = `Prefer deterministic, isolated setup and cleanup. Check relevant success paths,
error paths and boundaries through observable behavior: returned values,
outgoing requests, persisted files, warnings and prohibited side effects.
Mocks, stubs and internal seams are useful when they protect real contracts;
workflow, schema and package structural-contract checks are valid. Flag assertions
that only mirror implementation details or fixtures; do not require blanket
integration-test or style rewrites.`

func TestCodeRabbitJSTestScope(t *testing.T) {
	_, config := codeRabbitConfig(t)
	var got []string
	for _, rule := range config.Reviews.PathInstructions {
		if strings.Contains(rule.Path, ".test.") {
			got = append(got, rule.Path)
		}
	}
	if !reflect.DeepEqual(got, codeRabbitJSTestPaths) {
		t.Fatalf("JS/TS test paths = %v, want %v", got, codeRabbitJSTestPaths)
	}

	// These examples check the selected plain-glob contract locally, not
	// CodeRabbit's minimatch implementation or live instruction overlap.
	for _, tt := range []struct {
		file string
		want bool
	}{
		{".github/scripts/pr-size-notice.test.mjs", true},
		{"plugin/obsidian/test/sync.test.mjs", true},
		{"plugin/opencode/engram.test.mjs", true},
		{"plugin/opencode/engram.test.mts", true},
		{"plugin/pi/test/index-source.test.mjs", true},
		{"plugin/pi/test/nested/example.test.mjs", true},
		{"plugin/pi/test/nested/deep/example.test.mjs", true},
		{".github/scripts/pr-size-notice.mjs", false},
		{".github/scripts/nested/example.test.mjs", false},
		{"plugin/obsidian/test/sync.test.mts", false},
		{"plugin/opencode/nested/engram.test.mjs", false},
		{"plugin/pi/test/plugin-sandbox.mjs", false},
		{"plugin/pi/test/release-contract.mjs", false},
		{"plugin/pi/test/support/shutdown-delivery-child.mjs", false},
		{"plugin/pi/test/example.test.mts", false},
		{"plugin/pi/testing/example.test.mjs", false},
		{"plugin/pi/index.ts", false},
		{"internal/store/store_test.go", false},
	} {
		t.Run(tt.file, func(t *testing.T) {
			matched := false
			for _, glob := range got {
				file := tt.file
				if glob == "plugin/pi/test/**/*.test.mjs" {
					if !strings.HasPrefix(file, "plugin/pi/test/") {
						continue
					}
					glob, file = "*.test.mjs", path.Base(file)
				}
				ok, err := path.Match(glob, file)
				if err != nil {
					t.Fatalf("invalid selected glob %q: %v", glob, err)
				}
				matched = matched || ok
			}
			if matched != tt.want {
				t.Fatalf("selected JS/TS paths match %q = %t, want %t", tt.file, matched, tt.want)
			}
		})
	}
}

func TestCodeRabbitJSTestGuidance(t *testing.T) {
	_, config := codeRabbitConfig(t)
	want := strings.Join(strings.Fields(codeRabbitJSTestInstructions), " ")
	for _, glob := range codeRabbitJSTestPaths {
		t.Run(glob, func(t *testing.T) {
			var instructions []string
			for _, rule := range config.Reviews.PathInstructions {
				if rule.Path == glob {
					instructions = append(instructions, rule.Instructions)
				}
			}
			if !reflect.DeepEqual(instructions, []string{want}) {
				t.Fatalf("instructions for %q = %q, want one deterministic/observable/contract-aware rule %q", glob, instructions, want)
			}
		})
	}
}

func TestCodeRabbitJSTestDocumentation(t *testing.T) {
	content, err := os.ReadFile("CONTRIBUTING.md")
	if err != nil {
		t.Fatal(err)
	}
	_, section, ok := strings.Cut(string(content), "## Advisory AI Review")
	if !ok {
		t.Fatal("missing Advisory AI Review section")
	}
	section, _, _ = strings.Cut(section, "\n## ")
	for _, text := range append(append([]string{}, codeRabbitJSTestPaths...),
		"isolated mocks", "structural-contract checks", "not CodeRabbit's matcher", "live feedback") {
		if !strings.Contains(section, text) {
			t.Errorf("Advisory AI Review documentation missing %q", text)
		}
	}
}

var codeRabbitWorkflowPaths = []string{
	".github/workflows/*.yml",
	".github/workflows/*.yaml",
}

const codeRabbitWorkflowInstructions = `Review GitHub Actions permissions and trust boundaries in event context.
Require minimum necessary job/token permissions and justification for legitimate
write scopes; do not prescribe blanket contents: read for release, package
publishing, OIDC or repository maintenance jobs.
Review secret/credential handling for exposure in logs, scripts and untrusted code.
Distinguish trusted-base or trusted-workflow metadata/label checks from checking
out or executing attacker-controlled pull request code with privileged credentials,
including pull_request_target and workflow_run paths. Check checkout provenance
and treat fork inputs, PR metadata and downloaded artifacts as untrusted data;
flag unsafe interpolation or execution, not legitimate metadata processing.
Do not blanket-ban privileged events or replace deterministic checks/human review.`

func TestCodeRabbitWorkflowScope(t *testing.T) {
	_, config := codeRabbitConfig(t)
	var got []string
	for _, rule := range config.Reviews.PathInstructions {
		if strings.HasPrefix(rule.Path, ".github/workflows/") {
			got = append(got, rule.Path)
		}
	}
	if !reflect.DeepEqual(got, codeRabbitWorkflowPaths) {
		t.Fatalf("workflow paths = %v, want %v", got, codeRabbitWorkflowPaths)
	}

	// Check every current workflow and the selected plain-glob boundaries,
	// not CodeRabbit's minimatch implementation or review quality.
	files, err := os.ReadDir(".github/workflows")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		file string
		want bool
	}{
		{".github/workflows/example.yaml", true},
		{".github/workflows/nested/example.yml", false},
		{".github/dependabot.yml", false},
		{".github/scripts/pr-size-notice.mjs", false},
		{".github/workflows/example.yml.bak", false},
	}
	for _, file := range files {
		if !file.IsDir() && (path.Ext(file.Name()) == ".yml" || path.Ext(file.Name()) == ".yaml") {
			cases = append(cases, struct {
				file string
				want bool
			}{path.Join(".github/workflows", file.Name()), true})
		}
	}
	for _, tt := range cases {
		t.Run(tt.file, func(t *testing.T) {
			matched := false
			for _, glob := range got {
				ok, err := path.Match(glob, tt.file)
				if err != nil {
					t.Fatalf("invalid workflow glob %q: %v", glob, err)
				}
				matched = matched || ok
			}
			if matched != tt.want {
				t.Fatalf("workflow paths match %q = %t, want %t", tt.file, matched, tt.want)
			}
		})
	}
}

func TestCodeRabbitWorkflowGuidance(t *testing.T) {
	_, config := codeRabbitConfig(t)
	want := strings.Join(strings.Fields(codeRabbitWorkflowInstructions), " ")
	for _, glob := range codeRabbitWorkflowPaths {
		t.Run(glob, func(t *testing.T) {
			var instructions []string
			for _, rule := range config.Reviews.PathInstructions {
				if rule.Path == glob {
					instructions = append(instructions, rule.Instructions)
				}
			}
			if !reflect.DeepEqual(instructions, []string{want}) {
				t.Fatalf("instructions for %q = %q, want one privilege/provenance-aware rule %q", glob, instructions, want)
			}
		})
	}
}

func TestCodeRabbitWorkflowDocumentation(t *testing.T) {
	content, err := os.ReadFile("CONTRIBUTING.md")
	if err != nil {
		t.Fatal(err)
	}
	_, section, ok := strings.Cut(string(content), "## Advisory AI Review")
	if !ok {
		t.Fatal("missing Advisory AI Review section")
	}
	section, _, _ = strings.Cut(section, "\n## ")
	for _, text := range append(append([]string{}, codeRabbitWorkflowPaths...),
		"minimum necessary", "trusted-base", "privileged credentials",
		"legitimate write scopes", "without executing unsafe examples",
		"false positives", "two exact workflow instruction blocks") {
		if !strings.Contains(section, text) {
			t.Errorf("Advisory AI Review documentation missing %q", text)
		}
	}
}

var codeRabbitSyncPaths = []string{
	"internal/sync/**/*.go",
	"internal/cloud/chunkcodec/**/*.go",
	"internal/cloud/autosync/**/*.go",
	"internal/cloud/remote/transport.go",
	"internal/cloud/cloudserver/cloudserver.go",
	"internal/cloud/cloudserver/mutations.go",
}

var codeRabbitSyncPersistenceRules = []struct{ path, original string }{
	{"internal/store/**/*.go", "Persistence layer. SQL queries live here, never in handlers. No HTTP logic or HTML rendering. Schema changes require a test."},
	{"internal/cloud/cloudstore/**/*.go", "Cloud persistence. SQL stays here. Cloud is replication, not source of truth (SQLite local is). Admin-enforceable sync policies must be validated server-side and fail loudly, never drop data silently."},
}

const codeRabbitSyncInstructions = `Review sync/import changes against existing core contracts; local SQLite remains the source of truth.
Preserve records, stable identities, deletion semantics and review_after metadata,
including the existing distinction between omitted fields, explicit null and valid dates.
Check idempotent replay and deduplication without inventing new conflict-resolution rules.
Review transaction boundaries and partial failures; do not assume whole-sync atomicity.
Advance push acknowledgements and pull cursors only according to successfully accepted/persisted work.
Policy failures must remain explicit, not silently discard data or bypass enrollment/cloud pause.
Require deterministic regression evidence for changed behavior, including rejected-input side effects,
replay and recovery after partial failure; do not prescribe new sync algorithms, ownership or scheduling policy.`

func codeRabbitSyncBlock(glob, instructions string) string {
	block := "    - path: '" + glob + "'\n      instructions: >-\n"
	for _, line := range strings.Split(instructions, "\n") {
		block += "        " + line + "\n"
	}
	return block
}

func TestCodeRabbitSyncScope(t *testing.T) {
	_, config := codeRabbitConfig(t)
	var got []string
	for _, rule := range config.Reviews.PathInstructions {
		if strings.HasPrefix(rule.Instructions, "Review sync/import changes") {
			got = append(got, rule.Path)
		}
	}
	if !reflect.DeepEqual(got, codeRabbitSyncPaths) {
		t.Fatalf("sync paths = %v, want %v", got, codeRabbitSyncPaths)
	}
	// These examples pin our selected recursive-glob boundaries, not the vendor matcher.
	for _, tc := range []struct {
		file string
		want bool
	}{
		{"internal/sync/sync.go", true},
		{"internal/sync/nested/example_test.go", true},
		{"internal/cloud/chunkcodec/chunkcodec.go", true},
		{"internal/cloud/autosync/nested/deep/example.go", true},
		{"internal/cloud/remote/transport.go", true},
		{"internal/cloud/cloudserver/cloudserver.go", true},
		{"internal/cloud/cloudserver/mutations.go", true},
		{"internal/cloud/cloudserver/auth.go", false},
		{"internal/cloud/cloudserver/nested/mutations.go", false},
		{"internal/cloud/remote/credentials.go", false},
		{"internal/cloud/dashboard/handler.go", false},
		{"internal/sync/sync.go.bak", false},
		{"internal/synchronization/example.go", false},
		{"internal/server/server.go", false},
		{"plugin/pi/index.ts", false},
		{".github/workflows/ci.yml", false},
	} {
		t.Run(tc.file, func(t *testing.T) {
			matched := false
			for _, glob := range got {
				prefix, _, recursive := strings.Cut(glob, "**/")
				if recursive {
					matched = matched || strings.HasPrefix(tc.file, prefix) && path.Ext(tc.file) == ".go"
				} else {
					matched = matched || tc.file == glob
				}
			}
			if matched != tc.want {
				t.Fatalf("sync scope %q = %t, want %t", tc.file, matched, tc.want)
			}
		})
	}
}

func TestCodeRabbitSyncGuidance(t *testing.T) {
	_, config := codeRabbitConfig(t)
	want := strings.Join(strings.Fields(codeRabbitSyncInstructions), " ")
	for _, glob := range codeRabbitSyncPaths {
		t.Run(glob, func(t *testing.T) {
			var got []string
			for _, rule := range config.Reviews.PathInstructions {
				if rule.Path == glob {
					got = append(got, rule.Instructions)
				}
			}
			if !reflect.DeepEqual(got, []string{want}) {
				t.Fatalf("sync instructions = %q, want one rule %q", got, want)
			}
		})
	}
	for _, persistence := range codeRabbitSyncPersistenceRules {
		t.Run(persistence.path, func(t *testing.T) {
			var got []string
			for _, rule := range config.Reviews.PathInstructions {
				if rule.Path == persistence.path {
					got = append(got, rule.Instructions)
				}
			}
			expected := persistence.original + " For sync/import changes only: " + want
			if !reflect.DeepEqual(got, []string{expected}) {
				t.Fatalf("persistence instructions = %q, want preserved ownership plus conditional sync guidance %q", got, expected)
			}
		})
	}
}

func TestCodeRabbitSyncDocumentation(t *testing.T) {
	content, err := os.ReadFile("CONTRIBUTING.md")
	if err != nil {
		t.Fatal(err)
	}
	_, section, ok := strings.Cut(string(content), "## Advisory AI Review")
	if !ok {
		t.Fatal("missing Advisory AI Review section")
	}
	section, _, _ = strings.Cut(section, "\n## ")
	terms := append(append([]string{}, codeRabbitSyncPaths...), "review_after", "explicit null", "whole-sync atomicity", "push acknowledgements", "pull cursors", "six exact sync", "two conditional persistence", "sync algorithms", "live feedback")
	for _, term := range terms {
		if !strings.Contains(section, term) {
			t.Errorf("sync documentation missing %q", term)
		}
	}
}

type codeRabbitPolicy struct {
	Reviews struct {
		Profile          string `yaml:"profile"`
		PathInstructions []struct {
			Path         string `yaml:"path"`
			Instructions string `yaml:"instructions"`
		} `yaml:"path_instructions"`
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
