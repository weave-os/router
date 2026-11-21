package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"weave-os/router/internal/inference"
	"weave-os/router/internal/postgres/dbbudget"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/sqlc"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const usdPerMicro = 1.0 / 1_000_000.0

func microsToUSD(micros int64) float64 {
	return float64(micros) * usdPerMicro
}

func int64PtrFromUSD(usd *float64) *int64 {
	if usd == nil {
		return nil
	}
	v := catalog.SignedUSDToMicros(*usd)
	return &v
}

type TelemetryRepo struct {
	tx sqlc.DBTX
}

func NewTelemetryRepo(tx sqlc.DBTX) *TelemetryRepo {
	return &TelemetryRepo{tx: tx}
}

var _ proxy.TelemetryRepository = (*TelemetryRepo)(nil)

func (r *TelemetryRepo) InsertRequestTelemetry(ctx context.Context, p proxy.InsertTelemetryParams) error {
	id, err := uuid.Parse(p.InstallationID)
	if err != nil {
		return err
	}
	q := dbbudget.Queries(r.tx)
	return q.InsertRequestTelemetry(ctx, sqlc.InsertRequestTelemetryParams{
		InstallationID:                           id,
		APIKeyID:                                 uuidOrNil(p.APIKeyID),
		RequestID:                                p.RequestID,
		SpanType:                                 p.SpanType,
		TraceID:                                  p.TraceID,
		Timestamp:                                pgtype.Timestamptz{Time: p.Timestamp, Valid: true},
		RequestedModel:                           p.RequestedModel,
		DecisionModel:                            p.DecisionModel,
		DecisionProvider:                         p.DecisionProvider,
		DecisionReason:                           p.DecisionReason,
		BlindExperimentArm:                       stringPtrOrNil(string(p.BlindExperimentArm)),
		BlindExperimentAssignmentSource:          stringPtrOrNil(string(p.BlindExperimentAssignmentSource)),
		BlindExperimentSubjectKey:                stringPtrOrNil(p.BlindExperimentSubjectKey),
		BlindExperimentMode:                      stringPtrOrNil(string(p.BlindExperimentMode)),
		BlindExperimentSessionKey:                stringPtrOrNil(p.BlindExperimentSessionKey),
		CohortExperimentID:                       uuidOrNil(p.CohortExperimentID),
		CohortGroupID:                            p.CohortGroupID,
		CohortPhaseIndex:                         p.CohortPhaseIndex,
		CohortRevision:                           p.CohortRevision,
		CohortScheduledArm:                       stringPtrOrNil(string(p.CohortScheduledArm)),
		CohortTreatmentApplied:                   p.CohortTreatmentApplied,
		CohortBypassReason:                       stringPtrOrNil(string(p.CohortBypassReason)),
		BlindExperimentModelSource:               stringPtrOrNil(string(p.BlindExperimentModelSource)),
		BlindExperimentTreatmentApplied:          p.BlindExperimentTreatmentApplied,
		BlindExperimentBaselineModel:             stringPtrOrNil(p.BlindExperimentBaselineModel),
		BlindExperimentModelChanged:              p.BlindExperimentModelChanged,
		EstimatedInputTokens:                     p.EstimatedInputTokens,
		ContextSnapshot:                          p.ContextSnapshot,
		StickyHit:                                p.StickyHit,
		PinTier:                                  stringPtrOrNil(p.PinTier),
		EmbedInput:                               p.EmbedInput,
		InputTokens:                              p.InputTokens,
		OutputTokens:                             p.OutputTokens,
		RequestedInputCostUsd:                    catalog.USDToMicros(p.RequestedInputCostUSD),
		RequestedOutputCostUsd:                   catalog.USDToMicros(p.RequestedOutputCostUSD),
		ActualInputCostUsd:                       catalog.USDToMicros(p.ActualInputCostUSD),
		ActualOutputCostUsd:                      catalog.USDToMicros(p.ActualOutputCostUSD),
		RouteLatencyMs:                           p.RouteLatencyMs,
		UpstreamLatencyMs:                        p.UpstreamLatencyMs,
		TotalLatencyMs:                           p.TotalLatencyMs,
		CrossFormat:                              p.CrossFormat,
		UpstreamStatusCode:                       p.UpstreamStatusCode,
		ClusterIds:                               p.ClusterIDs,
		CandidateModels:                          p.CandidateModels,
		ChosenScore:                              p.ChosenScore,
		CandidateScores:                          p.CandidateScores,
		Propensity:                               p.Propensity,
		AlphaBreakdown:                           p.AlphaBreakdown,
		ClusterRouterVersion:                     stringPtrOrNil(p.ClusterRouterVersion),
		Strategy:                                 stringPtrOrNil(p.Strategy),
		RouteID:                                  stringPtrOrNil(p.RouteID),
		PolicyRouteKey:                           stringPtrOrNil(p.PolicyRouteKey),
		PolicyArtifactID:                         stringPtrOrNil(p.PolicyArtifactID),
		PolicyArtifactSha256:                     stringPtrOrNil(p.PolicyArtifactSHA256),
		RosterVersion:                            stringPtrOrNil(p.RosterVersion),
		ClassifierArtifactID:                     stringPtrOrNil(p.ClassifierArtifactID),
		ClassifierArtifactSha256:                 stringPtrOrNil(p.ClassifierArtifactSHA256),
		ClassifierPredictedLabel:                 stringPtrOrNil(p.ClassifierPredictedLabel),
		ClassifierClassOrder:                     p.ClassifierClassOrder,
		ClassifierProbabilities:                  p.ClassifierProbabilities,
		SelectionPolicyReleaseID:                 stringPtrOrNil(p.SelectionPolicyReleaseID),
		SelectionPolicySha256:                    stringPtrOrNil(p.SelectionPolicySHA256),
		SelectionHeadGeneration:                  p.SelectionHeadGeneration,
		SelectionTrace:                           p.SelectionTrace,
		SidecarSchemaVersion:                     stringPtrOrNil(p.SidecarSchemaVersion),
		TrainingAllowed:                          p.TrainingAllowed,
		CaptureMode:                              p.CaptureMode,
		DebugRef:                                 stringPtrOrNil(p.DebugRef),
		TtftMs:                                   p.TTFTMs,
		CacheCreationTokens:                      p.CacheCreationTokens,
		CacheCreation1hTokens:                    p.CacheCreation1hTokens,
		CacheReadTokens:                          p.CacheReadTokens,
		ReasoningTokens:                          p.ReasoningTokens,
		Speed:                                    stringPtrOrNil(p.Speed),
		InferenceGeo:                             stringPtrOrNil(p.InferenceGeo),
		DeviceID:                                 stringPtrOrNil(p.DeviceID),
		SessionID:                                stringPtrOrNil(p.SessionID),
		RouterUserID:                             uuidOrNil(p.RouterUserID),
		ClientApp:                                stringPtrOrNil(p.ClientApp),
		RolloutID:                                stringPtrOrNil(p.RolloutID),
		TurnType:                                 p.TurnType,
		UpstreamFinishReason:                     p.UpstreamFinishReason,
		StopReason:                               p.StopReason,
		ToolUseBlocks:                            p.ToolUseBlocks,
		InvalidToolArgsBlocks:                    p.InvalidToolArgsBlocks,
		LastToolUseName:                          stringPtrOrNil(p.LastToolUseName),
		LastToolUseInputBytes:                    p.LastToolUseInputBytes,
		ToolErrorCounts:                          p.ToolErrorCounts,
		AutonomyAppendFired:                      p.AutonomyAppendFired,
		WorkspaceAppendFired:                     p.WorkspaceAppendFired,
		FailoverUsed:                             p.FailoverUsed,
		FailoverAttempted:                        p.FailoverAttempted,
		DegenerateShadow:                         p.DegenerateShadow,
		PolicyPinRequested:                       p.PolicyPinRequested,
		PolicyPinHonoured:                        p.PolicyPinHonoured,
		SessionKey:                               p.SessionKey,
		Role:                                     stringPtrOrNil(p.Role),
		FreshDecisionModel:                       stringPtrOrNil(p.FreshDecisionModel),
		FreshCandidateScores:                     p.FreshCandidateScores,
		PinAgeSec:                                p.PinAgeSec,
		ToolResultBytes:                          p.ToolResultBytes,
		CredentialKeyPrefix:                      stringPtrOrNil(p.CredentialKeyPrefix),
		CredentialKeySuffix:                      stringPtrOrNil(p.CredentialKeySuffix),
		CredentialSource:                         stringPtrOrNil(p.CredentialSource),
		SubscriptionAccountID:                    uuidOrNil(p.SubscriptionAccountID),
		SubscriptionOwnerID:                      uuidOrNil(p.SubscriptionOwnerID),
		SubscriptionTier:                         stringPtrOrNil(string(p.SubscriptionTier)),
		IntendedModelFamily:                      stringPtrOrNil(p.IntendedModelFamily),
		FinalModelFamily:                         stringPtrOrNil(p.FinalModelFamily),
		UnifiedLimitHeaders:                      p.UnifiedLimitHeaders,
		CodexLimitHeaders:                        p.CodexLimitHeaders,
		PlannerOutcome:                           stringPtrOrNil(p.PlannerOutcome),
		PlannerReason:                            stringPtrOrNil(p.PlannerReason),
		PlannerPinModel:                          stringPtrOrNil(p.PlannerPinModel),
		PlannerPinProvider:                       stringPtrOrNil(p.PlannerPinProvider),
		PlannerExpectedSavingsUsdMicros:          int64PtrFromUSD(p.PlannerExpectedSavingsUSD),
		PlannerEvictionCostUsdMicros:             int64PtrFromUSD(p.PlannerEvictionCostUSD),
		PlannerPinCacheCold:                      p.PlannerPinCacheCold,
		PlannerShadowOutcome:                     stringPtrOrNil(p.PlannerShadowOutcome),
		PlannerShadowSavingsUsdMicros:            int64PtrFromUSD(p.PlannerShadowExpectedSavingsUSD),
		AuthorityShadowOutcome:                   stringPtrOrNil(p.AuthorityShadowOutcome),
		AuthorityShadowWouldDiverge:              p.AuthorityShadowWouldDiverge,
		AuthorityShadowReason:                    stringPtrOrNil(p.AuthorityShadowReason),
		AuthorityShadowStayModel:                 stringPtrOrNil(p.AuthorityShadowStayModel),
		AuthorityShadowStayProvider:              stringPtrOrNil(p.AuthorityShadowStayProvider),
		AuthorityShadowSavingsUsdMicros:          int64PtrFromUSD(p.AuthorityShadowExpectedSavingsUSD),
		AuthorityShadowEvictionCostUsdMicros:     int64PtrFromUSD(p.AuthorityShadowEvictionCostUSD),
		AuthorityShadowPinCacheCold:              p.AuthorityShadowPinCacheCold,
		AuthorityShadowCorrectedOutcome:          stringPtrOrNil(p.AuthorityShadowCorrectedOutcome),
		AuthorityShadowCorrectedSavingsUsdMicros: int64PtrFromUSD(p.AuthorityShadowCorrectedSavingsUSD),
		AuthorityShadowStayScore:                 p.AuthorityShadowStayScore,
		AuthorityShadowFreshScore:                p.AuthorityShadowFreshScore,
		SpiralErrStreak:                          p.SpiralErrStreak,
		SpiralErroredResults:                     p.SpiralErroredResults,
		SpiralToolResults:                        p.SpiralToolResults,
		SpiralMaxSameFileEdits:                   p.SpiralMaxSameFileEdits,
		SpiralSameFilePathHash:                   stringPtrOrNil(p.SpiralSameFilePathHash),
		SpiralRepeatFrac:                         p.SpiralRepeatFrac,
		SpiralMonologueLen:                       p.SpiralMonologueLen,
		SpiralToolCallCount:                      p.SpiralToolCallCount,
		SpiralMessageCount:                       p.SpiralMessageCount,
		SpiralPingPongLen:                        p.SpiralPingPongLen,
		SpiralStepsSinceProgress:                 p.SpiralStepsSinceProgress,
		SpiralEditAttempted:                      p.SpiralEditAttempted,
		SpiralReasons:                            p.SpiralReasons,
		RequestedAllowedModels:                   p.RequestedAllowedModels,
		ClientGitHeadSha:                         stringPtrOrNil(p.ClientGitHeadSHA),
		ClientGitBranch:                          stringPtrOrNil(p.ClientGitBranch),
		ClientGitDirty:                           p.ClientGitDirty,
		InferencePurpose:                         inferenceSummaryString(p.Inference, func(s inference.OperationSummary) string { return string(s.Purpose) }),
		InferencePolicyID:                        inferenceSummaryString(p.Inference, func(s inference.OperationSummary) string { return string(s.PolicyID) }),
		InferenceRegistryRevision:                inferenceSummaryString(p.Inference, func(s inference.OperationSummary) string { return string(s.RegistryRevision) }),
		InferencePolicyRevision:                  inferenceSummaryString(p.Inference, func(s inference.OperationSummary) string { return string(s.PolicyRevision) }),
		PlanModel:                                inferenceSummaryString(p.Inference, func(s inference.OperationSummary) string { return s.PlanTarget.CatalogID }),
		PlanProvider:                             inferenceSummaryString(p.Inference, func(s inference.OperationSummary) string { return s.PlanTarget.Provider }),
		FallbackReason:                           inferenceSummaryString(p.Inference, func(s inference.OperationSummary) string { return s.FallbackReason }),
		AccountingOutcome:                        inferenceSummaryString(p.Inference, func(s inference.OperationSummary) string { return string(s.AccountingOutcome) }),
		UsageKnown:                               inferenceUsageKnown(p.Inference),
		EffortArm:                                stringPtrOrNil(p.EffortArm),
		EffortSelected:                           stringPtrOrNil(p.EffortSelected),
		EffortSent:                               stringPtrOrNil(p.EffortSent),
		EffortSource:                             stringPtrOrNil(p.EffortSource),
		SubscriberPlan:                           stringPtrOrNil(p.SubscriberPlan),
		EntitlementVersion:                       p.EntitlementVersion,
		CapacitySource:                           stringPtrOrNil(p.CapacitySource),
		RetailUsageUsdMicros:                     p.RetailUsageMicros,
		IncludedUsageUsdMicros:                   p.IncludedUsageMicros,
		LinkedUsageUsdMicros:                     p.LinkedUsageMicros,
		PrepaidUsageUsdMicros:                    p.PrepaidUsageMicros,
		SettlementFailed:                         p.SettlementFailed,
		ServingProfileID:                         stringPtrOrNil(p.ServingProfileID),
		ServingProfileVersion:                    stringPtrOrNil(p.ServingProfileVersion),
		ServingReleaseID:                         stringPtrOrNil(p.ServingReleaseID),
		ServingBindingID:                         stringPtrOrNil(p.ServingBindingID),
		ServingTarget:                            stringPtrOrNil(p.ServingTarget),
		BoostOptimizerVersion:                    stringPtrOrNil(p.BoostOptimizerVersion),
		UserPrompt:                               p.UserPrompt,
		UserPromptGapMs:                          p.UserPromptGapMs,
		UserPromptGapPriorModel:                  stringPtrOrNil(p.UserPromptGapPriorModel),
		ErrorClass:                               stringPtrOrNil(string(p.ErrorClass)),
		FailureOwner:                             stringPtrOrNil(string(p.FailureOwner)),
		LatestToolCallCounts:                     p.LatestToolCallCounts,
	})
}

