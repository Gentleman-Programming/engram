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
		result.Windows = findContentWindows(runes, find, contextRunes)
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
	end := offset + limit
	if end > len(runes) {
		end = len(runes)
	}
	return string(runes[offset:end])
}

func findContentWindows(runes []rune, find string, contextRunes int) []ObservationWindow {
	needle := []rune(find)
	if len(needle) == 0 {
		return nil
	}
	var windows []ObservationWindow
	for i := 0; i <= len(runes)-len(needle); {
		if !runePrefixEqual(runes[i:], needle) {
			i++
			continue
		}
		start := i - contextRunes
		if start < 0 {
			start = 0
		}
		end := i + len(needle) + contextRunes
		if end > len(runes) {
			end = len(runes)
		}
		windows = append(windows, ObservationWindow{
			Offset:  start,
			Content: string(runes[start:end]),
		})
		i += len(needle)
	}
	return windows
}

func runePrefixEqual(haystack, needle []rune) bool {
	if len(haystack) < len(needle) {
		return false
	}
	for i, r := range needle {
		if haystack[i] != r {
			return false
		}
	}
	return true
}
