package cloudconfig

import (
	"os"
	"testing"
)

// writeCloudConfig serializes cfg to cloud.json inside dir. It is a
// thin wrapper around Save used by the EffectiveToken tests to set
// up the file side of the precedence table.
func writeCloudConfig(t *testing.T, dir string, cfg Config) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("setup MkdirAll(%q): %v", dir, err)
	}
	if err := Save(dir, &cfg); err != nil {
		t.Fatalf("setup Save(%q, %+v): %v", dir, cfg, err)
	}
}

// TestEffectiveToken is the table-driven test for the precedence
// contract documented in REQ-1 of the spec and the design's "Token
// + URL precedence" section: env (when set and non-empty after
// whitespace trimming) wins over the file; an empty file token is
// treated as "no token" and surfaces as SourceNone.
//
// Each subtest sets a fresh t.Setenv("ENGRAM_CLOUD_TOKEN") (so any
// inherited value from the parent shell is cleared) and writes a
// fresh cloud.json (or skips the write) for the file side.
//
// NOTE: t.Setenv is incompatible with t.Parallel, so the test is
// intentionally not parallel. This is correct: env-var changes
// affect the whole process, and t.Setenv's Cleanup restores the
// previous value when the subtest ends, so test order is also safe.
func TestEffectiveToken(t *testing.T) {
	cases := []struct {
		name        string
		fileContent *Config // nil = no file written
		envValue    string  // "" clears any inherited value via t.Setenv
		wantToken   string
		wantSource  Source
	}{
		{
			name:        "file+env",
			fileContent: &Config{Token: "t1"},
			envValue:    "e1",
			wantToken:   "e1",
			wantSource:  SourceEnv,
		},
		{
			name:        "file_only",
			fileContent: &Config{Token: "t1"},
			envValue:    "",
			wantToken:   "t1",
			wantSource:  SourceFile,
		},
		{
			name:        "env_only",
			fileContent: nil,
			envValue:    "e1",
			wantToken:   "e1",
			wantSource:  SourceEnv,
		},
		{
			name:        "neither",
			fileContent: nil,
			envValue:    "",
			wantToken:   "",
			wantSource:  SourceNone,
		},
		{
			name:        "whitespace_env",
			fileContent: &Config{Token: "t1"},
			envValue:    "   ",
			wantToken:   "t1",
			wantSource:  SourceFile,
		},
		{
			name:        "empty_file",
			fileContent: &Config{Token: ""},
			envValue:    "",
			wantToken:   "",
			wantSource:  SourceNone,
		},
		{
			name:        "env_with_surrounding_spaces",
			fileContent: &Config{Token: "t1"},
			envValue:    "  e1  ",
			wantToken:   "e1",
			wantSource:  SourceEnv,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			// Always set the env var; empty string clears any
			// inherited value from the parent shell.
			t.Setenv("ENGRAM_CLOUD_TOKEN", tc.envValue)

			dir := t.TempDir()
			if tc.fileContent != nil {
				writeCloudConfig(t, dir, *tc.fileContent)
			}

			gotToken, gotSource := EffectiveToken(dir)
			if gotToken != tc.wantToken {
				t.Errorf("EffectiveToken(%q): token = %q, want %q", dir, gotToken, tc.wantToken)
			}
			if gotSource != tc.wantSource {
				t.Errorf("EffectiveToken(%q): source = %v, want %v", dir, gotSource, tc.wantSource)
			}
		})
	}
}

// TestSourceLabel verifies that each Source value maps to the
// user-facing label the CLI auth status line and the TUI's
// TokenSource* constants are expected to mirror. The labels are
// part of the user-visible output, so a regression here is
// user-visible.
func TestSourceLabel(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		source Source
		want   string
	}{
		{"none", SourceNone, "not set"},
		{"file", SourceFile, "read from cloud.json"},
		{"env", SourceEnv, "set via ENGRAM_CLOUD_TOKEN"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := SourceLabel(tc.source); got != tc.want {
				t.Errorf("SourceLabel(%v) = %q, want %q", tc.source, got, tc.want)
			}
		})
	}
}

// TestSourceEnumOrdering guards against accidental reordering of
// the iota block. The integer values are part of the package's
// observable behavior: the TUI's TokenSource* mapping and any
// future switch on Source values depend on the order.
func TestSourceEnumOrdering(t *testing.T) {
	t.Parallel()

	if SourceNone != 0 {
		t.Errorf("SourceNone = %d, want 0", SourceNone)
	}
	if SourceFile != 1 {
		t.Errorf("SourceFile = %d, want 1", SourceFile)
	}
	if SourceEnv != 2 {
		t.Errorf("SourceEnv = %d, want 2", SourceEnv)
	}
}

// TestEffectiveTokenEnvWinsOverFile is a prominent regression
// test for the design's intent: env wins over file. The
// table-driven test covers each input in isolation, but this test
// makes the precedence explicit and is the first thing a reviewer
// sees when they ask "what does the function do when both are
// set?".
func TestEffectiveTokenEnvWinsOverFile(t *testing.T) {
	t.Setenv("ENGRAM_CLOUD_TOKEN", "env-wins")
	dir := t.TempDir()
	writeCloudConfig(t, dir, Config{Token: "file-loses"})

	tok, src := EffectiveToken(dir)
	if tok != "env-wins" {
		t.Errorf("EffectiveToken: token = %q, want %q (env must win over file)", tok, "env-wins")
	}
	if src != SourceEnv {
		t.Errorf("EffectiveToken: source = %v, want %v (env must win over file)", src, SourceEnv)
	}
}

// TestEffectiveTokenHandlesMalformedFile is a defensive test:
// when cloud.json exists but contains invalid JSON, Load returns
// (nil, err). EffectiveToken must fall through to env (or
// SourceNone when env is also unset) without panicking. The
// "nil config" defensive branch in readFileToken is exercised
// indirectly via the same path: the malformed file triggers
// err != nil, which short-circuits to ("", false).
func TestEffectiveTokenHandlesMalformedFile(t *testing.T) {
	t.Setenv("ENGRAM_CLOUD_TOKEN", "")
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("setup MkdirAll: %v", err)
	}
	if err := os.WriteFile(Path(dir), []byte("{not valid json"), 0o644); err != nil {
		t.Fatalf("setup WriteFile: %v", err)
	}

	tok, src := EffectiveToken(dir)
	if tok != "" {
		t.Errorf("EffectiveToken: token = %q, want empty (malformed file must not surface)", tok)
	}
	if src != SourceNone {
		t.Errorf("EffectiveToken: source = %v, want %v", src, SourceNone)
	}
}

// TestSourceLabelStringer verifies that SourceLabel and the
// Source.String method return the same string for every Source
// value. They are two call shapes for the same data; if they
// ever drift, a caller using one form will see a different label
// than a caller using the other, and the CLI/TUI parity test
// (TestCLIAndTUIAgreeOnTokenSource, planned for T-608.5) will
// catch the drift only if the TUI happens to use the same form.
func TestSourceLabelStringer(t *testing.T) {
	t.Parallel()

	for _, s := range []Source{SourceNone, SourceFile, SourceEnv} {
		if got, want := SourceLabel(s), s.String(); got != want {
			t.Errorf("SourceLabel(%v) = %q, want %q (== String())", s, got, want)
		}
	}
}