func inferenceSummaryString(summary *inference.OperationSummary, field func(inference.OperationSummary) string) *string {
	if summary == nil {
		return nil
	}
	return stringPtrOrNil(field(*summary))
}

func inferenceUsageKnown(summary *inference.OperationSummary) *bool {
	if summary == nil {
		return nil
	}
	known := summary.Usage.Known
	return &known
}

var _ proxy.RouterFeedbackStore = (*TelemetryRepo)(nil)

func (r *TelemetryRepo) InsertRouterFeedback(ctx context.Context, p proxy.RouterFeedbackEvent) error {
	id, err := uuid.Parse(p.InstallationID)
	if err != nil {
		return err
	}
	q := dbbudget.Queries(r.tx)
	return q.InsertRouterFeedback(ctx, sqlc.InsertRouterFeedbackParams{
		InstallationID: id,
		SessionKey:     p.SessionKey,
		Role:           p.Role,
		RouterUserID:   uuidOrNil(p.RouterUserID),
		ClientApp:      stringPtrOrNil(p.ClientApp),
		SessionID:      stringPtrOrNil(p.SessionID),
		RequestedModel: p.RequestedModel,
		ServedModel:    p.ServedModel,
		Rating:         stringPtrOrNil(p.Rating),
		SuggestedLabel: stringPtrOrNil(p.SuggestedLabel),
		Feedback:       p.Feedback,
		Source:         p.Source,
		RequestID:      stringPtrOrNil(p.RequestID),
		RouteID:        stringPtrOrNil(p.RouteID),
	})
}

