package store

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// Frozen matching algorithm from main9567b54b. Normalization remains unchanged.
func legacyCandidateRelevance(encoded, sourceTopic, title string, topic *string) bool {
	if sourceTopic != "" && topic != nil && sourceTopic == *topic {
		return true
	}
	var sourceTerms []string
	if encoded != "" {
		sourceTerms = strings.Split(encoded, "\x1f")
	}
	targets := saveCandidateTerms(title)
	shared := 0
	for _, source := range sourceTerms {
		for _, target := range targets {
			if source == target {
				shared++
				break
			}
		}
	}
	return shared >= 2 || (shared == 1 && len(sourceTerms) == 1 && len(targets) == 1)
}

func TestSaveCandidateRelevanceEquivalent(t *testing.T) {
	titles := []string{"", "the and fix", "quartz", "quartz quartz", "the quartz and", "quartz extra", "quartz anchor", "QUARTZ ANCHOR", "quartz quartz unrelated", "C++ C#", "C C++", "área decisión", "ÁREA DECISIÓN", "Σ σ ς", "İ i ı", "longſ s", "東京 保存", "+++ ###", "quartz\x00anchor", "quartz\x1fanchor", "quartz\xffanchor", "café cafe\u0301", "#tag +flag"}
	var manyTerms []string
	for i := 0; i < 64; i++ {
		manyTerms = append(manyTerms, fmt.Sprintf("term%d", i))
	}
	titles = append(titles, strings.Join(manyTerms, " "), strings.Repeat("quartz quartz unrelated ", 256))
	same := "policy/shared"
	different := "policy/other"
	empty := ""
	check := func(source, title string, topic *string) {
		t.Helper()
		encoded := strings.Join(saveCandidateTerms(source), "\x1f")
		want := legacyCandidateRelevance(encoded, same, title, topic)
		if got := saveCandidateRelevant(encoded, same, title, topic); got != want {
			t.Fatalf("source=%q target=%q topic=%v got%v want%v", source, title, topic, got, want)
		}
	}
	for _, source := range titles {
		for _, target := range titles {
			for _, topic := range []*string{nil, &same, &different, &empty} {
				check(source, target, topic)
			}
		}
	}
	rng := rand.New(rand.NewSource(1536))
	atoms := []string{"quartz", "anchor", "beacon", "policy", "the", "fix", "C++", "C#", "東京", "ÁREA", "σ", "ς", "i", "İ", "+++", "#", "\xff"}
	separators := []string{" ", "-", "/", "\x00", "\x1f", "\t"}
	randomTitle := func() string {
		var b strings.Builder
		for n := rng.Intn(12); n > 0; n-- {
			b.WriteString(atoms[rng.Intn(len(atoms))])
			b.WriteString(separators[rng.Intn(len(separators))])
		}
		return b.String()
	}
	for i := 0; i < 10000; i++ {
		check(randomTitle(), randomTitle(), nil)
	}
}

func TestSaveCandidateEncodingContainsWholeTerms(t *testing.T) {
	for _, tc := range []struct {
		encoded, term string
		want          bool
	}{
		{"anchor\x1fquartz", "anchor", true},
		{"anchor\x1fquartz", "quartz", true},
		{"anchorite\x1fquartz", "anchor", false},
		{"xanchor\x1fquartz", "anchor", false},
		{"xanchor\x1fanchor\x1fquartz", "anchor", true},
		{"c++\x1fc#", "c", false},
		{"c++\x1fc#", "c#", true},
		{"東京\x1f保存", "京", false},
		{"東京\x1f保存", "保存", true},
		{strings.Repeat("a", 4096) + "\x1fquartz", strings.Repeat("a", 2048), false},
	} {
		if got := saveCandidateEncodingContains(tc.encoded, tc.term); got != tc.want {
			t.Fatalf("encoded=%q term=%q got%v want%v", tc.encoded, tc.term, got, tc.want)
		}
	}
}

func BenchmarkSaveCandidateRelevance(b *testing.B) {
	var manyTerms []string
	for i := 0; i < 64; i++ {
		manyTerms = append(manyTerms, fmt.Sprintf("term%d", i))
	}
	for _, tc := range []struct{ name, source, title string }{
		{"unrelated", "quartz anchor beacon policy", "unrelated background retention 1234"},
		{"repeated-target", "quartz anchor beacon policy", strings.Repeat("unrelated ", 256)},
		{"many-source-terms", strings.Join(manyTerms, " "), "unrelated background retention 1234"},
		{"uppercase-match", "quartz anchor beacon policy", "QUARTZ ANCHOR"},
		{"single-term", "quartz", "the QUARTZ quartz"},
	} {
		b.Run(tc.name, func(b *testing.B) {
			encoded := strings.Join(saveCandidateTerms(tc.source), "\x1f")
			want := legacyCandidateRelevance(encoded, "", tc.title, nil)
			for _, name := range []string{"Legacy", "Iterator"} {
				b.Run(name, func(b *testing.B) {
					match := saveCandidateRelevant
					if name == "Legacy" {
						match = legacyCandidateRelevance
					}
					b.ReportAllocs()
					for i := 0; i < b.N; i++ {
						if got := match(encoded, "", tc.title, nil); got != want {
							b.Fatalf("got %v want %v", got, want)
						}
					}
				})
			}
		})
	}
}
