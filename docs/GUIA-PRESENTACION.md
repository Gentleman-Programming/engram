# Guia de Presentacion — Arquitectura de Engram

Guia para explicar el documento de arquitectura a stakeholders, equipo tecnico o cualquier audiencia que necesite entender que es engram, por que existe, y como esta construido.

---

## Estructura Sugerida de la Presentacion

### 1. El Problema (2-3 min)

**Tema**: Por que necesitamos engram.

**Puntos clave**:
- Los agentes de IA (Claude Code, Copilot, Codex, etc.) pierden TODA la memoria entre sesiones
- Un agente puede resolver el mismo bug 3 veces si no recuerda que ya lo hizo
- En equipos, las decisiones de arquitectura viven en la cabeza de quien las tomo — el agente no las conoce
- Hoy los devs copian contexto manualmente (pegan texto, escriben CLAUDE.md, etc.)

**Analogia**: "Imaginate tener un senior dev nuevo cada dia que no recuerda nada de lo que se hablo ayer. Eso es un agente sin engram."

**Pregunta para la audiencia**: "Cuantas veces les paso que su agente hizo algo que ya habian decidido no hacer?"

---

### 2. Que es Engram (3-5 min)

**Tema**: La solucion — una memoria persistente que sobrevive entre sesiones.

**Puntos clave**:
- Un unico binario en Go — instalar es copiar un ejecutable
- Funciona con CUALQUIER agente que soporte MCP (protocolo estandar de Anthropic)
- Almacena: decisiones, bugs resueltos, convenciones, patrones, descubrimientos
- El agente guarda y busca memorias automaticamente via herramientas MCP (mem_save, mem_search, etc.)

**Demo rapida** (si es posible): Mostrar `engram search "auth"` o `engram context` para que vean como se siente.

**Frase de cierre**: "Engram le da un cerebro al agente. Lo que aprende hoy, lo recuerda manana."

---

### 3. Principios de Diseno (2-3 min)

**Tema**: Las decisiones fundamentales que guian toda la arquitectura.

| Principio | Que Significa | Por Que Importa |
|-----------|---------------|-----------------|
| **Local-first** | SQLite es la fuente de verdad, siempre | Si el cloud se cae, seguimos trabajando. Cero downtime para el dev. |
| **Agnostico del agente** | MCP como interfaz, no APIs propietarias | No atamos a nadie a un proveedor. Hoy Claude, manana el que sea. |
| **Cero dependencias** | Un binario Go, sin Node/Python/Docker | Instalacion en 5 segundos. Sin "npm install" que rompa algo. |
| **Fallar ruidosamente** | Errores de sync se reportan, nunca se tragan | Si una politica rechaza una edicion, el dev lo sabe. Sin sorpresas. |

**Por que estos y no otros**:
- Local-first vs Cloud-first: Probamos ambos. Cloud-first agrega latencia a CADA operacion del agente. Un mem_search que tarda 200ms en vez de 2ms mata la experiencia.
- Zero-dep vs Framework: Cada dependencia es un vector de rotura. Go compila a un binario estatico que funciona en cualquier OS sin runtime.

---

### 4. Arquitectura del Sistema (5-7 min)

**Tema**: Como estan organizados los modulos.

**Empezar con el diagrama de Vista General** (pagina 1 del doc de arquitectura).

**Explicar de arriba hacia abajo**:

1. **Capa CLI** (`cmd/engram`): Un binario, multiples interfaces
   - `engram mcp` — para agentes (stdio)
   - `engram serve` — API REST para integraciones custom
   - `engram tui` — UI de terminal para humanos
   - `engram cloud` — gestion de sync

2. **Capa de Acceso** (internal/mcp, server, tui): Tres formas de acceder a la misma data
   - Todas usan la misma interfaz `StoreInterface`
   - Cambiar de backend (local/cloud/local-sync) es transparente

3. **StoreInterface** — el contrato central:
   - "Esto es lo que hace poderosa a la arquitectura. Da igual si la data esta en SQLite local o en PostgreSQL remoto — el agente no nota la diferencia."
   - Permite 3 modos de operacion sin cambiar una linea de codigo del agente

4. **Capa de Datos**:
   - SQLite + FTS5 local (rapido, offline, busqueda full-text)
   - PostgreSQL + tsvector cloud (multi-usuario, persistente, escalable)

