package candidates

import (
	"strings"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/sessionpin"
)

func MergeSessionStrikes(a, b []string) []string {
	if len(a) == 0 {
		return b
	}
	if len(b) == 0 {
		return a
	}
	seen := make(map[string]struct{}, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, p := range a {
		if _, ok := seen[p]; !ok {
			seen[p] = struct{}{}
			out = append(out, p)
		}
	}
	for _, p := range b {
		if _, ok := seen[p]; !ok {
			seen[p] = struct{}{}
			out = append(out, p)
		}
	}
	return out
}

func PinServesImages(pin sessionpin.Pin, req router.Request) bool {
	return !req.HasImages || catalog.AcceptsImages(pin.Model) && catalog.AcceptsImageCount(pin.Model, req.ImageCount, req.EnabledProviders)
}

func ForcedPinServesImages(pin sessionpin.Pin, req router.Request) bool {
	return !req.HasImages || catalog.AcceptsImages(pin.Model)
}

func ExcludingModel(excluded map[string]struct{}, model string) map[string]struct{} {
	if _, ok := excluded[model]; ok {
		return excluded
	}
	out := make(map[string]struct{}, len(excluded)+1)
	for k := range excluded {
		out[k] = struct{}{}
	}
	out[model] = struct{}{}
	return out
}

func PinEligible(pin sessionpin.Pin, req router.Request) bool {
	if pin.Model == "" || pin.Provider == "" {
		return false
	}
	if _, excluded := req.Candidates.ExcludedModels[pin.Model]; excluded {
		return false
	}
	if !PinServesImages(pin, req) {
		return false
	}
	if req.EnabledProviders == nil {
		return true
	}
	_, ok := req.EnabledProviders[pin.Provider]
	return ok
}

func AutomaticPinEligible(pin sessionpin.Pin, req router.Request) bool {
	return PinEligible(pin, req) && !AutomaticallyDisabled(req, pin.Model)
}

func ForcedPinEligible(pin sessionpin.Pin, req router.Request) bool {
	req = ReadmitForcedContextEstimate(req, pin.Model)
	req = ReadmitForcedImageLimit(req, pin.Model)
	return PinEligible(pin, req)
}

func ForcedPinIneligibilityReason(pin sessionpin.Pin, req router.Request) string {
	if req.EnabledProviders != nil {
		if _, enabled := req.EnabledProviders[pin.Provider]; !enabled {
			return "provider_not_enabled"
		}
	}
	if !ForcedPinServesImages(pin, req) {
		return "not_image_capable"
	}
	return "excluded"
}

func MergeExcludedModels(hard, automatic map[string]struct{}) map[string]struct{} {
	if len(automatic) == 0 {
		return hard
	}
	merged := make(map[string]struct{}, len(hard)+len(automatic))
	for model := range hard {
		merged[model] = struct{}{}
	}
	for model := range automatic {
		merged[model] = struct{}{}
	}
	return merged
}

func ReadmitForcedImageLimit(req router.Request, model string) router.Request {
	if _, imageLimitExcluded := req.Candidates.ImageLimitExcludedModels[model]; !imageLimitExcluded {
		return req
	}
	req.Candidates.ExcludedModels = WithoutModels(req.Candidates.ExcludedModels, []string{model})
	req.ImageCount = 0
	return req
}

func ReadmitForcedContextEstimate(req router.Request, model string) router.Request {
	if _, estimateOnly := req.Candidates.ContextWindowExcludedModels[model]; !estimateOnly {
		return req
	}
	req.Candidates.ExcludedModels = WithoutModels(req.Candidates.ExcludedModels, []string{model})
	req.Candidates.SafetyExcludedModels = WithoutModels(req.Candidates.SafetyExcludedModels, []string{model})
	return req
}

func AutomaticallyDisabled(req router.Request, model string) bool {
	_, disabled := req.Candidates.AutomaticExcludedModels[model]
	return disabled
}

func MaxedOutServedModel(pin sessionpin.Pin) string {
	if pin.LastOutputLimitAt.IsZero() || !pin.LastOutputLimitAt.Equal(pin.LastTurnEndedAt) {
		return ""
	}
	model := pin.LastServedModel
	if model == "" {
		model = pin.Model
	}
	return BaseModelOf(model)
}

func BaseModelOf(servedIdentity string) string {
	if idx := strings.LastIndex(servedIdentity, ":"); idx > 0 {
		return servedIdentity[:idx]
	}
	return servedIdentity
}

func ExcludeMaxedModel(req router.Request, model string) router.Request {
	excluded := make(map[string]struct{}, len(req.Candidates.ExcludedModels)+1)
	for k := range req.Candidates.ExcludedModels {
		excluded[k] = struct{}{}
	}
	excluded[model] = struct{}{}
	req.Candidates.ExcludedModels = excluded
	safety := make(map[string]struct{}, len(req.Candidates.SafetyExcludedModels)+1)
	for k := range req.Candidates.SafetyExcludedModels {
		safety[k] = struct{}{}
	}
	safety[model] = struct{}{}
	req.Candidates.SafetyExcludedModels = safety
	return req
}

func ReadmitPinnedModel(req router.Request, model string) router.Request {
	if len(req.Candidates.ExcludedModels) > 0 {
		pruned := make(map[string]struct{}, len(req.Candidates.ExcludedModels)-1)
		for k := range req.Candidates.ExcludedModels {
			if k != model {
				pruned[k] = struct{}{}
			}
		}
		req.Candidates.ExcludedModels = pruned
	}
	delete(req.Candidates.SafetyExcludedModels, model)
	return req
}
