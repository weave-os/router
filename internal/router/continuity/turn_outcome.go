package continuity

import (
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/candidates"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/planner"
	"weave-os/router/internal/router/sessionpin"
)

type Reanchor int

const (
	ReanchorNone Reanchor = iota
	ReanchorSkipMaxedOut
	ReanchorSkipTextOnly
	ReanchorServe
)

func JudgeReanchor(pin sessionpin.Pin, fresh router.Decision, req router.Request, availableModels map[string]struct{}) Reanchor {
	pinTier, freshTier := catalog.TierFor(pin.Model), catalog.TierFor(fresh.Model)
	if pinTier == catalog.TierUnknown || freshTier == catalog.TierUnknown || freshTier > pinTier {
		return ReanchorNone
	}
	if _, excluded := req.Candidates.ExcludedModels[pin.Model]; excluded || candidates.AutomaticallyDisabled(req, pin.Model) {
		return ReanchorNone
	}
	if _, available := availableModels[pin.Model]; !available {
		return ReanchorNone
	}
	if _, providerEnabled := req.EnabledProviders[pin.Provider]; req.EnabledProviders != nil && !providerEnabled {
		return ReanchorNone
	}
	if candidates.MaxedOutServedModel(pin) != "" {
		return ReanchorSkipMaxedOut
	}
	if req.HasImages && (!catalog.AcceptsImages(pin.Model) ||
		req.ImageCount > 0 && !catalog.AcceptsImageCount(pin.Model, req.ImageCount, req.EnabledProviders)) {
		return ReanchorSkipTextOnly
	}
	return ReanchorServe
}

type Handover int

const (
	HandoverNone Handover = iota
	HandoverSkippedTrimmedHistory
	HandoverSummarize
)

type Outcome struct {
	Stay     bool
	PinTier  string
	Handover Handover
}

func Settle(decision planner.Decision, pinFound, prefixBroken bool) Outcome {
	switch {
	case decision.Outcome == planner.OutcomeStay && pinFound:
		return Outcome{Stay: true, PinTier: "postgres_stay_" + decision.Reason}
	case !pinFound:
		return Outcome{}
	case prefixBroken:
		return Outcome{PinTier: "switch_" + decision.Reason, Handover: HandoverSkippedTrimmedHistory}
	default:
		return Outcome{PinTier: "switch_" + decision.Reason, Handover: HandoverSummarize}
	}
}