func (r *TelemetryRepo) GetTelemetrySummary(ctx context.Context, installationID string, from, to time.Time) (proxy.TelemetrySummary, error) {
	id, err := uuid.Parse(installationID)
	if err != nil {
		return proxy.TelemetrySummary{}, err
	}
	q := dbbudget.Queries(r.tx)
	row, err := q.GetTelemetrySummary(ctx, sqlc.GetTelemetrySummaryParams{
		InstallationID: id,
		FromTime:       pgtype.Timestamptz{Time: from, Valid: true},
		ToTime:         pgtype.Timestamptz{Time: to, Valid: true},
	})
	if err != nil {
		return proxy.TelemetrySummary{}, err
	}
	return proxy.TelemetrySummary{
		RequestCount:          row.RequestCount,
		TotalTokens:           row.TotalTokens,
		TotalRequestedCostUSD: microsToUSD(row.TotalRequestedCostUsd),
		TotalActualCostUSD:    microsToUSD(row.TotalActualCostUsd),
		TotalSavingsUSD:       microsToUSD(row.TotalSavingsUsd),
	}, nil
}

func (r *TelemetryRepo) GetTelemetryTimeseries(ctx context.Context, installationID string, from, to time.Time, granularity string) ([]proxy.TelemetryBucket, error) {
	id, err := uuid.Parse(installationID)
	if err != nil {
		return nil, err
	}
	q := dbbudget.Queries(r.tx)
	fromTs := pgtype.Timestamptz{Time: from, Valid: true}
	toTs := pgtype.Timestamptz{Time: to, Valid: true}

	return selectTimeseriesGranularity(granularity,
		func() ([]proxy.TelemetryBucket, error) {
			rows, err := q.GetTelemetryTimeseriesWeekly(ctx, sqlc.GetTelemetryTimeseriesWeeklyParams{
				InstallationID: id,
				FromTime:       fromTs,
				ToTime:         toTs,
			})
			if err != nil {
				return nil, err
			}
			return mapRows(rows, telemetryBucketFromWeeklyRow), nil
		},
		func() ([]proxy.TelemetryBucket, error) {
			rows, err := q.GetTelemetryTimeseriesDaily(ctx, sqlc.GetTelemetryTimeseriesDailyParams{
				InstallationID: id,
				FromTime:       fromTs,
				ToTime:         toTs,
			})
			if err != nil {
				return nil, err
			}
			return mapRows(rows, telemetryBucketFromDailyRow), nil
		},
		func() ([]proxy.TelemetryBucket, error) {
			rows, err := q.GetTelemetryTimeseriesHourly(ctx, sqlc.GetTelemetryTimeseriesHourlyParams{
				InstallationID: id,
				FromTime:       fromTs,
				ToTime:         toTs,
			})
			if err != nil {
				return nil, err
			}
			return mapRows(rows, telemetryBucketFromHourlyRow), nil
		},
	)
}

