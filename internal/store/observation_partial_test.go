package store

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestResolveObservationReadFullUnchanged(t *testing.T) {
	content := "plain body"
	got, err := ResolveObservationRead(content, ObservationReadRequest{})
	if err != nil {
		t.Fatalf("ResolveObservationRead: %v", err)
	}
	if got.Mode != ObservationReadFull || got.Content != content {
		t.Fatalf("full read = %+v", got)
	}
	if got.TotalRunes != len([]rune(content)) {
		t.Fatalf("total runes = %d", got.TotalRunes)
	}
}

func TestResolveObservationReadRangeRuneSlice(t *testing.T) {
	content := "aé😊z" // runes: a é 😊 z
	offset := 1
	limit := 2
	got, err := ResolveObservationRead(content, ObservationReadRequest{Offset: &offset, Limit: &limit})
	if err != nil {
		t.Fatalf("ResolveObservationRead: %v", err)
	}
	if got.Content != "é😊" {
		t.Fatalf("range content = %q", got.Content)
	}
	if !utf8.ValidString(got.Content) {
		t.Fatal("range slice split a multi-byte rune")
	}
}

func TestResolveObservationReadLimitWithoutOffsetStartsAtZero(t *testing.T) {
	content := "abcdefghij"
	limit := 3
	got, err := ResolveObservationRead(content, ObservationReadRequest{Limit: &limit})
	if err != nil {
		t.Fatalf("ResolveObservationRead: %v", err)
	}
	if got.Offset != 0 || got.Content != "abc" {
		t.Fatalf("limit-only read = %+v", got)
	}
}

func TestResolveObservationReadOffsetPastEndIsEmpty(t *testing.T) {
	content := "abc"
	offset := 10
	got, err := ResolveObservationRead(content, ObservationReadRequest{Offset: &offset})
	if err != nil {
		t.Fatalf("ResolveObservationRead: %v", err)
	}
	if got.Content != "" {
		t.Fatalf("past-end content = %q", got.Content)
	}
	if got.Limit != DefaultPartialReadLimit {
		t.Fatalf("default limit = %d", got.Limit)
	}
}

func TestResolveObservationReadFindWindows(t *testing.T) {
	content := "xxTESTyyyyyTESTzz"
	find := "TEST"
	contextRunes := 2
	got, err := ResolveObservationRead(content, ObservationReadRequest{Find: &find, Context: &contextRunes})
	if err != nil {
		t.Fatalf("ResolveObservationRead: %v", err)
	}
	if len(got.Windows) != 2 {
		t.Fatalf("windows = %d", len(got.Windows))
	}
	if got.Windows[0].Offset != 0 || got.Windows[0].Content != "xxTESTyy" {
		t.Fatalf("first window = %+v", got.Windows[0])
	}
	if got.Windows[1].Offset != 9 || got.Windows[1].Content != "yyTESTzz" {
		t.Fatalf("second window = %+v", got.Windows[1])
	}
}

func TestResolveObservationReadFindMergesOverlappingOccurrences(t *testing.T) {
	content := "aaaaa"
	find := "aa"
	contextRunes := 0
	got, err := ResolveObservationRead(content, ObservationReadRequest{Find: &find, Context: &contextRunes})
	if err != nil {
		t.Fatalf("ResolveObservationRead: %v", err)
	}
	if got.MatchCount != 4 {
		t.Fatalf("match count = %d, want 4", got.MatchCount)
	}
	if len(got.Windows) != 1 {
		t.Fatalf("windows = %d, want 1", len(got.Windows))
	}
	if got.Windows[0].Offset != 0 || got.Windows[0].Content != content {
		t.Fatalf("merged window = %+v", got.Windows[0])
	}
}

func TestResolveObservationReadFindMergesAdjacentContexts(t *testing.T) {
	find := "a"
	contextRunes := 1
	got, err := ResolveObservationRead("abca", ObservationReadRequest{Find: &find, Context: &contextRunes})
	if err != nil {
		t.Fatalf("ResolveObservationRead: %v", err)
	}
	if got.MatchCount != 2 || len(got.Windows) != 1 {
		t.Fatalf("matches/windows = %d/%d, want 2/1", got.MatchCount, len(got.Windows))
	}
	if got.Windows[0].Offset != 0 || got.Windows[0].Content != "abca" {
		t.Fatalf("merged adjacent window = %+v", got.Windows[0])
	}
}

func TestResolveObservationReadFindDefaultContextDoesNotDuplicateContent(t *testing.T) {
	find := "needle"
	content := strings.Repeat("a", DefaultFindContext+100) + find + strings.Repeat("b", 100) + find + strings.Repeat("c", DefaultFindContext+100)
	got, err := ResolveObservationRead(content, ObservationReadRequest{Find: &find})
	if err != nil {
		t.Fatalf("ResolveObservationRead: %v", err)
	}
	if got.MatchCount != 2 || len(got.Windows) != 1 {
		t.Fatalf("matches/windows = %d/%d, want 2/1", got.MatchCount, len(got.Windows))
	}
	windowRunes := 0
	for _, window := range got.Windows {
		windowRunes += len([]rune(window.Content))
	}
	if windowRunes > len([]rune(content)) {
		t.Fatalf("window runes = %d, source runes = %d", windowRunes, len([]rune(content)))
	}
}

