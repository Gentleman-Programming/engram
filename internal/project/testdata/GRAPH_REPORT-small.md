# Graph Report - acme-widgets (2026-09-04)

## Corpus Check
- 15 files · ~200 words
- Verdict: corpus is large enough that graph structure adds value.

## Summary
- 15 nodes · 13 edges · 4 communities (4 shown, 0 thin omitted)
- Extraction: 92% EXTRACTED · 8% INFERRED · 0% AMBIGUOUS · INFERRED: 1 edges (avg confidence: 0.75)
- Token cost: 0 input · 0 output

## Graph Freshness
- Built from commit: `deadbeef`
- Run `git rev-parse HEAD` and compare to check if the graph is stale.
- Run `graphify update .` after code changes (no API cost).

## God Nodes (most connected - your core abstractions)
1. `svc.Handler` - 6 edges
2. `db.Conn` - 4 edges
3. `worker.Job` - 3 edges
4. `worker.Queue` - 3 edges
5. `db.Query` - 1 edges
6. `db.Migration` - 1 edges
7. `db.Pool` - 1 edges
8. `svc.Router` - 1 edges
9. `svc.Middleware` - 1 edges
10. `svc.Config` - 1 edges

## Surprising Connections (you probably didn't know these)
- None detected.

## Import Cycles
- None detected.

## Communities (4 total, 0 thin omitted)

### Community 0 - "API Layer"
Cohesion: 0.50
Nodes (5): svc.Handler, svc.Router, svc.Middleware, svc.Config, svc.Logger

### Community 2 - "Community 2"
Cohesion: 0.40
Nodes (5): worker.Job, worker.Queue, worker.Retry, worker.Metrics, worker.Scheduler

### Community 3 - "Community 3"
Cohesion: 0.00
Nodes (1): README

## Knowledge Gaps
- None detected.

## Suggested Questions
_Questions this graph is uniquely positioned to answer:_

- **Why does `db.Conn` bridge Community 1 to Community 0?**
  _High betweenness centrality - this node is a cross-community bridge._
