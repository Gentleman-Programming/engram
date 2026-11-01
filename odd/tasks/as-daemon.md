# Feature: as-daemon — Docker-style daemon verbs for engram serve

Implements issue #2228 (still `status:needs-review`; user authorized start by direct instruction 2026-11-01). Docker-modeled lifecycle verbs (`run -d`, `ps`, `stop`, `logs`, `restart`) on branch `feat/as-daemon` (renamed from `as-daemon`: GitHub ruleset enforces `type/description`). Targets macOS, Debian Linux, Windows 11.

## Specs

- S1 (port-in-use non-zero): "**Port-in-use returns a non-zero exit code** (today it is 0), keeping the clear message." Repro: "while another process held port 7437, `engram serve` exited 0 every ~10 seconds … an invisible crash-loop."
- S2 (detached run): "Run in background | `docker run -d` | `engram run -d` | Starts detached and prints its PID and port."
- S3 (already running): "A second `serve` against a live instance detects it (pidfile + port probe) and reports `already running (pid N)` with a non-zero exit code by default, so callers can decide to reuse or replace."
- S4 (status): "Status | `docker ps` | `engram ps` | Tells you whether the daemon is up."
- S5 (stop): "Stop | `docker stop` | `engram stop` | Stops gently, then force-kills if needed."
- S6 (port flag fails loudly): "Port already taken | `docker run -d -p 1234` | `engram run -d -p 1234` | Fails loudly instead of exiting 0."
- S7 (logs): "Logs | `docker logs` | `engram logs` | Follows the daemon's logs."
- S8 (restart): "Restart | `docker restart` | `engram restart` | Brings it back; auto-restart stays with launchd/systemd."
- S9 (pidfile): daemon "records a pidfile under the data dir (e.g. `~/.engram/serve.pid`), prints the PID and port, exits 0."
- S10 (platforms, user verbatim): "code for mac, linux debian and windows 11"

## Tasks

| ID | Spec | Route | Commit |
| --- | --- | --- | --- |
| T1 | S1 | internal/server typed error for same-instance port ownership + cmdServe maps to non-zero exit; fix DOCS.md exit-0 text; RED→GREEN tests (seam: newHTTPServer/startHTTP, listen seam in server) | f24bb0a |
| T2 | S9 | internal/daemon package: pidfile (atomic write, stale-pid liveness), health probe (extract/reuse cloud_daemon_probe logic), log-sink path helper; unit tests | 0ba49f2 |
| T3 | S2, S3, S6 | `engram run [-d] [-p PORT]` verb: already-running guard, detach (Unix double-fork/setsid; Windows exec-detach per serve_background.go), stdio→log sink, readiness handshake, pidfile write, print PID+port; tests (child env-marker) | — |
| T4 | S4, S5, S7, S8 | `engram ps` / `stop` / `logs [-f]` / `restart` verbs; graceful-then-force stop (SIGTERM/SIGKILL unix; CTRL_BREAK/TerminateProcess windows); pidfile-owned daemon only; tests | — |
| T5 | S1-S8 | DOCS.md daemon section (verb table, pidfile/log/exit codes), update supervisor-templates cross-links; docs alignment check | — |

## Log

- L1 (request, verbatim): "get memory #4446 and update to implement docker style commands as in issue #2228" → investigation updated (memory #4446 rev 2); then "only investigation".
- L2 (request, verbatim): "gime an implementation plan" → plan delivered: 5 work units, decisions recommended.
- L3 (request, verbatim): "create a new branch called 'as-daemon', checkout to it and start with plan" — go-ahead. Branch created then renamed `feat/as-daemon` (commit-hygiene ruleset rejects bare names on push). Decisions adopted: `run` without `-d` = foreground serve; daemon verbs manage only the pidfile-tracked daemon (never session-owned MCP serves); log sink `<dataDir>/serve.log`; `service install` deferred to follow-up issue.
- L4 (requirement, verbatim): "code for mac, linux debian and windows 11" — platform split via `*_unix.go`/`*_windows.go` file pattern (prior art: job_windows.go/job_other.go).
- L5: Issue #2228 remains `status:needs-review` upstream; user instruction authorizes local implementation anyway.
- L6: T1 done (commit f24bb0a). RED observed: `same-instance bind loser: <nil>, want ErrPortOwned`; GREEN + full internal/server suite green; go build ./cmd/engram clean. Existing test TestStartAcceptsOnlySameInstanceBindLoser rewritten to TestStartRejectsSameInstanceBindLoser (pinned old exit-0 semantics); `log` import removed from server_test.go (only user). cmd-level exit-code test skipped as over-proportional: cmdServe pass-through (`return serveErr`) is unchanged generic code; the exit-0 origin was server.Start returning nil, now pinned. Risk: item 3 — serve port-owned exit code changes 0→non-zero (requested by S1, pinned by test).
- L7: T2 done (commit 0ba49f2). RED observed (7 assertion failures against stubs); GREEN internal/daemon; GOOS=windows + GOOS=linux builds clean (windows.STILL_ACTIVE undefined in x/sys — local const 259). cmd DaemonProbe tests green via adapter; full cmd/engram suite has 2 PRE-EXISTING failures (TestDoctorDiscardEmptyPromptApply/BackupErrors, SQLITE_BUSY) — reproduced identically on clean main worktree; reported, not fixed (out of scope).
- L8 (slice boundary): commits f24bb0a (63 lines) + 0ba49f2 (434 lines) = 497 authored lines on the branch. T2 alone exceeds the ~400-line review slice (test-heavy: 8 new daemon files + adapter). Delivery strategy: keep work-unit commits (each independently reviewable) and apply chained-PR slices at delivery time; T3/T4 commits target <=400 lines each. Slice boundary recorded per budget notice 2026-11-01.
