# internal/router/sessionpin — CLAUDE

> **Mirror notice.** Source for generated [AGENTS.md](AGENTS.md). Edit this file, then run `make generate-agent-guides`; CI rejects drift.

`Pin` type + `Store` interface for sticky per-session routing. Read [root CLAUDE.md](../../../CLAUDE.md) first.

## Surface

- `Pin` value type.
- `Store` interface (inner-ring contract) — implemented by `postgres.SessionPinRepo` in [`../../postgres`](../../postgres).
- Keyed by `(api_key_id, session_key, role)` where `session_key` = 16-byte sha256 truncation derived from the inbound request (see [`../../proxy/session_key.go`](../../proxy/session_key.go)).

## Roles

Stage 1 emits `role="default"` only. The column exists so a turn-type detector can land role-conditioned pinning later without a schema change.

## TTL sweep

`runSessionPinSweep` in `cmd/router/main.go` runs the TTL sweep loop. The store interface lives here; the loop and the Postgres adapter live outside the inner ring.

## Tokenizer calibration

[TokenizerCalibration](tokenizer_calibration.go) is a thread's measured conversion between tokenizer families. Each served main-loop or tool-result turn records the full prompt its tokenizer billed; when both families billed the thread within TokenizerCalibrationMaxGap, their ratio is stored and kept until the next switch remeasures it. The proxy uses it to report a cross-tokenizer turn's input usage in the requested model's tokens, so a client's auto-compaction tracks the window it believes it has. There is deliberately no fleet-wide default: an unmeasured thread is reported in the serving provider's own tokens. Only the optional TokenizerCalibrationStore writes it; Upsert and UpdateUsage leave it untouched.

## Invariants

- **Interface in inner ring; impl in `internal/postgres`.** Proxy service is unit-tested with an in-memory fake; the Postgres adapter is exercised end-to-end via the docker-compose stack.
- **No I/O in this package.** Just types + interface.
