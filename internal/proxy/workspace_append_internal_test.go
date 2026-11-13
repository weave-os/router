package proxy

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"
)

func workspaceGateCtx(flagOn bool, clientApp string) context.Context {
	ctx := requestcontext.WithClientIdentity(context.Background(), requestcontext.ClientIdentity{ClientApp: clientApp})
	if flagOn {
		ctx = flags.WithOverrides(ctx, flags.Overrides{Bools: map[flags.Key]bool{flags.KeyCCWorkspaceSystemAppend: true}})
	}
	return ctx
}

func TestWorkspaceSystemAppendApplies_GateAndTurnType(t *testing.T) {
	svc := newAutonomyGateService()
	env := parseGateBody(t, autonomyGateBody)

	t.Run("flag off never fires", func(t *testing.T) {
		assert.False(t, svc.workspaceSystemAppendApplies(workspaceGateCtx(false, ClientAppClaudeCode), []byte(autonomyGateBody), env, turntype.MainLoop))
	})
	t.Run("autonomy flag alone does not turn the workspace append on", func(t *testing.T) {
		ctx := autonomyGateCtx(true, ClientAppClaudeCode)
		assert.False(t, svc.workspaceSystemAppendApplies(ctx, []byte(autonomyGateBody), env, turntype.MainLoop))
		assert.True(t, svc.autonomySystemAppendApplies(ctx, []byte(autonomyGateBody), env, turntype.MainLoop))
	})
	t.Run("deployment default on fires without an org override", func(t *testing.T) {
		on := newAutonomyGateService().WithCCWorkspaceSystemAppend(true)
		assert.True(t, on.workspaceSystemAppendApplies(workspaceGateCtx(false, ClientAppClaudeCode), []byte(autonomyGateBody), env, turntype.MainLoop))
	})
	t.Run("org override turns a default-on deployment off", func(t *testing.T) {
		on := newAutonomyGateService().WithCCWorkspaceSystemAppend(true)
		off := flags.WithOverrides(workspaceGateCtx(false, ClientAppClaudeCode), flags.Overrides{Bools: map[flags.Key]bool{flags.KeyCCWorkspaceSystemAppend: false}})
		assert.False(t, on.workspaceSystemAppendApplies(off, []byte(autonomyGateBody), env, turntype.MainLoop))
	})
	t.Run("only claude code identity qualifies", func(t *testing.T) {
		assert.True(t, svc.workspaceSystemAppendApplies(workspaceGateCtx(true, ClientAppClaudeCode), []byte(autonomyGateBody), env, turntype.MainLoop))
		assert.False(t, svc.workspaceSystemAppendApplies(workspaceGateCtx(true, ClientAppCodex), []byte(autonomyGateBody), env, turntype.MainLoop))
		assert.False(t, svc.workspaceSystemAppendApplies(workspaceGateCtx(true, ""), []byte(autonomyGateBody), env, turntype.MainLoop))
	})
	t.Run("turn types", func(t *testing.T) {
		ctx := workspaceGateCtx(true, ClientAppClaudeCode)
		want := map[turntype.TurnType]bool{
			turntype.MainLoop:         true,
			turntype.ToolResult:       true,
			turntype.SubAgentDispatch: false,
			turntype.Compaction:       false,
			turntype.Probe:            false,
			turntype.TitleGen:         false,
			turntype.Classifier:       false,
		}
		for tt, expect := range want {
			assert.Equal(t, expect, svc.workspaceSystemAppendApplies(ctx, []byte(autonomyGateBody), env, tt), "turn type %s", tt)
		}
	})
}

func TestWorkspaceSystemAppendApplies_IdempotentAndSkipsSearchSubTurn(t *testing.T) {
	svc := newAutonomyGateService()
	ctx := workspaceGateCtx(true, ClientAppClaudeCode)

	t.Run("request already carrying the instruction is left alone", func(t *testing.T) {
		body := `{"model":"claude-opus-5","system":[{"type":"text","text":"You are Claude Code."},{"type":"text","text":"` + translate.WorkspaceSystemText + `"}],"messages":[{"role":"user","content":"go"}],"max_tokens":8}`
		assert.False(t, svc.workspaceSystemAppendApplies(ctx, []byte(body), parseGateBody(t, body), turntype.MainLoop))
	})
	t.Run("request carrying only the autonomy text still gets the workspace append", func(t *testing.T) {
		body := `{"model":"claude-opus-5","system":[{"type":"text","text":"You are Claude Code."},{"type":"text","text":"` + translate.AutonomySystemText + `"}],"messages":[{"role":"user","content":"go"}],"max_tokens":8}`
		assert.True(t, svc.workspaceSystemAppendApplies(ctx, []byte(body), parseGateBody(t, body), turntype.MainLoop))
	})
	t.Run("native web-search sub-turn is skipped", func(t *testing.T) {
		body := `{"model":"claude-opus-5","system":"You are Claude Code.","tools":[{"type":"web_search_20250305","name":"web_search"}],"messages":[{"role":"user","content":"Perform a web search for the query: snowflake timestamp_tz"}],"max_tokens":8}`
		assert.False(t, svc.workspaceSystemAppendApplies(ctx, []byte(body), parseGateBody(t, body), turntype.MainLoop))
	})
}

func TestWorkspaceAppendFired_OnlyForCrossFormatServedAttempts(t *testing.T) {
	on := translate.EmitOptions{AppendWorkspaceSystem: true}
	assert.True(t, workspaceAppendFired(on, providers.ProviderOpenAI))
	assert.True(t, workspaceAppendFired(on, providers.ProviderOpenAIGateway))
	assert.True(t, workspaceAppendFired(on, providers.ProviderOpenRouter))
	assert.True(t, workspaceAppendFired(on, providers.ProviderGoogle), "PrepareGemini carries this append, unlike the autonomy one")
	assert.False(t, workspaceAppendFired(on, providers.ProviderAnthropic), "PrepareAnthropic ignores the option, telemetry must agree")
	assert.False(t, workspaceAppendFired(on, providers.ProviderAnthropicGateway))
	assert.False(t, workspaceAppendFired(translate.EmitOptions{}, providers.ProviderOpenAI))
}
