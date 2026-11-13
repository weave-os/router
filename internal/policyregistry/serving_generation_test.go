package policyregistry_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/policyregistry"
)

func TestRejectStaleServingGeneration(t *testing.T) {
	require.NoError(t, policyregistry.RejectStaleServingGeneration(1, 1))
	require.NoError(t, policyregistry.RejectStaleServingGeneration(1, 2))
	require.ErrorIs(t, policyregistry.RejectStaleServingGeneration(3, 2), policyregistry.ErrStaleServingGeneration)
}
