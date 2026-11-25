package main

import (
	"context"
	"log/slog"
	"strconv"
	"strings"

	"weave-os/router/internal/api/admin"
	"weave-os/router/internal/config"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/postgres"
	"weave-os/router/internal/server"
	"weave-os/router/internal/sqlc"
)

func uiEnabledFromEnv() bool {
	if enabled, err := strconv.ParseBool(strings.TrimSpace(config.GetOr("ROUTER_UI_ENABLED", ""))); err == nil {
		return enabled
	}
	linkedToWeave := server.DeploymentMode(config.GetOr("ROUTER_DEPLOYMENT_MODE", string(server.DeploymentModeSelfHosted))) == server.DeploymentModeSelfHosted && internalServiceTokenFromEnv() != ""
	return !linkedToWeave
}

func withVersionDetails(features server.Features, pool sqlc.DBTX) server.Features {
	var servingSource *admin.ServingSource
	if features.ServingAdmission != nil {
		servingSource = exportedServingSource(features.ServingAdmission.Identity)
	}
	features.Version = admin.VersionDetails{
		SchemaVersion: func(ctx context.Context) (int64, error) { return postgres.AppliedSchemaVersion(ctx, pool) },
		ServingSource: servingSource,
	}
	return features
}

func exportedServingSource(identity policyregistry.WorkerIdentity) *admin.ServingSource {
	registryURI := strings.TrimSpace(config.GetOr("ROUTER_SERVING_REGISTRY_URI", ""))
	if registryURI == "" || strings.HasPrefix(registryURI, "gs://") {
		return nil
	}
	source, err := policyregistry.ReadSelfHostedServingSource(registryURI, identity.Configuration)
	if err != nil {
		slog.Info("Serving registry carries no self-hosted export provenance", "registry_uri", registryURI, "err", err)
		return nil
	}
	slog.Info("Serving self-hosted export", "source_target", source.Target, "source_activation_id", source.ActivationID, "source_selection_set_sha256", source.SelectionSetSHA256)
	return &admin.ServingSource{Target: string(source.Target), ActivationID: source.ActivationID, SelectionSetSHA256: source.SelectionSetSHA256}
}
