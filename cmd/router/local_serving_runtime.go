package main

import (
	"context"
	"errors"
	"slices"

	"weave-os/router/internal/config"
	"weave-os/router/internal/policyclient"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/router/cluster"
	"weave-os/router/internal/router/hmm"
	"weave-os/router/internal/server"
)

// buildLocalServingRuntime prepares the exported V4 selection set's default lane against the
// self-hosted classifier worker. Only self-hosted mode may serve a local release: its worker
// is a plain private origin called without an ID token, which managed serving never accepts.
func buildLocalServingRuntime(
	ctx context.Context,
	mode server.DeploymentMode,
	dir string,
	availableProviders map[string]struct{},
	taskRuntimes ...*taskDomainRuntime,
) (*policyregistry.Snapshot, policyregistry.LocalServingRelease, error) {
	if mode != server.DeploymentModeSelfHosted {
		return nil, policyregistry.LocalServingRelease{}, errors.New("ROUTER_SERVING_RELEASE_DIR is only supported in self-hosted deployment mode")
	}
	directory, err := policyregistry.NewServingDirectory(dir, config.GetOr("ROUTER_CLASSIFIER_WORKER_URL", ""))
	if err != nil {
		return nil, policyregistry.LocalServingRelease{}, err
	}
	timeout := parseEnvDurationMs("ROUTER_HMM_SIDECAR_TIMEOUT_MS", policyclient.DefaultTimeout)
	attemptTimeout := parseEnvAttemptTimeoutMs("ROUTER_HMM_SIDECAR_ATTEMPT_TIMEOUT_MS", policyclient.DeriveAttemptTimeout(timeout))
	cache, err := policyregistry.NewServingRuntimeCache(directory, hmmPolicySnapshotBuilder(
		availableProviders, slices.Clone(managedServingStrategies), policySidecarAuthNone, timeout, attemptTimeout, taskRuntimes...,
	))
	if err != nil {
		return nil, policyregistry.LocalServingRelease{}, err
	}
	release := directory.Release()
	snapshot, err := cache.Snapshot(ctx, policyregistry.SessionReleaseBinding{
		Target: release.Target, Selection: directory.SelectionSet().View(release.SelectionSet).Default,
	})
	if err != nil {
		return nil, policyregistry.LocalServingRelease{}, err
	}
	return snapshot, release, nil
}

// localHMMRosterSource maps the exported release's arms to the HMM deployed-model roster.
type localHMMRosterSource struct {
	roster policyregistry.LocalReleaseRosterSource
}

func (s localHMMRosterSource) HMMDeployedModels(ctx context.Context) ([]cluster.DeployedEntry, error) {
	arms, err := s.roster.Roster(ctx)
	if err != nil {
		return nil, err
	}
	return hmm.DeployedModelsForRosterIDs(arms), nil
}
