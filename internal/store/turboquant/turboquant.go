package turboquant

import (
	"container/heap"
	"encoding/gob"
	"hash/fnv"
	"math/bits"
	"os"
	"strings"
	"sync"
	"unicode"
)

// BlockSignature represents the quantized 64-bit footprint of a context block
type BlockSignature uint64

type CacheEntry struct {
	Signature BlockSignature
	Offset    int64
}

// TurboCache is the ultra-lightweight memory store footprint.
// We keep the hash signatures and their location offsets in contiguous memory.
type TurboCache struct {
	mu         sync.RWMutex
	Entries    []CacheEntry  // Contiguous slice for CPU cache locality (LSH friendly)
	IDToOffset map[int64]int // Map ID (Offset) to index in Entries for O(1) lookup
}

// NewTurboCache creates an empty, lightweight memory index
func NewTurboCache() *TurboCache {
	return &TurboCache{
		Entries:    make([]CacheEntry, 0),
		IDToOffset: make(map[int64]int),
	}
}

// Add safely inserts a new signature and offset into the cache.
func (tc *TurboCache) Add(sig BlockSignature, offset int64) {
	tc.mu.Lock()
	defer tc.mu.Unlock()

	if idx, exists := tc.IDToOffset[offset]; exists {
		tc.Entries[idx].Signature = sig
		return
	}

	tc.IDToOffset[offset] = len(tc.Entries)
	tc.Entries = append(tc.Entries, CacheEntry{Signature: sig, Offset: offset})
}

// Reset clears all entries from the cache.
func (tc *TurboCache) Reset() {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	tc.Entries = make([]CacheEntry, 0)
	tc.IDToOffset = make(map[int64]int)
}

// Remove deletes all entries associated with a specific offset (ID).
// This prevents memory leaks and stale signatures after updates/deletes.
func (tc *TurboCache) Remove(offset int64) {
	tc.mu.Lock()
	defer tc.mu.Unlock()

	idx, exists := tc.IDToOffset[offset]
	if !exists {
		return
	}

	// Swap with last element for O(1) removal
	lastIdx := len(tc.Entries) - 1
	if idx != lastIdx {
		lastEntry := tc.Entries[lastIdx]
		tc.Entries[idx] = lastEntry
		tc.IDToOffset[lastEntry.Offset] = idx
	}

	tc.Entries = tc.Entries[:lastIdx]
	delete(tc.IDToOffset, offset)
}

// Size returns the total number of entries in the cache.
func (tc *TurboCache) Size() int {
	tc.mu.RLock()
	defer tc.mu.RUnlock()
	return len(tc.Entries)
}

// GetExact performs an exact signature match.
func (tc *TurboCache) GetExact(sig BlockSignature) (int64, bool) {
	tc.mu.RLock()
	defer tc.mu.RUnlock()
	for _, entry := range tc.Entries {
		if entry.Signature == sig {
			return entry.Offset, true
		}
	}
	return -1, false
}

// Save persists the TurboCache index to disk using gob encoding for extreme efficiency.
func (tc *TurboCache) Save(path string) error {
	tc.mu.RLock()
	defer tc.mu.RUnlock()

	tmpPath := path + ".tmp"
	file, err := os.Create(tmpPath)
	if err != nil {
		return err
	}

	encoder := gob.NewEncoder(file)
	if err := encoder.Encode(tc.Entries); err != nil {
		file.Close()
		os.Remove(tmpPath)
		return err
	}

	// Ensure data is on disk before renaming
	if err := file.Sync(); err != nil {
		file.Close()
		os.Remove(tmpPath)
		return err
	}
	file.Close()

	// Atomic swap (best effort on Windows, standard on POSIX)
	return os.Rename(tmpPath, path)
}

// Load populates the TurboCache index from a file on disk.
func (tc *TurboCache) Load(path string) error {
	tc.mu.Lock()
	defer tc.mu.Unlock()

	file, err := os.Open(path)
	if err != nil {
		return err
	}

	decoder := gob.NewDecoder(file)
	var loaded []CacheEntry
	decodeErr := decoder.Decode(&loaded)

	closeErr := file.Close()
	if decodeErr != nil {
		return decodeErr
	}

	tc.Entries = loaded

	// Rebuild map for O(1) removals after loading
	tc.IDToOffset = make(map[int64]int)
	for i, entry := range tc.Entries {
		tc.IDToOffset[entry.Offset] = i
	}

	return closeErr
}

