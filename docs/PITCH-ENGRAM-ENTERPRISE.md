# Prompt para NotebookLM — Presentacion Engram Enterprise

## Instrucciones para el generador de slides

Genera una presentacion de slides con el contenido de abajo. La audiencia es el CEO y stakeholders de una empresa de tecnologia con ~70 developers.

### Estilo obligatorio

- Corporativo, limpio, profesional. Colores neutros (blanco, gris oscuro, un acento azul o verde sobrio).
- Tipografia sans-serif (Inter, Helvetica, o similar). Titulos grandes, cuerpo legible.
- Maximo 5-6 bullets por slide. Si hay mas contenido, dividir en dos slides.
- Tablas simples con bordes finos, sin colores de fondo agresivos.
- Diagramas minimalistas con cajas y flechas, sin iconos decorativos.
- Numeros grandes y destacados cuando haya metricas (ej: "85-90%" ocupa medio slide).
- Una idea por slide. No amontonar.

### Que evitar

- Nada artistico: sin gradientes, sin imagenes de stock, sin ilustraciones, sin emojis.
- Sin animaciones ni transiciones.
- Sin jerga tecnica excesiva — el CEO no sabe que es un "advisory lock" ni un "tsvector". Traducir a impacto de negocio.
- Sin slides de "agenda" o "tabla de contenidos".
- Sin slide de "gracias" o "preguntas" al final. La ultima slide debe ser el mensaje de cierre.
- Sin logos placeholder ni mockups. Solo texto, numeros y diagramas simples.

### Estructura de la presentacion

---

**SLIDE 1 — Titulo**

Engram — Memoria persistente para IA en equipos de desarrollo

Subtitulo: Como hacer que 70 agentes de IA trabajen con las mismas convenciones, recuerden decisiones pasadas, y acumulen conocimiento compartido.

---

**SLIDE 2 — El problema**

Titulo: Cada sesion de IA empieza desde cero

- Los agentes de IA (Claude, Cursor, Copilot, Gemini) no tienen memoria entre sesiones
- Cada vez que un developer abre una sesion, tiene que re-explicar el contexto del proyecto
- El agente no sabe que stack se usa, que decisiones se tomaron, que bugs se descubrieron
- Es como contratar un consultor nuevo cada dia que no recuerda nada

---

**SLIDE 3 — El problema escala con el equipo**

Titulo: Con 1 developer es incomodo. Con 70 es catastrofico.

Tres ejemplos concretos:

- Developer A descubre un bug critico. Lo arregla. Cierra la sesion. Developer B pierde 2 horas redescubriendo el mismo bug en otro servicio.
- Developer C decide usar JWT para auth. Developer D implementa sesiones en cookies en otro servicio. Nadie se entera hasta que se integran y se rompe.
- La empresa define convenciones. Cada developer se las explica a su agente diferente. Resultado: 70 interpretaciones distintas.

Frase de cierre: La IA sin memoria compartida no escala. Escala el caos.

---

**SLIDE 4 — El costo medido**

Titulo: El costo de la amnesia — datos reales

Tabla o numeros grandes:

- Sin memoria: 60,000-170,000 tokens por sesion gastados en redescubrir contexto
- Con memoria: 8,000-16,000 tokens por sesion
- Reduccion: 85-90%

Para 70 developers:
- Sin memoria: $450-750/mes solo en tokens de contexto desperdiciados
- Con memoria: $45-75/mes
- Ahorro: $400-675/mes

Nota al pie: Esto es SOLO tokens de contexto. No incluye horas de developer recuperadas ni errores evitados.

---

**SLIDE 5 — El problema real**

Titulo: El verdadero costo no son los tokens. Es el conocimiento perdido.

- El codigo vive en git. Las decisiones — por que se eligio una tecnologia, por que se diseno asi, por que se descarto otra opcion — se pierden en Slack, en reuniones, en la cabeza de quien las tomo.
- Cuando esa persona se va, el contexto se va con ella.
- El equipo redescubre las mismas cosas, toma las mismas decisiones (o peores), y repite los mismos errores.

---

**SLIDE 6 — La solucion**

Titulo: Engram — el agente recuerda

- Sistema de memoria persistente para agentes de IA
- Lo que el agente aprende en una sesion, lo recuerda en la siguiente
- Decisiones, bugs, convenciones, descubrimientos — todo persiste
- Protocolo MCP estandar: funciona con cualquier agente (Claude, Cursor, Copilot, Gemini)
- Un solo binario, sin dependencias

---

**SLIDE 7 — Como se ve en la practica**

Titulo: Antes y despues

Antes (sin Engram):
- Sesion 1: Developer A implementa auth con JWT. Cierra sesion.
- Sesion 2: Developer B trabaja en otro servicio. Su agente no sabe nada de auth. Implementa algo diferente.

Despues (con Engram):
- Sesion 1: Developer A implementa auth con JWT. El agente guarda la decision.
- Sesion 2: Developer B trabaja en otro servicio. Su agente encuentra la decision de A. Sigue el mismo patron automaticamente.

---

**SLIDE 8 — Metricas reales de uso**

Titulo: Datos medidos — 34 dias, un developer