func TestResolveObservationReadFindWindowRunesStayBoundedAcrossIndependentWindows(t *testing.T) {
	find := "needle"
	contextRunes := 10
	content := strings.Repeat("a", 20) + find + strings.Repeat("b", 2_000) + find + strings.Repeat("c", 20)
	got, err := ResolveObservationRead(content, ObservationReadRequest{Find: &find, Context: &contextRunes})
	if err != nil {
		t.Fatalf("ResolveObservationRead: %v", err)
	}
	if got.MatchCount != 2 || len(got.Windows) != 2 {
		t.Fatalf("matches/windows = %d/%d, want 2/2", got.MatchCount, len(got.Windows))
	}
	windowRunes := 0
	for _, window := range got.Windows {
		windowRunes += len([]rune(window.Content))
	}
	if windowRunes > len([]rune(content)) {
		t.Fatalf("window runes = %d, source runes = %d", windowRunes, len([]rune(content)))
	}
}

func TestResolveObservationReadFindLargeNearMatch(t *testing.T) {
	find := strings.Repeat("a", 5_000) + "b"
	content := strings.Repeat("a", 10_000) + "b"
	contextRunes := 0
	got, err := ResolveObservationRead(content, ObservationReadRequest{Find: &find, Context: &contextRunes})
	if err != nil {
		t.Fatalf("ResolveObservationRead: %v", err)
	}
	if got.MatchCount != 1 || len(got.Windows) != 1 {
		t.Fatalf("matches/windows = %d/%d, want 1/1", got.MatchCount, len(got.Windows))
	}
	if got.Windows[0].Offset != 5_000 || got.Windows[0].Content != find {
		t.Fatalf("near-match window = %+v", got.Windows[0])
	}
}

func TestResolveObservationReadHugeLimitsClampToContent(t *testing.T) {
	huge := int(^uint(0) >> 1)
	cases := []struct {
		name string
		req  ObservationReadRequest
	}{
		{"range limit", ObservationReadRequest{Limit: &huge}},
		{"find context", func() ObservationReadRequest {
			find := "cd"
			return ObservationReadRequest{Find: &find, Context: &huge}
		}()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveObservationRead("abcdef", tc.req)
			if err != nil {
				t.Fatalf("ResolveObservationRead: %v", err)
			}
			if tc.req.Find == nil && got.Content != "abcdef" {
				t.Fatalf("range content = %q", got.Content)
			}
			if tc.req.Find != nil && (len(got.Windows) != 1 || got.Windows[0].Content != "abcdef") {
				t.Fatalf("find windows = %+v", got.Windows)
			}
		})
	}
}

func TestResolveObservationReadFindNotFound(t *testing.T) {
	find := "missing"
	got, err := ResolveObservationRead("hello", ObservationReadRequest{Find: &find})
	if err != nil {
		t.Fatalf("ResolveObservationRead: %v", err)
	}
	if len(got.Windows) != 0 {
		t.Fatalf("expected zero windows, got %d", len(got.Windows))
	}
}

func TestResolveObservationReadFindRuneOffset(t *testing.T) {
	content := "áéTEST"
	find := "TEST"
	contextRunes := 0
	got, err := ResolveObservationRead(content, ObservationReadRequest{Find: &find, Context: &contextRunes})
	if err != nil {
		t.Fatalf("ResolveObservationRead: %v", err)
	}
	if len(got.Windows) != 1 || got.Windows[0].Offset != 2 {
		t.Fatalf("rune offset window = %+v", got.Windows)
	}
}

func TestResolveObservationReadValidation(t *testing.T) {
	offset := 0
	find := "x"
	contextRunes := 1
	neg := -1

	cases := []struct {
		name string
		req  ObservationReadRequest
		want error
	}{
		{"both modes", ObservationReadRequest{Offset: &offset, Find: &find}, ErrPartialReadModesExclusive},
		{"context without find", ObservationReadRequest{Context: &contextRunes}, ErrContextRequiresFind},
		{"negative offset", ObservationReadRequest{Offset: &neg}, ErrPartialReadOffsetNegative},
		{"negative limit", ObservationReadRequest{Limit: &neg}, ErrPartialReadLimitNegative},
		{"negative context", ObservationReadRequest{Find: &find, Context: &neg}, ErrPartialReadContextNegative},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ResolveObservationRead("body", tc.req)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestResolveObservationReadDefaultContext(t *testing.T) {
	find := "needle"
	pad := strings.Repeat("a", DefaultFindContext+10)
	content := pad + find + pad
	got, err := ResolveObservationRead(content, ObservationReadRequest{Find: &find})
	if err != nil {
		t.Fatalf("ResolveObservationRead: %v", err)
	}
	if len(got.Windows) != 1 {
		t.Fatalf("windows = %d", len(got.Windows))
	}
	wantLen := DefaultFindContext + len([]rune(find)) + DefaultFindContext
	if len([]rune(got.Windows[0].Content)) != wantLen {
		t.Fatalf("default context window runes = %d, want %d", len([]rune(got.Windows[0].Content)), wantLen)
	}
}
