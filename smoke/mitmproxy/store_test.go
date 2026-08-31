package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRequestKey_MasksPromptCacheKey(t *testing.T) {
	base := []byte(`{"model":"gpt-5.4-nano","instructions":"hi","tools":[]}`)
	withA := []byte(`{"model":"gpt-5.4-nano","instructions":"hi","tools":[],"prompt_cache_key":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)
	withB := []byte(`{"model":"gpt-5.4-nano","instructions":"hi","tools":[],"prompt_cache_key":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}`)
	other := []byte(`{"model":"gpt-5.4-nano","instructions":"other","tools":[],"prompt_cache_key":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)

	assert.Equal(t, requestKey("POST", "/v1/responses", base),
		requestKey("POST", "/v1/responses", withA),
		"a body with and without prompt_cache_key must hash identically")
	assert.Equal(t, requestKey("POST", "/v1/responses", withA),
		requestKey("POST", "/v1/responses", withB),
		"two sessions that differ only in the affinity hint must share a cassette")
	assert.NotEqual(t, requestKey("POST", "/v1/responses", withA),
		requestKey("POST", "/v1/responses", other),
		"a genuine body change (instructions) must still produce a distinct key")
}

func TestRequestKey_LeavesBodiesWithoutTheFieldUnchanged(t *testing.T) {
	a := []byte(`{"model":"claude-haiku-4-5","system":"x"}`)
	b := []byte(`{"model":"claude-haiku-4-5","system":"y"}`)
	assert.NotEqual(t, requestKey("POST", "/v1/messages", a), requestKey("POST", "/v1/messages", b))
	assert.Equal(t, requestKey("POST", "/v1/messages", a), requestKey("POST", "/v1/messages", a))
}
