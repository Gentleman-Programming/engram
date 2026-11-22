package cloudstore

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
	engramsync "github.com/Gentleman-Programming/engram/v3/internal/sync"
)

var historicalChunkDecodeFixtures = []struct {
	name    string
	payload string
}{
	{"null", `null`},
	{"unknown fields", `{"unknown":{"sessions":123},"mutations":[{"entity":"observation","payload":"{}","future":{"wrong":42}}]}`},
	{"mixed case", `{"SESSIONS":[{"ID":" padded ","OWNERSHIP_MODE":"shared","DIRECTORY":null}],"Mutations":[{"Entity":"session","Entity_Key":" padded ","Payload":"{}"}]}`},
	{"case alias merge", `{"Sessions":[{"id":" retained ","ownership_mode":"shared","directory":"old"}],"sessions":[{"directory":null}],"Mutations":[{"pAyLoAd":"{\"z\":1,\"a\":2}"}],"mutations":[{"Entity":"observation","Project":"test"}]}`},
	{"exact duplicate", `{"sessions":[{"ownership_mode":"shared"}],"sessions":[{"OWNERSHIP_MODE":"shared","directory":null}]}`},
	{"earlier malformed session array", `{"SESSIONS":{},"sessions":[]}`},
	{"earlier malformed session", `{"SESSIONS":[123],"sessions":[{}]}`},
	{"malformed ownership", `{"SESSIONS":[{"OWNERSHIP_MODE":false}],"sessions":[{}]}`},
	{"project-owned null", `{"sessions":[{"ownership_mode":"project_owned","directory":null}]}`},
	{"project-owned null with case aliases", `{"SESSIONS":[{"OWNERSHIP_MODE":"project_owned","DIRECTORY":null}]}`},
	{"escaped ownership", `{"sessions":[{"ownership_mode":"pro\u006aect_owned","directory":null}]}`},
	{"project-owned overridden", `{"Sessions":[{"ownership_mode":"project_owned","Ownership_Mode":"shared","directory":null}]}`},
	{"project-owned accepted", `{"sessions":[{"ownership_mode":"project_owned","directory":""}]}`},
	{"malformed observations", `{"observations":[{"ID":"wrong"}],"mutations":[{"entity":"observation","payload":"{}"}]}`},
	{"malformed prompts", `{"PROMPTS":{},"prompts":[]}`},
	{"malformed review clears", `{"REVIEW_CLEARS":[{}]}`},
	{"malformed mutations", `{"mutations":[{"payload":123}]}`},
}

func assertHistoricalChunkDecodeMatches(t *testing.T, payload []byte) {
	t.Helper()
	want, wantErr := parseChunkData(payload)
	got, err := engramsync.DecodeHistoricalChunk(payload)
	if fmt.Sprint(err) != fmt.Sprint(wantErr) || !reflect.DeepEqual(got, want) {
		t.Fatalf("historical decode = %+v, %v; full decode = %+v, %v", got, err, want, wantErr)
	}
	for i, mutation := range got.Mutations {
		actual, actualErr := syncMutationSignature(mutation)
		expected, expectedErr := syncMutationSignature(want.Mutations[i])
		if actual != expected || fmt.Sprint(actualErr) != fmt.Sprint(expectedErr) {
			t.Fatalf("canonical signature changed for mutation %d", i)
		}
	}
}

func TestHistoricalChunkDecodeMatchesFullDecoder(t *testing.T) {
	for _, fixture := range historicalChunkDecodeFixtures {
		t.Run(fixture.name, func(t *testing.T) {
			assertHistoricalChunkDecodeMatches(t, []byte(fixture.payload))
		})
	}
	t.Run("invalid JSON syntax", func(t *testing.T) {
		assertHistoricalChunkDecodeMatches(t, []byte(`{"mutations":`))
	})
}

func TestFullChunkDecodePreservesErrorAndReceiver(t *testing.T) {
	chunk := engramsync.ChunkData{Sessions: []store.Session{{ID: "existing"}}}
	err := json.Unmarshal([]byte(`{"sessions":[{"id":"replacement"}],"prompts":{}}`), &chunk)
	const want = "json: cannot unmarshal object into Go struct field plain.prompts of type []store.Prompt"
	if err == nil || err.Error() != want {
		t.Fatalf("decode error = %v, want %q", err, want)
	}
	if !reflect.DeepEqual(chunk, engramsync.ChunkData{Sessions: []store.Session{{ID: "existing"}}}) {
		t.Fatalf("failed decode mutated the receiver: %+v", chunk)
	}
}

func TestHistoricalChunkDecodeMatchesFullDecoderAfterJSONBRoundTrip(t *testing.T) {
	cs := openIsolatedCloudStore(t)
	for _, fixture := range historicalChunkDecodeFixtures {
		t.Run(fixture.name, func(t *testing.T) {
			var stored []byte
			err := cs.db.QueryRowContext(context.Background(), `
				INSERT INTO cloud_chunks(project_name, chunk_id, created_by, payload)
				VALUES ('decode', $1, 'fixture', $2::jsonb)
				RETURNING payload`, fixture.name, fixture.payload).Scan(&stored)
			if err != nil {
				t.Fatal(err)
			}
			assertHistoricalChunkDecodeMatches(t, stored)
		})
	}
}
