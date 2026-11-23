package proxy

import "weave-os/router/internal/ingress"

func responsesBroadFormats(enabled bool) ingress.Formats {
	formats := ingress.New(0)
	formats.OpenAIResponsesBroad = enabled
	return formats
}
