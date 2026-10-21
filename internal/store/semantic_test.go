package store

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestSemanticCacheRejectsNullComponents(t *testing.T) {
	p := semanticProviderFake()
	p.vectors = [][]float64{{1, 1}}
	s := semanticStore(t, p)
	id := semanticAdd(t, s, "source", "engram", "project", "note")
	before := semanticGet(t, s, id)
	if _, err := s.BuildSemanticIndex(context.Background(), SearchOptions{}); err != nil {
		t.Fatal(err)
	}
	readCache := func() (string, string, string) {
		t.Helper()
		var cache, model, stamp string
		if err := s.db.QueryRow(`SELECT embedding, embedding_model, embedding_created_at FROM observations WHERE id=?`, id).Scan(&cache, &model, &stamp); err != nil {
			t.Fatal(err)
		}
		return cache, model, stamp
	}
	original, model, stamp := readCache()
	mutations := semanticMutationCount(t, s)
	for _, tc := range []struct {
		name, vector string
		valid        bool
		score        float64
	}{
		{name: "null component", vector: `[null,1]`},
		{name: "null vector", vector: `null`},
		{name: "invalid shape", vector: `{"0":1,"1":1}`},
		{name: "numeric zero", vector: `[0,1]`, valid: true, score: 1 / math.Sqrt(2)},
		{name: "negative component", vector: `[-1,0]`, valid: true, score: -1 / math.Sqrt(2)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var envelope map[string]json.RawMessage
			if err := json.Unmarshal([]byte(original), &envelope); err != nil {
				t.Fatal(err)
			}
			envelope["vector"] = json.RawMessage(tc.vector)
			fault, err := json.Marshal(envelope)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(`UPDATE observations SET embedding=? WHERE id=?`, fault, id); err != nil {
				t.Fatal(err)
			}
			hits, searchErr := s.SearchContext(context.Background(), "source", SearchOptions{SearchMode: "semantic"})
			if !reflect.DeepEqual(before, semanticGet(t, s, id)) || semanticMutationCount(t, s) != mutations {
				t.Error("search changed source/counters/sync")
			}
			cached, afterModel, afterStamp := readCache()
			if cached != string(fault) || afterModel != model || afterStamp != stamp {
				t.Error("search modified cache")
			}
			if !tc.valid {
				if hits != nil || !errors.Is(searchErr, ErrSemanticIndex) {
					t.Fatalf("malformed vector must fail closed: hits=%+v error=%v", hits, searchErr)
				}
			} else if searchErr != nil || len(hits) != 1 || hits[0].Similarity == nil || math.Abs(*hits[0].Similarity-tc.score) > 1e-15 {
				t.Fatalf("valid numeric vector: hits=%+v error=%v", hits, searchErr)
			}
		})
	}
}

func TestSemanticUnknownMode(t *testing.T) {
	var opts SearchOptions
	if err := json.Unmarshal([]byte(`{"search_mode":"hybrid"}`), &opts); err != nil {
		t.Fatal(err)
	}
	s := &Store{}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("unknown mode reached database: %v", r)
		}
	}()
	_, err := s.SearchContext(context.Background(), "question", opts)
	if err == nil || !strings.Contains(err.Error(), "invalid search_mode") {
		t.Fatalf("got %v", err)
	}
}

func TestSemanticPreservesSourceBytes(t *testing.T) {
	p := semanticProviderFake()
	s := semanticStore(t, p)
	content := "raw-" + string([]byte{0xff})
	id, err := s.AddObservation(AddObservationParams{SessionID: "semantic-test", Title: "sourcebytes", Content: content, Project: "engram", Type: "note"})
	if err != nil {
		t.Fatal(err)
	}
	source := semanticGet(t, s, id)
	if _, err := s.BuildSemanticIndex(context.Background(), SearchOptions{}); err != nil {
		t.Fatal(err)
	}
	hits, err := s.Search("question", SearchOptions{SearchMode: "semantic"})
	if err != nil || len(hits) != 1 {
		t.Fatalf("hits %v %v", hits, err)
	}
	if !reflect.DeepEqual(source, &hits[0].Observation) {
		t.Fatalf("source bytes changed: want %q got %q", source.Content, hits[0].Content)
	}
	lexical, err := s.SearchPreviewsContext(context.Background(), "sourcebytes", SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	semantic, err := s.SearchPreviewsContext(context.Background(), "question", SearchOptions{SearchMode: "semantic"})
	if err != nil {
		t.Fatal(err)
	}
	if lexical[0].Preview != semantic[0].Preview || lexical[0].Truncated != semantic[0].Truncated {
		t.Fatalf("raw preview mismatch %q %q", lexical[0].Preview, semantic[0].Preview)
	}
}

func TestSemanticPreviewMatchesLexicalNUL(t *testing.T) {
	p := semanticProviderFake()
	s := semanticStore(t, p)
	_, err := s.AddObservation(AddObservationParams{SessionID: "semantic-test", Title: "nulpreview", Content: "visible\x00hidden", Project: "engram", Type: "note"})
	if err != nil {
		t.Fatal(err)
	}
	lexical, err := s.SearchPreviewsContext(context.Background(), "nulpreview", SearchOptions{})
	if err != nil || len(lexical) != 1 {
		t.Fatalf("lexical %v %v", lexical, err)
	}
	if _, err := s.BuildSemanticIndex(context.Background(), SearchOptions{}); err != nil {
		t.Fatal(err)
	}
	semantic, err := s.SearchPreviewsContext(context.Background(), "question", SearchOptions{SearchMode: "semantic"})
	if err != nil || len(semantic) != 1 {
		t.Fatalf("semantic %v %v", semantic, err)
	}
	semantic[0].Similarity = nil
	semantic[0].RetrievalMode = ""
	semantic[0].Rank = lexical[0].Rank
	if !reflect.DeepEqual(lexical, semantic) {
		t.Fatalf("preview contract differs: lexical=%+v semantic=%+v", lexical, semantic)
	}
}