func (r *TelemetryRepo) GetTelemetrySummaryAll(ctx context.Context, from, to time.Time) (proxy.TelemetrySummary, error) {
	q := dbbudget.Queries(r.tx)
	row, err := q.GetTelemetrySummaryAll(ctx, sqlc.GetTelemetrySummaryAllParams{
		FromTime: pgtype.Timestamptz{Time: from, Valid: true},
		ToTime:   pgtype.Timestamptz{Time: to, Valid: true},
	})
	if err != nil {
		return proxy.TelemetrySummary{}, err
	}
	return proxy.TelemetrySummary{
		RequestCount:          row.RequestCount,
		TotalTokens:           row.TotalTokens,
		TotalRequestedCostUSD: microsToUSD(row.TotalRequestedCostUsd),
		TotalActualCostUSD:    microsToUSD(row.TotalActualCostUsd),
		TotalSavingsUSD:       microsToUSD(row.TotalSavingsUsd),
	}, nil
}

func dashboardInstallationFilter(installationID string) (pgtype.UUID, error) {
	if installationID == "" {
		return pgtype.UUID{}, nil
	}
	id, err := uuid.Parse(installationID)
	if err != nil {
		return pgtype.UUID{}, err
	}
	return pgtype.UUID{Bytes: id, Valid: true}, nil
}

func (r *TelemetryRepo) GetTelemetryDashboardFacts(ctx context.Context, installationID string, from, to time.Time, granularity string) ([]proxy.TelemetryDashboardFact, error) {
	installation, err := dashboardInstallationFilter(installationID)
	if err != nil {
		return nil, err
	}
	rows, err := dbbudget.Queries(r.tx).GetTelemetryDashboardFacts(ctx, sqlc.GetTelemetryDashboardFactsParams{
		Granularity:    granularity,
		FromTime:       pgtype.Timestamptz{Time: from, Valid: true},
		ToTime:         pgtype.Timestamptz{Time: to, Valid: true},
		InstallationID: installation,
	})
	if err != nil {
		return nil, err
	}
	return mapRows(rows, func(row sqlc.GetTelemetryDashboardFactsRow) proxy.TelemetryDashboardFact {
		return proxy.TelemetryDashboardFact{
			Bucket:              row.Bucket.Time,
			DecisionModel:       row.DecisionModel,
			SubscriptionServed:  row.SubscriptionServed,
			DecisionType:        row.DecisionType,
			AutomaticAlias:      row.AutomaticAlias,
			RequestCount:        row.RequestCount,
			InputTokens:         row.InputTokens,
			OutputTokens:        row.OutputTokens,
			CacheCreationTokens: row.CacheCreationTokens,
			CacheReadTokens:     row.CacheReadTokens,
			ActualCostUSD:       microsToUSD(row.ActualCostUsd),
		}
	}), nil
}

func (r *TelemetryRepo) GetTelemetryDashboardActiveUsers(ctx context.Context, installationID string, from, to time.Time, granularity string) ([]proxy.TelemetryActiveUsers, error) {
	installation, err := dashboardInstallationFilter(installationID)
	if err != nil {
		return nil, err
	}
	rows, err := dbbudget.Queries(r.tx).GetTelemetryDashboardActiveUsers(ctx, sqlc.GetTelemetryDashboardActiveUsersParams{
		Granularity:    granularity,
		FromTime:       pgtype.Timestamptz{Time: from, Valid: true},
		ToTime:         pgtype.Timestamptz{Time: to, Valid: true},
		InstallationID: installation,
	})
	if err != nil {
		return nil, err
	}
	return mapRows(rows, func(row sqlc.GetTelemetryDashboardActiveUsersRow) proxy.TelemetryActiveUsers {
		return proxy.TelemetryActiveUsers{Bucket: row.Bucket.Time, Linked: row.Linked, UserCount: row.UserCount}
	}), nil
}

func (r *TelemetryRepo) GetTelemetryDashboardPeople(ctx context.Context, installationID string) ([]proxy.TelemetryPerson, error) {
	installation, err := dashboardInstallationFilter(installationID)
	if err != nil {
		return nil, err
	}
	rows, err := dbbudget.Queries(r.tx).GetTelemetryDashboardPeople(ctx, installation)
	if err != nil {
		return nil, err
	}
	return mapRows(rows, func(row sqlc.GetTelemetryDashboardPeopleRow) proxy.TelemetryPerson {
		person := proxy.TelemetryPerson{ID: row.PersonID, Linked: row.Linked, DisplayName: row.DisplayName, Email: row.Email}
		if row.LastRequestAt.Valid {
			last := row.LastRequestAt.Time
			person.LastRequestAt = &last
		}
		return person
	}), nil
}

