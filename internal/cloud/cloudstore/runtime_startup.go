package cloudstore

import (
	"context"
	"database/sql"
	"slices"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud"
)

// StartupReceiptVerifier is constructed by cloudstore. A successful check
// must cover both global session indexing and repair for the supplied projects.
type StartupReceiptVerifier struct {
	verify func(context.Context, *sql.DB, []string) (bool, error)
}

// NewRuntimeContext reports whether a verified receipt covered both startup
// passes. Without such proof, it builds the ordinary session index and leaves
// the caller responsible for mutation chunk repair in its configured order.
func NewRuntimeContext(ctx context.Context, cfg cloud.Config, projects []string, verifier *StartupReceiptVerifier) (*CloudStore, bool, error) {
	if verifier == nil || verifier.verify == nil {
		cs, err := NewContext(ctx, cfg)
		return cs, false, err
	}
	cs, err := openCloudStore(ctx, cfg)
	if err != nil {
		return nil, false, err
	}
	covered, err := cs.initializeRuntime(ctx, projects, verifier)
	if err != nil {
		_ = cs.Close()
		return nil, false, err
	}
	return cs, covered, nil
}

func (cs *CloudStore) initializeRuntime(ctx context.Context, projects []string, verifier *StartupReceiptVerifier) (bool, error) {
	if err := cs.migrateSchema(ctx); err != nil {
		return false, err
	}
	covered, proofErr := verifier.verify(ctx, cs.db, slices.Clone(projects))
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if covered && proofErr == nil {
		return true, nil
	}
	return false, cs.backfillProjectSessionsFromChunks(ctx)
}
