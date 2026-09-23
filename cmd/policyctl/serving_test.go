package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestServingCommandsRejectUnsafeTargetsBeforeOpeningRegistry(t *testing.T) {
	for _, target := range []string{"beta", "prod/beta", "prod/unknown"} {
		err := run(context.Background(), []string{string(commandServing), string(commandStatus), "--target", target})
		require.ErrorContains(t, err, "unsupported managed target")
	}
	err := run(context.Background(), []string{string(commandServing), string(commandStatus)})
	require.ErrorContains(t, err, "exactly one of --target or --proposal")
	err = run(context.Background(), []string{string(commandServing), string(commandStatus), "--target", "staging", "--proposal", "ref.json"})
	require.ErrorContains(t, err, "exactly one of --target or --proposal")
}

func TestServingLifecycleRequiresExactProposalBeforeOpeningRegistry(t *testing.T) {
	for _, command := range []commandName{commandApply, commandRollback} {
		err := run(context.Background(), []string{string(commandServing), string(command)})
		require.ErrorContains(t, err, "exact immutable proposal reference")
	}
	err := run(context.Background(), []string{string(commandServing), string(commandApply), "--proposal", "ref.json", "--proposal-sha256", "abc"})
	require.ErrorContains(t, err, "exactly one of --proposal")
	err = run(context.Background(), []string{string(commandServing), string(commandRollback), "--proposal-sha256", "abc"})
	require.ErrorContains(t, err, "flag provided but not defined: -proposal-sha256")
	err = run(context.Background(), []string{string(commandServing), string(commandPublish), "--kind", "candidate", "--manifest", "m.json", "--target", "staging"})
	require.ErrorContains(t, err, "flag provided but not defined: -target")
}

func TestServingRemovedVerbsExitNonZeroThroughTheRootCommand(t *testing.T) {
	for verb, replacement := range map[string]string{"validate": "publish --dry-run", "resolve": "apply --proposal-sha256", "prepare": "apply --dry-run", "activate": "serving apply"} {
		err := run(context.Background(), []string{string(commandServing), verb})
		require.ErrorContains(t, err, "was removed", verb)
		require.ErrorContains(t, err, replacement, verb)
	}
}
