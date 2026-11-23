package ingress

import (
	"context"
	"net/http"
	"time"

	"weave-os/router/internal/translate"
	"weave-os/router/internal/websearch"
)

type Adapter interface {
	Format() translate.Format
	TraceBody(body []byte) []byte
	Parse(ctx context.Context, body []byte) (*translate.RequestEnvelope, []byte, error)
	CheckTarget(provider string) error
}

type StreamErrorWriter interface {
	WriteStreamError(w http.ResponseWriter)
}

type Formats struct {
	Gemini interface {
		Adapter
		StreamErrorWriter
	}
	Anthropic                   Anthropic
	OpenAIChat                  OpenAIChat
	Responses                   Responses
	Keepalive                   time.Duration
	AllowedModelsHeader         bool
	OpenAIResponsesBroad        bool
	EscapeNormalize             bool
	CCOrchToolsCrossVendor      bool
	CCTaskToolsCrossVendor      bool
	CCAutonomySystemAppend      bool
	CCWorkspaceSystemAppend     bool
	ScopedSearchRequirement     bool
	SearchRequirementDecayTurns int
	WebSearch                   websearch.Executor
}

const DefaultSearchRequirementDecayTurns = 3

func New(keepalive time.Duration) Formats {
	return Formats{Gemini: Gemini{}, Anthropic: Anthropic{}, OpenAIChat: OpenAIChat{}, Responses: Responses{}, Keepalive: keepalive,
		OpenAIResponsesBroad: true, CCOrchToolsCrossVendor: true, SearchRequirementDecayTurns: DefaultSearchRequirementDecayTurns}
}
