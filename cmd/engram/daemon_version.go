package main

import (
	"fmt"
	"strings"

	"golang.org/x/mod/semver"
)

// daemonVersionStatus reports release skew, never process identity or restart
// authority. Build metadata is not part of a semantic release comparison.
func daemonVersionStatus(server, installed string) string {
	normalize := func(value string) string {
		return strings.TrimPrefix(semver.Canonical("v"+strings.TrimPrefix(strings.TrimSpace(value), "v")), "v")
	}
	server, installed = normalize(server), normalize(installed)
	known := server != "" && installed != ""
	if server == "" {
		server = "unknown"
	}
	if installed == "" {
		installed = "unknown"
	}
	result := fmt.Sprintf("Versions: server %s; installed CLI %s\n", server, installed)
	if !known {
		return result + "Version comparison unavailable.\n"
	}
	if server != installed {
		result += "Restart pending: server and installed CLI versions differ. Restart the intended server manually using its existing supervisor; `engram restart` manages only the pidfile-tracked daemon.\n"
	}
	return result
}
