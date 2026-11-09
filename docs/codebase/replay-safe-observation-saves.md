[← Codebase Guide](../CODEBASE-GUIDE.md) | [Memory Core](memory-core.md) | [Interfaces](interfaces.md)

# Replay-Safe Observation Saves

**Observation saves can use a caller-supplied operation ID to recover a committed result without creating a second observation.** This guide records the local-store, HTTP, Pi, and timing boundaries introduced for [issue #1629](https://github.com/Gentleman-Programming/engram/issues/1629).

## Quick path

1. Send an observation save with a stable operation ID.
2. If the write outcome is unknown, look up the committed result before considering a replay.
3. Treat a conflicting or tombstoned result as non-replayable. Only a never-recorded result has the explicit typed recovery evidence described below.

## Contract and ownership

| Area | Responsibility |
|---|---|
| `internal/store` | Owns operation-ID validation, the durable save ledger, atomic observation/result creation, and committed-result lookup. |
| Local HTTP API | Accepts an optional operation ID on `POST /observations` and exposes `GET /observations/save-result?operation_id=…`. |
| Pi integration | Preserves an unknown write outcome by default and performs bounded recovery only when the lookup proves the narrowly defined replay case. |
| Timing diagnostics | Emits opt-in, structured write timing records without request or response content. |

Operation IDs are frozen to their original payload. Reusing an ID for a different payload is a conflict, not a new save. The ledger retains operation records indefinitely; deleting or soft-deleting the observation makes its result unavailable without removing the operation record. Creating the observation and recording its result are one transaction, so a replay returns the committed result rather than producing prompt capture or sync side effects again.

## Pi recovery boundary

Pi treats uncertain writes as unknown. Only a typed `404` “committed result not found” lookup for a never-recorded operation authorizes one byte-identical replay. A retained soft-deleted or tombstoned operation returns `410` and is non-replayable. A successful write whose result cannot be read remains ambiguous; initial HTTP errors retain their typed status, and caller cancellation propagates. No other response or transport failure authorizes an additional write, and the recovery wait is cancellable.

## Opt-in timing diagnostics

The local server can emit structured stderr timing records for session registration, observation saves, and passive capture when write timing is explicitly enabled. Records contain operation, outcome, attempt count, and duration fields; they exclude request and response bodies, observation text, prompt text, IDs, and project names.

## Verification evidence

Run the following commands to verify the affected boundaries:

- `go test ./internal/store -run 'Test(AddObservationWithOperationID|AddObservationWithoutOperationID|ObservationOperation|ObservationSaveOperation|GetObservationSaveResult)' -count=1 -timeout=90s`
- `go test ./internal/server -run 'Test(HandleGetObservationSaveResult|HandleAddObservation|ObservationPromptCapture)' -count=1 -timeout=90s`
- `node --test plugin/pi/test/*.test.mjs`
- `go test ./internal/server -run 'TestHTTPWriteTiming' -count=1 -timeout=90s`

## Source references

- `internal/store/store.go` and `internal/store/observation_save_operation_test.go`
- `internal/server/server.go` and `internal/server/observation_save_result_test.go`
- `plugin/pi/index.ts` and `plugin/pi/test/replay-recovery.test.mjs`
- `internal/server/http_write_timing.go` and `internal/server/http_write_timing_test.go`
