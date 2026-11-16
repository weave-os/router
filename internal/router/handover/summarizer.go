package handover

import (
	"context"

	"weave-os/router/internal/router"
	"weave-os/router/internal/translate"
)

type Usage struct {
	InputTokens     int
	OutputTokens    int
	CacheCreation   int
	CacheRead       int
	CacheCreation1h int
	InferenceGeo    string
	Model           string
	Provider        string
}

type Summarizer interface {
	Summarize(ctx context.Context, env *translate.RequestEnvelope, scope router.Request) (summary string, usage Usage, err error)
	Provider() string
}

func RewriteEnvelope(env *translate.RequestEnvelope, summary string) int {
	if env == nil {
		return 0
	}
	return env.RewriteForHandover(summary)
}

func TrimLastN(env *translate.RequestEnvelope, n int) int {
	if env == nil {
		return 0
	}
	return env.TrimLastNMessages(n)
}
