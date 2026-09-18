package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestServingCommandsRejectUnsafeTargetsBeforeOpeningRegistry(t *testing.T) {
	for _, target := range []string{"beta", "prod/beta", "prod/unknown", ""} {
		err := run(context.Background(), []string{string(commandServing), string(commandStatus), "--target", target})
		require.ErrorContains(t, err, "unsupported managed target")
	}
}

func TestServingLifecycleRequiresExactProposalBeforeOpeningRegistry(t *testing.T) {
	for _, command := range []commandName{commandActivate, commandPrepare, commandRollback} {
		err := run(context.Background(), []string{string(commandServing), string(command)})
		require.ErrorContains(t, err, "exact immutable proposal reference")
	}
}
