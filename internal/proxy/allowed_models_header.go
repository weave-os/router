package proxy

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"
)

// AllowedModelsHeader restricts routing for one request to a comma-separated
// subset of catalog IDs or aliases. The subset is intersected with the
// installation's allowed_models and never widens it.
const AllowedModelsHeader = "x-weave-allowed-models"

// AllowlistOverrideReasonPrefix is prepended to the persisted decision reason
// when a request-level allowlist narrowed the candidate pool.
const AllowlistOverrideReasonPrefix = "allowlist_override:"

// RequestAllowedModelsContextKey carries the RequestAllowedModels parsed from
// AllowedModelsHeader by the middleware.
type RequestAllowedModelsContextKey struct{}

// RequestAllowedModels is the resolved request-level allowlist. Requested is
// every canonical model the caller named; Effective is Requested intersected
// with the installation allowlist and is never empty.
type RequestAllowedModels struct {
	Requested []string
	Effective []string
}

// AllowedModelsHeaderError is a client-input problem with AllowedModelsHeader
// (unknown alias or an empty intersection with the installation allowlist).
type AllowedModelsHeaderError struct {
	Reason string
}

// Error implements error.
func (e *AllowedModelsHeaderError) Error() string {
	return fmt.Sprintf("invalid %s header: %s", AllowedModelsHeader, e.Reason)
}

// ParseAllowedModelsHeader resolves a comma-separated list of catalog IDs or
// aliases and intersects it with installationAllowed (an empty installation
// allowlist means unrestricted). Unknown entries and an empty intersection are
// rejected instead of falling back to the full roster.
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

// modelInRequestSubset reports whether model may be served under the
// request-level allowlist; true when no subset was sent.
func modelInRequestSubset(ctx context.Context, model string) bool {
	subset := requestAllowedModelSet(ctx)
	if subset == nil {
		return true
	}
	_, ok := subset[model]
	return ok
}

// requestedAllowedModelsForTelemetry returns the caller's canonical request
// allowlist, sorted, or nil when the header was absent.
func requestedAllowedModelsForTelemetry(ctx context.Context) []string {
	ram, ok := requestAllowedModelsFromContext(ctx)
	if !ok {
		return nil
	}
	out := append([]string(nil), ram.Requested...)
	sort.Strings(out)
	return out
}

// telemetryDecisionReason prefixes reason with AllowlistOverrideReasonPrefix
// when a request allowlist narrowed the pool. A user force is a strict pin
// that ignores the allowlist, so its reason is left untouched.
func telemetryDecisionReason(ctx context.Context, reason string) string {
	if _, ok := requestAllowedModelsFromContext(ctx); !ok || isUserForcedReason(reason) {
		return reason
	}
	return AllowlistOverrideReasonPrefix + reason
}

// readmitForcedModel lifts a forced model's exclusion when it stems only from
// the request allowlist: the force is a strict pin and outranks the header.
// Callers must already have validated the model against installation policy
// (forcedModelBinding); a policy or context-window exclusion is never lifted.
func (s *Service) readmitForcedModel(
	ctx context.Context,
	req router.Request,
	env *translate.RequestEnvelope,
	feats translate.RoutingFeatures,
	pin sessionpin.Pin,
) map[string]struct{} {
	excluded := req.ExcludedModels
	subset := requestAllowedModelSet(ctx)
	if subset == nil {
		return excluded
	}
	if _, inSubset := subset[pin.Model]; inSubset {
		return excluded
	}
	if _, ok := excluded[pin.Model]; !ok {
		return excluded
	}
	if _, ok := s.policyExcludedModels(ctx)[pin.Model]; ok {
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

// requestAllowedModelsPresent reports whether the request carries an
// x-weave-allowed-models subset. Such requests bypass the semantic cache: the
// subset is not part of the cache key, so a hit could serve a body produced by
// a model outside it.
func requestAllowedModelsPresent(ctx context.Context) bool {
	_, ok := requestAllowedModelsFromContext(ctx)
	return ok
}
