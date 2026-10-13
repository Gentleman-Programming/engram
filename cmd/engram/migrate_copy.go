package main

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

const migrateCopyUsage = "Usage: engram migrate-copy --data-dir <absolute-source> --output-dir <new-absolute-destination> --confirm-quiescent --json\n"

// Parse all flags before reading any source or resolving default configuration.
func runMigrateCopy(args []string, stdout, stderr io.Writer) int {
	fail := func(code int, e error) int { _, _ = fmt.Fprintf(stderr, "engram migrate-copy: %v\n", e); return code }
	if len(args) == 1 && args[0] == "--help" {
		_, _ = fmt.Fprint(stdout, migrateCopyUsage)
		return 0
	}
	seen := map[string]bool{}
	var source, output string
	for i := 0; i < len(args); i++ {
		flag := args[i]
		if seen[flag] {
			return fail(2, fmt.Errorf("duplicate flag %s", flag))
		}
		seen[flag] = true
		switch flag {
		case "--data-dir", "--output-dir":
			if i+1 == len(args) || strings.HasPrefix(args[i+1], "--") {
				return fail(2, fmt.Errorf("missing value for %s", flag))
			}
			i++
			if !filepath.IsAbs(args[i]) {
				return fail(2, fmt.Errorf("%s must be absolute", flag))
			}
			if flag == "--data-dir" {
				source = args[i]
			} else {
				output = args[i]
			}
		case "--confirm-quiescent", "--json":
		default:
			return fail(2, fmt.Errorf("unknown flag %s", flag))
		}
	}
	for _, flag := range []string{"--data-dir", "--output-dir", "--confirm-quiescent", "--json"} {
		if !seen[flag] {
			return fail(2, fmt.Errorf("required flag %s", flag))
		}
	}
	result, e := store.MigrateCopy(source, output, true)
	if e != nil {
		return fail(1, e)
	}
	if e = json.NewEncoder(stdout).Encode(result); e != nil {
		return fail(1, e)
	}
	return 0
}