func (r *TelemetryRepo) GetTelemetrySubscriptionPoolSeats(ctx context.Context, installationID string, servedSince, readingsSince time.Time) ([]proxy.TelemetrySubscriptionSeat, error) {
	installation, err := dashboardInstallationFilter(installationID)
	if err != nil {
		return nil, err
	}
	rows, err := dbbudget.Queries(r.tx).GetTelemetrySubscriptionPoolSeats(ctx, sqlc.GetTelemetrySubscriptionPoolSeatsParams{
		ServedSince:    pgtype.Timestamptz{Time: servedSince, Valid: true},
		ReadingsSince:  pgtype.Timestamptz{Time: readingsSince, Valid: true},
		InstallationID: installation,
	})
	if err != nil {
		return nil, err
	}
	seats := make([]proxy.TelemetrySubscriptionSeat, 0, len(rows))
	for _, row := range rows {
		seat := proxy.TelemetrySubscriptionSeat{
			ID:               row.ID.String(),
			Provider:         row.Provider,
			DisplayName:      row.DisplayName,
			Enabled:          row.Enabled,
			State:            row.HealthState,
			ServedRequests:   row.ServedRequests,
			CooldownUntil:    optionalTime(row.CooldownUntil),
			LimitsObservedAt: optionalTime(row.LimitsObservedAt),
		}
		if row.OwnerName != "" {
			owner := row.OwnerName
			seat.OwnerName = &owner
		}
		if seat.ClaudeLimitHeaders, err = limitHeaders(row.UnifiedLimitHeaders); err != nil {
			return nil, err
		}
		if seat.CodexLimitHeaders, err = limitHeaders(row.CodexLimitHeaders); err != nil {
			return nil, err
		}
		seats = append(seats, seat)
	}
	return seats, nil
}

func optionalTime(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	t := value.Time
	return &t
}

func limitHeaders(raw []byte) (map[string]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var headers map[string]string
	if err := json.Unmarshal(raw, &headers); err != nil {
		return nil, fmt.Errorf("decode limit headers: %w", err)
	}
	return headers, nil
}

func (r *TelemetryRepo) GetTelemetryTimeseriesAll(ctx context.Context, from, to time.Time, granularity string) ([]proxy.TelemetryBucket, error) {
	q := dbbudget.Queries(r.tx)
	fromTs := pgtype.Timestamptz{Time: from, Valid: true}
	toTs := pgtype.Timestamptz{Time: to, Valid: true}

	return selectTimeseriesGranularity(granularity,
		func() ([]proxy.TelemetryBucket, error) {
			rows, err := q.GetTelemetryTimeseriesWeeklyAll(ctx, sqlc.GetTelemetryTimeseriesWeeklyAllParams{
				FromTime: fromTs,
				ToTime:   toTs,
			})
			if err != nil {
				return nil, err
			}
			return mapRows(rows, telemetryBucketFromWeeklyAllRow), nil
		},
		func() ([]proxy.TelemetryBucket, error) {
			rows, err := q.GetTelemetryTimeseriesDailyAll(ctx, sqlc.GetTelemetryTimeseriesDailyAllParams{
				FromTime: fromTs,
				ToTime:   toTs,
			})
			if err != nil {
				return nil, err
			}
			return mapRows(rows, telemetryBucketFromDailyAllRow), nil
		},
		func() ([]proxy.TelemetryBucket, error) {
			rows, err := q.GetTelemetryTimeseriesHourlyAll(ctx, sqlc.GetTelemetryTimeseriesHourlyAllParams{
				FromTime: fromTs,
				ToTime:   toTs,
			})
			if err != nil {
				return nil, err
			}
			return mapRows(rows, telemetryBucketFromHourlyAllRow), nil
		},
	)
}

func (r *TelemetryRepo) GetTelemetryModelBreakdown(ctx context.Context, installationID string, from, to time.Time, granularity string) ([]proxy.TelemetryModelBucket, error) {
	id, err := uuid.Parse(installationID)
	if err != nil {
		return nil, err
	}
	q := dbbudget.Queries(r.tx)
	fromTs := pgtype.Timestamptz{Time: from, Valid: true}
	toTs := pgtype.Timestamptz{Time: to, Valid: true}

	return selectModelBreakdownGranularity(granularity,
		func() ([]proxy.TelemetryModelBucket, error) {
			rows, err := q.GetTelemetryModelBreakdownWeekly(ctx, sqlc.GetTelemetryModelBreakdownWeeklyParams{
				InstallationID: id,
				FromTime:       fromTs,
				ToTime:         toTs,
			})
			if err != nil {
				return nil, err
			}
			return mapRows(rows, modelBucketFromWeeklyRow), nil
		},
		func() ([]proxy.TelemetryModelBucket, error) {
			rows, err := q.GetTelemetryModelBreakdownDaily(ctx, sqlc.GetTelemetryModelBreakdownDailyParams{
				InstallationID: id,
				FromTime:       fromTs,
				ToTime:         toTs,
			})
			if err != nil {
				return nil, err
			}
			return mapRows(rows, modelBucketFromDailyRow), nil
		},
		func() ([]proxy.TelemetryModelBucket, error) {
			rows, err := q.GetTelemetryModelBreakdownHourly(ctx, sqlc.GetTelemetryModelBreakdownHourlyParams{
				InstallationID: id,
				FromTime:       fromTs,
				ToTime:         toTs,
			})
			if err != nil {
				return nil, err
			}
			return mapRows(rows, modelBucketFromHourlyRow), nil
		},
	)
}

