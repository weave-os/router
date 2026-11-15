# db — AGENTS

> **Mirror notice.** Generated from [CLAUDE.md](CLAUDE.md). Edit CLAUDE.md, then run `make generate-agent-guides`; CI rejects drift.

Migrations + SQLC query sources. Read [root CLAUDE.md](../CLAUDE.md) first. Adapter that consumes these lives in [`../internal/postgres`](../internal/postgres) — see [its CLAUDE.md](../internal/postgres/CLAUDE.md).

## Layout

- `migrations/` — `NNNN_<name>.up.sql` + `.down.sql` pairs, sequential numbering.
- `queries/<table>.sql` — SQLC query sources.

## Migration conventions

- Never create migration files manually — use `make migrate-create NAME=<name>`.
- **Down migrations must be precise rollbacks.** Don't separately drop indexes when dropping tables. `TestMigrationsAreTransactional` and `TestDownMigrationsArePreciseRollbacks` ([db_conventions_test.go](../internal/architecture/db_conventions_test.go)) enforce `BEGIN; ... COMMIT;` and no `IF EXISTS`.
- `organization_id` + `created_by` are opaque external identifiers — **never add foreign keys to tables outside the router's own schema.** Such tables don't exist in this project.
- Soft-delete via `deleted_at TIMESTAMP` on tables that need lifecycle. Hot-path queries filter `WHERE deleted_at IS NULL`.

## Query conventions

- **Named params** only (`TestQueriesUseNamedParameters`).
- **Always include type casts** so SQLC inference is unambiguous.
- Query names start with `Insert`/`Upsert`/`Get`/`Update`/`Delete` or a domain verb from `queryNameVerbs` (`TestQueriesUsePrefixedNames`).
- Every query gets an explanatory comment (SQLC turns it into godoc on the generated function).
- No-rows single-row queries return an error — caller checks `errors.Is(err, sql.ErrNoRows)`.

## Regeneration

After touching anything in `queries/`, run `make generate` and commit the regenerated `../internal/sqlc/` alongside the query change. CI fails if generated code drifts from sources.
