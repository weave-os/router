# httputil

Shared HTTP transport, stream watchdogs, upstream error helpers and the egress policy used by every provider adapter. Operator-facing tunables are documented in [CONFIGURATION.md](../../../docs/CONFIGURATION.md#upstream-transport-and-stream-watchdogs).

## Transport

`httputil.NewTransport` is a pooled transport for sustained traffic to one upstream host. A 30s TCP keepalive keeps connections alive through AWS NAT gateway, NLB and VPC-endpoint idle reapers, which drop idle connections at 350s. `ResponseHeaderTimeout` only bounds time to first byte (some upstreams withhold headers until prefill completes, so large-prompt adapters pass a wider guard to `NewTransportWithResponseHeaderTimeout`); per-read inactivity is enforced separately by `httputil.StreamBody`.

HTTP/2 connections are pinged after `DefaultH2ReadIdleTimeout` so a half-open connection an upstream load balancer reaped is detected instead of failing silently at the header timeout. PING ACKs come from the framing layer, so a slow first token is never mistaken for a dead connection. Read-idle plus ping may use at most two thirds of the caller's header guard; an over-budget pair (including env overrides) is scaled down proportionally rather than silently accepted.

`httputil.NewClient` records OpenTelemetry client spans, propagates trace context and fails the call on any redirect (`ErrRefusedRedirect`): a provider base URL is configuration, and a redirect names a host the deployment never configured. `WarmTransport` exercises DNS/TCP/TLS through the serving clients without sending a credential or inference.

## Egress policy

When upstream egress is restricted (managed deployments by default), the dialer's `Control` hook refuses any address outside the public internet after DNS resolution, so a hostname is judged on what it resolves to at dial time. Environment proxies are ignored on restricted transports because a proxied dial connects to the proxy, which would make the policy look enforced when it is not. Model discovery always uses the same destination policy, validates every resolved address and dials a validated IP directly. Besides the stdlib loopback/private/link-local predicates, `0.0.0.0/8`, `100.64.0.0/10` (CGNAT) and `240.0.0.0/4` (reserved, including broadcast) are refused.

## Stream watchdogs

Three independent watchdogs share the request context; whichever trips first sets its cause and the others become no-ops. Each surfaces a sentinel that `providers.IsRetryable` treats as upstream-owned:

| Watchdog | Trips when | Cause |
| --- | --- | --- |
| byte idle (`StartIdleWatchdog`) | no bytes for the idle budget | `ErrUpstreamIdleTimeout` |
| output stall (`StartIdleWatchdogCause`) | bytes keep arriving (keepalives, empty or role-only deltas, status events) but no output or advancing reasoning | `ErrUpstreamOutputStall` |
| throughput (`StartThroughputWatchdog`) | output flows, but below the floor for two consecutive productive windows after warmup | `ErrUpstreamSlowThroughput` |

Translators decide what counts as output progress and call the mark function. The throughput watchdog never fires on a zero-mark window (silence is the output-stall watchdog's job, and reasoning models can be output-silent for minutes); a zero-mark window resets its streak, and warmup keeps the window anchored so the first evaluation sees only post-warmup traffic. `StreamBody` returns another watchdog's cause rather than a bare cancellation so the proxy can fail over.

## Upstream errors

`ReadCapped` buffers a bounded prefix of an error body and drains at most 1 MiB more, bounding failover latency on a huge body. `LogUpstreamStatus` logs non-2xx responses at ERROR, except 429 (routine, handled by failover) at WARN, on the request's context logger so the line joins the session. When content logging is disallowed it drops the body preview and keeps only an allowlisted provider error type, because providers echo request content in messages. `WritePassthroughError` streams up to 1 KiB of the body to the client and returns `providers.UpstreamStatusError`.

Traffic capture retains both request and response spools until both bodies finish, because `RoundTrip` may return while the transport is still consuming the request.
