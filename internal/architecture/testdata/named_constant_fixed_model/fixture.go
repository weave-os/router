package namedconstantfixedmodel

import (
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
)

const fixedModel = "claude-haiku-4-5"

func decision() router.Decision {
	return router.Decision{Provider: providers.ProviderAnthropic, Model: fixedModel}
}
