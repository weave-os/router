package usage_test

import (
	"testing"

	"weave-os/router/internal/usage"

	"github.com/stretchr/testify/assert"
	"github.com/tidwall/gjson"
)

func TestProviderTokenInclusion(t *testing.T) {
	tests := []struct {
		name, object                            string
		parse                                   func(gjson.Result) usage.Facts
		prompt, fresh, output, total, reasoning int
	}{
		{"anthropic", `{"input_tokens":20,"output_tokens":10,"cache_creation_input_tokens":30,"cache_read_input_tokens":50}`, usage.ParseAnthropic, 100, 20, 10, 110, 0},
		{"chat", `{"prompt_tokens":100,"completion_tokens":10,"prompt_tokens_details":{"cached_tokens":50,"cache_write_tokens":30},"completion_tokens_details":{"reasoning_tokens":3}}`, usage.ParseOpenAI, 100, 20, 10, 110, 3},
		{"responses", `{"input_tokens":100,"output_tokens":10,"input_tokens_details":{"cached_tokens":50,"cache_creation_tokens":30},"output_tokens_details":{"reasoning_tokens":3}}`, usage.ParseOpenAI, 100, 20, 10, 110, 3},
		{"gemini", `{"promptTokenCount":100,"candidatesTokenCount":7,"thoughtsTokenCount":3,"cachedContentTokenCount":80,"totalTokenCount":110}`, usage.ParseGemini, 100, 20, 10, 110, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			facts := tt.parse(gjson.Parse(tt.object))
			assert.Equal(t, tt.prompt, facts.PromptTokens())
			assert.Equal(t, tt.fresh, facts.FreshInputTokens())
			assert.Equal(t, tt.output, facts.BilledOutputTokens())
			assert.Equal(t, tt.total, facts.TotalTokens())
			assert.Equal(t, tt.reasoning, facts.Reasoning.Value)
		})
	}
}

func TestMergeCumulativeSnapshotsRetainsAbsentAndReplacesZero(t *testing.T) {
	facts := usage.ParseOpenAI(gjson.Parse(`{"prompt_tokens":100,"completion_tokens":10,"prompt_tokens_details":{"cached_tokens":50,"cache_write_tokens":30},"completion_tokens_details":{"reasoning_tokens":3}}`))
	facts.Merge(facts)
	assert.Equal(t, 110, facts.TotalTokens(), "repeated snapshots must not add tokens")
	facts.Merge(usage.ParseOpenAI(gjson.Parse(`{"completion_tokens":0,"total_tokens":0,"prompt_tokens_details":{"cached_tokens":0},"completion_tokens_details":{"reasoning_tokens":0}}`)))
	assert.Equal(t, usage.Reported(100), facts.Input)
	assert.Equal(t, usage.Reported(30), facts.CacheWrite)
	assert.Equal(t, usage.Reported(0), facts.Output)
	assert.Equal(t, usage.Reported(0), facts.CacheRead)
	assert.Equal(t, usage.Reported(0), facts.Reasoning)
	assert.Zero(t, facts.TotalTokens(), "an explicit total zero is authoritative")
}

func TestAliasPrecedenceIncludesZero(t *testing.T) {
	facts := usage.ParseOpenAI(gjson.Parse(`{"prompt_tokens":0,"input_tokens":100,"completion_tokens":0,"output_tokens":10,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":0,"cache_creation_tokens":30},"prompt_tokens_details":{"cached_tokens":50,"cache_write_tokens":30},"output_tokens_details":{"reasoning_tokens":0},"completion_tokens_details":{"reasoning_tokens":3}}`))
	for _, count := range []usage.Count{facts.Input, facts.Output, facts.CacheRead, facts.CacheWrite, facts.Reasoning} {
		assert.Equal(t, usage.Reported(0), count)
	}
}