var (
	diacriticReplacer = strings.NewReplacer(
		"á", "a", "é", "e", "í", "i", "ó", "o", "ú", "u",
		"ü", "u", "ñ", "n",
		"Á", "A", "É", "E", "Í", "I", "Ó", "O", "Ú", "U",
		"Ü", "U", "Ñ", "N",
	)

	// stopWords is now package-level to avoid re-allocation
	stopWords = map[string]bool{
		"the": true, "and": true, "is": true, "in": true, "a": true, "to": true, "of": true,
		"are": true, "with": true, "for": true, "all": true, "using": true, "use": true,
		"this": true, "that": true, "from": true, "been": true, "has": true, "have": true, "will": true,
		"el": true, "la": true, "y": true, "en": true, "es": true, "para": true, "por": true,
		"que": true, "un": true, "una": true, "con": true, "los": true, "las": true,
		"del": true, "como": true, "mas": true, "pero": true, "sus": true, "este": true, "esta": true,
	}

	// tokenFunc global to avoid closure allocations and clean punctuation
	tokenFunc = func(c rune) bool {
		return !unicode.IsLetter(c) && !unicode.IsNumber(c)
	}
)

// normalizeText removes common diacritics for Spanish/English robustness
// without introducing external dependencies (like golang.org/x/text)
func normalizeText(text string) string {
	return diacriticReplacer.Replace(text)
}

// ComputeSimHash extracts a 64-bit locality-sensitive hash (LSH) from the block text.
// This is the core "quantization" step that requires zero neural networks.
func ComputeSimHash(text string) BlockSignature {
	var v [64]int // Stack allocated fixed array

	// Normalize text before processing
	cleanText := normalizeText(text)

	// Precise tokenizer: split by non-alphanumeric, keep words >= 3 chars
	words := strings.FieldsFunc(strings.ToLower(cleanText), tokenFunc)

	h := fnv.New64a()

	for _, word := range words {
		if len(word) < 3 || stopWords[word] {
			continue
		}

		// Compute FNV-1a 64-bit hash for the word without allocating []byte
		h.Reset()
		var b [1]byte
		for i := 0; i < len(word); i++ {
			b[0] = word[i]
			h.Write(b[:])
		}
		wordHash := h.Sum64()

		// Update the 64-bit vector
		for i := 0; i < 64; i++ {
			bit := (wordHash >> i) & 1
			if bit == 1 {
				v[i]++
			} else {
				v[i]--
			}
		}
	}

	// Reconstruct the final 64-bit signature according to SimHash logic
	var signature uint64
	for i := 0; i < 64; i++ {
		if v[i] > 0 {
			signature |= 1 << i
		}
	}

	return BlockSignature(signature)
}

// HammingDistance calculates the distance between two signatures utilizing native CPU instructions.
// The lower the distance, the more semantically similar the blocks are.
func HammingDistance(a, b BlockSignature) int {
	return bits.OnesCount64(uint64(a ^ b))
}

// FindNearest searches the cache for the signature with the lowest Hamming distance.
func (tc *TurboCache) FindNearest(query BlockSignature) (BlockSignature, int64, int) {
	tc.mu.RLock()
	defer tc.mu.RUnlock()

	if len(tc.Entries) == 0 {
		return 0, -1, 64
	}

	var bestMatch BlockSignature
	var bestOffset int64 = -1
	minDist := 65 // Max distance is 64

	// Sequential scan over contiguous memory block -> Cache L1/L2 hits on every cycle.
	for _, entry := range tc.Entries {
		dist := HammingDistance(entry.Signature, query)
		if dist < minDist {
			minDist = dist
			bestMatch = entry.Signature
			bestOffset = entry.Offset
		}
	}

	return bestMatch, bestOffset, minDist
}

type NearestMatch struct {
	ID       int64
	Distance int
}

type matchHeap []NearestMatch

func (h matchHeap) Len() int           { return len(h) }
func (h matchHeap) Less(i, j int) bool { return h[i].Distance > h[j].Distance } // Max-heap to keep smallest distances
func (h matchHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *matchHeap) Push(x any)        { *h = append(*h, x.(NearestMatch)) }
func (h *matchHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[0 : n-1]
	return x
}

// FindNearestN searches the cache for the top N signatures with the lowest Hamming distance.
func (tc *TurboCache) FindNearestN(query BlockSignature, n int) []NearestMatch {
	if n <= 0 {
		return nil
	}

	tc.mu.RLock()
	defer tc.mu.RUnlock()

	if len(tc.Entries) == 0 {
		return nil
	}

	h := &matchHeap{}
	*h = make(matchHeap, 0, n)
	heap.Init(h)

	for _, entry := range tc.Entries {
		dist := HammingDistance(entry.Signature, query)
		if h.Len() < n {
			heap.Push(h, NearestMatch{ID: entry.Offset, Distance: dist})
		} else if dist < (*h)[0].Distance {
			heap.Pop(h)
			heap.Push(h, NearestMatch{ID: entry.Offset, Distance: dist})
		}
	}

	results := make([]NearestMatch, h.Len())
	for i := h.Len() - 1; i >= 0; i-- {
		results[i] = heap.Pop(h).(NearestMatch)
	}

	return results
}
