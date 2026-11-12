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

The router defaults to `hmm_embedding` and refuses to boot unless a policy
release that pins this sidecar is configured (`ROUTER_POLICY_RELEASE_DIR`, or
`ROUTER_POLICY_ENVIRONMENT` for a registry). Stop the stack with
`make down-hmm`.

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

### Versioned classifier API for self-hosted deployments

The existing sidecar mounts `/v5/classify`, `/v5/preview`, and
`/v5/capabilities` alongside its legacy routes. Self-hosted deployments can
enable them with two local JSON files; no WorkWeave registry, GCS resolver, or
managed-service credentials are used. The release file is a
`router_classifier_release_v1` manifest whose exact stored bytes determine
`classifier_release_id`. For self-hosted deployments, the
`storage_uri` fields document artifact provenance; package bytes are read only
from the already configured local HMM package registry. The selection file is a strict object with
`schema_version: "router_classifier_selection_v1"`, that exact
`classifier_release_id`, and a sorted `enabled_variants` array; its exact bytes
determine `classifier_selection_id`.

Create the selection after writing the release file, using the release file's
exact byte hash (do not reformat either file afterward):

```json
{"schema_version":"router_classifier_selection_v1","classifier_release_id":"<sha256-of-release-file>","enabled_variants":["primary"]}
```

Then record both identities with `sha256sum classifier-release.json classifier-selection.json`.
The router sends those digests with `classifier_variant_id` and the
classification evidence to `/v5/classify` or `/v5/preview`. The classifier
returns labels, probabilities, confidence/margin, nullable HMM state facts,
and the exact release/selection/variant/artifact/taxonomy identities; it never
chooses a provider or model. `/v5/capabilities` accepts the same two digests
and reports the release taxonomy and enabled variants. Unknown or mismatched
identities fail closed. The opaque session identifier is used only for
in-memory inference continuity.

| Variable | Meaning |
|---|---|
| `HMM_CLASSIFIER_RELEASE_MANIFEST_PATH` | Path to the immutable classifier release JSON |
| `HMM_CLASSIFIER_SELECTION_PATH` | Path to the immutable local selection JSON |

Both variables are required together. Every enabled HMM package must already
be present in the boot-loaded `HMM_PACKAGE_PATH` / `HMM_PACKAGE_REGISTRY`, and
its artifact, semantic configuration, and embedding-contract hashes must
match the release manifest. The local resolver rejects digest drift or an
enabled variant without a loaded package during startup. Qwen bundles are not
supported by this self-hosted package runtime.

### Converting a pinned legacy v8 package

The public helper
`hmm_sidecar.classifier_package.legacy_conversion.convert_legacy_classifier_package(archive_bytes, expected_sha256)`
converts a verified `hmm_router_sidecar_package_v8` archive to the
classifier-only v1 format. It preserves every classifier and HMM file byte,
the raw-V5 runtime contract, and classifier/HMM training-privacy attestations;
it removes the roster and arm selector, verifies their source digests, and
returns a deterministic archive with its SHA-256 and source archive/manifest
digests. It performs no registry writes or model inference. It accepts any
raw-V5 runtime contract the package parser accepts: v1 contracts are limited
to the frozen Gemini, user-agent-only, one-turn window geometry, while v2
interaction-state contracts keep their declared document window (for example
per-message). Other shapes fail closed.

Callers must resolve the archive through its canonical `MODEL_ARTIFACT`
WeaveDataRef and pass that exact `content_sha256` as `expected_sha256`. The
private publisher then registers the returned bytes as a new artifact and
records lineage to the source model and WeaveDataRef.

The sidecar also serves `hmm_router_classifier_package_v1` packages whose
`runtime_contract.input_mode` is `raw_v5_configured_serving_v1`: the format
Weave's managed router publishes. They use the same variables
(`HMM_PACKAGE_URL`/`HMM_PACKAGE_PATH` + `HMM_PACKAGE_SHA256`). The sidecar
verifies every `classifier/` and `hmm_bundle/` file against the manifest digest
list and reports `hmm_router_sidecar_package_<first 16 hex of the package
sha256>` as `classifier_artifact_id`, which is the id a policy release pins.
The serving code is in `hmm_sidecar/classifier_package/`.

A package declares its embedding provider in
`raw_v5_runtime_contract.serving_config.provider`. Gemini packages
(`gemini`) embed with `google-genai`:

| Variable | Meaning |
|---|---|
| `GEMINI_API_KEY` / `GOOGLE_API_KEY` | Gemini API key; selects API-key transport when set |
| `HMM_GEMINI_TRANSPORT` | `api_key` or `vertex`; overrides the default above |
| `HMM_VERTEX_PROJECT` / `GOOGLE_CLOUD_PROJECT` | Vertex project (ADC credentials) |
| `HMM_VERTEX_LOCATION` | Vertex location |

At startup the sidecar embeds one probe text through the configured transport
and checks the vector has the package's declared width and is finite and
non-zero. Missing credentials, an unknown `HMM_GEMINI_TRANSPORT`, a denied
Vertex project or a wrong-width endpoint fail `/readyz` with the cause in
`error`, instead of failing every route. Gemini packages carry no reference
probe vector, so `/readyz` reports no probe similarity for them.

A package retrained on a customer's own embedding model declares
`provider: openai_compatible`, its `embedding_model` and `embedding_dim`,
`sequence_representation: openai-compatible-raw-router-v5-element-v1`, and
`reference_probes` (texts with the vectors that model returned when the package
was built). The documents and HMM/classifier geometry are the same as the Gemini
per-message profile; only the encoder differs. Configure the endpoint with:

| Variable | Meaning |
|---|---|
| `HMM_EMBEDDING_PROVIDER` | `openai-compatible` (required; Gemini packages need it unset, `google` or `gemini`) |
| `HMM_EMBEDDING_BASE_URL` | endpoint root serving `POST /embeddings`, e.g. `http://tei/v1` |
| `HMM_EMBEDDING_MODEL` | must equal the package's `embedding_model` exactly |
| `HMM_EMBEDDING_API_KEY` | optional bearer token |

Startup fails `/readyz` unless the provider and model match the package and
every reference probe re-embeds at cosine >= 0.999. Nothing falls back to
Gemini or to another package. `tests/test_classifier_package.py` runs the production-parity check
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
Ollama, vLLM, or another local embedder requires a raw-V5 package
trained in that model's own vector space (see above).

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
- the live embedding endpoint against the stored probe (frozen v1 packages);
- one live embedding of the declared width (raw-V5 packages), plus the
  declared provider/model and reference vectors (customer-model packages).

The public v1 package contains data arrays and XGBoost JSON only. The legacy
training pickle, training rows, prompts, embedding cache, local filesystem
paths, and WorkWeave registry records are excluded. Raw-V5 packages include a
pickled HMM bundle, so load them only from a digest you trust; the sidecar maps
the bundle's training module paths onto `hmm_sidecar.classifier_package` and nothing else.
