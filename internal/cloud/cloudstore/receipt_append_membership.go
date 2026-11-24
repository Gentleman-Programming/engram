package cloudstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	engramsync "github.com/Gentleman-Programming/engram/v3/internal/sync"
)

const (
	receiptAppendEventCap   = 128
	receiptAppendRowBytes   = 4 << 20
	receiptAppendTotalBytes = 16 << 20
	receiptAppendIndexCap   = 1024
)

var errReceiptAppendUnproven = errors.New("append membership unproven")

type receiptAppendEvent struct {
	kind, project, id string
	seq               int64
}

type receiptAppendKey struct {
	project, id string
}

// verifyAppendMembership consumes complete, trusted append event keys in the
// same read snapshot as the source rows. It establishes only membership. The
// caller must attest catalogs, event visibility, and the baseline before using it.
func verifyAppendMembership(ctx context.Context, tx *sql.Tx, projects []string, events []receiptAppendEvent) error {
	if len(events) > receiptAppendEventCap {
		return errReceiptAppendUnproven
	}
	allowed := make(map[string]struct{}, len(projects))
	for _, project := range projects {
		allowed[project] = struct{}{}
	}
	seen := make(map[receiptAppendEvent]struct{}, len(events))
	var chunks, journal []receiptAppendEvent
	indexKeys := make(map[receiptAppendKey]struct{})
	for _, event := range events {
		if strings.TrimSpace(event.project) == "" {
			return errReceiptAppendUnproven
		}
		if _, duplicate := seen[event]; duplicate {
			return errReceiptAppendUnproven
		}
		seen[event] = struct{}{}
		switch {
		case event.kind == "cloud_chunks" && event.id != "" && event.seq == 0:
			chunks = append(chunks, event)
		case event.kind == "journal" && event.seq > 0 && event.id == "":
			journal = append(journal, event)
		case event.kind == "cloud_project_sessions" && event.id != "" && event.seq == 0:
			indexKeys[receiptAppendKey{event.project, event.id}] = struct{}{}
		default:
			return errReceiptAppendUnproven
		}
	}
	if len(chunks)+len(journal) > 0 {
		// The ordinary project predicate uses SQL equality. Only use byte-exact
		// maps when both the column types and collations preserve that equality.
		var exact bool
		if err := tx.QueryRowContext(ctx, `
			SELECT count(*)=2 AND coalesce(bool_and(
				a.atttypid='pg_catalog.text'::pg_catalog.regtype AND c.collisdeterministic),false)
			FROM pg_catalog.pg_attribute a JOIN pg_catalog.pg_collation c ON c.oid=a.attcollation
			WHERE (a.attrelid='cloud_chunks'::pg_catalog.regclass AND a.attname='project_name')
			   OR (a.attrelid='cloud_mutations'::pg_catalog.regclass AND a.attname='project')`).Scan(&exact); err != nil {
			return err
		}
		if !exact {
			return errReceiptAppendUnproven
		}
	}
	remaining := receiptAppendTotalBytes
	materialized := make(map[receiptAppendKey]struct{})
	if len(chunks) > 0 {
		if err := readAppendChunkMembership(ctx, tx, chunks, allowed, materialized, indexKeys, &remaining); err != nil {
			return err
		}
	}
	if len(journal) > 0 {
		if err := checkAppendJournal(ctx, tx, journal, allowed, materialized, &remaining); err != nil {
			return err
		}
	}
	if len(indexKeys) > 0 {
		return checkAppendIndex(ctx, tx, indexKeys)
	}
	return ctx.Err()
}

