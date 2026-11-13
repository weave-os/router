package proxy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/translate"
)

func TestExcludeAdvisorOutrankingModels_ExcludesModelsTheAdvisorCannotAdvise(t *testing.T) {
	available := map[string]struct{}{
		"claude-fable-5-1":  {},
		"claude-fable-5":    {},
		"claude-opus-5-5":   {},
		"claude-sonnet-5":   {},
		"claude-sonnet-4-5": {},
		"gpt-5.5":           {},
	}
	env, err := translate.ParseAnthropic([]byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hi"}],"tools":[{"name":"Bash","input_schema":{"type":"object"}},{"type":"advisor_20260301","name":"advisor","model":"claude-opus-5-5-20260801"}]}`))
	require.NoError(t, err)

	out, modelsAdvisorCannotAdvise := excludeAdvisorOutrankingModels(env, map[string]struct{}{"claude-fable-5": {}}, available)
	assert.Equal(t, []string{"claude-fable-5", "claude-fable-5-1", "claude-sonnet-4-5"}, modelsAdvisorCannotAdvise,
		"already-excluded and unranked Claude models are reported so later re-admission keeps them out")
	assert.Equal(t, map[string]struct{}{"claude-fable-5": {}, "claude-fable-5-1": {}, "claude-sonnet-4-5": {}}, out)
}

func TestExcludeAdvisorOutrankingModels_NoAdvisorIsNoop(t *testing.T) {
	available := map[string]struct{}{"claude-fable-5-1": {}, "claude-opus-5-5": {}}
	env, err := translate.ParseAnthropic([]byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hi"}],"tools":[{"name":"Bash","input_schema":{"type":"object"}}]}`))
	require.NoError(t, err)

	out, added := excludeAdvisorOutrankingModels(env, nil, available)
	assert.Nil(t, added)
	assert.Nil(t, out)
}

func TestSafetyExcludedModelsIncludesAdvisorOutranking(t *testing.T) {
	s := &Service{availableModels: map[string]struct{}{"claude-fable-5-1": {}, "claude-opus-5-5": {}}}
	env, err := translate.ParseAnthropic([]byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"advisor_20260301","name":"advisor","model":"claude-opus-5-5"}]}`))
	require.NoError(t, err)

	assert.Equal(t, map[string]struct{}{"claude-fable-5-1": {}}, s.safetyExcludedModels(env, 0, nil, nil))
}

func TestAdvisorRejectsModel(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"advisor_20260301","name":"advisor","model":"claude-opus-5-5"}]}`))
	require.NoError(t, err)
	assert.True(t, advisorRejectsModel(env, "claude-fable-5-1"))
	assert.True(t, advisorRejectsModel(env, "claude-sonnet-4-5"), "unranked Claude models take no advisor")
	assert.False(t, advisorRejectsModel(env, "claude-opus-5"))
	assert.False(t, advisorRejectsModel(env, "gpt-5.5"))
}