func (r *TelemetryRepo) GetTelemetryModelBreakdownAll(ctx context.Context, from, to time.Time, granularity string) ([]proxy.TelemetryModelBucket, error) {
	q := dbbudget.Queries(r.tx)
	fromTs := pgtype.Timestamptz{Time: from, Valid: true}
	toTs := pgtype.Timestamptz{Time: to, Valid: true}

	return selectModelBreakdownGranularity(granularity,
		func() ([]proxy.TelemetryModelBucket, error) {
			rows, err := q.GetTelemetryModelBreakdownWeeklyAll(ctx, sqlc.GetTelemetryModelBreakdownWeeklyAllParams{
				FromTime: fromTs,
				ToTime:   toTs,
			})
			if err != nil {
				return nil, err
			}
			return mapRows(rows, modelBucketFromWeeklyAllRow), nil
		},
		func() ([]proxy.TelemetryModelBucket, error) {
			rows, err := q.GetTelemetryModelBreakdownDailyAll(ctx, sqlc.GetTelemetryModelBreakdownDailyAllParams{
				FromTime: fromTs,
				ToTime:   toTs,
			})
			if err != nil {
				return nil, err
			}
			return mapRows(rows, modelBucketFromDailyAllRow), nil
		},
		func() ([]proxy.TelemetryModelBucket, error) {
			rows, err := q.GetTelemetryModelBreakdownHourlyAll(ctx, sqlc.GetTelemetryModelBreakdownHourlyAllParams{
				FromTime: fromTs,
				ToTime:   toTs,
			})
			if err != nil {
				return nil, err
			}
			return mapRows(rows, modelBucketFromHourlyAllRow), nil
		},
	)
}

func selectModelBreakdownGranularity(
	granularity string,
	weekly, daily, hourly func() ([]proxy.TelemetryModelBucket, error),
) ([]proxy.TelemetryModelBucket, error) {
	switch granularity {
	case "week":
		return weekly()
	case "day":
		return daily()
	default:
		return hourly()
	}
}

func selectTimeseriesGranularity(
	granularity string,
	weekly, daily, hourly func() ([]proxy.TelemetryBucket, error),
) ([]proxy.TelemetryBucket, error) {
	switch granularity {
	case "week":
		return weekly()
	case "day":
		return daily()
	default:
		return hourly()
	}
}

func mapRows[T, U any](rows []T, convert func(T) U) []U {
	out := make([]U, 0, len(rows))
	for _, row := range rows {
		out = append(out, convert(row))
	}
	return out
}

func telemetryBucketFromWeeklyRow(row sqlc.GetTelemetryTimeseriesWeeklyRow) proxy.TelemetryBucket {
	return proxy.TelemetryBucket{
		Bucket:           row.Bucket.Time,
		RequestedCostUSD: microsToUSD(row.RequestedCostUsd),
		ActualCostUSD:    microsToUSD(row.ActualCostUsd),
	}
}

func telemetryBucketFromDailyRow(row sqlc.GetTelemetryTimeseriesDailyRow) proxy.TelemetryBucket {
	return proxy.TelemetryBucket{
		Bucket:           row.Bucket.Time,
		RequestedCostUSD: microsToUSD(row.RequestedCostUsd),
		ActualCostUSD:    microsToUSD(row.ActualCostUsd),
	}
}

func telemetryBucketFromHourlyRow(row sqlc.GetTelemetryTimeseriesHourlyRow) proxy.TelemetryBucket {
	return proxy.TelemetryBucket{
		Bucket:           row.Bucket.Time,
		RequestedCostUSD: microsToUSD(row.RequestedCostUsd),
		ActualCostUSD:    microsToUSD(row.ActualCostUsd),
	}
}

func telemetryBucketFromWeeklyAllRow(row sqlc.GetTelemetryTimeseriesWeeklyAllRow) proxy.TelemetryBucket {
	return proxy.TelemetryBucket{
		Bucket:           row.Bucket.Time,
		RequestedCostUSD: microsToUSD(row.RequestedCostUsd),
		ActualCostUSD:    microsToUSD(row.ActualCostUsd),
	}
}

func telemetryBucketFromDailyAllRow(row sqlc.GetTelemetryTimeseriesDailyAllRow) proxy.TelemetryBucket {
	return proxy.TelemetryBucket{
		Bucket:           row.Bucket.Time,
		RequestedCostUSD: microsToUSD(row.RequestedCostUsd),
		ActualCostUSD:    microsToUSD(row.ActualCostUsd),
	}
}

func telemetryBucketFromHourlyAllRow(row sqlc.GetTelemetryTimeseriesHourlyAllRow) proxy.TelemetryBucket {
	return proxy.TelemetryBucket{
		Bucket:           row.Bucket.Time,
		RequestedCostUSD: microsToUSD(row.RequestedCostUsd),
		ActualCostUSD:    microsToUSD(row.ActualCostUsd),
	}
}

func (r *TelemetryRepo) GetTelemetryRows(ctx context.Context, installationID string, from, to time.Time, limit int32) ([]proxy.TelemetryRow, error) {
	id, err := uuid.Parse(installationID)
	if err != nil {
		return nil, err
	}
	q := dbbudget.Queries(r.tx)
	rows, err := q.GetTelemetryRows(ctx, sqlc.GetTelemetryRowsParams{
		InstallationID: id,
		FromTime:       pgtype.Timestamptz{Time: from, Valid: true},
		ToTime:         pgtype.Timestamptz{Time: to, Valid: true},
		RowLimit:       limit,
	})
	if err != nil {
		return nil, err
	}
	out := mapRows(rows, telemetryRowFromRowsRow)
	return out, nil
}

func (r *TelemetryRepo) GetTelemetryRowsAll(ctx context.Context, from, to time.Time, limit int32) ([]proxy.TelemetryRow, error) {
	q := dbbudget.Queries(r.tx)
	rows, err := q.GetTelemetryRowsAll(ctx, sqlc.GetTelemetryRowsAllParams{
		FromTime: pgtype.Timestamptz{Time: from, Valid: true},
		ToTime:   pgtype.Timestamptz{Time: to, Valid: true},
		RowLimit: limit,
	})
	if err != nil {
		return nil, err
	}
	out := mapRows(rows, telemetryRowFromRowsAllRow)
	return out, nil
}

