package project

import (
	"errors"
	"reflect"
	"testing"

	"golang.org/x/sys/windows"
)

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