func readAppendChunkMembership(ctx context.Context, tx *sql.Tx, chunks []receiptAppendEvent, allowed map[string]struct{}, materialized, indexKeys map[receiptAppendKey]struct{}, remaining *int) error {
	projects, ids := make([]string, 0, len(chunks)), make([]string, 0, len(chunks))
	for _, event := range chunks {
		projects, ids = append(projects, event.project), append(ids, event.id)
	}
	// Enforce the transfer budget in SQL too. Closing the result may drain
	// remaining rows even after the verifier sees one it cannot accept.
	rows, err := tx.QueryContext(ctx, `
		SELECT c.project_name, c.chunk_id,
		       CASE WHEN octet_length(c.payload::text) <= $3
		                  AND sum(octet_length(c.payload::text)) OVER (ORDER BY requested.position ROWS UNBOUNDED PRECEDING) <= $4
		            THEN c.payload::text END
		FROM unnest($1::text[], $2::text[]) WITH ORDINALITY requested(project, id, position)
		JOIN cloud_chunks c ON c.project_name=requested.project AND c.chunk_id=requested.id
		ORDER BY requested.position`, projects, ids, receiptAppendRowBytes, *remaining)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	i := 0
	for rows.Next() {
		var project, id string
		var payload sql.NullString
		if err := rows.Scan(&project, &id, &payload); err != nil {
			return err
		}
		if i >= len(chunks) || chunks[i].project != project || chunks[i].id != id || !payload.Valid || len(payload.String) > *remaining {
			return errReceiptAppendUnproven
		}
		i++
		*remaining -= len(payload.String)
		chunk, err := engramsync.DecodeHistoricalChunk([]byte(payload.String))
		if err != nil {
			return fmt.Errorf("%w: historical chunk decode: %v", errReceiptAppendUnproven, err)
		}
		for id := range collectSessionIDs(chunk) {
			if project != strings.TrimSpace(project) {
				return errReceiptAppendUnproven
			}
			indexKeys[receiptAppendKey{project, id}] = struct{}{}
			if len(indexKeys) > receiptAppendIndexCap {
				return errReceiptAppendUnproven
			}
		}
		if _, inScope := allowed[project]; !inScope {
			continue
		}
		for _, mutation := range chunk.Mutations {
			if !isChunkMaterializableMutationEntity(mutation.Entity) {
				continue
			}
			signature, err := syncMutationSignature(mutation)
			if err != nil {
				return fmt.Errorf("%w: historical mutation signature: %v", errReceiptAppendUnproven, err)
			}
			materialized[receiptAppendKey{project, signature}] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if i != len(chunks) {
		return errReceiptAppendUnproven
	}
	return nil
}

func checkAppendJournal(ctx context.Context, tx *sql.Tx, journal []receiptAppendEvent, allowed map[string]struct{}, materialized map[receiptAppendKey]struct{}, remaining *int) error {
	seqs := make([]int64, 0, len(journal))
	for _, event := range journal {
		seqs = append(seqs, event.seq)
	}
	// Reuse the remaining budget after accounting for the chunk response.
	rows, err := tx.QueryContext(ctx, `
		SELECT m.seq, m.project, m.entity, m.entity_key, m.op,
		       CASE WHEN octet_length(m.payload::text) <= $2
		                  AND sum(octet_length(m.payload::text)) OVER (ORDER BY requested.position ROWS UNBOUNDED PRECEDING) <= $3
		            THEN m.payload::text END
		FROM unnest($1::bigint[]) WITH ORDINALITY requested(seq, position)
		JOIN cloud_mutations m ON m.seq=requested.seq
		ORDER BY requested.position`, seqs, receiptAppendRowBytes, *remaining)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	i := 0
	for rows.Next() {
		var seq int64
		var entry MutationEntry
		var payload sql.NullString
		if err := rows.Scan(&seq, &entry.Project, &entry.Entity, &entry.EntityKey, &entry.Op, &payload); err != nil {
			return err
		}
		if i >= len(journal) || journal[i].project != entry.Project || journal[i].seq != seq || !payload.Valid || len(payload.String) > *remaining {
			return errReceiptAppendUnproven
		}
		i++
		*remaining -= len(payload.String)
		if _, inScope := allowed[entry.Project]; !inScope || !isChunkMaterializableMutationEntity(entry.Entity) {
			continue
		}
		entry.Payload = []byte(payload.String)
		signature, err := mutationEntrySignature(entry)
		if err != nil { // The ordinary planner also skips invalid candidates.
			continue
		}
		if _, covered := materialized[receiptAppendKey{entry.Project, signature}]; !covered {
			return errReceiptAppendUnproven
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if i != len(journal) {
		return errReceiptAppendUnproven
	}
	return nil
}

func checkAppendIndex(ctx context.Context, tx *sql.Tx, indexKeys map[receiptAppendKey]struct{}) error {
	projects, ids := make([]string, 0, len(indexKeys)), make([]string, 0, len(indexKeys))
	for key := range indexKeys {
		projects, ids = append(projects, key.project), append(ids, key.id)
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT i.project_name, i.session_id
		FROM unnest($1::text[], $2::text[]) WITH ORDINALITY requested(project, id, position)
		JOIN cloud_project_sessions i ON i.project_name=requested.project AND i.session_id=requested.id
		ORDER BY requested.position`, projects, ids)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	i := 0
	for rows.Next() {
		var project, id string
		if err := rows.Scan(&project, &id); err != nil {
			return err
		}
		if i >= len(projects) || projects[i] != project || ids[i] != id {
			return errReceiptAppendUnproven
		}
		i++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if i != len(projects) {
		return errReceiptAppendUnproven
	}
	return nil
}
