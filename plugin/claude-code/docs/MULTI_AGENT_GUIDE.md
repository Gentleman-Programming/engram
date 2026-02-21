# Guia de Memoria Multi-Agente con Engram

Esta guia cubre el uso de Engram en proyectos de Claude Code que orquestan multiples
agentes especializados mediante el patron Task().

## Que es un Workflow Multi-Agente?

En Claude Code, podes lanzar subagentes especializados para manejar tareas especificas:

```python
# Ejemplo de orquestacion en Claude Code
Task(subagent_type="backend", prompt="Implementar el endpoint de autenticacion de usuarios")
Task(subagent_type="testing", prompt="Escribir tests para el endpoint de auth")
Task(subagent_type="security-reviewer", prompt="Auditar la implementacion de auth contra OWASP")
```

Cada subagente es una instancia de IA independiente con su propio transcript. Sin Engram, cuando
cada subagente termina, su conocimiento se pierde. Con Engram, cada aprendizaje se persiste.

## Flujo de Memoria en Workflows Multi-Agente

```
Orquestador (padre)
    |
    +-- Task(subagent_type="backend")
    |       |
    |       +-- Trabaja: implementa endpoint de auth
    |       +-- Llama mem_save("Elegimos JWT sobre sessions", type="decision")
    |       +-- Salida: "## Aprendizajes Clave:\n1. bcrypt cost=12 es..."
    |               |
    |               +-- SubagentStop se dispara -------------------------+
    |                                                                    |
    +-- Task(subagent_type="testing")                                    |
    |       |                                                            v
    |       +-- Llama mem_search("auth JWT") <-- encuentra las           |
    |       |       memorias guardadas por backend                       |
    |       +-- Salida: "## Aprendizajes Clave:\n1. Mock JWT..."         |
    |               |                                                    |
    |               +-- SubagentStop se dispara ----------------------+  |
    |                                                                 |  |
    +-- Sesion termina                                                |  |
            |                                                         v  v
            +-- Hook Stop busca mem_session_summary              Engram DB
                                                                 (todos los
                                                                  aprendizajes
                                                                  preservados por
                                                                  agent_name)
```

## Como Funciona la Captura Pasiva

El hook `subagent_stop.py` se dispara despues de cada completacion de Task(). Este:

1. Lee el transcript del subagente
2. Busca secciones `## Key Learnings:`, `## Aprendizajes Clave:`, o `### Learnings:`
3. Extrae cada item numerado o con vineta
4. Guarda cada uno como una observacion con `agent_name` en los metadatos
5. Registra logs en `~/.cache/engram/logs/subagent-stop.log`

Esto se ejecuta de forma asincrona y nunca bloquea al agente padre.

## Captura Activa vs Pasiva

Ambos mecanismos trabajan juntos como defensa en profundidad:

| Mecanismo | Como | Cuando Confiar En El |
|-----------|------|----------------------|
| **Activo** (agente llama mem_save) | El agente decide que vale la pena guardar | Decisiones de alto valor, bugfixes especificos |
| **Pasivo** (hook SubagentStop) | El hook extrae del transcript | Red de seguridad, aprendizajes de fin de tarea |

**Mejor practica:** Los agentes deberian hacer ambas cosas:
1. Llamar `mem_save` para decisiones importantes durante la tarea
2. Terminar la respuesta con la seccion `## Aprendizajes Clave:` para que el hook la capture

## Ensenar a tus Agentes a Emitir Aprendizajes

Agrega esto a los prompts de tus agentes o SKILL.md:

```markdown
## Requisito de Output

AL FINAL de tu respuesta, incluir:

## Aprendizajes Clave:

1. [Insight tecnico especifico de esta tarea]
2. [Patron o mejor practica aplicada]
3. [Conocimiento reutilizable para futuras tareas]
```

El hook SubagentStop lee esta seccion y guarda cada item en Engram automaticamente.

## Namespacing de Topic Keys para Equipos de Agentes

Cuando multiples agentes trabajan en el mismo proyecto, usa topic_keys con prefijo de agente
para prevenir colisiones:

```
backend/architecture/auth-model     <-- decisiones de auth del agente @backend
frontend/architecture/auth-model    <-- decisiones de auth del agente @frontend
security/architecture/auth-model    <-- hallazgos de auth de @security-reviewer
```

**Por que importa:** Sin namespacing, si `@backend` guarda con `topic_key="auth-model"`
y `@frontend` tambien guarda con `topic_key="auth-model"`, uno sobreescribe al otro. Con
prefijos, ambos coexisten.

**Patron:**
```
{agent_name}/{categoria}/{topico}
```

**Categorias comunes:**
- `architecture/` — decisiones de diseno
- `patterns/` — patrones descubiertos
- `bugfixes/` — causas raiz
- `conventions/` — acuerdos del equipo
- `config/` — entorno y herramientas

## Buscar Memorias Entre Agentes

Para encontrar lo que cualquier agente aprendio sobre un tema:
```
mem_search(query="autenticacion JWT")
```

Para encontrar lo que un agente especifico aprendio:
```
mem_search(query="backend autenticacion JWT")
# o usar filtro de metadatos cuando este disponible
mem_search(query="JWT", filter={"agent_name": "backend"})
```

## Resumen de Sesion en Workflows Multi-Agente

El orquestador (agente padre) deberia llamar `mem_session_summary` al final, resumiendo
el trabajo combinado de todos los subagentes:

```
mem_session_summary(
  goal="Implementar autenticacion de usuarios con JWT",
  discoveries=[
    "backend: bcrypt cost=12 es el balance correcto para nuestro servidor",
    "testing: mock de JWT requiere algoritmo HS256 especifico en fixtures",
    "security: la rotacion de refresh token debe ser atomica para prevenir race conditions"
  ],
  accomplished=[
    "Implementados /auth/login, /auth/refresh, /auth/logout",
    "Suite completa de tests con 94% de cobertura",
    "Auditoria de seguridad aprobada -- 0 issues OWASP"
  ],
  next_steps=["Agregar rate limiting a /auth/login"],
  relevant_files=["api/auth.py", "tests/test_auth.py"]
)
```

## Depuracion

**Verificar que capturo SubagentStop:**
```bash
cat ~/.cache/engram/logs/subagent-stop.log | tail -50
```

**Verificar que encontro session-stop:**
```bash
cat ~/.cache/engram/logs/session-stop.log | tail -20
```

**Buscar aprendizajes de un agente especifico:**
```bash
engram search "backend"
# o en la interfaz TUI
engram tui
```

**Verificar que el hook esta registrado:**
```bash
cat plugin/claude-code/hooks/hooks.json | jq '.hooks.SubagentStop'
```

## Anti-Patrones a Evitar

**NO uses topic_keys genericos entre agentes:**
```
# Incorrecto -- frontend sobreescribira las decisiones de backend
topic_key="auth-model"

# Correcto -- con scope por agente
topic_key="backend/architecture/auth-model"
```

**NO omitas mem_session_summary en el orquestador:**
```
# Incorrecto -- los aprendizajes de subagentes se guardan pero el contexto de sesion se pierde
Task("backend") -> Task("testing") -> "Listo!"

# Correcto
Task("backend") -> Task("testing") -> mem_session_summary(...) -> "Listo!"
```

**NO busques solo tus propias memorias:**
```
# Incorrecto -- pierde aprendizajes de otros agentes del equipo
mem_search("auth backend/auth-model")

# Correcto -- busqueda amplia entre todos los agentes
mem_search("autenticacion JWT")
```
