package store

import (
	"context"
	"fmt"
	"io"
	"time"
)

// StartupTrace reports elapsed process time at fixed startup code boundaries.
// The caller controls the destination, and no database data is read to report.
type StartupTrace struct {
	out   io.Writer
	start time.Time
}

// NewStartupTrace sets the destination and elapsed-time origin for a startup.
func NewStartupTrace(out io.Writer, start time.Time) *StartupTrace {
	return &StartupTrace{out: out, start: start}
}

// Mark reports a fixed phase label supplied by the caller.
func (trace *StartupTrace) Mark(phase string) {
	if trace != nil {
		_, _ = fmt.Fprintf(trace.out, "ENGRAM_STARTUP_PHASE v1 %d %s\n", time.Since(trace.start).Milliseconds(), phase)
	}
}

func (trace *StartupTrace) markSQLiteLock(phase string, err error) {
	if trace == nil {
		return
	}
	code, _ := sqlitePrimaryErrorCode(err)
	if code == sqlitePrimaryBusy || code == sqlitePrimaryLocked {
		trace.Mark(phase)
	}
}

type startupTraceKey struct{}

// Context attaches the trace to the synchronous store startup.
func (trace *StartupTrace) Context(ctx context.Context) context.Context {
	if trace == nil {
		return ctx
	}
	return context.WithValue(ctx, startupTraceKey{}, trace)
}

func startupTraceFrom(ctx context.Context) *StartupTrace {
	trace, _ := ctx.Value(startupTraceKey{}).(*StartupTrace)
	return trace
}
