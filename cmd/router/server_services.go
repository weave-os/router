package main

import (
	"weave-os/router/internal/ingress"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/server"
)

func serverServices(proxySvc *proxy.Service) server.Services {
	return server.Services{
		Messages:                  proxySvc,
		Passthrough:               proxySvc,
		Router:                    proxySvc,
		RoutePreviewer:            proxySvc,
		ChatCompletions:           proxySvc,
		Responses:                 proxySvc,
		GenerateContent:           proxySvc,
		CodexImages:               proxy.NewCodexImages(proxySvc).ForwardWithCallerCredential,
		CodexModelCatalog:         proxySvc.CodexModelCatalog,
		CodexSelectionCatalog:     proxySvc.CodexSelectionCatalog,
		ModelPreparer:             proxySvc,
		ClassifierThreads:         proxySvc,
		ClassifierThreadAdmission: proxySvc,
		AllowedModelsHeader:       proxySvc,
		ParseAllowedModelsHeader:  parseAllowedModelsHeader,
		RoutingCatalog:            proxy.NewRoutingCatalog(proxySvc),
		UpstreamModels:            proxy.DispatchEngine(proxySvc),
		InferencePlans:            proxy.DispatchEngine(proxySvc),
		ErrorVocab:                ingress.ErrorVocabulary{Classify: proxy.ClassifyDispatchError, TerminalMessage: proxy.TerminalErrorMessage},
	}
}

func parseAllowedModelsHeader(raw string, installationAllowed []string) (any, error) {
	return proxy.ParseAllowedModelsHeader(raw, installationAllowed)
}