**Frase clave**: "La interfaz no cambia. Lo que cambia es donde vive la data."

---

### 5. Los Tres Modos de Operacion (3-5 min)

**Tema**: Como engram escala de un dev solo a un equipo de 70.

| Modo | Flujo | Caso de Uso |
|------|-------|-------------|
| **Local** (default) | Agente → SQLite | Dev solo, prototipos, evaluacion |
| **Local-Sync** | Agente → SQLite → SyncClient → PostgreSQL | Equipo que quiere memoria compartida sin perder velocidad local |
| **Cloud-Only** | Agente → HTTP → PostgreSQL | Entornos CI/CD, agentes efimeros, thin clients |

**El que mas importa para equipos es Local-Sync**:
- El dev trabaja contra SQLite (0ms latencia)
- En background, SyncClient pushea cambios cada 10s
- Cada 120s, pullea cambios del equipo
- Si pierde conexion, sigue trabajando — el sync retoma cuando vuelve
- Si el server esta caido, backoff exponencial (30s → 60s → 120s → 5min)

**Por que no solo Cloud-Only para todos?**:
- Latencia: cada mem_search agrega ~100-200ms de red
- Disponibilidad: sin internet = sin agente
- Local-Sync da lo mejor de ambos mundos

---

### 6. Sync Cloud — Como Funciona (5-7 min)

**Tema**: El protocolo de sincronizacion bidireccional.

**Empezar con la analogia**: "Funciona como git push/pull, pero automatico y en background."

**Push** (cambios locales → cloud):
1. Cada escritura local genera una mutacion en `sync_mutations`
2. SyncClient agrupa en lotes de 100
3. POST al servidor con las mutaciones
4. Servidor asigna secuencia monotonica (sin gaps, advisory lock)
5. Cliente confirma (ACK) — las mutaciones no se re-envian

**Pull** (cambios del equipo → local):
1. "Dame todo desde el ultimo punto donde me quede" (cursor)
2. Servidor devuelve paginas de 500 entidades
3. Cliente aplica al SQLite local
4. Avanza el cursor

**Conflictos**: LWW (Last-Writer-Wins)
- Si dos devs editan la misma skill, gana el ultimo en llegar al servidor
- La version anterior se guarda como revision (nunca se pierde data)
- "No es perfecto, pero es predecible. El dev puede revisar el historial y hacer rollback."

**Preguntas frecuentes**:
- "Y si dos devs pushean al mismo tiempo?" → Advisory lock por proyecto serializa las escrituras. Sin race conditions.
- "Y si el sync inicial tarda mucho?" → Corre en background. El dev trabaja normalmente mientras el sync baja las 10k observaciones del equipo.

---

### 7. Skills de Proyecto (3-5 min)

**Tema**: Conocimiento gobernado que los agentes pueden consultar.

**El problema que resuelve**:
- "Nuestra arquitectura es hexagonal" — eso esta en la cabeza del tech lead
- "Usamos table-driven tests" — eso lo sabe quien escribio la convencion
- El agente necesita acceder a estas decisiones, pero no deberian ser editables por cualquiera

**Como funciona**:
- Las skills son observaciones con `type: "skill"` — cero schema extra
- Organizacion jerarquica por topic_key: `skill/architecture`, `skill/conventions/testing`
- Roles controlan quien edita: viewer < member < senior < lead < owner
- Cada edicion crea revision + entrada en audit log
- Rollback restaura version anterior (como git revert, no git reset)

**Enforcement server-side**: Los permisos se validan en el push, no en el cliente. Local-first se respeta — el dev puede editar offline, pero el servidor acepta o rechaza.

**Frase clave**: "Las skills son la base de conocimiento del equipo. El agente las consulta con mem_search, y el equipo las gobierna con roles."

---

### 8. Dashboard (2-3 min)

**Tema**: Interfaz web para gestion visual.

**Puntos clave**:
- Embebido en el mismo binario `engram-cloud` (go:embed)
- htmx + Go templates — sin React, sin build step, sin node_modules
- Paginas: memories, skills (editar/rollback), members, audit log, stats
- Un solo puerto para API + dashboard

**Por que htmx y no React/Next.js?**:
- Consistencia con la filosofia zero-dep del proyecto
- 14kb de JS total vs 300kb+ de un bundle React
- Server-rendered — el servidor ES el estado
- Cualquier dev Go puede contribuir sin saber frontend

