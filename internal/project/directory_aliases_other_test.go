//go:build !windows

package project

import "testing"

func TestDirectoryHistoryMatchesExact(t *testing.T) {
	selected := "/selected parent/selected leaf"
	aliases, err := DirectoryHistoryAliases(selected)
	if err != nil {
		t.Fatal(err)
	}
	if !DirectoryHistoryMatches(aliases, selected) {
		t.Fatal("selected spelling must retain exact matching")
	}
	for _, historical := range []string{
		"/parent~1/selected leaf", "/selected parent/leaf~1",
		selected + "/child", "/selected parent", "/other/selected leaf",
	} {
		if DirectoryHistoryMatches(aliases, historical) {
			t.Fatalf("non-Windows history unexpectedly matched %q", historical)
		}
	}
}
