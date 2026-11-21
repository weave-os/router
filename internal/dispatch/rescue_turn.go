package dispatch

import (
	"weave-os/router/internal/inference"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/turntype"

	"github.com/google/uuid"
)

type RescueTurn struct {
	Decision                    router.Decision
	Purpose                     inference.Purpose
	Origin                      policy.OverrideSource
	SessionKey                  [sessionpin.SessionKeyLen]byte
	InstallationID              uuid.UUID
	TurnType                    turntype.TurnType
	StickyHit                   bool
	HardPinned                  bool
	AuthoritativePerTurn        bool
	CallerModelPassthrough      bool
	UsageBypass                 bool
	StickyRole                  string
	FreshMetadata               *router.RoutingMetadata
	PriorServedModel            string
	SessionEverSwitched         bool
	HandoverInvoked             bool
	PrefixTrimmed               bool
	EscalateEffort              bool
	TranslationEnabledProviders map[string]struct{}
}

func (r RescueTurn) DispatchPurpose(surface inference.Purpose) inference.Purpose {
	if len(r.Purpose) > 0 {
		return r.Purpose
	}
	return surface
}

func (r RescueTurn) DispatchOrigin(decision router.Decision) policy.OverrideSource {
	if r.Origin != "" && decision.Model == r.Decision.Model && decision.Provider == r.Decision.Provider {
		return r.Origin
	}
	return RoutedOrigin(decision, r.HardPinned, r.StickyHit)
}

func (r RescueTurn) RescueOrigin() policy.OverrideSource {
	if len(r.Purpose) > 0 {
		return policy.OverrideSourceDeployment
	}
	return ""
}

func RoutedOrigin(decision router.Decision, hardPinned, stickyHit bool) policy.OverrideSource {
	switch {
	case decision.UserForcedModel():
		return policy.OverrideSourceRequest
	case hardPinned:
		return policy.OverrideSourceDeployment
	case stickyHit:
		return policy.OverrideSourceSession
	}
	return ""
}