func TestAbsentAndNonNumericCountsRemainAbsent(t *testing.T) {
	for _, object := range []string{`{}`, `{"prompt_tokens":null,"completion_tokens":"0","prompt_tokens_details":{"cached_tokens":false}}`} {
		facts := usage.ParseOpenAI(gjson.Parse(object))
		assert.True(t, facts.Present)
		assert.Equal(t, usage.Count{}, facts.Input)
		assert.Equal(t, usage.Count{}, facts.Output)
		assert.Equal(t, usage.Count{}, facts.CacheRead)
	}
	for _, parse := range []func(gjson.Result) usage.Facts{usage.ParseAnthropic, usage.ParseOpenAI, usage.ParseGemini} {
		for _, object := range []string{`null`, `[]`, `0`, `"usage"`} {
			assert.Equal(t, usage.Facts{}, parse(gjson.Parse(object)))
		}
	}
}

func TestCacheModifiersRetainReportedZeroAndAbsentFields(t *testing.T) {
	facts := usage.ParseAnthropic(gjson.Parse(`{"cache_creation_input_tokens":30,"cache_creation":{"ephemeral_5m_input_tokens":30,"ephemeral_1h_input_tokens":0},"speed":"fast","inference_geo":"us"}`))
	facts.Merge(usage.ParseAnthropic(gjson.Parse(`{"output_tokens":10}`)))
	assert.True(t, facts.CacheSplitReported())
	assert.Equal(t, usage.Reported(0), facts.CacheWrite1h)
	assert.Equal(t, usage.SpeedFast, facts.Speed)
	assert.Equal(t, usage.InferenceGeoUS, facts.InferenceGeo)
	assert.Equal(t, 30, facts.PromptTokens(), "TTL shares must not be additional cache writes")
	facts.Merge(usage.ParseAnthropic(gjson.Parse(`{"speed":"","inference_geo":""}`)))
	assert.Empty(t, facts.Speed)
	assert.Empty(t, facts.InferenceGeo)
}

func TestExclusiveReasoningIsAddedOnce(t *testing.T) {
	facts := usage.ParseOpenAI(gjson.Parse(`{"prompt_tokens":100,"completion_tokens":7,"completion_tokens_details":{"reasoning_tokens":3}}`))
	facts.OutputBasis = usage.OutputExcludesReasoning
	facts.Merge(facts)
	assert.Equal(t, 10, facts.BilledOutputTokens())
	assert.Equal(t, 110, facts.TotalTokens())
}

func TestGeminiPartialSnapshotsRetainCandidatesAndThoughts(t *testing.T) {
	facts := usage.ParseGemini(gjson.Parse(`{"candidatesTokenCount":7,"thoughtsTokenCount":3}`))
	facts.Merge(usage.ParseGemini(gjson.Parse(`{"candidatesTokenCount":10}`)))
	assert.Equal(t, 13, facts.BilledOutputTokens())
	facts.Merge(usage.ParseGemini(gjson.Parse(`{"thoughtsTokenCount":0}`)))
	assert.Equal(t, 10, facts.BilledOutputTokens())
	assert.Equal(t, usage.Reported(0), facts.Reasoning)
}

func TestNegativeCountsDoNotErasePriorUsage(t *testing.T) {
	facts := usage.ParseOpenAI(gjson.Parse(`{"prompt_tokens":100,"completion_tokens":10,"prompt_tokens_details":{"cached_tokens":50},"completion_tokens_details":{"reasoning_tokens":3}}`))
	facts.Merge(usage.ParseOpenAI(gjson.Parse(`{"prompt_tokens":-1,"completion_tokens":-1,"prompt_tokens_details":{"cached_tokens":-1},"completion_tokens_details":{"reasoning_tokens":-1}}`)))
	assert.Equal(t, 100, facts.Input.Value)
	assert.Equal(t, 10, facts.BilledOutputTokens())
	assert.Equal(t, 50, facts.CacheRead.Value)
	assert.Equal(t, 3, facts.Reasoning.Value)
}
