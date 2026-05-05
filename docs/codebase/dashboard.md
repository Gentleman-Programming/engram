[← Codebase Guide](../CODEBASE-GUIDE.md) | [← Previous: Sync and Cloud](sync-and-cloud.md) | [Next: Integrations →](integrations.md)

# Cloud Dashboard

**The dashboard is a server-rendered browser UI over cloud state; it must never simulate policy that is not enforced server-side.** UI belongs in `internal/cloud/dashboard`, while durable state and policy enforcement belong in `cloudstore`/`cloudserver`.

## Architecture

The dashboard lives in `internal/cloud/dashboard` and is mounted from `internal/cloud/cloudserver`. It is server-rendered with templ and uses HTMX for partials.

```text
Browser
  GET /dashboard/*
      │
      ▼
internal/cloud/cloudserver
  auth/session/admin boundary
      │
      ▼
internal/cloud/dashboard
  handlers + templ components + static assets
      │
      ▼
DashboardStore interface
      │
      ▼
internal/cloud/cloudstore
  Postgres read model + controls + audit log
```

## Key files

| File | Responsibility |
|---|---|
| `internal/cloud/dashboard/dashboard.go` | Route mount, handlers, `DashboardStore` interface. |
| `internal/cloud/dashboard/*_templ.go` | Generated and checked templ components. |
| `internal/cloud/dashboard/static/styles.css` | Dashboard styles. |
| `internal/cloud/dashboard/middleware.go` | Session/route protection. |
| `internal/cloud/dashboard/principal.go` | Operator visual identity. |
| `internal/cloud/cloudserver/cloudserver.go` | Authentication boundary, dashboard mount, and sync transport. |
| `internal/cloud/cloudstore/dashboard_queries.go` | Dashboard queries/read model. |
| `internal/cloud/cloudstore/project_controls.go` | Per-project controls. |
| `internal/cloud/cloudstore/audit_log.go` | Relevant-event audit. |

## Central dashboard invariant

The UI cannot lie. If it shows “sync paused”, every push path must be blocked server-side, including `POST /sync/push` and `POST /sync/mutations/push`. If it shows administration controls, those controls must map to enforceable and testable behavior.

## Dashboard change checklist

- [ ] The UI represents real state, not a fake control.
- [ ] Admin policy is enforced in `cloudserver`/`cloudstore`, not only in templ/HTMX.
- [ ] Handlers stay in `internal/cloud/dashboard`.
- [ ] Queries/read model stay in `internal/cloud/cloudstore`.
- [ ] Generated templ components are kept with the change when applicable.
- [ ] Tests cover routes, authentication/session/administration, HTMX partials, and edge cases.

---

[← Previous: Sync and Cloud](sync-and-cloud.md) | [Next: Integrations →](integrations.md)
