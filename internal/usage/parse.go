package usage

import (
	"strings"

	"github.com/tidwall/gjson"
)

func count(field gjson.Result) Count {
	if field.Type != gjson.Number || field.Int() < 0 {
		return Count{}
	}
	return Reported(int(field.Int()))
}

// ParseAnthropic reads a Messages usage object, including rate modifiers.
func ParseAnthropic(object gjson.Result) Facts {
	if !object.IsObject() {
		return Facts{}
	}
	speed, geo := object.Get("speed"), object.Get("inference_geo")
	return Facts{
		Present: true, InputBasis: InputFresh, OutputBasis: OutputInclusive,
		Input: count(object.Get("input_tokens")), Output: count(object.Get("output_tokens")),
		CacheWrite: count(object.Get("cache_creation_input_tokens")), CacheRead: count(object.Get("cache_read_input_tokens")),
		CacheWrite5m: count(object.Get("cache_creation.ephemeral_5m_input_tokens")), CacheWrite1h: count(object.Get("cache_creation.ephemeral_1h_input_tokens")),
		Speed: Speed(strings.Clone(speed.String())), SpeedReported: speed.Type == gjson.String,
		InferenceGeo: InferenceGeo(strings.Clone(geo.String())), InferenceGeoReported: geo.Type == gjson.String,
	}
}

// ParseOpenAI reads Chat or Responses usage. Native shape fields take
// precedence over compatibility aliases, including when they report zero.
func ParseOpenAI(object gjson.Result) Facts {
	if !object.IsObject() {
		return Facts{}
	}
	f := Facts{Present: true, InputBasis: InputInclusive, OutputBasis: OutputInclusive, Total: count(object.Get("total_tokens"))}
	input, output := object.Get("prompt_tokens"), object.Get("completion_tokens")
	if !input.Exists() {
		input = object.Get("input_tokens")
	}
	if !output.Exists() {
		output = object.Get("output_tokens")
	}
	f.Input, f.Output = count(input), count(output)

	for _, prefix := range []string{"input_tokens_details", "prompt_tokens_details"} {
		details := object.Get(prefix)
		if !f.CacheRead.Reported {
			f.CacheRead = count(details.Get("cached_tokens"))
		}
		if !f.CacheWrite.Reported {
			write := details.Get("cache_write_tokens")
			if !write.Exists() {
				write = details.Get("cache_creation_tokens")
			}
			f.CacheWrite = count(write)
		}
	}
	reasoning := object.Get("output_tokens_details.reasoning_tokens")
	if !reasoning.Exists() {
		reasoning = object.Get("completion_tokens_details.reasoning_tokens")
	}
	f.Reasoning = count(reasoning)
	return f
}

// ParseGemini retains candidates and thoughts separately so partial cumulative
// snapshots can update either count without erasing the other.
func ParseGemini(object gjson.Result) Facts {
	if !object.IsObject() {
		return Facts{}
	}
	return Facts{Present: true, InputBasis: InputInclusive, OutputBasis: OutputExcludesReasoning,
		Input: count(object.Get("promptTokenCount")), Output: count(object.Get("candidatesTokenCount")), Reasoning: count(object.Get("thoughtsTokenCount")),
		Total: count(object.Get("totalTokenCount")), CacheRead: count(object.Get("cachedContentTokenCount")),
	}
}
