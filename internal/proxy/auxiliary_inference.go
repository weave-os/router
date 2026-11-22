package proxy

import (
	"context"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/handover"
	"weave-os/router/internal/router/llmescalation"
	"weave-os/router/internal/telemetry/turnrecord"

	"github.com/google/uuid"
)

const SpanTypeAuxiliaryInference = "router.auxiliary_inference"

const SpanTypePlatformAuxiliaryInference = "router.platform_auxiliary_inference"

const decisionReasonEscalationJudge = "escalation_judge"

const (
	auxSuffixHandoverSummary          = "_summary"
	auxSuffixPrecompactionSummary     = "_precompaction_summary"
	auxSuffixCompactionHandoverSummry = "_compaction_summary"
)

func (s *Service) billAuxiliaryInference(ctx context.Context, requestID, requestIDSuffix, externalID string, usage handover.Usage) {
	if usage.Model == "" || (usage.InputTokens == 0 && usage.OutputTokens == 0) {
		return
	}
	pricing, _ := catalog.PrimaryPriceFor(usage.Model)
	usageMods := catalog.UsageModifiers{CacheCreation1h: usage.CacheCreation1h, InferenceGeo: catalog.InferenceGeo(usage.InferenceGeo)}
	apiKeyID := apiKeyIDFromContext(ctx)
	auxRequestID := requestID + requestIDSuffix

	byokServed := credentials.ByokServedForProvider(ctx, usage.Provider)

	s.fireBilling(ctx, billing.DebitInferenceParams{
		OrganizationID:  externalID,
		RouterRequestID: auxRequestID,
		Model:           usage.Model,
		Provider:        usage.Provider,
		InputTokens:     usage.InputTokens,
		OutputTokens:    usage.OutputTokens,
		CacheCreation:   usage.CacheCreation,
		CacheRead:       usage.CacheRead,
		Pricing:         pricing,
		UsageModifiers:  usageMods,
		HasOverride:     billing.HasOverrideFromContext(ctx) || billing.WeaveCreditsDeniedFromContext(ctx),
		ByokServed:      byokServed,
		APIKeyID:        apiKeyID,
		RouterUserID:    auth.UserIDFrom(ctx),
	})

	installationID := requestcontext.InstallationIDFromContext(ctx)
	if installationID == uuid.Nil {
		return
	}
	clientID := ClientIdentityFrom(ctx)
	inputCost := catalog.EffectiveInputCost(usage.InputTokens, usage.CacheCreation, usage.CacheRead, pricing, usage.Provider, usageMods)
	outputCost := catalog.EffectiveOutputCost(catalog.PromptTokens(usage.InputTokens, usage.CacheCreation, usage.CacheRead, usage.Provider), usage.OutputTokens, pricing, usageMods)

	s.fireTelemetry(ctx, turnrecord.InsertTelemetryParams{
		InstallationID:         installationID.String(),
		APIKeyID:               apiKeyID,
		RequestID:              auxRequestID,
		SpanType:               SpanTypeAuxiliaryInference,
		TraceID:                requestID,
		Timestamp:              s.clock.Now(),
		DecisionModel:          usage.Model,
		DecisionProvider:       usage.Provider,
		RequestedInputCostUSD:  inputCost,
		RequestedOutputCostUSD: outputCost,
		ActualInputCostUSD:     inputCost,
		ActualOutputCostUSD:    outputCost,
		InputTokens:            int32(usage.InputTokens),
		OutputTokens:           int32(usage.OutputTokens),
		CacheCreationTokens:    cacheTokenPtr(usage.CacheCreation),
		CacheCreation1hTokens:  cacheCreation1hPtr(usage.CacheCreation, usage.CacheCreation1h),
		CacheReadTokens:        cacheTokenPtr(usage.CacheRead),
		InferenceGeo:           usage.InferenceGeo,
		DeviceID:               clientID.DeviceID,
		SessionID:              clientID.SessionID,
		RouterUserID:           auth.UserIDFrom(ctx),
		ClientApp:              clientID.TelemetryClientApp(),
		RolloutID:              clientID.RolloutID,
	})
}

func (s *Service) recordEscalationJudgeInference(ctx context.Context, installationID, requestID string, mode llmescalation.Mode, job llmescalation.Job, judgment llmescalation.Judgment, failure llmescalation.FailureCode) {
	succeeded := failure == llmescalation.FailureNone
	if s.turnTelemetry.repo == nil || installationID == "" || (!judgment.Usage.Known && !succeeded) {
		return
	}
	verdict := ""
	if succeeded && judgment.Escalate {
		verdict = "escalate"
	} else if succeeded {
		verdict = "continue"
	}
	inputCost, outputCost := 0.0, 0.0
	if pricing, found := catalog.PriceFor(job.Provider, job.Model); found {
		inputCost = catalog.EffectiveInputCost(judgment.Usage.InputTokens, 0, judgment.Usage.CacheReadTokens, pricing, job.Provider, catalog.UsageModifiers{})
		outputCost = catalog.EffectiveOutputCost(catalog.PromptTokens(judgment.Usage.InputTokens, 0, judgment.Usage.CacheReadTokens, job.Provider), judgment.Usage.OutputTokens, pricing, catalog.UsageModifiers{})
	}
	if judgment.CostSource == llmescalation.CostSourceProviderReported {
		inputCost, outputCost = judgment.CostUSD, 0
	}
	s.fireTelemetry(ctx, turnrecord.InsertTelemetryParams{
		InstallationID:           installationID,
		RequestID:                requestID + "_escalation_judge_" + job.ID,
		SpanType:                 SpanTypePlatformAuxiliaryInference,
		TraceID:                  requestID,
		Timestamp:                s.clock.Now(),
		DecisionModel:            job.Model,
		DecisionProvider:         job.Provider,
		DecisionReason:           decisionReasonEscalationJudge,
		ClassifierArtifactID:     string(mode) + "/" + job.Version,
		ClassifierPredictedLabel: verdict,
		InputTokens:              int32(judgment.Usage.InputTokens),
		OutputTokens:             int32(judgment.Usage.OutputTokens),
		CacheReadTokens:          cacheTokenPtr(judgment.Usage.CacheReadTokens),
		ActualInputCostUSD:       inputCost,
		ActualOutputCostUSD:      outputCost,
		SessionID:                job.Lifetime,
	})
}
