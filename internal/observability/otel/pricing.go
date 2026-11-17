//go:generate go run ../../../cmd/genprices
package otel

import "weave-os/router/internal/router/catalog"

type Pricing = catalog.Pricing

func AllPricing() map[string]Pricing {
	return catalog.AllPrimaryPricing()
}

func Lookup(model string) Pricing {
	p, _ := catalog.PrimaryPriceFor(model)
	return p
}
