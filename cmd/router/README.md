# cmd/router

The composition root: the only package that constructs concrete adapters
(`internal/postgres`, provider clients, Pub/Sub, OpenTelemetry) and wires them
into `auth.Service` and `proxy.Service`. Operator-facing variables are listed in
[docs/CONFIGURATION.md](../../docs/CONFIGURATION.md).

## Wiring invariants

- **Telemetry before clients.** `apm.Init` runs before any HTTP client is built so
  OpenTelemetry transports capture the configured providers and propagator.
- **Schema preference.** Every pooled connection sets `search_path` to
  `router, public` in `AfterConnect`, so unqualified names resolve to `router`
  first even when `DATABASE_URL` omits `?search_path=router`. It is not an
  isolation boundary: names missing from `router` and qualified `public.*`
  references still reach `public`.
- **Small pool.** The pool is capped at 6 connections; the API-key cache and the
  in-process LRUs absorb most reads. Sustained pool wait above ~1 ms with session
  pinning on is the signal to move pin traffic off Postgres, not to grow the pool.
- **Startup ping.** pgxpool connects lazily, so boot pings once; a failure keeps
  `/startupz` unsuccessful until the database recovers instead of making the first
  request build the pool inside its own budget.
- **Readiness.** `/readyz` requires Postgres, the HMM policy snapshot when one is
  wired, and a routable deployment default strategy, so a revision whose default
  would 503 every request never passes its startup probe.
- **Managed serving.** The deployment default of a managed worker must be one of
  `managedServingStrategies`; workers admitted to `policyregistry.TargetInternal`
  additionally register the beta and Qwen HMM strategies. Any serving variable
  requires an explicit fleet target, and a stamped managed revision refuses to
  boot without fleet admission.
- **Encryption.** An absent `EXTERNAL_KEY_ENCRYPTION_KEY` selects the no-op
  encryptor (self-hosted/local only); a malformed keyset fails boot.
- **Billing.** Managed mode wires billing by default. A boot-time billing health
  check error keeps billing enabled rather than dropping to BYOK-only, which would
  400 every request. Managed without billing is BYOK-only so a wiring failure never
  spends platform keys; self-hosted is never BYOK-only.
- **Credential surfaces.** `deploymentEligible` holds only providers with a real
  deployment key. Client-auth passthrough providers (Anthropic `x-api-key`/OAuth,
  OpenAI plan auth) are kept in a separate set admitted only when the request
  arrives on that provider's own surface; merging the sets would forward one
  vendor's credential to another vendor.
- **Provider families.** Every registered provider must have a
  `ProviderFamilies` entry; boot panics otherwise, instead of 502ing every request
  for a provider that looks enabled.
- **Hard pins.** The boot-time hard pin seeds title, classifier, probe and
  sub-agent targets. The per-request hard-pin resolver applies `excluded_models`
  (the hard-pin tier bypasses the scorer) and is not wired when
  `ROUTER_HARD_PIN_MODEL` is set: an operator override is never rewritten.
- **Nil interfaces.** Optional collaborators (telemetry emitter, handover
  summarizer, routers) are declared with their interface type and assigned only
  when configured, so callers' `== nil` checks see a true nil rather than a typed
  nil pointer.
- **Background work.** Long-running goroutines start through `safeGo`, which
  recovers and logs a panic instead of crashing the process. Pin and turn-clock
  sweeps run hourly on a background context so an idempotent sweep keeps draining
  during shutdown.
- **HTTP server.** No server-wide `ReadTimeout`/`WriteTimeout` (they would cut
  streams); non-streaming routes carry per-route timeouts.
- **Attempt telemetry.** Inference attempt rows go through a bounded
  per-installation fair queue (`DefaultAttemptQueueCapacity` total across all
  installations, drained round-robin by 4 workers with at most one in-flight
  write per installation) so a slow insert cannot stall failover and one
  installation's burst cannot evict another's rows. Buffered contexts retain
  only trace and client-session correlation (not request bodies), and drops log
  at power-of-two drop counts. The worker count stays below the 6-connection
  Postgres pool so request-path queries keep capacity.
- **Shutdown budget.** Cloud Run allows 10 s between SIGTERM and SIGKILL: server
  shutdown 6 s, then the OTel emitter flush (1.5 s) runs concurrently with the
  attempt-sink drain (1.5 s) followed by APM shutdown (1.5 s), preserving queued
  attempt spans within ~1 s of slack. A `ListenAndServe` failure flushes the
  attempt sink and APM itself because it bypasses that path.
- **Flag registry.** Boot publishes the compiled-in flag registry with the
  defaults this process resolved, best effort; nothing on the request path reads
  it.
- **Entra token source.** The client that posts a decrypted BYOK
  `client_secret` never follows redirects.
