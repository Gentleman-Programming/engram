[← Back to README](../README.md)

# Binary self-testing

Released Engram binaries include a local self-test command for RC performance and reliability reports. It is safe to run alongside an existing installation:

```bash
engram test
```

The default run executes both suites. Use `--quick` for a bounded smoke run, or select one suite when investigating a result:

```bash
engram test --quick
engram test reliability --quick
engram test performance
```

`engram test` prints a result for every scenario. A passing run exits `0`; an executed suite with one or more failed scenarios exits `2`; invalid command usage exits `1`. Performance results report elapsed time and throughput for comparison between runs, but have no hardware-dependent pass/fail threshold.

## What the suites exercise

- **Reliability** initializes and migrates a fresh local SQLite database, then verifies save, full-text search, formatted context, and concurrent local writes.
- **Performance** seeds a representative local search corpus and reports repeated store-search throughput. A full run uses 1,000 records and 200 searches; `--quick` uses 100 records and 20 searches.

## JSON reports

Use `--json` for CI, issue attachments, or RC reports:

```bash
engram test --quick --json > engram-self-test.json
```

The stable top-level shape is `engram-self-test/v1`:

```json
{
  "schema_version": "engram-self-test/v1",
  "suite": "all",
  "quick": true,
  "passed": true,
  "duration_ms": 0,
  "scenarios": []
}
```

Each scenario contains `name`, `suite`, `passed`, and `duration_ms`. Failed scenarios also contain `error`; performance scenarios contain `metrics.operations` and `metrics.throughput_ops_per_second`.

## Isolation and limitations

Each run creates a new temporary directory and removes it after completion. The command does not resolve or use `ENGRAM_DATA_DIR`, your `~/.engram` database, cloud credentials, configured endpoints, or user databases. All scenarios are local and offline; they open no network connection and start no service.

Run the command on the same supported Windows, macOS, or Linux platform as the released binary. It requires permission to create temporary files and a working local filesystem. It does **not** validate live Engram Cloud, Postgres, remote sync, agent plugins, or production-network reliability. Use the platform binary and the relevant cloud or end-to-end procedures for those checks.
