package main

import "testing"

// TestRequestKeyIgnoresPromptCacheKey pins that per-run session-affinity hints
// don't change the cassette key, so replay-only CI stays green.
func TestRequestKeyIgnoresPromptCacheKey(t *testing.T) {
	base := `{"model":"gpt-5.1","input":"hi"}`
	runA := `{"model":"gpt-5.1","input":"hi","prompt_cache_key":"aaaa"}`
	runB := `{"model":"gpt-5.1","input":"hi","prompt_cache_key":"bbbb"}`

	keyA := requestKey("POST", "/v1/responses", []byte(runA))
	keyB := requestKey("POST", "/v1/responses", []byte(runB))
	if keyA != keyB {
		t.Errorf("prompt_cache_key changed the cassette key: %s != %s", keyA, keyB)
	}
	// Cassettes recorded before the router started sending the hint must keep
	// replaying, so the hinted body has to hash like the unhinted one.
	if want := requestKey("POST", "/v1/responses", []byte(base)); keyA != want {
		t.Errorf("hinted body key %s, want unhinted key %s", keyA, want)
	}
}

// TestRequestKeyDistinguishesScenarios guards against normalization collapsing
// genuinely different requests onto one cassette.
func TestRequestKeyDistinguishesScenarios(t *testing.T) {
	one := requestKey("POST", "/v1/responses", []byte(`{"input":"hi"}`))
	two := requestKey("POST", "/v1/responses", []byte(`{"input":"bye"}`))
	if one == two {
		t.Error("different request bodies hashed to the same cassette key")
	}
	if p := requestKey("POST", "/v1/messages", []byte(`{"input":"hi"}`)); p == one {
		t.Error("different paths hashed to the same cassette key")
	}
}

// TestNormalizeRequestBodyLeavesOtherBodiesUntouched keeps the Anthropic
// cassettes valid: a body with no volatile field must hash its exact bytes.
func TestNormalizeRequestBodyLeavesOtherBodiesUntouched(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-5","metadata":{"user_id":"smoke-basic"}}`)
	if got := string(normalizeRequestBody(body)); got != string(body) {
		t.Errorf("body rewritten: %s", got)
	}
	if got := string(normalizeRequestBody([]byte("not json"))); got != "not json" {
		t.Errorf("non-JSON body rewritten: %s", got)
	}
}
