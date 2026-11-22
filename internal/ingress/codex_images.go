package ingress

import "errors"

var (
	ErrCodexImagesRequireOwnCredential   = errors.New("image generation through the router requires signing in to Codex with ChatGPT, an OpenAI API key, or an OpenAI key saved in Weave")
	ErrCodexImagesUnavailableWithGateway = errors.New("image generation through the router is unavailable while a gateway key serves this installation")
)
