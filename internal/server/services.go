package server

import (
	"context"
	"net/http"

	"weave-os/router/internal/api/admin"
	anthropicapi "weave-os/router/internal/api/anthropic"
	classifierapi "weave-os/router/internal/api/classifier"
	geminiapi "weave-os/router/internal/api/gemini"
	openaiapi "weave-os/router/internal/api/openai"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/ingress"
	"weave-os/router/internal/server/middleware"
)

type Services struct {
	Messages                  anthropicapi.MessagesProxy
	Passthrough               anthropicapi.Passthrough
	Router                    anthropicapi.AnthropicRouter
	RoutePreviewer            anthropicapi.RoutePreviewer
	ChatCompletions           openaiapi.ChatCompletions
	Responses                 openaiapi.Responses
	GenerateContent           geminiapi.GenerateContent
	CodexImages               openaiapi.CodexImageForwarder
	CodexModelCatalog         func(context.Context, http.Header, string) ([]byte, error)
	CodexSelectionCatalog     func(context.Context, http.Header, string) ([]byte, error)
	ModelPreparer             classifierapi.RouterModelPreparer
	ClassifierThreads         classifierapi.ClassifierThreads
	ClassifierThreadAdmission middleware.ClassifierThreadAdmission
	AllowedModelsHeader       middleware.AllowedModelsHeaderGate
	ParseAllowedModelsHeader  middleware.AllowedModelsHeaderParser
	RoutingCatalog            admin.RoutingCatalog
	UpstreamModels            dispatch.UpstreamModelLister
	InferencePlans            dispatch.InferencePlanInspector
	ErrorVocab                ingress.ErrorVocabulary
}
