[← Volver al README](../README.md)

# Engram — Arquitectura del Sistema

Referencia tecnica de la arquitectura interna, estructura de modulos y diseno de sincronizacion cloud.

---

## Tabla de Contenidos

- [Que es Engram?](#que-es-engram)
- [Principios de Diseno](#principios-de-diseno)
- [Vista General del Sistema](#vista-general-del-sistema)
- [Mapa de Paquetes](#mapa-de-paquetes)
- [Flujo de Datos](#flujo-de-datos)
- [Esquema de Base de Datos](#esquema-de-base-de-datos)
- [Herramientas MCP](#herramientas-mcp)
- [Arquitectura de Sincronizacion Cloud](#arquitectura-de-sincronizacion-cloud)
- [Estrategia de Testing](#estrategia-de-testing)
- [Dependencias Externas](#dependencias-externas)
- [Estructura de Archivos](#estructura-de-archivos)

---

## Que es Engram?

Engram es un **sistema de memoria persistente para agentes de IA de programacion**. Cuando un agente (Claude Code, OpenCode, Gemini CLI, Codex, etc.) termina una sesion, todo lo que aprendio — decisiones, fixes de bugs, convenciones, descubrimientos — desaparece. Engram le da un cerebro.

Un unico binario en Go con SQLite + FTS5 (busqueda full-text), expuesto via CLI, API HTTP, servidor MCP y una TUI interactiva. Funciona con **cualquier agente** que soporte MCP.

> **engram** `/ˈen.ɡræm/` — *neurociencia*: la huella fisica de un recuerdo en el cerebro.

---

## Principios de Diseno

1. **Local-first**: SQLite es siempre la fuente de verdad. El cloud es replicacion y acceso compartido.
2. **Agnostico del agente**: Funciona con cualquier agente compatible con MCP. Sin dependencia de un proveedor.
3. **Cero dependencias**: Un solo binario, sin Node.js, sin Python, sin Docker para uso local.
4. **Fallar ruidosamente**: Cuando la sincronizacion se bloquea o una politica impide una operacion, reportarlo — nunca descartar datos silenciosamente.

---

## Vista General del Sistema

```
┌─────────────────────────────────────────────────────────────────────┐
│                        cmd/engram (CLI)                             │
│                                                                     │
│  engram mcp          engram serve       engram tui                  │
│  (MCP stdio)         (HTTP REST)        (Bubbletea)                 │
│                                                                     │
│  engram cloud setup/sync/status         engram sync                 │
│  (CLI sync cloud)                       (Sync via git)              │
└───────────┬──────────────┬───────────────┬──────────────────────────┘
            │              │               │
     ┌──────▼──────┐ ┌────▼─────┐  ┌──────▼──────┐
     │ internal/mcp│ │ internal/│  │ internal/tui│
     │ (15 tools)  │ │ server   │  │ (Bubbletea) │
     └──────┬──────┘ └────┬─────┘  └──────┬──────┘
            │              │               │
            └──────────────┼───────────────┘
                           │
                    ┌──────▼──────┐
                    │StoreInterface│  ← contrato (internal/types)
                    └──────┬──────┘
                           │
              ┌────────────┼─────────────┐
              │            │             │
       ┌──────▼──────┐    │      ┌──────▼──────┐
       │ store.Store  │    │      │ RemoteStore │ (planificado)
       │ (SQLite+FTS5)│    │      │ (proxy HTTP)│
       └──────┬──────┘    │      └─────────────┘
              │            │
       ┌──────▼──────┐    │
       │ SyncClient   │    │
       │ (push/pull)  │    │
       └──────┬──────┘    │
              │            │
              ▼            ▼
       ┌────────────────────────┐
       │  Servidor engram-cloud │  (cmd/engram-cloud)
       │  cloudserver (chi)     │
       │  cloudstore (pgx/v5)  │
       │  PostgreSQL + tsvector │
       └────────────────────────┘
```

---

## Mapa de Paquetes

### Nucleo

| Paquete | Proposito | Exports Principales |
|---------|-----------|---------------------|
| `internal/types` | Modelo de dominio compartido. Todas las estructuras e interfaces viven aqui. Sin dependencias internas. | `Observation`, `Session`, `Prompt`, `Stats`, `SyncState`, `SyncMutation`, `StoreInterface`, `StoreSyncer` |
| `internal/store` | Motor de persistencia SQLite. Busqueda full-text FTS5, journal de mutaciones de sincronizacion, enrollment de proyectos, almacenamiento de configuracion cloud. | `Store`, `New()`, `Config` |
| `internal/format` | Formatea observaciones, sesiones y prompts en strings de contexto para las herramientas MCP. | `Context()`, `Truncate()` |

### Capas de Acceso

| Paquete | Proposito | Exports Principales |
|---------|-----------|---------------------|
| `internal/mcp` | Servidor MCP via stdio. Expone 15 herramientas en dos perfiles (agent + admin). Los agentes se conectan via transporte stdio. | `NewServerWithConfig()` |
| `internal/server` | API REST HTTP para uso local. Potencia `engram serve`. | `Server`, `New()` |
| `internal/tui` | UI interactiva de terminal (Bubbletea + Lipgloss). Pantallas de dashboard, busqueda y detalle de observaciones. Tema Catppuccin Mocha. | `Model`, `New()` |

### Sincronizacion Cloud

| Paquete | Proposito | Exports Principales |
|---------|-----------|---------------------|
| `internal/cloudserver` | API HTTP para el servidor de sincronizacion cloud (router chi). Middleware de autenticacion, version de protocolo, rate limiting, operaciones batch. | `New(store)` → `http.Handler` |
| `internal/cloudstore` | Backend PostgreSQL para cloud. Usuarios, proyectos, membresia, observaciones con busqueda tsvector, protocolo push/pull, idempotencia, mantenimiento. | `Store`, `New()`, `ProcessPush()`, `Pull()` |
| `internal/remote` | Cliente de sincronizacion cloud. Wrapper HTTP con reintentos, gestion de configuracion, SyncClient para push/pull local-first. | `Client`, `CloudConfig`, `SyncClient` |
| `internal/sync` | Sincronizacion basada en archivos compatible con git (manifiesto + chunks comprimidos). Para compartir memorias via repositorios git sin cloud. | `Syncer`, `Manifest`, `ChunkEntry` |

### Utilidades

| Paquete | Proposito | Exports Principales |
|---------|-----------|---------------------|
| `internal/project` | Detecta el nombre del proyecto a partir de la ruta del filesystem. | `DetectProject()`, `Similar()` |
| `internal/setup` | Instalador de plugins para agentes. Configura Claude Code, OpenCode, Gemini CLI, Codex, VS Code. | `Install()`, `SupportedAgents()` |
| `internal/version` | Verifica nuevas versiones en GitHub. | `CheckLatest()` |
| `internal/obsidian` | Exporta memorias como un grafo de conocimiento de Obsidian (beta). | `Exporter`, `Hub` |

### Binarios

| Binario | Punto de Entrada | Proposito |
|---------|-----------------|-----------|
| `engram` | `cmd/engram/main.go` | CLI principal — servidor MCP, servidor HTTP, TUI, sync, setup, busqueda, comandos cloud |
| `engram-cloud` | `cmd/engram-cloud/main.go` | Servidor de sincronizacion cloud — backend PostgreSQL, API HTTP |

### Grafo de Dependencias

```
cmd/engram
  ├── internal/mcp        (servidor MCP stdio)   → store, project
  ├── internal/server     (API REST HTTP)        → store
  ├── internal/tui        (TUI Bubbletea)        → store
  ├── internal/setup      (instalador de agentes)
  ├── internal/sync       (sync basado en archivos) → store
  ├── internal/remote     (cliente sync cloud)   → types
  ├── internal/obsidian   (exportar a vault)     → store, types
  └── internal/version    (verificar updates)

cmd/engram-cloud
  └── internal/cloudserver (API HTTP)            → cloudstore

internal/store            → internal/types, internal/format
internal/cloudstore       → (PostgreSQL, standalone)
internal/remote           → internal/types (usa interfaz SyncStore, no *store.Store)
internal/types            → (sin deps internas, compartido por todos)
internal/format           → internal/types
```

---

## Flujo de Datos

### Modo Local (por defecto)

```
Agente escribe → herramienta MCP (mem_save) → store.Store → SQLite
Agente lee     → herramienta MCP (mem_search) → store.Store → SQLite FTS5
```

### Local-First con Sync Cloud (`--backend local-sync`)

```
Agente escribe → store.Store → SQLite
                    ↓ (trigger automatico)
               tabla sync_mutations
                    ↓ (background, debounce 10s)
               SyncClient.PushOnce()
                    ↓ (HTTP POST, lotes de 100)
               engram-cloud /api/v1/sync/push
                    ↓
               PostgreSQL (cloudstore)

Escrituras de otros miembros del equipo:
               PostgreSQL → engram-cloud /api/v1/sync/pull
                    ↓ (background, cada 120s, paginas de 500)
               SyncClient.PullOnce()
                    ↓
               store.ApplyPulledMutation() → SQLite
```

### Modo Cloud-Only (`--backend cloud`, planificado)

```
Agente escribe → herramienta MCP → RemoteStore → HTTP POST → engram-cloud → PostgreSQL
Agente lee     → herramienta MCP → RemoteStore → HTTP GET  → engram-cloud → PostgreSQL
```

---

## Esquema de Base de Datos

### SQLite (Store Local)

| Tabla | Proposito |
|-------|-----------|
| `sessions` | Sesiones de programacion con directorio, timestamps, resumen |
| `observations` | Memorias: decisiones, bugs, patrones, descubrimientos |
| `observations_fts` | Tabla virtual FTS5 para busqueda full-text |
| `user_prompts` | Templates de prompts guardados |
| `prompts_fts` | Tabla virtual FTS5 para busqueda de prompts |
| `sync_state` | Cursor de sync, lease, estado de backoff por target |
| `sync_mutations` | Journal de cambios locales pendientes de push (auto-poblado por triggers) |
| `sync_enrolled_projects` | Que proyectos participan en la sincronizacion cloud |
| `sync_cloud_config` | Configuracion de conexion cloud (pares clave-valor) |
| `sync_chunks` | Rastrea chunks sincronizados por git para evitar re-importacion |

### PostgreSQL (Store Cloud)

| Tabla | Proposito |
|-------|-----------|
| `users` | Cuentas de usuario con API keys hasheadas (bcrypt) |
| `projects` | Definiciones de proyectos |
| `project_members` | Membresia usuario-proyecto con roles |
| `observations` | Observaciones cloud con busqueda tsvector |
| `observation_revisions` | Revisiones de conflictos LWW (colisiones de topic_key) |
| `sessions` | Sesiones cloud |
| `prompts` | Prompts cloud |
| `server_seq_counter` | Secuencia monotonica por proyecto (advisory lock) |
| `sync_cursors` | Cursor de pull por usuario y por proyecto |
| `idempotency_keys` | Deduplicacion de requests push (TTL 24h) |
| `rate_limits` | Contadores de ventana deslizante por usuario y por endpoint |

---

## Herramientas MCP

15 herramientas en dos perfiles:

| Categoria | Herramientas | Perfil |
|-----------|-------------|--------|
| **Guardar y Actualizar** | `mem_save`, `mem_update`, `mem_delete`, `mem_suggest_topic_key` | agent/admin |
| **Buscar y Recuperar** | `mem_search`, `mem_context`, `mem_timeline`, `mem_get_observation` | agent |
| **Ciclo de Vida de Sesion** | `mem_session_start`, `mem_session_end`, `mem_session_summary` | agent |
| **Utilidades** | `mem_save_prompt`, `mem_stats`, `mem_capture_passive`, `mem_merge_projects` | agent/admin |

---

## Arquitectura de Sincronizacion Cloud

### Fases de Implementacion

| Fase | Estado | Descripcion |
|------|--------|-------------|
| **Fase 1** | Completa | Migraciones de esquema, extraccion de StoreInterface, triggers de sync_mutations, idempotencia de importacion |
| **Fase 2** | Completa | Cloud server MVP — esquema PostgreSQL, autenticacion, protocolo push/pull, CRUD, busqueda tsvector, endpoint batch, rate limiting, mantenimiento. 32 tests de integracion. |
| **Fase 3** | En Progreso | Integracion del cliente — wrapper HTTP, configuracion, SyncClient (push/pull/debounce/backoff). 32 tests unitarios. Pendiente: RemoteStore, comandos CLI, flag --backend. |
| **Fase 4** | Planificada | Auto-sync, monitoreo, hardening para produccion |

### Protocolo de Sincronizacion

**Push**: El cliente lee `sync_mutations` → agrupa en lotes (100/push) → POST `/api/v1/sync/push` → el servidor asigna `server_seq` monotonica por mutacion → el cliente hace ACK.

**Pull**: El cliente envia cursor `since_seq` → GET `/api/v1/sync/pull` → el servidor retorna entidades con `server_seq > cursor` (500/pagina) → el cliente aplica al store local → avanza el cursor.

**Resolucion de Conflictos**: Last-Writer-Wins (LWW) por `server_seq`. Cuando dos observaciones comparten un `topic_key`, el servidor guarda la version anterior como revision y sobreescribe con la mas nueva.

**Garantias**:
- Orden monotonica via advisory lock por proyecto + contador de secuencia por proyecto
- Secuencias sin gaps (advisory lock dentro de transaccion, rollback recupera valores)
- Push idempotente (claves de idempotencia, TTL 24h)
- Aislamiento de scope (observaciones personales visibles solo para el creador)

### Diseno del SyncClient

```
┌──────────────── SyncClient ─────────────────┐
│                                              │
│  Goroutine de Push      Goroutine de Pull    │
│  ┌─────────────┐        ┌─────────────┐     │
│  │ Debounce    │        │ Ticker      │     │
│  │ (10s luego  │        │ (cada 120s) │     │
│  │  de la      │        │             │     │
│  │  ultima     │        │             │     │
│  │  escritura) │        │             │     │
│  └──────┬──────┘        └──────┬──────┘     │
│         │                      │             │
│         ▼                      ▼             │
│  ┌─────────────┐        ┌─────────────┐     │
│  │ PushOnce()  │        │ PullOnce()  │     │
│  │ - Lease     │        │ - Cursor    │     │
│  │ - Lote 100  │        │ - Pagina 500│     │
│  │ - POST      │        │ - Aplicar   │     │
│  │ - ACK       │        │ - Avanzar   │     │
│  └─────────────┘        └─────────────┘     │
│                                              │
│  Backoff: 30s → 60s → 120s → 300s (max)    │
│  Guard de enrollment: solo sync enrollados   │
│  Shutdown graceful: flush + liberar lease    │
└──────────────────────────────────────────────┘
```

### Mitigacion de Syncs Grandes: Pull en Background por Chunks

La sincronizacion es un **enriquecimiento asincrono**, no un prerequisito. El store local funciona al 100% mientras el sync corre en background.

**Sync inicial (seq=0)**:
- El loop de pull corre en una goroutine de background
- Pagina las entidades: `GET /pull?since_seq={cursor}&limit=500`
- Cada pagina: aplicar al store local → actualizar cursor → pausa 100ms → siguiente pagina
- Si se interrumpe (la app se cierra), retoma desde el ultimo cursor en el proximo inicio

**Reconexion despues de estar offline mucho tiempo**:
- Push primero: drenar `sync_mutations` pendientes (cambios locales mientras estaba offline) — rapido, solo delta
- Pull despues: retomar desde el ultimo cursor, paginar los cambios remotos acumulados
- El desarrollador sigue trabajando — las lecturas/escrituras van a SQLite local, nunca se bloquean

**Invariante clave**: Los comandos MCP/serve NUNCA esperan al sync. SyncClient.Start() lanza goroutines y retorna inmediatamente.

### Endpoints del Servidor Cloud

| Metodo | Ruta | Auth | Descripcion |
|--------|------|------|-------------|
| GET | `/api/v1/health` | No | Health check + estado de PostgreSQL |
| POST | `/api/v1/sync/push` | Si | Push de mutaciones (observaciones, sesiones, prompts) |
| GET | `/api/v1/sync/pull` | Si | Pull de cambios desde cursor |
| POST | `/api/v1/observations` | Si | Crear observacion directamente |
| GET | `/api/v1/observations/{id}` | Si | Obtener observacion por ID |
| GET | `/api/v1/search` | Si | Busqueda full-text (tsvector) |
| GET | `/api/v1/context` | Si | Obtener contexto formateado |
| GET | `/api/v1/stats` | Si | Estadisticas del proyecto |
| GET | `/api/v1/projects` | Si | Listar proyectos del usuario |
| POST | `/api/v1/auth/rotate-key` | Si | Rotar API key |
| POST | `/api/v1/batch` | Si | Ejecutar multiples operaciones en un solo round trip |

Autenticacion: `Authorization: Bearer <api_key>` + `X-Engram-Protocol: 1`.

### Planificado: RemoteStore

`RemoteStore` implementara `StoreInterface` proxeando cada operacion al servidor cloud via HTTP. Sin estado — sin SQLite local, sin cache. Para equipos que quieren una unica fuente de verdad en el cloud sin almacenamiento local.

Las operaciones de escritura se rutean a traves del protocolo push (`POST /sync/push`) como pushes de una sola mutacion, retornando el ID numerico asignado por el servidor. Las operaciones de lectura llaman a endpoints dedicados.

### Planificado: Comandos CLI Cloud

```bash
engram cloud setup              # Configurar conexion cloud
engram cloud sync               # Push + pull manual
engram cloud status             # Mostrar salud del sync, mutaciones pendientes, cursor
engram cloud enroll <proyecto>  # Habilitar sync para un proyecto
engram cloud unenroll <proyecto># Deshabilitar sync para un proyecto
engram mcp --backend cloud      # Usar RemoteStore (solo cloud)
engram mcp --backend local-sync # Usar store local + SyncClient
```

---

## Estrategia de Testing

| Capa | Enfoque | Cantidad |
|------|---------|----------|
| `internal/store` | Tests unitarios con SQLite en memoria | ~30 |
| `internal/cloudstore` | Tests de integracion con PostgreSQL real (docker) | 16 |
| `internal/cloudserver` | Tests de integracion con httptest + PostgreSQL real | 17 |
| `internal/remote` | Tests unitarios con servidores mock httptest | 32 |
| `internal/tui` | teatest de Bubbletea para transiciones de teclas y renderizado | existentes |

```bash
# Tests unitarios (rapidos, sin dependencias externas)
go test ./...

# Tests de integracion (requiere PostgreSQL)
docker run -d --name engram-test-pg -p 5433:5432 \
  -e POSTGRES_USER=engram -e POSTGRES_PASSWORD=testpass \
  -e POSTGRES_DB=engram_test postgres:16-alpine

go test -tags integration -v ./internal/cloudstore/ ./internal/cloudserver/

# Cobertura
go test -cover ./internal/remote/...
```

---

## Dependencias Externas

| Dependencia | Proposito |
|-------------|-----------|
| `modernc.org/sqlite` | Driver SQLite puro en Go (sin CGO) |
| `jackc/pgx/v5` | Driver PostgreSQL (store cloud) |
| `mark3labs/mcp-go` | Implementacion del protocolo MCP (transporte stdio) |
| `charmbracelet/bubbletea` | Framework de TUI |
| `charmbracelet/bubbles` | Componentes de TUI (textinput, viewport, table) |
| `charmbracelet/lipgloss` | Estilos de TUI (tema Catppuccin Mocha) |
| `go-chi/chi/v5` | Router HTTP (servidor cloud) |
| `google/uuid` | Generacion de UUIDs |

---

## Estructura de Archivos

```
engram/
├── cmd/
│   ├── engram/              # Binario CLI principal
│   └── engram-cloud/        # Binario del servidor cloud
├── internal/
│   ├── types/               # Modelo de dominio compartido + interfaces
│   ├── store/               # Persistencia SQLite (local)
│   ├── format/              # Formateo de strings de contexto
│   ├── mcp/                 # Servidor MCP stdio (15 herramientas)
│   ├── server/              # API REST HTTP (local)
│   ├── tui/                 # UI de Terminal (Bubbletea)
│   ├── cloudserver/         # API HTTP cloud (chi)
│   ├── cloudstore/          # Backend PostgreSQL cloud
│   ├── remote/              # Cliente de sync cloud
│   ├── sync/                # Sync basado en archivos (git)
│   ├── project/             # Deteccion de nombre de proyecto
│   ├── setup/               # Instalador de plugins de agentes
│   ├── version/             # Verificador de releases en GitHub
│   └── obsidian/            # Exportacion a vault de Obsidian (beta)
├── plugin/
│   ├── claude-code/         # Config del plugin Claude Code
│   ├── opencode/            # Config del plugin OpenCode
│   └── obsidian/            # Assets de integracion con Obsidian
├── openspec/                # Artefactos de planificacion SDD
│   └── changes/
│       └── cloud-sync-phase3/
│           ├── proposal.md
│           ├── spec.md
│           ├── design.md
│           └── tasks.md
├── docs/                    # Documentacion
│   ├── ARCHITECTURE.md      # Guia de arquitectura para el usuario
│   ├── SYSTEM-ARCHITECTURE.md  # Referencia tecnica (ingles)
│   ├── ARQUITECTURA-DEL-SISTEMA.md  # Este documento (espanol)
│   ├── INSTALLATION.md
│   ├── AGENT-SETUP.md
│   ├── PLUGINS.md
│   └── COMPARISON.md
└── assets/                  # Imagenes y archivos estaticos
```
