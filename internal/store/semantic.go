package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/Gentleman-Programming/engram/v3/internal/embedding"
)

// SemanticProvider is injected per Store. Lexical operations never consult it.
type SemanticProvider interface {
	Resolve(context.Context) (embedding.Identity, error)
	Embed(context.Context, embedding.Identity, []string) ([][]float64, error)
}

var (
	ErrSemanticUnconfigured = errors.New("semantic provider unconfigured: configure a local embedding provider")
	ErrSemanticIndex        = errors.New("semantic index missing, stale or invalid: rebuild with BuildSemanticIndex")
	ErrSemanticChanged      = errors.New("semantic source, cache or model changed: retry BuildSemanticIndex or search")
	ErrSemanticVector       = errors.New("invalid semantic vector: configure provider and rebuild with BuildSemanticIndex")
)

func validateSearchMode(opts SearchOptions) error {
	switch opts.SearchMode {
	case "", "lexical":
		return nil
	case "semantic":
		if opts.MatchMode != "" {
			return fmt.Errorf("semantic search does not accept match_mode; omit it")
		}
		return nil
	default:
		return fmt.Errorf("invalid search_mode %q: must be \"lexical\" or \"semantic\"", opts.SearchMode)
	}
}

// SemanticIndexResult counts the complete filtered corpus, not the result limit.
type SemanticIndexResult struct {
	Indexed int `json:"indexed"`
}

// semanticVector keeps version-1 numeric-array encoding while rejecting JSON
// null components, which encoding/json otherwise silently coerces to zero.
type semanticVector []float64

func (v *semanticVector) UnmarshalJSON(data []byte) error {
	var components []*float64
	if err := json.Unmarshal(data, &components); err != nil {
		return err
	}
	if components == nil {
		return ErrSemanticVector
	}
	decoded := make(semanticVector, len(components))
	for i, component := range components {
		if component == nil {
			return ErrSemanticVector
		}
		decoded[i] = *component
	}
	*v = decoded
	return nil
}

type semanticEnvelope struct {
	Version    int                `json:"version"`
	Identity   embedding.Identity `json:"identity"`
	Dimensions int                `json:"dimensions"`
	Source     string             `json:"source"`
	Vector     semanticVector     `json:"vector"`
}

type semanticRow struct {
	preview   string
	truncated bool
	result    SearchResult
	source    string
	cache     []byte
	model     sql.NullString
	cacheTime sql.NullString
}

// semanticSourceScanner reuses the canonical observation scanner while also
// collecting all raw source/cache columns from the same SQLite row snapshot.
type semanticSourceScanner struct {
	rows      *sql.Rows
	columns   []string
	values    []any
	preview   string
	truncated bool
}

func (s *semanticSourceScanner) Scan(dest ...any) error {
	dest = append(dest, &s.preview, &s.truncated)
	s.columns = s.columns[len(dest):]
	s.values = make([]any, len(s.columns))
	for i := range s.values {
		dest = append(dest, &s.values[i])
	}
	return s.rows.Scan(dest...)
}

type semanticQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// Snapshot uses the exact lexical visibility predicates, without FTS or LIMIT.
// All source columns (including local provenance) participate in the fingerprint.
// Cache fields alone are excluded. Reading all columns also prevents accidentally
// overlooking future source metadata when detecting concurrent changes.
func semanticSnapshot(ctx context.Context, q semanticQuerier, opts SearchOptions) ([]semanticRow, error) {
	opts.Project, _ = NormalizeProject(opts.Project)
	query := `SELECT ` + observationSelectColumns + `, substr(content, 1, 300), length(content) > 300, * FROM observations WHERE deleted_at IS NULL`
	args := []any{}
	if opts.Type != "" {
		query += " AND type = ?"
		args = append(args, opts.Type)
	}
	if opts.Project != "" {
		query += " AND LOWER(project) = ?"
		args = append(args, opts.Project)
	}
	if opts.Scope != "" {
		query += " AND scope = ?"
		args = append(args, normalizeScope(opts.Scope))
	}
	query += " ORDER BY id ASC"
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := []semanticRow{}
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var r semanticRow
		scanner := semanticSourceScanner{rows: rows, columns: columns}
		if err := scanObservationRow(&scanner, &r.result.Observation); err != nil {
			return nil, err
		}
		r.preview, r.truncated = scanner.preview, scanner.truncated
		source := map[string]any{}
		for i, name := range scanner.columns {
			v := scanner.values[i]
			switch name {
			case "embedding":
				if v != nil {
					switch b := v.(type) {
					case []byte:
						r.cache = append([]byte(nil), b...)
					case string:
						r.cache = []byte(b)
					}
				}
			case "embedding_model":
				if v != nil {
					r.model = sql.NullString{String: fmt.Sprint(v), Valid: true}
				}
			case "embedding_created_at":
				if v != nil {
					r.cacheTime = sql.NullString{String: fmt.Sprint(v), Valid: true}
				}
			default:
				if b, ok := v.([]byte); ok {
					v = string(b)
				}
				source[name] = v
			}
		}
		// Preserve raw text bytes in the fingerprint. JSON string encoding is
		// otherwise lossy for invalid UTF-8; byte slices encode as base64.
		fingerprint := make(map[string]any, len(source))
		for name, value := range source {
			if text, ok := value.(string); ok {
				fingerprint[name] = []byte(text)
			} else {
				fingerprint[name] = value
			}
		}
		encoded, err := json.Marshal(fingerprint)
		if err != nil {
			return nil, err
		}
		hash := sha256.Sum256(encoded)
		r.source = hex.EncodeToString(hash[:])
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func sameSemanticSnapshot(a, b []semanticRow) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].source != b[i].source || !bytes.Equal(a[i].cache, b[i].cache) || a[i].model != b[i].model || a[i].cacheTime != b[i].cacheTime {
			return false
		}
	}
	return true
}

