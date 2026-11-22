package candidates

import (
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/bandswap"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/sessionpin"
)

type BandSwapEligibility struct {
	Available        map[string]struct{}
	Excluded         map[string]struct{}
	EnabledProviders map[string]struct{}
	HasImages        bool
	ImageCount       int
}

func BandSwapTarget(pin sessionpin.Pin, band string) router.Decision {
	large, small := pin.OrderedBandPair()
	if band == bandswap.Small {
		return small
	}
	return large
}

func (e BandSwapEligibility) Admits(served router.Decision) bool {
	if _, available := e.Available[served.Model]; !available {
		return false
	}
	if e.HasImages && (!catalog.AcceptsImages(served.Model) || e.ImageCount > 0 && !catalog.AcceptsImageCount(served.Model, e.ImageCount, e.EnabledProviders)) {
		return false
	}
	if _, excluded := e.Excluded[served.Model]; excluded {
		return false
	}
	if e.EnabledProviders != nil {
		if _, ok := e.EnabledProviders[served.Provider]; !ok {
			return false
		}
	}
	return true
}
