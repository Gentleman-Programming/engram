package cloudconfig

import (
	"os"
	"strings"
)

// EnvCloudToken is the environment variable consulted by
// EffectiveToken when resolving the runtime cloud token. Defining
// it as a constant keeps the contract discoverable and prevents
// typos at call sites; both the CLI and the TUI read the same
// variable, and the TUI's precedence drift (the bug this package
// fixes) was caused by the TUI reading the env var inconsistently
// with the CLI.
const EnvCloudToken = "ENGRAM_CLOUD_TOKEN"

// User-facing labels for each Source value. These are the
// canonical strings the CLI prints on its "Auth status" line and
// the TUI's view layer renders via the TokenSource* constants.
// The strings are exported as constants so that consumers (TUI,
// future CLI status subcommands, dashboard UIs, log messages)
// can reference them directly instead of duplicating the literal
// — which is exactly the drift that produced the TUI's silent
// precedence bug this package fixes.
//
// The TUI/CLI agreement is pinned by TestCLIAndTUIAgreeOnTokenSource
// (T-608.5): if you change any of these strings, update every
// consumer to match in the same commit.
const (
	// LabelSourceNone is the user-facing label for SourceNone
	// ("the runtime cloud token is not configured").
	LabelSourceNone = "not set"

	// LabelSourceFile is the user-facing label for SourceFile
	// ("the runtime cloud token was read from cloud.json").
	LabelSourceFile = "read from cloud.json"

	// LabelSourceEnv is the user-facing label for SourceEnv
	// ("the runtime cloud token was provided via the
	// ENGRAM_CLOUD_TOKEN environment variable").
	LabelSourceEnv = "set via ENGRAM_CLOUD_TOKEN"
)

// Source identifies which input provided the effective token. The
// zero value is SourceNone, which also happens to be the value
// returned when no token is configured.
type Source int

const (
	// SourceNone indicates no token is configured. The effective
	// token returned alongside this value is always empty.
	SourceNone Source = iota

	// SourceFile indicates the token came from cloud.json inside
	// the supplied data directory. The file must exist and
	// contain a non-empty Token field for this to be the source.
	SourceFile

	// SourceEnv indicates the token came from the
	// ENGRAM_CLOUD_TOKEN environment variable. The variable must
	// be set and non-empty (after whitespace trimming) for this
	// to be the source.
	SourceEnv
)

// String returns the user-facing label for the source. The label
// is the same string the CLI auth status line prints; the TUI's
// TokenSource* constants are expected to mirror it via
// SourceLabel. The TUI/CLI parity is pinned by
// TestCLIAndTUIAgreeOnTokenSource (T-608.5): if you change a
// label, update every consumer to match in the same commit.
func (s Source) String() string {
	switch s {
	case SourceFile:
		return LabelSourceFile
	case SourceEnv:
		return LabelSourceEnv
	default:
		return LabelSourceNone
	}
}

// SourceLabel returns the user-facing label for a Source value.
// The CLI uses Source internally to choose behavior; the TUI uses
// SourceLabel(Source) to display the same source. The strings
// MUST match what the TUI's view layer renders (or what the CLI's
// status command prints) for the same (file, env) input, so the
// regression test TestCLIAndTUIAgreeOnTokenSource pins this
// invariant. If you change a label, update the TUI's view (and
// any other consumer) to match.
//
// The function form is provided for callers that prefer functions
// over methods (e.g. a switch statement in a view layer that
// needs to map a Source to a string).
func SourceLabel(s Source) string {
	return s.String()
}

// EffectiveToken resolves the runtime cloud token by consulting
// the ENGRAM_CLOUD_TOKEN environment variable and cloud.json
// inside dataDir, in that order.
//
// The env var takes precedence: when it is set and non-empty
// (after whitespace trimming), the file is not consulted.
// Otherwise the file's Token field is used if it is non-empty
// (after whitespace trimming). Otherwise the effective token is
// empty and the source is SourceNone.
//
// Whitespace-only env values are treated as unset. Surrounding
// whitespace on a real env value is stripped before the value is
// returned, so a token in the form "  e1  " surfaces as "e1".
// This matches the existing CLI's resolveCloudRuntimeConfig
// behavior at cmd/engram/main.go:447, where the trimmed value
// is what gets written to the runtime config.
func EffectiveToken(dataDir string) (string, Source) {
	if v := os.Getenv(EnvCloudToken); strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v), SourceEnv
	}
	if tok, ok := readFileToken(dataDir); ok {
		return tok, SourceFile
	}
	return "", SourceNone
}

// readFileToken returns the token stored in cloud.json inside
// dataDir. The second return is true only when a non-empty token
// was found; false indicates either a load error, missing file,
// nil config, or an empty/whitespace-only token.
//
// Treating an empty file token as "no token" is intentional: it
// lets the caller distinguish "the file says we have no token"
// from "the file has a real token", and it keeps the function's
// contract simple — a non-empty token always comes back with
// SourceFile, never SourceNone.
func readFileToken(dataDir string) (string, bool) {
	cfg, err := Load(dataDir)
	if err != nil || cfg == nil {
		return "", false
	}
	if strings.TrimSpace(cfg.Token) == "" {
		return "", false
	}
	return cfg.Token, true
}
