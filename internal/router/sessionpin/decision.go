package sessionpin

import (
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
)

func (p Pin) Decision() router.Decision {
	return router.Decision{
		Provider: p.Provider,
		Model:    p.Model,
		Effort:   p.Effort,
		Reason:   p.Reason,
	}
}

func (p Pin) BandPair() (large, small router.Decision) {
	a := p.Decision()
	b := router.Decision{Provider: p.PairedProvider, Model: p.PairedModel, Reason: p.Reason}
	ta, tb := catalog.TierFor(a.Model), catalog.TierFor(b.Model)
	if ta != tb {
		if ta > tb {
			return a, b
		}
		return b, a
	}
	if primaryInputPrice(a.Model) >= primaryInputPrice(b.Model) {
		return a, b
	}
	return b, a
}

func primaryInputPrice(model string) float64 {
	pricing, ok := catalog.PrimaryPriceFor(model)
	if !ok {
		return 0
	}
	return pricing.InputUSDPer1M
}
