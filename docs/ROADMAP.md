[← Volver al README](../README.md)

# Engram — Roadmap

Roadmap tecnico de Engram Cloud, organizado por fases con dependencias y estimaciones de esfuerzo.

**Estado actual**: Phases 1-3 completadas (store local, cloud server, cliente de sync). Lo que sigue es hardening para produccion y features enterprise.

---

## Tabla de Contenidos

- [Resumen de fases](#resumen-de-fases)
- [Phase 4: Produccion y Hardening](#phase-4-produccion-y-hardening)
- [Phase 5: Skills Enterprise](#phase-5-skills-enterprise)
- [Phase 6: Dashboard Web](#phase-6-dashboard-web)
- [Phase 7: Integraciones y Observabilidad](#phase-7-integraciones-y-observabilidad)
- [Backlog (sin fase asignada)](#backlog-sin-fase-asignada)

---

## Resumen de fases

| Fase | Nombre | Dependencias | Estado |
|------|--------|-------------|--------|
| 1 | Store local (SQLite + FTS5) | — | Completada |
| 2 | Cloud server (PostgreSQL, auth, push/pull) | 1 | Completada |
| 3 | Cliente de sync (SyncClient, RemoteStore, CLI) | 2 | Completada |
| **4** | **Produccion y Hardening** | 3 | **Siguiente** |
| **5** | **Skills Enterprise** | 4 | Planificada |
| **6** | **Dashboard Web** | 4 | Planificada |
| **7** | **Integraciones y Observabilidad** | 5, 6 | Planificada |

---

## Phase 4: Produccion y Hardening

**Objetivo**: que engram cloud sea deployable en produccion con confianza para 50-200 devs.

### 4.1 Indices de rendimiento

Optimizaciones criticas de base de datos. Principio: REEMPLAZAR indices redundantes, no agregar encima. Se mantiene el mismo numero de indices (6) para evitar write amplification innecesaria.

| Item | Descripcion | Esfuerzo |
|------|-------------|----------|
| `btree_gin` composite index para FTS | `DROP INDEX idx_obs_search` + `CREATE INDEX idx_obs_search_project USING GIN(project, search_vector)` con extension `btree_gin`. Reemplaza el GIN simple. Busquedas FTS 10-50x mas rapidas con volumen. | S |
| Index `(project, server_seq)` para observations | `DROP INDEX idx_obs_server_seq` + `CREATE INDEX idx_obs_project_seq ON observations(project, server_seq)`. Reemplaza el indice simple. Pulls no escanean datos de otros proyectos. | S |
| Index `(project, server_seq)` para sessions | `DROP INDEX idx_sessions_server_seq` + crear composite. Reemplaza el indice simple. | S |
| Index `(project, server_seq)` para prompts | `DROP INDEX idx_prompts_server_seq` + crear composite. Reemplaza el indice simple. | S |
| Audit de indices no usados | Query periodica a `pg_stat_user_indexes` para detectar indices con `idx_scan = 0`. Revisar anualmente. | S |

### 4.2 Batch limit en push

Prevenir lock starvation cuando un cliente envia pushes grandes.

| Item | Descripcion | Esfuerzo |
|------|-------------|----------|
| Limite de mutations por push (server) | Rechazar push con >50 mutations. Retornar error `BATCH_TOO_LARGE` con instrucciones de chunking. | S |
| Chunking automatico en SyncClient | SyncClient agrupa mutations pendientes en lotes de 50 con idempotency keys por lote. Retry parcial seguro. | M |

### 4.3 Retention policy

Prevenir crecimiento infinito de datos.

| Item | Descripcion | Esfuerzo |
|------|-------------|----------|
| Purge de idempotency_keys | Job diario: `DELETE WHERE created_at < now() - interval '24 hours'` | S |
| Purge de rate_limits | Job diario: `DELETE WHERE window_start < now() - interval '1 hour'` | S |
| Purge de tombstones | Job semanal: purgar observations con `deleted_at` donde `server_seq < MIN(sync_cursors.last_seq)` para ese proyecto AND `deleted_at < 30 dias`. | M |
| Purge de revisiones antiguas | Job mensual: `DELETE FROM observation_revisions WHERE superseded_at < 90 dias`. | S |
| Maintenance endpoint | `POST /api/v1/admin/maintenance` para ejecutar purges manualmente. Solo admins. | S |
| Configuracion de retention | Tabla `retention_policies` con TTL configurable por tipo y por proyecto. | M |

### 4.4 PostgreSQL RLS

Seguridad multi-tenant por construccion.

| Item | Descripcion | Esfuerzo |
|------|-------------|----------|
| RLS en tabla observations | Policy que filtra por membership + scope visibility. | M |
| RLS en tabla sessions | Policy que filtra por membership. | S |
| RLS en tabla prompts | Policy que filtra por `user_id` (privacidad de prompts). | S |
| SET LOCAL app.user_id en middleware | El middleware de auth setea `app.user_id` en la conexion antes de pasar al handler. | M |
| Actualizar tests de integracion | Todos los tests deben setear `app.user_id` en la conexion. | M |

### 4.5 Sync de observaciones personales configurable

| Item | Descripcion | Esfuerzo |
|------|-------------|----------|
| Flag en enrollment | `sync_personal: true/false` en `sync_enrolled_projects`. Default: true. | S |
| Filtro en PushOnce | SyncClient respeta el flag: si false, no incluye observations con `scope=personal` en el push. | S |
| Comando CLI | `engram cloud enroll --no-personal <proyecto>` | S |

### 4.6 Guardrail de scope en push response

| Item | Descripcion | Esfuerzo |
|------|-------------|----------|
| Tracking de ratio personal:project | Rolling window (ultimas 50 saves) por usuario. | S |
| Warning en push response | Si >60% personal, agregar warning `HIGH_PERSONAL_RATIO` al response. El LLM lo ve y se autocorrige. | S |

### 4.7 Confidence con exponential decay

Sistema de confianza para mitigar la propagacion de conocimiento incorrecto entre developers. Las observaciones nacen como `unverified`, se confirman o challengean con el uso, y decaen con el tiempo.

| Item | Descripcion | Esfuerzo |
|------|-------------|----------|
| Campos de confidence en observations | `confidence` (unverified/confirmed/challenged), `confirmed_by` (array), `confirmed_count`, `last_confirmed_at`, `challenged_by`, `challenge_reason`. Agregar en schema cloud + local. | M |
| MCP tool `mem_confirm` | Confirma una observacion: agrega user al array `confirmed_by`, incrementa count, resetea `last_confirmed_at`. Sync via push. | M |
| MCP tool `mem_challenge` | Marca como challenged con razon. Sync via push. Observacion no se borra — se marca. | M |
| Decay calculation en mem_search | Calcular `effective_confidence` client-side al retornar resultados. Formula: `base × 0.5^(days / (half_life × (1 + ln(confirmations))))`. Retornar threshold: TRUSTED/VERIFY/UNTRUSTED. | M |
| Half-life configurable por tipo | Tabla de half-life por tipo de observacion (skill=365d, architecture=90d, bugfix=30d). Configurable por org. | S |
| Team Rule de verificacion | Instrucciones al agente: verificar observations VERIFY/UNTRUSTED contra codigo, llamar `mem_confirm` si coincide, `mem_challenge` si no. | S (config) |
| Migracion de observations existentes | Observations sin campo confidence se migran como `unverified` con `last_confirmed_at = created_at`. | S |

### 4.8 Deteccion de contradicciones por topic_key

| Item | Descripcion | Esfuerzo |
|------|-------------|----------|
| Warning en push por topic_key overlap | Cuando un push incluye una observation con `topic_key` que ya existe de otro dev, el server busca la existente y retorna warning `POTENTIAL_CONTRADICTION` con el ID de la conflictiva. | M |

---

## Phase 5: Skills Enterprise

**Objetivo**: soportar la arquitectura de skills de organizacion y dominio con RBAC, versionado, y cascada.

**Dependencia**: Phase 4 (RLS, retention, indices deben estar en produccion).

### 5.1 Modelo organizacional

| Item | Descripcion | Esfuerzo |
|------|-------------|----------|
| Tablas organizations y domains | `organizations(id, name)`, `domains(id, name, organization_id)`, FK `projects.domain_id` | M |
| Pseudo-proyectos para skills | Convencion `__org__{org_name}` y `__domain__{domain_name}` como project names para skills. Skills se almacenan como observations con estos pseudo-proyectos. | M |
| Enrollment cascading | Al enrollar un proyecto, auto-enrollar al dev en `__org__` y `__domain__` correspondientes. | M |
| Resolver domain desde proyecto | Endpoint o logica server-side que resuelve proyecto → domain → org. | S |

### 5.2 Campos enterprise en observations

| Item | Descripcion | Esfuerzo |
|------|-------------|----------|
| Campo `locked` | Boolean en observations. Si true, la skill no admite excepciones. | S |
| Campo `skill_scope` | Enum: `org`, `domain`, `null`. Distingue skills de org vs domain vs observation regular. | S |
| Campo `deprecated_by` | topic_key de la skill que reemplaza a esta. mem_context retorna con flag. | S |
| Migracion de schema | ALTER TABLE para agregar los campos. Backward-compatible (nullable). | S |

### 5.3 RBAC para skills

| Item | Descripcion | Esfuerzo |
|------|-------------|----------|
| Tabla `skill_policies` | Configuracion de rol minimo para editar/eliminar skills por proyecto/domain/org. | M |
| Verificacion en push | Cuando un push incluye una mutation de tipo skill, verificar RBAC. Rechazar con 403 si no tiene permisos. | M |
| Roles extendidos | Agregar roles `senior`, `lead`, `owner` a `project_members.role`. | S |

### 5.4 Versionado y auditoria de skills

| Item | Descripcion | Esfuerzo |
|------|-------------|----------|
| Revision obligatoria en edicion de skill | Cada edicion de una skill crea revision ANTES de sobreescribir (no solo en conflictos LWW). | M |
| Tabla `skill_audit_log` | Append-only: user_id, operation, topic_key, revision_from, revision_to, timestamp. | M |
| Endpoint de historial | `GET /api/v1/skills/{topic_key}/history` — retorna todas las revisiones. | S |
| Rollback | `POST /api/v1/skills/{topic_key}/rollback?to_revision=N` — crea nueva version con contenido de la revision N. | M |

### 5.5 Cascada en mem_context

| Item | Descripcion | Esfuerzo |
|------|-------------|----------|
| mem_context con cascada (cloud) | Endpoint `/api/v1/context` retorna: skills de org + skills de domain + memorias del proyecto. Progressive disclosure: titulos + IDs, no contenido completo. | M |
| mem_context con cascada (local) | El store local consulta los pseudo-proyectos enrollados y fusiona resultados. | M |
| Marcado de locked/overridable | mem_context retorna el flag `locked` para cada skill. El agente sabe si puede proponer excepciones. | S |

### 5.6 Ingesta de skills

| Item | Descripcion | Esfuerzo |
|------|-------------|----------|
| CLI import desde filesystem | `engram cloud skills import ./docs/skills/ --org jph-lions`. Escanea `*.md`, deriva topic_key de la ruta, crea/actualiza via push. Idempotente. | M |
| Dashboard skill editor | Editor markdown en el dashboard con preview. Crear, editar, deprecar skills. | L (Phase 6) |

### 5.7 Propagacion urgente de skills criticas

| Item | Descripcion | Esfuerzo |
|------|-------------|----------|
| Notificacion push en cambio de skill locked | Cuando se edita una skill locked, el servidor envia webhook a SyncClients activos: "pull ahora". | M |
| Pull reactivo en SyncClient | SyncClient expone endpoint local para recibir el nudge y hacer pull inmediato fuera del ciclo de 120s. | M |

---

## Phase 6: Dashboard Web

**Objetivo**: interfaz web para gestionar memorias, skills, miembros y auditoria.

**Dependencia**: Phase 4. Puede implementarse en paralelo con Phase 5.

### 6.1 Core

| Item | Descripcion | Esfuerzo |
|------|-------------|----------|
| Autenticacion web | Login con API key, cookie de sesion. | M |
| Layout y navegacion | htmx + Go templates + CSS custom. Sidebar con secciones. | M |
| Pagina de memorias | Buscar, filtrar por proyecto/tipo/scope, ver detalle. | M |
| Pagina de skills | Listar por categoria, ver markdown renderizado, historial de revisiones, rollback. | L |
| Pagina de miembros | Gestion de usuarios, asignacion de roles por proyecto. | M |
| Pagina de auditoria | Timeline de cambios en skills con filtros. | M |
| Pagina de proyectos | Estadisticas, salud del sync, proyectos enrollados. | M |

### 6.2 Real-time

| Item | Descripcion | Esfuerzo |
|------|-------------|----------|
| SSE para live updates | PostgreSQL `LISTEN/NOTIFY` → SSE endpoint → htmx swap. | M |
| Indicadores de actividad | "Dev A guardo una decision hace 30s" en el dashboard. | S |

### 6.3 Analytics

| Item | Descripcion | Esfuerzo |
|------|-------------|----------|
| Metricas por developer | Ratio project:personal, skills consultadas, session summaries completados. | M |
| Metricas por proyecto | Observaciones/dia, top topics, skills mas consultadas. | M |
| Metricas de adoption | % de devs con engagement activo, devs que no usan engram. | M |

---

## Phase 7: Integraciones y Observabilidad

**Objetivo**: conectar engram con el ecosistema de desarrollo de la organizacion.

### 7.1 Observabilidad

| Item | Descripcion | Esfuerzo |
|------|-------------|----------|
| Logging JSON estructurado | pino/zerolog con request_id, user_id, project. | M |
| Metricas Prometheus | RED metrics, histogram de latencia por endpoint, gauge de connections. | M |
| Health check avanzado | `/health` con detalle: PostgreSQL lag, connections pool, disk usage, maintenance status. | S |

### 7.2 Integraciones

| Item | Descripcion | Esfuerzo |
|------|-------------|----------|
| Webhook en cambios | Notificar sistemas externos cuando cambian skills o memorias. | M |
| CI/CD verification | `engram cloud verify --project X` — script para CI que verifica convenciones antes de merge. | L |
| Busqueda remota (`--remote` flag) | `mem_search("auth", remote=true)` busca en cloud en vez de local. Para datos frescos. | M |

### 7.3 Encryption at rest para personales

| Item | Descripcion | Esfuerzo |
|------|-------------|----------|
| Encryption del content para scope=personal | Server almacena ciphertext, key derivada de la API key del usuario. Admin ve metadata pero no contenido. | L |
| Key rotation | Cuando se rota la API key, re-encriptar observations personales. | L |

---

## Backlog (sin fase asignada)

Items que podrian ser relevantes pero no tienen prioridad asignada.

| Item | Descripcion | Trigger |
|------|-------------|---------|
| Table partitioning por proyecto | Particionar observations cuando supere ~10M rows. | Monitoreo de performance |
| Hybrid Logical Clocks | Reemplazar advisory lock por HLC para concurrencia extrema. | >500 devs por proyecto |
| Pull reactivo en search | Si `mem_search` no encuentra resultados, hacer pull incremental antes de reportar vacio. | Feedback de usuarios |
| Archivado de observations antiguas | Mover observations >1 ano a tabla archive. | Cuando la tabla supere ~20M rows |
| Soporte multi-region | Read replicas en regiones diferentes. | Equipos distribuidos globalmente |
| SSO / SAML | Autenticacion enterprise con identity provider corporativo. | Clientes enterprise |
| Rate limiting adaptativo | Ajustar limites por patron de uso, no por threshold fijo. | Cuando el rate limiting actual sea insuficiente |

---

## Leyenda de esfuerzo

| Simbolo | Significado | Estimacion aproximada |
|---------|-------------|----------------------|
| S | Small | 1-2 horas |
| M | Medium | 4-8 horas |
| L | Large | 2-5 dias |
| XL | Extra Large | 1-2 semanas |

Estas estimaciones son para un developer senior familiarizado con el codebase.
