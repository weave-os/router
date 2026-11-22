package main

import (
	"weave-os/router/internal/ingress"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/server"
)

func withErrorVocabulary(features server.Features) server.Features {
	features.Errors = ingress.ErrorVocabulary{Classify: proxy.ClassifyDispatchError, TerminalMessage: proxy.TerminalErrorMessage}
	return features
}
