package recordedlaws_test

import (
	"net/http"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const (
	chatTargetModel    = "deepseek/deepseek-v4-pro"
	gemini25Model      = "gemini-2.5-pro"
	gemini3Model       = "gemini-3.1-pro-preview"
	geminiSignedTarget = "gemini3"
)

type requestTargets struct {
	anthropicModel string
	responsesModel string
}

func recordedTargets(t *testing.T) requestTargets {
	t.Helper()
	anthropicModel := gjson.GetBytes(realRecordings(t, anthropicJSON)[0].body, "model").String()
	require.NotEmpty(t, anthropicModel)
	return requestTargets{anthropicModel: anthropicModel, responsesModel: richestResponsesEcho(t).Get("model").String()}
}

type prepareFunc func(*translate.RequestEnvelope, http.Header, translate.EmitOptions) (providers.PreparedRequest, error)

func emitTo(parse func([]byte) (*translate.RequestEnvelope, error), prepare prepareFunc, opts translate.EmitOptions) func([]byte) ([]byte, error) {
	return func(body []byte) ([]byte, error) {
		env, err := parse(body)
		if err != nil {
			return nil, err
		}
		prep, err := prepare(env, http.Header{}, opts)
		return prep.Body, err
	}
}

func convertResponses(body []byte) ([]byte, error) {
	conv, err := translate.ConvertResponsesToChatCompletions(body)
	return conv.Body, err
}

func mustSucceed(try func([]byte) ([]byte, error)) func(*testing.T, []byte) []byte {
	return func(t *testing.T, body []byte) []byte {
		t.Helper()
		out, err := try(body)
		require.NoError(t, err)
		return out
	}
}

type requestHop struct {
	hop
	try func([]byte) ([]byte, error)
}

func (rt requestTargets) emitters(from wireFormat, parse func([]byte) (*translate.RequestEnvelope, error)) []requestHop {
	targets := []struct {
		to      wireFormat
		via     string
		prepare prepareFunc
		opts    translate.EmitOptions
	}{
		{anthropicReq, "", (*translate.RequestEnvelope).PrepareAnthropic, translate.EmitOptions{TargetModel: rt.anthropicModel, TargetProvider: providers.ProviderAnthropic}},
		{chatReq, "", (*translate.RequestEnvelope).PrepareOpenAI, translate.EmitOptions{TargetModel: chatTargetModel, TargetProvider: providers.ProviderOpenRouter, KeepCrossVendorOrchestrationTools: true}},
		{responsesReq, "", (*translate.RequestEnvelope).PrepareOpenAIResponses, translate.EmitOptions{TargetModel: rt.responsesModel, TargetProvider: providers.ProviderOpenAI, ReasoningReplayScope: reasoningScope, KeepCrossVendorOrchestrationTools: true}},
		{geminiReq, "", (*translate.RequestEnvelope).PrepareGemini, translate.EmitOptions{TargetModel: gemini25Model, TargetProvider: providers.ProviderGoogle, KeepCrossVendorOrchestrationTools: true}},
		{geminiReq, geminiSignedTarget, (*translate.RequestEnvelope).PrepareGemini, translate.EmitOptions{TargetModel: gemini3Model, TargetProvider: providers.ProviderGoogle, KeepCrossVendorOrchestrationTools: true}},
	}
	out := make([]requestHop, 0, len(targets))
	for _, target := range targets {
		try := emitTo(parse, target.prepare, target.opts)
		out = append(out, requestHop{hop: hop{from: from, to: target.to, via: target.via, run: mustSucceed(try)}, try: try})
	}
	return out
}

func (rt requestTargets) requestHops() []requestHop {
	hops := append(rt.emitters(anthropicReq, translate.ParseAnthropic), rt.emitters(chatReq, translate.ParseOpenAI)...)
	return append(hops, requestHop{hop: hop{from: responsesReq, to: chatReq, run: mustSucceed(convertResponses)}, try: convertResponses})
}

func plainHops(hops []requestHop) []hop {
	out := make([]hop, 0, len(hops))
	for _, h := range hops {
		out = append(out, h.hop)
	}
	return out
}

func findRequestHop(hops []requestHop, from, to wireFormat, via string) requestHop {
	for _, h := range hops {
		if h.from == from && h.to == to && h.via == via {
			return h
		}
	}
	panic("no request hop " + string(from) + ">" + string(to) + "@" + via)
}
