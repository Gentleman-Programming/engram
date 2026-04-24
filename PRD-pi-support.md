# PRD: Soporte oficial de Engram para pi.dev

## Contexto

Pi (`pi-coding-agent`) es un coding agent con extensiones nativas en TypeScript, skills, hooks de ciclo de vida y tools personalizadas. Hoy Engram no tiene soporte oficial de primera clase para pi, lo que obliga a una integración manual y no aprovecha las capacidades nativas del ecosistema de pi.

## Problema

Los usuarios de pi no tienen una experiencia oficial, simple y comparable a OpenCode para usar Engram. La integración manual genera fricción y no habilita de forma coherente:

- protocolo de memoria nativo
- ciclo de vida de sesión
- resiliencia frente a compaction
- auto-start del backend
- una instalación con un solo comando

## Objetivo

Agregar soporte oficial para pi mediante:

```bash
engram setup pi
```

La integración debe ser nativa, global, offline/local y comparable a la experiencia actual de OpenCode.

## Objetivos del producto

La solución debe:

- instalarse con un solo comando
- funcionar de forma global
- ser offline/local, sin depender de npm o git durante el setup
- usar una extensión nativa de pi
- auto-arrancar Engram cuando sea necesario, igual que OpenCode
- exponer tools de memoria dentro de pi
- aplicar el Memory Protocol
- soportar session lifecycle
- manejar compaction recovery
- usar una política de memoria inicial asistida pero no intrusiva

## No objetivos

Por ahora no buscamos:

- integración MCP como experiencia principal
- instalación por proyecto
- inyección automática de memoria previa al abrir sesión
- reimplementar lógica de memoria en TypeScript
- introducir una UX inconsistente respecto al resto de Engram

## Experiencia esperada del usuario

### Setup

El usuario ejecuta:

```bash
engram setup pi
```

Y queda listo para usar Engram dentro de pi sin pasos manuales adicionales.

### Uso inicial

Al abrir pi:

- la integración está disponible globalmente
- Engram puede auto-iniciarse si no está corriendo
- el agente cuenta con tools de memoria
- el protocolo de memoria está disponible
- si existe memoria relevante, se notifica su disponibilidad, pero no se inyecta automáticamente en el contexto

### Durante la sesión

El agente puede:

- guardar memorias relevantes
- consultar memorias cuando el usuario lo pida
- consultar memoria de forma proactiva solo cuando haya alta probabilidad de continuidad útil

### En compaction

La integración debe preservar continuidad de trabajo sin romper la política conservadora del inicio de sesión.

## Requisitos funcionales

### RF1 — Setup oficial

Debe existir `engram setup pi`.

### RF2 — Instalación global

La integración se instala en el espacio global de pi, no por proyecto.

### RF3 — Distribución embebida

La extensión y skill necesarias deben poder instalarse desde el binario de Engram, sin depender de fetch remoto durante setup.

### RF4 — Tools de memoria

La integración debe exponer tools para buscar, guardar, contextualizar y cerrar sesión de memoria.

### RF5 — Memory Protocol

La integración debe enseñar al agente:

- cuándo guardar
- cuándo buscar
- cómo cerrar sesión
- cómo recuperarse tras compaction

### RF6 — Session lifecycle

Debe existir integración con inicio y fin de sesión.

### RF7 — Auto-start backend

Debe comportarse como OpenCode: si Engram no está corriendo, se intenta levantar automáticamente.

### RF8 — Privacidad

El contenido dentro de `<private>` no debe salir de la integración sin ser sanitizado.

### RF9 — Memoria inicial conservadora

Al inicio:

- no inyectar contexto automáticamente
- no cargar memoria completa
- sí notificar si existe memoria relevante

### RF10 — Compaction resilience

La integración debe ayudar a no perder continuidad tras compaction.

## Decisiones ya tomadas

Estas decisiones ya están acordadas:

- instalación mediante `engram setup pi`
- distribución local/offline
- alcance global
- auto-start del backend igual que OpenCode
- política de memoria inicial: notificar memoria relevante sin insertarla
- objetivo de calidad: experiencia comparable a OpenCode

## Decisiones abiertas

Estas decisiones requieren diseño técnico posterior:

1. naming de tools (`mem_*` vs `engram_*`)
2. forma exacta de la notificación inicial
3. criterio de “memoria relevante”
4. estrategia exacta de compaction recovery
5. shape final del adaptador: extensión sola vs extensión + skill + assets auxiliares

## Principios de diseño

- adaptador fino
- lógica real en Engram, no en TypeScript
- experiencia consistente con OpenCode
- sin UX mágica o intrusiva
- coherencia de producto antes que soluciones ad hoc

## Riesgos

- compaction difícil de diseñar correctamente
- sobrecargar el contexto inicial
- divergencia de naming o semántica respecto a otros agentes
- mover lógica de negocio a la extensión por conveniencia

## Criterios de éxito

Consideraremos exitosa la integración si:

- un usuario puede activar Engram en pi con `engram setup pi`
- no necesita configurar MCP manualmente
- la experiencia se siente first-class
- hay continuidad útil entre sesiones
- no se inserta memoria al inicio sin demanda
- existe señal clara cuando hay memoria relevante disponible
