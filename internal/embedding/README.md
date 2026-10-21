# Local embedding provider

This package implements explicit Ollama operations only. It is not yet wired to
search, indexing, configuration environment variables, or any adapter.

- `New(Config{BaseURL, Model}) (*Client, error)` validates configuration without
  network I/O. The URL defaults to `http://127.0.0.1:11434`; the model is required
  and must already be installed. There is no model or dimension default.
- `Resolve(context.Context) (Identity, error)` reads `/api/tags`. Identity is the
  canonical model name and full lowercase SHA-256 content digest. An untagged
  configured name can match its `:latest` entry. Missing, ambiguous, inconsistent,
  or invalid identities are rejected, as are cloud-backed entries advertising
  `remote_model` or `remote_host`.
- `Embed(context.Context, Identity, []string) ([][]float64, error)` requires that
  identity to match configuration and current metadata before and after embedding.
  It posts the canonical model name and input array to `/api/embed`, explicitly
  setting `truncate:false`. It validates response identity, count, dimensions,
  finite components and nonzero norms. Empty or whitespace-only input is rejected.

Only root HTTP loopback endpoints are accepted. Credentials, non-loopback hosts,
queries, fragments, invalid ports, proxies and redirects are prohibited. A private
stdlib transport resolves and checks every address before dialing a numeric
loopback address. `localhost` must resolve exclusively to loopback. Configured
ports are preserved. Requests have a 120-second timeout; caller contexts may set
shorter limits. Responses are limited to 4 MiB and one valid JSON value. No raw
provider bodies or input texts are included in errors. Unknown JSON fields are
allowed for native API forward compatibility.

Callers can use `errors.Is` with `ErrConfig`, `ErrInput`, `ErrUnavailable`,
`ErrModel`, `ErrRemoteModel`, `ErrModelChanged`, `ErrResponse`, `ErrVectors`, and
context cancellation/deadline errors. HTTP failures include the numeric status.
There are no pulls, subprocesses, fallback providers, storage writes, background
operations, environment reads or global configuration changes. Callers must not
hold database transactions while invoking provider operations.

## Identity trust boundary

The local daemon's metadata is trusted. Loopback alone does not make a cloud
model local, hence the metadata rejection. The embed response supplies a model
name, not an immutable digest attestation. Checks reject observed replacement;
they cannot detect an unobservable A-to-B-to-A replacement or attest a malicious
daemon. Consumers must keep model identity with derived data and discard rejected
results. No index, cache, sync format or ranking contract is implemented here.

Tests use synthetic data and local HTTP fixtures only. Run:

```sh
GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off go test ./internal/embedding -count=1
GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off go vet ./internal/embedding
```
