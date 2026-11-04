# Frozen HMM policy sidecar

This companion Python service makes the router's opt-in `hmm` strategy
self-hostable. This is a barebones, frozen version of the policy; for the best
experience, use the [Weave-managed router](../../README.md#get-started). It serves immutable PCA/HMM/XGBoost artifacts through the
router's `policy_router_v4` HTTP contract. Training, online learning,
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
[`hmm-model-v1`](https://github.com/weave-os/router/releases/tag/hmm-model-v1)
asset and verifies SHA-256 before extraction. Set `HMM_PACKAGE_PATH` instead
when running the sidecar directly with a local package.

### Serving more than one frozen package (policy pins)

`HMM_PACKAGE_URL` / `HMM_PACKAGE_PATH` (+ `HMM_PACKAGE_SHA256`) name the
**default** package. `HMM_PACKAGE_REGISTRY` optionally adds a bounded set of
further packages as a JSON list of `{"sha256": ..., "url": ...}` entries
(`path` may replace `url` for a pre-staged archive; at most 8 entries; https
only). Every entry is downloaded, digest-checked, extracted, manifest-verified
and loaded during startup — a failure in any entry fails readiness, and nothing
is ever fetched on a live request.

```bash
HMM_PACKAGE_REGISTRY='[{"sha256":"<sha256>","url":"https://.../hmm-model-v0.tar.gz"}]'
```

`/route` and `/preview` (request field `artifact_sha256`) select a loaded package by its outer digest; omitting it
serves the default. An unknown digest answers **404** rather than the default
package, so a router honouring an `x-weave-policy-pin` header fails closed.
`/readyz` lists every loaded digest under `policy_artifact_sha256s`.

## Configured raw-V5 packages

The sidecar also serves `hmm_router_classifier_package_v1` packages whose
`runtime_contract.input_mode` is `raw_v5_configured_serving_v1`: the format
Weave's managed router publishes. They use the same variables
(`HMM_PACKAGE_URL`/`HMM_PACKAGE_PATH` + `HMM_PACKAGE_SHA256`). The sidecar
verifies every `classifier/` and `hmm_bundle/` file against the manifest digest
list and reports `hmm_router_sidecar_package_<first 16 hex of the package
sha256>` as `classifier_artifact_id`, which is the id a policy release pins.
The serving code is in `hmm_sidecar/classifier_package/`.

Only Gemini packages are served. Embeddings use `google-genai`:

| Variable | Meaning |
|---|---|
| `GEMINI_API_KEY` / `GOOGLE_API_KEY` | Gemini API key; selects API-key transport when set |
| `HMM_GEMINI_TRANSPORT` | `api_key` or `vertex`; overrides the default above |
| `HMM_VERTEX_PROJECT` / `GOOGLE_CLOUD_PROJECT` | Vertex project (ADC credentials) |
| `HMM_VERTEX_LOCATION` | Vertex location |

These packages carry no embedding probe vector, so `/readyz` reports no probe
similarity for them. `tests/test_classifier_package.py` runs the production-parity check
when `HMM_RAW_V5_PARITY_PACKAGE` names a package tarball; the fixtures under
`tests/fixtures/classifier_package/` are synthetic conversations and the readouts Weave's
production sidecar produced for them with deterministic stub embeddings.

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

## Response contract: classification, not selection

`/route` speaks `policy_router_v4` and answers with classifier facts only:
`predicted_label`, `class_order`, `class_probabilities`, confidence and margin,
the HMM state id/path/probabilities, and the classifier identity
(`classifier_artifact_id`, `classifier_artifact_sha256`,
`classifier_image_digest`, `classifier_taxonomy_sha256`). It never names a
model. The router filters candidates and picks the provider/model from its
policy release, and checks the identity fields against the release before
using a classification. `/readyz` reports the same identity fields, so a router
refuses a sidecar that serves a different package or image than its release
pins. Run the sidecar and the router from matching releases. See
[docs/HMM_GO_SELECTION.md](../../docs/HMM_GO_SELECTION.md).

## Artifact safety

The runtime accepts `hmm_router_frozen_package_v1` archives and configured
raw-V5 `hmm_router_classifier_package_v1` archives. It verifies:

- the outer package digest;
- safe archive paths, member count, and size limits;
- every file digest listed by the manifest;
- classifier/HMM dimensions and class order (frozen v1 packages);
- the live embedding endpoint against the stored probe (frozen v1 packages).

The public v1 package contains data arrays and XGBoost JSON only. The legacy
training pickle, training rows, prompts, embedding cache, local filesystem
paths, and WorkWeave registry records are excluded. Raw-V5 packages include a
pickled HMM bundle, so load them only from a digest you trust; the sidecar maps
the bundle's training module paths onto `hmm_sidecar.classifier_package` and nothing else.
