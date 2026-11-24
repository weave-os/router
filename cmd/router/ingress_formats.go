package main

import (
	"weave-os/router/internal/ingress"
	"weave-os/router/internal/websearch"
)

func ingressFormatsFor(allowedModelsHeader, scopedSearchRequirement bool, searchRequirementDecayTurns int, webSearch websearch.Executor) ingress.Formats {
	formats := ingress.New(sseKeepaliveInterval())
	formats.AllowedModelsHeader = allowedModelsHeader
	formats.ScopedSearchRequirement = scopedSearchRequirement
	if searchRequirementDecayTurns > 0 {
		formats.SearchRequirementDecayTurns = searchRequirementDecayTurns
	}
	formats.WebSearch = webSearch
	return formats
}
