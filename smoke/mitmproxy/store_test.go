package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRequestKeyIgnoresVolatileFields is the guard for the failure mode that
// broke every replay-only CI run: the router began injecting a per-run
// prompt_cache_key (derived from the freshly-seeded API key id), so the same
// scenario hashed to a new cassette key on every run and always cache-missed.
func TestRequestKeyIgnoresVolatileFields(t *testing.T) {
	const path = "/v1/responses"
	base := []byte(`{"model":"gpt-5.4-nano","input":[{"role":"user","content":"ok"}]}`)
	runA := []byte(`{"model":"gpt-5.4-nano","prompt_cache_key":"wv_aaaa","input":[{"role":"user","content":"ok"}]}`)
	runB := []byte(`{"model":"gpt-5.4-nano","prompt_cache_key":"wv_bbbb","input":[{"role":"user","content":"ok"}]}`)

	keyA := requestKey("POST", path, runA)
	keyB := requestKey("POST", path, runB)
	if keyA != keyB {
		t.Errorf("two runs of one scenario must share a cassette key; got %s vs %s", keyA, keyB)
	}
	if got := requestKey("POST", path, base); got != keyA {
		t.Errorf("a body without the hint must match one carrying it; got %s want %s", got, keyA)
	}
}

// TestRequestKeyDistinguishesRealChanges pins the other half of the contract:
// canonicalization must not collapse genuinely different requests onto one
// cassette, which would silently replay the wrong recorded response.
func TestRequestKeyDistinguishesRealChanges(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4-nano","input":[{"role":"user","content":"ok"}]}`)
	other := []byte(`{"model":"gpt-5.4-nano","input":[{"role":"user","content":"different"}]}`)

	if requestKey("POST", "/v1/responses", body) == requestKey("POST", "/v1/responses", other) {
		t.Error("different request bodies must not share a cassette key")
	}
	if requestKey("POST", "/v1/responses", body) == requestKey("POST", "/v1/chat/completions", body) {
		t.Error("different paths must not share a cassette key")
	}
	if requestKey("POST", "/v1/responses", body) == requestKey("GET", "/v1/responses", body) {
		t.Error("different methods must not share a cassette key")
	}
}

// TestCanonicalizeBodyPassesThroughNonJSON guards the fallback: a body the
// stripper can't parse must still hash, not panic or hash to empty.
func TestCanonicalizeBodyPassesThroughNonJSON(t *testing.T) {
	raw := []byte("not json at all")
	if got := string(canonicalizeBody(raw)); got != string(raw) {
		t.Errorf("non-JSON body must pass through unchanged; got %q", got)
	}
	if got := canonicalizeBody(nil); got != nil {
		t.Errorf("nil body must pass through; got %q", got)
	}
}

// TestSaveWritesGroupReadableCassette is the guard for the bug that kept the
// nightly refresh red for a month: CreateTemp's 0600 left the recorded
// cassettes unreadable by the CI runner's git, so `git add` failed and no
// refresh PR was ever opened.
func TestSaveWritesGroupReadableCassette(t *testing.T) {
	dir := t.TempDir()
	s, err := newStore(dir)
	if err != nil {
		t.Fatalf("newStore: %v", err)
	}
	const key = "abc123"
	if err := s.save(key, &cassette{Method: "POST", Path: "/v1/responses", StatusCode: 200, Body: []byte("{}")}); err != nil {
		t.Fatalf("save: %v", err)
	}

	info, err := os.Stat(filepath.Join(dir, key+".json"))
	if err != nil {
		t.Fatalf("stat cassette: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o644 {
		t.Errorf("cassette must be world-readable so the CI runner's git can add it; got %o want 644", perm)
	}
}
