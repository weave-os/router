package proxy

import (
	"context"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/continuity"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/telemetry"
	"weave-os/router/internal/translate"
)

type upstreamTelemetryRow struct {
	selectionAndResponseFields bool
	installationID             string
	facts                      requestcontext.RequestFacts
	feats                      translate.RoutingFeatures
	decision                   router.Decision
	provider                   string
	contextSnapshot            []byte
	routeRes                   *turnLoopResult
	obs                        observationContext
	stickyHit                  bool
	embedInput                 string
	inputTokens                int
	outputTokens               int
	cacheCreation              int
	cacheRead                  int
	usageMods                  catalog.UsageModifiers
	requestedInputCost         float64
	reqPricing                 catalog.Pricing
	actPricing                 catalog.Pricing
	routeMs                    int64
	proxyMs                    int64
	requestStart               time.Time
	crossFormat                bool
	proxyErr                   error
	extractor                  *telemetry.UsageExtractor
	respSummary                translate.ResponseSummary
	nativeRespSummary          bool
	failoverUsed               *bool
	failoverAttempted          *bool
	sessionKey                 []byte
	pinAgeSec                  int64
	inboundLastUser            translate.LastUserMessageInfo
	turnType                   turntype.TurnType
	userPrompt                 *bool
	errorClass                 TurnErrorClass
	failureOwner               FailureOwner
	credentialKeyPrefix        string
	credentialKeySuffix        string
	credentialSource           string
	effortServed               effortResolution
	strategyPorts              continuity.StrategyPorts
	surfaceFields              func(*InsertTelemetryParams)
}

func newUpstreamTelemetryRow(ctx context.Context, clock auth.Clock, row upstreamTelemetryRow) InsertTelemetryParams {
	tel := upstreamTelemetryRowFields(ctx, clock, row)
	if row.selectionAndResponseFields {
		applySelectionAndResponseTelemetry(ctx, &tel, row)
	}
	if row.surfaceFields != nil {
		row.surfaceFields(&tel)
	}
	applyServedGroupTelemetry(row.strategyPorts, ctx, &tel, *row.routeRes, row.decision)
	applyPlannerTelemetry(&tel, *row.routeRes)
	applyEffortTelemetry(&tel, row.effortServed)
	applyAuthorityShadowTelemetry(&tel, *row.routeRes)
	applyBlindExperimentTelemetry(ctx, &tel, row.routeRes)
	applyPolicyPinTelemetry(ctx, &tel, row.decision.Metadata)
	applySubscriberTelemetry(ctx, &tel)
	applyAgreementShadowTelemetry(row.strategyPorts, ctx, &tel, *row.routeRes)
	return tel
}

