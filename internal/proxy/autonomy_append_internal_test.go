package proxy

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"
)

const autonomyGateBody = `{
	"model":"claude-opus-5",
	"system":[{"type":"text","text":"You are a Claude agent, built on Anthropic's Claude Agent SDK.","cache_control":{"type":"ephemeral"}}],
	"messages":[{"role":"user","content":"why is revenue too high in fct_orders?"}],
	"tools":[{"name":"Read","description":"r","input_schema":{"type":"object"}}],
	"max_tokens":256
}`

func newAutonomyGateService() *Service {
	return NewService(nil, map[string]providers.Client{}, nil, false, nil, nil,
		false, providers.ProviderAnthropic, "claude-haiku-4-5", nil)
}

func autonomyGateCtx(flagOn bool, clientApp string) context.Context {
	ctx := requestcontext.WithClientIdentity(context.Background(), requestcontext.ClientIdentity{ClientApp: clientApp})
	if flagOn {
		ctx = flags.WithOverrides(ctx, flags.Overrides{Bools: map[flags.Key]bool{flags.KeyCCAutonomySystemAppend: true}})
	}
	return ctx
}

func parseGateBody(t *testing.T, body string) *translate.RequestEnvelope {
	t.Helper()
	env, err := translate.ParseAnthropic([]byte(body))
	require.NoError(t, err)
	return env
}

func TestAutonomySystemAppendApplies_GateAndTurnType(t *testing.T) {
	svc := newAutonomyGateService()
	env := parseGateBody(t, autonomyGateBody)

	t.Run("flag off never fires", func(t *testing.T) {
		assert.False(t, svc.autonomySystemAppendApplies(autonomyGateCtx(false, ClientAppClaudeCode), []byte(autonomyGateBody), env, turntype.MainLoop))
	})
	t.Run("deployment default on fires without an org override", func(t *testing.T) {
		on := newAutonomyGateService().WithCCAutonomySystemAppend(true)
		assert.True(t, on.autonomySystemAppendApplies(autonomyGateCtx(false, ClientAppClaudeCode), []byte(autonomyGateBody), env, turntype.MainLoop))
	})
	t.Run("org override turns a default-on deployment off", func(t *testing.T) {
		on := newAutonomyGateService().WithCCAutonomySystemAppend(true)
		off := flags.WithOverrides(autonomyGateCtx(false, ClientAppClaudeCode), flags.Overrides{Bools: map[flags.Key]bool{flags.KeyCCAutonomySystemAppend: false}})
		assert.False(t, on.autonomySystemAppendApplies(off, []byte(autonomyGateBody), env, turntype.MainLoop))
	})
	t.Run("only claude code identity qualifies", func(t *testing.T) {
		assert.True(t, svc.autonomySystemAppendApplies(autonomyGateCtx(true, ClientAppClaudeCode), []byte(autonomyGateBody), env, turntype.MainLoop))
		assert.False(t, svc.autonomySystemAppendApplies(autonomyGateCtx(true, ClientAppCodex), []byte(autonomyGateBody), env, turntype.MainLoop))
		assert.False(t, svc.autonomySystemAppendApplies(autonomyGateCtx(true, ""), []byte(autonomyGateBody), env, turntype.MainLoop))
	})
	t.Run("turn types", func(t *testing.T) {
		ctx := autonomyGateCtx(true, ClientAppClaudeCode)
		want := map[turntype.TurnType]bool{
			turntype.MainLoop:         true,
			turntype.ToolResult:       true,
			turntype.SubAgentDispatch: false,
			turntype.Compaction:       false,
			turntype.Probe:            false,
			turntype.TitleGen:         false,
			turntype.Classifier:       false,
			turntype.Recap:            false,
		}
		for tt, expect := range want {
			assert.Equal(t, expect, svc.autonomySystemAppendApplies(ctx, []byte(autonomyGateBody), env, tt), "turn type %s", tt)
		}
	})
}

func TestAutonomySystemAppendApplies_IdempotentAndSkipsSearchSubTurn(t *testing.T) {
	svc := newAutonomyGateService()
	ctx := autonomyGateCtx(true, ClientAppClaudeCode)

	t.Run("request already carrying the instruction is left alone", func(t *testing.T) {
		body := `{"model":"claude-opus-5","system":[{"type":"text","text":"You are Claude Code."},{"type":"text","text":"You are operating autonomously. The user is not watching in real time."}],"messages":[{"role":"user","content":"go"}],"max_tokens":8}`
		assert.False(t, svc.autonomySystemAppendApplies(ctx, []byte(body), parseGateBody(t, body), turntype.MainLoop))
	})
	t.Run("native web-search sub-turn is skipped even though it detects as main loop", func(t *testing.T) {
		body := `{"model":"claude-opus-5","system":"You are Claude Code.","tools":[{"type":"web_search_20250305","name":"web_search"}],"messages":[{"role":"user","content":"Perform a web search for the query: dbt incremental strategies"}],"max_tokens":8}`
		env := parseGateBody(t, body)
		assert.False(t, svc.autonomySystemAppendApplies(ctx, []byte(body), env, turntype.MainLoop))
	})
	t.Run("an ordinary turn that merely declares web_search still fires", func(t *testing.T) {
		body := `{"model":"claude-opus-5","system":"You are Claude Code.","tools":[{"type":"web_search_20250305","name":"web_search"},{"name":"Read","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"why is revenue too high in fct_orders?"}],"max_tokens":8}`
		assert.True(t, svc.autonomySystemAppendApplies(ctx, []byte(body), parseGateBody(t, body), turntype.MainLoop))
	})
}

func TestAutonomyAppendFired_NotRecordedForGeminiServedAttempt(t *testing.T) {
	on := translate.EmitOptions{AppendAutonomySystem: true}
	assert.True(t, autonomyAppendFired(on, providers.ProviderAnthropic))
	assert.True(t, autonomyAppendFired(on, providers.ProviderOpenAI))
	assert.True(t, autonomyAppendFired(on, providers.ProviderOpenRouter))
	assert.False(t, autonomyAppendFired(on, providers.ProviderGoogle), "PrepareGemini drops the append, telemetry must agree")
	assert.False(t, autonomyAppendFired(translate.EmitOptions{}, providers.ProviderAnthropic))
}