func (r *TelemetryRepo) GetSessionCost(ctx context.Context, installationID, sessionID string) (proxy.SessionCost, error) {
	id, err := uuid.Parse(installationID)
	if err != nil {
		return proxy.SessionCost{}, err
	}
	q := dbbudget.Queries(r.tx)
	row, err := q.GetSessionCost(ctx, sqlc.GetSessionCostParams{
		InstallationID: id,
		SessionID:      sessionID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return proxy.SessionCost{}, proxy.ErrSessionCostNotFound
	}
	if err != nil {
		return proxy.SessionCost{}, err
	}
	if row.SessionID == nil {
		return proxy.SessionCost{}, proxy.ErrSessionCostNotFound
	}
	return proxy.SessionCost{
		SessionID:              *row.SessionID,
		RequestCount:           row.RequestCount,
		ActualCostUSDMicros:    row.ActualInputCostUsd + row.ActualOutputCostUsd,
		RequestedCostUSDMicros: row.RequestedInputCostUsd + row.RequestedOutputCostUsd,
		InputTokens:            row.InputTokens,
		OutputTokens:           row.OutputTokens,
		CacheCreationTokens:    row.CacheCreationTokens,
		CacheReadTokens:        row.CacheReadTokens,
		LastRecordedAt:         row.LastRecordedAt.Time,
		ContextSnapshot:        proxy.ParseContextSnapshot(row.ContextSnapshot),
		BlindExperiment:        row.BlindExperiment,
	}, nil
}

func telemetryRowFromRow(
	ts time.Time,
	requestID string,
	requestedModel *string,
	decisionModel *string,
	decisionProvider *string,
	decisionReason *string,
	stickyHit *bool,
	inputTokens *int32,
	outputTokens *int32,
	cacheCreationTokens *int32,
	cacheReadTokens *int32,
	requestedCostUsdMicros int64,
	actualCostUsdMicros int64,
	totalLatencyMs *int64,
	upstreamStatusCode *int32,
	routerUserID string,
	clientApp *string,
	turnType string,
	userEmail *string,
) proxy.TelemetryRow {
	deref := func(s *string) string {
		if s == nil {
			return ""
		}
		return *s
	}
	derefBool := func(b *bool) bool { return b != nil && *b }
	derefInt32 := func(i *int32) int32 {
		if i == nil {
			return 0
		}
		return *i
	}
	derefInt64 := func(i *int64) int64 {
		if i == nil {
			return 0
		}
		return *i
	}
	return proxy.TelemetryRow{
		Timestamp:           ts,
		RequestID:           requestID,
		RequestedModel:      deref(requestedModel),
		DecisionModel:       deref(decisionModel),
		DecisionProvider:    deref(decisionProvider),
		DecisionReason:      deref(decisionReason),
		StickyHit:           derefBool(stickyHit),
		InputTokens:         derefInt32(inputTokens),
		OutputTokens:        derefInt32(outputTokens),
		CacheCreationTokens: cacheCreationTokens,
		CacheReadTokens:     cacheReadTokens,
		RequestedCostUSD:    microsToUSD(requestedCostUsdMicros),
		ActualCostUSD:       microsToUSD(actualCostUsdMicros),
		TotalLatencyMs:      derefInt64(totalLatencyMs),
		UpstreamStatusCode:  derefInt32(upstreamStatusCode),
		RouterUserID:        routerUserID,
		ClientApp:           deref(clientApp),
		TurnType:            turnType,
		UserEmail:           deref(userEmail),
	}
}

func telemetryRowFromRowsRow(row sqlc.GetTelemetryRowsRow) proxy.TelemetryRow {
	return telemetryRowFromRow(
		row.Timestamp.Time,
		row.RequestID,
		row.RequestedModel,
		row.DecisionModel,
		row.DecisionProvider,
		row.DecisionReason,
		row.StickyHit,
		row.InputTokens,
		row.OutputTokens,
		row.CacheCreationTokens,
		row.CacheReadTokens,
		row.RequestedCostUsd,
		row.ActualCostUsd,
		row.TotalLatencyMs,
		row.UpstreamStatusCode,
		uuidString(row.RouterUserID),
		row.ClientApp,
		row.TurnType,
		row.UserEmail,
	)
}

func telemetryRowFromRowsAllRow(row sqlc.GetTelemetryRowsAllRow) proxy.TelemetryRow {
	return telemetryRowFromRow(
		row.Timestamp.Time,
		row.RequestID,
		row.RequestedModel,
		row.DecisionModel,
		row.DecisionProvider,
		row.DecisionReason,
		row.StickyHit,
		row.InputTokens,
		row.OutputTokens,
		row.CacheCreationTokens,
		row.CacheReadTokens,
		row.RequestedCostUsd,
		row.ActualCostUsd,
		row.TotalLatencyMs,
		row.UpstreamStatusCode,
		uuidString(row.RouterUserID),
		row.ClientApp,
		row.TurnType,
		row.UserEmail,
	)
}

func modelBucketFromWeeklyRow(row sqlc.GetTelemetryModelBreakdownWeeklyRow) proxy.TelemetryModelBucket {
	return proxy.TelemetryModelBucket{
		Bucket:        row.Bucket.Time,
		DecisionModel: row.DecisionModel,
		RequestCount:  row.RequestCount,
		TotalTokens:   row.TotalTokens,
		ActualCostUSD: microsToUSD(row.ActualCostUsd),
	}
}

func modelBucketFromDailyRow(row sqlc.GetTelemetryModelBreakdownDailyRow) proxy.TelemetryModelBucket {
	return proxy.TelemetryModelBucket{
		Bucket:        row.Bucket.Time,
		DecisionModel: row.DecisionModel,
		RequestCount:  row.RequestCount,
		TotalTokens:   row.TotalTokens,
		ActualCostUSD: microsToUSD(row.ActualCostUsd),
	}
}

func modelBucketFromHourlyRow(row sqlc.GetTelemetryModelBreakdownHourlyRow) proxy.TelemetryModelBucket {
	return proxy.TelemetryModelBucket{
		Bucket:        row.Bucket.Time,
		DecisionModel: row.DecisionModel,
		RequestCount:  row.RequestCount,
		TotalTokens:   row.TotalTokens,
		ActualCostUSD: microsToUSD(row.ActualCostUsd),
	}
}

func modelBucketFromWeeklyAllRow(row sqlc.GetTelemetryModelBreakdownWeeklyAllRow) proxy.TelemetryModelBucket {
	return proxy.TelemetryModelBucket{
		Bucket:        row.Bucket.Time,
		DecisionModel: row.DecisionModel,
		RequestCount:  row.RequestCount,
		TotalTokens:   row.TotalTokens,
		ActualCostUSD: microsToUSD(row.ActualCostUsd),
	}
}

func modelBucketFromDailyAllRow(row sqlc.GetTelemetryModelBreakdownDailyAllRow) proxy.TelemetryModelBucket {
	return proxy.TelemetryModelBucket{
		Bucket:        row.Bucket.Time,
		DecisionModel: row.DecisionModel,
		RequestCount:  row.RequestCount,
		TotalTokens:   row.TotalTokens,
		ActualCostUSD: microsToUSD(row.ActualCostUsd),
	}
}

func modelBucketFromHourlyAllRow(row sqlc.GetTelemetryModelBreakdownHourlyAllRow) proxy.TelemetryModelBucket {
	return proxy.TelemetryModelBucket{
		Bucket:        row.Bucket.Time,
		DecisionModel: row.DecisionModel,
		RequestCount:  row.RequestCount,
		TotalTokens:   row.TotalTokens,
		ActualCostUSD: microsToUSD(row.ActualCostUsd),
	}
}

func (r *TelemetryRepo) GetTelemetryBySessionSequence(ctx context.Context, installationID uuid.UUID, sessionKey []byte, role string, seq int) (proxy.TelemetryTurnResult, error) {
	q := dbbudget.Queries(r.tx)
	if seq > 0 {
		row, err := q.GetTelemetryBySessionAsc(ctx, sqlc.GetTelemetryBySessionAscParams{
			InstallationID: installationID,
			SessionKey:     sessionKey,
			Role:           role,
			TurnOffset:     int32(seq - 1),
		})
		if err != nil {
			return proxy.TelemetryTurnResult{}, err
		}
		return proxy.TelemetryTurnResult{
			RequestID:        row.RequestID,
			DecisionModel:    derefString(row.DecisionModel),
			DecisionProvider: derefString(row.DecisionProvider),
			RouteID:          derefString(row.RouteID),
			Strategy:         derefString(row.Strategy),
			Timestamp:        row.Timestamp.Time,
		}, nil
	}
	row, err := q.GetTelemetryBySessionDesc(ctx, sqlc.GetTelemetryBySessionDescParams{
		InstallationID: installationID,
		SessionKey:     sessionKey,
		Role:           role,
		TurnOffset:     int32(-seq - 1),
	})
	if err != nil {
		return proxy.TelemetryTurnResult{}, err
	}
	return proxy.TelemetryTurnResult{
		RequestID:        row.RequestID,
		DecisionModel:    derefString(row.DecisionModel),
		DecisionProvider: derefString(row.DecisionProvider),
		RouteID:          derefString(row.RouteID),
		Strategy:         derefString(row.Strategy),
		Timestamp:        row.Timestamp.Time,
	}, nil
}

func (r *TelemetryRepo) GetTelemetryDashboardDrillDown(ctx context.Context, installationID string, from, to time.Time) ([]proxy.TelemetryDrillDownRow, error) {
	installation, err := dashboardInstallationFilter(installationID)
	if err != nil {
		return nil, err
	}
	rows, err := dbbudget.Queries(r.tx).GetTelemetryDashboardDrillDown(ctx, sqlc.GetTelemetryDashboardDrillDownParams{
		FromTime:       pgtype.Timestamptz{Time: from, Valid: true},
		ToTime:         pgtype.Timestamptz{Time: to, Valid: true},
		InstallationID: installation,
	})
	if err != nil {
		return nil, err
	}
	return mapRows(rows, func(row sqlc.GetTelemetryDashboardDrillDownRow) proxy.TelemetryDrillDownRow {
		return proxy.TelemetryDrillDownRow{
			TelemetryDashboardFact: proxy.TelemetryDashboardFact{
				Bucket:              row.StartedAt.Time,
				DecisionModel:       row.DecisionModel,
				SubscriptionServed:  row.SubscriptionServed,
				DecisionType:        row.DecisionType,
				AutomaticAlias:      row.AutomaticAlias,
				RequestCount:        row.RequestCount,
				InputTokens:         row.InputTokens,
				OutputTokens:        row.OutputTokens,
				CacheCreationTokens: row.CacheCreationTokens,
				CacheReadTokens:     row.CacheReadTokens,
				ActualCostUSD:       microsToUSD(row.ActualCostUsd),
			},
			PersonID:       row.PersonID,
			Linked:         row.Linked,
			DisplayName:    row.DisplayName,
			Email:          row.Email,
			SessionID:      row.SessionID,
			ClientApp:      row.ClientApp,
			RequestedModel: row.RequestedModel,
			Providers:      row.Providers,
			StartedAt:      row.StartedAt.Time,
			FinishedAt:     row.FinishedAt.Time,
		}
	}), nil
}

func (r *TelemetryRepo) GetTelemetryDashboardIndividualFacts(ctx context.Context, installationID string, from, to time.Time, granularity string) ([]proxy.TelemetryIndividualFact, error) {
	installation, err := dashboardInstallationFilter(installationID)
	if err != nil {
		return nil, err
	}
	rows, err := dbbudget.Queries(r.tx).GetTelemetryDashboardIndividualFacts(ctx, sqlc.GetTelemetryDashboardIndividualFactsParams{
		Granularity:    granularity,
		FromTime:       pgtype.Timestamptz{Time: from, Valid: true},
		ToTime:         pgtype.Timestamptz{Time: to, Valid: true},
		InstallationID: installation,
	})
	if err != nil {
		return nil, err
	}
	return mapRows(rows, func(row sqlc.GetTelemetryDashboardIndividualFactsRow) proxy.TelemetryIndividualFact {
		return proxy.TelemetryIndividualFact{
			TelemetryDashboardFact: proxy.TelemetryDashboardFact{
				Bucket:              row.Bucket.Time,
				DecisionModel:       row.DecisionModel,
				SubscriptionServed:  row.SubscriptionServed,
				DecisionType:        row.DecisionType,
				AutomaticAlias:      row.AutomaticAlias,
				RequestCount:        row.RequestCount,
				InputTokens:         row.InputTokens,
				OutputTokens:        row.OutputTokens,
				CacheCreationTokens: row.CacheCreationTokens,
				CacheReadTokens:     row.CacheReadTokens,
				ActualCostUSD:       microsToUSD(row.ActualCostUsd),
			},
			PersonID:    row.PersonID,
			Linked:      row.Linked,
			DisplayName: row.DisplayName,
			Email:       row.Email,
		}
	}), nil
}
