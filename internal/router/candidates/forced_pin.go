package candidates

import (
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/sessionpin"
)

type ForcedPinVerdict struct {
	Serve               bool
	PastContextEstimate bool
	PastImageCap        bool
	DropReason          string
	TierFloor           catalog.Tier
	ExcludeModel        bool
}

func JudgeForceModelPin(pin sessionpin.Pin, req router.Request) ForcedPinVerdict {
	_, excluded := req.Candidates.ExcludedModels[pin.Model]
	imageCapable := ForcedPinServesImages(pin, req)
	verdict := ForcedPinVerdict{TierFloor: catalog.TierUnknown}
	if _, estimateOnly := req.Candidates.ContextWindowExcludedModels[pin.Model]; excluded && estimateOnly {
		verdict.PastContextEstimate = true
		excluded = false
	}
	if _, imageLimitExcluded := req.Candidates.ImageLimitExcludedModels[pin.Model]; imageLimitExcluded {
		verdict.PastImageCap = true
		excluded = false
	}
	if !excluded && pinProviderEnabled(pin, req) && imageCapable {
		verdict.Serve = true
		return verdict
	}
	verdict.DropReason = ForcedPinIneligibilityReason(pin, req)
	return verdict.withDropEffects(pin, excluded, imageCapable)
}

func JudgeUserForcedPin(pin sessionpin.Pin, req router.Request) ForcedPinVerdict {
	_, excluded := req.Candidates.ExcludedModels[pin.Model]
	providerEnabled := pinProviderEnabled(pin, req)
	imageCapable := PinServesImages(pin, req)
	verdict := ForcedPinVerdict{TierFloor: catalog.TierUnknown}
	if !excluded && providerEnabled && imageCapable {
		verdict.Serve = true
		return verdict
	}
	switch {
	case !providerEnabled:
		verdict.DropReason = "provider_not_enabled"
	case !imageCapable:
		verdict.DropReason = "not_image_capable"
	default:
		verdict.DropReason = "excluded"
	}
	return verdict.withDropEffects(pin, excluded, imageCapable)
}

func (v ForcedPinVerdict) withDropEffects(pin sessionpin.Pin, excluded, imageCapable bool) ForcedPinVerdict {
	if excluded || !imageCapable {
		v.TierFloor = catalog.TierFor(pin.Model)
	}
	v.ExcludeModel = !imageCapable
	return v
}

func pinProviderEnabled(pin sessionpin.Pin, req router.Request) bool {
	_, enabled := req.EnabledProviders[pin.Provider]
	return req.EnabledProviders == nil || enabled
}
