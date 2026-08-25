package main

import (
	"testing"

	"workweave/router/internal/router/catalog"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProxyRoutableModels_WithoutHMMLeavesGenericSet(t *testing.T) {
	generic := map[string]struct{}{"claude-opus-4-7": {}}
	got := proxyRoutableModels(generic, map[string]struct{}{"anthropic": {}}, false)
	assert.Equal(t, generic, got)
}

func TestProxyRoutableModels_WithHMMUnionsHMMOnlyTargets(t *testing.T) {
	providers := map[string]struct{}{}
	for _, m := range catalog.Models {
		for _, b := range m.Providers {
			providers[b.Provider] = struct{}{}
		}
	}
	generic := catalog.RoutingTargetSet(providers)
	hmm := catalog.HMMRoutingTargetSet(providers)
	require.Greater(t, len(hmm), len(generic), "catalog must have HMM-only rows or this test is vacuous")

	got := proxyRoutableModels(generic, providers, true)
	assert.Equal(t, len(hmm), len(got))
	for id := range hmm {
		assert.Contains(t, got, id)
	}
}
