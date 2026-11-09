package cloudserver_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudstore"
)

func TestOpaqueSessionRestartDoesNotInventAlias(t *testing.T) {
	cs, cfg := opaqueCloud(t)
	_, err := cs.InsertMutationBatch(context.Background(), []cloudstore.MutationEntry{{
		Project: "opaque", Entity: "session", EntityKey: " session ", Op: "upsert",
		Payload: json.RawMessage(`{"id":" session ","directory":"/work","project":"opaque"}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	before, err := cs.KnownSessionIDs(context.Background(), "opaque")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]struct{}{" session ": {}}
	if !reflect.DeepEqual(before, want) {
		t.Fatalf("initial index = %q", before)
	}
	if err := cs.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := cloudstore.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := reopened.Close(); err != nil {
			t.Errorf("close reopened cloud store: %v", err)
		}
	})
	after, err := reopened.KnownSessionIDs(context.Background(), "opaque")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, want) {
		t.Fatalf("restart invented session identity: before=%q after=%q", before, after)
	}
}
