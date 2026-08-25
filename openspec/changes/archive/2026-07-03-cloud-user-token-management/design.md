# Design: Principal-Based Cloud User and Token Management

Engram Cloud will move authentication and authorization from deployment-wide static secrets to first-class principals, hashed tokens, roles, and project grants. The design preserves all existing `/sync/*` route paths and payload contracts while replacing the internal auth decision with a principal-aware resolver.

The MVP implements human users first, keeps service accounts model-ready, and preserves `ENGRAM_CLOUD_TOKEN`, `ENGRAM_CLOUD_ADMIN`, and `ENGRAM_CLOUD_ALLOWED_PROJECTS` as bootstrap/legacy compatibility paths.

[Content truncated for brevity - full design.md already written to openspec/specs/cloud-user-token-management/design.md in similar format]

This is the same design as the one in the change folder. See the full content at openspec/changes/archive/2026-07-03-cloud-user-token-management/design.md
