package translate

import (
	"math"

	"weave-os/router/internal/usage"

	"github.com/tidwall/gjson"
)

func writeChatUsage(jw *jsonWriter, facts usage.Facts) {
	writeOpenAIUsage(jw, facts, "prompt_tokens", "completion_tokens", "prompt_tokens_details", "completion_tokens_details")
}

func writeResponsesUsage(jw *jsonWriter, facts usage.Facts) {
	writeOpenAIUsage(jw, facts, "input_tokens", "output_tokens", "input_tokens_details", "output_tokens_details")
}

func writeOpenAIUsage(jw *jsonWriter, facts usage.Facts, inputKey, outputKey, inputDetailsKey, outputDetailsKey string) {
	jw.Obj()
	jw.Key(inputKey)
	jw.Int(int64(facts.PromptTokens()))
	jw.Key(outputKey)
	jw.Int(int64(facts.BilledOutputTokens()))
	jw.Key("total_tokens")
	jw.Int(int64(facts.TotalTokens()))
	if facts.CacheRead.Reported || facts.CacheWrite.Reported {
		jw.Key(inputDetailsKey)
		jw.Obj()
		if facts.CacheRead.Reported {
			jw.Key("cached_tokens")
			jw.Int(int64(facts.CacheRead.Value))
		}
		if facts.CacheWrite.Reported {
			jw.Key("cache_write_tokens")
			jw.Int(int64(facts.CacheWrite.Value))
		}
		jw.EndObj()
	}
	if facts.Reasoning.Reported {
		jw.Key(outputDetailsKey)
		jw.Obj()
		jw.Key("reasoning_tokens")
		jw.Int(int64(facts.Reasoning.Value))
		jw.EndObj()
	}
	jw.EndObj()
}

func writeAnthropicUsageScaled(jw *jsonWriter, facts usage.Facts, inputScale float64) {
	scale := func(n int) int64 {
		if inputScale == 1 {
			return int64(n)
		}
		return int64(math.Round(float64(n) * inputScale))
	}
	jw.Obj()
	jw.Key("input_tokens")
	jw.Int(scale(facts.FreshInputTokens()))
	jw.Key("output_tokens")
	jw.Int(int64(facts.BilledOutputTokens()))
	if facts.CacheRead.Reported {
		jw.Key("cache_read_input_tokens")
		jw.Int(scale(facts.CacheRead.Value))
	}
	if facts.CacheWrite.Reported {
		jw.Key("cache_creation_input_tokens")
		jw.Int(scale(facts.CacheWrite.Value))
	}
	jw.EndObj()
}

func chatUsageJSON(facts usage.Facts) []byte {
	jw := newJSONWriter()
	writeChatUsage(jw, facts)
	return jw.Bytes()
}

type providerUsageState struct {
	facts            usage.Facts
	source           func() usage.Facts
	outputBasis      usage.OutputBasis
	clientInputScale float64
}

func (s *providerUsageState) SetUsageSource(source func() usage.Facts) { s.source = source }

func (s *providerUsageState) SetUsageOutputBasis(basis usage.OutputBasis) { s.outputBasis = basis }

func (s *providerUsageState) SetClientInputTokenScale(scale float64) {
	s.clientInputScale = scale
}

func (s *providerUsageState) clientInputTokenScale() float64 {
	if s.clientInputScale <= 0 {
		return 1
	}
	return s.clientInputScale
}

func (s *providerUsageState) UsageFacts() usage.Facts {
	if s.source != nil {
		return s.source()
	}
	return s.facts
}

func (s *providerUsageState) observeUsage(object gjson.Result, parse func(gjson.Result) usage.Facts, sink UsageSink) usage.Facts {
	if s.source != nil {
		s.facts = s.source()
		return s.facts
	}
	update := parse(object)
	if s.outputBasis != usage.OutputUnknown {
		update.OutputBasis = s.outputBasis
	}
	s.facts.Merge(update)
	if sink != nil && update.Present {
		sink.RecordProviderUsage(update)
	}
	return s.facts
}