func (s *Store) semanticProvider(ctx context.Context, opts SearchOptions) (SemanticProvider, error) {
	if err := validateSearchMode(opts); err != nil {
		return nil, err
	}
	if opts.MatchMode != "" {
		return nil, fmt.Errorf("semantic indexing does not accept match_mode; omit it")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.cfg.EmbeddingProvider == nil {
		return nil, ErrSemanticUnconfigured
	}
	return s.cfg.EmbeddingProvider, nil
}

func resolveSemantic(ctx context.Context, p SemanticProvider) (embedding.Identity, error) {
	if err := ctx.Err(); err != nil {
		return embedding.Identity{}, err
	}
	id, err := p.Resolve(ctx)
	if err != nil {
		return id, fmt.Errorf("semantic provider: configure/check local provider: %w", errors.Join(err, ctx.Err()))
	}
	if err := ctx.Err(); err != nil {
		return id, err
	}
	if strings.TrimSpace(id.Model) == "" || strings.TrimSpace(id.Digest) == "" {
		return id, fmt.Errorf("%w: provider returned empty model identity", ErrSemanticIndex)
	}
	return id, nil
}

// unitVector scales before Hypot to avoid overflow even for valid near-MaxFloat64
// components. Cosine is then a bounded dot product of normalized vectors.
func unitVector(v []float64, dimensions int) ([]float64, error) {
	if len(v) == 0 || (dimensions != 0 && len(v) != dimensions) {
		return nil, ErrSemanticVector
	}
	scale := 0.0
	for _, x := range v {
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return nil, ErrSemanticVector
		}
		scale = math.Max(scale, math.Abs(x))
	}
	if scale == 0 {
		return nil, ErrSemanticVector
	}
	norm := 0.0
	for _, x := range v {
		norm = math.Hypot(norm, x/scale)
	}
	out := make([]float64, len(v))
	for i, x := range v {
		out[i] = (x / scale) / norm
	}
	return out, nil
}

