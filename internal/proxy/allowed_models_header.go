package proxy

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"
)

const AllowedModelsHeader = "x-weave-allowed-models"

const AllowlistOverrideReasonPrefix = "allowlist_override:"

type RequestAllowedModelsContextKey struct{}

type RequestAllowedModels struct {
	Requested []string
	Effective []string
}

type AllowedModelsHeaderError struct {
	Reason string
}

func (e *AllowedModelsHeaderError) Error() string {
	return fmt.Sprintf("invalid %s header: %s", AllowedModelsHeader, e.Reason)
}

func ParseAllowedModelsHeader(raw string, installationAllowed []string) (RequestAllowedModels, error) {
	requested := make([]string, 0, 4)
	seen := make(map[string]struct{}, 4)
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		canonical, _, known := resolveForceModel(part)
		if !known {
			return RequestAllowedModels{}, &AllowedModelsHeaderError{Reason: fmt.Sprintf("%q is not a known model", part)}
		}
		if _, dup := seen[canonical]; dup {
			continue
		}
		seen[canonical] = struct{}{}
		requested = append(requested, canonical)
	}
	if len(requested) == 0 {
		return RequestAllowedModels{}, &AllowedModelsHeaderError{Reason: "no models listed"}
	}
	effective := requested
	if len(installationAllowed) > 0 {
		allowed := make(map[string]struct{}, len(installationAllowed))
		for _, m := range installationAllowed {
			allowed[m] = struct{}{}
		}
		effective = make([]string, 0, len(requested))
		for _, m := range requested {
			if _, ok := allowed[m]; ok {
				effective = append(effective, m)
			}
		}
		if len(effective) == 0 {
			return RequestAllowedModels{}, &AllowedModelsHeaderError{
				Reason: fmt.Sprintf("none of %s is on this organization's allowed-model list", strings.Join(requested, ", ")),
			}
		}
	}
	return RequestAllowedModels{Requested: requested, Effective: effective}, nil
}

func requestAllowedModelsFromContext(ctx context.Context) (RequestAllowedModels, bool) {
	v, ok := ctx.Value(RequestAllowedModelsContextKey{}).(RequestAllowedModels)
	return v, ok && len(v.Effective) > 0
}

func requestAllowedModelSet(ctx context.Context) map[string]struct{} {
	ram, ok := requestAllowedModelsFromContext(ctx)
	if !ok {
		return nil
	}
	out := make(map[string]struct{}, len(ram.Effective))
	for _, m := range ram.Effective {
		out[m] = struct{}{}
	}
	return out
}

func modelInRequestSubset(ctx context.Context, model string) bool {
	subset := requestAllowedModelSet(ctx)
	if subset == nil {
		return true
	}
	_, ok := subset[model]
	return ok
}

func requestedAllowedModelsForTelemetry(ctx context.Context) []string {
	ram, ok := requestAllowedModelsFromContext(ctx)
	if !ok {
		return nil
	}
	out := append([]string(nil), ram.Requested...)
	sort.Strings(out)
	return out
}

func telemetryDecisionReason(ctx context.Context, reason string) string {
	if _, ok := requestAllowedModelsFromContext(ctx); !ok || isUserForcedReason(reason) {
		return reason
	}
	return AllowlistOverrideReasonPrefix + reason
}

func (s *Service) readmitForcedModel(
	ctx context.Context,
	req router.Request,
	env *translate.RequestEnvelope,
	feats translate.RoutingFeatures,
	pin sessionpin.Pin,
) map[string]struct{} {
	excluded := req.ExcludedModels
	bypassesAllowlist := len(s.gatewayProvidersForRequest(ctx)) > 0 && !modelPermittedByAllowlist(ctx, pin.Model)
	if modelInRequestSubset(ctx, pin.Model) && !bypassesAllowlist {
		return excluded
	}
	if _, ok := excluded[pin.Model]; !ok {
		return excluded
	}
	if _, ok := s.forcePolicyExcludedModels(ctx)[pin.Model]; ok {
		return excluded
	}
	if advisorRejectsModel(env, pin.Model) {
		return excluded
	}
	if catalog.RequiresSignedToolHistory(pin.Model) && env.HasUnsignedToolCallHistory() {
		return excluded
	}
	outputReserve := contextWindowOutputReserve
	if feats.MaxTokens > outputReserve {
		outputReserve = feats.MaxTokens
	}
	estimate := env.ContextOverflowTokenEstimate()
	if modelStripsAnthropicSignatures(pin.Model) {
		estimate -= env.SignatureTokenSavings()
	}
	if estimate+outputReserve > contextWindowForRequest(pin.Model, pin.Provider) {
		return excluded
	}
	out := make(map[string]struct{}, len(excluded))
	for m := range excluded {
		out[m] = struct{}{}
	}
	delete(out, pin.Model)
	return out
}

func requestAllowedModelsPresent(ctx context.Context) bool {
	_, ok := requestAllowedModelsFromContext(ctx)
	return ok
}