func upstreamTelemetryRowFields(ctx context.Context, clock auth.Clock, row upstreamTelemetryRow) InsertTelemetryParams {
	facts, decision, obs := row.facts, row.decision, row.obs
	promptTokens := catalog.PromptTokens(row.inputTokens, row.cacheCreation, row.cacheRead, row.provider)
	return InsertTelemetryParams{
		InstallationID:         row.installationID,
		APIKeyID:               apiKeyIDFromContext(ctx),
		RequestID:              facts.RequestID,
		SpanType:               "router.upstream",
		TraceID:                facts.RequestID,
		Timestamp:              row.requestStart,
		RequestedModel:         row.feats.Model,
		DecisionModel:          decision.Model,
		DecisionProvider:       row.provider,
		DecisionReason:         telemetryDecisionReason(ctx, decision.Reason),
		RequestedAllowedModels: requestedAllowedModelsForTelemetry(ctx),
		EstimatedInputTokens:   int32(row.feats.Tokens),
		ContextSnapshot:        row.contextSnapshot,
		StickyHit:              row.stickyHit,
		PinTier:                row.routeRes.PinTier,
		EmbedInput:             row.embedInput,
		InputTokens:            int32(row.inputTokens),
		OutputTokens:           int32(row.outputTokens),
		RequestedInputCostUSD:  row.requestedInputCost,
		RequestedOutputCostUSD: catalog.EffectiveOutputCost(promptTokens, row.outputTokens, row.reqPricing, row.usageMods),
		ActualInputCostUSD:     catalog.EffectiveInputCost(row.inputTokens, row.cacheCreation, row.cacheRead, row.actPricing, row.provider, row.usageMods),
		ActualOutputCostUSD:    catalog.EffectiveOutputCost(promptTokens, row.outputTokens, row.actPricing, row.usageMods),
		RouteLatencyMs:         row.routeMs,
		UpstreamLatencyMs:      row.proxyMs,
		TotalLatencyMs:         clock.Now().Sub(row.requestStart).Milliseconds(),
		CrossFormat:            row.crossFormat,
		UpstreamStatusCode:     int32(upstreamStatus(row.proxyErr)),
		CandidateModels:        obs.CandidateModels,
		ChosenScore:            obs.ChosenScore,
		CandidateScores:        obs.CandidateScores,
		Propensity:             obs.Propensity,
		Strategy:               obs.Strategy,
		RouteID:                obs.RouteID,
		PolicyRouteKey:         obs.PolicyRouteKey,
		PolicyArtifactID:       obs.PolicyArtifactID,
		PolicyArtifactSHA256:   obs.PolicyArtifactSHA256,
		RosterVersion:          obs.RosterVersion,
		SidecarSchemaVersion:   obs.SidecarSchemaVersion,
		TrainingAllowed:        obs.TrainingAllowed,
		CaptureMode:            obs.CaptureMode,
		DebugRef:               obs.DebugRef,
		TTFTMs:                 obs.TTFTMs,
		CacheCreationTokens:    cacheTokenPtr(row.cacheCreation),
		CacheReadTokens:        cacheTokenPtr(row.cacheRead),
		ReasoningTokens:        cacheTokenPtr(row.extractor.ReasoningTokens()),
		DeviceID:               facts.Client.DeviceID,
		SessionID:              facts.Client.SessionID,
		RouterUserID:           auth.UserIDFrom(ctx),
		ClientApp:              facts.Client.TelemetryClientApp(),
		TurnType:               string(row.routeRes.TurnType),
		RolloutID:              obs.RolloutID,
		FailoverUsed:           row.failoverUsed,
		FailoverAttempted:      row.failoverAttempted,
		SessionKey:             row.sessionKey,
		Role:                   row.routeRes.PinRole,
		FreshDecisionModel:     obs.FreshDecisionModel,
		FreshCandidateScores:   obs.FreshCandidateScores,
		PinAgeSec:              int64PtrIf(row.stickyHit && row.pinAgeSec > 0, row.pinAgeSec),
		ToolResultBytes:        toolResultBytesPtr(row.inboundLastUser, row.turnType),
		UserPrompt:             row.userPrompt,
		ErrorClass:             row.errorClass,
		FailureOwner:           row.failureOwner,
		CredentialKeyPrefix:    row.credentialKeyPrefix,
		CredentialKeySuffix:    row.credentialKeySuffix,
		CredentialSource:       row.credentialSource,
	}
}

func applySelectionAndResponseTelemetry(ctx context.Context, tel *InsertTelemetryParams, row upstreamTelemetryRow) {
	obs, resp := row.obs, row.respSummary
	tel.ClassifierArtifactID = obs.ClassifierArtifactID
	tel.ClassifierArtifactSHA256 = obs.ClassifierArtifactSHA256
	tel.ClassifierPredictedLabel = obs.ClassifierPredictedLabel
	tel.ClassifierClassOrder = obs.ClassifierClassOrder
	tel.ClassifierProbabilities = obs.ClassifierProbabilities
	tel.SelectionPolicyReleaseID = obs.SelectionPolicyReleaseID
	tel.SelectionPolicySHA256 = obs.SelectionPolicySHA256
	tel.SelectionHeadGeneration = int64PtrIf(obs.SelectionHeadGeneration > 0, obs.SelectionHeadGeneration)
	tel.SelectionTrace = obs.SelectionTrace
	tel.CacheCreation1hTokens = cacheCreation1hPtr(row.cacheCreation, row.usageMods.CacheCreation1h)
	tel.Speed = string(row.extractor.Speed())
	tel.InferenceGeo = string(row.extractor.InferenceGeo())
	tel.UpstreamFinishReason = stringPtrOrEmpty(resp.UpstreamFinishReason)
	tel.StopReason = stringPtrOrEmpty(resp.StopReason)
	tel.ToolUseBlocks = int32PtrIfKnown(int32(resp.ToolUseBlocks), resp.StopReason != "")
	tel.InvalidToolArgsBlocks = int32PtrIfKnown(int32(resp.InvalidToolArgsBlocks), resp.StopReason != "" && !row.nativeRespSummary)
	tel.UnifiedLimitHeaders = unifiedLimitHeadersJSON(ctx)
	tel.CodexLimitHeaders = codexLimitHeadersJSON(ctx)
}

func applyTelemetrySettlement(tel *InsertTelemetryParams, billable bool, settlement subscriberSettlementState) {
	if billable {
		applySubscriberSettlementTelemetry(settlement, tel)
	} else {
		markSubscriberTelemetryUnsettled(tel)
	}
}