Numeros grandes:
- 528 observaciones guardadas
- 27 por dia en promedio
- 14 proyectos cubiertos

Cada observacion es conocimiento que no hay que redescubrir. 528 veces en un mes que el agente encontro la respuesta en memoria en vez de adivinar o preguntar.

---

**SLIDE 9 — Cloud: de memoria personal a cerebro de equipo**

Titulo: El salto a cloud

Tabla comparativa:

| Capacidad | Solo local | Con cloud |
|-----------|-----------|-----------|
| Memoria entre sesiones | Si | Si |
| Conocimiento compartido entre developers | No | Si |
| Convenciones enforced para todos los agentes | No | Si |
| Visibilidad para leads y managers | No | Si |
| Backup y recuperacion | No | Si |

Frase: Local elimina la amnesia. Cloud convierte la memoria en conocimiento de equipo.

---

**SLIDE 10 — Convenciones unificadas**

Titulo: 70 agentes, las mismas reglas

- Las convenciones se guardan UNA vez como "skills" en el servidor
- Todos los agentes las leen automaticamente al iniciar sesion
- Stack aprobado, patterns de arquitectura, politica de seguridad, git workflow
- No depende de que cada developer le explique las reglas a su agente

Diagrama simple:
Admin guarda skill → sync automatico → 70 agentes la leen → todos generan codigo consistente

---

**SLIDE 11 — Gobernanza**

Titulo: Control sin friccion

- Roles: Member, Senior, Lead, Owner — cada uno con permisos distintos sobre las skills
- Skills "locked": no se pueden cambiar sin autorizacion (ej: politica de secrets)
- Skills "overridable": admiten excepciones documentadas (ej: un proyecto legacy usa otro framework)
- Cada cambio queda en un audit log: quien, cuando, que cambio
- Los permisos se verifican en el servidor — no depende de que el agente "se porte bien"

---

**SLIDE 12 — Jerarquia organizacional**

Titulo: Conocimiento en tres niveles

Diagrama de arbol simple:

- Organizacion: convenciones globales (stack, seguridad, workflow)
  - Domain (ej: cargoflow): reglas de negocio compartidas entre proyectos
    - Proyecto (ej: cargoflow-api): memorias del equipo (bugs, decisiones)

Cuando un agente trabaja en un proyecto, automaticamente ve los tres niveles. Sin configuracion manual.

---

**SLIDE 13 — Calidad del conocimiento**

Titulo: Confianza que decae con el tiempo

- Cada observacion tiene un nivel de confianza
- Recien guardada: el agente verifica antes de usarla
- Confirmada por otro developer: el agente la usa directamente
- Vieja sin confirmar: el agente la verifica de nuevo
- La confianza decae automaticamente — lo que era cierto hace 6 meses puede no serlo hoy
- El sistema se auto-corrige: cada verificacion exitosa renueva la confianza para todos

---

**SLIDE 14 — Usos futuros**

Titulo: Que mas se puede hacer con esta data

- Onboarding acelerado: un developer nuevo tiene el contexto de meses de trabajo en segundos
- Deteccion de patrones: que areas generan mas bugs, que convenciones se ignoran
- Metricas de adopcion de IA: cuantos developers usan activamente el agente con memoria
- Verificacion en CI/CD: antes de mergear, el CI verifica contra las skills
- Base para RAG especializado: datos curados por el equipo, con confianza medible
- Knowledge graphs: visualizar como se conecta el conocimiento del equipo

---

**SLIDE 15 — Arquitectura (simplificada)**

Titulo: Como esta construido

Diagrama simple:

Developer → Agente IA → Engram (SQLite local) ←sync→ Engram Cloud (PostgreSQL)

Cuatro bullets:
- Local-first: si el servidor se cae, los developers siguen trabajando
- Agnostico: funciona con cualquier agente que soporte MCP
- Sin dependencias: un solo binario, sin instalaciones complejas
- Escalabilidad probada: 70 developers, ~480K observaciones/ano, sin degradacion

---

**SLIDE 16 — Estado y timeline**

Titulo: Donde estamos

- Completado (~13h): motor local, servidor cloud, protocolo de sync, CLI
- Pendiente (~46-50h): hardening, skills enterprise, dashboard web, integraciones
- Timeline desarrollo: ~7-8 semanas

---

**SLIDE 17 — El caso de negocio (tabla)**

Titulo: Sin Engram vs Con Engram

| Sin Engram | Con Engram |
|-----------|------------|
| Cada sesion empieza desde cero | El agente recuerda todo |
| 70 interpretaciones de las convenciones | Una fuente de verdad |
| Decisiones se pierden | Decisiones buscables para siempre |
| Bugs redescubiertos multiples veces | Un bug = documentado para todos |
| Sin control sobre la IA | RBAC, audit trail, skills locked |
| $450-750/mes en tokens desperdiciados | $45-75/mes |
| Onboarding: semanas | Onboarding: minutos |

---

**SLIDE 18 — Cierre**

Titulo grande, centrado:

"Engram no hace que la IA sea mas inteligente. Hace que recuerde. Y cuando 70 developers comparten la misma memoria, la IA deja de ser una herramienta individual y se convierte en infraestructura de conocimiento."
