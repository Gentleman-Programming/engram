package cloudstore

import "context"

type managedChunkSubmitterKey struct{}

// WithManagedChunkSubmitter carries a server-authenticated managed principal
// through a Sync write. Store methods use it only on initial chunk insertion.
// Transport payloads and the credited author do not participate.
func WithManagedChunkSubmitter(ctx context.Context, principalID int64) context.Context {
	return context.WithValue(ctx, managedChunkSubmitterKey{}, principalID)
}

func managedChunkSubmitter(ctx context.Context) any {
	id, ok := ctx.Value(managedChunkSubmitterKey{}).(int64)
	if !ok || id <= 0 {
		return nil
	}
	return id
}
