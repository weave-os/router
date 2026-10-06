# Optional task-domain Qwen service

This service classifies the initial logical user task into five independent bits
(`ui,logic,data,infra,docs`). It does **not** choose a provider/model or replace
per-call HMM complexity classification. It is disabled unless a managed serving
candidate admits a `task_domain` auxiliary model and the worker has its exact
release binding. Legacy/self-hosted policy loading does not enable this feature.

## Request and selection lifecycle

The worker starts task resolution and normal complexity classification concurrently
and joins them before selecting the first model. Task resolution has a three-second
total budget (including persistence); inference reserves the final 100ms for commit.
The normal complexity error behavior is unchanged. Task transport, output, storage,
capacity and timeout failures retain baseline selection; there is no provider
fallback or HTTP retry. A late prediction is discarded.

Only the initial logical user turn is sent to Qwen. Leading system/developer
instructions, known workspace wrappers and model-control commands are omitted;
consecutive substantive user messages are joined until assistant/tool activity.
Requests without an authenticated credential or client session ID remain baseline.
Distinct initial task roots, including subagents, get distinct profiles. Recognized
Claude Code, Codex and Pi continuation summaries and histories starting with
assistant/tool content use recovery, never summary inference. Recovery requires
exactly one unexpired root in the authenticated conversation and release namespace;
zero or multiple roots retain baseline. Unmarked client history replacement cannot
be reliably distinguished from a new task and is not a supported continuity contract.

Profiles are stored in `router.task_domain_profiles`, keyed by authenticated
conversation, task-root hash, release digest and scoring-evidence digest. Managed
binding generations also namespace the conversation. No prompts or generated text
are stored in this table. Profiles are retained for 30 days, without a sliding
extension. A failed classification (timeout, rejection, transport or output error)
is retryable after five minutes: turns within those five minutes keep baseline
ranking rather than retrying an unhealthy classifier on every tool call, and the
first turn after the five minutes reclassifies while the original task is still
present. The failed row keeps its 30-day lifetime so it still counts toward resume
ambiguity. A new release/binding or expiry likewise permits a fresh classification.
Two first-inference transactions per worker run at once; a further first turn waits
up to one second for a slot, then keeps baseline for that turn without storing a
failure. Completed profiles use a read-only fast path. A row lock deduplicates
inference across replicas.

Each scored domain has a fixed benchmark mix (the weights sum to 1):

| Benchmark | ui | logic | data | infra |
|---|---:|---:|---:|---:|
| Terminal-Bench 4.0 | 0.7 | 0.7 | 0.4 | 0.6 |
| AA Long Context Reasoning | 0.2 | 0.2 | 0.1 | — |
| IFBench | 0.1 | 0.1 | — | 0.1 |
| AA-AnalystAgent | — | — | 0.3 | — |
| Terminal-Bench-Science | — | — | 0.2 | — |
| ITBench-AA | — | — | — | 0.3 |

```text
domain_delta = sum(weight * (benchmark_quality - GlobalWII)) over measured benchmarks
task_delta   = mean(domain_delta) over active ui/logic/data/infra bits
score       += alpha * 0.15 * task_delta
```

Benchmark qualities are min-max normalized to the WII 0–100 scale. A benchmark
an arm was not evaluated on contributes nothing (its weight is not
redistributed), so an arm with no relevant scores keeps its score. Docs carries
no recipe and is excluded from the mean: `logic + docs` scores as `logic`, and
docs alone keeps baseline. With full coverage the quality term becomes 85% WII
and 15% task mix; the price term is untouched.

The recipe lives only in `internal/router/hmm/selection/domain.go`; evidence
carries benchmark data, never weights. The control plane's admin preview posts
every serving lane's published policy and evidence in one batch to
`POST /internal/v1/task-domain/preview`, which runs the same selector, so the
dashboard reflects whichever recipe the worker is running.

HMM probabilities, cluster order, membership, eligibility, manual pins, harness
vendor preferences and stronger overrides remain authoritative. Traces include the content-free outcome and a
separate `task_domain_correction` alongside the uncorrected base score.

## Immutable release and staged model

Stage a merged text-only `Qwen3_5ForCausalLM` checkpoint with safetensors. No model
files are bundled or downloaded by this code. All files in the model directory
must appear in the manifest, with their exact SHA-256 digests; symlinks, extra
files, directories and remote Python code are rejected. Required files are
`model.safetensors`, `config.json`, `tokenizer.json`, `tokenizer_config.json` and
`generation_config.json`. Additional tokenizer/chat-template files must be pinned
too. Sharded checkpoints and adapters require conversion to this reviewed layout.

The release JSON has exactly these fields (placeholders are not usable digests):

```json
{
  "schema_version": "task_domain_classifier_v1",
  "projection_version": "initial_logical_user_turn_v1",
  "prompt_sha256": "SHA256_OF_SYSTEM_PROMPT_UTF8",
  "files": {
    "model.safetensors": "FILE_SHA256",
    "config.json": "FILE_SHA256",
    "tokenizer.json": "FILE_SHA256",
    "tokenizer_config.json": "FILE_SHA256",
    "generation_config.json": "FILE_SHA256"
  },
  "evidence": {"SERVING_ROSTER_SHA256": "DOMAIN_EVIDENCE_SHA256"}
}
```

