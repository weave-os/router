package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"weave-os/router/internal/inference"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
)

var errObservationPayloadTooLarge = errors.New("observation payload exceeds admission limit")

// WithObservationWorkers injects process-owned best-effort capacity. Without it,
// optional persistence/reporting is disabled; no per-service workers are started.
func (s *Service) WithObservationWorkers(workers *observability.ObservationWorkers) *Service {
	s.observations = workers
	return s
}

// submitObservation snapshots DTOs before returning to the caller. The producer
// checks variable-sized fields first so serialization cannot allocate an inbound
// request-sized buffer. Decoding and any sink serialization run in fixed workers.
func submitObservation[T any](queue *observability.WorkQueue, kind observability.WorkKind, log *slog.Logger, payload T, upperBytes int, timeout time.Duration, persist func(context.Context, T) error) {
	if queue == nil {
		return
	}
	if upperBytes > observability.MaxWorkPayloadBytes {
		queue.Reject(kind, log, errObservationPayloadTooLarge)
		return
	}
	snapshot, err := json.Marshal(payload)
	if err != nil {
		queue.Reject(kind, log, err)
		return
	}
	queue.Submit(kind, snapshot, timeout, log, func(ctx context.Context, body []byte) error {
		var decoded T
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.UseNumber()
		err := decoder.Decode(&decoded)
		if err != nil {
			return err
		}
		return persist(ctx, decoded)
	})
}

// JSON escapes can expand one byte to six. The fixed allowance covers field
// names, scalar values, and nullable scalar pointers in this metadata-only DTO.
func telemetryPayloadBound(p InsertTelemetryParams) int {
	size := 16 * 1024
	for _, text := range []string{
		p.InstallationID,
		p.APIKeyID,
		p.RequestID,
		p.SpanType,
		p.TraceID,
		p.RequestedModel,
		p.DecisionModel,
		p.DecisionProvider,
		p.DecisionReason,
		string(p.BlindExperimentArm),
		string(p.BlindExperimentAssignmentSource),
		p.BlindExperimentSubjectKey,
		p.PinTier,
		p.EmbedInput,
		p.ClusterRouterVersion,
		p.Strategy,
		p.RouteID,
		p.PolicyRouteKey,
		p.PolicyArtifactID,
		p.PolicyArtifactSHA256,
		p.RosterVersion,
		p.ClassifierArtifactID,
		p.ClassifierArtifactSHA256,
		p.ClassifierPredictedLabel,
		p.SelectionPolicyReleaseID,
		p.SelectionPolicySHA256,
		p.SidecarSchemaVersion,
		p.CaptureMode,
		p.DebugRef,
		p.DeviceID,
		p.SessionID,
		p.RouterUserID,
		p.ClientApp,
		p.TurnType,
		p.RolloutID,
		p.Role,
		p.FreshDecisionModel,
		p.CredentialKeyPrefix,
		p.CredentialKeySuffix,
		p.CredentialSource,
		p.PlannerOutcome,
		p.PlannerReason,
		p.PlannerPinModel,
		p.PlannerPinProvider,
		p.PlannerShadowOutcome,
		p.AuthorityShadowOutcome,
		p.AuthorityShadowReason,
		p.AuthorityShadowStayModel,
		p.AuthorityShadowStayProvider,
		p.AuthorityShadowCorrectedOutcome,
		p.SpiralSameFilePathHash,
		p.ClientGitHeadSHA,
		p.ClientGitBranch,
	} {
		size += 6 * len(text)
	}
	size += 16 * len(p.ClusterIDs)
	if len(p.CandidateModels) > observability.MaxWorkPayloadBytes/16 {
		return observability.MaxWorkPayloadBytes + 1
	}
	for _, text := range p.CandidateModels {
		size += 16 + 6*len(text)
		if size > observability.MaxWorkPayloadBytes {
			return size
		}
	}
	size += 16 * len(p.AlphaBreakdown)
	size += 16 * len(p.CandidateScores)
	if len(p.ClassifierClassOrder) > observability.MaxWorkPayloadBytes/16 {
		return observability.MaxWorkPayloadBytes + 1
	}
	for _, text := range p.ClassifierClassOrder {
		size += 16 + 6*len(text)
		if size > observability.MaxWorkPayloadBytes {
			return size
		}
	}
	size += 16 * len(p.ClassifierProbabilities)
	size += 16 * len(p.SelectionTrace)
	size += 16 * len(p.SessionKey)
	size += 16 * len(p.FreshCandidateScores)
	size += 16 * len(p.UnifiedLimitHeaders)
	if len(p.SpiralReasons) > observability.MaxWorkPayloadBytes/16 {
		return observability.MaxWorkPayloadBytes + 1
	}
	for _, text := range p.SpiralReasons {
		size += 16 + 6*len(text)
		if size > observability.MaxWorkPayloadBytes {
			return size
		}
	}
	if len(p.RequestedAllowedModels) > observability.MaxWorkPayloadBytes/16 {
		return observability.MaxWorkPayloadBytes + 1
	}
	for _, text := range p.RequestedAllowedModels {
		size += 16 + 6*len(text)
		if size > observability.MaxWorkPayloadBytes {
			return size
		}
	}
	if p.UpstreamFinishReason != nil {
		size += 6 * len(*p.UpstreamFinishReason)
	}
	if p.StopReason != nil {
		size += 6 * len(*p.StopReason)
	}
	if p.Inference != nil {
		v := p.Inference
		size += 6 * (len(v.Purpose) + len(v.PolicyID) + len(v.RegistryRevision) + len(v.PolicyRevision) + targetPayloadSize(v.PlanTarget) + targetPayloadSize(v.ServedTarget) + len(v.FallbackReason) + len(v.AccountingOutcome))
	}
	return size
}

func policyPayloadBound(payload map[string]any) int {
	size := 16 * 1024
	for _, value := range payload {
		switch v := value.(type) {
		case string:
			size += 6 * len(v)
		case []router.ConversationMessage:
			if len(v) > observability.MaxWorkPayloadBytes/128 {
				return observability.MaxWorkPayloadBytes + 1
			}
			for _, message := range v {
				size += 128 + 6*(len(message.Role)+len(message.Text))
				if len(message.ToolCalls)+len(message.ToolResults) > observability.MaxWorkPayloadBytes/128 {
					return observability.MaxWorkPayloadBytes + 1
				}
				for _, call := range message.ToolCalls {
					size += 128 + 6*(len(call.Name)+len(call.InputJSON))
					if len(call.InputKeys) > observability.MaxWorkPayloadBytes/16 {
						return observability.MaxWorkPayloadBytes + 1
					}
					for _, key := range call.InputKeys {
						size += 16 + 6*len(key)
						if size > observability.MaxWorkPayloadBytes {
							return size
						}
					}
				}
				for _, result := range message.ToolResults {
					size += 128 + 6*(len(result.ToolUseID)+len(result.Text)+len(result.ExitCategory))
				}
				if size > observability.MaxWorkPayloadBytes {
					return size
				}
			}
		}
		if size > observability.MaxWorkPayloadBytes {
			return size
		}
	}
	return size
}

func targetPayloadSize(target inference.Target) int {
	return len(target.ArmID) + len(target.CatalogID) + len(target.Provider) + len(target.UpstreamID) + len(target.Endpoint) + len(target.ModelRevision) + len(target.ReasoningConfigurationSHA256) + len(target.ToolConfigurationSHA256) + len(target.Effort)
}