---

### 9. Stack Tecnico (2 min)

**Para la audiencia tecnica — rapido y concreto**:

| Que | Eleccion | Por Que |
|-----|----------|---------|
| Lenguaje | Go 1.24 | Binario estatico, sin runtime, excelente concurrencia |
| DB local | SQLite + FTS5 | Embebida, zero-config, busqueda full-text nativa |
| DB cloud | PostgreSQL + tsvector | Multi-usuario, ACID, busqueda full-text nativa |
| Protocolo agente | MCP (stdio) | Estandar abierto de Anthropic, soportado por todos los agentes principales |
| TUI | Bubbletea | Framework TUI para Go, tema Catppuccin |
| Router cloud | chi | Lightweight, composable, middleware chain |
| Driver PG | pgx/v5 | El driver PostgreSQL mas rapido para Go |

**Cero dependencias de runtime**: No necesitas Node, Python, Docker, Redis, ni ningun servicio externo para correr engram localmente.

---

### 10. Testing (1-2 min)

| Capa | Tipo | Cantidad |
|------|------|----------|
| Store local | Unit tests (SQLite en memoria) | ~30 tests |
| Client HTTP | Unit tests (httptest mock) | 32 tests |
| CLI commands | Unit tests (httptest + real SQLite) | 10 tests |
| Cloud store | Integration (PostgreSQL real) | 16 tests |
| Cloud server | Integration (httptest + PostgreSQL) | 27+ tests |

"Todo lo que toca red se testea con servidores mock. Todo lo que toca PostgreSQL se testea con PostgreSQL real en Docker."

---

## Preguntas Anticipadas y Respuestas

**"Por que no usaron embeddings/vector DB para la busqueda?"**
FTS5 (SQLite) y tsvector (PostgreSQL) cubren el caso de uso. La busqueda es por texto (titulos, contenido, tipo). Los embeddings agregan complejidad (modelo, inferencia, indice vectorial) sin beneficio medible para buscar decisiones tecnicas por keywords.

**"Que pasa si el servidor cloud se cae?"**
Nada para los devs. Siguen trabajando en local. El SyncClient detecta la caida, activa backoff exponencial, y retoma cuando el server vuelve. Cero data loss.

**"Como escala para 70 desarrolladores?"**
- Advisory lock POR PROYECTO (no global) — 70 devs en 10 proyectos = 10 locks paralelos
- Pull paginado (500/pagina) con cursor — no carga todo en memoria
- Rate limiting por usuario y endpoint
- Idempotencia en push — re-enviar es seguro

**"Podemos desplegar en AWS/GCP?"**
Si. `engram-cloud` es un binario Go + PostgreSQL. Docker, ECS, Cloud Run, VM — cualquier cosa que corra un binario y tenga PostgreSQL.

**"Cuanto cuesta mantener esto?"**
Un binario Go + un PostgreSQL. Sin Redis, sin Elasticsearch, sin message queues. El costo es basicamente el costo de la instancia de PostgreSQL.

**"Y si queremos migrar de proveedor de agente?"**
Engram usa MCP que es un protocolo abierto. Si el agente soporta MCP, funciona con engram. No hay lock-in.

---

## Flujo Recomendado de Demo (si aplica)

1. `engram mcp --tools=agent` → mostrar que las herramientas aparecen en el agente
2. Pedirle al agente que guarde una decision → `mem_save` automatico
3. Cerrar sesion, abrir nueva → `mem_search "decision"` → la memoria persiste
4. `engram cloud status` → mostrar el estado del sync
5. `engram tui` → navegar memorias visualmente

---

## Tips para la Presentacion

- **Empezar por el dolor, no por la solucion**. Si la audiencia no siente el problema, la solucion no les importa.
- **La demo vale mas que 10 diagramas**. Si podes mostrar un agente recordando algo, ganas.
- **No entrar en detalles de implementacion a menos que pregunten**. Advisory locks, cursores, debounce — eso es para la audiencia tecnica que pregunte.
- **El mensaje central es simple**: "Engram le da memoria al agente. Lo que aprende hoy, lo recuerda manana. Y si trabajas en equipo, todos los agentes comparten la misma memoria."
