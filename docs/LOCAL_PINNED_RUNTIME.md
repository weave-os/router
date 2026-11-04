# Local immutable V3 runtime composition

This opt-in composition loads one exact V3 selection and reuses the ordinary HMM selection, compatibility, pin, recovery and dispatch implementations. It changes the classifier transport to a loopback endpoint while retaining and verifying its artifact/package/image identity. It never activates a policy or consults a mutable lane head.

Set `ROUTER_DEPLOYMENT_MODE=selfhosted` and supply all of:

- `ROUTER_LOCAL_POLICY_TARGET`, `ROUTER_LOCAL_SELECTION_SET_URI`, `ROUTER_LOCAL_SELECTION_SET_SHA256`, `ROUTER_LOCAL_SELECTION_SET_GENERATION`
- `ROUTER_LOCAL_ROUTER_REVISION`, `ROUTER_LOCAL_POLICY_SHA256`
- `ROUTER_LOCAL_CLASSIFIER_ARTIFACT_ID`, `ROUTER_LOCAL_CLASSIFIER_SHA256`, `ROUTER_LOCAL_CLASSIFIER_IMAGE_DIGEST`
- `ROUTER_LOCAL_CLASSIFIER_URL`, an explicit HTTP loopback origin/port
- `ROUTER_SERVING_REGISTRY_URI`, the read-only registry root

Only the default profile is admitted by this composition. Subscriber products and live managed admission are not emulated. The generic pinned-loader preserves explicit V3 profile policies, but this entrypoint supplies the default until the corresponding product/tenant admission exists.

The server binds loopback, places the exact snapshot on every local request and requires `hmm_embedding`. A seeded installation or strategy header cannot switch to the legacy cluster or another lane. Missing strategy/snapshot fails closed. Ordinary model compatibility, manual model controls, funding and recovery still run in their existing owners. The local runtime does not fabricate a managed activation, serving assertion or production execution identity: retain source selection/policy/classifier identities and local execution provenance separately.

Provider availability, tenant controls, flags, fresh session state, subscription funding and infrastructure can differ from a managed worker. Reusing the same selection does not establish their parity. Use uniquely owned local Postgres/PubSub and synthetic/local credentials; never point this composition's session/telemetry writes at production.

A bounded live experiment requires an external pre-dispatch reservation boundary for every physical provider/auxiliary/retry request, plus classifier-internal embedding and compute bounds. Existing settled-spend caps are not that boundary. Managed egress restrictions ignore HTTP proxies; a controlled local transport/network envelope must prove interception before real credentials are supplied. Raw HTTP capture also retains private reasoning bodies; use a metadata-only observer for model/effort/usage and explicit visible phase/tool topology when full bodies are not authorized.

The startup classification check makes no provider completion, but the classifier may request an embedding. Do not launch it without an embedding allowance and enforced network coverage.

Local pinned mode does not initialize the unrelated cluster/ONNX scorer. Its
unselected-strategy router refuses requests, so a missing pinned HMM snapshot
cannot silently fall back to cluster selection. Normal mode still builds and
validates the existing cluster scorer. This avoids learned compute outside the
pinned classifier's separately enforced transport and compute allowance.

The classification readiness probe includes one authored visible user message.
The frozen raw-V5 classifier requires `conversation_messages` (or raw route
text); `PromptText` alone does not satisfy its serving input contract. The
probe validates classification only and never dispatches a model completion.
