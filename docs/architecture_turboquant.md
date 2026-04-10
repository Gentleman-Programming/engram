# Arquitectura: TurboQuant + Engram Hybrid Search 🚀🧠

Esta documentación detalla la integración del motor de indexación semántica **TurboQuant** dentro del servidor de memoria **Engram**.

## 1. El Problema
La búsqueda tradicional en Engram dependía exclusivamente de **SQLite FTS5**, que es excelente para coincidencias de texto exactas pero falla estrepitosamente en:
- **Conceptual Matching**: Si buscas "memoria" y el registro dice "almacenamiento", FTS5 no lo encuentra.
- **Typo Tolerance**: Pequeños errores en el query pueden anular el resultado.
- **Semantic Re-ranking**: No hay forma de priorizar resultados que "significan" lo mismo que el query pero usan palabras distintas.

## 2. La Solución: Motor Híbrido
Hemos implementado una capa híbrida que combina el poder de los índices invertidos (FTS5) con la eficiencia de los **Locality Sensitive Hashes (LSH)**.

### Componentes Clave:

| Componente | Función | Tecnología |
| :--- | :--- | :--- |
| **FTS5 Gatekeeper** | Coincidencias exactas y rápidas. | SQLite Virtual Tables |
| **SimHash (TurboQuant)** | Genera huellas digitales de 64 bits de conceptos. | FNV-1a Hash + Bitwise Quantization |
| **TurboCache** | Cache en memoria contigua para navegación LSH ultra-rápida. | Go Slices (Cache Locality) |
| **Hamming Distance** | Calcula la cercanía semántica entre el query y los recuerdos. | Native CPU `POPCNT` |

## 3. Flujo de Búsqueda 🧬

1.  **Carga Inicial**: Al iniciar el `Store`, todos los `simhash` de la base de datos se cargan en el `TurboCache` en memoria.
2.  **Query Sanitización**: El query del usuario se normaliza (se quitan tildes, se pasa a minúsculas) y se calcula su `querySimHash`.
3.  **Ejecución FTS5**: Se buscan coincidencias exactas en la tabla virtual de SQLite.
4.  **Expansión Semántica (TurboQuant)**:
    -   Se escanea el `TurboCache` buscando las 10 observaciones con menor **Distancia de Hamming**.
    -   Cualquier resultado con distancia `< 20` se suma al set de resultados, aunque FTS5 no lo haya encontrado.
5.  **Re-Ranking**: Se aplica un *boost* a los resultados que tengan alta similitud semántica, subiéndolos en la lista de prioridades.

## 4. Persistencia y Compatibilidad 💾
- **Esquema**: Se añadió la columna `simhash` (INTEGER) a la tabla `observations`.
- **Compatibilidad**: Se utiliza `int64` en Go para representar los 64 bits de forma compatible con los enteros con signo de SQLite, evitando crasheos por desbordamiento de bit alto.
- **Migración**: El sistema incluye lógica para auto-migrar bases de datos existentes añadiendo la columna.

## 5. Rendimiento 🚀
Al usar un array contiguo (`[]CacheEntry`) en memoria, el escaneo lineal es extremadamente rápido para el procesador (L1/L2 hits), permitiendo comparar miles de firmas en fracciones de milisegundo sin dependencias externas (como bases de datos vectoriales pesadas).

---
**Nota de Arquitectura**: Este diseño prioriza la **Localidad** y la **Autonomía**. No necesitas una nube o un modelo de 10GB para tener búsqueda semántica; solo necesitas matemáticas y una CPU eficiente.
