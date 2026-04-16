[← Volver al README](../README.md)

# Engram Cloud — Respuestas a Preguntas de Escalabilidad y Arquitectura

Documento preparado para debates tecnicos sobre la propuesta de Engram Cloud para equipos de 50-200 developers.

---

## Tabla de Contenidos

- [1. Degradacion de busquedas con volumen](#1-degradacion-de-busquedas-con-volumen)
- [2. Filtrado por proyecto y rendimiento](#2-filtrado-por-proyecto-y-rendimiento)
- [3. Queries en cloud vs sync-only](#3-queries-en-cloud-vs-sync-only)
- [4. Observaciones personales en cloud](#4-observaciones-personales-en-cloud)
- [5. Contention en pushes concurrentes](#5-contention-en-pushes-concurrentes)
- [6. Crecimiento infinito de datos](#6-crecimiento-infinito-de-datos)
- [7. Seguridad multi-tenant](#7-seguridad-multi-tenant)
- [8. Indices: cuando se vuelven contraproducentes](#8-indices-cuándo-se-vuelven-contraproducentes)
- [9. Hash collisions en advisory locks](#9-hash-collisions-en-advisory-locks-renumerado)
- [10. Polling vs real-time](#10-polling-vs-real-time)
- [11. Skills desde cloud: carga y sincronizacion](#11-skills-desde-cloud-carga-y-sincronizacion)
- [12. Garantias de que el LLM guarde con scope correcto](#12-garantias-de-que-el-llm-guarde-con-scope-correcto)
- [13. Propagacion de conocimiento incorrecto](#13-propagacion-de-conocimiento-incorrecto)

---

## 1. Degradacion de busquedas con volumen

### La pregunta
> Con 70 developers repartidos en proyectos, se degradan las busquedas en algun momento?

### Numeros reales

Datos medidos sobre uso real de engram (un developer activo, 34 dias de datos):
- **Promedio**: ~27 observations/dia (en dias activos)
- **Pico**: ~53 observations/dia (dias de trabajo intenso)
- **Dias activos**: 56% de los dias del periodo medido

| Escenario | Devs | Obs/dia | Obs/mes | Obs/año |
|-----------|------|---------|---------|---------|
| Conservador (27/dev/dia) | 70 | ~1,900 | ~40K | ~480K |
| Pico sostenido (53/dev/dia) | 70 | ~3,700 | ~80K | ~960K |
| Crecimiento 2x | 140 | ~3,800-7,400 | ~80K-160K | ~960K-1.9M |
| Proyeccion 3 años | 70 | — | — | ~1.4M-2.9M |

### Posicion

PostgreSQL maneja tablas de decenas de millones de rows sin degradacion SI los indices estan bien disenados. El schema actual tiene los indices correctos para CRUD y pull, pero tiene un punto debil en full-text search que requiere una optimizacion concreta.

### El problema: FTS sin composite index

El indice GIN actual es global a TODA la tabla `observations`:

```sql
CREATE INDEX idx_obs_search ON observations USING GIN(search_vector);
```

Cuando un developer busca "JWT authentication", PostgreSQL:
1. Consulta el GIN index → obtiene TODOS los document IDs que matchean (de todos los proyectos)
2. Luego filtra por `project = X AND deleted_at IS NULL AND scope visibility`

Con 3.6M rows, el GIN devuelve potencialmente miles de candidatos ANTES de filtrar por proyecto. Es un index scan + filter, no un index-only scan.

### Solucion: `btree_gin` composite index (reemplaza al GIN simple)

```sql
CREATE EXTENSION btree_gin;

-- REEMPLAZAR el indice GIN simple por un composite que incluye project
DROP INDEX idx_obs_search;  -- ya no necesario
CREATE INDEX idx_obs_search_project ON observations USING GIN(project, search_vector);
```

Esto permite que PostgreSQL use el GIN index filtrando por proyecto Y por texto en una sola operacion. Es la solucion mas pragmatica — una linea de SQL, sin cambios de schema, sin partitioning.

**Importante**: el indice nuevo REEMPLAZA al viejo, no se suma. Todas las queries de search ya filtran por proyecto, asi que el GIN simple queda redundante. Eliminarlo evita write amplification innecesaria (cada insert actualiza un GIN index menos).

**Tradeoff**: el indice composite es ~20% mas grande que el simple (almacena project como parte del GIN tree) y `btree_gin` es una extension que hay que habilitar en el servidor. Disponible en todas las versiones de PostgreSQL >= 9.4 y en todos los managed services (RDS, Cloud SQL, Supabase).

### Alternativa para volumen extremo: Table partitioning

Si se supera ~10M rows, la siguiente escalacion es particionar por proyecto:

```sql
CREATE TABLE observations (...) PARTITION BY LIST (project);
CREATE TABLE observations_myproject PARTITION OF observations FOR VALUES IN ('myproject');
```

Cada particion tiene su propio GIN index. FTS busca SOLO dentro del proyecto.

**Tradeoff**: requiere crear particiones dinamicamente (DDL en runtime), queries cross-project son mas lentas, mas complejidad operacional.

### Recomendacion

| Volumen | Solucion | Esfuerzo |
|---------|----------|----------|
| < 5M rows | `btree_gin` composite index | 1 linea SQL |
| 5-50M rows | Table partitioning por proyecto | ~300 LOC + DDL management |
| > 50M rows | Partitioning + read replicas | Infraestructura |

**Para 70 devs: `btree_gin` es suficiente por al menos 3-5 años** (con datos reales de ~27 obs/dev/dia, se alcanza 1M rows recien al año 2).

---

## 2. Filtrado por proyecto y rendimiento

### La pregunta
> Filtrar por proyecto acelera las consultas?

### Posicion

Si, significativamente — pero depende de que el indice lo soporte.

### Analisis por tipo de query

| Query | Indice actual | Eficiente? | Mejora necesaria |
|-------|---------------|-----------|------------------|
| `WHERE project = X AND scope = Y` | `idx_obs_project_scope` (composite) | Si | Ninguna |
| `WHERE topic_key = X AND project = Y` | `idx_obs_topic_key` (composite) | Si | Ninguna |
| `WHERE search_vector @@ query AND project = X` | `idx_obs_search` (GIN solo) | Parcial | `btree_gin` composite |
| `WHERE server_seq > N` (pull) | `idx_obs_server_seq` (B-tree solo) | Parcial | Agregar `(project, server_seq)` |
| `WHERE project = X AND server_seq > N` (pull filtrado) | No existe | No | Crear `idx_obs_project_seq` |

### Optimizaciones concretas

```sql
-- 1. FTS con filtro de proyecto (REEMPLAZA idx_obs_search)
CREATE EXTENSION btree_gin;
DROP INDEX idx_obs_search;
CREATE INDEX idx_obs_search_project ON observations USING GIN(project, search_vector);

-- 2. Pull filtrado por proyecto (REEMPLAZA idx_obs_server_seq)
DROP INDEX idx_obs_server_seq;
CREATE INDEX idx_obs_project_seq ON observations(project, server_seq);
```

Los indices nuevos REEMPLAZAN a los viejos, no se suman. Los indices `idx_obs_search` y `idx_obs_server_seq` quedan redundantes porque todas las queries ya filtran por proyecto. Reemplazar en vez de agregar mantiene el mismo numero de indices (6) y evita write amplification innecesaria.

El indice `(project, server_seq)` convierte el pull de un sequential scan global a un range scan dentro del proyecto. Con 70 devs haciendo pull cada 120s, esto es critico.

### Numeros estimados

| Operacion | Sin indice optimizado | Con indice optimizado |
|-----------|----------------------|----------------------|
| FTS "JWT auth" en 3.6M rows | ~50-200ms | ~5-20ms |
| Pull since_seq (global scan) | ~20-100ms | ~2-10ms |
| Topic key lookup | ~1-5ms | ~1-5ms (ya optimizado) |

---

## 3. Queries en cloud vs sync-only

### La pregunta
> Se harian consultas en cloud o solo sirve como punto de sincronizacion y fuente de la verdad?

### Posicion

**El cloud sirve para AMBAS cosas**, pero con roles distintos:

### Modelo actual: local-first con sync

```
Developer A guarda decision → SQLite local → push (10s) → PostgreSQL cloud
                                                              ↓
Developer B hace pull (120s) → SQLite local ← pull ← PostgreSQL
Developer B busca "auth" → SQLite local FTS5 → resultado
```

El search del developer es SIEMPRE local. El cloud es sync + fuente de verdad.

### Problema: ventana de inconsistencia

Si Dev A guarda una decision y Dev B busca 30 segundos despues, Dev B NO la encuentra porque:
- Push de A: ~10s de debounce
- Pull de B: cada 120s
- Ventana total: hasta ~130 segundos de delay

Para la mayoria de casos esto es aceptable — las memorias no son chat en tiempo real. Pero para skills criticas (ej: "esta dependencia esta prohibida"), 2 minutos de delay puede significar que un dev instala algo que no deberia.

### Solucion: busqueda remota explicita

Agregar un flag `--cloud` o `--remote` a `mem_search`:

```
mem_search("auth middleware", remote=true)
```

- Sin flag: busqueda local (5ms, funciona offline) — default
- Con flag: busqueda contra el cloud server (100-300ms, datos frescos)

El agente usa busqueda local por defecto. La Team Rule puede instruir al agente a usar `--remote` para temas criticos (skills, stack aprobado).

### Alternativa: pull reactivo

Cuando el agente hace `mem_search` y no encuentra resultados, automaticamente hace un pull incremental antes de reportar "no encontre nada". Esto agrega ~200ms al caso de "no resultados" pero garantiza datos frescos cuando importa.

**Tradeoff**: agrega latencia al caso negativo. El agente no siempre sabe si "no encontre" es porque no existe o porque no se sincronizo.

### Recomendacion

1. **Mantener search local como default** — es la experiencia correcta para el 95% de los casos
2. **Agregar `remote` flag** — explicito, predecible, no rompe offline
3. **Considerar pull reactivo** como optimizacion futura para skills criticas

### Queries directas al cloud (dashboard, API externa)

El servidor cloud TAMBIEN expone endpoints CRUD y search para:
- **Dashboard web**: visualizar memorias, editar skills, audit log
- **CI/CD**: verificar convenciones antes de merge
- **Integraciones**: conectar con Redmine, Jira, etc.

Estos queries van directo a PostgreSQL, sin pasar por SQLite local.

---

## 4. Observaciones personales en cloud

### La pregunta
> Tiene sentido subir observaciones personales a cloud?

### Posicion: si, pero configurable

### Razones para sincronizar personales

1. **Multi-dispositivo**: el dev trabaja en laptop y desktop, necesita sus notas en ambos
2. **Backup**: si el SQLite local se corrompe, las personales se recuperan del cloud
3. **El protocolo ya lo maneja**: el pull filtra `created_by = user_id`, nadie mas las ve via API

### Razones para no sincronizarlas

1. **Privacidad**: un admin con acceso a la DB puede leerlas
2. **Compliance**: algunas organizaciones no quieren datos personales en servidores compartidos
3. **Volumen**: las personales son high-volume, low-shared-value

### Solucion recomendada

**Default: sincronizar personales. Configurable por dev o por organizacion.**

```yaml
# Configuracion de enrollment
sync:
  personal_observations: true  # default
  # false = personales solo viven en SQLite local
```

Para organizaciones con requisitos de compliance estrictos:

**Phase futura: encryption at rest para scope=personal**
- El servidor almacena el content como ciphertext
- La key de encriptacion la tiene solo el usuario (derivada de su API key o una passphrase)
- El admin ve metadata (title, topic_key, timestamps) pero NO el contenido
- **Tradeoff**: FTS no funciona sobre content encriptado. Busqueda personal solo funciona local o sobre metadata.

### Para 70 devs internos con confianza

La configuracion default (sync personales, sin encriptacion) es suficiente. Agregar el flag de opt-out para devs que prefieran no sincronizar. La encriptacion es una feature de enterprise que se implementa cuando hay clientes externos.

---

## 5. Contention en pushes concurrentes

### La pregunta
> El advisory lock per-project bloquea pushes concurrentes?

### Posicion: si, por diseno — y esta bien para este volumen

```go
SELECT pg_advisory_xact_lock(hashtext($1)::bigint)
```

Si 10 devs del mismo proyecto hacen push simultaneo, se serializan. Cada push tipico (1-5 mutations) toma ~1-5ms por lock. Con 10 concurrent, el ultimo espera ~50ms. **Aceptable.**

### El problema real: pushes grandes

Si alguien hace un push de 500 observations (migracion, import masivo), bloquea a todos los demas devs del proyecto por segundos.

### Solucion: batch limit en el protocolo

```json
{
  "max_mutations_per_push": 50,
  "error": "BATCH_TOO_LARGE",
  "message": "Maximum 50 mutations per push. Split into multiple requests."
}
```

El cliente chunkenea automaticamente:
1. Lee sync_mutations pendientes (ej: 200)
2. Agrupa en lotes de 50
3. Envia 4 requests secuenciales con idempotency keys
4. Cada lote toma el lock por ~25ms

**Tradeoff**: el cliente necesita logica de chunking + retry parcial. Pero ya tenemos idempotency keys, asi que el retry es seguro.

### Alternativa descartada: Hybrid Logical Clocks

HLC eliminaria el lock central, pero a cambio:
- Perdes el ordering total (no hay un `server_seq` global por proyecto)
- El pull necesita merge de streams parcialmente ordenados
- La complejidad del cliente se multiplica x5
- Over-engineering para el volumen actual

**Conclusion**: advisory lock per-project + batch limit es la solucion correcta. HLC es para cuando un proyecto tiene 500+ devs haciendo push por segundo — eso no es engram, es un OLTP database.

---

## 6. Crecimiento infinito de datos

### La pregunta
> La tabla observations va a crecer infinitamente?

### Posicion: si, sin una retention policy. Y eso NO es opcional para produccion.

### Proyeccion de crecimiento

| Tabla | Crecimiento/año (70 devs) | En 3 años |
|-------|--------------------------|-----------|
| observations | ~1.8M-3.6M | 5.4M-10.8M |
| observation_revisions | ~200K-500K | 600K-1.5M |
| sessions | ~50K-100K | 150K-300K |
| prompts | ~100K-200K | 300K-600K |
| idempotency_keys | Purgeable (TTL 24h) | N/A |
| rate_limits | Purgeable (ventana deslizante) | N/A |

### Solucion: maintenance job con tres estrategias

**1. Purge de tombstones procesados**

```sql
-- Solo purgar tombstones que TODOS los clientes ya procesaron
DELETE FROM observations
WHERE deleted_at IS NOT NULL
  AND server_seq < (
    SELECT MIN(last_seq) FROM sync_cursors WHERE project = observations.project
  )
  AND deleted_at < now() - interval '30 days';
```

Requiere que todos los clientes hayan hecho pull mas alla del tombstone. Si un dev no se conecta por 60 dias, sus tombstones no se purgan (correcto — al reconectarse los necesita).

**2. Purge de revisiones antiguas**

```sql
DELETE FROM observation_revisions
WHERE superseded_at < now() - interval '90 days';
```

Nadie necesita el historial de conflictos de hace 3 meses. Si lo necesitan, esta en el audit log.

**3. Archivado de observations antiguas** (futuro)

Mover observations con `updated_at < now() - interval '1 year'` a una tabla `observations_archive`. El search no incluye archived por default. Un flag `--include-archived` lo habilita.

**Tradeoff del archivado**: `mem_context` (que retorna memorias recientes) no se ve afectado. Pero `mem_search` puede perder resultados relevantes si una decision de arquitectura vieja sigue vigente. Solucion: las skills nunca se archivan — solo observations regulares.

### Frecuencia recomendada

| Job | Frecuencia | Hora |
|-----|-----------|------|
| Purge idempotency_keys (TTL 24h) | Diario | 03:00 UTC |
| Purge rate_limits (ventana vieja) | Diario | 03:00 UTC |
| Purge tombstones (>30 dias, todos los cursores pasaron) | Semanal | Domingo 04:00 UTC |
| Purge revisiones (>90 dias) | Mensual | Primer domingo 04:00 UTC |
| Archivado (>1 año) | Trimestral | Manual o automatico |

---

## 7. Seguridad multi-tenant

### La pregunta
> Es seguro el modelo multi-tenant actual?

### Posicion: funcional pero no robusto. Para enterprise necesita PostgreSQL RLS.

### Modelo actual: row-level filtering en queries

```sql
AND (o.scope = 'project' OR (o.scope = 'personal' AND o.created_by = $3))
```

Esto es filtrado en la query, no Row-Level Security. Un bug en UNA query puede leakear datos personales de otro developer.

### Solucion: PostgreSQL RLS

```sql
ALTER TABLE observations ENABLE ROW LEVEL SECURITY;

CREATE POLICY obs_project_access ON observations
    FOR ALL
    USING (
        EXISTS (
            SELECT 1 FROM project_members pm
            JOIN projects p ON p.id = pm.project_id
            WHERE p.name = observations.project
              AND pm.user_id = current_setting('app.user_id')::uuid
        )
        AND (
            scope = 'project'
            OR (scope = 'personal' AND created_by = current_setting('app.user_id')::uuid)
        )
    );
```

Cada conexion setea `app.user_id`:

```go
conn.Exec(ctx, "SET LOCAL app.user_id = $1", userID)
```

### Tradeoffs

| Aspecto | Sin RLS | Con RLS |
|---------|---------|---------|
| Seguridad | Depende de que CADA query filtre correctamente | Seguro por construccion |
| Performance | Sin overhead | Subquery de membership en cada operacion (~1ms) |
| Testing | Tests normales | Necesita setear `app.user_id` en cada test |
| Complejidad | Baja | Media |
| Riesgo de bug | Un query sin filtro = data leak | Imposible — PostgreSQL lo enforce |

### Recomendacion

**Para 70 devs en una empresa: RLS es lo correcto.** El overhead de ~1ms por query es invisible. La garantia de seguridad por construccion vale la complejidad adicional en testing.

Implementar ANTES de habilitar datos sensibles (skills locked, datos de compliance).

---

## 8. Indices: cuándo se vuelven contraproducentes

### La pregunta
> No hay riesgo de que tantos indices degraden los writes?

### Posicion: si, los indices NO son gratis. Por eso reemplazamos en vez de agregar.

### Write amplification

Cada INSERT/UPDATE actualiza TODOS los indices de la tabla. La tabla `observations` tiene actualmente 6 indices. Si agregaramos los 2 nuevos sin eliminar los viejos, serian 8 — cada push escribiria en 8 estructuras.

**Nuestra decision**: los indices nuevos REEMPLAZAN a los viejos redundantes. Se mantiene en 6 indices.

```sql
-- REEMPLAZAR, no agregar
DROP INDEX idx_obs_search;      -- reemplazado por idx_obs_search_project (GIN composite)
DROP INDEX idx_obs_server_seq;  -- reemplazado por idx_obs_project_seq (btree composite)
```

### Overhead por tipo de indice

| Tipo | Costo por INSERT | Notas |
|------|-----------------|-------|
| B-tree | ~0.01-0.05ms | Barato, la mayoria de nuestros indices |
| GIN | ~0.1-0.5ms | Mas caro; usa pending list que se mergea periodicamente |
| Total (6 indices) | ~1-2ms | Con ~1,900 inserts/dia (70 devs), es un insert cada ~45s. Irrelevante. |

### GIN pending list

Los GIN indexes no escriben directo al arbol en cada insert — acumulan cambios en una pending list y los mergean periodicamente (controlado por `gin_pending_list_limit`, default 4MB). Esto significa:

- Inserts son rapidos (solo append)
- Las primeras busquedas despues de muchos inserts pueden ser mas lentas (escanean la pending list)
- Con ~27 obs/dev/dia, la pending list nunca acumula mas que unos pocos entries. No es problema real.

### Overhead de espacio

| Indice | Tipo | Tamaño estimado (1M rows) |
|--------|------|--------------------------|
| PK (UUID) | btree | ~30MB |
| idx_obs_search_project | GIN composite | ~120-250MB |
| idx_obs_topic_key | btree partial | ~15MB |
| idx_obs_project_scope | btree partial | ~15MB |
| idx_obs_project_seq | btree | ~25MB |
| idx_obs_numeric_id | btree | ~20MB |
| **Total indices** | | **~225-355MB** |
| **Datos** | | **~500MB-1GB** |

Los indices ocupan ~30-50% del tamaño de los datos. Normal para PostgreSQL.

### Recomendacion

- **REEMPLAZAR** indices redundantes, no agregar encima de los existentes
- Revisar indices una vez al año con `pg_stat_user_indexes` para detectar indices no usados
- El unico caso para agregar un indice nuevo (sin quitar otro) seria si necesitamos queries cross-project — hoy no las tenemos

---

## 9. Hash collisions en advisory locks (renumerado)

### La pregunta
> Puede haber colision en `hashtext(project)::bigint`?

### Posicion: riesgo negligible, pero tiene solucion trivial si alguien lo plantea.

`hashtext()` devuelve `int32` (4 bytes, ~4.3 mil millones de valores). Con 100 proyectos, la probabilidad de colision es ~1 en 43 millones (birthday problem: n^2 / 2m).

Si hay colision, el efecto es que dos proyectos DIFERENTES se serializan entre si al hacer push. No hay corrupcion de datos — solo contention innecesaria.

### Solucion (si la piden)

Usar el ID numerico del proyecto directamente:

```go
// En vez de hashtext del nombre
SELECT pg_advisory_xact_lock((SELECT id FROM project_numeric_ids WHERE name = $1))
```

O usar un hash mas robusto sobre el UUID del proyecto. Pero para <1000 proyectos, `hashtext` es mas que suficiente.

---

## 10. Polling vs real-time

### La pregunta
> Por que polling y no WebSockets/SSE?

### Posicion: polling es correcto para el caso de uso de MCP. SSE para dashboard futuro.

### Analisis

El MCP tool se ejecuta asi:
1. El agente invoca `mem_search` → el MCP tool se levanta
2. Busca en SQLite local → responde
3. El MCP tool termina

No hay proceso persistente corriendo. Un WebSocket requiere un daemon que mantenga la conexion abierta — eso es un cambio de arquitectura mayor.

### Carga de polling actual

70 devs, pull cada 120s = ~35 requests/minuto al endpoint de pull. Esto es trivial para cualquier servidor.

### Cuando SI necesitamos real-time

- **Dashboard web**: para ver cambios en vivo (SSE, no WebSockets — mas simple, reconexion automatica)
- **Notificaciones criticas**: "una skill cambio, hacé pull ahora" (push notification al SyncClient)

### Solucion futura: SSE para dashboard + webhook para urgentes

```
Dashboard (browser) ← SSE ← engram-cloud (PostgreSQL LISTEN/NOTIFY)
SyncClient ← webhook POST ← engram-cloud (solo para skills locked que cambian)
```

**Tradeoff**: SSE requiere persistent connections en el load balancer (sticky sessions o L4). Para 70 devs con dashboard abierto: ~70 connections, ~700KB de memoria. Trivial.

---

## 11. Skills desde cloud: carga y sincronizacion

### La pregunta
> Las skills se cargan desde cloud bajo demanda o se cachean localmente?

### Posicion: se cachean localmente via el protocolo de sync existente

### Por que NO fetch on-demand

1. **Principio local-first**: las skills DEBEN funcionar offline
2. **Performance**: `mem_context` y `mem_search` consultan SQLite local (<5ms). Un fetch al cloud seria 100-300ms
3. **El protocolo de sync ya existe**: las skills son observations con `type: skill`. No hay infra nueva que construir.

### Arquitectura: enrollment cascading con pseudo-proyectos

```
Cloud (PostgreSQL)                    Local (SQLite)
├── __org__ (28 skills org)           ├── __org__ (cacheado via pull)
├── __domain_cargoflow__ (6 skills)   ├── __domain_cargoflow__ (cacheado)
├── cargoflow-api (memorias)          └── cargoflow-api (memorias + pull)
└── cargoflow-etl (memorias)
```

**Flujo**:
1. Dev ejecuta `engram cloud enroll cargoflow-api`
2. El servidor resuelve: cargoflow-api → domain cargoflow → org JPH Lions
3. El servidor auto-enrolla al dev en `__org__` y `__domain_cargoflow__`
4. El SyncClient hace pull de los 3 pseudo-proyectos
5. Las skills se guardan en SQLite local como observations normales
6. `mem_context` las retorna fusionadas: org skills + domain skills + memorias del proyecto

**Actualizacion de diffs**: cuando un lead edita una skill en el cloud (via dashboard o CLI), la skill recibe un nuevo `server_seq`. En el proximo pull (cada 120s), el SyncClient la baja y actualiza la copia local. Solo se transfiere el diff — no hay re-descarga completa.

### Tradeoffs

| Aspecto | Pro | Contra |
|---------|-----|--------|
| Offline | Skills disponibles sin internet | Pueden estar desactualizadas hasta el proximo pull |
| Performance | <5ms para `mem_context` | N/A |
| Duplicacion | Cada dev tiene copia local | ~28 skills de org x 70 devs = 1960 copias (pero cada una pesa <2KB, total ~4MB) |
| Consistencia | Eventualmente consistente (120s) | Una skill critica tarda hasta 2min en propagarse |
| Complejidad | Usa infraestructura existente | Auto-enrollment de pseudo-proyectos es logica nueva |

### Para skills criticas con propagacion urgente

Cuando se edita una skill `locked`, el servidor puede:
1. Incrementar `server_seq` normalmente
2. Enviar una notificacion push (webhook) a los SyncClients activos: "pull ahora"
3. El SyncClient hace un pull inmediato fuera del ciclo de 120s

Esto reduce la ventana de inconsistencia de 120s a ~1-2s para skills criticas. Es un "nudge", no un requisito — si el webhook falla, el pull regular lo cubre.

---

## 12. Garantias de que el LLM guarde con scope correcto

### La pregunta
> Si no subimos personales, como aseguramos que el LLM no guarde todo como personal?

### Posicion: tres capas de defensa, de menos a mas intrusiva

### Capa 1: Default del tool (ya implementada)

`mem_save` defaultea a `scope: project`. Si el LLM no especifica scope, la observacion va como project. Esto cubre ~80% de los casos — la mayoria de las saves son decisiones, bugs, convenciones.

### Capa 2: Team Rule enforced (configuracion)

La Team Rule enforced que describimos incluye ejemplos explicitos:

```markdown
### Cuando usar cada scope

- `project` (default): decisiones de arquitectura, bugs descubiertos, convenciones,
  notas sobre el codebase, ADRs, cualquier cosa que otro developer necesite saber.
- `personal`: preferencias de tu entorno de desarrollo, notas sobre tu workflow personal,
  cosas que SOLO te importan a ti.

Regla: si dudas, usa `project`. Es mejor compartir de mas que perder conocimiento.
```

El LLM lee esto en CADA conversacion porque es una Team Rule enforced. No puede ignorarla.

### Capa 3: Guardrail server-side (nueva)

El servidor puede analizar el ratio de saves por scope y retornar warnings:

```json
{
  "status": "ok",
  "warnings": [
    {
      "code": "HIGH_PERSONAL_RATIO",
      "message": "75% of your recent saves are personal-scoped. Consider using project scope for decisions and discoveries that benefit the team."
    }
  ]
}
```

El LLM ve este warning en la respuesta del push y se autocorrige en saves subsiguientes.

**Implementacion**: el servidor trackea un rolling window (ultimas 50 saves) por usuario. Si >60% son personal, agrega el warning al push response.

### Capa 4: Analytics en dashboard (visibilidad)

El dashboard muestra metricas por developer:
- Ratio project:personal
- Skills consultadas vs ignoradas
- Session summaries completados vs saltados

Esto no es enforcement automatico — es visibilidad para leads. Si un dev consistentemente guarda todo como personal, el lead lo ve y lo corrige.

### Recomendacion de implementacion

| Capa | Esfuerzo | Impacto | Cuando |
|------|----------|---------|--------|
| Default del tool | Ya existe | Alto | Ya |
| Team Rule con ejemplos | Configuracion | Alto | Al desplegar |
| Warning server-side | ~50 LOC | Medio | Phase 4 |
| Analytics dashboard | Feature del dashboard | Bajo (visibilidad) | Phase 5 |

---

## 13. Propagacion de conocimiento incorrecto

### La pregunta
> Si un developer guarda una observacion incorrecta, se propaga a todo el equipo. Como evitamos que un error de una persona afecte las decisiones de IA de todos?

### Posicion: dos capas complementarias — verificacion + confidence con decay

Este es el riesgo mas serio del sistema. Un bug en codigo afecta un proyecto. Un "bug" en knowledge afecta TODOS los proyectos que lean esa observacion.

### Tipos de conocimiento incorrecto

| Tipo | Ejemplo | Gravedad | Frecuencia |
|------|---------|----------|------------|
| Factualmente incorrecto | "Este servicio usa REST" cuando es GraphQL | Alta | Baja |
| Desactualizado | "Usamos Express" — era cierto, migramos a NestJS | Media | Alta |
| Fuera de contexto | Decision valida para proyecto A guardada sin especificar proyecto | Alta | Media |
| Opinion como hecho | "Siempre usar Repository pattern" guardado como convencion | Media | Alta |
| Entendimiento parcial | Correcto pero omite un edge case critico | Media | Media |

### Capa 1: Verificacion por Team Rule

La Team Rule enforced instruye al agente a verificar ANTES de actuar:

```markdown
### Uso de conocimiento de otros developers (OBLIGATORIO)

Cuando encuentres una observacion de otro developer:
1. NUNCA la tomes como verdad absoluta — verifica contra el codigo actual
2. Si la observacion dice "usamos X", confirma con un grep/read antes de actuar
3. Si contradice lo que ves en el codigo, el CODIGO gana siempre
4. Reporta la contradiccion con mem_challenge para que se corrija
```

**Costo en tokens de la verificacion**:

| Escenario | Tokens/sesion | Ahorro vs sin engram |
|-----------|---------------|---------------------|
| Sin engram (descubrir contexto desde cero) | ~60,000-170,000 | Baseline |
| Con engram, sin verificacion (hoy) | ~8,000-16,000 | ~85-90% |
| Con engram + verificacion obligatoria (Capa 1) | ~11,000-26,000 | ~75-85% |
| Con engram + confidence decay (Capa 1 + 2) | ~9,500-21,000 | ~85-88% |

El overhead de verificar es ~3,000-10,000 tokens extra por sesion (~5-7% del ahorro total). La Capa 2 recupera la mayor parte de ese overhead.

### Capa 2: Confidence con exponential decay

Agregar un campo `confidence` que determina si el agente necesita verificar o puede confiar directamente.

**Niveles base**:

| Estado | base_confidence | Significado |
|--------|----------------|-------------|
| `unverified` | 0.5 | Recien guardada, nadie la valido |
| `confirmed` | 1.0 | Al menos otro dev/agente la verifico contra codigo |
| `challenged` | 0.1 | Alguien la marco como posiblemente incorrecta |

**Formula de decay**:

```
effective_half_life = base_half_life × (1 + ln(max(1, unique_confirmations)))
days_since = now() - last_confirmed_at
decay_factor = 0.5 ^ (days_since / effective_half_life)
effective_confidence = base_confidence × decay_factor
```

**Half-life por tipo de observacion**:

| Tipo | Half-life base | Razon |
|------|---------------|-------|
| `skill` (locked) | 365 dias | Gobernada, cambia por proceso formal |
| `skill` (overridable) | 180 dias | Gobernada pero con excepciones |
| `architecture` | 90 dias | Decisiones duran trimestres |
| `decision` | 60 dias | Decisiones tacticas cambian mas rapido |
| `pattern` | 60 dias | Patterns evolucionan |
| `discovery` | 45 dias | Descubrimientos se vuelven obvios o irrelevantes |
| `bugfix` | 30 dias | El codigo donde estaba el bug puede cambiar |
| `config` | 30 dias | Configs cambian frecuentemente |

**Boost por multiples confirmaciones (logaritmico)**:

| Confirmaciones unicas | Multiplicador half-life | Half-life `architecture` |
|----------------------|------------------------|--------------------------|
| 1 | 1.0x | 90 dias |
| 2 | 1.69x | 152 dias |
| 3 | 2.10x | 189 dias |
| 5 | 2.61x | 235 dias |
| 10 | 3.30x | 297 dias |

Logaritmico porque la 10ma confirmacion agrega menos certeza que la 2da.

**Thresholds de accion para el agente**:

```
confidence >= 0.7  → TRUSTED    — usar sin verificar (ahorra tokens)
confidence 0.3-0.7 → VERIFY     — verificar contra codigo antes de usar
confidence < 0.3   → UNTRUSTED  — warning, no actuar sin confirmacion explicita
```

**Ejemplo concreto**: observacion "Auth usa JWT con refresh tokens en Redis" (tipo: architecture, half-life: 90 dias, confirmada dia 0):

| Dia | Decay | Confidence | Accion del agente |
|-----|-------|------------|-------------------|
| 0 | 1.0 | 1.0 | Usa sin verificar |
| 30 | 0.79 | 0.79 | Usa sin verificar |
| 60 | 0.63 | 0.63 | Usa sin verificar (por poco) |
| 90 | 0.50 | 0.50 | **Verifica** (cayo debajo de 0.7) |
| 120 | 0.40 | 0.40 | Verifica |
| 180 | 0.25 | 0.25 | **Warning** (debajo de 0.3) |

Si en el dia 90 otro agente verifica y confirma → el reloj se resetea a 1.0. Cada confirmacion evita verificaciones futuras para todo el equipo.

**Efecto virtuoso — el sistema se auto-optimiza**:

```
Mas confirmaciones → mayor confidence → menos verificaciones
→ menos tokens gastados → pero cada verificacion que SI se hace
→ confirma o challenge → alimenta confidence → ciclo se repite
```

Al principio verifica mucho (mas tokens). Despues de semanas de uso con 70 devs, la mayoria de las observaciones utiles estan confirmed y el overhead cae a casi cero.

**Implementacion**: calculo client-side en `mem_search` (local-first, funciona offline). El store local tiene `base_confidence`, `last_confirmed_at`, `confirmed_count`, y `half_life`. El calculo es una multiplicacion y una exponencial por observation — trivial.

**Campos nuevos**:

```sql
confidence        TEXT NOT NULL DEFAULT 'unverified',
confirmed_by      TEXT[],
confirmed_count   INTEGER DEFAULT 0,
last_confirmed_at TIMESTAMPTZ,
challenged_by     UUID,
challenge_reason  TEXT
```

**MCP tools nuevos**: `mem_confirm(id)` y `mem_challenge(id, reason)`.

---

## Resumen ejecutivo

| Preocupacion | Riesgo real | Solucion | Esfuerzo |
|-------------|-------------|----------|----------|
| FTS con volumen | Medio (>1M rows, ~año 2) | `btree_gin` composite index (reemplaza GIN simple) | 1 linea SQL |
| Pull sin filtro de proyecto | Medio | Index `(project, server_seq)` (reemplaza server_seq solo) | 1 linea SQL |
| Write amplification por indices | Bajo (controlado) | Reemplazar indices redundantes, no agregar | 0 LOC extra |
| Push grande bloquea equipo | Medio | Batch limit 50 mutations | ~50 LOC |
| Crecimiento infinito | Alto (produccion) | Retention policy + purge job | ~200 LOC + cron |
| Data leak multi-tenant | Medio-Alto | PostgreSQL RLS | ~100 LOC SQL |
| Skills offline | Critico | Cache local via sync protocol | ~200 LOC |
| LLM scope incorrecto | Medio | 3 capas: default + rule + warning | Progresivo |
| Conocimiento incorrecto propagado | Alto | Verificacion + confidence con decay | ~150 LOC + 2 MCP tools |
| Polling vs real-time | Bajo | Polling OK; SSE para dashboard futuro | Cuando haya dashboard |
| Advisory lock contention | Bajo (<100 devs) | Ya per-project; batch limit mitiga | Ya resuelto |
| Hash collision | Negligible | N/A para <1000 proyectos | No action |
