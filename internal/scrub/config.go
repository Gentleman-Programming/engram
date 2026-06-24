package scrub

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
)

// EnvCustomPatterns names an env var holding a path to a JSON file of extra,
// deployment-specific detectors. This lets operators add organization-specific
// patterns (internal ID formats, proprietary field names, issued-token prefixes)
// WITHOUT placing any of that in this open-source code. The file lives only in
// the operator's private deployment.
//
// File format: a JSON array of objects:
//
//	[
//	  {"name":"acme_resource_id","category":"internal_id","severity":"heuristic","regex":"\\bacme_[0-9a-f]{12}\\b"},
//	  {"name":"acme_field","category":"cardholder_data","severity":"high","regex":"(?i)\\bsecret_field\\b\\s*[:=]\\s*\\S"}
//	]
const EnvCustomPatterns = "ENGRAM_SCRUB_PATTERNS"

// CustomPattern is an operator-supplied detector loaded from config.
type CustomPattern struct {
	Name     string `json:"name"`
	Category string `json:"category"` // cardholder_data | secret | pii | internal_id
	Severity string `json:"severity"` // "high" | "heuristic" (default heuristic)
	Regex    string `json:"regex"`
}

type compiledCustom struct {
	name string
	cat  Category
	sev  Severity
	re   *regexp.Regexp
}

var (
	customMu   sync.RWMutex
	customComp []compiledCustom
	customOnce sync.Once
)

func severityFromString(s string) Severity {
	if strings.EqualFold(strings.TrimSpace(s), "high") {
		return SeverityHigh
	}
	return SeverityHeuristic
}

func compileCustom(ps []CustomPattern) ([]compiledCustom, error) {
	out := make([]compiledCustom, 0, len(ps))
	for _, p := range ps {
		re, err := regexp.Compile(p.Regex)
		if err != nil {
			return nil, fmt.Errorf("custom scrub pattern %q: %w", p.Name, err)
		}
		cat := Category(strings.TrimSpace(p.Category))
		if cat == "" {
			cat = CategorySecret
		}
		name := strings.TrimSpace(p.Name)
		if name == "" {
			name = "custom"
		}
		out = append(out, compiledCustom{name: name, cat: cat, sev: severityFromString(p.Severity), re: re})
	}
	return out, nil
}

// SetCustomPatterns compiles and installs the active custom detectors. Passing
// nil clears them. Used by the env loader and by tests.
func SetCustomPatterns(ps []CustomPattern) error {
	comp, err := compileCustom(ps)
	if err != nil {
		return err
	}
	customMu.Lock()
	customComp = comp
	customMu.Unlock()
	return nil
}

// LoadCustomPatternsFromEnv reads ENGRAM_SCRUB_PATTERNS (if set) and installs the
// patterns. It is safe to call repeatedly; failures are returned, not panicked.
func LoadCustomPatternsFromEnv() error {
	path := strings.TrimSpace(os.Getenv(EnvCustomPatterns))
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", EnvCustomPatterns, err)
	}
	var ps []CustomPattern
	if err := json.Unmarshal(data, &ps); err != nil {
		return fmt.Errorf("parse %s: %w", EnvCustomPatterns, err)
	}
	return SetCustomPatterns(ps)
}

func customFindings(s string) []Finding {
	customOnce.Do(func() { _ = LoadCustomPatternsFromEnv() })
	customMu.RLock()
	defer customMu.RUnlock()
	var out []Finding
	for _, c := range customComp {
		if c.re.MatchString(s) {
			out = append(out, Finding{c.cat, c.name, c.sev})
		}
	}
	return out
}
