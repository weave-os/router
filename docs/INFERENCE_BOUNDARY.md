# Inference Boundary Inventory

Baseline: router `main` at `70a3532fa9a256c53fd70df372a33ca64351963d`.

This inventory freezes the behavior-neutral starting point for the centralized inference policy migration. It records every provider dispatch owner, target-selection exception, and non-inference HTTP client discovered in the source tree. The machine-readable exception list is [`internal/architecture/inference_boundary_baseline.json`](../internal/architecture/inference_boundary_baseline.json); `make inference-boundary` rejects new entries and stale generated policy docs.

## Pending Router PR Sequencing

Router PRs [#884](https://github.com/weave-os/router/pull/884) and [#792](https://github.com/weave-os/router/pull/792) are both open and unmerged at this baseline. This slice does not port either change because it must remain behavior-neutral. Before Phase 6 migrates handover, rebase the surviving PRs onto the then-current router `main` or port their exact behavior and regression tests; freeze the post-port behavior as the handover comparison baseline.

## Operation Inventory

| Operation | Purpose | Class | Current selection/dispatch owner | Migration |
| --- | --- | --- | --- | --- |
| Anthropic Messages | `anthropic_messages` | main inference | `proxy.Service.ProxyMessages` | Phase 8 |
| OpenAI Chat Completions | `openai_chat_completions` | main inference | `proxy.Service.ProxyOpenAIChatCompletion` | Phase 8 |
| OpenAI Responses | `openai_responses` | main inference | OpenAI conversion and dispatch branches in `proxy.Service` | Phase 8 |
| Gemini Generate Content | `gemini_generate_content` | main inference | `proxy.Service.ProxyGeminiGenerateContent` | Phase 8 |
| Switch handover summary | `handover_summary` | auxiliary inference | `proxy.ProviderSummarizer` | Phase 6 |
| Proactive compaction summary | `precompaction_summary` | auxiliary inference | compaction cascade plus `proxy.ProviderSummarizer` | Phase 7 |
| Post-trim compaction handover | `compaction_handover_summary` | auxiliary inference | `proxy.Service.runCompactionHandover` | Phase 7 |
| Title generation | `title_generation` | auxiliary inference | turn-type hard pin | Phase 7 |
| Classifier turn | `classifier` | auxiliary inference | turn-type hard pin | Phase 7 |
| Probe turn | `probe` | auxiliary inference | turn-type hard pin | Phase 7 |
| Sub-agent dispatch | `sub_agent_dispatch` | auxiliary inference | turn-type/operator hard pin | Phase 7 |
| Client compaction turn | `client_compaction` | client authoritative | client body plus current compaction hard-pin path | Phase 7 |
| Agent shadow evaluation | `agent_shadow_evaluation` | auxiliary inference | request-scoped explicit catalog target | Phase 8 |
| Anthropic `count_tokens` | `count_tokens` | metadata passthrough | named-provider passthrough with local estimate rescue | Phase 5 metadata method |
| Upstream model listing | `upstream_model_listing` | metadata passthrough | provider adapter list methods | remains non-selection |
| Policy decision/preview/outcome/feedback | `policy_sidecar_*` | control plane | `internal/policyclient` and legacy `internal/router/rl` HTTP clients | remains non-inference |
| Cluster/cache embeddings | `cluster_embedding`, `semantic_cache_embedding` | local support | local embedder/cache packages | remains local-only |
| Native/Cortex web search | `native_web_search` | web-search tool | explicit `websearch.Executor` | remains separate from generic inference |

The generated static registry in [`POLICY_INFERENCE.md`](POLICY_INFERENCE.md) is the authoritative review projection for policy IDs, rationale, constraints, budgets, fallback, owner, and migration status.

## Direct Provider Calls

These are the complete production `providers.Client.Proxy`/`Passthrough` call sites at the baseline. No new call site may be added outside `internal/dispatch` or a provider adapter.

| File / symbol | Operation |
| --- | --- |
| `internal/proxy/handover.go` — `ProviderSummarizer.summarize` | auxiliary handover/compaction summary |
| `internal/proxy/usage_bypass.go` — `Service.bypassToAnthropic` | subscription-token main inference |
| `internal/proxy/gemini.go` — `Service.ProxyGeminiGenerateContent` | Gemini main inference |
| `internal/proxy/service.go` — `Service.PassthroughToNamedProvider` | metadata/passthrough |
| `internal/proxy/service.go` — `Service.anthropicNativeAttempt` | Anthropic main inference |
| `internal/proxy/service.go` — two calls in `Service.ProxyMessages` | OpenAI-compatible and Gemini translations |
| `internal/proxy/service.go` — three calls in `Service.ProxyOpenAIChatCompletion` | OpenAI, Anthropic, and Gemini dispatch branches |

`proxy.Service`, `NewService`, and the two provider-filter helpers in `internal/proxy/translation_plan.go` are the only non-composition-root owners of `map[string]providers.Client`. Phase 5 moves that ownership to `internal/dispatch`.

## Approved Upstream Adapters

Real upstream HTTP is implemented only in these adapter areas:

- `internal/providers/anthropic/client.go` and `list_models.go`
- `internal/providers/openai/client.go`
- `internal/providers/google/native_client.go`
- `internal/providers/openaicompat/client.go` and `list_models.go`
- `internal/providers/cortexagents/client.go` for the explicit web-search tool executor

The composition root `cmd/router/main.go` is the only non-adapter package allowed to import concrete provider packages. The architecture fixtures prove that aliases and blank imports do not bypass this rule.

## Raw HTTP That Is Not Upstream Inference

These clients are explicitly classified so they cannot be mistaken for inference escapes:

| Area | Class |
| --- | --- |
| `internal/policyclient/client.go` | policy-sidecar health, capabilities, roster, decision, preview, outcome, and feedback control-plane I/O |
| `internal/router/rl/client.go` | legacy policy-sidecar decision I/O |
| `internal/subscriptions/oauth.go` | subscription OAuth token refresh |
| `internal/entra/client_credentials.go` | Entra client-credential minting |
| `internal/observability/otel/emitter.go` | telemetry export |

The checker flags raw `net/http` calls outside provider adapters when their compile-time URL targets a known inference route (`/v1/messages`, `/v1/chat/completions`, `/v1/responses`, or Gemini generate content). Other HTTP clients remain subject to the repository's ordinary layer rules.

## Feature-Owned Target Baseline

The reviewed exception manifest records the current utility hard pin, handover model, compaction default and large-window candidate, loop-escalation target, direct `router.Decision` constructions in proxy, and all direct provider calls. Each entry has an owner, rationale, operation class, and removal phase. These are migration debt, not precedent; baseline additions require an owned manifest diff.

## Existing Behavior Tests

The migration baseline is covered by:

- fallback and binding behavior: `internal/proxy/fallback_test.go`, `baseline_failover_integration_test.go`, `sibling_failover_integration_test.go`
- compaction and handover: `internal/proxy/compaction_test.go`, `handover_internal_test.go`, `internal/translate/compaction_test.go`, `handover_test.go`
- translation and streaming: `internal/proxy/conformance_*_test.go`, `internal/translate/stream_*_test.go`, `responses_to_anthropic_writer_stream_test.go`
- subscriptions, BYOK, and gateways: `internal/proxy/subscription_*_test.go`, `managed_subscriptions_test.go`, `gateway_*_test.go`, provider credential tests
- telemetry: `internal/proxy/planner_telemetry_internal_test.go`, `turn_signal_telemetry_internal_test.go`, `fire_telemetry_panic_internal_test.go`
- metadata passthrough: `internal/proxy/count_tokens_test.go`, `upstream_models_test.go`

## Enforcement

Run `make inference-boundary`. The check uses Go type information, so import aliases, provider-client type aliases, and named string constants are resolved rather than matched only as text. Negative fixtures cover direct and named-constant targets, aliased clients, raw inference HTTP, concrete provider imports, unregistered purposes, and baseline expansion.

The GitHub workflow publishes a dedicated `Inference boundary` status. Repository rulesets must require that status and CODEOWNERS approval from `@steventohme`; workflow configuration alone cannot enforce either repository setting.
