package project

import (
	"errors"
	"reflect"
	"testing"

	"golang.org/x/sys/windows"
)

func TestDirectoryHistoryMatchesComponents(t *testing.T) {
	// These are lexical matcher inputs, not a claim that invented paths have
	// real aliases. The public store regression proves real Windows identity.
	aliases := []string{`c:\selected parent\selected leaf`, `c:\parent~1\leaf~1`}
	for _, tc := range []struct {
		name, historical string
		want             bool
	}{
		{"long", aliases[0], true},
		{"short", aliases[1], true},
		{"mixed-prefix", `c:\parent~1\selected leaf`, true},
		{"mixed-leaf", `c:\selected parent\leaf~1`, true},
		{"other-drive", `d:\parent~1\leaf~1`, false},
		{"parent", `c:\parent~1`, false},
		{"child", `c:\parent~1\leaf~1\child`, false},
		{"sibling", `c:\parent~1\other leaf`, false},
		{"unrelated-parent", `c:\other parent\leaf~1`, false},
		{"relative", `parent~1\leaf~1`, false},
		{"historical-unc", `\\offline.invalid\share\leaf~1`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := DirectoryHistoryMatches(aliases, tc.historical); got != tc.want {
				t.Fatalf("lexical history match for %q = %v, want %v", tc.historical, got, tc.want)
			}
		})
	}
	unc := []string{`\\offline.invalid\share\selected directory`}
	if !DirectoryHistoryMatches(unc, unc[0]) || DirectoryHistoryMatches(unc, `\\offline.invalid\share\select~1`) {
		t.Fatal("UNC matching must remain exact lexical evidence")
	}
}

func TestDirectoryHistoryAliasesNoDistinctAlias(t *testing.T) {
	canonical := DirectoryIdentity(t.TempDir())
	// A successful query returning the long spelling models a volume without
	// a distinct short name, without inventing an 8.3 alias.
	got, err := directoryHistoryAliases(canonical, windows.GetLongPathName, windows.GetLongPathName)
	if err != nil || !reflect.DeepEqual(got, []string{canonical}) {
		t.Fatalf("canonical-only aliases = %v, %v", got, err)
	}
}

func TestDirectoryHistoryAliasesQueryError(t *testing.T) {
	failure := errors.New("alias query failed")
	fail := func(*uint16, *uint16, uint32) (uint32, error) { return 0, failure }
	for _, queries := range [][2]directoryPathQuery{
		{fail, windows.GetShortPathName}, {windows.GetLongPathName, fail},
	} {
		got, err := directoryHistoryAliases(DirectoryIdentity(t.TempDir()), queries[0], queries[1])
		if !errors.Is(err, failure) || got != nil {
			t.Fatalf("query failure became usable history aliases: %v, %v", got, err)
		}
	}
}

func TestDirectoryHistoryAliasesDoesNotQueryUNC(t *testing.T) {
	unexpected := func(*uint16, *uint16, uint32) (uint32, error) {
		t.Fatal("UNC selection triggered alias query")
		return 0, nil
	}
	directory := `\\Nonlocal.Invalid\Share\project`
	got, err := directoryHistoryAliases(directory, unexpected, unexpected)
	if err != nil || !reflect.DeepEqual(got, []string{directory}) {
		t.Fatalf("UNC aliases = %v, %v", got, err)
	}
}
