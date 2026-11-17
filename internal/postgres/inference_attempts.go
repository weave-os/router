package postgres

import (
	"context"
	"github.com/google/uuid"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/postgres/dbbudget"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/sqlc"
)

func (r *TelemetryRepo) InsertInferenceAttempt(ctx context.Context, p proxy.InsertInferenceAttemptParams) error {
	id, err := uuid.Parse(p.InstallationID)
	if err != nil {
		return err
	}
	return dbbudget.Queries(r.tx).InsertInferenceAttempt(ctx, inferenceAttemptParams(id, p.Event))
}

func inferenceAttemptParams(installationID uuid.UUID, event inference.AttemptEvent) sqlc.InsertInferenceAttemptParams {
	params := sqlc.InsertInferenceAttemptParams{
		InstallationID:   installationID,
		RequestID:        event.RequestID,
		OperationID:      event.OperationID,
		AttemptIndex:     int32(event.AttemptIndex),
		Purpose:          string(event.Purpose),
		PolicyID:         string(event.PolicyID),
		RegistryRevision: string(event.RegistryRevision),
		PolicyRevision:   string(event.PolicyRevision),
		Model:            event.Target.CatalogID,
		Provider:         event.Target.Provider,
		BindingIndex:     int32(event.Target.BindingIndex),
		Outcome:          string(event.Outcome),
		FailureReason:    stringPtrOrNil(event.FailureReason),
		UsageKnown:       event.Usage.Known,
		ErrorClass:       stringPtrOrNil(event.Diagnosis.ErrorClass),
		FailureOwner:     stringPtrOrNil(event.Diagnosis.FailureOwner),
		BodyTranslated:   event.Diagnosis.BodyTranslated,
	}
	if event.UpstreamStatusCode != 0 {
		status := int32(event.UpstreamStatusCode)
		params.UpstreamStatusCode = &status
	}
	if event.Latency > 0 {
		latency := event.Latency.Milliseconds()
		params.LatencyMs = &latency
	}
	if event.Usage.Known {
		input, output := int32(event.Usage.InputTokens), int32(event.Usage.OutputTokens)
		cacheCreation, cacheRead := int32(event.Usage.CacheCreationTokens), int32(event.Usage.CacheReadTokens)
		cost := catalog.USDToMicros(event.CostUSD)
		params.InputTokens = &input
		params.OutputTokens = &output
		params.CacheCreationTokens = &cacheCreation
		params.CacheReadTokens = &cacheRead
		params.CostUsdMicros = &cost
	}
	return params
}
