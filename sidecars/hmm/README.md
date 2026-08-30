# Frozen HMM policy sidecar

This companion Python service makes the router's opt-in `hmm` strategy
self-hostable. It serves immutable PCA/HMM/XGBoost artifacts through the
router's existing `policy_router_v1` HTTP contract. Training, online learning,
WorkWeave registries, GCS, and managed-service credentials are intentionally
outside this package.

## Quickstart

Add a Google API key to `.env.local`, then start the optional profile:

```bash
echo 'GOOGLE_API_KEY=...' >> .env.local
make up-hmm
```

The router still defaults to `cluster`. Select HMM through an installation
strategy or an authorized `x-weave-router-strategy: hmm` override. To stop the
sidecar and return to the normal stack, run `make down-hmm && make up`.

The Compose configuration downloads the public
[`hmm-model-v1`](https://github.com/workweave/router/releases/tag/hmm-model-v1)
asset and verifies SHA-256 before extraction. Set `HMM_PACKAGE_PATH` instead
when running the sidecar directly with a local package.

## Embedding compatibility

The v1 artifact requires `google/gemini-embedding-2` with 3,072 output
dimensions. The embedding is not a replaceable preprocessing detail: it is the
majority of the classifier feature vector and defines the HMM's emission space.
The sidecar therefore verifies a reference-vector probe at startup.

An OpenAI-compatible `/embeddings` endpoint can be used by setting:

```bash
HMM_EMBEDDING_PROVIDER=openai-compatible
HMM_EMBEDDING_BASE_URL=https://embedding-proxy.example/v1
HMM_EMBEDDING_API_KEY=...
HMM_EMBEDDING_MODEL=google/gemini-embedding-2
```

It must expose the same underlying Google embedding model and pass the probe.
Ollama, vLLM, or another local embedder requires an HMM/classifier package
trained in that model's own vector space.

## Typed contract fields

In addition to the required `policy_router_v1` response fields, `/route`
responses emit optional typed fields:

- `predicted_label` — the classifier's predicted complexity label;
- `class_probabilities` — the classifier's per-class probability map;
- `pin_sticky_override_eligible` — typed successor of the legacy
  `[pin_sticky_override_eligible]` reason-string sentinel. This sidecar always
  reports `false`; when present the router treats it as authoritative and
  ignores the sentinel.

The fields are additive: routers and sidecars that predate them interoperate
unchanged via the legacy reason-string path.

## Deprecated: sidecar-side deterministic selection

This sidecar still performs the deterministic within-cluster arm selection
(harness-specific roster ordering, rank-1 eligible-arm pick, ranked
cluster-fallback walk) after classification, but the router no longer serves
that pick: it reselects the arm itself from the declarative roster file
(`ROUTER_HMM_ROSTER_PATH`, required alongside `ROUTER_HMM_SIDECAR_URL`) using
`internal/router/hmm/selection`. What the router still consumes from this
sidecar is ML inference — the classification label and probabilities via the
typed fields above — plus the ranked cluster fallback that orders the walk. The
sidecar's own arm pick is only a fail-open fallback when no ranked group holds
an eligible arm. See
[docs/HMM_GO_SELECTION.md](../../docs/HMM_GO_SELECTION.md).

## Artifact safety

The runtime accepts only `hmm_router_frozen_package_v1` archives. It verifies:

- the outer package digest;
- safe archive paths, member count, and size limits;
- every file digest listed by the manifest;
- classifier/HMM dimensions and class order;
- the live embedding endpoint against the stored probe.

The public package contains data arrays and XGBoost JSON only. The legacy
training pickle, training rows, prompts, embedding cache, local filesystem
paths, and WorkWeave registry records are excluded.
