package translate

import "github.com/tidwall/gjson"

// OpenAICacheTokens extracts cache-write and cache-read counts from an OpenAI
// usage object. GPT-5.6+ reports cache_write_tokens; cache_creation_tokens is
// a compat fallback for hosts that already emit our internal name.
func OpenAICacheTokens(usage gjson.Result) (cacheWrite, cacheRead int) {
	if !usage.Exists() {
		return 0, 0
	}
	for _, prefix := range []string{"input_tokens_details", "prompt_tokens_details"} {
		details := usage.Get(prefix)
		if !details.Exists() {
			continue
		}
		if cacheRead == 0 {
			cacheRead = int(details.Get("cached_tokens").Int())
		}
		if cacheWrite == 0 {
			if w := details.Get("cache_write_tokens"); w.Exists() {
				cacheWrite = int(w.Int())
			} else {
				cacheWrite = int(details.Get("cache_creation_tokens").Int())
			}
		}
	}
	return cacheWrite, cacheRead
}
