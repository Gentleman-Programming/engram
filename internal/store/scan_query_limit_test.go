package store

import (
	"errors"
	"strings"
	"testing"
)

func TestScanCandidateBatchQueryLimit(t *testing.T) {
	s := scanBatchStore(t)
	for _, title := range []string{strings.Repeat("a", 65536), strings.Repeat("x ", 769)} {
		if _, err := s.scanCandidateBatch([]scanSourceRow{{id: 1, title: title}}, 10); err != nil {
			t.Fatalf("valid candidate query: %v", err)
		}
	}
	// Every source must be checked before any FTS query. A closed database
	// proves a valid earlier source cannot mask the later input-limit error.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for _, title := range []string{strings.Repeat("a", 65537), strings.Repeat("é", 32769)} {
		_, err := s.scanCandidateBatch([]scanSourceRow{{id: 1, title: "valid source"}, {id: 2, title: title}}, 10)
		if !errors.Is(err, ErrFTSQueryTooLarge) {
			t.Fatalf("batch limit bypass: %v", err)
		}
		if strings.Contains(err.Error(), title) {
			t.Fatal("limit error leaks source title")
		}
	}
}
