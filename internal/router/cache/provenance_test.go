package cache_test

import (
	"fmt"
	"testing"
	"weave-os/router/internal/router/catalog"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/cache"
)

func provenanceScope() cache.ProvenanceScope {
	return cache.ProvenanceScope{CredentialSubject: "subject", Product: cache.ProductLegacy, Profile: "profile", ProfileRevision: "revision", Release: "release", Binding: "binding", Model: catalog.ModelIDClaudeHaiku45, Provider: providers.ProviderAnthropic, UpstreamScope: "account"}
}

func TestCache_ProvenanceIsolation(t *testing.T) {
	original := provenanceScope()
	provenance := cache.NewProvenance(original)
	c := cache.New(cache.DefaultConfig())
	embedding := []float32{1, 0}
	c.Store("installation", cache.FormatAnthropic, embedding, 0, sampleResponse("original"), "version", 0, provenance)
	response, hit := c.Lookup("installation", cache.FormatAnthropic, embedding, []int{0}, "version", 0, provenance)
	require.True(t, hit)
	assert.Equal(t, provenance, response.Provenance)
	for name, mutate := range map[string]func(*cache.ProvenanceScope){
		"subject":          func(s *cache.ProvenanceScope) { s.CredentialSubject = "other" },
		"product":          func(s *cache.ProvenanceScope) { s.Product = cache.ProductBoost },
		"profile":          func(s *cache.ProvenanceScope) { s.Profile = "other" },
		"profile revision": func(s *cache.ProvenanceScope) { s.ProfileRevision = "other" },
		"release":          func(s *cache.ProvenanceScope) { s.Release = "other" },
		"binding":          func(s *cache.ProvenanceScope) { s.Binding = "other" },
		"model":            func(s *cache.ProvenanceScope) { s.Model = catalog.ModelIDClaudeOpus47 },
		"provider":         func(s *cache.ProvenanceScope) { s.Provider = providers.ProviderAnthropicGateway },
		"upstream account": func(s *cache.ProvenanceScope) { s.UpstreamScope = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := original
			mutate(&changed)
			_, hit := c.Lookup("installation", cache.FormatAnthropic, embedding, []int{0}, "version", 0, cache.NewProvenance(changed))
			assert.False(t, hit)
		})
	}
	_, hit = c.Lookup("installation", cache.FormatOpenAI, embedding, []int{0}, "version", 0, provenance)
	assert.False(t, hit)
	_, hit = c.Lookup("installation", cache.FormatAnthropic, embedding, []int{0}, "version", 0, cache.Provenance{})
	assert.False(t, hit)
}

func TestCache_IncompleteProvenanceCannotPopulate(t *testing.T) {
	for name, mutate := range map[string]func(*cache.ProvenanceScope){
		"subject":         func(s *cache.ProvenanceScope) { s.CredentialSubject = "" },
		"product":         func(s *cache.ProvenanceScope) { s.Product = "" },
		"model":           func(s *cache.ProvenanceScope) { s.Model = "" },
		"provider":        func(s *cache.ProvenanceScope) { s.Provider = "" },
		"upstream":        func(s *cache.ProvenanceScope) { s.UpstreamScope = "" },
		"partial profile": func(s *cache.ProvenanceScope) { s.ProfileRevision = "" },
		"orphan revision": func(s *cache.ProvenanceScope) { s.Profile = "" },
		"orphan release":  func(s *cache.ProvenanceScope) { s.Binding = "" },
		"orphan binding":  func(s *cache.ProvenanceScope) { s.Release = "" },
		"unknown product": func(s *cache.ProvenanceScope) { s.Product = "unknown" },
	} {
		t.Run(name, func(t *testing.T) {
			scope := provenanceScope()
			mutate(&scope)
			provenance := cache.NewProvenance(scope)
			require.False(t, provenance.Valid())
			c := cache.New(cache.DefaultConfig())
			c.Store("installation", cache.FormatAnthropic, []float32{1}, 0, sampleResponse("bad"), "", 0, provenance)
			_, hit := c.Lookup("installation", cache.FormatAnthropic, []float32{1}, []int{0}, "", 0, provenance)
			assert.False(t, hit)
		})
	}
}

func TestCache_ProvenanceChurnKeepsBucketAndInstallationCaps(t *testing.T) {
	config := cache.DefaultConfig()
	config.MaxBucketsPerInstallation = 4
	config.MaxInstallations = 2
	c := cache.New(config)
	scope := provenanceScope()
	first := cache.NewProvenance(scope)
	c.Store("retained", cache.FormatAnthropic, []float32{1}, 0, sampleResponse("retained"), "", 0, first)
	for i := 0; i < 1000; i++ {
		scope.CredentialSubject = fmt.Sprint(i)
		c.Store("churn", cache.FormatAnthropic, []float32{1}, 0, sampleResponse("churn"), "", 0, cache.NewProvenance(scope))
	}
	stats := c.Stats()
	assert.Equal(t, uint64(996), stats.BucketEvictions)
	_, hit := c.Lookup("retained", cache.FormatAnthropic, []float32{1}, []int{0}, "", 0, first)
	require.True(t, hit)
	c.Store("third", cache.FormatAnthropic, []float32{1}, 0, sampleResponse("third"), "", 0, first)
	assert.Equal(t, uint64(1), c.Stats().InstallationEvictions)
	_, hit = c.Lookup("churn", cache.FormatAnthropic, []float32{1}, []int{0}, "", 0, cache.NewProvenance(scope))
	assert.False(t, hit)
	assert.Equal(t, uint64(1), c.Stats().Hits)
	assert.Equal(t, uint64(1), c.Stats().Misses)
}

func TestCache_ResponseOwnershipDetachedOnStoreAndReplay(t *testing.T) {
	c := cache.New(cache.DefaultConfig())
	provenance := cache.NewProvenance(provenanceScope())
	response := sampleResponse("original")
	c.Store("installation", cache.FormatAnthropic, []float32{1}, 0, response, "", 0, provenance)
	response.Body[0] = 'x'
	response.Headers.Set("Content-Type", "mutated")
	first, hit := c.Lookup("installation", cache.FormatAnthropic, []float32{1}, []int{0}, "", 0, provenance)
	require.True(t, hit)
	assert.Equal(t, "original", string(first.Body))
	assert.Equal(t, "application/json", first.Headers.Get("Content-Type"))
	first.Body[0] = 'x'
	first.Headers.Set("Content-Type", "mutated")
	second, hit := c.Lookup("installation", cache.FormatAnthropic, []float32{1}, []int{0}, "", 0, provenance)
	require.True(t, hit)
	assert.Equal(t, "original", string(second.Body))
	assert.Equal(t, "application/json", second.Headers.Get("Content-Type"))
}
