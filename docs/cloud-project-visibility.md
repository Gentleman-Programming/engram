# Managed dashboard project visibility

Managed dashboard grants use normalized project identities. The dashboard resolves
an identity to its canonical project name only when exactly one project in its
neutral read model matches. Unique names containing spaces, percent characters,
or literal `%20` are therefore visible and accessible through canonical-name
detail and sync-control requests. Literal `%20` is not decoded to a space by this
resolution; project-name and URL normalization elsewhere are unchanged.

When multiple canonical names share an identity (for example, `alpha project` and
`alpha-project`), that identity authorizes neither project for listing, explicit
reads, or sync-control mutations. This also applies when one canonical name equals
the normalized identity. Other uniquely granted projects remain accessible.

Ungranted projects are hidden and forbidden on explicit access. Empty grants
produce an empty view. An explicit `*` grant exposes all projects, including names
that otherwise collide. Resolution is request-owned and does not modify the shared
neutral cache or grant enrollment rules.

Focused deterministic regression check:

```sh
go test ./internal/cloud/cloudstore -run TestDashboardStoreForProjects -count=1
```
