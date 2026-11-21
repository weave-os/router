package candidates_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/candidates"
)

func TestExcludeMaxedModelCopiesBothExclusionSets(t *testing.T) {
	excluded := map[string]struct{}{outputLimitSecondModel: {}}
	safety := map[string]struct{}{}
	req := router.Request{Candidates: router.Candidates{ExcludedModels: excluded, SafetyExcludedModels: safety}}

	got := candidates.ExcludeMaxedModel(req, outputLimitFirstModel)

	assert.Equal(t, map[string]struct{}{outputLimitFirstModel: {}, outputLimitSecondModel: {}}, got.Candidates.ExcludedModels)
	assert.Equal(t, map[string]struct{}{outputLimitFirstModel: {}}, got.Candidates.SafetyExcludedModels)
	assert.Equal(t, map[string]struct{}{outputLimitSecondModel: {}}, excluded)
	assert.Empty(t, safety)
}

func TestReadmitPinnedModelKeepsOtherExclusions(t *testing.T) {
	req := router.Request{Candidates: router.Candidates{
		ExcludedModels:       map[string]struct{}{outputLimitFirstModel: {}, outputLimitSecondModel: {}},
		SafetyExcludedModels: map[string]struct{}{outputLimitFirstModel: {}},
	}}

	got := candidates.ReadmitPinnedModel(req, outputLimitFirstModel)

	assert.Equal(t, map[string]struct{}{outputLimitSecondModel: {}}, got.Candidates.ExcludedModels)
	assert.Empty(t, got.Candidates.SafetyExcludedModels)
}