func embedSemantic(ctx context.Context, p SemanticProvider, id embedding.Identity, texts []string, dimensions int) ([][]float64, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	vectors, err := p.Embed(ctx, id, texts)
	if err != nil {
		return nil, fmt.Errorf("semantic embedding: check local provider: %w", errors.Join(err, ctx.Err()))
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(vectors) != len(texts) {
		return nil, ErrSemanticVector
	}
	for _, v := range vectors {
		if _, err := unitVector(v, dimensions); err != nil {
			return nil, err
		}
		if dimensions == 0 {
			dimensions = len(v)
		}
	}
	return vectors, nil
}

// BuildSemanticIndex explicitly embeds all visible filtered memories. Limit is
// intentionally ignored. No provider calls occur while rows or transactions live.
func (s *Store) BuildSemanticIndex(ctx context.Context, opts SearchOptions) (SemanticIndexResult, error) {
	empty := SemanticIndexResult{}
	p, err := s.semanticProvider(ctx, opts)
	if err != nil {
		return empty, err
	}
	snapshot, err := semanticSnapshot(ctx, s.db, opts)
	if err != nil {
		return empty, err
	}
	id, err := resolveSemantic(ctx, p)
	if err != nil {
		return empty, err
	}
	envelopes := make([][]byte, len(snapshot))
	dimensions := 0
	// Keep each request bounded independently of corpus size; a single source is
	// already bounded by the store's public content limits.
	for start := 0; start < len(snapshot); {
		end, size := start, 0
		texts := []string{}
		for end < len(snapshot) && end-start < 32 {
			o := snapshot[end].result
			text := o.Title + "\n" + o.Content
			if end > start && size+len(text) > 128*1024 {
				break
			}
			texts = append(texts, text)
			size += len(text)
			end++
		}
		vectors, err := embedSemantic(ctx, p, id, texts, dimensions)
		if err != nil {
			return empty, err
		}
		for i, v := range vectors {
			if dimensions == 0 {
				dimensions = len(v)
			}
			e := semanticEnvelope{Version: 1, Identity: id, Dimensions: dimensions, Source: snapshot[start+i].source, Vector: v}
			envelopes[start+i], err = json.Marshal(e)
			if err != nil {
				return empty, err
			}
		}
		start = end
	}
	after, err := resolveSemantic(ctx, p)
	if err != nil {
		return empty, err
	}
	if after != id {
		return empty, ErrSemanticChanged
	}
	err = s.withTx(func(tx *sql.Tx) error {
		current, err := semanticSnapshot(ctx, tx, opts)
		if err != nil {
			return err
		}
		if !sameSemanticSnapshot(snapshot, current) {
			return ErrSemanticChanged
		}
		for i, r := range snapshot {
			if err := ctx.Err(); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE observations SET embedding = ?, embedding_model = ?, embedding_created_at = ? WHERE id = ?`, envelopes[i], id.Model, Now(), r.result.ID); err != nil {
				return err
			}
		}
		return ctx.Err()
	})
	if err != nil {
		return empty, err
	}
	return SemanticIndexResult{Indexed: len(snapshot)}, nil
}

func decodeSemantic(r semanticRow, id embedding.Identity, dimensions int) ([]float64, error) {
	var e semanticEnvelope
	if len(r.cache) == 0 || json.Unmarshal(r.cache, &e) != nil || e.Version != 1 || e.Identity != id || e.Source != r.source || e.Dimensions != len(e.Vector) || !r.model.Valid || r.model.String != id.Model || !r.cacheTime.Valid {
		return nil, ErrSemanticIndex
	}
	if dimensions != 0 && e.Dimensions != dimensions {
		return nil, ErrSemanticIndex
	}
	v, err := unitVector(e.Vector, e.Dimensions)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSemanticIndex, err)
	}
	return v, nil
}

func (s *Store) searchSemantic(ctx context.Context, query string, opts SearchOptions) ([]SearchResult, error) {
	rows, err := s.semanticSearchRows(ctx, query, opts)
	if err != nil {
		return nil, err
	}
	out := make([]SearchResult, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.result)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) semanticSearchRows(ctx context.Context, query string, opts SearchOptions) ([]semanticRow, error) {
	p, err := s.semanticProvider(ctx, opts)
	if err != nil {
		return nil, err
	}
	snapshot, err := semanticSnapshot(ctx, s.db, opts)
	if err != nil {
		return nil, err
	}
	id, err := resolveSemantic(ctx, p)
	if err != nil {
		return nil, err
	}
	vectors := make([][]float64, len(snapshot))
	dimensions := 0
	for i, r := range snapshot {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		vectors[i], err = decodeSemantic(r, id, dimensions)
		if err != nil {
			return nil, err
		}
		dimensions = len(vectors[i])
	}
	results := make([]semanticRow, 0, len(snapshot))
	if len(snapshot) > 0 {
		queryVectors, err := embedSemantic(ctx, p, id, []string{query}, dimensions)
		if err != nil {
			return nil, err
		}
		q, err := unitVector(queryVectors[0], dimensions)
		if err != nil {
			return nil, err
		}
		for i, r := range snapshot {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			score := 0.0
			for j, x := range vectors[i] {
				score += x * q[j]
			}
			score = math.Max(-1, math.Min(1, score))
			r.result.Similarity = &score
			r.result.RetrievalMode = "semantic"
			results = append(results, r)
		}
	}
	after, err := resolveSemantic(ctx, p)
	if err != nil {
		return nil, err
	}
	if after != id {
		return nil, ErrSemanticChanged
	}
	current, err := semanticSnapshot(ctx, s.db, opts)
	if err != nil {
		return nil, err
	}
	if !sameSemanticSnapshot(snapshot, current) {
		return nil, ErrSemanticChanged
	}
	sort.Slice(results, func(i, j int) bool {
		if *results[i].result.Similarity == *results[j].result.Similarity {
			return results[i].result.ID < results[j].result.ID
		}
		return *results[i].result.Similarity > *results[j].result.Similarity
	})
	limit := opts.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > s.cfg.MaxSearchResults {
		limit = s.cfg.MaxSearchResults
	}
	if len(results) > limit {
		results = results[:limit]
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

func (s *Store) semanticPreviews(ctx context.Context, query string, opts SearchOptions) ([]SearchPreviewResult, error) {
	results, err := s.semanticSearchRows(ctx, query, opts)
	if err != nil {
		return nil, err
	}
	out := make([]SearchPreviewResult, 0, len(results))
	for _, row := range results {
		r := row.result
		out = append(out, SearchPreviewResult{ID: r.ID, SyncID: r.SyncID, Type: r.Type, Title: r.Title, Preview: row.preview, Truncated: row.truncated, Project: r.Project, TopicKey: r.TopicKey, Scope: r.Scope, ReviewAfter: r.ReviewAfter, Pinned: r.Pinned, CreatedAt: r.CreatedAt, Rank: r.Rank, Similarity: r.Similarity, RetrievalMode: r.RetrievalMode})
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
