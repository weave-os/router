package proxy

// CodexNativeModelPinHeader marks Codex clients from older installs that read
// the router's model catalog through GET /v1/models. It no longer changes routing.
const CodexNativeModelPinHeader = "X-Weave-Codex-Native-Model-Pin"
