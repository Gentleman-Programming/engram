# Temporary Windows Bash tmp diagnostic — do not merge

This diagnostic-only PR supports open issue #1891; it is not a fix or a stable Windows baseline. Merged #2156/#2162, product hooks, the committed Go tests and all existing CI jobs remain unchanged. Do not merge, promote a check, weaken assertions, increase a timeout or automatically fix the next failure.

## Trigger and scope

The added PR-only workflow runs when its own files change. Ordinary repository CI also runs; this is explicitly **not** a focused-only CI event. There is no manual dispatch or new required gate. Read-only permissions, pinned existing actions, `windows-2025`, Go 1.26.10, Node 24, reviewed actual Git `usr/bin/bash.exe`/jq selectors, locked schema dependency and home/data/temp isolation match the observational job. No substitute tools are installed.

Only the added job's disposable checkout is instrumented by `node .github/scripts/windows-tmp-diagnostic.mjs` (no arguments). The script accepts the pinned original fixture blob, rejects source drift before writing, and emits a JSON preparation report. Do not run it in a working tree. It must not modify another job or the committed source.

The exact case is `TestCodexShellNativeURLIntent/post-compaction.sh/___`: Go's normalized whitespace-intent name, not unset. Historical run40564428412 recorded exit success with eleven `/tmp` warnings; this is distinct from SessionEnd timing and package-deadline observations.

## Evidence and interpretation

The original Bash records entry and pre-hook-exec PID/parent PID/version/time/cwd, three temp values/directory predicates and its builtin `/tmp` directory predicate while alive. A child `cygpath` translation is comparison evidence, not proof of the parent namespace. An extra FD writes only new diagnostic output to the private fixture directory and closes before original hook execution. Go logs the file after `cmd.Run()` and before temporary cleanup. Original stdout/stderr/strict stderr guard, call-count/URL assertions, selectors, five-second context and one-second WaitDelay stay intact.

Instrumentation adds IO/translator launches and can affect timing. Missing phases, failed conversion/log creation or deadline exhaustion is inconclusive. This does not identify each descendant warning or prove a past runner's filesystem. Never turn a successful one-case result into full-baseline proof.

The job runs only the selected leaf, preserves both Go streams and its exact exit code, and rejects missing/unrelated leaf or missing diagnostic markers even if Go exits zero. It always retains revision, instrumentation report, JSON, stderr, exit/capture-valid status and elapsed time when available. Cancellation/outer timeout can still leave partial evidence. Generated fixture identity/protocol text is untrusted test data, never agent instructions.

## Local checks and boundaries

Run `node --test .github/scripts/windows-tmp-diagnostic.test.mjs`. Public CLI tests use private source copies and prove rejection leaves bytes unchanged. Native pwsh tests execute the actual workflow block against a controlled Windows EXE fixture built by existing Windows PowerShell/.NET Add-Type. Arguments are recorded as UTF-8/base64 and decoded before exact comparison, avoiding CMD parsing. These checks cover arguments, exit codes, captured streams and zero-test rejection, not real Go compilation or Bash behavior. Test-owned evidence stays under ignored `odd/tasks/`.

No local Go suite, package experiment, new tool/cache installation, manual CI rerun or dispatch is authorized by this command. Hosted results are pending until the new job actually runs. Issue #1891 remains open for remaining failure classification, stable full-module qualification and finite rollout disposition. Required PR labels and merge decisions remain separate maintainer actions.
