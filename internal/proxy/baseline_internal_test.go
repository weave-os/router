package proxy

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBaselineFor(t *testing.T) {
	t.Run("known model returns itself", func(t *testing.T) {
		s := &Service{defaultBaselineModel: "claude-sonnet-4-5"}
		assert.Equal(t, "claude-opus-4-7", s.baselineFor(context.Background(), "claude-opus-4-7"))
	})

	t.Run("unknown model returns baseline", func(t *testing.T) {
		s := &Service{defaultBaselineModel: "claude-sonnet-4-5"}
		assert.Equal(t, "claude-sonnet-4-5", s.baselineFor(context.Background(), "weave-router"))
	})

	t.Run("empty model returns baseline", func(t *testing.T) {
		s := &Service{defaultBaselineModel: "claude-sonnet-4-5"}
		assert.Equal(t, "claude-sonnet-4-5", s.baselineFor(context.Background(), ""))
	})

	t.Run("weave-auto compares against the client's own default", func(t *testing.T) {
		s := &Service{defaultBaselineModel: "claude-sonnet-4-5"}
		claudeCode := context.WithValue(context.Background(), ClientIdentityContextKey{}, ClientIdentity{ClientApp: ClientAppClaudeCode})
		codex := context.WithValue(context.Background(), ClientIdentityContextKey{}, ClientIdentity{ClientApp: ClientAppCodex})
		cursor := context.WithValue(context.Background(), ClientIdentityContextKey{}, ClientIdentity{ClientApp: ClientAppCursor})
		assert.Equal(t, "claude-opus-5-5", s.baselineFor(claudeCode, "weave-auto"))
		assert.Equal(t, "gpt-6.1-sol", s.baselineFor(codex, "weave-auto"))
		assert.Equal(t, "claude-sonnet-4-5", s.baselineFor(cursor, "weave-auto"))
		assert.Equal(t, "claude-sonnet-5", s.baselineFor(claudeCode, "claude-sonnet-5"))
	})

	t.Run("unknown model with no baseline returns empty", func(t *testing.T) {
		s := &Service{}
		assert.Equal(t, "", s.baselineFor(context.Background(), "weave-router"))
	})
}

func TestWithDefaultBaselineModel(t *testing.T) {
	s := &Service{}
	s.WithDefaultBaselineModel("claude-sonnet-4-5")
	assert.Equal(t, "claude-sonnet-4-5", s.defaultBaselineModel)
}

// The baseline rescue path must check the allowlist directly: passthrough-only
// models never enter the desugared exclusion set, so ExcludedModels alone won't block them.
func TestBaselineModelPermittedByAllowlist(t *testing.T) {
	restricted := context.WithValue(context.Background(),
		InstallationAllowedModelsContextKey{}, []string{"claude-opus-5"})

	assert.True(t, modelPermittedByAllowlist(restricted, "claude-opus-5"),
		"an allowlisted model clears the gate")
	assert.False(t, modelPermittedByAllowlist(restricted, "claude-opus-4-8"),
		"a passthrough-only model outside the allowlist must NOT be rescued to")

	assert.True(t, modelPermittedByAllowlist(context.Background(), "claude-opus-4-8"),
		"no allowlist means no restriction, so passthrough stays servable")
}
