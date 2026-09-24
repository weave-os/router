# Third-party licensing

The repository's Apache-2.0 license covers Weave's code. It does not
relicense third-party code, dependencies, model weights, or datasets, or grant
access to upstream API services. Retain the applicable upstream licenses,
copyrights, and attribution when redistributing them.

## Distributed components

- **Switchyard:** Apache-2.0, NVIDIA CORPORATION & AFFILIATES. The router embeds
  its prompt/schema and adapts its transcript condenser. The source revision,
  modification notice, and original license are in
  [internal/router/llmescalation/NOTICE](internal/router/llmescalation/NOTICE)
  and [LICENSE.switchyard](internal/router/llmescalation/LICENSE.switchyard).
- **Go dependencies and the Go runtime:** retain the upstream license and
  notice files when redistributing them. These are not all Apache-2.0: for example,
  `github.com/hashicorp/golang-lru/v2` is MPL-2.0. Its unmodified source is
  available at <https://github.com/hashicorp/golang-lru/tree/v2.0.7>;
  `go mod download github.com/hashicorp/golang-lru/v2@v2.0.7` retrieves the same
  source. The full dependency versions are recorded in [go.mod](go.mod) and
  [go.sum](go.sum). Preserve file-level copyleft obligations when modifying
  dependencies; they are not overridden by this project's license.
- **Dashboard dependencies:** retain applicable upstream license and notice
  files when distributing the bundled static dashboard.
  Versions are recorded in [frontend/package-lock.json](frontend/package-lock.json).
- **ONNX Runtime:** MIT; retain its release's `LICENSE` and
  `ThirdPartyNotices.txt` when redistributing the shared library.
- **Native tokenizers:** the `daulet/tokenizers` library is MIT and contains
  separately licensed Rust dependencies, including Hugging Face Tokenizers
  (Apache-2.0). Check the release source and its locked Cargo dependencies
  for their licenses/notices; do not infer them from the Go bindings.
- **Python dependencies:** the HMM image retains the installed distributions'
  `.dist-info` metadata, license files, and bundled notices in `/app/.venv`.
  Dependencies for benchmark tooling remain subject to their own terms too.
- **Container base images:** their operating-system and runtime components
  retain their upstream licenses and notices. The project's Apache-2.0 license
  does not apply to every component of an image.

The npm installer ships the project `LICENSE` and `NOTICE`. It does not bundle
the router server, model weights, or its separately installed peer dependencies.

## Models, artifacts, and datasets

The default embedder is
[Jina embeddings v2 base code](https://huggingface.co/jinaai/jina-embeddings-v2-base-code),
whose pinned model card declares Apache-2.0. The optional Qwen embedder is derived
from [Qwen3-Embedding-0.6B](https://huggingface.co/Qwen/Qwen3-Embedding-0.6B),
also Apache-2.0; the export adds last-token pooling and INT8 quantization.
The repositories and revisions are pinned in [Dockerfile](Dockerfile).
Overriding model repositories requires checking the replacement's terms and
retaining its own notices; public downloadability is not licensing permission.

Separately downloaded HMM archives, benchmark tasks, evaluation corpora, and
API-provider models are not relicensed by changing this repository's license.
Use the terms accompanying each specific release or dataset, including any
underlying-source restrictions. In particular, old `hmm-model-*` release
assets retain their original terms unless separately relicensed by their owner.
Preserve embedded notices in evaluation fixtures; the top-level Apache-2.0
declaration does not replace them.

When changing dependencies or distributing new artifacts, review their exact
versions, upstream notices, source-availability requirements, and any usage or
redistribution restrictions.
