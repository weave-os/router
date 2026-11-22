package main

import "weave-os/router/internal/ingress"

func ingressFormatsFor(allowedModelsHeader bool) ingress.Formats {
	formats := ingress.New(sseKeepaliveInterval())
	formats.AllowedModelsHeader = allowedModelsHeader
	return formats
}
