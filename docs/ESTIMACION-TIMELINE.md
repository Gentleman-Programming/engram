[← Volver al README](../README.md)

# Engram Cloud — Estimacion de Timeline

---


| Fase | Descripcion | Tiempo |
|------|-------------|--------|
| Phase 1 | Store local: migraciones, StoreInterface, sync_mutations, idempotencia | ~2h |
| Phase 2 | Cloud server: PostgreSQL schema, auth, push/pull, CRUD, FTS, batch, rate limiting | ~6h |
| Phase 3 | Cliente sync: HTTP client, SyncClient, RemoteStore, CLI commands, --backend flag | ~5h |
| **Total Pendiente** | | **~13h** |



| Fase | Descripcion | Tiempo estimado |
|------|-------------|----------------|
| Phase 4 | Hardening: indices, batch limit, retention, RLS, sync personal, guardrails, confidence decay, deteccion contradicciones | ~12-13h |
| Phase 5 | Skills Enterprise: modelo organizacional, enrollment cascading, RBAC, versionado, auditoria, cascada mem_context, ingesta CLI, propagacion urgente | ~14-15h |
| Phase 6 | Dashboard Web: auth, layout, 5 paginas htmx, SSE real-time, analytics | ~12-14h |
| Phase 7 | Integraciones: observabilidad, webhooks, CI/CD verify, --remote search, encryption at rest | ~8h |
| **Total pendiente** | | **~46-50h** |

---

## Aproximado general

| | Horas |
|--|-------|
| **Total del proyecto** | **~59-63h** |