`contract.py:SYSTEM_PROMPT` and Go's `taskdomain.SystemPrompt` contain the exact
training prompt. Hash its UTF-8 bytes without adding a newline. Hash the final
manifest bytes to obtain the release identity. Evidence uses the
`domain_wmi_evidence_v3` contract and must match the roster, model arms, index
versions and benchmark identities. Generate a new manifest/digest for any model,
tokenizer, prompt or evidence change; never overwrite a release.

From this directory, on a CUDA host:

```sh
uv sync --locked --extra qwen
export TASK_DOMAIN_MODEL_PATH=/artifacts/model
export TASK_DOMAIN_RELEASE_PATH=/artifacts/release.json
export TASK_DOMAIN_RELEASE_SHA256=MANIFEST_SHA256
# Inject TASK_DOMAIN_BEARER from the deployment secret manager (at least 32 characters).
uv run --locked --extra qwen python server.py
```

The service runs the checkpoint on vLLM (pinned in `uv.lock`) with continuous
batching: every admitted request joins the engine immediately and is decoded
alongside the others, and a long input is prefilled in chunks between other
requests' decode steps. Startup verifies the release, rejects a weak bearer, builds
the engine (compile and CUDA-graph capture take minutes), then classifies short and
long synthetic inputs alone and concurrently, twice. The port opens only after every
second-pass output is valid, so a TCP startup probe succeeds only after warmup;
there is no separate ongoing readiness endpoint. Any startup error exits the process
instead of serving a cold or broken model. Startup measured 1.5-4.5 minutes on an L4
(less with a persisted `VLLM_CACHE_ROOT` compile cache), so keep a warm replica and
roll new revisions before draining old ones. On SIGTERM the server drains and shuts
the engine core down, releasing the GPU for the next start.

Alternatively build the included Dockerfile from the repository root. Serve port
8095 behind authenticated-network TLS termination; the Go client accepts HTTPS
origins only, and refuses redirects. Restrict network access to router workers.
Disable request-body capture at the ingress: task text is sensitive. The service
does not emit access logs and error responses omit prompts and model output.
Up to 256 requests are admitted at once (the engine decodes 64 together and queues
the rest); beyond that the service returns 503 `classifier busy` before tokenizing.
The Go client sends its remaining budget in `X-Task-Domain-Budget-Ms` (1-10,000;
callers without it get 2.9s). The deadline covers tokenization and inference: a
request still running at its deadline is aborted inside the engine and returns 503
`classification deadline exceeded`, and a disconnected caller's work is aborted the
same way. Input is capped at 32,768 UTF-8 bytes / 8,192 templated tokens, and at
most 16 new tokens are generated greedily with thinking disabled. The image disables
FlashInfer's sampler (greedy decoding never uses it and its JIT build needs the CUDA
toolkit) and vLLM usage-stat reporting.

## Managed worker binding and admission

Apply the task-domain migration before enabling any candidate. Mount a binding
inventory and set `ROUTER_TASK_DOMAIN_BINDINGS_PATH` to its path:

```json
[
  {
    "release_file": "/artifacts/release.json",
    "release_sha256": "MANIFEST_SHA256",
    "endpoint": "https://task-classifier.example.net",
    "bearer_env": "TASK_DOMAIN_BEARER",
    "evidence_files": {"DOMAIN_EVIDENCE_SHA256": "/artifacts/evidence.json"}
  }
]
```

This inventory maps immutable releases to deployment-owned endpoints; it does not
activate them. Admission must select the same manifest digest in the candidate's
`classifier.auxiliary_models["task_domain"]` object reference. The existing core
classifier attestation must advertise that same auxiliary inventory and pass the
normal managed candidate validation. Configuring this standalone service does not
change that attestation or the selected candidate. Preserve bindings for old pinned
sessions during rollout; up to 16 releases may be loaded together.

A selected but unloaded release fails snapshot construction. Invalid local
manifests, file digests or evidence fail configuration rather than silently serving
a different artifact. A valid release without evidence for the admitted roster
returns `evidence_unavailable`, skips inference and retains baseline. Runtime
service failures are optional baseline fallbacks. To disable task weighting for
new sessions, admit a reviewed candidate without the auxiliary model; removing
an old binding while sessions still pin it is not a safe rollback.

## Verification and rollout boundary

```sh
uv run --locked --extra test pytest -q
```

These tests use synthetic predictors and model-file bytes, not a GPU checkpoint.
The Go suites exercise concurrency, transport validation, task extraction,
version isolation, live selection and optional failure behavior. Run
`go run ./scripts/task_domain_check` from the repository root with
`ROUTER_TEST_DATABASE_URL` pointing to a disposable, migrated localhost database
to verify replica deduplication, cached failures, ambiguity and expiry.

Before activation, independently validate the staged checkpoint on CUDA, tokenizer
and prompt parity, first-turn latency under load, and held-out task-label quality.
No private checkpoint publication, deployment, registry mutation or production
activation is performed by these tests or this implementation.
