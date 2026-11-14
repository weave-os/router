---
name: add-context-port
description: Add a new store, external client, queue, cache or config knob as a port in the owning bounded context plus an adapter wired in `cmd/router`. Use whenever new behavior needs I/O or configuration.
---

# Add a context port

Logic stays I/O-free. I/O sits behind an interface (the port) declared by the code that uses it, implemented by an adapter, and wired only in the composition root.

## Steps

1. **Find the owning context** with `where-does-this-go`, then look for an existing port to extend before adding one:
   ```bash
   rg -n 'type \w+ interface' internal/<pkg> --type go
   ```
2. **Declare the port** in the owning inner-ring package: a small interface with only the methods the caller needs, in domain types (no SQL, HTTP or SDK types in the signature). Existing examples: `rg -n 'type (Store|Repo|Repository) interface' internal --type go`.
3. **Implement it in an adapter package** (`internal/postgres` for database access, or `internal/<adapter>/`). Adapters import the inner ring, never each other. New SQL goes in `db/queries/`, then `make generate`.
4. **Wire it in `cmd/router`.** Construct the adapter there and pass it into the consumer's constructor. Never construct concrete adapters inside `internal/`.
5. **Config knobs** go in typed config read in `cmd/router` through the `internal/config` helpers, then passed in as constructor parameters. No new raw `os.Getenv` outside `internal/config`. Document new env vars where the package's `CLAUDE.md` lists them.
6. **Test** the consumer with an in-memory fake of the port and the adapter against its real backend where the package already does so.

## Escalate

Ask the CODEOWNER (`.github/CODEOWNERS`) when the port must be shared by two contexts, or the adapter needs a new third-party dependency or infrastructure.

## Verify

```bash
go build ./... && go vet ./...
go test ./internal/architecture/...      # layering and import rules
git diff origin/main -- internal | rg '^\+.*os\.Getenv' | rg -v '^\+\+\+'   # expect none outside internal/config
make arch-ratchet                        # when the target exists
make check-changed
```
