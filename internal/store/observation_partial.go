package store

const (
	// DefaultPartialReadLimit is the rune window used when limit is omitted
	// from a ranged mem_get_observation read.
	DefaultPartialReadLimit = 2000
	// DefaultFindContext is the rune padding on each side of a find match
	// when context is omitted.
	DefaultFindContext = 600
)

// ObservationReadMode is how mem_get_observation selects content.
type ObservationReadMode int

const (
	ObservationReadFull ObservationReadMode = iota
	ObservationReadRange
	ObservationReadFind
)

// ObservationReadRequest is the optional partial-read contract for GetObservation.
// Pointers distinguish omitted values from explicit zeros.
type ObservationReadRequest struct {
	Offset  *int
	Limit   *int
	Find    *string
	Context *int
}

// ObservationWindow is one rune-indexed slice of observation content.
type ObservationWindow struct {
	Offset  int
	Content string
}

// ObservationReadResult is the resolved partial or full read of one body.
type ObservationReadResult struct {
	Mode       ObservationReadMode
	TotalRunes int
	Offset     int
	Limit      int
	Find       string
	MatchCount int
	Windows    []ObservationWindow
	Content    string
}

func (r ObservationReadRequest) rangeRequested() bool {
	return r.Offset != nil || r.Limit != nil
}

func (r ObservationReadRequest) findRequested() bool {
	return r.Find != nil || r.Context != nil
}

// ResolveObservationRead applies the issue #812 validation and rune-indexed
// slice rules to a stored body. Offsets and limits are counted in runes.
func ResolveObservationRead(content string, req ObservationReadRequest) (ObservationReadResult, error) {
	runes := []rune(content)
	result := ObservationReadResult{TotalRunes: len(runes)}

	if req.rangeRequested() && req.findRequested() {
		return ObservationReadResult{}, ErrPartialReadModesExclusive
	}
	if req.Context != nil && req.Find == nil {
		return ObservationReadResult{}, ErrContextRequiresFind
	}
	if req.Offset != nil && *req.Offset < 0 {
		return ObservationReadResult{}, ErrPartialReadOffsetNegative
	}
	if req.Limit != nil && *req.Limit < 0 {
		return ObservationReadResult{}, ErrPartialReadLimitNegative
	}
	if req.Context != nil && *req.Context < 0 {
		return ObservationReadResult{}, ErrPartialReadContextNegative
	}

	if !req.rangeRequested() && !req.findRequested() {
		result.Mode = ObservationReadFull
		result.Content = content
		return result, nil
	}

	if req.findRequested() {
		result.Mode = ObservationReadFind
		find := ""
		if req.Find != nil {
			find = *req.Find
		}
		contextRunes := DefaultFindContext
		if req.Context != nil {
			contextRunes = *req.Context
		}
		result.Find = find
		result.Windows, result.MatchCount = findContentWindows(runes, find, contextRunes)
		return result, nil
	}

	offset := 0
	if req.Offset != nil {
		offset = *req.Offset
	}
	limit := DefaultPartialReadLimit
	if req.Limit != nil {
		limit = *req.Limit
	}
	result.Mode = ObservationReadRange
	result.Offset = offset
	result.Limit = limit
	result.Content = sliceRunes(runes, offset, limit)
	result.Windows = []ObservationWindow{{Offset: offset, Content: result.Content}}
	return result, nil
}

func sliceRunes(runes []rune, offset, limit int) string {
	if offset >= len(runes) || limit == 0 {
		return ""
	}
	if limit >= len(runes)-offset {
		return string(runes[offset:])
	}
	return string(runes[offset : offset+limit])
}

type observationInterval struct {
	start int
	end   int
}

func findContentWindows(runes []rune, find string, contextRunes int) ([]ObservationWindow, int) {
	needle := []rune(find)
	if len(needle) == 0 {
		return nil, 0
	}
	prefix := runePrefixTable(needle)
	merged := make([]observationInterval, 0)
	matchCount := 0
	matched := 0
	for i, r := range runes {
		for matched > 0 && r != needle[matched] {
			matched = prefix[matched-1]
		}
		if r == needle[matched] {
			matched++
		}
		if matched != len(needle) {
			continue
		}

		matchCount++
		matchStart := i - len(needle) + 1
		matchEnd := i + 1
		start := 0
		if contextRunes < matchStart {
			start = matchStart - contextRunes
		}
		end := matchEnd
		if contextRunes >= len(runes)-end {
			end = len(runes)
		} else {
			end += contextRunes
		}
		appendMergedObservationInterval(&merged, observationInterval{start: start, end: end})
		matched = prefix[matched-1]
	}

	windows := make([]ObservationWindow, 0, len(merged))
	for _, interval := range merged {
		windows = append(windows, ObservationWindow{
			Offset:  interval.start,
			Content: string(runes[interval.start:interval.end]),
		})
	}
	return windows, matchCount
}

func appendMergedObservationInterval(merged *[]observationInterval, interval observationInterval) {
	last := len(*merged) - 1
	if last < 0 || interval.start > (*merged)[last].end {
		*merged = append(*merged, interval)
		return
	}
	if interval.end > (*merged)[last].end {
		(*merged)[last].end = interval.end
	}
}

func runePrefixTable(needle []rune) []int {
	prefix := make([]int, len(needle))
	matched := 0
	for i := 1; i < len(needle); i++ {
		for matched > 0 && needle[i] != needle[matched] {
			matched = prefix[matched-1]
		}
		if needle[i] == needle[matched] {
			matched++
		}
		prefix[i] = matched
	}
	return prefix
}
