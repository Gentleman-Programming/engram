# Ceremony diet: defer memory protocol in headless mode (C2)

Defer the Engram memory ceremony in headless/bench mode rather than removing it for interactive sessions.

## Specs

S1. Human: "Hazlo" — implement C2. Introduce a dedicated `ENGRAM_DEFER_MEMORY=1` env flag that skips/defer `MEMORY_INSTRUCTIONS` injection into the system prompt, at both injection call sites in `plugin/pi/index.ts` (legacy fallback and structured options). Do not reuse `GENTLE_PI_AGENTS_CHILD` (it collides with existing test fixtures and semantics). Interactive sessions keep the injection.

## Tasks

T1 [pending] S1 — add the flag and guard at both injection sites; keep the mem_* tools registered (the defer only suppresses the injected prompt text, not the tools); update/add tests; report which pinned tests change. — commit: pending.

## Log

L1. Human: "Hazlo" after the explorer confirmed C2 is a two-call-site change needing a new headless signal (no env/mode gating exists today).
